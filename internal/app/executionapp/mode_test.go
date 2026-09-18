package executionapp_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/capacityprofile"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// stubCapacity is a scriptable CapacityProfiles: one profile per key, a
// fan-out verdict computed from it exactly the way the real one would
// (stale fingerprint, saturated-by, per-pod rate), and a record of every
// key it was asked about.
type stubCapacity struct {
	profiles    map[capacityprofile.Key]capacityprofile.CapacityProfile
	fingerprint string
	asked       []capacityprofile.Key
}

func (c *stubCapacity) FanOut(_ context.Context, key capacityprofile.Key, targetQPS float64) (capacityprofile.Result, error) {
	c.asked = append(c.asked, key)
	profile, ok := c.profiles[key]
	if !ok {
		return capacityprofile.FanOut(nil, targetQPS, ""), nil
	}
	return capacityprofile.FanOut(&profile, targetQPS, c.fingerprint), nil
}

func (c *stubCapacity) ProfileFor(_ context.Context, key capacityprofile.Key) (capacityprofile.CapacityProfile, error) {
	profile, ok := c.profiles[key]
	if !ok {
		return capacityprofile.CapacityProfile{}, ports.ErrNotFound
	}
	return profile, nil
}

// modeTestEnv is an execution service wired with scriptable mode sources
// over the fake store, plus the ids a config upload needs.
type modeTestEnv struct {
	svc         *executionapp.Service
	store       *fake.Store
	capacity    *stubCapacity
	scenarioID  int64
	executionID int64
}

func newModeEnv(t *testing.T, exec execution.Execution, latencyHint time.Duration, profile *capacityprofile.CapacityProfile) *modeTestEnv {
	t.Helper()
	store := fake.NewStore()
	projectID := seedProject(t, store, nil)
	scenarioID := seedScenario(t, store, "target", projectID)

	exec.ProjectID = projectID
	if exec.Name == "" {
		exec.Name = "load"
	}
	executionID, err := store.CreateExecution(context.Background(), exec)
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}

	cap := &stubCapacity{profiles: map[capacityprofile.Key]capacityprofile.CapacityProfile{}, fingerprint: "fp-current"}
	if profile != nil {
		cap.profiles[modeTestKey(scenarioID, exec)] = *profile
	}
	svc := executionapp.NewService(store, fake.NewObjectStore(), 500).WithModeSources(executionapp.ModeSources{
		Capacity: cap, Jobs: store, Reports: store,
		LatencyHint: latencyHint, DefaultEngine: taurus.ExecutorJMeter,
	})
	return &modeTestEnv{svc: svc, store: store, capacity: cap, scenarioID: scenarioID, executionID: executionID}
}

// modeTestKey is the capacity key modeTestEnv's wiring expects resolution
// to ask about -- modeCapacityKey's own normalization (default engine,
// baseline pod size) applied to the execution under test.
func modeTestKey(scenarioID int64, exec execution.Execution) capacityprofile.Key {
	cpu, memory := exec.CPU, exec.Memory
	if cpu == "" {
		cpu = "500m"
	}
	if memory == "" {
		memory = "512Mi"
	}
	engine := exec.Engine
	if engine == "" {
		engine = taurus.ExecutorJMeter
	}
	return capacityprofile.Key{ScenarioID: scenarioID, Engine: engine, CPU: cpu, Memory: memory}
}

// freshProfile is a confirmed engine-limited profile: perPodQPS sustained
// per pod, calibrated by jobID, fingerprinted fp-current.
func freshProfile(perPodQPS float64, jobID int64) *capacityprofile.CapacityProfile {
	return &capacityprofile.CapacityProfile{
		PerPodQPS: perPodQPS, SaturatedBy: calibration.SaturatedByEngine,
		ScenarioFingerprint: "fp-current", JobID: jobID,
	}
}

// modeConfig builds the PUT payload a Simple-mode client sends for the
// env's scenario: mode + rate + duration, nothing else.
func modeConfig(e *modeTestEnv, mode string, qps, duration int) loadprofile.Profile {
	return loadprofile.Profile{
		ExecutionID: e.executionID,
		Tests: []loadprofile.Entry{{
			Name: "t", ScenarioID: e.scenarioID, Mode: mode,
			Throughput: qps, Duration: duration,
		}},
	}
}

