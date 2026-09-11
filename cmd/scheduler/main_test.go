package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/calibrationapp"
	"github.com/heridotlife/honryu/internal/app/campaignapp"
	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/app/quotaapp"
	"github.com/heridotlife/honryu/internal/app/scheduleapp"
	"github.com/heridotlife/honryu/internal/config"
	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/campaign"
	"github.com/heridotlife/honryu/internal/domain/clusterregistry"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/reservation"
	runpkg "github.com/heridotlife/honryu/internal/domain/run" // aliased: this package's own run() is the local name
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/schedule"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

func TestNewRepository_Fake(t *testing.T) {
	t.Parallel()
	repo, err := newRepository(config.DBConfig{Driver: "fake"}, "default")
	if err != nil || repo == nil {
		t.Fatalf("newRepository(fake) = %v, %v", repo, err)
	}
}

func TestNewRepository_Unsupported(t *testing.T) {
	t.Parallel()
	if _, err := newRepository(config.DBConfig{Driver: "postgres"}, "default"); err == nil {
		t.Fatal("newRepository(postgres): expected error, got nil")
	}
}

func TestNewRepository_MySQL_Unreachable(t *testing.T) {
	t.Parallel()
	// Nothing listens on port 1, so the ping fails fast: covers the mysql
	// open-ok / ping-error wiring branch without needing a container.
	_, err := newRepository(config.DBConfig{
		Driver: "mysql",
		DSN:    "honryu:secret@tcp(127.0.0.1:1)/honryu?parseTime=true",
	}, "default")
	if err == nil {
		t.Fatal("newRepository(mysql, unreachable): expected error, got nil")
	}
}

func TestNewScheduler(t *testing.T) {
	t.Parallel()
	if s, err := newScheduler(config.ClusterConfig{Scheduler: "fake"}, fake.NewStore()); err != nil || s == nil {
		t.Fatalf("newScheduler(fake) = %v, %v", s, err)
	}
	if _, err := newScheduler(config.ClusterConfig{Scheduler: "k8s", Namespace: "default", EnginePort: 8080}, fake.NewStore()); err == nil {
		t.Fatal("newScheduler(k8s) outside cluster: expected error, got nil")
	}
	if _, err := newScheduler(config.ClusterConfig{Scheduler: "nope"}, fake.NewStore()); err == nil {
		t.Fatal("newScheduler(nope): expected error, got nil")
	}
}

func TestNewObjectStore(t *testing.T) {
	t.Parallel()
	if s, err := newObjectStore(config.StorageConfig{Driver: "local", Root: t.TempDir()}); err != nil || s == nil {
		t.Fatalf("newObjectStore(local) = %v, %v", s, err)
	}
	s, err := newObjectStore(config.StorageConfig{
		Driver: "nexus", BaseURL: "https://nexus.example", Repo: "raw", Username: "u", Password: "p",
	})
	if err != nil || s == nil {
		t.Fatalf("newObjectStore(nexus) = %v, %v", s, err)
	}
	if got := s.URL("scenario/1/a.jmx"); got != "https://nexus.example/repository/raw/scenario/1/a.jmx" {
		t.Fatalf("nexus URL = %q", got)
	}
	if _, err := newObjectStore(config.StorageConfig{Driver: "s3"}); err == nil {
		t.Fatal("newObjectStore(s3): expected error, got nil")
	}
}

func TestSetupLogging_AllVariants(t *testing.T) {
	// Not parallel: mutates the global slog default logger.
	for _, c := range []config.LogConfig{
		{Level: "debug", Format: "text"},
		{Level: "info", Format: "json"},
		{Level: "warn", Format: "json"},
		{Level: "error", Format: "text"},
		{Level: "unknown", Format: "unknown"},
	} {
		setupLogging(c)
	}
}

func TestRun_ConfigError(t *testing.T) {
	t.Parallel()
	env := map[string]string{"HONRYU_HTTP_PORT": "not-a-number"}
	if err := run(context.Background(), func(k string) string { return env[k] }); err == nil {
		t.Fatal("run with invalid config: expected error, got nil")
	}
}

// A valid config that fails at wiring (rather than at Load/validate) --
// here, a well-formed mysql driver selection with an unreachable DSN --
// covers run()'s own newRepository error-return branch.
func TestRun_NewRepositoryError(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"HONRYU_DB_DRIVER": "mysql",
		"HONRYU_DB_DSN":    "honryu:secret@tcp(127.0.0.1:1)/honryu?parseTime=true",
	}
	if err := run(context.Background(), func(k string) string { return env[k] }); err == nil {
		t.Fatal("run with an unreachable mysql DSN: expected error, got nil")
	}
}

