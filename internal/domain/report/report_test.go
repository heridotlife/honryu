package report_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
)

func interval(ts int64, label string, samples, failed int64, latency metrics.Histogram) metrics.Interval {
	return metrics.Interval{
		Timestamp: ts, Label: label, Concurrency: 5,
		Samples: samples, Succeeded: samples - failed, Failed: failed,
		Latency: latency,
	}
}

// A report is built from the intervals a run's pods pushed. Its numbers must be
// what those intervals actually said, since every judgement downstream rests on
// them.
func TestBuild_SummarisesARun(t *testing.T) {
	t.Parallel()

	started := time.Unix(1000, 0)
	in := report.Input{
		ExecutionID: 7, ScenarioID: 11, RunID: 3,
		Engine:    taurus.ExecutorJMeter,
		StartedAt: started,
		EndedAt:   started.Add(time.Minute),
		Requested: report.Load{Concurrency: 50, Throughput: 500, DurationSeconds: 60},
		Outcome:   taurus.OutcomePassed,
		Intervals: []metrics.Interval{
			interval(1000, "checkout-cart", 100, 0, metrics.Histogram{0.01: 100}),
			interval(1001, "checkout-cart", 100, 10, metrics.Histogram{0.01: 90, 0.5: 10}),
		},
	}

	rep := report.Build(in)

	if rep.ExecutionID != 7 || rep.ScenarioID != 11 || rep.RunID != 3 {
		t.Errorf("identity = %+v", rep)
	}
	if rep.Engine != taurus.ExecutorJMeter {
		t.Errorf("engine = %q", rep.Engine)
	}
	if rep.Achieved.Samples != 200 || rep.Achieved.Failed != 10 {
		t.Errorf("achieved = %+v, want 200 samples with 10 failures", rep.Achieved)
	}
	// Achieved throughput is what was measured, not what was asked for: 200
	// samples over the two seconds that actually carried them (ts 1000, 1001),
	// not the requested 500/s and not the 60s wall clock.
	if got := rep.Achieved.Throughput; got != 100 {
		t.Errorf("achieved throughput = %v, want 100/s over the 2 measured seconds", got)
	}
	if rep.Requested.Concurrency != 50 || rep.Requested.Throughput != 500 {
		t.Errorf("requested = %+v", rep.Requested)
	}
	if rep.ErrorRate < 0.049 || rep.ErrorRate > 0.051 {
		t.Errorf("error rate = %v, want 0.05", rep.ErrorRate)
	}
}

