package calibrationapp_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/calibrationapp"
	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/run"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

const stepImage = "honryu/jmeter:latest"

// scriptedMetrics implements lifecycleapp.Metrics by writing a fixed report
// template (execution/run id filled in) to a report store on Finalize --
// standing in for the real metricsapp pipeline, whose settled numbers a step
// only cares about reading back correctly.
type scriptedMetrics struct {
	reports  *fake.ReportStore
	template report.Report
}

func (m *scriptedMetrics) Purge(int64) {}

func (m *scriptedMetrics) FinalizeOrphaned(ctx context.Context, executionID, runID int64, _ []ports.OrphanCompletion) error {
	return m.Finalize(ctx, executionID, runID)
}

func (m *scriptedMetrics) Finalize(ctx context.Context, executionID, runID int64) error {
	r := m.template
	r.ExecutionID = executionID
	r.RunID = runID
	return m.reports.SaveReport(ctx, r)
}

// selfCompletingLifecycle drives a real lifecycleapp.Service but, the moment
// Trigger succeeds, also finalizes the run's report itself -- standing in
// for metricsapp's own natural-completion path (Ingest finalizes a run once
// every shard reports itself finished, independent of and typically well
// before any explicit Stop), which awaitReport polls for. Without this, the
// fake environment has no mechanism at all for a run to self-complete: only
// Stop ever finalizes, so a step run before Stop would never find a report
// and would poll for its whole stepReportTimeout.
type selfCompletingLifecycle struct {
	*lifecycleapp.Service
	store   *fake.Store
	metrics *scriptedMetrics
}

func (l *selfCompletingLifecycle) Trigger(ctx context.Context, executionID int64) error {
	if err := l.Service.Trigger(ctx, executionID); err != nil {
		return err
	}
	runID, running, err := l.store.CurrentRun(ctx, executionID)
	if err != nil || !running {
		return err
	}
	return l.metrics.Finalize(ctx, executionID, runID)
}

// fakeClock is a manually-advanced time source for deterministic tests
// against RunStep's bounded polling loops. Paired with a sleep hook that
// calls Advance, a loop whose deadline is measured against Now reaches that
// deadline after a bounded number of iterations instead of the real timeout
// duration -- critical since a fake sleep never actually blocks, so a loop
// measured against the real wall clock would spin as fast as the CPU
// allows for however many iterations the real timeout takes to elapse.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Now()} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// maxTestSleeps bounds how many times a test's sleep hook may fire before
// failing loudly -- a guard against a future change reopening the spin this
// fake clock exists to close, so a regression fails fast instead of OOMing.
const maxTestSleeps = 1000

// stepEnv wires a StepRunner against a real lifecycleapp.Service (itself
// wired to fakes), so RunStep's Deploy/Trigger/Stop calls exercise the
// genuine lifecycle state machine, not a hand-rolled stand-in.
type stepEnv struct {
	store   *fake.Store
	sched   *fake.Scheduler
	obj     *fake.ObjectStore
	reports *fake.ReportStore
	runner  *calibrationapp.StepRunner

	executionID int64
	scenarioID  int64
	slept       []time.Duration
}

