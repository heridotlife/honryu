package lifecycleapp_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/domain/compile"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/run"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/domain/telemetry"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

const image = "honryu/jmeter:latest"

type env struct {
	store *fake.Store
	sched *fake.Scheduler
	obj   *fake.ObjectStore
	svc   *lifecycleapp.Service

	projectID   int64
	executionID int64
	planIDs     []int64
}

// setup seeds a project, an execution, and scenarios (each with a JMX test file),
// and stores an execution config. csvSplit toggles execution-level CSV split;
// specs give each scenario's engine count.
func setup(t *testing.T, csvSplit bool, engines ...int) *env {
	t.Helper()
	ctx := context.Background()
	store := fake.NewStore()

	p, _ := project.New("web", "honryu", "")
	// Tenant 7, matching freeze_test.go's campaigns -- campaignapp.Create
	// now requires a participating project to actually belong to the
	// campaign's own tenant. No other test in this file reads TenantID.
	tenantID := int64(7)
	p.TenantID = &tenantID
	projectID, _ := store.CreateProject(ctx, p)
	coll, _ := execution.New("peak", projectID)
	coll.CSVSplit = csvSplit
	executionID, _ := store.CreateExecution(ctx, coll)

	obj := fake.NewObjectStore()
	var tests []loadprofile.Entry
	var planIDs []int64
	for i, n := range engines {
		// A scenario carrying a .jmx is JMeter-native: the config points at the
		// script, and the file has to exist for a pod to run it.
		pl, _ := scenario.NewNative("scenario", projectID, taurus.ExecutorJMeter)
		scenarioID, _ := store.CreateScenario(ctx, pl)
		if err := store.AddScenarioFile(ctx, scenarioID, "test.jmx", true); err != nil {
			t.Fatalf("add test file: %v", err)
		}
		if err := obj.Upload(ctx, fmt.Sprintf("scenario/%d/test.jmx", scenarioID), strings.NewReader("<jmx/>")); err != nil {
			t.Fatalf("upload test file: %v", err)
		}
		planIDs = append(planIDs, scenarioID)
		tests = append(tests, loadprofile.Entry{
			Name: "p", ScenarioID: scenarioID, Concurrency: 10, Rampup: 1, Engines: n, Duration: 30,
		})
		_ = i
	}
	if err := store.StoreLoadProfile(ctx, executionID, csvSplit, tests); err != nil {
		t.Fatalf("store exec execution: %v", err)
	}

	sched := fake.NewScheduler()
	svc := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage(image))
	return &env{store: store, sched: sched, obj: obj, svc: svc, projectID: projectID, executionID: executionID, planIDs: planIDs}
}

func TestDeploy_HappyPath(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2, 3)
	ctx := context.Background()

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	deployed, _ := e.sched.DeployedExecutions(ctx, "")
	if _, ok := deployed[e.executionID]; !ok {
		t.Fatalf("execution not deployed: %v", deployed)
	}
	status, _ := e.sched.ExecutionStatus(ctx, "", e.executionID, []ports.ScenarioRef{{ScenarioID: e.planIDs[0], Shards: 2}, {ScenarioID: e.planIDs[1], Shards: 3}})
	if status.PoolSize != 5 {
		t.Fatalf("pool size = %d, want 5", status.PoolSize)
	}
}

// A Normal execution (setup's own default) deploys at the cluster's default
// pod size -- Deploy must not invent a CPU/Memory request that was never
// configured.
func TestDeploy_LeavesPodSizeEmptyForAnOrdinaryExecution(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	spec, ok := e.sched.LastDeploy(e.executionID, e.planIDs[0])
	if !ok {
		t.Fatal("no deploy recorded")
	}
	if spec.CPU != "" || spec.Memory != "" {
		t.Fatalf("DeploySpec CPU/Memory = %q/%q, want both empty for an ordinary execution", spec.CPU, spec.Memory)
	}
}

// A CalibrateEngine execution's pinned pod size reaches the DeploySpec Deploy
// actually hands the scheduler -- calibration's whole premise depends on
// every step's pod really being the size it claims.
func TestDeploy_ThreadsPinnedPodSizeToTheDeploySpec(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()

	p, _ := project.New("web", "honryu", "")
	projectID, _ := store.CreateProject(ctx, p)
	coll, _ := execution.New("engine-calibration", projectID)
	coll.Kind = execution.KindCalibrateEngine
	coll.CPU, coll.Memory = "2", "1Gi"
	executionID, _ := store.CreateExecution(ctx, coll)

	pl, _ := scenario.NewNative("scenario", projectID, taurus.ExecutorJMeter)
	scenarioID, _ := store.CreateScenario(ctx, pl)
	if err := store.AddScenarioFile(ctx, scenarioID, "test.jmx", true); err != nil {
		t.Fatalf("add test file: %v", err)
	}
	obj := fake.NewObjectStore()
	if err := obj.Upload(ctx, fmt.Sprintf("scenario/%d/test.jmx", scenarioID), strings.NewReader("<jmx/>")); err != nil {
		t.Fatalf("upload test file: %v", err)
	}
	tests := []loadprofile.Entry{{Name: "p", ScenarioID: scenarioID, Concurrency: 10, Rampup: 1, Engines: 1, Duration: 30}}
	if err := store.StoreLoadProfile(ctx, executionID, false, tests); err != nil {
		t.Fatalf("StoreLoadProfile: %v", err)
	}

	sched := fake.NewScheduler()
	svc := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage(image))
	if err := svc.Deploy(ctx, executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	spec, ok := sched.LastDeploy(executionID, scenarioID)
	if !ok {
		t.Fatal("no deploy recorded")
	}
	if spec.CPU != "2" || spec.Memory != "1Gi" {
		t.Fatalf("DeploySpec CPU/Memory = %q/%q, want 2/1Gi (the execution's pinned size)", spec.CPU, spec.Memory)
	}
}