// stored reads back the persisted entry (single-scenario envs).
func stored(t *testing.T, e *modeTestEnv) loadprofile.Entry {
	t.Helper()
	entries, err := e.store.LoadProfileFor(context.Background(), e.executionID)
	if err != nil {
		t.Fatalf("LoadProfileFor: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("stored entries = %d, want 1", len(entries))
	}
	return entries[0]
}

// seedCalibrationReport writes a settled report carrying p95 onto the
// calibration execution jobID points at, so the latency chain can walk
// profile -> job -> execution -> report.
func seedCalibrationReport(t *testing.T, e *modeTestEnv, jobID int64, p95Seconds float64) {
	t.Helper()
	job, err := e.store.GetCalibrationJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("GetCalibrationJob: %v", err)
	}
	rpt := report.Report{RunID: job.ExecutionID * 100, ExecutionID: job.ExecutionID, Outcome: taurus.OutcomePassed}
	rpt.Latency = report.Percentiles{95: p95Seconds}
	if err := e.store.SaveReport(context.Background(), rpt); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
}

func seedCalibrationJob(t *testing.T, e *modeTestEnv, exec execution.Execution) int64 {
	t.Helper()
	exec.ProjectID = seedProject(t, e.store, nil)
	exec.Name = "calib"
	id, err := e.store.CreateExecution(context.Background(), exec)
	if err != nil {
		t.Fatalf("CreateExecution (calibration): %v", err)
	}
	jobID, err := e.store.CreateCalibrationJob(context.Background(), id, e.scenarioID)
	if err != nil {
		t.Fatalf("CreateCalibrationJob: %v", err)
	}
	return jobID
}

// The happy path: a burst entry resolves engines from the profile,
// concurrency from the calibration report's p95, ramp-up from the burst
// policy, and persists as an ordinary entry carrying mode as provenance.
func TestStoreConfig_ModeResolutionBurst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	calibExec := execution.Execution{Engine: taurus.ExecutorJMeter}
	e := newModeEnv(t, execution.Execution{Engine: taurus.ExecutorJMeter}, 250*time.Millisecond, nil)
	jobID := seedCalibrationJob(t, e, calibExec)
	e.capacity.profiles[capacityprofile.Key{ScenarioID: e.scenarioID, Engine: taurus.ExecutorJMeter, CPU: "500m", Memory: "512Mi"}] = *freshProfile(125, jobID)
	seedCalibrationReport(t, e, jobID, 0.25)

	if err := e.svc.StoreConfig(ctx, e.executionID, modeConfig(e, "burst", 500, 600)); err != nil {
		t.Fatalf("StoreConfig: %v", err)
	}
	got := stored(t, e)
	// Engines: ceil(500/125) = 4. Concurrency: Little's Law 500*0.25*3 =
	// 375 (the spec's worked example). Ramp-up: burst is 0.
	if got.Engines != 4 {
		t.Errorf("Engines = %d, want 4 (500 rps / 125 per pod)", got.Engines)
	}
	if got.Concurrency != 375 {
		t.Errorf("Concurrency = %d, want 375 (500 x p95 250ms x 3.0)", got.Concurrency)
	}
	if got.Rampup != 0 {
		t.Errorf("Rampup = %d, want 0 (burst: cold start is the subject)", got.Rampup)
	}
	if got.Mode != "burst" || got.Throughput != 500 || got.Duration != 600 {
		t.Errorf("provenance/rate/duration = %q/%d/%d, want burst/500/600", got.Mode, got.Throughput, got.Duration)
	}
	// The capacity key the resolution used: the execution's engine at the
	// baseline pod size (no pin on an ordinary execution).
	wantKey := capacityprofile.Key{ScenarioID: e.scenarioID, Engine: taurus.ExecutorJMeter, CPU: "500m", Memory: "512Mi"}
	if len(e.capacity.asked) != 1 || e.capacity.asked[0] != wantKey {
		t.Errorf("capacity key asked = %v, want %v", e.capacity.asked, wantKey)
	}
}