// setupStep seeds a project, a CalibrateEngine execution, and (unless
// noScenario) a single bound scenario with a JMX test file and an initial
// (pre-calibration) profile entry -- the "ordinary config upload" a
// calibration execution's scenario is bound by, before its own step-runner
// ever rewrites the QPS-varying fields.
func setupStep(t *testing.T, configureScenario bool) *stepEnv {
	t.Helper()
	ctx := context.Background()
	store := fake.NewStore()
	projectID := seedProject(t, store)

	exe, err := execution.New("calibrate", projectID)
	if err != nil {
		t.Fatalf("execution.New: %v", err)
	}
	exe.Kind = execution.KindCalibrateEngine
	exe.CPU, exe.Memory = "500m", "512Mi"
	executionID, err := store.CreateExecution(ctx, exe)
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}

	obj := fake.NewObjectStore()
	var scenarioID int64
	if configureScenario {
		pl, _ := scenario.NewNative("target", projectID, taurus.ExecutorJMeter)
		scenarioID, err = store.CreateScenario(ctx, pl)
		if err != nil {
			t.Fatalf("CreateScenario: %v", err)
		}
		if err := store.AddScenarioFile(ctx, scenarioID, "test.jmx", true); err != nil {
			t.Fatalf("AddScenarioFile: %v", err)
		}
		if err := obj.Upload(ctx, fmt.Sprintf("scenario/%d/test.jmx", scenarioID), strings.NewReader("<jmx/>")); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		base := []loadprofile.Entry{{
			Name: "calibration-target", ScenarioID: scenarioID,
			Engines: 3, Concurrency: 10, Rampup: 1, Duration: 30,
		}}
		if err := store.StoreLoadProfile(ctx, executionID, false, base); err != nil {
			t.Fatalf("StoreLoadProfile (base): %v", err)
		}
	}

	sched := fake.NewScheduler()
	reports := fake.NewReportStore()
	lifecycle := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage(stepImage))
	metrics := &scriptedMetrics{
		reports: reports,
		template: report.Report{
			Outcome:   taurus.OutcomePassed,
			StartedAt: time.Now().Add(-time.Minute),
			EndedAt:   time.Now(),
			Achieved:  report.Load{Throughput: 100},
		},
	}
	lifecycle.WithMetrics(metrics)

	e := &stepEnv{store: store, sched: sched, obj: obj, reports: reports, executionID: executionID, scenarioID: scenarioID}
	clock := newFakeClock()
	selfCompleting := &selfCompletingLifecycle{Service: lifecycle, store: store, metrics: metrics}
	e.runner = calibrationapp.NewStepRunner(store, selfCompleting, reports).WithSleep(func(d time.Duration) {
		e.slept = append(e.slept, d)
		if len(e.slept) > maxTestSleeps {
			t.Fatalf("RunStep slept %d times, want a bounded handful -- a polling loop is spinning unbounded", len(e.slept))
		}
		clock.Advance(d)
	}).WithClock(clock.Now)
	return e
}

func TestRunStep_HappyPath_RewritesProfileDeploysTriggersHoldsStopsAndReturnsReport(t *testing.T) {
	t.Parallel()
	e := setupStep(t, true)
	ctx := context.Background()

	rpt, err := e.runner.RunStep(ctx, e.executionID, 123.4, 17, 0)
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}

	// The profile was rewritten to a single pinned pod at the requested QPS,
	// preserving the scenario/name the ordinary config upload named.
	entries, err := e.store.LoadProfileFor(ctx, e.executionID)
	if err != nil {
		t.Fatalf("LoadProfileFor: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("profile entries = %d, want 1", len(entries))
	}
	got := entries[0]
	// Name is not asserted: the persisted schema never carries a scenario
	// name (fake.Store.StoreLoadProfile drops it too, matching MySQL), for
	// any config upload, calibration or not.
	if got.ScenarioID != e.scenarioID {
		t.Fatalf("entry identity changed: %+v", got)
	}
	if got.Engines != 1 {
		t.Fatalf("Engines = %d, want 1 (pinned single pod)", got.Engines)
	}
	if got.Throughput != 124 { // ceil(123.4)
		t.Fatalf("Throughput = %d, want 124", got.Throughput)
	}
	if got.Duration != 17 {
		t.Fatalf("Duration = %d, want 17 (the hold)", got.Duration)
	}
	if got.Concurrency < 20 {
		t.Fatalf("Concurrency = %d, want a generous floor", got.Concurrency)
	}

	// Deploy actually happened, at the pinned pod size.
	spec, ok := e.sched.LastDeploy(e.executionID, e.scenarioID)
	if !ok {
		t.Fatal("scenario was never deployed")
	}
	if spec.CPU != "500m" || spec.Memory != "512Mi" {
		t.Fatalf("deployed pod size = %q/%q, want 500m/512Mi", spec.CPU, spec.Memory)
	}

	// selfCompletingLifecycle finalizes the report the instant Trigger
	// succeeds, so awaitReport's first poll already finds it -- no sleep is
	// needed at all, matching the fake scheduler's own "everything ready
	// instantly unless a test injects delay" idiom.
	if len(e.slept) != 0 {
		t.Fatalf("slept = %v, want none (report available on first poll)", e.slept)
	}
	// The run was actually stopped (no run left current).
	if _, running, _ := e.store.CurrentRun(ctx, e.executionID); running {
		t.Fatal("run still current after RunStep, want stopped")
	}

	// The settled report scripted by Finalize is what came back.
	if rpt.ExecutionID != e.executionID || rpt.Outcome != taurus.OutcomePassed || rpt.Achieved.Throughput != 100 {
		t.Fatalf("report = %+v, want the scripted settled report", rpt)
	}
}

