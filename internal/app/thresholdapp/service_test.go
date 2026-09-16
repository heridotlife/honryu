package thresholdapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/threshold"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// env wires the service to fakes, with the scenario pre-created and a frozen
// clock, and returns the pieces the assertions need.
type env struct {
	svc   *Service
	th    *fake.ThresholdStore
	clock time.Time
}

func newEnv(t *testing.T, scenarioID int64) *env {
	t.Helper()
	fakeScenarios := fake.NewStore()
	if _, err := fakeScenarios.CreateScenario(context.Background(), scenario.Scenario{Name: "thresholded", ProjectID: 1}); err != nil {
		t.Fatalf("seed scenario: %v", err)
	}
	// The tests address the seeded scenario by id; the fake assigns 1 on
	// create, so anything else means the caller wants an unknown scenario.
	if scenarioID != 1 {
		// Delete the real row so the id the test asks for is genuinely
		// unknown.
		if err := fakeScenarios.DeleteScenario(context.Background(), 1); err != nil {
			t.Fatalf("clear scenario: %v", err)
		}
	}
	store := fake.NewThresholdStore()
	clock := time.Unix(1700000000, 0).UTC()
	svc := NewService(struct {
		*fake.Store
		*fake.ThresholdStore
	}{fakeScenarios, store}).WithNow(func() time.Time { return clock })
	return &env{svc: svc, th: store, clock: clock}
}

