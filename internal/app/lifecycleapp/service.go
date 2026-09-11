// Package lifecycleapp holds the test-lifecycle use-cases: deploy engines,
// trigger a run, stop it, purge the engines, and report status/detail. It
// orchestrates the domain (engine config building, run state machine) over the
// Scheduler, Executor, ObjectStore, and repository ports; it performs no I/O
// of its own.
package lifecycleapp

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/heridotlife/honryu/internal/domain/compile"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/reservation"
	"github.com/heridotlife/honryu/internal/domain/run"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/shard"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/domain/telemetry"
	"github.com/heridotlife/honryu/internal/ports"
)

// ErrNoTestFile is returned when a scenario to be triggered has no JMX test file.
var ErrNoTestFile = errors.New("lifecycle: scenario has no test file")

// Repo is the persistence the lifecycle use-cases need: read access to the
// execution, its execution scenarios and files, plus run/running-scenario state.
type Repo interface {
	GetProject(ctx context.Context, id int64) (project.Project, error)
	GetExecution(ctx context.Context, id int64) (execution.Execution, error)
	LoadProfileFor(ctx context.Context, executionID int64) ([]loadprofile.Entry, error)
	GetScenario(ctx context.Context, id int64) (scenario.Scenario, error)
	ScenarioFilesFor(ctx context.Context, scenarioID int64) (ports.ScenarioFiles, error)
	// GetScenarioRequests returns a portable scenario's declarative workload.
	// ErrNotFound means nothing has been uploaded yet.
	GetScenarioRequests(ctx context.Context, scenarioID int64) ([]byte, error)
	ExecutionFilesFor(ctx context.Context, executionID int64) ([]string, error)
	// CriteriaFor returns the execution's configured Taurus pass/fail
	// criteria, compiled into every shard's passfail module.
	CriteriaFor(ctx context.Context, executionID int64) ([]string, error)
	// SetPendingCorrelationID parks the trace id a Deploy minted on the
	// execution, for the next Trigger to stamp onto the run it creates.
	SetPendingCorrelationID(ctx context.Context, executionID int64, correlationID string) error
	// PendingCorrelationID returns the id the latest Deploy minted, which
	// Trigger stamps onto the run it starts.
	PendingCorrelationID(ctx context.Context, executionID int64) (string, error)
	// TouchActivity restamps the execution's engine idle clock: deploy, run
	// start, and run teardown below each call it, and metricsapp calls it on
	// natural completion -- every event that proves the engines are in use
	// resets the TTL the idle reaper measures.
	TouchActivity(ctx context.Context, executionID int64) error
	// GetReport returns a run's stored report, or ports.ErrNotFound when the
	// run never got one. The abandoned-run sweep uses it to tell a wedged run
	// (report stored, marker left open: heal the marker only) from an
	// abandoned one (no report can ever land: write the synthetic one).
	GetReport(ctx context.Context, runID int64) (report.Report, error)
	ports.OrphanRepository
	ports.RunRepository
}

// Metrics is the metric use-case's hook into the lifecycle. With measurements
// pushed, there is no collection to start or stop -- pods send while they run
// and stop when they are gone -- so what remains is dropping a purged
// execution's series, and finalising a run's report when Honryu itself ends it
// rather than letting it finish on its own.
//
// The metricsapp service implements it; a no-op default is used when none is
// wired (e.g. in tests that don't exercise metrics).
type Metrics interface {
	Purge(executionID int64)
	// Finalize writes the report for a run Honryu is deliberately ending.
	// Idempotent: a run already finalised by its own natural completion is left
	// untouched.
	Finalize(ctx context.Context, executionID, runID int64) error
	// FinalizeOrphaned writes the report for a stranded run -- one still open
	// whose engines already finished (their Finals arrived orphaned). The
	// outcome is derived from the orphaned shards' own exit codes.
	FinalizeOrphaned(ctx context.Context, executionID, runID int64, orphans []ports.OrphanCompletion) error
}

type noopMetrics struct{}

func (noopMetrics) Purge(int64)                                  {}
func (noopMetrics) Finalize(context.Context, int64, int64) error { return nil }
func (noopMetrics) FinalizeOrphaned(context.Context, int64, int64, []ports.OrphanCompletion) error {
	return nil
}

// Usage records the usage of a run: a launch opened on trigger and closed on
// teardown. The usageapp service implements it; a no-op default is used when
// none is wired.
type Usage interface {
	RecordStart(ctx context.Context, executionID int64, owner string, engines, vu int) error
	RecordFinish(ctx context.Context, executionID int64, vu int) error
}

type noopUsage struct{}

func (noopUsage) RecordStart(context.Context, int64, string, int, int) error { return nil }
func (noopUsage) RecordFinish(context.Context, int64, int) error             { return nil }

// Quota is the admission-control hook into Trigger: the reservation ledger
// that turns a tenant's engine quota into a guarantee rather than a
// best-effort check. Only consulted when an execution declares a tenant
// (TenantID != nil) -- multi-tenancy is opt-in, and an execution with no
// tenant has no ceiling to check against.
//
// The quotaapp service implements it; a no-op default is used when none is
// wired (e.g. in tests that don't exercise quota), and always admits.
type Quota interface {
	Reserve(ctx context.Context, tenantID int64, cluster string, engineCount int, start, end time.Time, executionID int64) (reservation.Reservation, error)
	Release(ctx context.Context, executionID int64) error
}

type noopQuota struct{}

func (noopQuota) Reserve(context.Context, int64, string, int, time.Time, time.Time, int64) (reservation.Reservation, error) {
	return reservation.Reservation{}, nil
}
func (noopQuota) Release(context.Context, int64) error { return nil }

// Freeze is the campaign-freeze hook into Trigger: while an active campaign
// covers an execution's project and this execution is not that campaign's
// own designated readiness test, Trigger must reject rather than proceed.
// Checked before Quota -- a categorical block needs no reservation-capacity
// read at all -- and before any other Trigger side effect.
//
// The campaignapp service implements it; a no-op default is used when none
// is wired (e.g. in tests that don't exercise campaigns), and never blocks.
type Freeze interface {
	IsFrozen(ctx context.Context, projectID, executionID int64) (blocked bool, campaignName string, err error)
}

