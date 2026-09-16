package digestapp_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/digestapp"
	"github.com/heridotlife/honryu/internal/domain/digest"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/domain/threshold"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// fPtr returns a pointer to f -- the observed-value shapes.
func fPtr(f float64) *float64 { return &f }

// bPtr returns a pointer to b -- the satisfied shapes.
func bPtr(b bool) *bool { return &b }

// seedResult stores one grading row for a run, the way thresholdapp's
// EvaluateRun would have left it at report finalize.
func seedResult(t *testing.T, store *fake.Store, r threshold.Result) {
	t.Helper()
	if err := store.SaveThresholdResults(context.Background(), []threshold.Result{r}); err != nil {
		t.Fatalf("SaveThresholdResults(run %d): %v", r.RunID, err)
	}
}

// saveScenarioRun is saveRun plus the scenario the run belongs to -- the
// join key the threshold section resolves names by.
func saveScenarioRun(t *testing.T, store *fake.Store, executionID, scenarioID int64, runID int, started time.Time, outcome taurus.Outcome) {
	t.Helper()
	rep := report.Report{
		ExecutionID: executionID, ScenarioID: scenarioID, RunID: int64(runID),
		StartedAt: started, EndedAt: started.Add(5 * time.Minute),
		Outcome: outcome,
	}
	if err := store.SaveReport(context.Background(), rep); err != nil {
		t.Fatalf("SaveReport(run %d): %v", runID, err)
	}
}

