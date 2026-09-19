// Phase 91: the re-resolve action's application matrix. A mode entry
// snapshots its derivation at PUT time; ReResolveConfig re-runs the stored
// config through the CURRENT resolution chain and persists the refresh as
// a new config version. The matrix: the refresh itself (numbers move,
// provenance does not), the refusal (nothing persisted), advanced entries
// byte-untouched beside a refreshed mode sibling, the version semantics
// (criteria and csv_split ride along), and the degenerate configs.
package executionapp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/domain/capacityprofile"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
)

// recalibrate rewrites the capacity profile under key and seeds a newer
// settled report with the given p95, the way a real recalibration would:
// a different per-pod rate, and a fresher measurement the latency chain's
// "latest settled report" step picks up.
func recalibrate(t *testing.T, e *modeTestEnv, exec execution.Execution, key capacityprofile.Key, jobID int64, perPodQPS, p95Seconds float64) {
	t.Helper()
	e.capacity.profiles[key] = *freshProfile(perPodQPS, jobID)
	job, err := e.store.GetCalibrationJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("GetCalibrationJob: %v", err)
	}
	rpt := report.Report{
		RunID: job.ExecutionID*100 + 1, ExecutionID: job.ExecutionID,
		Outcome: taurus.OutcomePassed,
		// Later than seedCalibrationReport's zero StartedAt, so the fake's
		// most-recent-first listing serves this one to the chain.
		StartedAt: time.Now().Add(time.Minute),
	}
	rpt.Latency = report.Percentiles{95: p95Seconds}
	if err := e.store.SaveReport(context.Background(), rpt); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
}

// The refresh: a stored burst entry resolved against a 125 rps/pod profile
// with p95 250ms (the spec's worked example, 4 engines / 375 VUs) is
// re-resolved after recalibration to 100 rps/pod and p95 500ms -- engines
// 5, concurrency 750 -- while the operator's statement (mode, rate,
// duration) rides along unchanged.
func TestReResolveConfig_RefreshesAgainstCurrentCalibration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	exec := execution.Execution{Engine: taurus.ExecutorJMeter}
	e := newModeEnv(t, exec, 250*time.Millisecond, nil)
	jobID := seedCalibrationJob(t, e, exec)
	key := modeTestKey(e.scenarioID, exec)
	e.capacity.profiles[key] = *freshProfile(125, jobID)
	seedCalibrationReport(t, e, jobID, 0.25)

	if err := e.svc.StoreConfig(ctx, e.executionID, modeConfig(e, "burst", 500, 600)); err != nil {
		t.Fatalf("StoreConfig: %v", err)
	}
	if got := stored(t, e); got.Engines != 4 || got.Concurrency != 375 {
		t.Fatalf("precondition: stored entry = %d engines / %d concurrency, want 4/375", got.Engines, got.Concurrency)
	}

	recalibrate(t, e, exec, key, jobID, 100, 0.5)
	result, err := e.svc.ReResolveConfig(ctx, e.executionID)
	if err != nil {
		t.Fatalf("ReResolveConfig: %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(result.Entries))
	}
	got := result.Entries[0]
	if got.ScenarioID != e.scenarioID || got.Mode != "burst" {
		t.Fatalf("entry identity = %d/%q, want the stored scenario with its mode", got.ScenarioID, got.Mode)
	}
	if got.Before != (executionapp.ResolvedDiff{Engines: 4, Concurrency: 375, Rampup: 0, Throughput: 500}) {
		t.Errorf("Before = %+v, want the stored snapshot 4/375/0/500", got.Before)
	}
	if got.After != (executionapp.ResolvedDiff{Engines: 5, Concurrency: 750, Rampup: 0, Throughput: 500}) {
		t.Errorf("After = %+v, want 5 engines (500/100) and 750 VUs (500 x 0.5s x 3)", got.After)
	}
	if !got.Changed {
		t.Errorf("Changed = false, want true (the derivation moved)")
	}

	// The refreshed numbers are what is now stored -- a later deploy runs
	// them, and the provenance is still the operator's statement.
	persisted := stored(t, e)
	if persisted.Engines != 5 || persisted.Concurrency != 750 || persisted.Rampup != 0 {
		t.Fatalf("persisted entry = %d/%d/%d, want 5/750/0", persisted.Engines, persisted.Concurrency, persisted.Rampup)
	}
	if persisted.Mode != "burst" || persisted.Throughput != 500 || persisted.Duration != 600 {
		t.Fatalf("provenance = %q/%d/%d, want burst/500/600 unchanged", persisted.Mode, persisted.Throughput, persisted.Duration)
	}
}