// ErrCampaignFrozen is returned when Trigger is blocked by an active
// campaign's freeze. The error names the blocking campaign.
var ErrCampaignFrozen = errors.New("lifecycleapp: blocked by an active campaign's freeze")

// EnginesFinishedError is the typed form Trigger refuses with when the
// deployed engines already ran and finished: it carries the orphaned
// completion count so the HTTP layer can surface it in the details
// envelope (phase 24). Error()'s text is a pinned contract -- byte-
// identical to the fmt.Errorf wrap this type replaced.
type EnginesFinishedError struct {
	Orphaned int
}

// Error keeps the exact text of the wrap this type replaced.
func (e *EnginesFinishedError) Error() string {
	return fmt.Sprintf("%s: %d orphaned shard completion(s)", run.ErrEnginesFinished, e.Orphaned)
}

// Unwrap keeps the domain sentinel reachable through intervening %w
// wraps: errors.Is(err, run.ErrEnginesFinished) still matches.
func (e *EnginesFinishedError) Unwrap() error { return run.ErrEnginesFinished }

type noopFreeze struct{}

func (noopFreeze) IsFrozen(context.Context, int64, int64) (bool, string, error) {
	return false, "", nil
}

// Service implements the lifecycle use-cases.
type Service struct {
	repo  Repo
	sched ports.Scheduler
	store ports.ObjectStore
	image ImageResolver
	// defaultEngine runs an execution that names none.
	defaultEngine taurus.Executor
	metrics       Metrics
	usage         Usage
	quota         Quota
	freeze        Freeze
	// now is the reservation window's clock and the abandoned-run sweep's
	// age threshold, overridable for deterministic tests.
	now func() time.Time
	// traceContext mints the trace identity a deploy's load carries, once per
	// Deploy call. Overridable for deterministic tests, like now.
	traceContext func() (telemetry.TraceContext, error)
}

// ImageResolver returns the container image for an engine. An empty engine
// means the execution expressed no preference and takes the deployment default.
type ImageResolver func(engine taurus.Executor) (string, error)

// WithDefaultEngine sets the engine used when an execution names none.
func (s *Service) WithDefaultEngine(e taurus.Executor) *Service {
	s.defaultEngine = e
	return s
}

// StaticImage is an ImageResolver that always answers with the same image,
// for tests and single-engine deployments.
func StaticImage(image string) ImageResolver {
	return func(taurus.Executor) (string, error) { return image, nil }
}

// NewService wires the lifecycle service. image resolves an execution's engine
// to the container image its pods run.
func NewService(repo Repo, sched ports.Scheduler, store ports.ObjectStore, image ImageResolver) *Service {
	return &Service{
		repo: repo, sched: sched, store: store, image: image, defaultEngine: taurus.ExecutorJMeter,
		metrics: noopMetrics{}, usage: noopUsage{}, quota: noopQuota{}, freeze: noopFreeze{}, now: time.Now,
		traceContext: randomTraceContext,
	}
}

// WithMetrics attaches the metric collector started on trigger and stopped on
// teardown/purge. Returns the receiver for chaining.
func (s *Service) WithMetrics(m Metrics) *Service {
	if m != nil {
		s.metrics = m
	}
	return s
}

// WithUsage attaches the usage recorder. Returns the receiver for chaining.
func (s *Service) WithUsage(u Usage) *Service {
	if u != nil {
		s.usage = u
	}
	return s
}

// WithQuota attaches the quota admission/release hook. Returns the receiver
// for chaining.
func (s *Service) WithQuota(q Quota) *Service {
	if q != nil {
		s.quota = q
	}
	return s
}

// WithFreeze attaches the campaign-freeze check. Returns the receiver for
// chaining.
func (s *Service) WithFreeze(f Freeze) *Service {
	if f != nil {
		s.freeze = f
	}
	return s
}

// WithNow overrides the clock a reservation window is measured from and
// the abandoned-run sweep ages runs by. Returns the receiver for chaining.
func (s *Service) WithNow(now func() time.Time) *Service {
	if now != nil {
		s.now = now
	}
	return s
}

// WithTraceContext overrides the per-deploy trace-context generator, for
// deterministic tests. Returns the receiver for chaining.
func (s *Service) WithTraceContext(gen func() (telemetry.TraceContext, error)) *Service {
	if gen != nil {
		s.traceContext = gen
	}
	return s
}

// randomTraceContext mints a fresh trace identity: a random W3C trace id (16
// bytes) and parent id (8 bytes), never derived from stable identifiers --
// deriving it would make every run of a recurring execution share one id
// forever, and would also pad internal ids into a third-party system.
func randomTraceContext() (telemetry.TraceContext, error) {
	var b [16 + 8]byte
	if _, err := crand.Read(b[:]); err != nil {
		return telemetry.TraceContext{}, fmt.Errorf("lifecycle: mint trace context: %w", err)
	}
	return telemetry.TraceContext{
		TraceID:  hex.EncodeToString(b[:16]),
		ParentID: hex.EncodeToString(b[16:]),
	}, nil
}