// Ramp and soak policies arrive through the same resolution; the soak also
// exercises the fallback latency hint (report chain intentionally absent:
// the profile's job ledger row exists but no report was ever saved).
func TestStoreConfig_ModeResolutionRampAndSoakHintFallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newModeEnv(t, execution.Execution{Engine: taurus.ExecutorJMeter}, 200*time.Millisecond, nil)
	jobID := seedCalibrationJob(t, e, execution.Execution{Engine: taurus.ExecutorJMeter})
	e.capacity.profiles[capacityprofile.Key{ScenarioID: e.scenarioID, Engine: taurus.ExecutorJMeter, CPU: "500m", Memory: "512Mi"}] = *freshProfile(100, jobID)
	// No seedCalibrationReport: the chain breaks at the report step and the
	// configured fallback (200ms) sizes the threads instead.

	if err := e.svc.StoreConfig(ctx, e.executionID, modeConfig(e, "ramp", 100, 600)); err != nil {
		t.Fatalf("StoreConfig(ramp): %v", err)
	}
	got := stored(t, e)
	if got.Rampup != 120 {
		t.Errorf("ramp Rampup = %d, want 120 (600/5)", got.Rampup)
	}
	if want := int(math.Ceil(100 * 0.2 * 3.0)); got.Concurrency != want {
		t.Errorf("ramp Concurrency = %d, want %d (fallback hint 200ms)", got.Concurrency, want)
	}
	if got.Engines != 1 {
		t.Errorf("ramp Engines = %d, want 1 (100 rps / 100 per pod)", got.Engines)
	}

	if err := e.svc.StoreConfig(ctx, e.executionID, modeConfig(e, "soak", 50, 3600)); err != nil {
		t.Fatalf("StoreConfig(soak): %v", err)
	}
	got = stored(t, e)
	if got.Rampup != 60 {
		t.Errorf("soak Rampup = %d, want the fixed 60s warmup", got.Rampup)
	}
	if got.Engines != 1 || got.Concurrency != 30 { // ceil(50*0.2*3) = 30, above the floor
		t.Errorf("soak Engines/Concurrency = %d/%d, want 1/30", got.Engines, got.Concurrency)
	}
}

// The full refusal matrix: every non-ok FanOut status is a
// ModeResolutionError naming the status and the capacity key, and nothing
// is persisted.
func TestStoreConfig_ModeRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	engine := taurus.ExecutorJMeter
	wantKey := func(scenarioID int64) capacityprofile.Key {
		return capacityprofile.Key{ScenarioID: scenarioID, Engine: engine, CPU: "500m", Memory: "512Mi"}
	}

	cases := []struct {
		name    string
		profile *capacityprofile.CapacityProfile
		status  capacityprofile.Status
	}{
		{"no profile", nil, capacityprofile.StatusNoProfile},
		{"stale fingerprint", &capacityprofile.CapacityProfile{
			PerPodQPS: 100, SaturatedBy: calibration.SaturatedByEngine,
			ScenarioFingerprint: "fp-old",
		}, capacityprofile.StatusStale},
		{"target limited", &capacityprofile.CapacityProfile{
			PerPodQPS: 100, SaturatedBy: calibration.SaturatedByTarget,
			ScenarioFingerprint: "fp-current",
		}, capacityprofile.StatusTargetLimited},
		{"inconclusive", &capacityprofile.CapacityProfile{
			PerPodQPS: 100, SaturatedBy: calibration.SaturatedByNeither,
			ScenarioFingerprint: "fp-current",
		}, capacityprofile.StatusInconclusive},
		{"engine floor", &capacityprofile.CapacityProfile{
			PerPodQPS: 0, SaturatedBy: calibration.SaturatedByEngine,
			ScenarioFingerprint: "fp-current",
		}, capacityprofile.StatusEngineFloor},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newModeEnv(t, execution.Execution{Engine: engine}, 250*time.Millisecond, tc.profile)
			err := e.svc.StoreConfig(ctx, e.executionID, modeConfig(e, "soak", 100, 3600))
			var refused *executionapp.ModeResolutionError
			if !errors.As(err, &refused) {
				t.Fatalf("StoreConfig err = %v, want a ModeResolutionError", err)
			}
			if refused.Status != tc.status {
				t.Fatalf("refused.Status = %q, want %q", refused.Status, tc.status)
			}
			if refused.Key != wantKey(e.scenarioID) {
				t.Fatalf("refused.Key = %v, want %v", refused.Key, wantKey(e.scenarioID))
			}
			// Nothing persisted: a refusal must not leave a half-resolved
			// (or any) profile behind.
			entries, err := e.store.LoadProfileFor(ctx, e.executionID)
			if err != nil {
				t.Fatalf("LoadProfileFor: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("entries persisted after refusal = %+v, want none", entries)
			}
		})
	}
}

