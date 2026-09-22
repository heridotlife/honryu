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

// LabelSoakTrend is one label's own latency behaviour across the run's
// window, judged on that label's rows alone (phase 103). The run's
// aggregate trend is diluted by healthy labels -- a leaking checkout
// beside a flat browse can hold the aggregate's halves under the leak
// threshold -- while the same window, read per label, is not. Carries the
// same figures and the same verdict rule as the aggregate.
type LabelSoakTrend struct {
	// Label is the request path (or engine label) these figures belong to.
	Label string `json:"label"`
	// FirstHalfMs and SecondHalfMs are the label's sample-weighted mean
	// response times of its own sampled seconds' two halves, in
	// milliseconds.
	FirstHalfMs  float64 `json:"first_half_ms"`
	SecondHalfMs float64 `json:"second_half_ms"`
	// SlopeMsPerMin is the least-squares slope of the label's per-second
	// mean response times, in milliseconds per minute.
	SlopeMsPerMin float64 `json:"slope_ms_per_min"`
	// LeakSuspected is the flagged signal, judged exactly as the
	// aggregate's: the label's second half ran materially slower than its
	// first AND its per-second means rose throughout.
	LeakSuspected bool `json:"leak_suspected"`
}

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
	// Labels is each label's own trend across the same window (phase
	// 103): every label that held two minutes of its own latency-sampled
	// seconds, judged on its rows alone with the same rule -- a leak in
	// one label is diluted by healthy siblings here, not there. nil when
	// no label qualified (and absent from JSON: the aggregate rides
	// alone, exactly as before, on runs without per-label trends).
	Labels []LabelSoakTrend `json:"labels,omitempty"`
}

// secondMean is one sampled second's response-time tally: the second's
// timestamp plus the numerator and denominator of the mean response time
// being trended -- the run's own overall tally, or one label's.
type secondMean struct {
	ts      int64
	sum     float64
	samples int64
}

// soakFigures is the halves, slope, and verdict one trend carries,
// computed from a series' sampled seconds in time order. The halves are
// sample-weighted -- each half's mean is its seconds' sum/samples, not
// the average of its per-second means -- so a sparse second (one request)
// cannot move a half the way a busy one does; the split is by second
// count, the window's own shape. The slope regresses the per-second means
// on elapsed seconds from the first sampled one, so a gap in the series
// stretches the x axis rather than pretending the seconds on either side
// were adjacent. Shared by the run's aggregate trend and each label's own
// (phase 103), so the two can never drift into judging differently.
func soakFigures(sampled []secondMean) (firstHalfMs, secondHalfMs, slopeMsPerMin float64, leakSuspected bool) {
	half := len(sampled) / 2
	var firstSum, secondSum float64
	var firstN, secondN int64
	for i, sec := range sampled {
		if i < half {
			firstSum += sec.sum
			firstN += sec.samples
		} else {
			secondSum += sec.sum
			secondN += sec.samples
		}
	}
	firstHalfMs, secondHalfMs = meanMs(firstSum, firstN), meanMs(secondSum, secondN)

	base := sampled[0].ts
	var sumX, sumY, sumXY, sumXX float64
	for _, sec := range sampled {
		x := float64(sec.ts - base)
		y := sec.sum / float64(sec.samples)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	n := float64(len(sampled))
	if varX := sumXX - sumX*sumX/n; varX > 0 {
		slopePerSecond := (sumXY - sumX*sumY/n) / varX
		slopeMsPerMin = slopePerSecond * msPerSecond * secondsPerMin
	}

	leakSuspected = firstHalfMs > 0 &&
		secondHalfMs > soakLeakHalfGrowth*firstHalfMs &&
		slopeMsPerMin > 0
	return firstHalfMs, secondHalfMs, slopeMsPerMin, leakSuspected
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

	means := make([]secondMean, len(sampled))
	for i, ts := range sampled {
		sec := seconds[ts]
		means[i] = secondMean{ts: ts, sum: sec.latSum, samples: sec.latSamples}
	}
	first, second, slope, leak := soakFigures(means)
	return &SoakTrend{
		FirstHalfMs: first, SecondHalfMs: second,
		SlopeMsPerMin: slope, LeakSuspected: leak,
	}
}

// soakTrendLabels computes each label's own trend from the per-second
// per-label tallies (phase 103), or nil when no label held enough
// latency-sampled seconds of its own. Every judging rule is the
// aggregate's, applied to the label's own series: the same two-minute
// floor (a label that only reported for part of a long soak has no trend
// -- its few seconds are noise), the same skip for seconds the label did
// not report in, the same thresholds. Sorted by label name, so two
// reports of the same run match.
func soakTrendLabels(seconds map[int64]*secondState) []LabelSoakTrend {
	collected := map[string][]secondMean{}
	for ts, sec := range seconds {
		for name, lat := range sec.labelLatency {
			if lat.Samples > 0 {
				collected[name] = append(collected[name],
					secondMean{ts: ts, sum: lat.Sum, samples: lat.Samples})
			}
		}
	}
	names := make([]string, 0, len(collected))
	for name := range collected {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []LabelSoakTrend
	for _, name := range names {
		pts := collected[name]
		if len(pts) < soakTrendMinSampledSeconds {
			continue
		}
		sort.Slice(pts, func(i, j int) bool { return pts[i].ts < pts[j].ts })
		first, second, slope, leak := soakFigures(pts)
		out = append(out, LabelSoakTrend{
			Label: name, FirstHalfMs: first, SecondHalfMs: second,
			SlopeMsPerMin: slope, LeakSuspected: leak,
		})
	}
	return out
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
