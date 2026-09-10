package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/calibrationapp"
	"github.com/heridotlife/honryu/internal/config"
	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
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

func TestNewRepository_MySQL_BadDSN(t *testing.T) {
	t.Parallel()
	// A DSN missing the slash separating the database name is rejected at
	// Open, before any connection is attempted.
	if _, err := newRepository(config.DBConfig{Driver: "mysql", DSN: "honryu:secret"}, "default"); err == nil {
		t.Fatal("newRepository(mysql, malformed DSN): expected error, got nil")
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

// A service not configured for advancing (no runner) reports an operational
// error; advanceCalibrationOnce must log it and return rather than panic --
// the next tick moves on regardless.
func TestAdvanceCalibrationOnce_ErrorIsLoggedNotPropagated(t *testing.T) {
	t.Parallel()
	advanceCalibrationOnce(context.Background(), calibrationapp.NewService(fake.NewStore()))
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

func TestRun_TicksUntilContextCancelled(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"HONRYU_DB_DRIVER":                "fake",
		"HONRYU_LOG_FORMAT":               "text",
		"HONRYU_CALIBRATOR_TICK_INTERVAL": "10ms",
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

func TestWireCalibrations_ProducesAWorkingService(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	sched := fake.NewScheduler()
	obj := fake.NewObjectStore()
	cfg := config.Config{Cluster: config.ClusterConfig{EngineImages: map[taurus.Executor]string{taurus.ExecutorJMeter: "honryu/jmeter:latest"}}}

	calibrations := wireCalibrations(store, sched, obj, cfg)
	if calibrations == nil {
		t.Fatal("wireCalibrations returned nil")
	}

	p, _ := project.New("web", "honryu", "")
	projectID, err := store.CreateProject(ctx, p)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi"}
	scenarioID, src, err := seedBoundSource(ctx, store, projectID)
	if err != nil {
		t.Fatalf("seedBoundSource: %v", err)
	}
	executionID, err := calibrations.Create(ctx, "calibrate", projectID, taurus.ExecutorJMeter, spec, src, scenarioID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if executionID <= 0 {
		t.Fatalf("Create returned id = %d, want > 0", executionID)
	}
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

// stubRunner is a scripted calibrationapp.Runner, mirroring
// calibrationapp's own test double -- duplicated rather than imported,
// since cmd/calibrator is package main and cannot import
// calibrationapp_test's unexported helpers.
type stubRunner struct {
	responses []report.Report
	calls     int
}

func (r *stubRunner) RunStep(context.Context, int64, float64, int, float64) (report.Report, error) {
	if r.calls >= len(r.responses) {
		// Ticks after the search's already-scripted steps are a no-op find:
		// nothing left to advance in this test.
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

// targetSaturatedReport reports the engine keeping up while the overall
// error rate trips a "failures>5%" criterion -- a single-tick terminal
// outcome, enough to prove the loop actually advances a real job.
func targetSaturatedReport(requestedQPS float64) report.Report {
	return report.Report{
		Requested:   report.Load{Throughput: requestedQPS},
		Achieved:    report.Load{Throughput: requestedQPS, Samples: 1000},
		ErrorRate:   0.10,
		Attribution: report.Attribution{Target: 100},
	}
}

func TestRunCalibratorLoop_AdvancesADueJobAndStopsOnCancel(t *testing.T) {
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

	runner := &stubRunner{responses: []report.Report{targetSaturatedReport(10)}}
	calibrations := calibrationapp.NewService(store).WithRunner(runner).WithFingerprint(stubFingerprinter{})

	loopCtx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel()
	runCalibratorLoop(loopCtx, calibrations, 10*time.Millisecond)

	job, err := calibrations.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.Phase != calibration.PhaseDone {
		t.Fatalf("job.Phase = %q, want done -- runCalibratorLoop never advanced it within its ticking window", job.Phase)
	}
}

func TestAdvanceCalibrationOnce_NothingDueIsANoOp(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	calibrations := calibrationapp.NewService(store).WithRunner(&stubRunner{}).WithFingerprint(stubFingerprinter{})

	// Must not panic or block with an empty store.
	advanceCalibrationOnce(context.Background(), calibrations)
}
