package report_test

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
)

// Phase 99's signal: a soak holds constant load, and a target that leaks
// slows down under it. The trend a report flags is per-second latency
// growth -- mean of each half, least-squares slope of the per-second means,
// and a verdict that requires both halves' ratio and the slope to agree.

// soakMeta is the meta every case here finalises under: a soak-shaped
// request whose numbers the trend never reads (it reads measurements), so
// one constant serves.
func soakMeta() report.Meta {
	return report.Meta{
		ExecutionID: 1, ScenarioID: 2, RunID: 3,
		StartedAt: time.Unix(1000, 0).UTC(), EndedAt: time.Unix(1150, 0).UTC(),
		Outcome:   taurus.OutcomePassed,
		Requested: report.Load{Concurrency: 5, Throughput: 10, DurationSeconds: 150},
	}
}

// soakIntervals is a run of n seconds, one label row per second, every
// sample of second i in the bucket lat(i) returns. The shape a scripted
// soak produces: constant load, latency free to move.
func soakIntervals(base int64, n int, samples int64, lat func(i int) float64) []metrics.Interval {
	out := make([]metrics.Interval, 0, n)
	for i := range n {
		out = append(out, metrics.Interval{
			Timestamp: base + int64(i), Label: "checkout",
			Concurrency: 2, Samples: samples, Succeeded: samples,
			Latency: metrics.Histogram{lat(i): samples},
		})
	}
	return out
}

// linearGrowth returns a bucket function rising from lo to hi across n
// seconds -- the signature a leaking target paints on a soak.
func linearGrowth(lo, hi float64, n int) func(i int) float64 {
	return func(i int) float64 {
		if n <= 1 {
			return lo
		}
		return lo + (hi-lo)*float64(i)/float64(n-1)
	}
}

// approx reports got within ±tol of want, with the failure message a
// table row can print directly.
func approx(got, want, tol float64) bool {
	return math.Abs(got-want) <= tol
}

// A run whose response time rises steadily across the window is the leak
// signature: both halves and the slope must carry it, and the verdict must
// fire only when they agree.
func TestSoakTrend_RisingRunSuspectsLeak(t *testing.T) {
	t.Parallel()

	// 150 seconds rising 0.1s -> 0.4s: first half means ~174.5ms, second
	// ~325.5ms (ratio ~1.86 > 1.5), slope 0.3s/149s ~ 120.8 ms/min.
	acc := report.NewAccumulator()
	for _, iv := range soakIntervals(1000, 150, 10, linearGrowth(0.1, 0.4, 150)) {
		acc.Add(iv)
	}
	rep := acc.Report(soakMeta())

	tr := rep.SoakTrend
	if tr == nil {
		t.Fatalf("no soak trend on a 150-second run: %+v", rep)
	}
	if !tr.LeakSuspected {
		t.Errorf("leak not suspected on a rising run: %+v", tr)
	}
	if !approx(tr.FirstHalfMs, 174.5, 5) {
		t.Errorf("first half = %v ms, want ~174.5", tr.FirstHalfMs)
	}
	if !approx(tr.SecondHalfMs, 325.5, 5) {
		t.Errorf("second half = %v ms, want ~325.5", tr.SecondHalfMs)
	}
	if !approx(tr.SlopeMsPerMin, 120.8, 5) {
		t.Errorf("slope = %v ms/min, want ~120.8", tr.SlopeMsPerMin)
	}
}

// A flat run is a healthy soak: the trend is still reported (a reader
// wants the halves), but nothing is suspected.
func TestSoakTrend_FlatRunIsNotSuspected(t *testing.T) {
	t.Parallel()

	acc := report.NewAccumulator()
	for _, iv := range soakIntervals(1000, 150, 10, func(int) float64 { return 0.2 }) {
		acc.Add(iv)
	}
	rep := acc.Report(soakMeta())

	tr := rep.SoakTrend
	if tr == nil {
		t.Fatalf("no soak trend on a 150-second run: %+v", rep)
	}
	if tr.LeakSuspected {
		t.Errorf("leak suspected on a flat run: %+v", tr)
	}
	if !approx(tr.FirstHalfMs, 200, 1) || !approx(tr.SecondHalfMs, 200, 1) {
		t.Errorf("halves = %v/%v ms, want ~200/200", tr.FirstHalfMs, tr.SecondHalfMs)
	}
	if !approx(tr.SlopeMsPerMin, 0, 0.5) {
		t.Errorf("slope = %v ms/min, want ~0", tr.SlopeMsPerMin)
	}
}

