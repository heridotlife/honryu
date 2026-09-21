package report

import "sort"

// Phase 99's signal: a soak holds constant load, and a target that leaks
// degrades by slowing down -- at constant throughput, response times trend
// upward across the window. What is detectable from the outside is exactly
// that per-second latency growth (the target's own heap is not measurable
// over HTTP), so that is what the trend carries.

// soakTrendMinSampledSeconds is how many latency-carrying seconds a run
// needs before a trend is computed at all.
//
// Two minutes, for two reasons: shorter windows are noise (a GC pause or a
// cache warm-up paints the same slope a leak does), and soak's own floor is
// 60s x N steps of plateau, so anything under two minutes is not even the
// mode the reading is for. Seconds with no latency samples do not count
// toward the floor -- they carry no mean to trend.
const soakTrendMinSampledSeconds = 120

// soakLeakHalfGrowth is how much the second half's mean latency must exceed
// the first half's before the degradation reads as a leak: 1.5x. The slope
// requirement alongside it keeps a single step change (a deploy mid-run, a
// cache eviction) from firing the verdict on its own -- a leak grows
// throughout, a step does not.
const soakLeakHalfGrowth = 1.5

// Unit conversions for the trend's reported figures: histograms are keyed
// in seconds and the trend regresses per second, while a reader thinks in
// milliseconds and minutes.
const (
	msPerSecond   = 1000
	secondsPerMin = 60
)

// SoakTrend is a run's latency behaviour across its own window: whether
// response time held, and the figures that say so. It is a finding about
// the run, not a verdict input -- the criteria grammar has no slope
// subject, so the trend is reported and never graded.
type SoakTrend struct {
	// FirstHalfMs and SecondHalfMs are the sample-weighted mean response
	// times of the window's two halves, in milliseconds.
	FirstHalfMs  float64 `json:"first_half_ms"`
	SecondHalfMs float64 `json:"second_half_ms"`
	// SlopeMsPerMin is the least-squares slope of the per-second mean
	// response times, in milliseconds per minute.
	SlopeMsPerMin float64 `json:"slope_ms_per_min"`
	// LeakSuspected is the flagged signal: the second half ran materially
	// slower than the first AND the per-second means rose throughout --
	// the signature of a resource leak at steady load.
	LeakSuspected bool `json:"leak_suspected"`
}

// soakTrend computes the trend from a run's per-second state, or nil when
// too few seconds carried latency to say anything (see
// soakTrendMinSampledSeconds).
//
// Seconds with no samples are skipped, not counted as zero means: they are
// missing observations, and a zero would drag both halves and the slope
// toward "healthy" on the strength of seconds that measured nothing. That
// is also what lets a run restarted across phase 99's own deployment keep
// a trend from whatever half of its window the new code measured.
func soakTrend(seconds map[int64]*secondState) *SoakTrend {
	sampled := make([]int64, 0, len(seconds))
	for ts, sec := range seconds {
		if sec.latSamples > 0 {
			sampled = append(sampled, ts)
		}
	}
	if len(sampled) < soakTrendMinSampledSeconds {
		return nil
	}
	sort.Slice(sampled, func(i, j int) bool { return sampled[i] < sampled[j] })

	// The halves are sample-weighted -- each half's mean is its seconds'
	// latSum/latSamples, not the average of its per-second means -- so a
	// sparse second (one request) cannot move a half the way a busy one
	// does. The split is by second count, the window's own shape.
	half := len(sampled) / 2
	var firstSum, secondSum float64
	var firstN, secondN int64
	for i, ts := range sampled {
		sec := seconds[ts]
		if i < half {
			firstSum += sec.latSum
			firstN += sec.latSamples
		} else {
			secondSum += sec.latSum
			secondN += sec.latSamples
		}
	}
	tr := &SoakTrend{FirstHalfMs: meanMs(firstSum, firstN), SecondHalfMs: meanMs(secondSum, secondN)}

	// The slope regresses the per-second means on elapsed seconds from the
	// first sampled one, so a gap in the window stretches the x axis rather
	// than pretending the seconds on either side were adjacent.
	base := sampled[0]
	var sumX, sumY, sumXY, sumXX float64
	for _, ts := range sampled {
		sec := seconds[ts]
		x := float64(ts - base)
		y := sec.latSum / float64(sec.latSamples)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	n := float64(len(sampled))
	if varX := sumXX - sumX*sumX/n; varX > 0 {
		slopePerSecond := (sumXY - sumX*sumY/n) / varX
		tr.SlopeMsPerMin = slopePerSecond * msPerSecond * secondsPerMin
	}

	tr.LeakSuspected = tr.FirstHalfMs > 0 &&
		tr.SecondHalfMs > soakLeakHalfGrowth*tr.FirstHalfMs &&
		tr.SlopeMsPerMin > 0
	return tr
}

// meanMs is a latency total over its sample count, in milliseconds. A
// half with no samples reads zero, which the verdict's FirstHalfMs > 0
// guard then treats as unjudged rather than infinitely degraded.
func meanMs(sum float64, n int64) float64 {
	if n <= 0 {
		return 0
	}
	return sum / float64(n) * msPerSecond
}