// Input-shape refusals that never reach FanOut: an unknown mode and a
// mode entry without a rate are client errors (the loadprofile sentinels
// Validate itself would raise), not capacity refusals.
func TestStoreConfig_ModeInputRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newModeEnv(t, execution.Execution{Engine: taurus.ExecutorJMeter}, 250*time.Millisecond, nil)

	if err := e.svc.StoreConfig(ctx, e.executionID, modeConfig(e, "steady", 100, 600)); !errors.Is(err, loadprofile.ErrModeInvalid) {
		t.Fatalf("unknown mode err = %v, want ErrModeInvalid", err)
	}
	if err := e.svc.StoreConfig(ctx, e.executionID, modeConfig(e, "soak", 0, 3600)); !errors.Is(err, loadprofile.ErrModeThroughput) {
		t.Fatalf("rateless mode err = %v, want ErrModeThroughput", err)
	}
	if len(e.capacity.asked) != 0 {
		t.Fatalf("capacity asked = %v, want no fan-out call for input refusals", e.capacity.asked)
	}
}

// A mode entry against an unwired service (no mode sources) is refused
// rather than silently persisted unresolved.
func TestStoreConfig_ModeWithoutSourcesRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	projectID := seedProject(t, store, nil)
	scenarioID := seedScenario(t, store, "target", projectID)
	exec := execution.Execution{Name: "load", ProjectID: projectID}
	executionID, err := store.CreateExecution(ctx, exec)
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
	svc := executionapp.NewService(store, fake.NewObjectStore(), 500)

	cfg := loadprofile.Profile{ExecutionID: executionID, Tests: []loadprofile.Entry{{
		ScenarioID: scenarioID, Mode: "burst", Throughput: 100, Duration: 60,
	}}}
	if err := svc.StoreConfig(ctx, executionID, cfg); !errors.Is(err, executionapp.ErrModeResolutionUnavailable) {
		t.Fatalf("StoreConfig err = %v, want ErrModeResolutionUnavailable", err)
	}
}

// The advanced path is untouched: no mode field anywhere, no fan-out call,
// and the entry persists exactly as sent (byte-identical to the
// pre-phase-90 behaviour -- the golden compile tests pin the rest).
func TestStoreConfig_AdvancedEntriesNeverTouchModeSources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newModeEnv(t, execution.Execution{Engine: taurus.ExecutorJMeter}, 250*time.Millisecond, freshProfile(100, 0))

	cfg := loadprofile.Profile{ExecutionID: e.executionID, Tests: []loadprofile.Entry{{
		Name: "t", ScenarioID: e.scenarioID, Engines: 2, Concurrency: 10, Rampup: 5, Duration: 60, Throughput: 99,
	}}}
	if err := e.svc.StoreConfig(ctx, e.executionID, cfg); err != nil {
		t.Fatalf("StoreConfig: %v", err)
	}
	got := stored(t, e)
	if got.Engines != 2 || got.Concurrency != 10 || got.Rampup != 5 || got.Throughput != 99 || got.Mode != "" {
		t.Fatalf("advanced entry = %+v, want exactly what was sent", got)
	}
	if len(e.capacity.asked) != 0 {
		t.Fatalf("capacity asked = %v, want no fan-out for advanced entries", e.capacity.asked)
	}
}