func TestRunStep_ConcurrencyFloorAppliesForLowQPS(t *testing.T) {
	t.Parallel()
	e := setupStep(t, true)
	ctx := context.Background()

	if _, err := e.runner.RunStep(ctx, e.executionID, 1, 5, 0); err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	entries, err := e.store.LoadProfileFor(ctx, e.executionID)
	if err != nil {
		t.Fatalf("LoadProfileFor: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("profile entries = %d, want 1", len(entries))
	}
	// requestedQPS * stepConcurrencyPerQPS (2) = 2, well under the floor.
	if got := entries[0].Concurrency; got != 20 {
		t.Fatalf("Concurrency = %d, want the floor 20", got)
	}
}

func TestRunStep_ConcurrencySizedByMeasuredLatency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("a measured latency drives Little's-Law sizing", func(t *testing.T) {
		t.Parallel()
		e := setupStep(t, true)
		// 500 QPS at a measured 100ms p95: Little's Law minimum is 50 VUs,
		// times the headroom (3) ~= 150. (A tolerance absorbs the float
		// rounding of ceil(500 * 0.1 * 3).)
		if _, err := e.runner.RunStep(ctx, e.executionID, 500, 5, 0.1); err != nil {
			t.Fatalf("RunStep: %v", err)
		}
		entries, err := e.store.LoadProfileFor(ctx, e.executionID)
		if err != nil {
			t.Fatalf("LoadProfileFor: %v", err)
		}
		if got := entries[0].Concurrency; got < 150 || got > 151 {
			t.Fatalf("Concurrency = %d, want ~150 (500 * 0.1 * 3 headroom)", got)
		}
	})

	t.Run("a fast target collapses to the floor, not thousands of threads", func(t *testing.T) {
		t.Parallel()
		e := setupStep(t, true)
		// 1000 QPS at a 2ms p95: Little's minimum is 2 VUs, so the floor (20)
		// wins.
		if _, err := e.runner.RunStep(ctx, e.executionID, 1000, 5, 0.002); err != nil {
			t.Fatalf("RunStep: %v", err)
		}
		entries, err := e.store.LoadProfileFor(ctx, e.executionID)
		if err != nil {
			t.Fatalf("LoadProfileFor: %v", err)
		}
		if got := entries[0].Concurrency; got != 20 {
			t.Fatalf("Concurrency = %d, want the floor 20 (1000 * 0.002 * 3 = 6, below floor)", got)
		}
	})

	t.Run("the uninformed first attempt is a light probe at the floor", func(t *testing.T) {
		t.Parallel()
		e := setupStep(t, true)
		// 8000 QPS, no measured latency (hint 0): the first attempt is a light
		// probe at the floor, NOT sized to the rate -- so it can never open a
		// storm of connections or exhaust memory. The retry re-sizes from the
		// measured RT.
		if _, err := e.runner.RunStep(ctx, e.executionID, 8000, 5, 0); err != nil {
			t.Fatalf("RunStep: %v", err)
		}
		entries, err := e.store.LoadProfileFor(ctx, e.executionID)
		if err != nil {
			t.Fatalf("LoadProfileFor: %v", err)
		}
		if got := entries[0].Concurrency; got != 20 {
			t.Fatalf("Concurrency = %d, want the floor 20 (a light probe, not 8000-scaled)", got)
		}
	})

	t.Run("a measured slow target gets all the threads Little's Law calls for", func(t *testing.T) {
		t.Parallel()
		e := setupStep(t, true)
		// 8000 QPS at a genuinely slow 1s p95: Little's Law calls for
		// 8000 * 1 * 3 = 24000 VUs, and the measured (retry) path is not capped
		// -- a slow target really does need that many to sustain the rate, and
		// this comes from a real measurement rather than a guess.
		if _, err := e.runner.RunStep(ctx, e.executionID, 8000, 5, 1.0); err != nil {
			t.Fatalf("RunStep: %v", err)
		}
		entries, err := e.store.LoadProfileFor(ctx, e.executionID)
		if err != nil {
			t.Fatalf("LoadProfileFor: %v", err)
		}
		if got := entries[0].Concurrency; got != 24000 {
			t.Fatalf("Concurrency = %d, want 24000 (8000 * 1.0 * 3, from a real measurement)", got)
		}
	})
}

