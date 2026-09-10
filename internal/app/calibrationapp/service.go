// Package calibrationapp is the calibration use-case: configuring a
// CalibrateEngine execution's search, creating/tracking the jobs that run
// it, and driving the search one step at a time (AdvanceOne, task 79) over
// the one-step runner (step.go, task 78).
package calibrationapp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/capacityprofile"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

// ErrExecutionNotCalibration means the named execution is not Kind
// CalibrateEngine -- only a calibration execution can carry a search Spec
// or have a calibration job triggered against it.
var ErrExecutionNotCalibration = errors.New("calibrationapp: execution is not a CalibrateEngine execution")

// ErrEngineRequired means Create was given no engine. A capacity profile is
// keyed by engine and is meaningless without it -- a JMeter pod and a k6 pod
// of the same size have different capacities -- and the profile/fan-out API
// requires the engine to look one up, so a calibration that defers its engine
// would produce a profile nothing could ever query.
var ErrEngineRequired = errors.New("calibrationapp: a calibration execution must name an engine")

// ErrSourceScenarioNotBound means the source execution named by Create runs
// no load-profile entry for the requested scenario -- there is nothing to
// copy the search's starting configuration from. Before Create bound its
// scenario itself, every UI-created calibration was born a dead shell that
// failed its first Trigger with run.ErrNoScenarios; this error is the loud
// refusal that keeps that shell from ever existing.
var ErrSourceScenarioNotBound = errors.New("calibrationapp: source execution does not run the scenario")

// ErrNotConfiguredForAdvance means AdvanceOne was called before a Runner and
// a ScenarioFingerprinter were wired via WithRunner/WithFingerprint -- both
// are required to drive a step and, on terminal, record what it found,
// unlike Create/Trigger/Get/List, which need only Repo.
var ErrNotConfiguredForAdvance = errors.New("calibrationapp: AdvanceOne requires WithRunner and WithFingerprint")

// ErrFingerprintNotConfigured means FanOut was called before a
// ScenarioFingerprinter was wired via WithFingerprint -- required to detect
// scenario-content staleness against a stored profile.
var ErrFingerprintNotConfigured = errors.New("calibrationapp: FanOut requires WithFingerprint")

// Repo is the persistence calibrationapp needs: the calibration job ledger,
// enough of an execution to create and identify one, its configured
// Taurus criteria (execution_criteria, Phase 6's mechanism -- the
// target-health criterion is reused from it, not duplicated), its recorded
// search bounds, its load profile (to identify the scenario a terminal
// job's CapacityProfile is keyed by), and the capacity profile ledger.
type Repo interface {
	ports.CalibrationJobRepository
	ports.CapacityProfileRepository
	// GetProject resolves the project's tenant for tenant stamping, the
	// same Phase 20 rule executionapp/scenarioapp already follow: an
	// execution's tenant always equals its project's.
	GetProject(ctx context.Context, id int64) (project.Project, error)
	CreateExecution(ctx context.Context, c execution.Execution) (int64, error)
	GetExecution(ctx context.Context, id int64) (execution.Execution, error)
	// StoreExecutionConfig binds an execution's scenario (its load
	// profile) and its configured Taurus criteria together, in one
	// transaction -- the same atomic write an ordinary config upload
	// makes, reused by Create so a calibration is born with both halves of
	// its configuration or not at all.
	StoreExecutionConfig(ctx context.Context, executionID int64, csvSplit bool, entries []loadprofile.Entry, criteria []string) error
	CriteriaFor(ctx context.Context, executionID int64) ([]string, error)
	SetCalibrationBounds(ctx context.Context, executionID int64, bounds ports.CalibrationBounds) error
	CalibrationBoundsFor(ctx context.Context, executionID int64) (ports.CalibrationBounds, error)
	LoadProfileFor(ctx context.Context, executionID int64) ([]loadprofile.Entry, error)
}

// Runner drives one calibration step's real run. *StepRunner satisfies this
// directly; AdvanceOne depends on the interface so a test can substitute a
// stub without wiring a full StepRunner+Lifecycle+ReportStore.
type Runner interface {
	RunStep(ctx context.Context, executionID int64, requestedQPS float64, holdSeconds int, latencyHintSec float64) (report.Report, error)
}

// ScenarioFingerprinter computes a scenario's content fingerprint --
// *scenarioapp.Service satisfies this directly. A terminal job's
// CapacityProfile is stamped with the fingerprint at calibration time, so
// FanOut can later detect scenario-content staleness.
type ScenarioFingerprinter interface {
	ScenarioFingerprint(ctx context.Context, scenarioID int64) (string, error)
}