// Deploy provisions the engines for every scenario of an execution. It is rejected
// while a run is in progress and is otherwise idempotent.
func (s *Service) Deploy(ctx context.Context, executionID int64) error {
	coll, err := s.repo.GetExecution(ctx, executionID)
	if err != nil {
		return err
	}
	scenarios, err := s.repo.LoadProfileFor(ctx, executionID)
	if err != nil {
		return err
	}
	if len(scenarios) == 0 {
		return run.ErrNoScenarios
	}
	if _, running, err := s.repo.CurrentRun(ctx, executionID); err != nil {
		return err
	} else if err := run.CanDeploy(run.DerivePhase(0, running)); err != nil {
		return err
	}
	// Resolve once, before deploying anything: an execution whose engine this
	// deployment has no image for must fail before half its scenarios are up.
	image, err := s.image(coll.Engine)
	if err != nil {
		return err
	}
	// One fresh trace identity for the whole deploy, minted before any pod
	// exists. Every scenario and shard compiled below carries the same
	// traceparent/baggage pair, so all of a run's traffic is findable in a
	// target's APM under one correlation id -- and no earlier or later deploy
	// shares it. The run id itself does not exist yet (StartRun happens in
	// Trigger, after pods may already be loading), which is why the identity is
	// minted here and threaded forward rather than derived backwards.
	tc, err := s.traceContext()
	if err != nil {
		return err
	}
	// Park the minted id on the execution before any pod exists: the run it
	// belongs to is only created later (StartRun, in Trigger), and Trigger
	// needs this exact id to stamp onto that run. Persisting before the
	// scenario loop means a persistence failure fails the deploy with nothing
	// half-created, rather than leaving pods whose traffic nothing can ever be
	// correlated back to.
	if err := s.repo.SetPendingCorrelationID(ctx, executionID, tc.TraceID); err != nil {
		return err
	}
	// A new deploy is genuinely new engines: whatever orphaned Finals the
	// previous generation left are stale evidence, cleared before any pod of
	// this generation can run, so Trigger's stranded-run guard starts clean.
	if err := s.repo.ClearOrphanCompletions(ctx, executionID); err != nil {
		return err
	}
	// The deploy itself is engine activity: restamp the idle clock before any
	// pod exists, so a reaper sweeping mid-deploy cannot count this execution
	// as idle and tear down engines the deploy is about to (re)create. Like
	// the correlation-id park above, a failure here fails the deploy with
	// nothing half-created rather than leaving engines nothing vouches for.
	if err := s.repo.TouchActivity(ctx, executionID); err != nil {
		return err
	}
	headers := telemetry.Headers(tc, telemetry.Identity{
		TenantID:         coll.TenantID,
		ProjectID:        coll.ProjectID,
		ExecutionID:      executionID,
		RunCorrelationID: tc.TraceID,
	})
	for _, ep := range scenarios {
		shards, err := shard.Plan(ep, ep.Engines)
		if err != nil {
			return err
		}
		specs, files, err := s.compileShards(ctx, coll, ep, shards, engineOf(coll, s.defaultEngine), headers)
		if err != nil {
			return err
		}
		spec := ports.DeploySpec{
			// The execution's cluster (empty = this deployment's default), the
			// load origin the scheduler resolves to a per-cluster client.
			Cluster:     ports.ClusterRef(coll.Cluster),
			ProjectID:   coll.ProjectID,
			ExecutionID: executionID,
			ScenarioID:  ep.ScenarioID,
			Image:       image,
			// Empty (every execution before Phase 7's calibration search
			// pinned them) is the cluster's own default pod size -- only a
			// CalibrateEngine execution ever sets these, since a capacity
			// profile answers "QPS per pod of THIS size".
			CPU:           coll.CPU,
			Memory:        coll.Memory,
			Shards:        specs,
			ScenarioFiles: files,
		}
		if err := s.sched.DeployScenario(ctx, spec); err != nil {
			return err
		}
	}
	return nil
}

// baselineEngineCPU/baselineEngineMemory are the pod size one
// engine-equivalent quota unit represents. An execution with no pinned pod
// size (every execution before Phase 7's calibration search, and every
// ordinary one since) implicitly runs at this size already, so counting one
// unit per declared engine is exactly right for it; only a pinned pod
// (execution.CPU/Memory, set only by a CalibrateEngine execution, task 73)
// can differ from baseline, and its reservation must scale with how much
// bigger it actually is -- a single oversized calibration pod must not be
// allowed to reserve, and so occupy, no more than an ordinary "1 engine"
// would.
const (
	baselineEngineCPU    = "500m"
	baselineEngineMemory = "512Mi"
)

// engineEquivalents converts podCount pods of the given pinned size (cpu and
// memory empty meaning "no pin -- the cluster's own default, already 1
// baseline unit each") into the number of engine-equivalent quota units they
// actually represent: ceil(pod_resources / baseline_engine_size) per pod,
// summed across podCount. sizeRatio's own 1.0 floor already guarantees a pod,
// however small, is never worth less than one whole engine-equivalent.
func engineEquivalents(cpu, memory string, podCount int) (int, error) {
	ratio, err := sizeRatio(cpu, memory)
	if err != nil {
		return 0, err
	}
	return int(math.Ceil(ratio)) * podCount, nil
}

// sizeRatio is the larger of cpu's and memory's ratio to baseline -- whichever
// dimension a pinned pod is proportionally biggest in is what determines how
// many baseline-sized pods worth of capacity it actually occupies. Empty
// leaves that dimension's ratio at the 1.0 floor, unexamined -- so this never
// returns below 1.0.
func sizeRatio(cpu, memory string) (float64, error) {
	ratio := 1.0
	if cpu != "" {
		q, err := resource.ParseQuantity(cpu)
		if err != nil {
			return 0, fmt.Errorf("lifecycle: parse pinned cpu %q: %w", cpu, err)
		}
		baseline := resource.MustParse(baselineEngineCPU)
		if r := float64(q.MilliValue()) / float64(baseline.MilliValue()); r > ratio {
			ratio = r
		}
	}
	if memory != "" {
		q, err := resource.ParseQuantity(memory)
		if err != nil {
			return 0, fmt.Errorf("lifecycle: parse pinned memory %q: %w", memory, err)
		}
		baseline := resource.MustParse(baselineEngineMemory)
		if r := float64(q.Value()) / float64(baseline.Value()); r > ratio {
			ratio = r
		}
	}
	return ratio, nil
}