// Derived numbers the caller sent are overwritten: the server is the only
// resolver, so a stale or malicious pre-fill cannot smuggle different
// load through a mode entry.
func TestStoreConfig_ModeOverwritesCallerDerivedNumbers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newModeEnv(t, execution.Execution{Engine: taurus.ExecutorJMeter}, 250*time.Millisecond, nil)
	jobID := seedCalibrationJob(t, e, execution.Execution{Engine: taurus.ExecutorJMeter})
	e.capacity.profiles[capacityprofile.Key{ScenarioID: e.scenarioID, Engine: taurus.ExecutorJMeter, CPU: "500m", Memory: "512Mi"}] = *freshProfile(125, jobID)
	seedCalibrationReport(t, e, jobID, 0.25)

	cfg := modeConfig(e, "burst", 500, 600)
	cfg.Tests[0].Engines = 999
	cfg.Tests[0].Concurrency = 1
	cfg.Tests[0].Rampup = 1234
	if err := e.svc.StoreConfig(ctx, e.executionID, cfg); err != nil {
		t.Fatalf("StoreConfig: %v", err)
	}
	got := stored(t, e)
	if got.Engines != 4 || got.Concurrency != 375 || got.Rampup != 0 {
		t.Fatalf("entry = %+v, want derived 4/375/0 over the caller's pre-fill", got)
	}
}

// The concurrency floor at the engine count: a very slow target whose
// Little's-Law count lands under the prescribed engines must not let
// shard.Plan silently clamp pods away.
func TestStoreConfig_ModeConcurrencyFloorsAtEngines(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// A 1ms hint with no report seeded: little's law gives ceil(100*0.001*3)
	// = 1, the global floor lifts it to 20 -- and a per-pod rate of 2 rps
	// prescribes 50 engine pods, so the engine floor must win: 50.
	e := newModeEnv(t, execution.Execution{Engine: taurus.ExecutorJMeter}, time.Millisecond, nil)
	jobID := seedCalibrationJob(t, e, execution.Execution{Engine: taurus.ExecutorJMeter})
	e.capacity.profiles[capacityprofile.Key{ScenarioID: e.scenarioID, Engine: taurus.ExecutorJMeter, CPU: "500m", Memory: "512Mi"}] = *freshProfile(2, jobID)

	if err := e.svc.StoreConfig(ctx, e.executionID, modeConfig(e, "burst", 100, 300)); err != nil {
		t.Fatalf("StoreConfig: %v", err)
	}
	got := stored(t, e)
	if got.Engines != 50 {
		t.Fatalf("Engines = %d, want 50 (100 rps / 2 per pod)", got.Engines)
	}
	if got.Concurrency != 50 {
		t.Fatalf("Concurrency = %d, want the engine floor 50 (little's law floored at 20)", got.Concurrency)
	}
}

// A pinned execution (a calibration-sourced pod size) keys its capacity
// profile by that pin, not the baseline; an engine-less execution keys by
// the configured deployment default.
func TestStoreConfig_ModeCapacityKeyResolution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("pinned pod size", func(t *testing.T) {
		t.Parallel()
		exec := execution.Execution{Engine: taurus.ExecutorGatling, CPU: "2", Memory: "1Gi"}
		e := newModeEnv(t, exec, 250*time.Millisecond, nil)
		jobID := seedCalibrationJob(t, e, exec)
		e.capacity.profiles[capacityprofile.Key{ScenarioID: e.scenarioID, Engine: taurus.ExecutorGatling, CPU: "2", Memory: "1Gi"}] = *freshProfile(300, jobID)
		seedCalibrationReport(t, e, jobID, 0.1)

		if err := e.svc.StoreConfig(ctx, e.executionID, modeConfig(e, "ramp", 300, 300)); err != nil {
			t.Fatalf("StoreConfig: %v", err)
		}
		if got := stored(t, e); got.Engines != 1 {
			t.Fatalf("Engines = %d, want 1 from the pinned key's profile", got.Engines)
		}
	})

	t.Run("engineless execution uses the default engine", func(t *testing.T) {
		t.Parallel()
		e := newModeEnv(t, execution.Execution{}, 250*time.Millisecond, nil)
		jobID := seedCalibrationJob(t, e, execution.Execution{})
		// The profile exists under the DEFAULT engine (jmeter), not under
		// the empty engine -- resolution must resolve the same way deploy
		// does or the guided path would 409 for engine-less executions
		// that ARE calibrated.
		e.capacity.profiles[capacityprofile.Key{ScenarioID: e.scenarioID, Engine: taurus.ExecutorJMeter, CPU: "500m", Memory: "512Mi"}] = *freshProfile(100, jobID)
		seedCalibrationReport(t, e, jobID, 0.05)

		if err := e.svc.StoreConfig(ctx, e.executionID, modeConfig(e, "burst", 100, 60)); err != nil {
			t.Fatalf("StoreConfig: %v", err)
		}
		if got := stored(t, e); got.Engines != 1 {
			t.Fatalf("Engines = %d, want 1 via the default-engine key", got.Engines)
		}
	})
}