// Service implements the calibration use-cases.
type Service struct {
	repo        Repo
	runner      Runner
	fingerprint ScenarioFingerprinter
}

// NewService wires the calibration service. Create/SpecFor/Trigger/Get/List
// need only repo; AdvanceOne additionally needs WithRunner and
// WithFingerprint.
func NewService(repo Repo) *Service {
	return &Service{repo: repo}
}

// WithRunner attaches the step runner AdvanceOne drives. Returns the
// receiver for chaining.
func (s *Service) WithRunner(r Runner) *Service {
	if r != nil {
		s.runner = r
	}
	return s
}

// WithFingerprint attaches the scenario fingerprinter AdvanceOne stamps a
// terminal job's CapacityProfile with. Returns the receiver for chaining.
func (s *Service) WithFingerprint(f ScenarioFingerprinter) *Service {
	if f != nil {
		s.fingerprint = f
	}
	return s
}

// Create validates spec and creates a new CalibrateEngine execution under
// projectID configured to run it: the execution itself (pinned to spec's
// pod size), its scenario binding, its target-health criterion, and the
// remaining search bounds. Returns the created execution's id.
//
// The scenario is bound at creation, never afterward: sourceExecutionID
// must already run scenarioID (an ordinary config upload), and Create
// copies that entry into the new execution as its single pinned-size pod
// -- the one entry the step runner (task 78) then rewrites per search
// step. A source that runs no such entry fails the whole Create before
// anything is written, so no half-configured execution is ever left
// behind.
func (s *Service) Create(ctx context.Context, name string, projectID int64, engine taurus.Executor, spec calibration.Spec, sourceExecutionID, scenarioID int64) (int64, error) {
	if strings.TrimSpace(string(engine)) == "" {
		return 0, ErrEngineRequired
	}
	spec = spec.WithDefaults()
	if err := spec.Validate(); err != nil {
		return 0, err
	}

	bound, err := s.boundEntry(ctx, sourceExecutionID, scenarioID, spec)
	if err != nil {
		return 0, err
	}

	exe, err := execution.New(name, projectID)
	if err != nil {
		return 0, err
	}
	exe.Kind = execution.KindCalibrateEngine
	exe.Engine = engine
	exe.CPU, exe.Memory = spec.CPU, spec.Memory
	// Tenant stamping mirrors executionapp.Create: the CalibrateEngine
	// execution this creates is an execution like any other, so its tenant
	// equals its project's (Phase 20 block A). A missing project is
	// tolerated the same way -- the HTTP layer 404s unknown projects first.
	if p, err := s.repo.GetProject(ctx, projectID); err == nil {
		exe.TenantID = p.TenantID
	} else if !errors.Is(err, ports.ErrNotFound) {
		return 0, err
	}
	if err := exe.Validate(); err != nil {
		return 0, err
	}

	executionID, err := s.repo.CreateExecution(ctx, exe)
	if err != nil {
		return 0, err
	}
	// Scenario and criterion land together, in one transaction -- the same
	// atomicity an ordinary config upload gets -- so a calibration can
	// never exist with a criterion but no scenario to run (the dead shell
	// this binding exists to close). CSVSplit is always false: a
	// single-engine search never splits data across engines.
	if err := s.repo.StoreExecutionConfig(ctx, executionID, false, []loadprofile.Entry{bound}, []string{spec.Criterion}); err != nil {
		return 0, err
	}
	bounds := ports.CalibrationBounds{SeedQPS: spec.SeedQPS, MaxQPS: spec.MaxQPS, MaxSteps: spec.MaxSteps, HoldSeconds: spec.HoldSeconds}
	if err := s.repo.SetCalibrationBounds(ctx, executionID, bounds); err != nil {
		return 0, err
	}
	return executionID, nil
}

// boundEntry copies sourceExecutionID's load-profile entry for scenarioID
// into the single-pod entry a fresh calibration starts from: one engine at
// the search's seed rate, holding at least spec.HoldSeconds. Concurrency
// and rampup carry over from the source only as starting values -- every
// search step re-derives them (stepConcurrency, stepRampupSeconds), so only
// the scenario identity and a valid shape matter here.
func (s *Service) boundEntry(ctx context.Context, sourceExecutionID, scenarioID int64, spec calibration.Spec) (loadprofile.Entry, error) {
	entries, err := s.repo.LoadProfileFor(ctx, sourceExecutionID)
	if err != nil {
		return loadprofile.Entry{}, err
	}
	for _, e := range entries {
		if e.ScenarioID != scenarioID {
			continue
		}
		bound := loadprofile.Entry{
			Name:        e.Name,
			ScenarioID:  scenarioID,
			Engines:     1,
			Concurrency: e.Concurrency,
			Rampup:      e.Rampup,
			Duration:    max(spec.HoldSeconds, e.Duration),
			Throughput:  int(math.Ceil(spec.SeedQPS)),
		}
		return bound, bound.Validate()
	}
	return loadprofile.Entry{}, fmt.Errorf("%w: execution %d has no entry for scenario %d", ErrSourceScenarioNotBound, sourceExecutionID, scenarioID)
}