// Trigger starts a run across all deployed, ready engines. Engines must be
// deployed and reachable; every scenario must have a test file.
func (s *Service) Trigger(ctx context.Context, executionID int64) error {
	coll, err := s.repo.GetExecution(ctx, executionID)
	if err != nil {
		return err
	}
	// Checked first and cheaply -- needs only coll.ProjectID and executionID,
	// no load profile or scheduler round trip -- so a frozen Trigger is
	// rejected before any other work, exactly like an over-quota one is.
	if blocked, campaignName, err := s.freeze.IsFrozen(ctx, coll.ProjectID, executionID); err != nil {
		return err
	} else if blocked {
		return fmt.Errorf("%w: %s", ErrCampaignFrozen, campaignName)
	}
	scenarios, err := s.repo.LoadProfileFor(ctx, executionID)
	if err != nil {
		return err
	}
	ec := loadprofile.Profile{ExecutionID: executionID, Tests: scenarios, CSVSplit: coll.CSVSplit}

	if err := s.ensureTestFiles(ctx, scenarios); err != nil {
		return err
	}

	_, running, err := s.repo.CurrentRun(ctx, executionID)
	if err != nil {
		return err
	}
	status, err := s.sched.ExecutionStatus(ctx, ports.ClusterRef(coll.Cluster), executionID, planRefs(scenarios))
	if err != nil {
		return err
	}
	phase := run.DerivePhase(status.PoolSize, running)
	if err := run.CanTrigger(phase, ec, status.PoolSize); err != nil {
		return err
	}

	// Engines that already ran and finished cannot be triggered again: a
	// pod's bzt never reruns (task 23c's exit-code-then-sleep fix), so a run
	// opened against them is a corpse -- nothing will ever send its Final
	// (task 121 hit this live: StartRun minutes after deploy, engine already
	// done, run stranded open with no report). The orphaned Finals those
	// engines pushed are the only reliable signal; pods stay Ready forever,
	// so readiness alone cannot tell finished from starting.
	if orphans, err := s.repo.OrphanCompletions(ctx, executionID); err != nil {
		return err
	} else if len(orphans) > 0 {
		return &EnginesFinishedError{Orphaned: len(orphans)}
	}

	// Quota is opt-in with the tenant it scopes to: an execution that never
	// named one has no ceiling to check against, so every existing deployment
	// (none of which set up tenant context) triggers exactly as before.
	if coll.TenantID != nil {
		engineUnits, err := engineEquivalents(coll.CPU, coll.Memory, ec.TotalEngines())
		if err != nil {
			return err
		}
		start := s.now()
		end := start.Add(time.Duration(ec.LongestDurationSeconds()) * time.Second)
		if _, err := s.quota.Reserve(ctx, *coll.TenantID, coll.Cluster, engineUnits, start, end, executionID); err != nil {
			return err
		}
	}

	// StartRun's id identified the run to the engine agents; with the agent
	// protocol gone, the run is identified in metrics by the ingest path (task 21).
	// The correlation id is the one the deploy that created these pods minted:
	// parked on the execution at Deploy time, consumed here, so the run keeps
	// it even after a later deploy overwrites the pending value.
	correlationID, err := s.repo.PendingCorrelationID(ctx, executionID)
	if err != nil {
		return err
	}
	runID, err := s.repo.StartRun(ctx, executionID, correlationID)
	if err != nil {
		return err
	}
	// The run's start is engine activity -- the idle clock must measure from
	// the run, not the deploy that preceded it, or a long-idle-then-triggered
	// execution could read as already-expired mid-run. Best effort: the run
	// is open and the reaper's open-run guard protects these engines even if
	// the stamp is lost.
	_ = s.repo.TouchActivity(ctx, executionID)
	// Snapshot each scenario's currently-deployed config under this run's own
	// id, so what a re-deploy stages afterward can never change what this run
	// is shown to have used. Best effort, matching log capture: a customer must
	// be able to trigger even if the object store snapshot fails.
	s.snapshotConfigs(ctx, runID, scenarios)

	// Under Taurus there is no separate trigger step: a pod runs bzt and starts
	// generating load the moment it starts, so this records that the run is under
	// way rather than instructing engines. Wiring pods to their compiled config
	// is task 23; until then a deployed execution generates no load.
	var triggerErrs []error
	for _, ep := range scenarios {
		if err := s.repo.MarkScenarioRunning(ctx, executionID, ep.ScenarioID); err != nil {
			triggerErrs = append(triggerErrs, err)
		}
	}
	if len(triggerErrs) == len(scenarios) {
		// Every scenario failed: roll the run back so the execution is triggerable
		// again after the operator fixes the problem.
		_ = s.repo.StopRun(ctx, executionID)
	}
	if len(triggerErrs) > 0 {
		return fmt.Errorf("lifecycle: trigger errors: %w", errors.Join(triggerErrs...))
	}
	// Begin streaming metrics from the now-running engines and open a usage
	// launch (best effort: accounting must not fail a successful trigger).
	if proj, perr := s.repo.GetProject(ctx, coll.ProjectID); perr == nil {
		_ = s.usage.RecordStart(ctx, executionID, proj.Owner, ec.TotalEngines(), run.VirtualUsers(ec))
	}
	return nil
}

// Reconcile closes stranded runs: ones still open whose engines already
// finished, so no Final for them can ever arrive (they pushed orphaned Finals
// instead). A run qualifies only when the orphans cover every shard the load
// profile planned -- partial orphans might still be a mid-run pod dying, which
// the run's own progress handles -- and closing it writes an evidence-based
// report via FinalizeOrphaned, never an invented pass. A run with no orphan
// evidence is left alone: it may genuinely still be running.
//
// Idempotent by construction: SaveReport keeps the first report for a run, so
// a second pass over the same stranded run writes nothing new. Runs that
// finished naturally but were never torn down also pass through harmlessly --
// their orphans (if any) produce a report that loses to the real one already
// stored.
func (s *Service) Reconcile(ctx context.Context) error {
	open, err := s.repo.OpenRuns(ctx)
	if err != nil {
		return err
	}
	for _, orun := range open {
		orphans, err := s.repo.OrphanCompletions(ctx, orun.ExecutionID)
		if err != nil {
			return err
		}
		if len(orphans) == 0 {
			continue
		}
		profile, err := s.repo.LoadProfileFor(ctx, orun.ExecutionID)
		if err != nil {
			return err
		}
		if !orphansCoverProfile(orphans, profile) {
			continue
		}
		if err := s.metrics.FinalizeOrphaned(ctx, orun.ExecutionID, orun.RunID, orphans); err != nil {
			return err
		}
		// Close the run the way teardown would: the report is written, so
		// the execution must not read as running (the next Trigger after a
		// redeploy should work, not ErrAlreadyRunning).
		if err := s.repo.StopRun(ctx, orun.ExecutionID); err != nil {
			return err
		}
	}
	return nil
}