// Two minutes is the floor: shorter windows are noise (and below soak's
// own 60s x N plateau). The boundary itself is pinned from both sides.
func TestSoakTrend_UnderTwoMinutesHasNoTrend(t *testing.T) {
	t.Parallel()

	flat := func(int) float64 { return 0.2 }
	for _, n := range []int{1, 60, 119} {
		acc := report.NewAccumulator()
		for _, iv := range soakIntervals(1000, n, 10, flat) {
			acc.Add(iv)
		}
		if tr := acc.Report(soakMeta()).SoakTrend; tr != nil {
			t.Errorf("%d sampled seconds: trend = %+v, want none", n, tr)
		}
	}

	acc := report.NewAccumulator()
	for _, iv := range soakIntervals(1000, 120, 10, flat) {
		acc.Add(iv)
	}
	if tr := acc.Report(soakMeta()).SoakTrend; tr == nil {
		t.Error("120 sampled seconds: no trend, want one")
	}
}

// Seconds with no latency samples -- an engine that reported concurrency
// but no buckets, or a second lost to a restart before phase 99's state
// existed -- carry no mean and must be skipped, not counted as zeroes.
func TestSoakTrend_ZeroSampleSecondsAreSkipped(t *testing.T) {
	t.Parallel()

	// 180 seconds of which every 6th carries no samples: 150 sampled, still
	// a trend, judged only on the sampled seconds.
	lat := linearGrowth(0.1, 0.4, 150)
	sampled := 0
	acc := report.NewAccumulator()
	for i := range 180 {
		if i%6 == 5 {
			acc.Add(metrics.Interval{
				Timestamp: 1000 + int64(i), Label: "checkout",
				Concurrency: 2, // VUs held; nothing completed
			})
			continue
		}
		for _, iv := range soakIntervals(1000+int64(i), 1, 10, func(int) float64 { return lat(sampled) }) {
			acc.Add(iv)
		}
		sampled++
	}
	rep := acc.Report(soakMeta())
	tr := rep.SoakTrend
	if tr == nil {
		t.Fatalf("no trend with 150 sampled of 180 seconds: %+v", rep)
	}
	if !tr.LeakSuspected {
		t.Errorf("leak not suspected with zero-sample seconds skipped: %+v", tr)
	}
	if !approx(tr.FirstHalfMs, 174.5, 5) || !approx(tr.SecondHalfMs, 325.5, 5) {
		t.Errorf("halves = %v/%v ms, want the sampled-only ~174.5/~325.5",
			tr.FirstHalfMs, tr.SecondHalfMs)
	}

	// A run whose every second is zero-sample has no trend at all, however
	// long: there is no latency to speak of.
	empty := report.NewAccumulator()
	for i := range 150 {
		empty.Add(metrics.Interval{Timestamp: 1000 + int64(i), Label: "checkout", Concurrency: 2})
	}
	if tr := empty.Report(soakMeta()).SoakTrend; tr != nil {
		t.Errorf("trend from no latency samples: %+v", tr)
	}
}

// A first half of zero-valued buckets is not a 1.5x ratio waiting to
// happen: the verdict requires a real first-half figure to compare
// against, so a run that only ever measured zeroes reads unjudged rather
// than infinitely degraded.
func TestSoakTrend_ZeroFirstHalfIsNotSuspected(t *testing.T) {
	t.Parallel()

	acc := report.NewAccumulator()
	for _, iv := range soakIntervals(1000, 150, 10, func(i int) float64 {
		if i < 75 {
			return 0 // buckets keyed 0.0s: sampled, but summed to nothing
		}
		return 0.4
	}) {
		acc.Add(iv)
	}
	tr := acc.Report(soakMeta()).SoakTrend
	if tr == nil {
		t.Fatalf("no trend: %+v", tr)
	}
	if tr.LeakSuspected {
		t.Errorf("leak suspected with a zero first half: %+v", tr)
	}
}