// SpecFor reconstructs the full calibration.Spec configured for
// executionID, from the execution's own pinned pod size, its configured
// Taurus criteria, and its recorded search bounds.
func (s *Service) SpecFor(ctx context.Context, executionID int64) (calibration.Spec, error) {
	exe, err := s.repo.GetExecution(ctx, executionID)
	if err != nil {
		return calibration.Spec{}, err
	}
	criteria, err := s.repo.CriteriaFor(ctx, executionID)
	if err != nil {
		return calibration.Spec{}, err
	}
	bounds, err := s.repo.CalibrationBoundsFor(ctx, executionID)
	if err != nil {
		return calibration.Spec{}, err
	}
	var criterion string
	if len(criteria) > 0 {
		criterion = criteria[0]
	}
	return calibration.Spec{
		Criterion: criterion, CPU: exe.CPU, Memory: exe.Memory,
		SeedQPS: bounds.SeedQPS, MaxQPS: bounds.MaxQPS, MaxSteps: bounds.MaxSteps, HoldSeconds: bounds.HoldSeconds,
	}, nil
}

// Trigger creates a fresh Pending job for executionID's calibration search.
// executionID must be a CalibrateEngine execution with a valid,
// already-configured Spec (Create's own job) -- checked here, before any
// job exists, so a misconfigured calibration is rejected loudly rather than
// starting a search with an undefined target-health criterion or pod size.
// More than one job may be triggered over an execution's life, the same way
// an execution may have more than one run.
func (s *Service) Trigger(ctx context.Context, executionID int64) (int64, error) {
	exe, err := s.repo.GetExecution(ctx, executionID)
	if err != nil {
		return 0, err
	}
	if exe.Kind != execution.KindCalibrateEngine {
		return 0, fmt.Errorf("%w: execution %d is %q", ErrExecutionNotCalibration, executionID, exe.Kind)
	}
	spec, err := s.SpecFor(ctx, executionID)
	if err != nil {
		return 0, err
	}
	if err := spec.Validate(); err != nil {
		return 0, err
	}
	return s.repo.CreateCalibrationJob(ctx, executionID)
}

// Job is a calibration job's full state for a caller: the persisted
// decision-state plus its step-by-step history.
type Job struct {
	ports.CalibrationJob
	Steps []calibration.Step
}

// Get returns jobID's full state, including its step history.
func (s *Service) Get(ctx context.Context, jobID int64) (Job, error) {
	job, err := s.repo.GetCalibrationJob(ctx, jobID)
	if err != nil {
		return Job{}, err
	}
	steps, err := s.repo.StepsFor(ctx, jobID)
	if err != nil {
		return Job{}, err
	}
	return Job{CalibrationJob: job, Steps: steps}, nil
}

// ListByExecution returns every job ever run for executionID, most recent
// first, without their step history -- a caller wanting one job's steps
// calls Get.
func (s *Service) ListByExecution(ctx context.Context, executionID int64) ([]ports.CalibrationJob, error) {
	return s.repo.ListCalibrationJobsByExecution(ctx, executionID)
}

// leaseFor is how long a claimed job holds its lease before another
// controller replica may reclaim it -- long enough to cover a step's real
// run (deploy, trigger, hold, stop), which normal load-test steps' hold
// duration bounds; a wedged controller's job self-heals by expiring back to
// claimable rather than staying stuck forever.
const leaseFor = 30 * time.Minute