func TestRun_ObjectStoreError(t *testing.T) {
	t.Parallel()
	env := map[string]string{"HONRYU_DB_DRIVER": "fake", "HONRYU_STORAGE_DRIVER": "s3"}
	if err := run(context.Background(), func(k string) string { return env[k] }); err == nil {
		t.Fatal("run with an unsupported storage driver: expected error, got nil")
	}
}

func TestRun_SchedulerError(t *testing.T) {
	t.Parallel()
	env := map[string]string{"HONRYU_DB_DRIVER": "fake", "HONRYU_SCHEDULER": "nope"}
	if err := run(context.Background(), func(k string) string { return env[k] }); err == nil {
		t.Fatal("run with an unsupported scheduler: expected error, got nil")
	}
}

func TestNewRepository_MySQL_BadDSN(t *testing.T) {
	t.Parallel()
	// A DSN missing the slash separating the database name is rejected at
	// Open, before any connection is attempted.
	if _, err := newRepository(config.DBConfig{Driver: "mysql", DSN: "honryu:secret"}, "default"); err == nil {
		t.Fatal("newRepository(mysql, malformed DSN): expected error, got nil")
	}
}

func TestRun_TicksUntilContextCancelled(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"HONRYU_DB_DRIVER":               "fake",
		"HONRYU_LOG_FORMAT":              "text",
		"HONRYU_SCHEDULER_TICK_INTERVAL": "10ms",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- run(ctx, func(k string) string { return env[k] }) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return within 5s of its context being cancelled")
	}
}

// HostInScheduler wires and starts a third background loop alongside the
// horizon and drain loops -- proves that wiring doesn't panic or hang the
// WaitGroup shutdown, not any particular job getting advanced (run() builds
// its own fresh in-memory store internally, unreachable from the test).
func TestRun_HostsCalibratorLoopWhenConfigured(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"HONRYU_DB_DRIVER":                    "fake",
		"HONRYU_LOG_FORMAT":                   "text",
		"HONRYU_SCHEDULER_TICK_INTERVAL":      "10ms",
		"HONRYU_CALIBRATOR_TICK_INTERVAL":     "10ms",
		"HONRYU_CALIBRATOR_HOST_IN_SCHEDULER": "true",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- run(ctx, func(k string) string { return env[k] }) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return within 5s of its context being cancelled")
	}
}

// seedDueExecution creates a project, scenario (with a test file), execution,
// and load profile via the fake store, plus a tenant quota ceiling, and
// returns the execution id -- everything fireOnce's Deploy+Trigger call
// needs to actually succeed.
func seedDueExecution(t *testing.T, store *fake.Store, obj *fake.ObjectStore, tenantID int64, engines int) int64 {
	t.Helper()
	ctx := context.Background()
	p, _ := project.New("web", "honryu", "")
	projectID, err := store.CreateProject(ctx, p)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	pl, _ := scenario.NewNative("scenario", projectID, taurus.ExecutorJMeter)
	scenarioID, err := store.CreateScenario(ctx, pl)
	if err != nil {
		t.Fatalf("CreateScenario: %v", err)
	}
	if err := store.AddScenarioFile(ctx, scenarioID, "test.jmx", true); err != nil {
		t.Fatalf("AddScenarioFile: %v", err)
	}
	if err := obj.Upload(ctx, fmt.Sprintf("scenario/%d/test.jmx", scenarioID), strings.NewReader("<jmx/>")); err != nil {
		t.Fatalf("upload test file: %v", err)
	}

	e, _ := execution.New("peak", projectID)
	e.TenantID = &tenantID
	executionID, err := store.CreateExecution(ctx, e)
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
	entries := []loadprofile.Entry{{ScenarioID: scenarioID, Concurrency: 10, Engines: engines, Duration: 30}}
	if err := store.StoreLoadProfile(ctx, executionID, false, entries); err != nil {
		t.Fatalf("StoreLoadProfile: %v", err)
	}
	if err := store.SetCeiling(ctx, tenantID, "", engines); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}
	return executionID
}

// newTestServices wires scheduleapp/lifecycleapp against fakes. now pins the
// clock scheduleapp.Create measures its admission horizon from -- a fire
// time equal to now is included in that horizon (the boundary is inclusive),
// and since real wall-clock time only advances from here, by the time
// fireOnce runs (which claims against the real clock) that same fire time is
// already due.
func newTestServices(store *fake.Store, obj *fake.ObjectStore, now time.Time) (*scheduleapp.Service, *lifecycleapp.Service, *fake.Scheduler) {
	sched := fake.NewScheduler()
	quota := quotaapp.NewService(store).WithNow(func() time.Time { return now })
	lifecycle := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage("honryu/jmeter:latest")).WithQuota(quota)
	// Matches run()'s own wiring order exactly: retrofitted after lifecycle
	// exists, since lifecycle itself depends on quota.
	quota.WithStopper(lifecycle)
	schedules := scheduleapp.NewService(store, quota).WithNow(func() time.Time { return now })
	return schedules, lifecycle, sched
}