// orphansCoverProfile reports whether the orphaned completions name every
// (scenario, shard) the load profile planned: full coverage is the evidence
// that the whole engine pool finished while the run stood open.
func orphansCoverProfile(orphans []ports.OrphanCompletion, profile []loadprofile.Entry) bool {
	seen := make(map[shardKey]struct{}, len(orphans))
	for _, oc := range orphans {
		seen[shardKey{scenarioID: oc.ScenarioID, shard: oc.ShardIndex}] = struct{}{}
	}
	for _, e := range profile {
		for i := 0; i < e.Engines; i++ {
			if _, ok := seen[shardKey{scenarioID: e.ScenarioID, shard: i}]; !ok {
				return false
			}
		}
	}
	return len(profile) > 0
}

// ReconcileAbandoned closes runs whose own completion path cannot: the
// abandoned -- engines died without ever reporting, so the run row stands
// open, no report exists for it, and none ever can (a statefulset lost to a
// node kill or a failed PreStopHook, phase 36) -- and the wedged -- a
// report exists but the marker row was never cleared (a run that finalised
// before phase 43 taught finalize to close its own marker, or one caught
// in the crash window between SaveReport and that close). Until the marker
// goes the execution wedges either way: every Trigger 409s on the open run
// row, and a purge clears the run with no report ever landing.
//
// A run is considered only when older than after -- never touch a run
// younger than the threshold; its engines may be slow to report or a deploy
// mid-flight. Past it, two closes:
//
//   - wedged: a stored report is definitive -- the run is over whatever its
//     pods are doing (they linger forever after bzt exits) -- so the marker
//     is closed and nothing else is done: no refinalise, since the stored
//     verdict is the audit record and the completion was already announced;
//   - abandoned: no stored report, and no live engine pods (pool size 0) --
//     a run whose engines exist may genuinely still be running -- closed
//     the way teardown would, with a synthetic aborted report (audit
//     evidence that the sweep ended the run, never an invented pass) and
//     then StopRun, so the execution can deploy and trigger again.
//
// after must be positive; zero or negative disables the pass.
// Returns the runs it closed so the caller can log every reconciliation
// action. Idempotent by construction: a closed run is no longer open, and
// SaveReport keeps the first report for a run anyway.
func (s *Service) ReconcileAbandoned(ctx context.Context, after time.Duration) ([]ports.OpenRun, error) {
	if after <= 0 {
		return nil, nil
	}
	open, err := s.repo.OpenRuns(ctx)
	if err != nil {
		return nil, err
	}
	var closed []ports.OpenRun
	for _, orun := range open {
		if s.now().Sub(orun.StartedTime) < after {
			continue
		}
		if _, err := s.repo.GetReport(ctx, orun.RunID); err == nil {
			// The healed wedge: the run finalised but its marker was never
			// cleared, so the execution reads running forever. Close the
			// marker exactly as teardown would have -- the report already
			// holds the verdict, so no refinalise, and the completion was
			// announced when the report landed.
			if err := s.repo.StopRun(ctx, orun.ExecutionID); err != nil {
				return nil, err
			}
			closed = append(closed, orun)
			continue
		} else if !errors.Is(err, ports.ErrNotFound) {
			return nil, err
		}
		coll, err := s.repo.GetExecution(ctx, orun.ExecutionID)
		if err != nil {
			// An execution deleted under an open run leaves a run row nothing
			// names any more; skip rather than error on every pass, and leave
			// it -- nothing can wedge behind a missing execution.
			if errors.Is(err, ports.ErrNotFound) {
				continue
			}
			return nil, err
		}
		scenarios, err := s.repo.LoadProfileFor(ctx, orun.ExecutionID)
		if err != nil {
			return nil, err
		}
		status, err := s.sched.ExecutionStatus(ctx, ports.ClusterRef(coll.Cluster), orun.ExecutionID, planRefs(scenarios))
		if err != nil {
			return nil, err
		}
		if status.PoolSize > 0 {
			continue
		}
		if err := s.metrics.Finalize(ctx, orun.ExecutionID, orun.RunID); err != nil {
			return nil, err
		}
		if err := s.repo.StopRun(ctx, orun.ExecutionID); err != nil {
			return nil, err
		}
		closed = append(closed, orun)
	}
	return closed, nil
}

// shardKey identifies one engine pod of one scenario.
type shardKey struct {
	scenarioID int64
	shard      int
}

// Stop halts the run: it stops every engine and clears run state. A run must be
// in progress.
func (s *Service) Stop(ctx context.Context, executionID int64) error {
	_, running, err := s.repo.CurrentRun(ctx, executionID)
	if err != nil {
		return err
	}
	if err := run.CanStop(run.DerivePhase(0, running)); err != nil {
		return err
	}
	return s.teardown(ctx, executionID)
}

// clusterFor returns the ClusterRef an execution's engines live on: its
// configured cluster, empty meaning the deployment default. Status, purge, and
// log operations resolve it so they target the same cluster the execution was
// deployed to.
func (s *Service) clusterFor(ctx context.Context, executionID int64) (ports.ClusterRef, error) {
	coll, err := s.repo.GetExecution(ctx, executionID)
	if err != nil {
		return "", err
	}
	return ports.ClusterRef(coll.Cluster), nil
}