// AdvanceOne claims one due calibration job, runs its next step, classifies
// the settled report, feeds the search's own decision function
// (calibration.Next), persists the result, and -- once the search reaches a
// terminal state -- writes the resulting CapacityProfile.
//
// found is false when no job is currently due -- an ordinary outcome of a
// controller tick against an empty queue, not an error. now is the claim
// lease's clock and, on a terminal job, the CapacityProfile's CalibratedAt.
func (s *Service) AdvanceOne(ctx context.Context, now time.Time) (found bool, err error) {
	if s.runner == nil || s.fingerprint == nil {
		return false, ErrNotConfiguredForAdvance
	}

	persisted, found, err := s.repo.ClaimNextStep(ctx, now, leaseFor)
	if err != nil || !found {
		return found, err
	}

	spec, err := s.SpecFor(ctx, persisted.ExecutionID)
	if err != nil {
		return true, s.fail(ctx, persisted.ID, err)
	}

	job, requestedQPS := startOrResume(spec, persisted)

	// The first attempt has no measured response time to size threads from,
	// so it uses the generous default (see stepConcurrency).
	step, latencySec, err := s.runClassifiedStep(ctx, persisted.ExecutionID, requestedQPS, spec, 0)
	if err != nil {
		return true, s.fail(ctx, persisted.ID, err)
	}
	// A single anomalous engine-short is retried once, at the same requested
	// rate, before being accepted as the ceiling -- only the retry's own
	// outcome is fed to the search; the discarded first attempt leaves no
	// trace in the step history. The retry is also where thread sizing is
	// corrected: it uses the first attempt's measured response time to size
	// virtual users by Little's Law, so an engine-short caused merely by
	// over-provisioned, poorly-paced threads (not real saturation) resolves
	// to a clean step instead of derailing the search downward.
	if step.Classification == calibration.ClassificationEngineSaturated {
		step, _, err = s.runClassifiedStep(ctx, persisted.ExecutionID, requestedQPS, spec, latencySec)
		if err != nil {
			return true, s.fail(ctx, persisted.ID, err)
		}
	}

	updatedJob, action := calibration.Next(job, step)
	updated := applyDomainJob(persisted, updatedJob, action)
	if err := s.repo.RecordStep(ctx, persisted.ID, step, updated); err != nil {
		return true, err
	}

	if updatedJob.Phase != calibration.PhaseDone {
		return true, nil
	}
	return true, s.writeProfile(ctx, persisted, updatedJob, now)
}

// fail marks jobID PhaseFailed with runErr's message and returns runErr --
// an operational failure (the step's run itself errored, never even
// producing a classification), distinct from any search outcome Next can
// reach. Best effort: a customer-visible run failure must not be hidden by
// a second failure recording it, so runErr is always what AdvanceOne
// returns, even if MarkFailed itself errors (in which case the job's lease
// simply expires and it is reclaimed).
func (s *Service) fail(ctx context.Context, jobID int64, runErr error) error {
	_ = s.repo.MarkFailed(ctx, jobID, runErr.Error())
	return runErr
}

// runClassifiedStep runs one real step at requestedQPS and classifies its
// settled report: engine-saturation (ShortOfRequest or EngineImpaired) is
// checked before the target-health criterion, since a report's overall
// ErrorRate -- unlike TargetErrorRate -- counts the engine's own failures
// too, and an engine-impaired run must never be misread as target distress.
//
// latencyHintSec sizes the run's virtual users (see RunStep). It also returns
// the response time this run measured, so a caller re-running the step can
// size the retry's threads from real data rather than the generous default.
func (s *Service) runClassifiedStep(ctx context.Context, executionID int64, requestedQPS float64, spec calibration.Spec, latencyHintSec float64) (calibration.Step, float64, error) {
	rpt, err := s.runner.RunStep(ctx, executionID, requestedQPS, spec.HoldSeconds, latencyHintSec)
	if err != nil {
		return calibration.Step{}, 0, err
	}
	class := calibration.ClassificationClean
	switch {
	case rpt.ShortOfRequest() || rpt.EngineImpaired():
		class = calibration.ClassificationEngineSaturated
	case len(rpt.EvaluateCriteria([]string{spec.Criterion})) > 0:
		class = calibration.ClassificationTargetSaturated
	}
	step := calibration.Step{RequestedQPS: requestedQPS, AchievedQPS: rpt.Achieved.Throughput, Classification: class}
	return step, observedLatencySeconds(rpt), nil
}

// observedLatencySeconds is the response time a step's report is sized from --
// p95, so a retry provisions for the slow tail rather than the median and is
// never left short of threads. Zero when the run measured no latency at all
// (an empty run), which sends the retry back to the generous default.
func observedLatencySeconds(rpt report.Report) float64 {
	return rpt.Latency[95]
}