func TestFireOnce_NothingDueIsANoOp(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	schedules, lifecycle, sched := newTestServices(store, fake.NewObjectStore(), time.Now())

	fireOnce(context.Background(), schedules, lifecycle)

	deployed, _ := sched.DeployedExecutions(context.Background(), "")
	if len(deployed) != 0 {
		t.Fatalf("deployed executions = %v, want none -- nothing was due", deployed)
	}
}

// The end-to-end path fireOnce exists for: claim, deploy, trigger, and the
// occurrence's hold reservation released in favor of Trigger's own live one.
func TestFireOnce_DeploysAndTriggersTheDueExecution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	const tenantID = int64(7)
	executionID := seedDueExecution(t, store, obj, tenantID, 2)
	now := time.Now()
	schedules, lifecycle, sched := newTestServices(store, obj, now)

	fireAt := now // included in the horizon, and already due by the time fireOnce runs
	view, err := schedules.Create(ctx, schedule.Schedule{
		ExecutionID: executionID, TenantID: tenantID, Kind: schedule.KindOneShot, FireAt: &fireAt, Active: true,
	})
	if err != nil {
		t.Fatalf("Create schedule: %v", err)
	}
	if view.Occurrences[0].Status != "reserved" {
		t.Fatalf("occurrence status = %v, want reserved", view.Occurrences[0].Status)
	}

	fireOnce(ctx, schedules, lifecycle)

	deployed, _ := sched.DeployedExecutions(ctx, "")
	if _, ok := deployed[executionID]; !ok {
		t.Fatalf("execution %d was not deployed", executionID)
	}
	if _, running, _ := store.CurrentRun(ctx, executionID); !running {
		t.Fatal("execution was not triggered -- no active run after fireOnce")
	}

	occs, err := store.OccurrencesForSchedule(ctx, view.Schedule.ID)
	if err != nil {
		t.Fatalf("OccurrencesForSchedule: %v", err)
	}
	if len(occs) != 1 || occs[0].Status != "fired" {
		t.Fatalf("occurrence after fireOnce = %+v, want status fired", occs)
	}

	// A second tick must not fire it again: the claim already consumed it.
	before, _ := sched.DeployedExecutions(ctx, "")
	fireOnce(ctx, schedules, lifecycle)
	after, _ := sched.DeployedExecutions(ctx, "")
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("a second fireOnce changed deployed executions: before=%v after=%v", before, after)
	}
}

// Firing re-checks quota at the moment it actually happens (spec: "re-checked
// again when that time actually arrives, catching drift from other activity
// in between"): if capacity the occurrence held at creation time is gone by
// fire time, Deploy still succeeds (it does not check quota) but Trigger's
// own live Reserve call rejects, and fireOnce must log that and return
// without panicking rather than leaving the execution half-started.
func TestFireOnce_TriggerReRejectsWhenCapacityDrainedSinceCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	const tenantID = int64(7)
	executionID := seedDueExecution(t, store, obj, tenantID, 2) // ceiling 2, wants 2
	now := time.Now()
	schedules, lifecycle, sched := newTestServices(store, obj, now)

	fireAt := now
	if _, err := schedules.Create(ctx, schedule.Schedule{
		ExecutionID: executionID, TenantID: tenantID, Kind: schedule.KindOneShot, FireAt: &fireAt, Active: true,
	}); err != nil {
		t.Fatalf("Create schedule: %v", err)
	}
	// Capacity dries up between schedule creation and its fire time.
	if err := store.SetCeiling(ctx, tenantID, "", 0); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}

	fireOnce(ctx, schedules, lifecycle) // must not panic

	deployed, _ := sched.DeployedExecutions(ctx, "")
	if _, ok := deployed[executionID]; !ok {
		t.Fatal("execution was not deployed -- Deploy does not check quota and must still have succeeded")
	}
	if _, running, _ := store.CurrentRun(ctx, executionID); running {
		t.Fatal("execution shows running despite Trigger's own quota re-check rejecting it")
	}
}