func TestRunStep_RejectsNonPositiveQPS(t *testing.T) {
	t.Parallel()
	for _, qps := range []float64{0, -5} {
		e := setupStep(t, true)
		ctx := context.Background()

		if _, err := e.runner.RunStep(ctx, e.executionID, qps, 30, 0); !errors.Is(err, calibrationapp.ErrRequestedQPSInvalid) {
			t.Fatalf("RunStep(%g) error = %v, want ErrRequestedQPSInvalid", qps, err)
		}
		if deployed, _ := e.sched.DeployedExecutions(ctx, ""); len(deployed) != 0 {
			t.Fatalf("deployed = %v, want none -- an invalid request must fail before any side effect", deployed)
		}
	}
}

func TestRunStep_RequiresExactlyOneConfiguredScenario(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("no scenario configured", func(t *testing.T) {
		t.Parallel()
		e := setupStep(t, false)
		if _, err := e.runner.RunStep(ctx, e.executionID, 10, 30, 0); !errors.Is(err, calibrationapp.ErrScenarioNotConfigured) {
			t.Fatalf("error = %v, want ErrScenarioNotConfigured", err)
		}
	})

	t.Run("two scenarios configured", func(t *testing.T) {
		t.Parallel()
		e := setupStep(t, true)
		second := append([]loadprofile.Entry(nil), loadprofile.Entry{
			Name: "extra", ScenarioID: e.scenarioID + 1, Engines: 1, Concurrency: 1, Duration: 30,
		})
		entries, _ := e.store.LoadProfileFor(ctx, e.executionID)
		entries = append(entries, second...)
		if err := e.store.StoreLoadProfile(ctx, e.executionID, false, entries); err != nil {
			t.Fatalf("StoreLoadProfile: %v", err)
		}
		if _, err := e.runner.RunStep(ctx, e.executionID, 10, 30, 0); !errors.Is(err, calibrationapp.ErrScenarioNotConfigured) {
			t.Fatalf("error = %v, want ErrScenarioNotConfigured", err)
		}
	})
}

// erroringStepRepo wraps a *fake.Store and lets a test force one RunnerRepo
// method to fail, proving RunStep propagates a downstream failure rather
// than swallowing it.
type erroringStepRepo struct {
	*fake.Store
	loadProfileForErr   error
	storeLoadProfileErr error
	currentRunErr       error
}

func (r *erroringStepRepo) LoadProfileFor(ctx context.Context, executionID int64) ([]loadprofile.Entry, error) {
	if r.loadProfileForErr != nil {
		return nil, r.loadProfileForErr
	}
	return r.Store.LoadProfileFor(ctx, executionID)
}

func (r *erroringStepRepo) StoreLoadProfile(ctx context.Context, executionID int64, csvSplit bool, entries []loadprofile.Entry) error {
	if r.storeLoadProfileErr != nil {
		return r.storeLoadProfileErr
	}
	return r.Store.StoreLoadProfile(ctx, executionID, csvSplit, entries)
}

func (r *erroringStepRepo) CurrentRun(ctx context.Context, executionID int64) (int64, bool, error) {
	if r.currentRunErr != nil {
		return 0, false, r.currentRunErr
	}
	return r.Store.CurrentRun(ctx, executionID)
}

func TestRunStep_PropagatesRepoFailures(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("boom")
	ctx := context.Background()

	cases := []struct {
		name string
		wrap func(*erroringStepRepo)
	}{
		{"LoadProfileFor", func(r *erroringStepRepo) { r.loadProfileForErr = sentinel }},
		{"StoreLoadProfile", func(r *erroringStepRepo) { r.storeLoadProfileErr = sentinel }},
		{"CurrentRun", func(r *erroringStepRepo) { r.currentRunErr = sentinel }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := setupStep(t, true)
			errRepo := &erroringStepRepo{Store: e.store}
			tc.wrap(errRepo)

			lifecycle := lifecycleapp.NewService(e.store, e.sched, e.obj, lifecycleapp.StaticImage(stepImage))
			runner := calibrationapp.NewStepRunner(errRepo, lifecycle, e.reports).WithSleep(func(time.Duration) {})

			if _, err := runner.RunStep(ctx, e.executionID, 10, 30, 0); !errors.Is(err, sentinel) {
				t.Fatalf("RunStep error = %v, want sentinel", err)
			}
		})
	}
}