// TestBuildDigestThresholdsAggregation pins the threshold section (phase
// 74): one line per window run that was graded -- met bounds leave no
// row, a definitive miss carries its observed value, an unjudgeable bound
// carries the reason with observed_value null -- runs whose scenario
// defined no thresholds are excluded entirely, scenario names resolve
// from the project's scenario list, and the per-line wire shape is exact.
func TestBuildDigestThresholdsAggregation(t *testing.T) {
	f := newFixture(t)
	start, end := window()
	in := start.Add(2 * time.Hour)

	// The graded runs belong to a scenario with a name the payload joins.
	scID := seedScenario(t, f.store, "checkout", f.projA)
	saveScenarioRun(t, f.store, f.execA1, scID, 1, in, taurus.OutcomeFailed)                  // missed: p95 crossed, error_rate met
	saveScenarioRun(t, f.store, f.execA1, scID, 2, in.Add(time.Hour), taurus.OutcomePassed)   // never graded: no line
	saveScenarioRun(t, f.store, f.execA2, scID, 3, in.Add(2*time.Hour), taurus.OutcomePassed) // all met
	saveScenarioRun(t, f.store, f.execA2, scID, 4, in.Add(3*time.Hour), taurus.OutcomePassed) // unknown: p99 absent

	// Run 1's two rows go in one call -- evaluation is per run, and a
	// second call for the same run would replace the first.
	if err := f.store.SaveThresholdResults(context.Background(), []threshold.Result{
		{
			ThresholdID: 7, ExecutionID: f.execA1, RunID: 1,
			Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: 300,
			Observed: fPtr(450.5), Satisfied: bPtr(false),
		},
		{
			ThresholdID: 8, ExecutionID: f.execA1, RunID: 1,
			Metric: threshold.MetricErrorRate, Comparison: threshold.ComparisonLT, Value: 0.05,
			Observed: fPtr(0.02), Satisfied: bPtr(true),
		},
	}); err != nil {
		t.Fatalf("SaveThresholdResults(run 1): %v", err)
	}
	seedResult(t, f.store, threshold.Result{
		ThresholdID: 9, ExecutionID: f.execA2, RunID: 3,
		Metric: threshold.MetricThroughputQPS, Comparison: threshold.ComparisonGT, Value: 50,
		Observed: fPtr(120), Satisfied: bPtr(true),
	})
	seedResult(t, f.store, threshold.Result{
		ThresholdID: 10, ExecutionID: f.execA2, RunID: 4,
		Metric: threshold.MetricHTTPP99MS, Comparison: threshold.ComparisonLT, Value: 500,
		Satisfied: nil, Reason: "no p99 latency in the report",
	})

	d, err := f.svc.BuildDigest(context.Background(), f.projA, digest.PeriodDaily, start, end)
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	var p digestapp.Payload
	if err := json.Unmarshal(d.Payload, &p); err != nil {
		t.Fatalf("decode payload: %v (%s)", err, d.Payload)
	}

	want := []digestapp.ThresholdLine{
		{
			RunID: 1, ExecutionID: f.execA1, ScenarioName: "checkout",
			Outcome: digestapp.ThresholdOutcomeMissed,
			Missed: []digestapp.ThresholdMiss{{
				Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT,
				Value: 300, ObservedValue: fPtr(450.5),
			}},
		},
		{
			RunID: 3, ExecutionID: f.execA2, ScenarioName: "checkout",
			Outcome: digestapp.ThresholdOutcomeAllMet, Missed: []digestapp.ThresholdMiss{},
		},
		{
			RunID: 4, ExecutionID: f.execA2, ScenarioName: "checkout",
			Outcome: digestapp.ThresholdOutcomeUnknown,
			Missed: []digestapp.ThresholdMiss{{
				Metric: threshold.MetricHTTPP99MS, Comparison: threshold.ComparisonLT,
				Value: 500, Reason: "no p99 latency in the report",
			}},
		},
	}
	if !reflect.DeepEqual(p.Thresholds, want) {
		t.Errorf("thresholds = %+v, want %+v", p.Thresholds, want)
	}

	// The line's own wire shape, from the bytes: exactly these keys, and
	// reason only where there was something to explain (omitempty), while
	// observed_value stays on the wire as an explicit null for the
	// unjudgeable row.
	var raw struct {
		Thresholds []map[string]any `json:"thresholds"`
	}
	if err := json.Unmarshal(d.Payload, &raw); err != nil {
		t.Fatalf("decode raw payload: %v", err)
	}
	lines := raw.Thresholds
	if len(lines) != 3 {
		t.Fatalf("thresholds = %d lines, want 3 (run 2 excluded)", len(lines))
	}
	assertKeys(t, "thresholds[0]", keysOf(t, lines[0]),
		[]string{"execution_id", "missed", "outcome", "run_id", "scenario_name"})
	missedRow := lines[0]["missed"].([]any)[0].(map[string]any)
	assertKeys(t, "thresholds[0].missed[0]", keysOf(t, missedRow),
		[]string{"comparison", "metric", "observed_value", "value"})
	if missedRow["observed_value"] != 450.5 {
		t.Errorf("missed[0].observed_value = %v, want the run's own p95", missedRow["observed_value"])
	}
	unknownRow := lines[2]["missed"].([]any)[0].(map[string]any)
	if v, ok := unknownRow["observed_value"]; !ok || v != nil {
		t.Errorf("unknown row's observed_value = %v (present=%t), want an explicit null on the wire", v, ok)
	}
	if unknownRow["reason"] != "no p99 latency in the report" {
		t.Errorf("unknown row's reason = %v, want why nothing could be compared", unknownRow["reason"])
	}
}

// TestBuildDigestThresholdsEmpty pins the honest empties: a window with no
// runs at all -- and a window whose runs were never graded -- carry an
// empty thresholds array, never null.
func TestBuildDigestThresholdsEmpty(t *testing.T) {
	f := newFixture(t)
	start, end := window()

	// Empty window entirely.
	d, err := f.svc.BuildDigest(context.Background(), f.projA, digest.PeriodDaily, start, end)
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	var p digestapp.Payload
	if err := json.Unmarshal(d.Payload, &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if p.Thresholds == nil || len(p.Thresholds) != 0 {
		t.Errorf("thresholds = %+v, want an empty non-nil array", p.Thresholds)
	}

	// Runs in the window, none graded.
	saveRun(t, f.store, f.execA1, 1, start.Add(time.Hour), taurus.OutcomePassed)
	d, err = f.svc.BuildDigest(context.Background(), f.projA, digest.PeriodDaily, start, end)
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	if err := json.Unmarshal(d.Payload, &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if p.Thresholds == nil || len(p.Thresholds) != 0 {
		t.Errorf("thresholds = %+v, want an empty non-nil array (ungraded runs excluded)", p.Thresholds)
	}
}