// An execution's cluster reaches the DeploySpec the scheduler receives, so a
// run deploys to the load origin the user chose -- not always the default.
func TestDeploy_ThreadsExecutionClusterToTheDeploySpec(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()

	p, _ := project.New("web", "honryu", "")
	projectID, _ := store.CreateProject(ctx, p)
	coll, _ := execution.New("on-eu", projectID)
	coll.Cluster = "prod-eu"
	executionID, _ := store.CreateExecution(ctx, coll)

	pl, _ := scenario.NewNative("scenario", projectID, taurus.ExecutorJMeter)
	scenarioID, _ := store.CreateScenario(ctx, pl)
	if err := store.AddScenarioFile(ctx, scenarioID, "test.jmx", true); err != nil {
		t.Fatalf("add test file: %v", err)
	}
	obj := fake.NewObjectStore()
	if err := obj.Upload(ctx, fmt.Sprintf("scenario/%d/test.jmx", scenarioID), strings.NewReader("<jmx/>")); err != nil {
		t.Fatalf("upload test file: %v", err)
	}
	tests := []loadprofile.Entry{{Name: "p", ScenarioID: scenarioID, Concurrency: 10, Rampup: 1, Engines: 1, Duration: 30}}
	if err := store.StoreLoadProfile(ctx, executionID, false, tests); err != nil {
		t.Fatalf("StoreLoadProfile: %v", err)
	}

	sched := fake.NewScheduler()
	svc := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage(image))
	if err := svc.Deploy(ctx, executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	spec, ok := sched.LastDeploy(executionID, scenarioID)
	if !ok {
		t.Fatal("no deploy recorded")
	}
	if spec.Cluster != ports.ClusterRef("prod-eu") {
		t.Fatalf("DeploySpec.Cluster = %q, want prod-eu", spec.Cluster)
	}
}

// An execution with no cluster (the pre-Phase-8 shape) still deploys to the
// default -- an empty ClusterRef -- exactly as before.
func TestDeploy_EmptyClusterIsDefault(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)
	ctx := context.Background()
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	spec, ok := e.sched.LastDeploy(e.executionID, e.planIDs[0])
	if !ok {
		t.Fatal("no deploy recorded")
	}
	if spec.Cluster != "" {
		t.Fatalf("DeploySpec.Cluster = %q, want empty (default)", spec.Cluster)
	}
}

func TestDeploy_NoScenarios(t *testing.T) {
	t.Parallel()
	e := setup(t, false) // no scenarios
	if err := e.svc.Deploy(context.Background(), e.executionID); !errors.Is(err, run.ErrNoScenarios) {
		t.Fatalf("Deploy no scenarios: err = %v, want ErrNoScenarios", err)
	}
}

func TestDeploy_RejectedWhileRunning(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()
	if _, err := e.store.StartRun(ctx, e.executionID, ""); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := e.svc.Deploy(ctx, e.executionID); !errors.Is(err, run.ErrAlreadyRunning) {
		t.Fatalf("Deploy while running: err = %v, want ErrAlreadyRunning", err)
	}
}

func TestDeploy_MissingExecution(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	if err := e.svc.Deploy(context.Background(), 9999); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Deploy missing execution: err = %v, want ErrNotFound", err)
	}
}

// The typed form carries the orphan count for the HTTP details envelope
// (phase 24); its message text stays byte-identical to the wrap it
// replaced, and both the domain sentinel (errors.Is) and the typed form
// (errors.As) survive an intervening re-wrap.
func TestTrigger_EnginesFinishedErrorCarriesOrphanCount(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	code := 0
	if err := e.store.RecordOrphanCompletion(ctx, ports.OrphanCompletion{
		ExecutionID: e.executionID, ScenarioID: e.planIDs[0], ShardIndex: 0,
		ExitCode: &code, FinishedAt: time.Unix(1000, 0),
	}); err != nil {
		t.Fatalf("RecordOrphanCompletion: %v", err)
	}

	err := e.svc.Trigger(ctx, e.executionID)
	var efe *lifecycleapp.EnginesFinishedError
	if !errors.As(err, &efe) {
		t.Fatalf("Trigger over finished engines = %T (%v), want *EnginesFinishedError", err, err)
	}
	if efe.Orphaned != 1 {
		t.Fatalf("Orphaned = %d, want 1", efe.Orphaned)
	}
	const want = "run: engines already finished, redeploy before triggering: 1 orphaned shard completion(s)"
	if err.Error() != want {
		t.Fatalf("error text = %q, want %q", err.Error(), want)
	}

	wrapped := fmt.Errorf("trigger: %w", err)
	if !errors.Is(wrapped, run.ErrEnginesFinished) {
		t.Fatal("errors.Is(wrapped, run.ErrEnginesFinished) = false, want true")
	}
	var wrappedEfe *lifecycleapp.EnginesFinishedError
	if !errors.As(wrapped, &wrappedEfe) || wrappedEfe.Orphaned != 1 {
		t.Fatal("errors.As on the re-wrap lost the typed form / count")
	}
}

// Trigger refuses to open a run for engines whose Finals already arrived
// orphaned (they ran and finished while nobody triggered — task 121's live
// stranding), and a fresh Deploy clears the evidence because it is genuinely
// new engines.
func TestTrigger_RefusesFinishedEnginesUntilRedeploy(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	// The engines ran out their hold before Trigger was called: their Final
	// arrived with no run open and was recorded as an orphan completion.
	code := 0
	if err := e.store.RecordOrphanCompletion(ctx, ports.OrphanCompletion{
		ExecutionID: e.executionID, ScenarioID: e.planIDs[0], ShardIndex: 0,
		ExitCode: &code, FinishedAt: time.Unix(1000, 0),
	}); err != nil {
		t.Fatalf("RecordOrphanCompletion: %v", err)
	}

	err := e.svc.Trigger(ctx, e.executionID)
	if !errors.Is(err, run.ErrEnginesFinished) {
		t.Fatalf("Trigger over finished engines = %v, want ErrEnginesFinished", err)
	}
	if _, running, _ := e.store.CurrentRun(ctx, e.executionID); running {
		t.Fatal("Trigger opened a run for engines that already finished")
	}

	// A redeploy is the fix: it clears the orphans, and Trigger proceeds.
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("re-Deploy: %v", err)
	}
	orphans, err := e.store.OrphanCompletions(ctx, e.executionID)
	if err != nil {
		t.Fatalf("OrphanCompletions: %v", err)
	}
	if len(orphans) != 0 {
		t.Fatalf("re-Deploy left %d orphans", len(orphans))
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger after redeploy: %v", err)
	}
}