// End to end: a tenant's earlier execution has overrun its declared duration
// (reservation end passed, run never stopped) and is the only thing standing
// between a newly due occurrence and admission. fireOnce's call into
// lifecycle.Trigger reclaims that capacity (quota.WithStopper(lifecycle),
// wired exactly as run() wires it) by stopping the overrunning execution,
// then admits and fires the due one.
func TestFireOnce_ReclaimsOverrunCapacityFromTheSameTenantToFireADueOccurrence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	const tenantID = int64(7)
	now := time.Now()

	overrunExecutionID := seedDueExecution(t, store, obj, tenantID, 2)
	if err := store.SetCeiling(ctx, tenantID, "", 2); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}
	if _, err := store.CreateReservation(ctx, reservation.Reservation{
		TenantID: tenantID, Cluster: "", EngineCount: 2,
		Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour), ExecutionID: overrunExecutionID,
	}); err != nil {
		t.Fatalf("CreateReservation (overrun): %v", err)
	}
	if _, err := store.StartRun(ctx, overrunExecutionID, ""); err != nil {
		t.Fatalf("StartRun (overrun): %v", err)
	}

	dueExecutionID := seedDueExecution(t, store, obj, tenantID, 2)
	schedules, lifecycle, sched := newTestServices(store, obj, now)
	fireAt := now
	if _, err := schedules.Create(ctx, schedule.Schedule{
		ExecutionID: dueExecutionID, TenantID: tenantID, Kind: schedule.KindOneShot, FireAt: &fireAt, Active: true,
	}); err != nil {
		t.Fatalf("Create schedule: %v", err)
	}

	fireOnce(ctx, schedules, lifecycle)

	deployed, _ := sched.DeployedExecutions(ctx, "")
	if _, ok := deployed[dueExecutionID]; !ok {
		t.Fatal("the due execution was not deployed")
	}
	if _, running, _ := store.CurrentRun(ctx, dueExecutionID); !running {
		t.Fatal("the due execution was not triggered -- reclaim should have freed enough capacity to admit it")
	}
	if _, running, _ := store.CurrentRun(ctx, overrunExecutionID); running {
		t.Fatal("the overrunning execution is still running -- it should have been reclaimed (stopped)")
	}
}

// A deploy/trigger failure (here: no test file, so Deploy fails ErrNoTestFile)
// must not panic or block the loop -- it is logged and the tick moves on.
func TestFireOnce_DeployFailureDoesNotPanic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	tenantID := int64(7)

	p, _ := project.New("web", "honryu", "")
	projectID, _ := store.CreateProject(ctx, p)
	pl, _ := scenario.NewNative("scenario", projectID, taurus.ExecutorJMeter) // no test file uploaded
	scenarioID, _ := store.CreateScenario(ctx, pl)
	e, _ := execution.New("peak", projectID)
	e.TenantID = &tenantID
	executionID, _ := store.CreateExecution(ctx, e)
	if err := store.StoreLoadProfile(ctx, executionID, false, []loadprofile.Entry{
		{ScenarioID: scenarioID, Concurrency: 10, Engines: 1, Duration: 30},
	}); err != nil {
		t.Fatalf("StoreLoadProfile: %v", err)
	}
	if err := store.SetCeiling(ctx, tenantID, "", 1); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}

	now := time.Now()
	schedules, lifecycle, _ := newTestServices(store, fake.NewObjectStore(), now)
	fireAt := now
	if _, err := schedules.Create(ctx, schedule.Schedule{
		ExecutionID: executionID, TenantID: tenantID, Kind: schedule.KindOneShot, FireAt: &fireAt, Active: true,
	}); err != nil {
		t.Fatalf("Create schedule: %v", err)
	}

	fireOnce(ctx, schedules, lifecycle) // must not panic

	if _, running, _ := store.CurrentRun(ctx, executionID); running {
		t.Fatal("execution shows running despite Deploy failing (no test file)")
	}
}

// Live finding, phase 16 task 170: fireOnce's first live fire against a real
// cluster failed with "engines are not deployed" -- Deploy returning success
// only means the StatefulSet was accepted, not that the pod is scheduled,
// image-pulled, and running, and fireOnce called Trigger immediately after
// with no wait at all. calibrationapp (triggerWhenReady, step.go) and the
// HTTP trigger handler (phase 11 task 124) both already carry this exact
// bounded wait; cmd/scheduler never got the same fix because honryu-scheduler
// had never been deployed anywhere until this phase, so nothing had hit the
// race in practice. Real cluster proof: both engine and sidecar images were
// already cached (no pull needed) and Trigger still failed on the first
// attempt -- this is not a rare edge case, it is the general case, since two
// sequential Go calls will essentially always outrun pod scheduling.
func TestTriggerWhenReady_RetriesOnlyTheReadinessRaceThenSucceeds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	const tenantID = int64(7)
	executionID := seedDueExecution(t, store, obj, tenantID, 2)
	now := time.Now()
	_, lifecycle, sched := newTestServices(store, obj, now)

	if err := lifecycle.Deploy(ctx, executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	// A just-deployed pod's startup, as a real cluster shows it (the fake's
	// own doc comment) -- the first 2 readiness checks report not-yet-ready.
	sched.NotReadyCalls = 2

	if err := triggerWhenReady(ctx, lifecycle, executionID, time.Millisecond, 50*time.Millisecond); err != nil {
		t.Fatalf("triggerWhenReady: %v, want it to retry past the readiness race", err)
	}
	if _, running, _ := store.CurrentRun(ctx, executionID); !running {
		t.Fatal("execution not running after triggerWhenReady succeeded")
	}
}