// Purge stops any in-progress run and removes all engines of an execution.
func (s *Service) Purge(ctx context.Context, executionID int64) error {
	cluster, err := s.clusterFor(ctx, executionID)
	if err != nil {
		return err
	}
	runID, running, err := s.repo.CurrentRun(ctx, executionID)
	if err != nil {
		return err
	}
	if running {
		if err := s.teardown(ctx, executionID); err != nil {
			return err
		}
		// After teardown, before the pods that hold them are deleted: this is
		// the last moment engine logs exist anywhere but here.
		s.captureLogs(ctx, executionID, runID)
	} else if last, lerr := s.repo.LastRun(ctx, executionID); lerr == nil {
		// Phase 43 closes the run marker the moment a run completes
		// naturally, so a purge arriving after that reads running=false --
		// but the pods and their logs still exist, and teardown's other
		// duties (clearing scenario markers, releasing usage and quota)
		// were never done: natural completion only finalises the report.
		// Teardown is safe on a closed marker -- its Finalize is guarded on
		// CurrentRun and StopRun is idempotent -- so run it, then capture
		// the last run's logs before the pods that hold them are deleted.
		if err := s.teardown(ctx, executionID); err != nil {
			return err
		}
		s.captureLogs(ctx, executionID, last.RunID)
	}
	if err := s.sched.PurgeExecution(ctx, cluster, executionID); err != nil {
		return err
	}
	s.metrics.Purge(executionID)
	return nil
}

// RunShardKey is the object-store key for one shard's captured run artefact --
// its engine log (ext "log") or the compiled config it ran (ext "yml").
// Exported so the httpapi adapter's read side computes the same key this
// package's write side does, rather than keeping its own copy of the format
// in sync by hand.
//
// Retention is a lifecycle policy on the underlying bucket (the same GCS/Nexus
// adapters already used for scenario and execution artefacts), not something
// this code tracks -- there is no TTL concept on ObjectStore, and adding a
// sweep would duplicate a capability object storage already has.
func RunShardKey(runID, scenarioID int64, shard int, ext string) string {
	return fmt.Sprintf("run/%d/scenario-%d-shard-%d.%s", runID, scenarioID, shard, ext)
}

// runLogKey is the object-store key for one shard's engine log.
func runLogKey(runID, scenarioID int64, shard int) string {
	return RunShardKey(runID, scenarioID, shard, "log")
}

// captureLogs saves each shard's engine output before Purge deletes its pod,
// so a run stays diagnosable after the cluster no longer has it.
//
// Best effort throughout: a customer must be able to purge a broken execution
// even if a log capture fails, and a missing log degrades diagnosability
// rather than the run itself. One shard's failure does not stop the others
// from being tried.
//
// Every shard's PodLog/Upload round trip is independent of every other's, so
// they run concurrently rather than one after another -- an execution with
// several scenarios can have dozens of shards, and Purge is a request a
// customer is waiting on.
func (s *Service) captureLogs(ctx context.Context, executionID, runID int64) {
	cluster, err := s.clusterFor(ctx, executionID)
	if err != nil {
		return
	}
	scenarios, err := s.repo.LoadProfileFor(ctx, executionID)
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	for _, ep := range scenarios {
		for i := 0; i < ep.Engines; i++ {
			wg.Add(1)
			go func(scenarioID int64, shard int) {
				defer wg.Done()
				log, err := s.sched.PodLog(ctx, cluster, executionID, scenarioID, shard)
				if err != nil {
					return
				}
				_ = s.store.Upload(ctx, runLogKey(runID, scenarioID, shard), strings.NewReader(log))
			}(ep.ScenarioID, i)
		}
	}
	wg.Wait()
}

// snapshotConfigs copies each scenario's staged compiled config into a
// run-keyed object, so a later re-deploy changing the staged copy can never
// alter what this run is shown to have used. One shard's failure does not
// stop the others from being tried, and -- as with captureLogs -- every
// shard's Download/Upload round trip is independent, so they run
// concurrently on this Trigger request path a customer is waiting on.
func (s *Service) snapshotConfigs(ctx context.Context, runID int64, scenarios []loadprofile.Entry) {
	var wg sync.WaitGroup
	for _, ep := range scenarios {
		for i := 0; i < ep.Engines; i++ {
			wg.Add(1)
			go func(scenarioID int64, shard int) {
				defer wg.Done()
				raw, err := s.store.Download(ctx, deployedConfigKey(scenarioID, shard))
				if err != nil {
					return
				}
				_ = s.store.Upload(ctx, runConfigKey(runID, scenarioID, shard), bytes.NewReader(raw))
			}(ep.ScenarioID, i)
		}
	}
	wg.Wait()
}

// podScenarioPath is where a scenario's artefacts are mounted in an engine pod.
const podScenarioPath = "/honryu/config/scenario/"

// scenarioKey is the object-store key of one of a scenario's files.
func scenarioKey(scenarioID int64, filename string) string {
	return fmt.Sprintf("scenario/%d/%s", scenarioID, filename)
}