// Stop must NOT clear the orphaned completions (phase 47 pinned this when
// the reap path learned to clear its own): a stopped run's engines really
// did finish, straggler Finals may still land after the stop closes the run,
// and that orphan evidence is exactly what makes the next Trigger over the
// dead engines refuse with ErrEnginesFinished instead of opening a corpse
// run. Clearing belongs to the paths that end the engine generation for
// good -- Deploy (new generation) and ReapIdle (pods deleted) -- not to the
// shared teardown. The same split is why Purge leaves the clear to the next
// Deploy, exactly as it always has.
func TestStop_KeepsOrphanedCompletions(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	// A straggler Final landing once the stop has closed the run.
	code := 0
	if err := e.store.RecordOrphanCompletion(ctx, ports.OrphanCompletion{
		ExecutionID: e.executionID, ScenarioID: e.planIDs[0], ShardIndex: 0,
		ExitCode: &code, FinishedAt: time.Unix(1000, 0),
	}); err != nil {
		t.Fatalf("RecordOrphanCompletion: %v", err)
	}

	if err := e.svc.Stop(ctx, e.executionID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if orphans, err := e.store.OrphanCompletions(ctx, e.executionID); err != nil || len(orphans) != 1 {
		t.Fatalf("orphans after stop = %v (%v), want the 1 recorded -- Stop keeps orphan evidence", orphans, err)
	}
}

func TestTrigger_HappyPathStartsRunAndMarksScenariosRunning(t *testing.T) {
	t.Parallel()
	e := setup(t, true, 2, 3) // 5 engines total, execution CSV split on
	ctx := context.Background()
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	// Under Taurus there is no engine call on trigger: a pod generates load from
	// the moment it starts. Trigger records that the run is under way.
	// A run is active and both scenarios are marked running.
	runID, ok, _ := e.store.CurrentRun(ctx, e.executionID)
	if !ok {
		t.Fatal("no active run after trigger")
	}
	rps, _ := e.store.RunningScenariosByExecution(ctx, e.executionID)
	if len(rps) != 2 {
		t.Fatalf("running scenarios = %d, want 2", len(rps))
	}
	// The run carries the correlation id its deploy minted: the pending id
	// Deploy parked is what Trigger stamps onto the run.
	pending, err := e.store.PendingCorrelationID(ctx, e.executionID)
	if err != nil {
		t.Fatalf("PendingCorrelationID: %v", err)
	}
	if pending == "" {
		t.Fatal("Deploy parked no correlation id")
	}
	history, err := e.store.RunHistory(ctx, runID)
	if err != nil {
		t.Fatalf("RunHistory: %v", err)
	}
	if history.CorrelationID != pending {
		t.Fatalf("run history correlation = %q, want the deploy's pending %q", history.CorrelationID, pending)
	}
}

// The compiled config is retrievable per run (spec AC), independent of the
// cluster and independent of a later re-deploy: a run's config is a snapshot
// taken when it started, not a live view of whatever is currently deployed.
func TestTrigger_SnapshotsEachShardsCompiledConfig(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2) // one scenario, two shards
	ctx := context.Background()

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	runID, _, _ := e.store.CurrentRun(ctx, e.executionID)

	for shard := 0; shard < 2; shard++ {
		key := lifecycleapp.RunShardKey(runID, e.planIDs[0], shard, "yml")
		got, err := e.obj.Download(ctx, key)
		if err != nil {
			t.Fatalf("Download(%q): %v", key, err)
		}
		if len(got) == 0 {
			t.Errorf("shard %d config is empty", shard)
		}
	}
}

// An execution's configured Taurus criteria (task 64/65's verdict machinery
// depends on this) are compiled into every shard's passfail module -- not
// silently dropped, and not left for a caller to add separately.
func TestTrigger_CompilesConfiguredCriteriaIntoEveryShard(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2) // one scenario, two shards
	ctx := context.Background()
	if err := e.store.SetExecutionCriteria(ctx, e.executionID, []string{"failures>10%", "p95>500ms"}); err != nil {
		t.Fatalf("SetExecutionCriteria: %v", err)
	}

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	runID, _, _ := e.store.CurrentRun(ctx, e.executionID)

	for shard := 0; shard < 2; shard++ {
		key := lifecycleapp.RunShardKey(runID, e.planIDs[0], shard, "yml")
		raw, err := e.obj.Download(ctx, key)
		if err != nil {
			t.Fatalf("Download(%q): %v", key, err)
		}
		var cfg taurus.Config
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			t.Fatalf("shard %d: unmarshal config: %v", shard, err)
		}
		if len(cfg.Reporting) != 1 || cfg.Reporting[0].Module != "passfail" {
			t.Fatalf("shard %d reporting = %+v, want one passfail reporter", shard, cfg.Reporting)
		}
		got := cfg.Reporting[0].Criteria
		if len(got) != 2 || got[0] != "failures>10%" || got[1] != "p95>500ms" {
			t.Fatalf("shard %d criteria = %v, want [failures>10%%, p95>500ms]", shard, got)
		}
	}
}

// A re-deploy changes what is currently staged, but a run that already
// started must keep showing the config it actually ran -- otherwise
// diagnosing a failed run would show a config that was never the one at fault.
func TestTrigger_SnapshotSurvivesALaterRedeploy(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)
	ctx := context.Background()

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	runID, _, _ := e.store.CurrentRun(ctx, e.executionID)
	key := lifecycleapp.RunShardKey(runID, e.planIDs[0], 0, "yml")
	first, err := e.obj.Download(ctx, key)
	if err != nil {
		t.Fatalf("Download after trigger: %v", err)
	}

	if err := e.svc.Stop(ctx, e.executionID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Re-deploy with a different profile, changing what is staged.
	if err := e.store.StoreLoadProfile(ctx, e.executionID, false, []loadprofile.Entry{
		{Name: "p", ScenarioID: e.planIDs[0], Concurrency: 99, Rampup: 1, Engines: 1, Duration: 30},
	}); err != nil {
		t.Fatalf("StoreLoadProfile: %v", err)
	}
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("re-Deploy: %v", err)
	}

	again, err := e.obj.Download(ctx, key)
	if err != nil {
		t.Fatalf("Download after re-deploy: %v", err)
	}
	if string(again) != string(first) {
		t.Error("the run's snapshotted config changed after a later re-deploy")
	}
}

