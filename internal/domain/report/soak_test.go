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
