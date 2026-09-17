// Package recsapp is the recommendations use-case: pure rule functions over
// one run's report, in the k6 style -- analyse the telemetry a run already
// produced, surface the handful of best practices each pattern calls for, and
// never more than that.
//
// The rules read exactly the stored-report fields the Checks and Thresholds
// tabs render -- ErrorRate, the merged-bucket latency percentiles, the
// requested-vs-achieved load, the stored threshold results -- so there is one
// accounting of what a run did, not a second parser. A rule fires on its own
// evidence alone, independently of its siblings; the output order is the
// fixed rule order below, never map or store order; a clean report yields an
// empty (never nil) list.
//
// Reachability note (phase 78): the scenario's capacity profile is NOT
// reachable from a report -- a report carries no profile, and this package is
// pure, holding no store dependency. What a report does carry is the
// requested rate, which for a paced scenario is the calibration's product, so
// the throughput rule keys on requested-vs-achieved and skips gracefully (no
// data, no fire) when the scenario asked for no fixed rate.
//
// Pure: no I/O.
package recsapp

import (
	"fmt"

	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/threshold"
)

// Severity grades how loudly a recommendation speaks. Icon and text always
// travel together on the wire's consumers: a severity is never colour-only.
type Severity string

const (
	// SeverityInfo is advisory: worth doing, not wrong to skip.
	SeverityInfo Severity = "info"
	// SeverityWarning flags a pattern in the run's own measurements that
	// usually costs the reader a wrong conclusion if ignored.
	SeverityWarning Severity = "warning"
)

// Rule IDs -- stable, wire-visible identifiers a client can key behaviour on.
const (
	// IDHighErrorRate fires when more than 1% of the run's requests failed.
	IDHighErrorRate = "high-error-rate"
	// IDLatencySpread fires when p99 overshoots p50 by more than the spread
	// factor -- a tail that dominates the experience.
	IDLatencySpread = "latency-spread"
	// IDThresholdMissed fires when the run's stored threshold results name a
	// bound the report missed.
	IDThresholdMissed = "threshold-missed"
	// IDLowThroughput fires when the run delivered materially less load than
	// its scenario requested.
	IDLowThroughput = "low-throughput"
	// IDNoThresholds fires when the scenario defines no thresholds at all.
	IDNoThresholds = "no-thresholds"
)

// highErrorRateThreshold is the error rate above which a run stops reading as
// noise: 1% of requests, the same order k6's own guidance treats as "failing".
const highErrorRateThreshold = 0.01

// latencySpreadFactor is how many times the median the tail may reach before
// the spread itself is the story: p99 beyond 4x p50 means a small fraction of
// requests owns the user-visible latency.
const latencySpreadFactor = 4.0

// Recommendation is one actionable advisory: what to do, why, and where to
// look. Detail is written to be acted on -- a bare observation ("errors are
// high") is not a recommendation.
type Recommendation struct {
	// ID names the rule that fired; stable across runs.
	ID string `json:"id"`
	// Title is the one-line headline.
	Title string `json:"title"`
	// Detail says what to do, why, and where -- the actionable body.
	Detail string `json:"detail"`
	// Severity is info or warning.
	Severity Severity `json:"severity"`
}

// Input is everything the rules read. The report is the engine of the
// analysis; the threshold fields are the scenario-threshold layer's evidence,
// which lives outside the report proper.
type Input struct {
	// Report is the run's stored report.
	Report report.Report
	// ThresholdsKnown reports whether the scenario's threshold set could
	// actually be read (a wired threshold service that answered). The
	// no-thresholds rule fires only when this is true and the set is empty:
	// an unknown set must never be reported as an empty one.
	ThresholdsKnown bool
	// ThresholdsDefined is the scenario's threshold set, definition order.
	ThresholdsDefined []threshold.Threshold
	// ThresholdResults is the run's stored grading evidence, evaluation
	// order.
	ThresholdResults []threshold.Result
}

// Analyze runs every rule over in and returns the fired recommendations in
// the fixed rule order: high error rate, latency spread, threshold missed,
// low throughput, no thresholds. Always non-nil; empty when the report is
// clean. Deterministic: the same input always yields the same list.
func Analyze(in Input) []Recommendation {
	out := make([]Recommendation, 0, 5)
	for _, rec := range []*Recommendation{
		highErrorRateRule(in.Report),
		latencySpreadRule(in.Report),
		thresholdMissedRule(in.ThresholdResults),
		lowThroughputRule(in.Report),
		noThresholdsRule(in.ThresholdsKnown, in.ThresholdsDefined),
	} {
		if rec != nil {
			out = append(out, *rec)
		}
	}
	return out
}

// highErrorRateRule fires above the 1% line: sustained failures usually mean
// the target, and the run's own failing criteria name where to start looking.
func highErrorRateRule(rep report.Report) *Recommendation {
	if rep.ErrorRate <= highErrorRateThreshold {
		return nil
	}
	return &Recommendation{
		ID:    IDHighErrorRate,
		Title: "High error rate",
		Detail: fmt.Sprintf(
			"%.1f%% of this run's requests failed (attribution: %d target-side, %d engine-side, %d unknown). "+
				"Check the target's health and logs for the run window, then read the failing pass/fail criteria on the Checks tab -- they name exactly what tripped.",
			rep.ErrorRate*100, rep.Attribution.Target, rep.Attribution.Engine, rep.Attribution.Unknown,
		),
		Severity: SeverityWarning,
	}
}

