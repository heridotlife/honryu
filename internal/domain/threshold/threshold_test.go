package threshold

import (
	"errors"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/report"
)

func mk(metric Metric, cmp Comparison, value float64) Threshold {
	return Threshold{ScenarioID: 7, Metric: metric, Comparison: cmp, Value: value}
}

// rep builds a report with the metrics a threshold reads: p95 at 0.25s,
// p99 at 0.4s, a 2% error rate, an achieved 80 req/s.
func thresholdRep() report.Report {
	return report.Report{
		ExecutionID: 1, RunID: 42,
		ErrorRate: 0.02,
		Latency:   report.Percentiles{95: 0.25, 99: 0.4},
		Achieved:  report.Load{Throughput: 80},
	}
}

func TestValidateAcceptsLegalThresholds(t *testing.T) {
	t.Parallel()
	legal := []Threshold{
		mk(MetricHTTPP95MS, ComparisonLT, 300),
		mk(MetricHTTPP99MS, ComparisonLT, 500),
		mk(MetricErrorRate, ComparisonLT, 0.01),
		mk(MetricErrorRate, ComparisonLT, 0), // a zero error rate is a legal bound
		mk(MetricErrorRate, ComparisonGT, 1),
		mk(MetricThroughputQPS, ComparisonGT, 50),
	}
	for _, th := range legal {
		if err := th.Validate(); err != nil {
			t.Errorf("%s %s %v: Validate = %v, want nil", th.Metric, th.Comparison, th.Value, err)
		}
	}
}

func TestValidateRejectsIllegalThresholds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		th   Threshold
		want error
	}{
		{Threshold{Metric: MetricHTTPP95MS, Comparison: ComparisonLT, Value: 1}, ErrScenarioRequired},
		{mk("latency_ms", ComparisonLT, 100), ErrMetricUnknown},
		{mk(MetricHTTPP95MS, "lte", 100), ErrComparisonUnknown},
		{mk(MetricHTTPP95MS, ComparisonLT, 0), ErrValueNotPositive},
		{mk(MetricThroughputQPS, ComparisonGT, -5), ErrValueNotPositive},
		{mk(MetricErrorRate, ComparisonLT, 1.5), ErrValueOutOfRange},
		{mk(MetricErrorRate, ComparisonGT, -0.1), ErrValueOutOfRange},
	}
	for _, tc := range cases {
		if err := tc.th.Validate(); !errors.Is(err, tc.want) {
			t.Errorf("%+v: Validate = %v, want %v", tc.th, err, tc.want)
		}
	}
}

// Each metric extracts the figure sloapp's aggregation reads, in the units
// the metric names: latency crosses seconds to milliseconds exactly once.
func TestObserveExtractsEachMetric(t *testing.T) {
	t.Parallel()
	rep := thresholdRep()
	cases := []struct {
		metric Metric
		want   float64
	}{
		{MetricHTTPP95MS, 250},
		{MetricHTTPP99MS, 400},
		{MetricErrorRate, 0.02},
		{MetricThroughputQPS, 80},
	}
	for _, tc := range cases {
		got, ok, reason := Observe(rep, tc.metric)
		if !ok {
			t.Errorf("Observe(%s) = not ok (%q), want %v", tc.metric, reason, tc.want)
			continue
		}
		if got != tc.want {
			t.Errorf("Observe(%s) = %v, want %v", tc.metric, got, tc.want)
		}
	}
}

// A percentile the report never measured is an absence, not a zero: ok is
// false and a reason names what was missing.
func TestObserveMissingPercentile(t *testing.T) {
	t.Parallel()
	rep := thresholdRep()
	rep.Latency = report.Percentiles{95: 0.25} // no p99
	if _, ok, reason := Observe(rep, MetricHTTPP99MS); ok {
		t.Error("Observe(p99) without p99 in the report = ok, want not ok")
	} else if reason == "" {
		t.Error("missing p99 carries an empty reason; the result needs one")
	}
}