func TestTriggerWhenReady_TimesOutRatherThanRetryingForever(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	const tenantID = int64(7)
	executionID := seedDueExecution(t, store, obj, tenantID, 2)
	now := time.Now()
	_, lifecycle, sched := newTestServices(store, obj, now)

	if err := lifecycle.Deploy(ctx, executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	sched.NotReadyCalls = 1000 // never becomes ready within the bound below

	err := triggerWhenReady(ctx, lifecycle, executionID, time.Millisecond, 20*time.Millisecond)
	if !errors.Is(err, runpkg.ErrEnginesNotReady) && !errors.Is(err, runpkg.ErrNotDeployed) {
		t.Fatalf("triggerWhenReady(never ready) = %v, want a readiness error surfaced at the deadline", err)
	}
}

// Only the readiness-class conflicts are worth waiting out -- everything
// else (already running, frozen campaign, quota, compile errors) must
// return on the first attempt, exactly as a bare Trigger call already did.
func TestTriggerWhenReady_NonReadinessErrorReturnsImmediately(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	const tenantID = int64(7)
	executionID := seedDueExecution(t, store, obj, tenantID, 2)
	now := time.Now()
	_, lifecycle, _ := newTestServices(store, obj, now)
	// Deliberately not deployed and never will be within the tiny timeout --
	// but the point is this returns on the FIRST attempt if the error is
	// something other than the readiness pair. Not applicable here since
	// ErrNotDeployed IS a readiness error; instead prove the fast path with
	// a large timeout and a tiny elapsed-time budget: a non-readiness error
	// (already running) must not consume any retry time at all.
	if err := lifecycle.Deploy(ctx, executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := lifecycle.Trigger(ctx, executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	start := time.Now()
	err := triggerWhenReady(ctx, lifecycle, executionID, time.Second, time.Minute)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("triggerWhenReady(already running) = nil, want the already-running error")
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("triggerWhenReady took %v for a non-readiness error, want an immediate return with no retry", elapsed)
	}
}

func TestRunLoop_FiresOnEachTickUntilCancelled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	const tenantID = int64(7)
	executionID := seedDueExecution(t, store, obj, tenantID, 2)
	now := time.Now()
	schedules, lifecycle, sched := newTestServices(store, obj, now)

	fireAt := now
	if _, err := schedules.Create(ctx, schedule.Schedule{
		ExecutionID: executionID, TenantID: tenantID, Kind: schedule.KindOneShot, FireAt: &fireAt, Active: true,
	}); err != nil {
		t.Fatalf("Create schedule: %v", err)
	}

	loopCtx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel()
	runLoop(loopCtx, schedules, lifecycle, 10*time.Millisecond)

	deployed, _ := sched.DeployedExecutions(ctx, "")
	if _, ok := deployed[executionID]; !ok {
		t.Fatal("runLoop never fired the due occurrence within its ticking window")
	}
}

func TestExtendHorizonsOnce_RecordsCompletion(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	schedules, _, _ := newTestServices(store, fake.NewObjectStore(), time.Now())

	extendHorizonsOnce(context.Background(), schedules)

	if _, found, err := schedules.LastHorizonExtension(context.Background()); err != nil || !found {
		t.Fatalf("LastHorizonExtension after extendHorizonsOnce = found:%v, err:%v, want found:true", found, err)
	}
}

// runHorizonLoop runs its pass immediately on startup, before ever checking
// ctx -- even a context cancelled before the call still gets one pass, since
// a schedule whose horizon drifted below 7 days while cmd/scheduler was down
// must be caught up right away, not left to wait for the first tick.
func TestRunHorizonLoop_RunsImmediatelyBeforeCheckingContext(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	schedules, _, _ := newTestServices(store, fake.NewObjectStore(), time.Now())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runHorizonLoop(ctx, schedules, time.Hour)

	if _, found, err := schedules.LastHorizonExtension(context.Background()); err != nil || !found {
		t.Fatalf("LastHorizonExtension after runHorizonLoop = found:%v, err:%v, want found:true (its immediate pass must still run)", found, err)
	}
}

// A schedule whose execution lost its load profile fails to extend, but
// extendHorizonsOnce must not panic on that -- it logs and moves on, and the
// pass still records its own completion (scheduleapp.ExtendHorizons does
// that even for a partial failure).
func TestExtendHorizonsOnce_ScheduleErrorDoesNotPanic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	const tenantID = int64(7)
	executionID := seedDueExecution(t, store, obj, tenantID, 2)
	now := time.Now()
	schedules, _, _ := newTestServices(store, obj, now)

	if _, err := schedules.Create(ctx, schedule.Schedule{
		ExecutionID: executionID, TenantID: tenantID, Kind: schedule.KindRecurring, Recurrence: "0 0 * * *", Active: true,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.StoreLoadProfile(ctx, executionID, false, nil); err != nil {
		t.Fatalf("StoreLoadProfile (clear): %v", err)
	}

	extendHorizonsOnce(ctx, schedules) // must not panic

	if _, found, err := schedules.LastHorizonExtension(ctx); err != nil || !found {
		t.Fatalf("LastHorizonExtension after a partial failure = found:%v, err:%v, want found:true", found, err)
	}
}

func TestRunHorizonLoop_TicksMoreThanOnceUntilCancelled(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	schedules, _, _ := newTestServices(store, fake.NewObjectStore(), time.Now())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	runHorizonLoop(ctx, schedules, 10*time.Millisecond) // must return once ctx is done, without panicking
}

// drainProject seeds a project with two executions: designated (the
// campaign's readiness test) and stray (any other execution under the same
// project). Both are returned undeployed; the caller deploys/runs whichever
// it needs for its scenario.
func drainProject(t *testing.T, store *fake.Store) (projectID, designated, stray int64) {
	t.Helper()
	ctx := context.Background()
	p, err := project.New("web", "honryu", "")
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	// Tenant 7, matching every campaign these drain tests create --
	// campaignapp.Create now requires a participating project to actually
	// belong to the campaign's own tenant.
	tenantID := int64(7)
	p.TenantID = &tenantID
	projectID, err = store.CreateProject(ctx, p)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	de, err := execution.New("readiness", projectID)
	if err != nil {
		t.Fatalf("execution.New (designated): %v", err)
	}
	designated, err = store.CreateExecution(ctx, de)
	if err != nil {
		t.Fatalf("CreateExecution (designated): %v", err)
	}
	se, err := execution.New("stray", projectID)
	if err != nil {
		t.Fatalf("execution.New (stray): %v", err)
	}
	stray, err = store.CreateExecution(ctx, se)
	if err != nil {
		t.Fatalf("CreateExecution (stray): %v", err)
	}
	return projectID, designated, stray
}

func deployAndRun(t *testing.T, store *fake.Store, sched *fake.Scheduler, executionID int64) {
	t.Helper()
	ctx := context.Background()
	if err := sched.DeployScenario(ctx, ports.DeploySpec{ExecutionID: executionID, ScenarioID: 1}); err != nil {
		t.Fatalf("DeployScenario: %v", err)
	}
	if _, err := store.StartRun(ctx, executionID, ""); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
}

func TestDrainOnce_StopsARunningNonCompliantExecutionUnderAParticipatingProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	sched := fake.NewScheduler()
	obj := fake.NewObjectStore()
	campaigns := campaignapp.NewService(store, sched)
	lifecycle := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage("honryu/jmeter:latest"))

	projectID, designated, stray := drainProject(t, store)
	deployAndRun(t, store, sched, stray)

	if _, err := campaigns.Create(ctx, campaign.Campaign{
		Name: "c", TenantID: 7, Window: campaign.Window{Start: time.Now().Add(-time.Hour), End: time.Now().Add(time.Hour)},
		Services: []campaign.Service{{ProjectID: projectID, ExecutionID: designated}},
	}); err != nil {
		t.Fatalf("Create campaign: %v", err)
	}

	drainOnce(ctx, campaigns, lifecycle, store)

	if _, running, _ := store.CurrentRun(ctx, stray); running {
		t.Fatal("the non-compliant execution should have been stopped by the drain sweep")
	}
}