// A native scenario without its script cannot run. The failure now lands on
// Deploy rather than Trigger: the config that points at the script is compiled
// when the pods are created, so nothing is deployed at all instead of pods
// starting and failing on their own.
func TestDeploy_NoTestFile(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()
	if err := e.store.DeleteScenarioFile(ctx, e.planIDs[0], "test.jmx", true); err != nil {
		t.Fatalf("delete test file: %v", err)
	}
	if err := e.svc.Deploy(ctx, e.executionID); !errors.Is(err, lifecycleapp.ErrNoTestFile) {
		t.Fatalf("Deploy without a test file: err = %v, want ErrNoTestFile", err)
	}
	deployed, _ := e.sched.DeployedExecutions(ctx, "")
	if _, ok := deployed[e.executionID]; ok {
		t.Error("pods were deployed for an execution whose scenario has no script")
	}
}

func TestTrigger_NotDeployed(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	if err := e.svc.Trigger(context.Background(), e.executionID); !errors.Is(err, run.ErrNotDeployed) {
		t.Fatalf("Trigger not deployed: err = %v, want ErrNotDeployed", err)
	}
}

func TestTrigger_EnginesNotReady(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 3)
	ctx := context.Background()
	// Deploy only 2 of the 3 wanted engines directly on the scheduler.
	if err := e.sched.DeployScenario(ctx, ports.DeploySpec{ProjectID: e.projectID, ExecutionID: e.executionID, ScenarioID: e.planIDs[0], Shards: deployShards(2), Image: image}); err != nil {
		t.Fatalf("partial deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); !errors.Is(err, run.ErrEnginesNotReady) {
		t.Fatalf("Trigger under-provisioned: err = %v, want ErrEnginesNotReady", err)
	}
}

func TestTrigger_AlreadyRunning(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if _, err := e.store.StartRun(ctx, e.executionID, ""); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); !errors.Is(err, run.ErrAlreadyRunning) {
		t.Fatalf("Trigger while running: err = %v, want ErrAlreadyRunning", err)
	}
}

func TestTrigger_ErrorRollsBackRun(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	e.store.MarkRunningErr = errors.New("db down")
	if err := e.svc.Trigger(ctx, e.executionID); err == nil {
		t.Fatal("Trigger with a failing repository: want error, got nil")
	}
	if _, ok, _ := e.store.CurrentRun(ctx, e.executionID); ok {
		t.Fatal("run was not rolled back after total failure")
	}
}

func TestStop_HappyPath(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2, 3)
	ctx := context.Background()
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if err := e.svc.Stop(ctx, e.executionID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, ok, _ := e.store.CurrentRun(ctx, e.executionID); ok {
		t.Fatal("run still active after stop")
	}
	if rps, _ := e.store.RunningScenariosByExecution(ctx, e.executionID); len(rps) != 0 {
		t.Fatalf("running scenarios after stop = %d, want 0", len(rps))
	}
	// Engines are torn down by removing their pods (Purge), not by calling them,
	// so Stop clears run state only.
}

func TestStop_NotRunning(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	if err := e.svc.Stop(context.Background(), e.executionID); !errors.Is(err, run.ErrNotRunning) {
		t.Fatalf("Stop not running: err = %v, want ErrNotRunning", err)
	}
}

func TestPurge_StopsThenRemoves(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if err := e.svc.Purge(ctx, e.executionID); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if _, ok, _ := e.store.CurrentRun(ctx, e.executionID); ok {
		t.Fatal("run still active after purge")
	}
	deployed, _ := e.sched.DeployedExecutions(ctx, "")
	if _, ok := deployed[e.executionID]; ok {
		t.Fatal("execution still deployed after purge")
	}
}

func TestPurge_WhenIdle(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Purge(ctx, e.executionID); err != nil {
		t.Fatalf("Purge idle: %v", err)
	}
	deployed, _ := e.sched.DeployedExecutions(ctx, "")
	if _, ok := deployed[e.executionID]; ok {
		t.Fatal("execution still deployed after purge")
	}
}

func TestStatus_ReportsPhaseAndProgress(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2, 3)
	ctx := context.Background()
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	st, err := e.svc.Status(ctx, e.executionID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Phase != run.PhaseDeployed || st.PoolSize != 5 || len(st.Scenarios) != 2 {
		t.Fatalf("status = %+v, want deployed/5/2", st)
	}

	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	st, _ = e.svc.Status(ctx, e.executionID)
	if st.Phase != run.PhaseRunning {
		t.Fatalf("phase after trigger = %q, want running", st.Phase)
	}
	for _, ps := range st.Scenarios {
		if !ps.InProgress {
			t.Fatalf("scenario %d not marked in progress: %+v", ps.ScenarioID, ps)
		}
	}
}

func TestEnginesDetailAndPodLog(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	detail, err := e.svc.EnginesDetail(ctx, e.projectID, e.executionID)
	if err != nil {
		t.Fatalf("EnginesDetail: %v", err)
	}
	if len(detail.Engines) != 2 {
		t.Fatalf("detail engines = %d, want 2", len(detail.Engines))
	}
	log, err := e.svc.PodLog(ctx, e.executionID, e.planIDs[0])
	if err != nil || log == "" {
		t.Fatalf("PodLog = %q, err = %v", log, err)
	}
}

func TestStop_WhenEnginesUnreachable(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	// Engines vanish before stop: teardown still clears run state (best effort).
	e.sched.Unreachable = true
	if err := e.svc.Stop(ctx, e.executionID); err != nil {
		t.Fatalf("Stop with unreachable engines: %v", err)
	}
	if _, ok, _ := e.store.CurrentRun(ctx, e.executionID); ok {
		t.Fatal("run still active after stop")
	}
	if rps, _ := e.store.RunningScenariosByExecution(ctx, e.executionID); len(rps) != 0 {
		t.Fatalf("running scenarios after stop = %d, want 0", len(rps))
	}
}

// Engine logs are the only diagnosis left once a pod is gone, and Purge is
// what deletes it -- so this is the last moment they exist anywhere else.
func TestPurge_CapturesEngineLogsBeforeDeletingPods(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2) // one scenario, two shards
	ctx := context.Background()

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	runID, ok, _ := e.store.CurrentRun(ctx, e.executionID)
	if !ok {
		t.Fatal("no active run after trigger")
	}

	if err := e.svc.Purge(ctx, e.executionID); err != nil {
		t.Fatalf("Purge: %v", err)
	}

	for shard := 0; shard < 2; shard++ {
		key := lifecycleapp.RunShardKey(runID, e.planIDs[0], shard, "log")
		got, err := e.obj.Download(ctx, key)
		if err != nil {
			t.Fatalf("Download(%q): %v", key, err)
		}
		if len(got) == 0 {
			t.Errorf("shard %d log is empty", shard)
		}
	}
}