// lt is the ceiling form: strictly under the bound satisfies, at the bound
// does not (the safe side is strict, matching k6).
func TestEvaluateLTMetAndMissed(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0).UTC()

	under := mk(MetricHTTPP95MS, ComparisonLT, 300)
	res := under.Evaluate(thresholdRep(), now)
	if res.Satisfied == nil || !*res.Satisfied {
		t.Errorf("p95 250ms vs lt 300: satisfied = %v, want true", res.Satisfied)
	}
	if res.Observed == nil || *res.Observed != 250 {
		t.Errorf("observed = %v, want 250", res.Observed)
	}

	at := mk(MetricHTTPP95MS, ComparisonLT, 250)
	if res := at.Evaluate(thresholdRep(), now); res.Satisfied == nil || *res.Satisfied {
		t.Errorf("p95 250ms vs lt 250: satisfied = %v, want false (strict)", res.Satisfied)
	}

	over := mk(MetricHTTPP95MS, ComparisonLT, 200)
	res = over.Evaluate(thresholdRep(), now)
	if res.Satisfied == nil || *res.Satisfied {
		t.Errorf("p95 250ms vs lt 200: satisfied = %v, want false", res.Satisfied)
	}
}

// gt is the floor form: strictly above the bound satisfies.
func TestEvaluateGTMetAndMissed(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0).UTC()

	above := mk(MetricThroughputQPS, ComparisonGT, 50)
	if res := above.Evaluate(thresholdRep(), now); res.Satisfied == nil || !*res.Satisfied {
		t.Errorf("qps 80 vs gt 50: satisfied = %v, want true", res.Satisfied)
	}
	below := mk(MetricThroughputQPS, ComparisonGT, 80)
	if res := below.Evaluate(thresholdRep(), now); res.Satisfied == nil || *res.Satisfied {
		t.Errorf("qps 80 vs gt 80: satisfied = %v, want false (strict)", res.Satisfied)
	}
}

// A missing metric grades to unknown: observed and satisfied stay nil and a
// reason says why -- never a fabricated fail.
func TestEvaluateMissingMetricIsUnknownNotFailed(t *testing.T) {
	t.Parallel()
	rep := thresholdRep()
	rep.Latency = report.Percentiles{} // no percentiles at all
	res := mk(MetricHTTPP99MS, ComparisonLT, 500).Evaluate(rep, time.Unix(5, 0).UTC())
	if res.Observed != nil {
		t.Errorf("observed = %v, want nil", res.Observed)
	}
	if res.Satisfied != nil {
		t.Errorf("satisfied = %v, want nil (unknown, not failed)", res.Satisfied)
	}
	if res.Reason == "" {
		t.Error("reason is empty; an unknown result must say why")
	}
}

// The result carries the run's identity and a snapshot of the definition, so
// a result stays legible after the operator re-edits the thresholds.
func TestEvaluateStampsIdentityAndSnapshot(t *testing.T) {
	t.Parallel()
	now := time.Unix(1234, 0).UTC()
	th := Threshold{ID: 9, ScenarioID: 7, Metric: MetricErrorRate, Comparison: ComparisonLT, Value: 0.05}
	res := th.Evaluate(thresholdRep(), now)
	if res.ThresholdID != 9 || res.ExecutionID != 1 || res.RunID != 42 {
		t.Errorf("identity = %d/%d/%d, want threshold 9, execution 1, run 42", res.ThresholdID, res.ExecutionID, res.RunID)
	}
	if res.Metric != MetricErrorRate || res.Comparison != ComparisonLT || res.Value != 0.05 {
		t.Errorf("snapshot = %s %s %v, want the definition as evaluated", res.Metric, res.Comparison, res.Value)
	}
	if !res.EvaluatedAt.Equal(now) {
		t.Errorf("evaluated_at = %v, want %v", res.EvaluatedAt, now)
	}
	if res.Satisfied == nil || !*res.Satisfied {
		t.Errorf("error rate 0.02 vs lt 0.05: satisfied = %v, want true", res.Satisfied)
	}
}

func TestEnumMembership(t *testing.T) {
	t.Parallel()
	for _, m := range Metrics {
		if !m.Valid() {
			t.Errorf("enum member %q reports invalid", m)
		}
	}
	for _, c := range Comparisons {
		if !c.Valid() {
			t.Errorf("enum member %q reports invalid", c)
		}
	}
	if !MetricErrorRate.IsFractional() || MetricHTTPP95MS.IsFractional() || MetricThroughputQPS.IsFractional() {
		t.Error("IsFractional is true only for error_rate")
	}
}