// compileShards turns one scenario's share of the load into a config per pod.
//
// Each shard gets its own config carrying only its fraction of the users, which
// is what makes the pods together produce the profile that was asked for. The
// scenario's own artefacts travel with them, since a native scenario's config
// points at a script the pod must be able to open. headers is the deploy-wide
// trace-context pair, identical on every shard and scenario of one Deploy.
func (s *Service) compileShards(
	ctx context.Context,
	exe execution.Execution,
	entry loadprofile.Entry,
	shards []shard.Shard,
	engine taurus.Executor,
	headers map[string]string,
) ([]ports.ShardSpec, map[string][]byte, error) {
	sc, err := s.repo.GetScenario(ctx, entry.ScenarioID)
	if err != nil {
		return nil, nil, err
	}
	pf, err := s.repo.ScenarioFilesFor(ctx, entry.ScenarioID)
	if err != nil {
		return nil, nil, err
	}
	if sc.Kind == scenario.KindNative && pf.TestFile == "" {
		return nil, nil, ErrNoTestFile
	}

	files := map[string][]byte{}
	for _, name := range append([]string{pf.TestFile}, pf.Data...) {
		if name == "" {
			continue
		}
		content, dlErr := s.store.Download(ctx, scenarioKey(entry.ScenarioID, name))
		if dlErr != nil {
			// Naming the file matters: "object not found" alone leaves an
			// operator guessing which of a scenario's artefacts is missing.
			return nil, nil, fmt.Errorf("lifecycleapp: scenario %d file %q: %w",
				entry.ScenarioID, name, dlErr)
		}
		files[name] = content
	}

	si := compile.ScenarioInput{Scenario: sc}
	switch sc.Kind {
	case scenario.KindNative:
		si.ScriptPath = podScenarioPath + pf.TestFile
	case scenario.KindPortable:
		raw, reqErr := s.repo.GetScenarioRequests(ctx, entry.ScenarioID)
		switch {
		case errors.Is(reqErr, ports.ErrNotFound):
			// Nothing uploaded yet. si.Requests stays nil, and
			// compile.Taurus's own ErrRequestsRequired surfaces below --
			// the same error a portable scenario has always failed to
			// deploy with, not a new, duplicate check.
		case reqErr != nil:
			return nil, nil, fmt.Errorf("lifecycleapp: scenario %d requests: %w", entry.ScenarioID, reqErr)
		default:
			var frag taurus.Scenario
			if err := yaml.Unmarshal(raw, &frag); err != nil {
				// scenarioapp.SetRequests validates before storing, so a
				// stored fragment that fails to parse here would mean the
				// stored bytes were corrupted after the fact, not a bad
				// upload -- worth its own error rather than silently
				// falling through to "no requests".
				return nil, nil, fmt.Errorf("lifecycleapp: scenario %d stored requests: %w", entry.ScenarioID, err)
			}
			si.Requests = frag.Requests
			si.DefaultAddress = frag.DefaultAddress
			// The fragment's own headers, timeout and keepalive ride along
			// to the engine; compileScenario merges the headers beneath the
			// execution's telemetry headers so trace context wins.
			si.Headers = frag.Headers
			si.Timeout = frag.Timeout
			si.KeepAlive = frag.KeepAlive
		}
	}
	for _, name := range pf.Data {
		si.DataPaths = append(si.DataPaths, podScenarioPath+name)
	}

	criteria, err := s.repo.CriteriaFor(ctx, exe.ID)
	if err != nil {
		return nil, nil, err
	}

	specs := make([]ports.ShardSpec, len(shards))
	for i, sh := range shards {
		// Only this shard's slice of the load: the pods together add up to the
		// requested profile.
		shardEntry := entry
		shardEntry.Concurrency = sh.Concurrency
		shardEntry.Throughput = sh.Throughput

		cfg, cErr := compile.Taurus(compile.Input{
			Execution: exe,
			Profile:   loadprofile.Profile{Tests: []loadprofile.Entry{shardEntry}},
			Engine:    engine,
			Scenarios: map[int64]compile.ScenarioInput{entry.ScenarioID: si},
			Headers:   headers,
			Criteria:  criteria,
		})
		if cErr != nil {
			return nil, nil, cErr
		}
		raw, mErr := yaml.Marshal(cfg)
		if mErr != nil {
			return nil, nil, fmt.Errorf("lifecycleapp: encode shard config: %w", mErr)
		}
		specs[i] = ports.ShardSpec{Index: sh.Index, Concurrency: sh.Concurrency, Config: raw}
		// Staged for Trigger to snapshot into a run-keyed copy once a run id
		// exists. Best effort: a customer must be able to deploy even if the
		// object store that makes the config independently retrievable is down --
		// the pod still gets it directly, through the ConfigMap.
		_ = s.store.Upload(ctx, deployedConfigKey(entry.ScenarioID, sh.Index), bytes.NewReader(raw))
	}
	return specs, files, nil
}

// deployedConfigKey is where a scenario's currently-deployed compiled config
// stages, ahead of a run existing to snapshot it into.
func deployedConfigKey(scenarioID int64, shard int) string {
	return fmt.Sprintf("scenario/%d/compiled/shard-%d.yml", scenarioID, shard)
}

// runConfigKey is the object-store key for one shard's compiled config, as the
// run actually used it -- immune to a later re-deploy changing the staged copy.
func runConfigKey(runID, scenarioID int64, shard int) string {
	return RunShardKey(runID, scenarioID, shard, "yml")
}

// engineOf is the execution's engine, or the deployment default when it named
// none.
func engineOf(exe execution.Execution, fallback taurus.Executor) taurus.Executor {
	if exe.Engine != "" {
		return exe.Engine
	}
	return fallback
}

// teardown stops metric execution and engines, and clears run/running-scenario
// state (best effort on the engine stop calls, which may already be gone).
func (s *Service) teardown(ctx context.Context, executionID int64) error {
	// A run being torn down is its own last activity: the engines usually
	// stay deployed (only Purge removes pods), so the idle clock must start
	// at the run's end, not its start -- this is the "stopped" half of the
	// completion activity, the natural-finalize half living in metricsapp.
	// Best effort, like every side duty below: a bookkeeping stamp must not
	// fail a stop.
	_ = s.repo.TouchActivity(ctx, executionID)
	scenarios, err := s.repo.LoadProfileFor(ctx, executionID)
	if err != nil {
		return err
	}
	// Engines are stopped by removing their pods (Purge), not by calling them.
	for _, ep := range scenarios {
		if err := s.repo.ClearScenarioRunning(ctx, executionID, ep.ScenarioID); err != nil {
			return err
		}
	}
	// Close the usage launch (best effort).
	vu := run.VirtualUsers(loadprofile.Profile{Tests: scenarios})
	_ = s.usage.RecordFinish(ctx, executionID, vu)

	// Release any quota reservation immediately rather than waiting for its
	// declared end -- Stop/Purge means the engines are gone now. Best effort,
	// matching RecordFinish: a customer must be able to stop or purge a broken
	// execution even if releasing its reservation fails, and most executions
	// (no tenant, or already released) never had one to begin with.
	_ = s.quota.Release(ctx, executionID)

	// Finalise the report before the run's identity is cleared: Finalize needs
	// the run id, and StopRun is what forgets it. Best effort, matching
	// RecordFinish above -- a customer must be able to stop or purge a broken
	// execution even if writing its final report fails.
	if runID, running, err := s.repo.CurrentRun(ctx, executionID); err == nil && running {
		_ = s.metrics.Finalize(ctx, executionID, runID)
	}
	return s.repo.StopRun(ctx, executionID)
}