// The whole point of freeze exempting the designated execution: draining
// must leave it running, not tear down the readiness test it exists to
// measure.
func TestDrainOnce_LeavesTheDesignatedExecutionRunning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	sched := fake.NewScheduler()
	obj := fake.NewObjectStore()
	campaigns := campaignapp.NewService(store, sched)
	lifecycle := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage("honryu/jmeter:latest"))

	projectID, designated, _ := drainProject(t, store)
	deployAndRun(t, store, sched, designated)

	if _, err := campaigns.Create(ctx, campaign.Campaign{
		Name: "c", TenantID: 7, Window: campaign.Window{Start: time.Now().Add(-time.Hour), End: time.Now().Add(time.Hour)},
		Services: []campaign.Service{{ProjectID: projectID, ExecutionID: designated}},
	}); err != nil {
		t.Fatalf("Create campaign: %v", err)
	}

	drainOnce(ctx, campaigns, lifecycle, store)

	if _, running, _ := store.CurrentRun(ctx, designated); !running {
		t.Fatal("the designated execution must be left running -- it is exempt from freeze")
	}
}

// A deployed-but-idle in-scope execution has nothing to drain -- Stop would
// reject it as not running, so drainOnce must not even attempt it (which
// would otherwise show up as a spurious error on every tick).
func TestDrainOnce_LeavesADeployedButIdleExecutionAlone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	sched := fake.NewScheduler()
	obj := fake.NewObjectStore()
	campaigns := campaignapp.NewService(store, sched)
	lifecycle := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage("honryu/jmeter:latest"))

	projectID, designated, stray := drainProject(t, store)
	if err := sched.DeployScenario(ctx, ports.DeploySpec{ExecutionID: stray, ScenarioID: 1}); err != nil {
		t.Fatalf("DeployScenario: %v", err)
	} // deployed, but never started

	if _, err := campaigns.Create(ctx, campaign.Campaign{
		Name: "c", TenantID: 7, Window: campaign.Window{Start: time.Now().Add(-time.Hour), End: time.Now().Add(time.Hour)},
		Services: []campaign.Service{{ProjectID: projectID, ExecutionID: designated}},
	}); err != nil {
		t.Fatalf("Create campaign: %v", err)
	}

	drainOnce(ctx, campaigns, lifecycle, store) // must not panic or error visibly

	if _, running, _ := store.CurrentRun(ctx, stray); running {
		t.Fatal("an idle execution cannot have started running on its own")
	}
}