// The per-second latency rides the snapshot like the concurrency does: a
// run that outlives the process measuring it keeps its trend, and what
// was written down rebuilds the same report as uninterrupted measuring.
func TestSoakTrend_SnapshotRoundTrip(t *testing.T) {
	t.Parallel()

	intervals := soakIntervals(1000, 150, 10, linearGrowth(0.1, 0.4, 150))

	first := report.NewAccumulator()
	for _, iv := range intervals[:75] {
		first.Add(iv)
	}
	snap := first.Snapshot()
	if len(snap.Seconds) != 75 {
		t.Fatalf("snapshot seconds = %d, want 75", len(snap.Seconds))
	}
	for _, sec := range snap.Seconds {
		if sec.LatencySamples != 10 || !approx(sec.LatencySum, 0.1*10, 1.5) {
			t.Errorf("second %d latency = %v/%d, want the flat warm-up half's ~1.0s/10",
				sec.Second, sec.LatencySum, sec.LatencySamples)
		}
	}

	resumed := report.Restore(snap)
	for _, iv := range intervals[75:] {
		resumed.Add(iv)
	}
	uninterrupted := report.NewAccumulator()
	for _, iv := range intervals {
		uninterrupted.Add(iv)
	}
	got, want := resumed.Report(soakMeta()), uninterrupted.Report(soakMeta())
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a restart changed the trend:\n got %+v\nwant %+v", got.SoakTrend, want.SoakTrend)
	}
	if want.SoakTrend == nil || !want.SoakTrend.LeakSuspected {
		t.Fatalf("the fixture lost its verdict: %+v", want.SoakTrend)
	}
}

// soakLabelIntervals is one label's own soak window: a row per second,
// every sample of second i in the bucket lat(i) returns, at that label's
// own sample count. The shape phase 103 judges per label.
func soakLabelIntervals(base int64, n int, name string, samples int64, lat func(i int) float64) []metrics.Interval {
	out := make([]metrics.Interval, 0, n)
	for i := range n {
		out = append(out, metrics.Interval{
			Timestamp: base + int64(i), Label: name,
			Concurrency: 2, Samples: samples, Succeeded: samples,
			Latency: metrics.Histogram{lat(i): samples},
		})
	}
	return out
}

// A leak in one label is diluted by its healthy siblings at the aggregate
// level -- phase 103's whole reason. Per label, the same window says exactly
// what each label did: the leaking one is flagged with its own halves and
// slope, the healthy one is reported unflagged, and the aggregate stays
// quiet the whole time.
func TestSoakTrend_PerLabelTrendFlagsOnlyTheLeakingLabel(t *testing.T) {
	t.Parallel()

	// checkout: 10 samples/s rising 0.1s -> 0.4s (ratio ~1.86, slope
	// ~120.8 ms/min). browse: 100 samples/s flat 0.2s -- nine tenths of the
	// traffic, so the aggregate's halves read ~198ms -> ~211ms (ratio ~1.07)
	// and stay under 1.5x.
	acc := report.NewAccumulator()
	for i := range 150 {
		ts := int64(1000 + i)
		leak := linearGrowth(0.1, 0.4, 150)(i)
		acc.Add(metrics.Interval{
			Timestamp: ts, Label: "checkout", Concurrency: 2,
			Samples: 10, Succeeded: 10, Latency: metrics.Histogram{leak: 10},
		})
		acc.Add(metrics.Interval{
			Timestamp: ts, Label: "browse", Concurrency: 2,
			Samples: 100, Succeeded: 100, Latency: metrics.Histogram{0.2: 100},
		})
	}
	rep := acc.Report(soakMeta())

	tr := rep.SoakTrend
	if tr == nil {
		t.Fatalf("no aggregate trend on a 150-second run: %+v", rep)
	}
	if tr.LeakSuspected {
		t.Errorf("aggregate suspected the diluted leak: %+v", tr)
	}
	if len(tr.Labels) != 2 {
		t.Fatalf("label trends = %+v, want one per label", tr.Labels)
	}
	byName := map[string]report.LabelSoakTrend{}
	for _, lt := range tr.Labels {
		byName[lt.Label] = lt
	}
	leaking, ok := byName["checkout"]
	if !ok {
		t.Fatalf("checkout missing from label trends: %+v", tr.Labels)
	}
	if !leaking.LeakSuspected {
		t.Errorf("checkout's own trend did not suspect the leak: %+v", leaking)
	}
	if !approx(leaking.FirstHalfMs, 174.5, 5) || !approx(leaking.SecondHalfMs, 325.5, 5) {
		t.Errorf("checkout halves = %v/%v ms, want ~174.5/~325.5",
			leaking.FirstHalfMs, leaking.SecondHalfMs)
	}
	if !approx(leaking.SlopeMsPerMin, 120.8, 5) {
		t.Errorf("checkout slope = %v ms/min, want ~120.8", leaking.SlopeMsPerMin)
	}
	healthy, ok := byName["browse"]
	if !ok {
		t.Fatalf("browse missing from label trends: %+v", tr.Labels)
	}
	if healthy.LeakSuspected {
		t.Errorf("browse's flat trend suspected a leak: %+v", healthy)
	}
	if !approx(healthy.FirstHalfMs, 200, 1) || !approx(healthy.SecondHalfMs, 200, 1) {
		t.Errorf("browse halves = %v/%v ms, want ~200/200",
			healthy.FirstHalfMs, healthy.SecondHalfMs)
	}
	// Sorted by label name, so two reports of the same run match.
	if tr.Labels[0].Label != "browse" || tr.Labels[1].Label != "checkout" {
		t.Errorf("label trends not sorted by name: %+v", tr.Labels)
	}
}