// The correlation id is the run's identity in a customer's APM: it rides on the
// report beside the run's time window, so a reader jumps straight from "this
// run looked bad" into their own telemetry for exactly that run.
func TestBuild_CarriesTheRunsCorrelationID(t *testing.T) {
	t.Parallel()

	in := report.Input{
		ExecutionID: 7, ScenarioID: 11, RunID: 3,
		CorrelationID: "4bf92f3577b34da6a3ce929d0e0e4736",
		Outcome:       taurus.OutcomePassed,
	}
	rep := report.Build(in)
	if rep.CorrelationID != in.CorrelationID {
		t.Fatalf("report correlation = %q, want %q", rep.CorrelationID, in.CorrelationID)
	}

	// On the wire it is correlation_id, omitted when empty -- a run from
	// before the field existed reports nothing, not a null.
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"correlation_id":"4bf92f3577b34da6a3ce929d0e0e4736"`) {
		t.Errorf("json %s does not carry correlation_id", raw)
	}
	empty, err := json.Marshal(report.Report{ExecutionID: 1, RunID: 1, Outcome: taurus.OutcomePassed})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if strings.Contains(string(empty), "correlation_id") {
		t.Errorf("json %s carries correlation_id for an empty value; want it omitted", empty)
	}
}

// Percentiles come from the merged buckets of every pod, which is the only way
// to get them right for a sharded run.
func TestBuild_PercentilesComeFromMergedBuckets(t *testing.T) {
	t.Parallel()

	in := report.Input{
		Requested: report.Load{DurationSeconds: 10},
		Intervals: []metrics.Interval{
			// Two pods, same second. One saw a slow tail the other never did.
			interval(1, "probe", 100, 0, metrics.Histogram{0.01: 100}),
			interval(1, "probe", 100, 0, metrics.Histogram{0.01: 90, 2.0: 10}),
		},
	}
	rep := report.Build(in)

	// 10 slow of 200 is 5%, so p95 is the boundary and p99 is deep in the tail.
	if got := rep.Latency[99]; got != 2.0 {
		t.Errorf("p99 = %v, want 2.0 from the merged buckets", got)
	}
	if got := rep.Latency[50]; got != 0.01 {
		t.Errorf("p50 = %v, want 0.01", got)
	}
	// Averaging the pods' own percentiles would have hidden the tail entirely:
	// one pod's p99 is 0.01.
	if rep.Latency[99] == 0.01 {
		t.Error("the tail one pod saw was lost")
	}
}

// A report is per request label as well as overall, since a service owner needs
// to know which request degraded, not just that something did.
func TestBuild_BreaksDownByLabel(t *testing.T) {
	t.Parallel()

	in := report.Input{
		Requested: report.Load{DurationSeconds: 10},
		Intervals: []metrics.Interval{
			interval(1, "checkout-cart", 100, 0, metrics.Histogram{0.01: 100}),
			interval(1, "checkout-pay", 50, 25, metrics.Histogram{1.0: 50}),
			interval(2, "checkout-cart", 100, 0, metrics.Histogram{0.01: 100}),
		},
	}
	rep := report.Build(in)

	if len(rep.Labels) != 2 {
		t.Fatalf("labels = %d, want 2: %+v", len(rep.Labels), rep.Labels)
	}
	byLabel := map[string]report.LabelSummary{}
	for _, l := range rep.Labels {
		byLabel[l.Label] = l
	}
	cart, pay := byLabel["checkout-cart"], byLabel["checkout-pay"]
	if cart.Samples != 200 || cart.Failed != 0 {
		t.Errorf("checkout-cart = %+v", cart)
	}
	if pay.Samples != 50 || pay.Failed != 25 {
		t.Errorf("checkout-pay = %+v", pay)
	}
	if pay.ErrorRate < 0.49 || pay.ErrorRate > 0.51 {
		t.Errorf("checkout-pay error rate = %v, want 0.5", pay.ErrorRate)
	}
	// The failing request is the one worth finding, so it must be distinguishable
	// from the healthy one rather than averaged with it.
	if cart.ErrorRate >= pay.ErrorRate {
		t.Error("a healthy request and a failing one report the same error rate")
	}
}

// bzt reports an aggregate under its own label. Counting it alongside the real
// requests would double every total.
func TestBuild_ExcludesTheEngineAggregateLabel(t *testing.T) {
	t.Parallel()

	in := report.Input{
		Requested: report.Load{DurationSeconds: 10},
		Intervals: []metrics.Interval{
			interval(1, "probe", 100, 0, metrics.Histogram{0.01: 100}),
			interval(1, report.TotalLabel, 100, 0, metrics.Histogram{0.01: 100}),
		},
	}
	rep := report.Build(in)

	if rep.Achieved.Samples != 100 {
		t.Errorf("achieved samples = %d, want 100; the engine's own total was counted twice",
			rep.Achieved.Samples)
	}
	for _, l := range rep.Labels {
		if l.Label == report.TotalLabel {
			t.Error("the engine's aggregate label appears as a request")
		}
	}
}

// A run that measured nothing is a real outcome -- the target refused every
// connection, or the engine died at once -- and must summarise cleanly rather
// than dividing by zero.
func TestBuild_EmptyRun(t *testing.T) {
	t.Parallel()

	rep := report.Build(report.Input{
		ExecutionID: 1, Outcome: taurus.OutcomeError,
		Requested: report.Load{Concurrency: 10, DurationSeconds: 60},
	})

	if rep.Achieved.Samples != 0 || rep.ErrorRate != 0 {
		t.Errorf("empty run = %+v", rep)
	}
	if len(rep.Labels) != 0 {
		t.Errorf("labels = %+v, want none", rep.Labels)
	}
	if rep.Outcome != taurus.OutcomeError {
		t.Errorf("outcome = %q", rep.Outcome)
	}
}

// The report records how far the run fell short of what was asked for. A run
// that produced a fraction of the requested load says little about the target,
// and a reader must be able to see that rather than trust the latency.
func TestBuild_ReportsLoadShortfall(t *testing.T) {
	t.Parallel()

	in := report.Input{
		Requested: report.Load{Concurrency: 50, Throughput: 1000, DurationSeconds: 10},
		Intervals: []metrics.Interval{
			interval(1, "probe", 100, 0, metrics.Histogram{0.01: 100}),
		},
	}
	rep := report.Build(in)

	// 100 samples over 10s is 10/s against 1000/s requested.
	if !rep.ShortOfRequest() {
		t.Error("a run at 1% of the requested rate does not report a shortfall")
	}

	full := in
	full.Requested.Throughput = 10
	if report.Build(full).ShortOfRequest() {
		t.Error("a run that met its requested rate reports a shortfall")
	}

	// With no requested rate there is nothing to fall short of.
	unlimited := in
	unlimited.Requested.Throughput = 0
	if report.Build(unlimited).ShortOfRequest() {
		t.Error("an unlimited run reports a shortfall")
	}
}

// A shard reports only the virtual users it is running. The run's concurrency is
// what every shard had in flight at the same moment, so taking the largest
// single shard would report a sharded run as a fraction of its size -- and
// Phase 3 exists to make runs sharded.
func TestBuild_ConcurrencySumsAcrossShards(t *testing.T) {
	t.Parallel()

	rep := report.Build(report.Input{
		ExecutionID: 1, RunID: 1, Outcome: taurus.OutcomePassed,
		Requested: report.Load{Concurrency: 50, DurationSeconds: 60},
		Intervals: []metrics.Interval{
			// Two pods, 25 users each, the same second.
			{Timestamp: 1000, Label: "probe", Concurrency: 25, Samples: 100, Succeeded: 100},
			{Timestamp: 1000, Label: "probe", Concurrency: 25, Samples: 100, Succeeded: 100},
			// A second later one pod has ramped down.
			{Timestamp: 1001, Label: "probe", Concurrency: 25, Samples: 100, Succeeded: 100},
			{Timestamp: 1001, Label: "probe", Concurrency: 10, Samples: 40, Succeeded: 40},
		},
	})

	if got := rep.Achieved.Concurrency; got != 50 {
		t.Errorf("achieved concurrency = %d, want 50 -- the peak across both shards", got)
	}
}

// Within one shard a virtual user is executing one request at a time, so its
// users are split across the labels it reports. Summing them recovers the
// shard's total; taking the largest label would lose the rest.
func TestBuild_ConcurrencySumsAcrossLabels(t *testing.T) {
	t.Parallel()

	rep := report.Build(report.Input{
		ExecutionID: 1, RunID: 1, Outcome: taurus.OutcomePassed,
		Requested: report.Load{Concurrency: 10, DurationSeconds: 60},
		Intervals: []metrics.Interval{
			{Timestamp: 1000, Label: "checkout-cart", Concurrency: 6, Samples: 60, Succeeded: 60},
			{Timestamp: 1000, Label: "checkout-pay", Concurrency: 4, Samples: 40, Succeeded: 40},
		},
	})

	if got := rep.Achieved.Concurrency; got != 10 {
		t.Errorf("achieved concurrency = %d, want 10 across both labels", got)
	}
}

// The engine sends its own per-shard aggregate alongside the per-label rows. Its
// requests are excluded to avoid double counting, but its concurrency is the
// shard's true total and is the better figure where it exists.
func TestBuild_ConcurrencyPrefersTheEngineAggregate(t *testing.T) {
	t.Parallel()

	rep := report.Build(report.Input{
		ExecutionID: 1, RunID: 1, Outcome: taurus.OutcomePassed,
		Requested: report.Load{Concurrency: 40, DurationSeconds: 60},
		Intervals: []metrics.Interval{
			// Shard one: the aggregate knows about users the labels miss, e.g.
			// users in think-time between requests.
			{Timestamp: 1000, Label: "probe", Concurrency: 15, Samples: 100, Succeeded: 100},
			{Timestamp: 1000, Label: "__total__", Concurrency: 20, Samples: 100, Succeeded: 100},
			// Shard two, likewise.
			{Timestamp: 1000, Label: "probe", Concurrency: 16, Samples: 100, Succeeded: 100},
			{Timestamp: 1000, Label: "__total__", Concurrency: 20, Samples: 100, Succeeded: 100},
		},
	})

	if got := rep.Achieved.Concurrency; got != 40 {
		t.Errorf("achieved concurrency = %d, want 40 from the shards' own aggregates", got)
	}
	// The aggregate's samples are still excluded.
	if got := rep.Achieved.Samples; got != 200 {
		t.Errorf("samples = %d, want 200 -- the aggregate must not be counted twice", got)
	}
}

// A second in which every user was between requests completes no request, so the
// engine reports its aggregate for that second and no label row at all. Those
// users were still running the test and still loading the target.
func TestBuild_ConcurrencyCountsSecondsWithNoCompletedRequests(t *testing.T) {
	t.Parallel()

	rep := report.Build(report.Input{
		ExecutionID: 1, RunID: 1, Outcome: taurus.OutcomePassed,
		Requested: report.Load{Concurrency: 30, DurationSeconds: 60},
		Intervals: []metrics.Interval{
			{Timestamp: 1000, Label: "probe", Concurrency: 10, Samples: 100, Succeeded: 100},
			// Everyone in think-time: no request finished this second.
			{Timestamp: 1001, Label: "__total__", Concurrency: 30},
		},
	})

	if got := rep.Achieved.Concurrency; got != 30 {
		t.Errorf("achieved concurrency = %d, want 30 -- users between requests still count", got)
	}
	if got := rep.Achieved.Samples; got != 100 {
		t.Errorf("samples = %d, want 100", got)
	}
}

// A run that was aborted did not hold load for the duration it was asked for.
// Reporting the requested duration would divide its samples by a window it never
// filled, understating the rate it actually reached -- and then ShortOfRequest
// would call a run that met its target rate a shortfall. The duration comes from
// the seconds the measurements themselves cover, not the wall clock and not the
// requested duration.
func TestBuild_AchievedDurationIsMeasuredNotRequested(t *testing.T) {
	t.Parallel()

	// Ten seconds of load at 100/s, then aborted -- a realistic per-second
	// stream, one interval per second it actually ran.
	var intervals []metrics.Interval
	for ts := int64(1000); ts < 1010; ts++ {
		intervals = append(intervals, metrics.Interval{
			Timestamp: ts, Label: "probe", Concurrency: 10, Samples: 100, Succeeded: 100,
		})
	}
	started := time.Unix(1000, 0)
	rep := report.Build(report.Input{
		ExecutionID: 1, RunID: 1, Outcome: taurus.OutcomeAborted,
		// A generous wall clock that outlasts the load -- teardown, a late
		// finalize -- must not dilute the rate.
		StartedAt: started,
		EndedAt:   started.Add(45 * time.Second),
		Requested: report.Load{Concurrency: 10, Throughput: 100, DurationSeconds: 60},
		Intervals: intervals,
	})

	if got := rep.Achieved.DurationSeconds; got != 10 {
		t.Errorf("achieved duration = %ds, want the 10s it actually ran", got)
	}
	if got := rep.Achieved.Throughput; got < 99 || got > 101 {
		t.Errorf("achieved throughput = %v, want about 100/s over the 10s it ran", got)
	}
	// It reached the rate it was asked for; it simply stopped early. The outcome
	// says it was aborted -- the throughput must not also claim a shortfall.
	if rep.ShortOfRequest() {
		t.Error("a run that met its target rate before being aborted reports a shortfall")
	}
}

// The measured span is the denominator even with no wall clock to fall back
// on -- the seconds the samples cover are load, by definition, whatever the
// requested duration said.
func TestBuild_AchievedDurationComesFromMeasuredSpan(t *testing.T) {
	t.Parallel()

	// 20 seconds of load at 10/s, no StartedAt/EndedAt supplied at all.
	var intervals []metrics.Interval
	for ts := int64(1); ts <= 20; ts++ {
		intervals = append(intervals, interval(ts, "probe", 10, 0, metrics.Histogram{0.01: 10}))
	}
	rep := report.Build(report.Input{
		ExecutionID: 1, RunID: 1, Outcome: taurus.OutcomePassed,
		Requested: report.Load{Concurrency: 10, DurationSeconds: 999}, // deliberately wrong
		Intervals: intervals,
	})

	if got := rep.Achieved.DurationSeconds; got != 20 {
		t.Errorf("achieved duration = %d, want the 20 measured seconds", got)
	}
	if got := rep.Achieved.Throughput; got != 10 {
		t.Errorf("throughput = %v, want 10/s", got)
	}
}

// An engine boots for seconds after the run's clock starts (StartRun stamps
// StartedAt at trigger; the engine only emits its first sample once it has
// booted). That boot dead time is in the wall clock but not in the load, so
// dividing by the wall clock understates the achieved rate -- which is what
// made every live calibration step read as engine-saturated and drove the
// search downward. The duration must exclude it: the samples span only the
// seconds load was actually produced.
func TestBuild_ThroughputExcludesEngineBootDeadTime(t *testing.T) {
	t.Parallel()

	// The run's clock starts at ts=1000, but the engine's first sample lands
	// at ts=1015 (15s of boot) and load then holds for 60s at 1000/s.
	started := time.Unix(1000, 0)
	var intervals []metrics.Interval
	for ts := int64(1015); ts < 1075; ts++ {
		intervals = append(intervals, metrics.Interval{
			Timestamp: ts, Label: "get", Concurrency: 200, Samples: 1000, Succeeded: 1000,
			Latency: metrics.Histogram{0.002: 1000},
		})
	}
	rep := report.Build(report.Input{
		ExecutionID: 1, RunID: 1, Outcome: taurus.OutcomePassed,
		StartedAt: started,
		EndedAt:   started.Add(77 * time.Second), // 15s boot + 60s load + ~2s teardown
		Requested: report.Load{Concurrency: 200, Throughput: 1000, DurationSeconds: 60},
		Intervals: intervals,
	})

	// 60 measured seconds, not the 77s wall clock -- so 1000/s, not ~780/s.
	if got := rep.Achieved.DurationSeconds; got != 60 {
		t.Errorf("achieved duration = %ds, want the 60s of actual load", got)
	}
	if got := rep.Achieved.Throughput; got != 1000 {
		t.Errorf("achieved throughput = %v, want 1000/s (boot dead time excluded)", got)
	}
	// The whole point: at the true rate this run met its request and must not
	// be mistaken for a shortfall (which is what flags engine saturation).
	if rep.ShortOfRequest() {
		t.Error("a run that met its requested rate is reported as short -- boot dead time leaked into the denominator")
	}
}

// A paced run's measurement span stretches past its hold: ramp-up and drain
// fall inside the span but complete few or no requests, so samples over span
// can never reach the requested rate even when the engine kept pace throughout
// -- 30s at a requested 10/s tops out at ~8.1/s over a 37s span. The two
// readings must part ways here: ShortOfRequest keeps the (deflated) rate, which
// is what a human reader of a trend sees; ShortOfVolume judges the sample
// count against what the hold implies, which is what calibration's search --
// the only caller that acts on the verdict -- must use to avoid calling a
// healthy engine saturated (phase 53, exec 16).
func TestShortOfVolume_JudgesTheHoldNotTheInflatedSpan(t *testing.T) {
	t.Parallel()

	// A calibration step as the engine actually ran it: 5s ramp-up with no
	// completions, a 30s hold, then a 2s drain -- 37 measured seconds. What
	// varies is the volume the hold itself produced.
	build := func(requested report.Load, holdPerSecond int64) report.Report {
		var intervals []metrics.Interval
		for ts := int64(1000); ts < 1037; ts++ {
			perSec := int64(0)
			if ts >= 1005 && ts < 1035 {
				perSec = holdPerSecond // the hold
			}
			intervals = append(intervals, interval(ts, "probe", perSec, 0, metrics.Histogram{0.002: perSec}))
		}
		return report.Build(report.Input{
			ExecutionID: 1, RunID: 1, Outcome: taurus.OutcomePassed,
			Requested: requested,
			Intervals: intervals,
		})
	}

	// The incident's own shape: the engine filled the hold exactly (300
	// samples at the requested 10/s), yet the span-deflated rate reads 8.1/s.
	kept := build(report.Load{Throughput: 10, DurationSeconds: 30}, 10)
	if got := kept.Achieved.Samples; got != 300 {
		t.Errorf("samples = %d, want the 300 the hold implies", got)
	}
	if got := kept.Achieved.DurationSeconds; got != 37 {
		t.Errorf("span = %ds, want the 37 measured seconds (ramp-up + hold + drain)", got)
	}
	if got := kept.Achieved.Throughput; got > 9.5 {
		t.Errorf("throughput = %v, want the span-deflated ~%.1f -- the scenario only bites when the rate reads short", got, 300.0/37)
	}
	// The rate reading flags this run -- documented old behaviour, and correct
	// for a human: the achieved rate genuinely was 8.1/s, not 10.
	if !kept.ShortOfRequest() {
		t.Error("ShortOfRequest = false, want true -- the span-deflated rate IS below the request")
	}
	// The volume reading must clear it: the engine produced every request the
	// hold asked for.
	if kept.ShortOfVolume() {
		t.Error("ShortOfVolume = true for a run that filled its hold exactly -- span inflation leaked into the volume judgement")
	}

	tests := []struct {
		name          string
		requested     report.Load
		holdPerSecond int64
		wantRequest   bool
		wantVolume    bool
	}{
		{
			name:          "kept pace: volume clears, span-deflated rate does not",
			requested:     report.Load{Throughput: 10, DurationSeconds: 30},
			holdPerSecond: 10,
			wantRequest:   true,
			wantVolume:    false,
		},
		{
			name:          "genuine shortfall: half the hold's volume is short by both readings",
			requested:     report.Load{Throughput: 10, DurationSeconds: 30},
			holdPerSecond: 5, // 150 samples, and 150/37 also reads short as a rate
			wantRequest:   true,
			wantVolume:    true,
		},
		{
			name:          "unlimited: no requested rate, nothing to fall short of",
			requested:     report.Load{DurationSeconds: 30},
			holdPerSecond: 10,
			wantRequest:   false,
			wantVolume:    false,
		},
		{
			name:          "no hold recorded: volume cannot be judged, rate still can",
			requested:     report.Load{Throughput: 10},
			holdPerSecond: 10,
			wantRequest:   true,
			wantVolume:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := build(tt.requested, tt.holdPerSecond)
			if got := r.ShortOfRequest(); got != tt.wantRequest {
				t.Errorf("ShortOfRequest = %v, want %v", got, tt.wantRequest)
			}
			if got := r.ShortOfVolume(); got != tt.wantVolume {
				t.Errorf("ShortOfVolume = %v, want %v", got, tt.wantVolume)
			}
		})
	}
}

func TestReport_Validate(t *testing.T) {
	t.Parallel()

	valid := func() report.Report {
		return report.Report{ExecutionID: 1, RunID: 2, Outcome: taurus.OutcomePassed}
	}
	cases := []struct {
		name    string
		mutate  func(*report.Report)
		wantErr error
	}{
		{"valid", func(*report.Report) {}, nil},
		{"no execution", func(r *report.Report) { r.ExecutionID = 0 }, report.ErrExecutionRequired},
		{"no run", func(r *report.Report) { r.RunID = 0 }, report.ErrRunRequired},
		{"unknown outcome", func(r *report.Report) { r.Outcome = "maybe" }, report.ErrOutcomeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := valid()
			tc.mutate(&r)
			if err := r.Validate(); !errors.Is(err, tc.wantErr) {
				t.Errorf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// A run that measured nothing has no rate: there is no span to divide by, and
// dividing by zero would make the achieved throughput infinite or NaN, which
// then reaches a report a human reads. (A run that measured anything always has
// a span of at least one second, so this is the only zero-rate case left once
// the duration is taken from the measurements rather than a declared window.)
func TestBuild_EmptyRunHasNoRate(t *testing.T) {
	t.Parallel()

	rep := report.Build(report.Input{
		ExecutionID: 1, RunID: 1,
		Requested: report.Load{Concurrency: 10, DurationSeconds: 60},
		Intervals: nil, // nothing measured
	})
	if rep.Achieved.Throughput != 0 {
		t.Errorf("throughput = %v, want 0 when nothing was measured", rep.Achieved.Throughput)
	}
	if rep.Achieved.Samples != 0 {
		t.Errorf("samples = %d, want 0", rep.Achieved.Samples)
	}
}

// A report crosses the API and the database, so its shape has to survive JSON.
func TestReport_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	rep := report.Build(report.Input{
		ExecutionID: 7, ScenarioID: 11, RunID: 3,
		Engine:    taurus.ExecutorK6,
		StartedAt: time.Unix(1000, 0).UTC(),
		EndedAt:   time.Unix(1060, 0).UTC(),
		Outcome:   taurus.OutcomeFailed,
		Requested: report.Load{Concurrency: 20, Throughput: 100, DurationSeconds: 60},
		Intervals: []metrics.Interval{
			interval(1000, "probe", 200, 20, metrics.Histogram{0.05: 180, 3.0: 20}),
		},
	})

	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got report.Report
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.RunID != rep.RunID || got.Outcome != rep.Outcome || got.Engine != rep.Engine {
		t.Errorf("identity lost: %+v", got)
	}
	if got.Latency[99] != rep.Latency[99] {
		t.Errorf("p99 = %v, want %v", got.Latency[99], rep.Latency[99])
	}
	if len(got.Labels) != 1 || got.Labels[0].Failed != 20 {
		t.Errorf("labels = %+v", got.Labels)
	}
	if !got.StartedAt.Equal(rep.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, rep.StartedAt)
	}
}

func TestPercentiles_UnmarshalRejectsNonNumericKeys(t *testing.T) {
	t.Parallel()

	var p report.Percentiles
	if err := json.Unmarshal([]byte(`{"p95":0.2}`), &p); err == nil {
		t.Error("a non-numeric percentile key was accepted")
	}
	if err := json.Unmarshal([]byte(`[1,2]`), &p); err == nil {
		t.Error("a non-object percentiles value was accepted")
	}
	if err := json.Unmarshal([]byte(`{"95":0.2}`), &p); err != nil {
		t.Fatalf("valid percentiles rejected: %v", err)
	}
	if p[95] != 0.2 {
		t.Errorf("p95 = %v, want 0.2", p[95])
	}
}