// Each shard's PodLog/Upload round trip is independent of every other's, so
// Purge fetches them concurrently rather than one after another -- a customer
// is waiting on this request, and an execution can have dozens of shards.
func TestPurge_CapturesShardLogsConcurrently(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 5) // one scenario, five shards
	ctx := context.Background()
	e.sched.PodLogDelay = 40 * time.Millisecond

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	start := time.Now()
	if err := e.svc.Purge(ctx, e.executionID); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	elapsed := time.Since(start)

	// Five shards run one after another would take at least 5*40ms = 200ms;
	// concurrently, one shard's delay dominates. A generous margin above one
	// delay still rules out serial execution without being timing-flaky.
	if elapsed >= 3*e.sched.PodLogDelay {
		t.Errorf("Purge took %v capturing 5 shards' logs at %v each, want well under serial time -- looks sequential", elapsed, e.sched.PodLogDelay)
	}
}

// Purge must still remove a broken execution's engines even if capturing its
// logs fails -- diagnosability degrades, the operation does not.
func TestPurge_LogCaptureFailureDoesNotFailPurge(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)
	ctx := context.Background()
	e.obj.UploadErr = errors.New("bucket unavailable")

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if err := e.svc.Purge(ctx, e.executionID); err != nil {
		t.Fatalf("Purge: %v, want nil despite the log store failing", err)
	}
}

// A shard whose pod is already unreachable must not stop the others from
// being captured.
func TestPurge_CapturesTheShardsItCanReach(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	runID, _, _ := e.store.CurrentRun(ctx, e.executionID)

	e.sched.PodLogErr = errors.New("pod unreachable")
	if err := e.svc.Purge(ctx, e.executionID); err != nil {
		t.Fatalf("Purge: %v, want nil despite every shard being unreachable", err)
	}

	key := lifecycleapp.RunShardKey(runID, e.planIDs[0], 0, "log")
	if _, err := e.obj.Download(ctx, key); !errors.Is(err, ports.ErrObjectNotFound) {
		t.Errorf("Download after every PodLog failed = %v, want ErrObjectNotFound", err)
	}
}

// Deploying without ever triggering leaves no run to key logs by, and Purge
// must not invent one.
func TestPurge_WithoutARunNeverCapturesLogs(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)
	ctx := context.Background()

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Purge(ctx, e.executionID); err != nil {
		t.Fatalf("Purge: %v", err)
	}

	key := lifecycleapp.RunShardKey(0, e.planIDs[0], 0, "log")
	if _, err := e.obj.Download(ctx, key); !errors.Is(err, ports.ErrObjectNotFound) {
		t.Errorf("Download = %v, want ErrObjectNotFound -- nothing was ever triggered", err)
	}
}

// recordingMetrics implements lifecycleapp.Metrics and records the calls.
type recordingMetrics struct {
	purged    []int64
	finalized []int64 // run ids
	// FinalizeErr, when set, is returned by Finalize -- teardown must tolerate
	// it, since a customer must be able to stop a broken execution regardless.
	FinalizeErr error
}

func (m *recordingMetrics) Purge(id int64) { m.purged = append(m.purged, id) }

func (m *recordingMetrics) FinalizeOrphaned(ctx context.Context, executionID, runID int64, _ []ports.OrphanCompletion) error {
	return m.Finalize(ctx, executionID, runID)
}

func (m *recordingMetrics) Finalize(_ context.Context, _, runID int64) error {
	m.finalized = append(m.finalized, runID)
	return m.FinalizeErr
}

// Purging an execution must drop its metric series: with measurements pushed
// there is nothing to start or stop, but nothing else would ever remove them.
func TestMetricsHook_PurgeDropsSeries(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()
	rec := &recordingMetrics{}
	e.svc.WithMetrics(rec)

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Purge(ctx, e.executionID); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if len(rec.purged) != 1 || rec.purged[0] != e.executionID {
		t.Fatalf("purged = %v, want [%d]", rec.purged, e.executionID)
	}
}

