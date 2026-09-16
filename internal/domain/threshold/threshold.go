// Package threshold holds the scenario threshold: a k6-style pass/fail bound
// an operator pins to one scenario's metrics, and the result of grading one
// run's report against it.
//
// Thresholds judge; they never verdict. A run's outcome stays the engine's
// own report -- threshold results are additive evidence a reader weighs, the
// same way the execution criteria verdict on the report response is.
//
// The metric extraction reads exactly the stored-report fields the SLO
// budget aggregation (sloapp) reads -- Latency[95]/Latency[99] (seconds,
// crossed over to milliseconds at the boundary), ErrorRate, and the
// achieved throughput -- so there is one accounting of what a run produced,
// not a second parser.
//
// Pure domain: no I/O.
package threshold

import (
	"errors"
	"fmt"
	"time"

	"github.com/heridotlife/honryu/internal/domain/report"
)

// Validation errors. Callers compare with errors.Is.
var (
	// ErrMetricUnknown rejects a metric outside the enum.
	ErrMetricUnknown = errors.New("threshold: unknown metric")
	// ErrComparisonUnknown rejects a comparison outside lt|gt.
	ErrComparisonUnknown = errors.New("threshold: unknown comparison")
	// ErrValueNotPositive rejects a latency/throughput bound of zero or
	// less: a threshold no real run can satisfy reads as a verdict on
	// nothing.
	ErrValueNotPositive = errors.New("threshold: value must be a positive number for this metric")
	// ErrValueOutOfRange rejects an error-rate bound outside 0..1, the
	// range the report's own error rate lives in.
	ErrValueOutOfRange = errors.New("threshold: error_rate value must be between 0 and 1")
	// ErrScenarioRequired rejects a threshold with no scenario to belong to.
	ErrScenarioRequired = errors.New("threshold: a valid scenario id is required")
)

// Metric names the figure of a run's report a threshold judges. The wire
// spells the unit in the metric itself, the convention sloapp's P95MS
// established.
type Metric string

const (
	// MetricHTTPP95MS is the p95 response time in milliseconds (reports
	// keep seconds; the cutover is the extraction's, exactly once).
	MetricHTTPP95MS Metric = "http_p95_ms"
	// MetricHTTPP99MS is the p99 response time in milliseconds.
	MetricHTTPP99MS Metric = "http_p99_ms"
	// MetricErrorRate is the share of failed requests, 0..1.
	MetricErrorRate Metric = "error_rate"
	// MetricThroughputQPS is the throughput the run actually achieved,
	// requests per second.
	MetricThroughputQPS Metric = "throughput_qps"
)

// Metrics is the full enum, in the stable order the editor lists them.
var Metrics = []Metric{MetricHTTPP95MS, MetricHTTPP99MS, MetricErrorRate, MetricThroughputQPS}

// Valid reports whether m is one of the enum's values.
func (m Metric) Valid() bool {
	switch m {
	case MetricHTTPP95MS, MetricHTTPP99MS, MetricErrorRate, MetricThroughputQPS:
		return true
	default:
		return false
	}
}

// IsFractional reports whether m's observed values live in 0..1 (error
// rate), which bounds what a threshold on it may say.
func (m Metric) IsFractional() bool { return m == MetricErrorRate }

// Comparison is which side of the value satisfies the threshold. Strict on
// both sides, matching k6: an observed value exactly at the bound sits on
// the safe side of lt and the short side of gt.
type Comparison string

const (
	// ComparisonLT is satisfied when the observed value is below the bound
	// -- the ceiling form ("p95 must stay under 300ms").
	ComparisonLT Comparison = "lt"
	// ComparisonGT is satisfied when the observed value is above the bound
	// -- the floor form ("throughput must exceed 50 req/s").
	ComparisonGT Comparison = "gt"
)

// Comparisons is the full enum, in the stable order the editor lists them.
var Comparisons = []Comparison{ComparisonLT, ComparisonGT}

// Valid reports whether c is one of the enum's values.
func (c Comparison) Valid() bool {
	switch c {
	case ComparisonLT, ComparisonGT:
		return true
	default:
		return false
	}
}

// Threshold is one scenario-scoped bound: metric, direction, value.
type Threshold struct {
	// ID is the storage-assigned row identity; zero before Replace assigns
	// one.
	ID int64
	// ScenarioID is the scenario the bound grades.
	ScenarioID int64
	Metric     Metric
	Comparison Comparison
	// Value is the bound in the metric's own unit (milliseconds for the
	// latency metrics, a 0..1 fraction for error_rate, requests/second for
	// throughput).
	Value float64
	// CreatedTime is when the bound was defined. The store assigns it;
	// zero before persistence.
	CreatedTime time.Time
}