// The engine limit still guards resolved configs: a mode entry whose
// fan-out exceeds the service's limit is refused with the ordinary
// ErrEngineLimit, never silently truncated.
func TestStoreConfig_ModeStillSubjectToEngineLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	projectID := seedProject(t, store, nil)
	scenarioID := seedScenario(t, store, "target", projectID)
	exec := execution.Execution{Name: "load", ProjectID: projectID, Engine: taurus.ExecutorJMeter}
	executionID, err := store.CreateExecution(ctx, exec)
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
	cap := &stubCapacity{profiles: map[capacityprofile.Key]capacityprofile.CapacityProfile{
		{ScenarioID: scenarioID, Engine: taurus.ExecutorJMeter, CPU: "500m", Memory: "512Mi"}: *freshProfile(1, 0),
	}, fingerprint: "fp-current"}
	svc := executionapp.NewService(store, fake.NewObjectStore(), 5).WithModeSources(executionapp.ModeSources{
		Capacity: cap, Jobs: store, Reports: store,
		LatencyHint: 250 * time.Millisecond, DefaultEngine: taurus.ExecutorJMeter,
	})

	cfg := loadprofile.Profile{ExecutionID: executionID, Tests: []loadprofile.Entry{{
		ScenarioID: scenarioID, Mode: "burst", Throughput: 100, Duration: 60,
	}}}
	if err := svc.StoreConfig(ctx, executionID, cfg); !errors.Is(err, executionapp.ErrEngineLimit) {
		t.Fatalf("StoreConfig err = %v, want ErrEngineLimit (100 pods > limit 5)", err)
	}
}

// GetConfig echoes the mode and the resolved numbers back -- the wire
// contract the Execution page's summary renders from.
func TestGetConfig_EchoesModeAndResolvedNumbers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newModeEnv(t, execution.Execution{Engine: taurus.ExecutorJMeter}, 250*time.Millisecond, nil)
	jobID := seedCalibrationJob(t, e, execution.Execution{Engine: taurus.ExecutorJMeter})
	e.capacity.profiles[capacityprofile.Key{ScenarioID: e.scenarioID, Engine: taurus.ExecutorJMeter, CPU: "500m", Memory: "512Mi"}] = *freshProfile(125, jobID)
	seedCalibrationReport(t, e, jobID, 0.25)

	if err := e.svc.StoreConfig(ctx, e.executionID, modeConfig(e, "soak", 500, 7200)); err != nil {
		t.Fatalf("StoreConfig: %v", err)
	}
	cfg, err := e.svc.GetConfig(ctx, e.executionID)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if len(cfg.Content.Tests) != 1 {
		t.Fatalf("tests = %d, want 1", len(cfg.Content.Tests))
	}
	got := cfg.Content.Tests[0]
	if got.Mode != "soak" || got.Engines != 4 || got.Concurrency != 375 || got.Rampup != 60 {
		t.Fatalf("echoed entry = %+v, want soak/4/375/60", got)
	}
}