// latencySpreadRule fires when p99 outruns p50 beyond the spread factor: a
// heavy tail usually traces to connection handling on the client or lock/GC
// pauses on the target, not to the median path.
func latencySpreadRule(rep report.Report) *Recommendation {
	p50, ok50 := rep.Latency[50]
	p99, ok99 := rep.Latency[99]
	if !ok50 || !ok99 || p50 <= 0 {
		return nil
	}
	if p99 <= latencySpreadFactor*p50 {
		return nil
	}
	return &Recommendation{
		ID:    IDLatencySpread,
		Title: "Wide latency spread",
		Detail: fmt.Sprintf(
			"p99 is %.1fx p50 (%.0f ms vs %.0f ms): a small tail of requests dominates the experience. "+
				"Hunt the slow endpoint on the Labels tab, then check connection reuse and pool sizing on the client and lock or GC pauses on the target.",
			p99/p50, p50*1000, p99*1000,
		),
		Severity: SeverityWarning,
	}
}

// thresholdMissedRule fires when the run's stored grading evidence holds at
// least one missed bound, naming every one of them: either the bound was
// optimistic, or the target regressed.
func thresholdMissedRule(results []threshold.Result) *Recommendation {
	missed := make([]threshold.Result, 0, len(results))
	for _, r := range results {
		if r.Satisfied != nil && !*r.Satisfied {
			missed = append(missed, r)
		}
	}
	if len(missed) == 0 {
		return nil
	}
	title := "Thresholds missed"
	if len(missed) == 1 {
		title = "Threshold missed"
	}
	detail := "This run missed its scenario's pass/fail bounds: "
	for i, r := range missed {
		if i > 0 {
			detail += "; "
		}
		detail += formatMissed(r)
	}
	detail += ". If the bound was set optimistically, recalibrate it in the scenario's threshold editor; otherwise treat this as a regression and diff the run against a baseline on the Overview tab."
	return &Recommendation{
		ID:       IDThresholdMissed,
		Title:    title,
		Detail:   detail,
		Severity: SeverityWarning,
	}
}

// lowThroughputRule fires when the run delivered materially less load than
// its scenario requested -- the report's own requested-vs-achieved reading
// (ShortOfRequest), which is the honest figure for a human reader. No
// requested rate (an open-ended scenario), no fire: there is nothing to fall
// short of, and no capacity-profile data is reachable from a report.
func lowThroughputRule(rep report.Report) *Recommendation {
	if rep.Requested.Throughput <= 0 || !rep.ShortOfRequest() {
		return nil
	}
	return &Recommendation{
		ID:    IDLowThroughput,
		Title: "Throughput below the requested rate",
		Detail: fmt.Sprintf(
			"The engine held %.1f req/s against the scenario's requested %.1f req/s -- under the 95%% pacing tolerance. "+
				"Either the target could not take the load (cross-check the error rate and latency on this page) or the scenario's calibration is stale and deserves a fresh calibration run.",
			rep.Achieved.Throughput, rep.Requested.Throughput,
		),
		Severity: SeverityWarning,
	}
}

// noThresholdsRule fires only when the scenario's set is known and empty: a
// scenario without bounds cannot self-judge, and every future run lands
// unbenchmarked.
func noThresholdsRule(known bool, defined []threshold.Threshold) *Recommendation {
	if !known || len(defined) > 0 {
		return nil
	}
	return &Recommendation{
		ID:    IDNoThresholds,
		Title: "No thresholds configured",
		Detail: "This scenario defines no pass/fail bounds, so no run of it can self-judge. " +
			"Add k6-style thresholds (e.g. \"p95 under 300 ms\") in the scenario's threshold editor to benchmark future runs automatically.",
		Severity: SeverityInfo,
	}
}

// formatMissed renders one missed bound: metric, observed figure, bound.
func formatMissed(r threshold.Result) string {
	s := string(r.Metric)
	if r.Observed != nil {
		s += " observed " + formatMetricValue(r.Metric, *r.Observed)
	} else {
		s += " had no observed value"
	}
	return s + fmt.Sprintf(" against a bound of %s %s", r.Comparison, formatMetricValue(r.Metric, r.Value))
}

// formatMetricValue renders a figure in its metric's own unit -- the wire
// spells the unit in the metric name; the text spells it in the value.
func formatMetricValue(m threshold.Metric, v float64) string {
	switch m {
	case threshold.MetricHTTPP95MS, threshold.MetricHTTPP99MS:
		return fmt.Sprintf("%.0f ms", v)
	case threshold.MetricThroughputQPS:
		return fmt.Sprintf("%.1f req/s", v)
	case threshold.MetricErrorRate:
		return fmt.Sprintf("%.1f%%", v*100)
	default:
		return fmt.Sprintf("%v", v)
	}
}