func TestDrainOnce_NoActiveCampaignsIsANoOp(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	sched := fake.NewScheduler()
	obj := fake.NewObjectStore()
	campaigns := campaignapp.NewService(store, sched)
	lifecycle := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage("honryu/jmeter:latest"))

	drainOnce(context.Background(), campaigns, lifecycle, store) // must not panic
}

func TestRunDrainLoop_TicksMoreThanOnceUntilCancelled(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	sched := fake.NewScheduler()
	obj := fake.NewObjectStore()
	campaigns := campaignapp.NewService(store, sched)
	lifecycle := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage("honryu/jmeter:latest"))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	runDrainLoop(ctx, campaigns, lifecycle, store, 10*time.Millisecond) // must return once ctx is done, without panicking
}

// stubRunner is a scripted calibrationapp.Runner, mirroring cmd/calibrator's
// own test double -- duplicated rather than imported, since the two binaries
// are separate package mains that cannot share test helpers.
type stubRunner struct {
	responses []report.Report
	calls     int
}

func (r *stubRunner) RunStep(context.Context, int64, float64, int, float64) (report.Report, error) {
	if r.calls >= len(r.responses) {
		return report.Report{}, errors.New("stubRunner: no more scripted responses")
	}
	resp := r.responses[r.calls]
	r.calls++
	return resp, nil
}

type stubFingerprinter struct{}

func (stubFingerprinter) ScenarioFingerprint(context.Context, int64) (string, error) {
	return "fp", nil
}

// targetSaturatedReport reports the engine keeping up while the overall error
// rate trips a "failures>5%" criterion -- a single-tick terminal outcome,
// enough to prove the hosted loop actually advances a real job.
func targetSaturatedReport(requestedQPS float64) report.Report {
	return report.Report{
		Requested:   report.Load{Throughput: requestedQPS},
		Achieved:    report.Load{Throughput: requestedQPS, Samples: 1000},
		ErrorRate:   0.10,
		Attribution: report.Attribution{Target: 100},
	}
}

// A service not configured for advancing (no runner) reports an operational
// error; advanceCalibrationOnce must log it and return rather than panic --
// the next tick moves on regardless.
func TestAdvanceCalibrationOnce_ErrorIsLoggedNotPropagated(t *testing.T) {
	t.Parallel()
	advanceCalibrationOnce(context.Background(), calibrationapp.NewService(fake.NewStore()))
}

// seedBoundSource creates the ordinary execution Create copies its
// scenario binding from: a native JMeter scenario and a source execution
// whose load profile runs it.
func seedBoundSource(ctx context.Context, store *fake.Store, projectID int64) (scenarioID, sourceID int64, err error) {
	pl, err := scenario.NewNative("target", projectID, taurus.ExecutorJMeter)
	if err != nil {
		return 0, 0, err
	}
	scenarioID, err = store.CreateScenario(ctx, pl)
	if err != nil {
		return 0, 0, err
	}
	exe, err := execution.New("source", projectID)
	if err != nil {
		return 0, 0, err
	}
	sourceID, err = store.CreateExecution(ctx, exe)
	if err != nil {
		return 0, 0, err
	}
	err = store.StoreLoadProfile(ctx, sourceID, false, []loadprofile.Entry{{ScenarioID: scenarioID, Engines: 1, Concurrency: 1, Duration: 1}})
	return scenarioID, sourceID, err
}