// Validate checks a threshold's own invariants, independent of persistence:
// a known metric, a known comparison, and a value legal for that metric --
// any positive number for the latency and throughput metrics (a zero bound
// can never be satisfied or is trivially so, and says nothing), any value in
// 0..1 for the fractional error rate.
func (t Threshold) Validate() error {
	switch {
	case t.ScenarioID <= 0:
		return ErrScenarioRequired
	case !t.Metric.Valid():
		return fmt.Errorf("%w: %q", ErrMetricUnknown, t.Metric)
	case !t.Comparison.Valid():
		return fmt.Errorf("%w: %q", ErrComparisonUnknown, t.Comparison)
	}
	if t.Metric.IsFractional() {
		if t.Value < 0 || t.Value > 1 {
			return ErrValueOutOfRange
		}
		return nil
	}
	if t.Value <= 0 {
		return ErrValueNotPositive
	}
	return nil
}

// Result is the record of grading one run's report against one threshold:
// what was looked for, what was seen, and whether it met the bound. The
// threshold's definition rides along as a snapshot -- a result is historical
// evidence, and must stay legible even after the operator re-edits the
// scenario's thresholds.
type Result struct {
	// ThresholdID is the definition the run was graded against. Dangling
	// once that definition is deleted (its results cascade with it).
	ThresholdID int64
	// ExecutionID and RunID locate the graded run. The run id is the
	// report's own key: results are per run, never overwritten by a later
	// run of the same execution.
	ExecutionID int64
	RunID       int64

	// The snapshot of the definition at evaluation time.
	Metric     Metric
	Comparison Comparison
	Value      float64

	// Observed is the report's own figure for the metric, nil when the
	// report carries none (a percentile the engine never measured).
	Observed *float64
	// Satisfied is whether the observed figure met the bound; nil when
	// there was nothing to compare -- a missing metric is unknown, not
	// failed.
	Satisfied *bool
	// Reason states why satisfied is nil (and which figure was missing).
	// Empty for a plain pass/fail.
	Reason string
	// EvaluatedAt is when the grading ran. The store assigns it.
	EvaluatedAt time.Time
}

// Observe extracts a metric's figure from a stored report, the same reading
// sloapp's budget aggregation performs: percentiles out of the report's
// merged-bucket map (seconds in, milliseconds out -- the one unit cutover,
// named in the metric itself), the error rate as stored, and the achieved
// throughput. ok is false -- and the second return value says why -- when the
// report carries no figure for the metric.
func Observe(rep report.Report, m Metric) (value float64, ok bool, reason string) {
	switch m {
	case MetricHTTPP95MS:
		secs, ok := rep.Latency[95]
		if !ok {
			return 0, false, "no p95 latency in the report"
		}
		return secs * 1000, true, ""
	case MetricHTTPP99MS:
		secs, ok := rep.Latency[99]
		if !ok {
			return 0, false, "no p99 latency in the report"
		}
		return secs * 1000, true, ""
	case MetricErrorRate:
		// Every report carries one: zero failures is a figure, not an
		// absence.
		return rep.ErrorRate, true, ""
	case MetricThroughputQPS:
		// The achieved rate, the figure ShortOfRequest judges shortfalls
		// by -- the honest reading of what the run produced.
		return rep.Achieved.Throughput, true, ""
	default:
		return 0, false, "unknown metric " + string(m)
	}
}

// satisfied applies the strict comparison.
func satisfied(c Comparison, observed, value float64) bool {
	if c == ComparisonGT {
		return observed > value
	}
	return observed < value
}

// Evaluate grades one run's report against this threshold: the extracted
// figure, the comparison, and the verdict. EvaluatedAt is the caller's now
// -- evaluation is stamping evidence, and evidence carries its moment.
func (t Threshold) Evaluate(rep report.Report, now time.Time) Result {
	res := Result{
		ThresholdID: t.ID,
		ExecutionID: rep.ExecutionID,
		RunID:       rep.RunID,
		Metric:      t.Metric,
		Comparison:  t.Comparison,
		Value:       t.Value,
		EvaluatedAt: now,
	}
	observed, ok, reason := Observe(rep, t.Metric)
	if !ok {
		res.Reason = reason
		return res
	}
	res.Observed = &observed
	out := satisfied(t.Comparison, observed, t.Value)
	res.Satisfied = &out
	return res
}