// startOrResume returns the domain Job and this step's requested QPS for
// persisted: a PhasePending job (never yet stepped) begins fresh via
// calibration.Start; any other non-terminal job resumes from its own
// persisted decision-state and NextRequestedQPS (ClaimNextStep never
// returns a Done or Failed job).
func startOrResume(spec calibration.Spec, persisted ports.CalibrationJob) (calibration.Job, float64) {
	if persisted.Phase == calibration.PhasePending {
		job, action := calibration.Start(spec)
		return job, action.NextRequestedQPS
	}
	return calibration.Job{
		Spec:               spec,
		Phase:              persisted.Phase,
		StepCount:          persisted.StepCount,
		BracketLoRequested: persisted.BracketLoRequested,
		BracketLoAchieved:  persisted.BracketLoAchieved,
		BracketHiRequested: persisted.BracketHiRequested,
		Result:             persisted.Result,
	}, persisted.NextRequestedQPS
}

// applyDomainJob folds job's updated decision-state and action's next QPS
// back into persisted's own identity/bookkeeping fields, ready for
// RecordStep.
func applyDomainJob(persisted ports.CalibrationJob, job calibration.Job, action calibration.Action) ports.CalibrationJob {
	persisted.Phase = job.Phase
	persisted.StepCount = job.StepCount
	persisted.BracketLoRequested = job.BracketLoRequested
	persisted.BracketLoAchieved = job.BracketLoAchieved
	persisted.BracketHiRequested = job.BracketHiRequested
	persisted.Result = job.Result
	persisted.NextRequestedQPS = action.NextRequestedQPS
	return persisted
}

// writeProfile records a terminal job's outcome as executionID's
// CapacityProfile, keyed by the scenario its (only) load profile entry
// names, the execution's pinned engine/pod size, and the scenario's content
// fingerprint at this moment -- true for every terminal SaturatedBy, not
// only SaturatedByEngine: a target-limited or inconclusive result is just as
// much a finding FanOut must be able to read back and explain, not silently
// dropped.
func (s *Service) writeProfile(ctx context.Context, job ports.CalibrationJob, done calibration.Job, now time.Time) error {
	exe, err := s.repo.GetExecution(ctx, job.ExecutionID)
	if err != nil {
		return err
	}
	entries, err := s.repo.LoadProfileFor(ctx, job.ExecutionID)
	if err != nil {
		return err
	}
	if len(entries) != 1 {
		return fmt.Errorf("%w: execution %d has %d scenarios, want exactly 1", ErrScenarioNotConfigured, job.ExecutionID, len(entries))
	}
	fingerprint, err := s.fingerprint.ScenarioFingerprint(ctx, entries[0].ScenarioID)
	if err != nil {
		return err
	}
	profile := capacityprofile.CapacityProfile{
		Key: capacityprofile.Key{
			ScenarioID: entries[0].ScenarioID,
			Engine:     exe.Engine,
			CPU:        exe.CPU,
			Memory:     exe.Memory,
		},
		PerPodQPS:           done.Result.PerPodQPS,
		SaturatedBy:         done.Result.SaturatedBy,
		ScenarioFingerprint: fingerprint,
		CalibratedAt:        now,
		JobID:               job.ID,
	}
	return s.repo.UpsertCapacityProfile(ctx, profile)
}

// ProfileFor returns the stored CapacityProfile for key, or ports.ErrNotFound
// if none has ever been calibrated for it.
func (s *Service) ProfileFor(ctx context.Context, key capacityprofile.Key) (capacityprofile.CapacityProfile, error) {
	return s.repo.GetCapacityProfile(ctx, key)
}

// FanOut answers "how many engines does key's scenario need for targetQPS",
// or -- via Result.Status -- why it cannot: no profile was ever calibrated,
// the profile is stale against the scenario's current content, or the
// profile found the target (not the engine) to be the limit.
//
// Unlike AdvanceOne, a missing profile is not an error here: it is FanOut's
// own StatusNoProfile answer, exactly the caller-facing distinction the
// domain function exists to make.
func (s *Service) FanOut(ctx context.Context, key capacityprofile.Key, targetQPS float64) (capacityprofile.Result, error) {
	if s.fingerprint == nil {
		return capacityprofile.Result{}, ErrFingerprintNotConfigured
	}
	profile, err := s.repo.GetCapacityProfile(ctx, key)
	if errors.Is(err, ports.ErrNotFound) {
		return capacityprofile.FanOut(nil, targetQPS, ""), nil
	}
	if err != nil {
		return capacityprofile.Result{}, err
	}
	fingerprint, err := s.fingerprint.ScenarioFingerprint(ctx, key.ScenarioID)
	if err != nil {
		return capacityprofile.Result{}, err
	}
	return capacityprofile.FanOut(&profile, targetQPS, fingerprint), nil
}