// The two-minute floor is each label's own, not the window's: a label that
// only reported for part of a long soak has no trend of its own -- its few
// seconds are noise, exactly the aggregate's reasoning.
func TestSoakTrend_SparseLabelHasNoTrend(t *testing.T) {
	t.Parallel()

	acc := report.NewAccumulator()
	// checkout spans the whole 150-second window; browse only its first
	// 100 seconds -- under the 120-second floor even though the run's own
	// window is plenty long.
	for _, iv := range soakLabelIntervals(1000, 150, "checkout", 10, linearGrowth(0.1, 0.4, 150)) {
		acc.Add(iv)
	}
	for _, iv := range soakLabelIntervals(1000, 100, "browse", 10, func(int) float64 { return 0.2 }) {
		acc.Add(iv)
	}
	tr := acc.Report(soakMeta()).SoakTrend
	if tr == nil {
		t.Fatalf("no aggregate trend: %+v", tr)
	}
	if len(tr.Labels) != 1 || tr.Labels[0].Label != "checkout" {
		t.Fatalf("label trends = %+v, want checkout only", tr.Labels)
	}
	if !tr.Labels[0].LeakSuspected {
		t.Errorf("checkout's leak was not suspected: %+v", tr.Labels[0])
	}
}

// A label absent from some seconds is judged only on the seconds it did
// report -- missing observations, not zero-latency ones, the same rule the
// aggregate applies to empty seconds.
func TestSoakTrend_LabelJudgedOnItsOwnSampledSeconds(t *testing.T) {
	t.Parallel()

	// browse reports on 2 of every 3 seconds: exactly 120 sampled of a
	// 180-second window -- at the floor, and judged only on its own 120
	// (flat 0.2s -> halves ~200/200), while checkout reports all 180 and
	// rises.
	acc := report.NewAccumulator()
	leak := linearGrowth(0.1, 0.4, 180)
	for i := range 180 {
		ts := int64(1000 + i)
		acc.Add(metrics.Interval{
			Timestamp: ts, Label: "checkout", Concurrency: 2,
			Samples: 10, Succeeded: 10, Latency: metrics.Histogram{leak(i): 10},
		})
		if i%3 != 0 {
			acc.Add(metrics.Interval{
				Timestamp: ts, Label: "browse", Concurrency: 2,
				Samples: 10, Succeeded: 10, Latency: metrics.Histogram{0.2: 10},
			})
		}
	}
	tr := acc.Report(soakMeta()).SoakTrend
	if tr == nil {
		t.Fatalf("no aggregate trend: %+v", tr)
	}
	var browse *report.LabelSoakTrend
	for i := range tr.Labels {
		if tr.Labels[i].Label == "browse" {
			browse = &tr.Labels[i]
		}
	}
	if browse == nil {
		t.Fatalf("browse trend missing: %+v", tr.Labels)
	}
	if browse.LeakSuspected {
		t.Errorf("browse's two-of-three-seconds flat trend suspected a leak: %+v", browse)
	}
	if !approx(browse.FirstHalfMs, 200, 1) || !approx(browse.SecondHalfMs, 200, 1) {
		t.Errorf("browse halves = %v/%v ms, want ~200/200 -- its own sampled seconds only",
			browse.FirstHalfMs, browse.SecondHalfMs)
	}
}