// A report whose scenario owns thresholds, graded metric by metric: p95
// crosses seconds to milliseconds, the comparison applies, the results carry
// the run's identity and the frozen stamp.
func TestEvaluateRunGradesEachThreshold(t *testing.T) {
	e := newEnv(t, 1)
	ctx := context.Background()
	if _, err := e.svc.Replace(ctx, 1, []threshold.Threshold{
		{Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: 300},
		{Metric: threshold.MetricErrorRate, Comparison: threshold.ComparisonLT, Value: 0.05},
		{Metric: threshold.MetricThroughputQPS, Comparison: threshold.ComparisonGT, Value: 50},
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	rep := report.Report{
		ExecutionID: 1, RunID: 42, ScenarioID: 1,
		ErrorRate: 0.02,
		Latency:   report.Percentiles{95: 0.25},
		Achieved:  report.Load{Throughput: 80},
	}
	if err := e.svc.EvaluateRun(ctx, rep); err != nil {
		t.Fatalf("EvaluateRun: %v", err)
	}
	got, err := e.svc.ResultsForRun(ctx, 42)
	if err != nil {
		t.Fatalf("ResultsForRun: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("results = %d, want 3", len(got))
	}
	if *got[0].Observed != 250 || !*got[0].Satisfied {
		t.Errorf("p95 = %v satisfied %v, want 250 true", *got[0].Observed, *got[0].Satisfied)
	}
	if *got[1].Observed != 0.02 || !*got[1].Satisfied {
		t.Errorf("error rate = %v satisfied %v, want 0.02 true", *got[1].Observed, *got[1].Satisfied)
	}
	if *got[2].Observed != 80 || !*got[2].Satisfied {
		t.Errorf("throughput = %v satisfied %v, want 80 true", *got[2].Observed, *got[2].Satisfied)
	}
	for _, r := range got {
		if r.EvaluatedAt != e.clock {
			t.Errorf("evaluated_at = %v, want the frozen clock %v", r.EvaluatedAt, e.clock)
		}
		if r.ExecutionID != 1 || r.RunID != 42 {
			t.Errorf("identity = %d/%d, want execution 1 run 42", r.ExecutionID, r.RunID)
		}
	}
}

// A percentile the report never measured grades to unknown: nil observed and
// satisfied, a reason, and the other thresholds still grade.
func TestEvaluateRunMissingMetricIsUnknown(t *testing.T) {
	e := newEnv(t, 1)
	ctx := context.Background()
	if _, err := e.svc.Replace(ctx, 1, []threshold.Threshold{
		{Metric: threshold.MetricHTTPP99MS, Comparison: threshold.ComparisonLT, Value: 500},
		{Metric: threshold.MetricErrorRate, Comparison: threshold.ComparisonLT, Value: 0.05},
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	rep := report.Report{ExecutionID: 1, RunID: 42, ScenarioID: 1, ErrorRate: 0.2}
	if err := e.svc.EvaluateRun(ctx, rep); err != nil {
		t.Fatalf("EvaluateRun: %v", err)
	}
	got, _ := e.svc.ResultsForRun(ctx, 42)
	if len(got) != 2 {
		t.Fatalf("results = %d, want 2", len(got))
	}
	if got[0].Satisfied != nil || got[0].Observed != nil || got[0].Reason == "" {
		t.Errorf("p99 result = %+v, want unknown with a reason", got[0])
	}
	if got[1].Satisfied == nil || *got[1].Satisfied {
		t.Errorf("error rate result = %+v, want graded (0.2 vs lt 0.05 missed)", got[1])
	}
}

// A scenario with no thresholds grades to nothing stored; the read answers
// empty, not null.
func TestEvaluateRunNoThresholdsStoresNothing(t *testing.T) {
	e := newEnv(t, 1)
	ctx := context.Background()
	rep := report.Report{ExecutionID: 1, RunID: 42, ScenarioID: 1, ErrorRate: 0}
	if err := e.svc.EvaluateRun(ctx, rep); err != nil {
		t.Fatalf("EvaluateRun: %v", err)
	}
	got, err := e.svc.ResultsForRun(ctx, 42)
	if err != nil {
		t.Fatalf("ResultsForRun: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("results = %#v, want an empty non-nil slice", got)
	}
}

// A multi-scenario execution's report (ScenarioID 0) grades nothing: the
// thresholds are per scenario and a bundle cannot be attributed to one.
func TestEvaluateRunSkipsMultiScenarioReports(t *testing.T) {
	e := newEnv(t, 1)
	ctx := context.Background()
	if _, err := e.svc.Replace(ctx, 1, []threshold.Threshold{
		{Metric: threshold.MetricErrorRate, Comparison: threshold.ComparisonLT, Value: 0.05},
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	rep := report.Report{ExecutionID: 1, RunID: 42, ScenarioID: 0, ErrorRate: 0.9}
	if err := e.svc.EvaluateRun(ctx, rep); err != nil {
		t.Fatalf("EvaluateRun: %v", err)
	}
	got, _ := e.svc.ResultsForRun(ctx, 42)
	if len(got) != 0 {
		t.Errorf("results = %+v, want none", got)
	}
}

// Replace validates before touching the store, and an unknown scenario is
// the usual ErrNotFound.
func TestReplaceValidatesAndScopes(t *testing.T) {
	e := newEnv(t, 1)
	ctx := context.Background()
	if _, err := e.svc.Replace(ctx, 1, []threshold.Threshold{
		{Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: 300},
		{Metric: threshold.MetricErrorRate, Comparison: threshold.ComparisonLT, Value: 2},
	}); err == nil {
		t.Error("Replace with an out-of-range error-rate bound = nil error, want the domain's rejection")
	}
	got, _ := e.svc.List(ctx, 1)
	if len(got) != 0 {
		t.Errorf("list after rejected replace = %+v, want the untouched empty set", got)
	}
	if _, err := e.svc.List(ctx, 99); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("List for an unknown scenario = %v, want ErrNotFound", err)
	}
	if _, err := e.svc.Replace(ctx, 99, nil); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("Replace for an unknown scenario = %v, want ErrNotFound", err)
	}
}

// Replace is replace-all: the second call's list is the whole truth, and the
// returned set carries storage ids in caller order.
func TestReplaceAllSemantics(t *testing.T) {
	e := newEnv(t, 1)
	ctx := context.Background()
	first, err := e.svc.Replace(ctx, 1, []threshold.Threshold{
		{Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: 300},
		{Metric: threshold.MetricErrorRate, Comparison: threshold.ComparisonLT, Value: 0.01},
	})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if len(first) != 2 || first[0].ID <= 0 || first[0].ScenarioID != 1 {
		t.Fatalf("first replace = %+v, want two stored rows stamped with the scenario", first)
	}
	second, err := e.svc.Replace(ctx, 1, []threshold.Threshold{
		{Metric: threshold.MetricThroughputQPS, Comparison: threshold.ComparisonGT, Value: 50},
	})
	if err != nil {
		t.Fatalf("second Replace: %v", err)
	}
	if len(second) != 1 || second[0].Metric != threshold.MetricThroughputQPS {
		t.Fatalf("second replace = %+v, want exactly the new definition", second)
	}
	listed, err := e.svc.List(ctx, 1)
	if err != nil || len(listed) != 1 || listed[0].ID != second[0].ID {
		t.Errorf("list after replace-all = %+v (%v), want the new set", listed, err)
	}
}