// ScenarioStatus is the lifecycle view of one scenario's engines.
type ScenarioStatus struct {
	ScenarioID      int64     `json:"scenario_id"`
	EnginesWanted   int       `json:"engines"`
	EnginesDeployed int       `json:"engines_deployed"`
	Reachable       bool      `json:"engines_reachable"`
	InProgress      bool      `json:"in_progress"`
	StartedTime     time.Time `json:"started_time,omitempty"`
}

// Status is the lifecycle view of an execution.
type Status struct {
	Phase     run.Phase        `json:"phase"`
	PoolSize  int              `json:"pool_size"`
	Scenarios []ScenarioStatus `json:"status"`
}

// RunExecutionID resolves which execution a run belongs to, via its
// history row. It is how the HTTP layer scopes a run-keyed report route
// (run report, shard log/config) to the owning execution's tenant -- the
// run row itself carries no tenant. Returns ports.ErrNotFound for an
// unknown run.
func (s *Service) RunExecutionID(ctx context.Context, runID int64) (int64, error) {
	rec, err := s.repo.RunHistory(ctx, runID)
	if err != nil {
		return 0, err
	}
	return rec.ExecutionID, nil
}

// Status reports the deployment/run status of an execution.
func (s *Service) Status(ctx context.Context, executionID int64) (Status, error) {
	cluster, err := s.clusterFor(ctx, executionID)
	if err != nil {
		return Status{}, err
	}
	scenarios, err := s.repo.LoadProfileFor(ctx, executionID)
	if err != nil {
		return Status{}, err
	}
	sched, err := s.sched.ExecutionStatus(ctx, cluster, executionID, planRefs(scenarios))
	if err != nil {
		return Status{}, err
	}
	_, running, err := s.repo.CurrentRun(ctx, executionID)
	if err != nil {
		return Status{}, err
	}
	runningByScenario, err := s.runningByScenario(ctx, executionID)
	if err != nil {
		return Status{}, err
	}

	out := Status{Phase: run.DerivePhase(sched.PoolSize, running), PoolSize: sched.PoolSize}
	for _, pr := range sched.Scenarios {
		ps := ScenarioStatus{
			ScenarioID:      pr.ScenarioID,
			EnginesWanted:   pr.EnginesWanted,
			EnginesDeployed: pr.EnginesDeployed,
			Reachable:       pr.Reachable,
		}
		if started, ok := runningByScenario[pr.ScenarioID]; ok {
			ps.InProgress = true
			ps.StartedTime = started
		}
		out.Scenarios = append(out.Scenarios, ps)
	}
	return out, nil
}

// EnginesDetail reports the engine pods and ingress of an execution.
func (s *Service) EnginesDetail(ctx context.Context, projectID, executionID int64) (ports.ExecutionDetail, error) {
	cluster, err := s.clusterFor(ctx, executionID)
	if err != nil {
		return ports.ExecutionDetail{}, err
	}
	return s.sched.EngineDetail(ctx, cluster, projectID, executionID)
}

// PodLog returns the logs of a scenario's engine pod.
func (s *Service) PodLog(ctx context.Context, executionID, scenarioID int64) (string, error) {
	cluster, err := s.clusterFor(ctx, executionID)
	if err != nil {
		return "", err
	}
	return s.sched.PodLog(ctx, cluster, executionID, scenarioID, 0)
}

// Resume returns the scenarios still marked running in this deployment context, so
// a restarted controller can re-establish tracking.
func (s *Service) Resume(ctx context.Context) ([]ports.RunningScenario, error) {
	return s.repo.RunningScenarios(ctx)
}

// --- helpers ----------------------------------------------------------------

// ensureTestFiles requires an uploaded script for every native scenario in
// the profile -- a portable scenario runs from its declarative requests
// instead (see compileShards), and has no test file to check for.
func (s *Service) ensureTestFiles(ctx context.Context, scenarios []loadprofile.Entry) error {
	for _, ep := range scenarios {
		sc, err := s.repo.GetScenario(ctx, ep.ScenarioID)
		if err != nil {
			return err
		}
		if sc.Kind != scenario.KindNative {
			continue
		}
		pf, err := s.repo.ScenarioFilesFor(ctx, ep.ScenarioID)
		if err != nil {
			return err
		}
		if pf.TestFile == "" {
			return fmt.Errorf("%w: scenario %d", ErrNoTestFile, ep.ScenarioID)
		}
	}
	return nil
}

func (s *Service) runningByScenario(ctx context.Context, executionID int64) (map[int64]time.Time, error) {
	rps, err := s.repo.RunningScenariosByExecution(ctx, executionID)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]time.Time, len(rps))
	for _, rp := range rps {
		out[rp.ScenarioID] = rp.StartedTime
	}
	return out, nil
}

func planRefs(scenarios []loadprofile.Entry) []ports.ScenarioRef {
	refs := make([]ports.ScenarioRef, 0, len(scenarios))
	for _, ep := range scenarios {
		refs = append(refs, ports.ScenarioRef{ScenarioID: ep.ScenarioID, Shards: ep.Engines})
	}
	return refs
}