// The per-label tally rides the snapshot like the aggregate does: a run
// that outlives the process measuring it keeps every label's trend, and
// what was written down rebuilds the same report as uninterrupted
// measuring -- labels included.
func TestSoakTrend_PerLabelSnapshotRoundTrip(t *testing.T) {
	t.Parallel()

	build := func(from, to int) []metrics.Interval {
		out := []metrics.Interval{}
		for i := from; i < to; i++ {
			ts := int64(1000 + i)
			leak := linearGrowth(0.1, 0.4, 150)(i)
			out = append(out,
				metrics.Interval{
					Timestamp: ts, Label: "checkout", Concurrency: 2,
					Samples: 10, Succeeded: 10, Latency: metrics.Histogram{leak: 10},
				},
				metrics.Interval{
					Timestamp: ts, Label: "browse", Concurrency: 2,
					Samples: 100, Succeeded: 100, Latency: metrics.Histogram{0.2: 100},
				})
		}
		return out
	}

	first := report.NewAccumulator()
	for _, iv := range build(0, 75) {
		first.Add(iv)
	}
	snap := first.Snapshot()
	var sawLabelTally bool
	for _, sec := range snap.Seconds {
		if len(sec.LabelLatency) > 0 {
			sawLabelTally = true
			if _, ok := sec.LabelLatency["checkout"]; !ok {
				t.Errorf("second %d: checkout missing from the label tally: %+v",
					sec.Second, sec.LabelLatency)
			}
		}
	}
	if !sawLabelTally {
		t.Error("snapshot carried no per-label latency tally at all")
	}

	resumed := report.Restore(snap)
	for _, iv := range build(75, 150) {
		resumed.Add(iv)
	}
	uninterrupted := report.NewAccumulator()
	for _, iv := range build(0, 150) {
		uninterrupted.Add(iv)
	}
	got, want := resumed.Report(soakMeta()), uninterrupted.Report(soakMeta())
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a restart changed the per-label trends:\n got %+v\nwant %+v",
			got.SoakTrend, want.SoakTrend)
	}
	if want.SoakTrend == nil || len(want.SoakTrend.Labels) != 2 {
		t.Fatalf("the fixture lost its label trends: %+v", want.SoakTrend)
	}
	leaking := false
	for _, lt := range want.SoakTrend.Labels {
		if lt.Label == "checkout" {
			leaking = lt.LeakSuspected
		}
	}
	if !leaking {
		t.Fatalf("the fixture's checkout trend did not suspect its leak: %+v",
			want.SoakTrend.Labels)
	}
}

// A run too short for an aggregate trend has no per-label trends either:
// no label can hold 120 sampled seconds inside a window that does not, so
// Labels rides only an existing trend.
func TestSoakTrend_ShortRunHasNoLabelTrends(t *testing.T) {
	t.Parallel()

	acc := report.NewAccumulator()
	for _, iv := range soakLabelIntervals(1000, 100, "checkout", 10, func(int) float64 { return 0.2 }) {
		acc.Add(iv)
	}
	if tr := acc.Report(soakMeta()).SoakTrend; tr != nil {
		t.Errorf("trend on a 100-second run: %+v", tr)
	}
}

// The engine's own aggregate row re-counts the label rows beside it, so
// its histogram must add nothing to the second it shares: the trend feeds
// on label rows only, the same exclusion the run's own totals use.
func TestSoakTrend_EngineTotalRowAddsNoLatency(t *testing.T) {
	t.Parallel()

	acc := report.NewAccumulator()
	for _, iv := range soakIntervals(1000, 150, 10, linearGrowth(0.1, 0.4, 150)) {
		acc.Add(iv)
		// A total row with its own (here absurd) buckets: counted for
		// concurrency, ignored for latency.
		acc.Add(metrics.Interval{
			Timestamp: iv.Timestamp, Label: report.TotalLabel,
			Concurrency: 2, Latency: metrics.Histogram{5: 100},
		})
	}
	tr := acc.Report(soakMeta()).SoakTrend
	if tr == nil {
		t.Fatalf("no trend: %+v", tr)
	}
	if !approx(tr.FirstHalfMs, 174.5, 5) || !approx(tr.SlopeMsPerMin, 120.8, 5) {
		t.Errorf("total row leaked into the trend: %+v", tr)
	}
}