// The refusal: a profile that went missing (or stale) since the config was
// stored refuses the re-resolve with the same typed error a PUT earns, and
// the stored config keeps its old snapshot -- nothing persisted.
func TestReResolveConfig_RefusalLeavesStoredConfigIntact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	exec := execution.Execution{Engine: taurus.ExecutorJMeter}
	e := newModeEnv(t, exec, 250*time.Millisecond, nil)
	jobID := seedCalibrationJob(t, e, exec)
	key := modeTestKey(e.scenarioID, exec)
	e.capacity.profiles[key] = *freshProfile(125, jobID)
	seedCalibrationReport(t, e, jobID, 0.25)
	if err := e.svc.StoreConfig(ctx, e.executionID, modeConfig(e, "soak", 100, 3600)); err != nil {
		t.Fatalf("StoreConfig: %v", err)
	}

	// The calibration is pruned: no profile under the key anymore.
	delete(e.capacity.profiles, key)
	_, err := e.svc.ReResolveConfig(ctx, e.executionID)
	var refused *executionapp.ModeResolutionError
	if !errors.As(err, &refused) {
		t.Fatalf("ReResolveConfig err = %v, want a ModeResolutionError", err)
	}
	if refused.Status != capacityprofile.StatusNoProfile || refused.Key != key {
		t.Fatalf("refusal = %q for %v, want no_profile for the entry's key", refused.Status, refused.Key)
	}
	// The stored config is exactly the old snapshot.
	if got := stored(t, e); got.Engines != 1 || got.Concurrency != 75 || got.Rampup != 60 {
		t.Fatalf("stored entry after refusal = %+v, want the untouched snapshot 1/75/60", got)
	}
}

// A mixed config: the advanced sibling is re-persisted byte-identically
// (changed=false in the report) while the mode entry beside it refreshes --
// re-resolving one scenario's derivation never disturbs a hand-tuned one.
func TestReResolveConfig_AdvancedEntriesUntouched(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	exec := execution.Execution{Engine: taurus.ExecutorJMeter}
	e := newModeEnv(t, exec, 250*time.Millisecond, nil)
	coll, err := e.store.GetExecution(ctx, e.executionID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	secondID := seedScenario(t, e.store, "aux", coll.ProjectID)
	jobID := seedCalibrationJob(t, e, exec)
	key := modeTestKey(e.scenarioID, exec)
	e.capacity.profiles[key] = *freshProfile(10, jobID)
	seedCalibrationReport(t, e, jobID, 0.5)

	advanced := loadprofile.Entry{ScenarioID: secondID, Concurrency: 5, Rampup: 45, Engines: 3, Duration: 120, Throughput: 40}
	if err := e.svc.StoreConfig(ctx, e.executionID, loadprofile.Profile{
		ExecutionID: e.executionID,
		Tests:       []loadprofile.Entry{advanced, {Name: "guided", ScenarioID: e.scenarioID, Mode: "burst", Throughput: 20, Duration: 600}},
	}); err != nil {
		t.Fatalf("StoreConfig: %v", err)
	}

	recalibrate(t, e, exec, key, jobID, 5, 1.0)
	result, err := e.svc.ReResolveConfig(ctx, e.executionID)
	if err != nil {
		t.Fatalf("ReResolveConfig: %v", err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(result.Entries))
	}
	if got := result.Entries[0]; got.Changed || got.Mode != "" || got.After.Engines != 3 || got.After.Concurrency != 5 {
		t.Errorf("advanced report = %+v, want unchanged 3 engines / 5 VUs", got)
	}
	// Mode sibling: 20 rps over 10/pod was 2 engines; over 5/pod it is 4,
	// and p95 1.0s lifts concurrency 30 -> 60.
	if got := result.Entries[1]; !got.Changed || got.After.Engines != 4 || got.After.Concurrency != 60 {
		t.Errorf("mode report = %+v, want changed to 4 engines / 60 VUs", got)
	}

	entries, err := e.store.LoadProfileFor(ctx, e.executionID)
	if err != nil {
		t.Fatalf("LoadProfileFor: %v", err)
	}
	if entries[0] != advanced {
		t.Errorf("advanced entry = %+v, want it byte-for-byte unchanged", entries[0])
	}
	if entries[1].Engines != 4 || entries[1].Concurrency != 60 || entries[1].Mode != "burst" {
		t.Errorf("mode entry = %+v, want 4/60/burst", entries[1])
	}
}

// Versioning semantics: the re-resolve persists a new config VERSION the
// way StoreConfig always has -- the whole profile (criteria, csv_split,
// entry order) rides along, so nothing but the mode entries' derived
// numbers changes between the stored versions.
func TestReResolveConfig_NewVersionPreservesCriteriaAndSplit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	exec := execution.Execution{Engine: taurus.ExecutorJMeter}
	e := newModeEnv(t, exec, 250*time.Millisecond, nil)
	jobID := seedCalibrationJob(t, e, exec)
	key := modeTestKey(e.scenarioID, exec)
	e.capacity.profiles[key] = *freshProfile(125, jobID)
	seedCalibrationReport(t, e, jobID, 0.25)

	cfg := modeConfig(e, "ramp", 250, 600)
	cfg.CSVSplit = true
	cfg.Criteria = []string{"failures>10%", "p95>500ms"}
	if err := e.svc.StoreConfig(ctx, e.executionID, cfg); err != nil {
		t.Fatalf("StoreConfig: %v", err)
	}

	recalibrate(t, e, exec, key, jobID, 50, 0.25)
	if _, err := e.svc.ReResolveConfig(ctx, e.executionID); err != nil {
		t.Fatalf("ReResolveConfig: %v", err)
	}
	echoed, err := e.svc.GetConfig(ctx, e.executionID)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if len(echoed.Content.Criteria) != 2 || echoed.Content.Criteria[0] != "failures>10%" {
		t.Errorf("criteria = %v, want both preserved", echoed.Content.Criteria)
	}
	if !echoed.Content.CSVSplit {
		t.Error("csv_split lost by the re-resolve store")
	}
	if len(echoed.Content.Tests) != 1 || echoed.Content.Tests[0].Engines != 5 {
		t.Errorf("tests = %+v, want the refreshed 5-engine entry", echoed.Content.Tests)
	}
}