// Stop and Purge both end a run through teardown, so both must finalise its
// report -- Honryu is the one deciding the run is over, and Finalize needs the
// run id before StopRun forgets it.
func TestMetricsHook_StopFinalizesTheReport(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()
	rec := &recordingMetrics{}
	e.svc.WithMetrics(rec)

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	runID, ok, _ := e.store.CurrentRun(ctx, e.executionID)
	if !ok {
		t.Fatal("no active run after trigger")
	}

	if err := e.svc.Stop(ctx, e.executionID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(rec.finalized) != 1 || rec.finalized[0] != runID {
		t.Fatalf("finalized = %v, want [%d]", rec.finalized, runID)
	}
}

// Deploying without ever triggering leaves no run to finalise, and Purge must
// not invent one.
func TestMetricsHook_PurgeWithoutARunNeverFinalizes(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()
	rec := &recordingMetrics{}
	e.svc.WithMetrics(rec)

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Purge(ctx, e.executionID); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if len(rec.finalized) != 0 {
		t.Errorf("finalized = %v, want none: nothing was ever triggered", rec.finalized)
	}
}

// A customer must be able to stop or purge a broken execution even if writing
// its final report fails -- that failure must not become the reason teardown
// itself fails.
func TestMetricsHook_StopSucceedsEvenIfFinalizeFails(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()
	rec := &recordingMetrics{FinalizeErr: errors.New("boom")}
	e.svc.WithMetrics(rec)

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if err := e.svc.Stop(ctx, e.executionID); err != nil {
		t.Fatalf("Stop: %v, want nil despite Finalize failing", err)
	}
	if len(rec.finalized) != 1 {
		t.Fatalf("Finalize was not attempted")
	}
}

// recordingUsage implements lifecycleapp.Usage.
type recordingUsage struct {
	started, finished int
	lastOwner         string
	lastVU            int
}

func (u *recordingUsage) RecordStart(_ context.Context, _ int64, owner string, _, vu int) error {
	u.started++
	u.lastOwner = owner
	u.lastVU = vu
	return nil
}

func (u *recordingUsage) RecordFinish(_ context.Context, _ int64, _ int) error {
	u.finished++
	return nil
}

func TestUsageHooks_FireOnTriggerAndTeardown(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2) // 2 engines, concurrency 10 -> VU 20
	ctx := context.Background()
	usage := &recordingUsage{}
	e.svc.WithUsage(usage)

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if usage.started != 1 || usage.lastOwner != "honryu" || usage.lastVU != 20 {
		t.Fatalf("usage start = %+v, want started=1 owner=honryu vu=20", usage)
	}
	if err := e.svc.Stop(ctx, e.executionID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if usage.finished != 1 {
		t.Fatalf("usage finished = %d, want 1", usage.finished)
	}
}

func TestResume_ListsRunningScenarios(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	rps, err := e.svc.Resume(ctx)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(rps) != 1 {
		t.Fatalf("resume running scenarios = %d, want 1", len(rps))
	}
}

// deployShards builds n placeholder shard specs for a deploy.
func deployShards(n int) []ports.ShardSpec {
	out := make([]ports.ShardSpec, n)
	for i := range out {
		out[i] = ports.ShardSpec{Index: i, Concurrency: 1}
	}
	return out
}

// Each pod must get a config carrying only its own slice of the load. If every
// shard got the whole profile, N pods would produce N times the load asked for.
func TestDeploy_CompilesAConfigPerShard(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 3) // 10 users across 3 pods
	ctx := context.Background()

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	spec, ok := e.sched.LastDeploy(e.executionID, e.planIDs[0])
	if !ok {
		t.Fatal("nothing was deployed")
	}
	if len(spec.Shards) != 3 {
		t.Fatalf("deployed %d shards, want 3", len(spec.Shards))
	}

	total := 0
	for _, sh := range spec.Shards {
		if len(sh.Config) == 0 {
			t.Fatalf("shard %d has no config, so its pod would run nothing", sh.Index)
		}
		var cfg taurus.Config
		if err := yaml.Unmarshal(sh.Config, &cfg); err != nil {
			t.Fatalf("shard %d config is not valid YAML: %v", sh.Index, err)
		}
		if len(cfg.Execution) != 1 {
			t.Fatalf("shard %d config has %d executions, want 1", sh.Index, len(cfg.Execution))
		}
		got := cfg.Execution[0].Concurrency
		if got != sh.Concurrency {
			t.Errorf("shard %d config says %d users, plan says %d", sh.Index, got, sh.Concurrency)
		}
		total += got
	}
	if total != 10 {
		t.Errorf("shards total %d users, want the 10 that were requested", total)
	}

	// The script the configs point at has to travel with them.
	if _, ok := spec.ScenarioFiles["test.jmx"]; !ok {
		t.Errorf("scenario files = %v, want the test plan", spec.ScenarioFiles)
	}
}

// A portable scenario compiles compile.ScenarioInput.Requests from whatever
// was uploaded via scenarioapp.SetRequests -- this is the wiring that makes a
// portable scenario runnable at all, not just able to accept an upload.
func TestDeploy_PortableScenarioCompilesUploadedRequests(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	obj := fake.NewObjectStore()

	p, _ := project.New("web", "honryu", "")
	projectID, _ := store.CreateProject(ctx, p)
	coll, _ := execution.New("peak", projectID)
	executionID, _ := store.CreateExecution(ctx, coll)

	pl, _ := scenario.New("portable", projectID)
	scenarioID, _ := store.CreateScenario(ctx, pl)
	raw := []byte("default-address: http://example.com\nrequests:\n  - url: /checkout\n")
	if err := store.SetScenarioRequests(ctx, scenarioID, raw); err != nil {
		t.Fatalf("SetScenarioRequests: %v", err)
	}

	tests := []loadprofile.Entry{{Name: "p", ScenarioID: scenarioID, Concurrency: 1, Rampup: 1, Engines: 1, Duration: 30}}
	if err := store.StoreLoadProfile(ctx, executionID, false, tests); err != nil {
		t.Fatalf("StoreLoadProfile: %v", err)
	}

	sched := fake.NewScheduler()
	svc := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage(image))
	if err := svc.Deploy(ctx, executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	spec, ok := sched.LastDeploy(executionID, scenarioID)
	if !ok {
		t.Fatal("nothing was deployed")
	}
	if len(spec.Shards) != 1 {
		t.Fatalf("deployed %d shards, want 1", len(spec.Shards))
	}
	var cfg taurus.Config
	if err := yaml.Unmarshal(spec.Shards[0].Config, &cfg); err != nil {
		t.Fatalf("shard config is not valid YAML: %v", err)
	}
	if len(cfg.Scenarios) != 1 {
		t.Fatalf("compiled config has %d scenarios, want 1", len(cfg.Scenarios))
	}
	for _, ts := range cfg.Scenarios {
		if ts.DefaultAddress != "http://example.com" {
			t.Errorf("default-address = %q, want http://example.com", ts.DefaultAddress)
		}
		if len(ts.Requests) != 1 || ts.Requests[0].URL != "/checkout" {
			t.Errorf("requests = %+v, want one request to /checkout", ts.Requests)
		}
	}
}

// portableCompiledFields names the taurus.Scenario fields a portable
// scenario's compileScenario branch actually populates from ScenarioInput
// (compile.go:141-166): DefaultAddress, Headers, Requests, Timeout,
// KeepAlive. Script is excluded deliberately -- it is only ever set in the
// NATIVE branch, never for a portable scenario, regardless of what a
// portable fragment's own script: key says.
var portableCompiledFields = map[string]struct{}{
	"default-address": {},
	"headers":         {},
	"requests":        {},
	"timeout":         {},
	"keepalive":       {},
}

// Phase 19b F3: pins scenarioapp.ModelledButUncompiled against the real
// compile path, structurally and behaviourally, so the two cannot drift
// apart the way they did before review finding 2 was fixed (F2).
//
// Structural half: every yaml-tagged field of taurus.Scenario must be
// accounted for by exactly one of {portableCompiledFields,
// scenarioapp.ModelledButUncompiled}, with no overlap and nothing left over.
// A new field added to taurus.Scenario that is not wired into either list
// fails here immediately, naming the field -- catching the review finding 2
// class of bug (a modelled field silently uncompiled with no diagnostic) at
// the type level, not only for the two fields known today.
func TestModelledButUncompiled_AccountsForEveryScenarioField(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(taurus.Scenario{})
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("yaml")
		name, _, _ := strings.Cut(tag, ",")
		_, compiled := portableCompiledFields[name]
		_, uncompiled := scenarioapp.ModelledButUncompiled[name]
		switch {
		case compiled && uncompiled:
			t.Errorf("field %q is in both portableCompiledFields and scenarioapp.ModelledButUncompiled", name)
		case !compiled && !uncompiled:
			t.Errorf("field %q (taurus.Scenario.%s) is in neither list -- if compileScenario now "+
				"reads it from the fragment, add it to portableCompiledFields here; if not, add it to "+
				"scenarioapp.ModelledButUncompiled (diagnostics.go) so the editor warns about it",
				name, typ.Field(i).Name)
		}
	}
	for name := range scenarioapp.ModelledButUncompiled {
		if _, compiled := portableCompiledFields[name]; compiled {
			t.Errorf("scenarioapp.ModelledButUncompiled names %q, but portableCompiledFields also claims it -- "+
				"compileScenario must have started compiling it; remove it from ModelledButUncompiled "+
				"(diagnostics.go) so the editor stops warning about a key that now works", name)
		}
	}
}