// stubLifecycle lets a test force Deploy/Trigger/Stop to fail independently,
// without driving the real lifecycleapp.Service through every prior stage.
type stubLifecycle struct {
	deployErr, triggerErr, stopErr error
	deployed, triggered, stopped   bool
}

func (l *stubLifecycle) Deploy(context.Context, int64) error {
	l.deployed = true
	return l.deployErr
}

func (l *stubLifecycle) Trigger(context.Context, int64) error {
	l.triggered = true
	return l.triggerErr
}

func (l *stubLifecycle) Stop(context.Context, int64) error {
	l.stopped = true
	return l.stopErr
}

func TestRunStep_PropagatesLifecycleFailures(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("boom")
	ctx := context.Background()

	t.Run("Deploy fails", func(t *testing.T) {
		t.Parallel()
		e := setupStep(t, true)
		lc := &stubLifecycle{deployErr: sentinel}
		runner := calibrationapp.NewStepRunner(e.store, lc, e.reports).WithSleep(func(time.Duration) {})
		if _, err := runner.RunStep(ctx, e.executionID, 10, 30, 0); !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want sentinel", err)
		}
		if lc.triggered {
			t.Fatal("Trigger called after Deploy failed")
		}
	})

	t.Run("Trigger fails", func(t *testing.T) {
		t.Parallel()
		e := setupStep(t, true)
		lc := &stubLifecycle{triggerErr: sentinel}
		runner := calibrationapp.NewStepRunner(e.store, lc, e.reports).WithSleep(func(time.Duration) {})
		if _, err := runner.RunStep(ctx, e.executionID, 10, 30, 0); !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want sentinel", err)
		}
		if lc.stopped {
			t.Fatal("Stop called after Trigger failed")
		}
	})

	t.Run("Stop fails", func(t *testing.T) {
		t.Parallel()
		e := setupStep(t, true)
		// Deploy and Trigger must genuinely run (so CurrentRun reports
		// running=true before Stop is reached) -- only Stop itself fails.
		// No metrics are wired, so the report never settles: awaitReport
		// must run out its full timeout before Stop is even attempted,
		// which is why this needs the fake clock too -- otherwise it spins
		// for the real timeout duration instead of a bounded iteration count.
		lifecycle := lifecycleapp.NewService(e.store, e.sched, e.obj, lifecycleapp.StaticImage(stepImage))
		lc := &stopFailsLifecycle{Service: lifecycle, stopErr: sentinel}
		clock := newFakeClock()
		var slept int
		runner := calibrationapp.NewStepRunner(e.store, lc, e.reports).WithSleep(func(d time.Duration) {
			slept++
			if slept > maxTestSleeps {
				t.Fatalf("RunStep slept %d times, want a bounded handful -- a polling loop is spinning unbounded", slept)
			}
			clock.Advance(d)
		}).WithClock(clock.Now)

		if _, err := runner.RunStep(ctx, e.executionID, 10, 30, 0); !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want sentinel", err)
		}
		if _, running, _ := e.store.CurrentRun(ctx, e.executionID); !running {
			t.Fatal("run no longer current despite Stop having failed")
		}
	})
}

// stopFailsLifecycle drives real Deploy/Trigger through an embedded
// lifecycleapp.Service but forces Stop to fail, proving RunStep propagates a
// Stop failure without pretending the run ended.
type stopFailsLifecycle struct {
	*lifecycleapp.Service
	stopErr error
}

func (l *stopFailsLifecycle) Stop(context.Context, int64) error {
	return l.stopErr
}

// flakyTriggerLifecycle fails Trigger with run.ErrNotDeployed for its first
// failCount calls, then succeeds -- standing in for a real cluster's pod
// still being scheduled, image-pulled, or started, which triggerWhenReady
// must retry rather than surface as a step failure.
type flakyTriggerLifecycle struct {
	failCount, calls int
	stopped          bool
}