// An execution with nothing stored has nothing to re-resolve: the ordinary
// StoreConfig refusal (ErrNoScenarios) answers, not a silent success.
func TestReResolveConfig_NoConfigStored(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newModeEnv(t, execution.Execution{Engine: taurus.ExecutorJMeter}, 250*time.Millisecond, nil)
	if _, err := e.svc.ReResolveConfig(ctx, e.executionID); !errors.Is(err, loadprofile.ErrNoScenarios) {
		t.Fatalf("ReResolveConfig err = %v, want ErrNoScenarios", err)
	}
}

// An all-advanced config re-resolves to itself: success, every entry
// changed=false, and the mode sources are never even consulted.
func TestReResolveConfig_AllAdvancedConfigIsANoOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newModeEnv(t, execution.Execution{Engine: taurus.ExecutorJMeter}, 250*time.Millisecond, freshProfile(100, 0))
	cfg := loadprofile.Profile{ExecutionID: e.executionID, Tests: []loadprofile.Entry{{
		Name: "t", ScenarioID: e.scenarioID, Engines: 2, Concurrency: 10, Rampup: 5, Duration: 60, Throughput: 99,
	}}}
	if err := e.svc.StoreConfig(ctx, e.executionID, cfg); err != nil {
		t.Fatalf("StoreConfig: %v", err)
	}

	result, err := e.svc.ReResolveConfig(ctx, e.executionID)
	if err != nil {
		t.Fatalf("ReResolveConfig: %v", err)
	}
	if len(result.Entries) != 1 || result.Entries[0].Changed {
		t.Fatalf("entries = %+v, want one unchanged advanced entry", result.Entries)
	}
	if got := stored(t, e); got.Engines != 2 || got.Concurrency != 10 || got.Rampup != 5 || got.Throughput != 99 {
		t.Fatalf("stored entry = %+v, want exactly what was stored", got)
	}
	if len(e.capacity.asked) != 0 {
		t.Fatalf("capacity asked = %v, want no fan-out for an advanced config", e.capacity.asked)
	}
}