// Behavioural half of F3: deploys a portable scenario whose stored fragment
// sets BOTH of today's modelled-but-uncompiled keys (data-sources, script)
// alongside requests, and asserts the compiled shard config carries neither
// -- the runtime proof behind the structural claim above. If compileScenario
// is ever changed to honour one of these from the fragment, this test fails
// with the fragment's own sentinel value, pointing at exactly what changed.
func TestDeploy_ModelledButUncompiledKeysNeverReachCompiledConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	obj := fake.NewObjectStore()

	p, _ := project.New("web", "honryu", "")
	projectID, _ := store.CreateProject(ctx, p)
	coll, _ := execution.New("peak", projectID)
	executionID, _ := store.CreateExecution(ctx, coll)

	pl, _ := scenario.New("portable", projectID)
	scenarioID, _ := store.CreateScenario(ctx, pl)
	raw := []byte("requests:\n  - url: /checkout\n" +
		"data-sources:\n  - path: sentinel-users.csv\n" +
		"script: sentinel-custom.jmx\n")
	if err := store.SetScenarioRequests(ctx, scenarioID, raw); err != nil {
		t.Fatalf("SetScenarioRequests: %v", err)
	}

	tests := []loadprofile.Entry{{Name: "p", ScenarioID: scenarioID, Concurrency: 1, Rampup: 1, Engines: 1, Duration: 30}}
	if err := store.StoreLoadProfile(ctx, executionID, false, tests); err != nil {
		t.Fatalf("StoreLoadProfile: %v", err)
	}

	sched := fake.NewScheduler()
	svc := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage(image))
	if err := svc.Deploy(ctx, executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	spec, ok := sched.LastDeploy(executionID, scenarioID)
	if !ok {
		t.Fatal("nothing was deployed")
	}
	var cfg taurus.Config
	if err := yaml.Unmarshal(spec.Shards[0].Config, &cfg); err != nil {
		t.Fatalf("shard config is not valid YAML: %v", err)
	}
	for _, ts := range cfg.Scenarios {
		if len(ts.DataSources) != 0 {
			t.Errorf("compiled DataSources = %+v, want empty -- data-sources must not reach the "+
				"engine from a fragment (only from uploaded scenario files)", ts.DataSources)
		}
		if ts.Script != "" {
			t.Errorf("compiled Script = %q, want empty -- a portable scenario's fragment script: "+
				"must never reach the engine", ts.Script)
		}
		if len(ts.Requests) != 1 || ts.Requests[0].URL != "/checkout" {
			t.Errorf("requests = %+v, want one request to /checkout (the compiled fields must still work)", ts.Requests)
		}
	}
}

// One Deploy call is one run in practice, so every scenario and shard it
// compiles must carry the same freshly-minted trace-context headers -- and two
// deploys must never share one, or an APM query for one run's id would return
// another run's traffic alongside it.
func TestDeploy_InjectsTraceContextHeaders(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2, 2) // two scenarios, two shards each
	ctx := context.Background()

	first := telemetry.TraceContext{TraceID: strings.Repeat("a", 32), ParentID: strings.Repeat("b", 16)}
	second := telemetry.TraceContext{TraceID: strings.Repeat("c", 32), ParentID: strings.Repeat("d", 16)}
	calls := 0
	e.svc.WithTraceContext(func() (telemetry.TraceContext, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		return second, nil
	})

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if calls != 1 {
		t.Fatalf("trace context generated %d times in one Deploy, want exactly once", calls)
	}
	// The minted id is parked on the execution for the next Trigger to stamp
	// onto the run it creates.
	if pending, err := e.store.PendingCorrelationID(ctx, e.executionID); err != nil || pending != first.TraceID {
		t.Fatalf("pending correlation id after Deploy = %q (err %v), want the minted %q", pending, err, first.TraceID)
	}

	// setup()'s execution carries no tenant (only its project does), so the
	// baggage renders without a honryu.tenant entry; the tenant-carrying shape
	// is asserted in TestDeploy_BaggageCarriesTheExecutionsTenant below.
	want := map[string]string{
		"traceparent": "00-" + first.TraceID + "-" + first.ParentID + "-00",
		"baggage": fmt.Sprintf("honryu.service=%d,honryu.execution=%d,honryu.run=%s",
			e.projectID, e.executionID, first.TraceID),
	}
	for _, sid := range e.planIDs {
		spec, ok := e.sched.LastDeploy(e.executionID, sid)
		if !ok {
			t.Fatalf("scenario %d not deployed", sid)
		}
		for _, sh := range spec.Shards {
			var cfg taurus.Config
			if err := yaml.Unmarshal(sh.Config, &cfg); err != nil {
				t.Fatalf("shard %d config is not valid YAML: %v", sh.Index, err)
			}
			for key, ts := range cfg.Scenarios {
				if !reflect.DeepEqual(ts.Headers, want) {
					t.Errorf("scenario %q shard %d headers = %v, want %v", key, sh.Index, ts.Headers, want)
				}
			}
		}
	}

	// A second deploy is a second future run: it must carry a fresh identity,
	// not the first one's.
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("second Deploy: %v", err)
	}
	if calls != 2 {
		t.Fatalf("trace context generated %d times across two Deploys, want 2", calls)
	}
	// Last deploy wins: the pending id now points at the second deploy, since
	// the next Trigger runs against the pods the latest Deploy created.
	if pending, err := e.store.PendingCorrelationID(ctx, e.executionID); err != nil || pending != second.TraceID {
		t.Fatalf("pending correlation id after second Deploy = %q (err %v), want the second minted %q", pending, err, second.TraceID)
	}
	spec, ok := e.sched.LastDeploy(e.executionID, e.planIDs[0])
	if !ok {
		t.Fatal("nothing re-deployed")
	}
	var cfg taurus.Config
	if err := yaml.Unmarshal(spec.Shards[0].Config, &cfg); err != nil {
		t.Fatalf("re-deployed shard config is not valid YAML: %v", err)
	}
	wantSecond := "00-" + second.TraceID + "-" + second.ParentID + "-00"
	for key, ts := range cfg.Scenarios {
		if got := ts.Headers["traceparent"]; got != wantSecond {
			t.Errorf("scenario %q re-deploy traceparent = %q, want the fresh %q", key, got, wantSecond)
		}
	}
}