// The hosted loop's reason to exist: one due job claimed, driven through its
// single terminal step, and left done.
func TestAdvanceCalibrationOnce_AdvancesADueJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()

	p, _ := project.New("web", "honryu", "")
	projectID, err := store.CreateProject(ctx, p)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 10, MaxQPS: 1000, MaxSteps: 5, HoldSeconds: 1}
	setup := calibrationapp.NewService(store)
	scenarioID, src, err := seedBoundSource(ctx, store, projectID)
	if err != nil {
		t.Fatalf("seedBoundSource: %v", err)
	}
	executionID, err := setup.Create(ctx, "calibrate", projectID, taurus.ExecutorJMeter, spec, src, scenarioID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	jobID, err := setup.Trigger(ctx, executionID)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	calibrations := calibrationapp.NewService(store).
		WithRunner(&stubRunner{responses: []report.Report{targetSaturatedReport(10)}}).
		WithFingerprint(stubFingerprinter{})

	advanceCalibrationOnce(ctx, calibrations)

	job, err := calibrations.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.Phase != calibration.PhaseDone {
		t.Fatalf("job.Phase = %q, want done", job.Phase)
	}
}

// sweepClusters must cover the deployment's own default cluster (the empty
// ref) plus every registered one -- an execution pinned to a registered
// cluster has its engines there, and a sweep that never lists that cluster
// never finds its idle pods.
func TestSweepClusters_DefaultPlusEveryRegisteredCluster(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()

	for _, name := range []string{"eu", "us"} {
		c := clusterregistry.Cluster{
			Name: name, Origin: clusterregistry.OriginOperator, SecretRef: "sec",
			Namespace: "honryu", SidecarImage: "sidecar:1", IngestURL: "https://ingest",
		}
		if err := store.CreateCluster(ctx, c); err != nil {
			t.Fatalf("CreateCluster(%s): %v", name, err)
		}
	}

	refs, err := sweepClusters(ctx, store)
	if err != nil {
		t.Fatalf("sweepClusters: %v", err)
	}
	want := []ports.ClusterRef{"", "eu", "us"}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("sweepClusters = %v, want %v", refs, want)
	}
}

// A zero EngineIdleTTL is the disabled state: the loop must return before
// its first tick rather than run a disabled sweep forever.
func TestRunEngineReaperLoop_ZeroTTLReturnsImmediately(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := fake.NewStore()
	sched := fake.NewScheduler()
	obj := fake.NewObjectStore()
	lifecycle := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage("honryu/jmeter:latest"))

	done := make(chan struct{})
	go func() { runEngineReaperLoop(ctx, lifecycle, store, config.Config{}); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runEngineReaperLoop with zero TTL did not return")
	}
}

// One reap pass through the loop's own entry point: an execution whose pods
// were deployed (never activity-stamped) beyond the TTL is torn down and its
// id handed back for the loop to log.
func TestReapIdleOnce_ReapsIdleEngines(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	sched := fake.NewScheduler()
	obj := fake.NewObjectStore()
	lifecycle := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage("honryu/jmeter:latest"))

	p, _ := project.New("web", "honryu", "")
	projectID, _ := store.CreateProject(ctx, p)
	coll, _ := execution.New("peak", projectID)
	executionID, _ := store.CreateExecution(ctx, coll)
	// Pods deployed an hour and a half ago, straight through the scheduler:
	// never activity-stamped, so the deploy-time fallback is the clock.
	sched.Now = func() time.Time { return time.Now().Add(-90 * time.Minute) }
	if err := sched.DeployScenario(ctx, ports.DeploySpec{
		ProjectID: projectID, ExecutionID: executionID, Image: "honryu/jmeter:latest",
		Shards: []ports.ShardSpec{{Index: 0, Config: []byte("cfg")}},
	}); err != nil {
		t.Fatalf("DeployScenario: %v", err)
	}

	reapIdleOnce(ctx, lifecycle, store, time.Hour)

	if deployed, _ := sched.DeployedExecutions(ctx, ""); len(deployed) != 0 {
		t.Fatalf("deployed after reap = %v, want none", deployed)
	}
	if _, err := store.GetExecution(ctx, executionID); err != nil {
		t.Fatalf("GetExecution after reap: %v (records must survive)", err)
	}
}