func (l *flakyTriggerLifecycle) Deploy(context.Context, int64) error { return nil }

func (l *flakyTriggerLifecycle) Trigger(context.Context, int64) error {
	l.calls++
	if l.calls <= l.failCount {
		return run.ErrNotDeployed
	}
	return nil
}

func (l *flakyTriggerLifecycle) Stop(context.Context, int64) error {
	l.stopped = true
	return nil
}

func TestRunStep_TriggerRetriesUntilEnginesAreReady(t *testing.T) {
	t.Parallel()
	e := setupStep(t, true)
	ctx := context.Background()
	// The stub never actually starts a run, so RunStep fails with
	// ErrStepRunNotStarted right after Trigger finally succeeds -- what
	// matters here is that it took retries to get there at all, not how the
	// step ends.
	lc := &flakyTriggerLifecycle{failCount: 3}
	clock := newFakeClock()
	var slept int
	runner := calibrationapp.NewStepRunner(e.store, lc, e.reports).WithSleep(func(d time.Duration) {
		slept++
		if slept > maxTestSleeps {
			t.Fatalf("RunStep slept %d times, want a bounded handful -- a polling loop is spinning unbounded", slept)
		}
		clock.Advance(d)
	}).WithClock(clock.Now)

	if _, err := runner.RunStep(ctx, e.executionID, 10, 30, 0); !errors.Is(err, calibrationapp.ErrStepRunNotStarted) {
		t.Fatalf("error = %v, want ErrStepRunNotStarted", err)
	}
	if lc.calls != 4 {
		t.Fatalf("Trigger called %d times, want 4 (3 retries + the succeeding call)", lc.calls)
	}
	if slept != 3 {
		t.Fatalf("slept %d times, want 3 (once per failed attempt)", slept)
	}
}

func TestRunStep_ErrorsWhenTriggeredRunIsNotReportedRunning(t *testing.T) {
	t.Parallel()
	e := setupStep(t, true)
	ctx := context.Background()
	lc := &stubLifecycle{} // Deploy/Trigger succeed but start no real run
	runner := calibrationapp.NewStepRunner(e.store, lc, e.reports).WithSleep(func(time.Duration) {})

	if _, err := runner.RunStep(ctx, e.executionID, 10, 30, 0); !errors.Is(err, calibrationapp.ErrStepRunNotStarted) {
		t.Fatalf("error = %v, want ErrStepRunNotStarted", err)
	}
	if lc.stopped {
		t.Fatal("Stop called despite no run having started")
	}
}

func TestRunStep_PropagatesReportReadFailure(t *testing.T) {
	t.Parallel()
	e := setupStep(t, true)
	ctx := context.Background()
	e.reports.GetErr = errors.New("boom")

	if _, err := e.runner.RunStep(ctx, e.executionID, 10, 30, 0); err == nil {
		t.Fatal("RunStep error = nil, want the report store's failure")
	}
}

// A completed step is calibration progress: the idle clock must carry a
// fresh stamp so the engine reaper does not count the gap between a search's
// steps against the engines the next step is about to reuse. Deploy and
// Trigger stamp their own halves through the real lifecycle; this pins that
// the completion -- after Stop, whatever settled the run -- leaves a stamp
// too (>= 3: deploy + trigger + completion).
func TestRunStep_StampsEngineActivityOnCompletion(t *testing.T) {
	t.Parallel()
	e := setupStep(t, true)
	ctx := context.Background()

	if got := e.store.TouchActivityCount(e.executionID); got != 0 {
		t.Fatalf("activity touches before RunStep = %d, want 0", got)
	}
	if _, err := e.runner.RunStep(ctx, e.executionID, 123.4, 17, 0); err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if got := e.store.TouchActivityCount(e.executionID); got < 3 {
		t.Fatalf("activity touches after RunStep = %d, want >= 3 (deploy, trigger, completion)", got)
	}
	if _, ok, err := e.store.LastActivity(ctx, e.executionID); err != nil || !ok {
		t.Fatalf("LastActivity after RunStep = %v,%v; want true,nil", ok, err)
	}
}