// An execution that carries a tenant must have it in the baggage, so a
// multi-tenant target's APM can attribute generated load to the right tenant.
func TestDeploy_BaggageCarriesTheExecutionsTenant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	obj := fake.NewObjectStore()

	p, _ := project.New("web", "honryu", "")
	projectID, _ := store.CreateProject(ctx, p)
	coll, _ := execution.New("peak", projectID)
	tenantID := int64(7)
	coll.TenantID = &tenantID
	executionID, _ := store.CreateExecution(ctx, coll)

	pl, _ := scenario.NewNative("scenario", projectID, taurus.ExecutorJMeter)
	scenarioID, _ := store.CreateScenario(ctx, pl)
	if err := store.AddScenarioFile(ctx, scenarioID, "test.jmx", true); err != nil {
		t.Fatalf("add test file: %v", err)
	}
	if err := obj.Upload(ctx, fmt.Sprintf("scenario/%d/test.jmx", scenarioID), strings.NewReader("<jmx/>")); err != nil {
		t.Fatalf("upload test file: %v", err)
	}
	tests := []loadprofile.Entry{{Name: "p", ScenarioID: scenarioID, Concurrency: 2, Rampup: 1, Engines: 1, Duration: 30}}
	if err := store.StoreLoadProfile(ctx, executionID, false, tests); err != nil {
		t.Fatalf("StoreLoadProfile: %v", err)
	}

	sched := fake.NewScheduler()
	svc := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage(image))
	if err := svc.Deploy(ctx, executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	spec, ok := sched.LastDeploy(executionID, scenarioID)
	if !ok {
		t.Fatal("nothing was deployed")
	}
	var cfg taurus.Config
	if err := yaml.Unmarshal(spec.Shards[0].Config, &cfg); err != nil {
		t.Fatalf("shard config is not valid YAML: %v", err)
	}
	for _, ts := range cfg.Scenarios {
		if !strings.HasPrefix(ts.Headers["baggage"], "honryu.tenant=7,") {
			t.Errorf("baggage = %q, want it to lead with honryu.tenant=7,", ts.Headers["baggage"])
		}
	}
}

// A portable scenario with nothing uploaded must fail exactly the way it
// always has -- compile.ErrRequestsRequired -- not hang or silently deploy
// with an empty workload.
func TestDeploy_PortableScenarioWithoutRequestsFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	obj := fake.NewObjectStore()

	p, _ := project.New("web", "honryu", "")
	projectID, _ := store.CreateProject(ctx, p)
	coll, _ := execution.New("peak", projectID)
	executionID, _ := store.CreateExecution(ctx, coll)

	pl, _ := scenario.New("portable", projectID)
	scenarioID, _ := store.CreateScenario(ctx, pl)

	tests := []loadprofile.Entry{{Name: "p", ScenarioID: scenarioID, Concurrency: 1, Rampup: 1, Engines: 1, Duration: 30}}
	if err := store.StoreLoadProfile(ctx, executionID, false, tests); err != nil {
		t.Fatalf("StoreLoadProfile: %v", err)
	}

	sched := fake.NewScheduler()
	svc := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage(image))
	if err := svc.Deploy(ctx, executionID); !errors.Is(err, compile.ErrRequestsRequired) {
		t.Fatalf("Deploy(portable, no requests) = %v, want ErrRequestsRequired", err)
	}
}

func TestWithDefaultEngine(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)
	if got := e.svc.WithDefaultEngine(taurus.ExecutorK6); got == nil {
		t.Fatal("WithDefaultEngine returned nil")
	}
}

// A data file the scenario recorded but never uploaded fails the deploy, naming
// the file: "object not found" alone leaves an operator guessing which of a
// scenario's artefacts is missing.
func TestDeploy_MissingDataFileNamesIt(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)
	ctx := context.Background()

	if err := e.store.AddScenarioFile(ctx, e.planIDs[0], "users.csv", false); err != nil {
		t.Fatalf("record data file: %v", err)
	}
	err := e.svc.Deploy(ctx, e.executionID)
	if err == nil {
		t.Fatal("Deploy with a missing data file succeeded")
	}
	if !strings.Contains(err.Error(), "users.csv") {
		t.Errorf("error %q does not name the missing file", err)
	}
}

// A failure part way through must not leave half an execution running.
func TestDeploy_UnknownScenarioFails(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)
	ctx := context.Background()

	if err := e.store.StoreLoadProfile(ctx, e.executionID, false, []loadprofile.Entry{
		{ScenarioID: 999999, Concurrency: 5, Rampup: 1, Engines: 1, Duration: 10},
	}); err != nil {
		t.Fatalf("StoreLoadProfile: %v", err)
	}
	if err := e.svc.Deploy(ctx, e.executionID); err == nil {
		t.Fatal("Deploy referencing a missing scenario succeeded")
	}
}

// RunExecutionID is the HTTP layer's run->execution resolution for the
// run-keyed report routes: it must map a run to the execution that owns it
// and surface ErrNotFound for anything else.
func TestRunExecutionID(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)

	ctx := context.Background()
	runID, err := e.store.StartRun(ctx, e.executionID, "trace-1")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	svc := lifecycleapp.NewService(e.store, e.sched, e.obj, lifecycleapp.StaticImage(image))

	got, err := svc.RunExecutionID(ctx, runID)
	if err != nil {
		t.Fatalf("RunExecutionID: %v", err)
	}
	if got != e.executionID {
		t.Fatalf("RunExecutionID = %d, want %d", got, e.executionID)
	}
	if _, err := svc.RunExecutionID(ctx, 9999); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("unknown run error = %v, want ErrNotFound", err)
	}
}
