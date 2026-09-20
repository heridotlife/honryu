// Package loadmode is the simplified execution-mode domain: the four
// modes an operator may state instead of hand-tuning concurrency, engines,
// and ramp-up (burst / ramp / soak / staircase), the ramp-up policy each
// mode implies, the staircase step-table derivation, and the
// Little's-Law concurrency formula shared by mode resolution and
// the calibration search's step sizing.
//
// A mode is a REQUEST shape, never a stored one: executionapp.StoreConfig
// resolves a mode entry into ordinary loadprofile numbers before anything
// is validated or persisted, and compile never sees a mode at all. This
// package therefore holds only the pure derivation rules -- no I/O, no
// clocks, deterministic throughout.
package loadmode

import (
	"errors"
	"math"

	"github.com/heridotlife/honryu/internal/domain/threshold"
)

// Mode is a simplified execution mode: the operator's statement of intent
// (a cold-start blast, a rising-load ramp, or a long steady soak) that the
// server resolves into concrete load-profile numbers.
type Mode string

const (
	// ModeBurst hits the target at full rate from the first second --
	// cold-start behaviour IS the subject, so its ramp-up is zero.
	ModeBurst Mode = "burst"
	// ModeRamp raises load over a bounded fraction of the window so
	// behaviour under rising load is observable.
	ModeRamp Mode = "ramp"
	// ModeSoak holds a steady rate for a long window -- leak and
	// degradation hunting, warmed up briefly so minute-0 connection-pool
	// churn is never misread as degradation.
	ModeSoak Mode = "soak"
	// ModeStaircase raises load in discrete plateaus (phase 98) -- the
	// find-the-ceiling capacity probe. The stated rate is the CEILING
	// plateau; resolution derives the step table and the engine pods are
	// sized once, at the ceiling.
	ModeStaircase Mode = "staircase"
)

// ErrModeInvalid means a string that is neither a known mode nor the
// empty/absent advanced marker was offered where a mode was expected.
var ErrModeInvalid = errors.New("loadmode: mode must be one of burst, ramp, soak, staircase")

// Valid reports whether m is one of the named modes. The empty
// string is deliberately NOT valid here: callers treat it as "advanced"
// (no mode) before ever reaching this package, and conflating the two
// would let an absent mode silently resolve.
func Valid(m Mode) bool {
	return m == ModeBurst || m == ModeRamp || m == ModeSoak || m == ModeStaircase
}

// ParseMode converts a wire/API string into a Mode.
func ParseMode(s string) (Mode, error) {
	m := Mode(s)
	if !Valid(m) {
		return "", ErrModeInvalid
	}
	return m, nil
}

// Ramp-up policy constants. Named (rather than inline magic numbers)
// because each is a product decision the spec argued explicitly, and a
// future tuning pass should be able to find -- and reason about -- each
// bound in one place.
const (
	// rampWindowDivisor keeps a ramp's rising phase at most a fifth of
	// the measurement window: the ramp exists to observe behaviour under
	// rising load, not to consume the window it should be measuring.
	rampWindowDivisor = 5
	// rampMinSeconds bounds the shortest useful ramp: under a minute of
	// rise is indistinguishable from a burst to most systems.
	rampMinSeconds = 60
	// rampMaxSeconds bounds the longest ramp so an hour-long run does not
	// spend its first ten minutes climbing.
	rampMaxSeconds = 600
	// soakWarmupSeconds is the fixed warm-up a soak runs before its
	// steady hold: long enough to absorb connection-pool and JIT churn,
	// short enough to never eat into an hours-long window.
	soakWarmupSeconds = 60
)

// Staircase policy constants (phase 98), named for the same reason as the
// ramp-up policy above: each is a product decision the spec argued
// explicitly.
const (
	// StaircaseStepsDefault is the step count a staircase entry with no
	// stated steps resolves with -- k6's steps-executor parity.
	StaircaseStepsDefault = 5
	// StaircaseStepsMin is the least staircase there is: one plateau is
	// just a flat run, so the shape starts at two.
	StaircaseStepsMin = 2
	// StaircaseStepsMax bounds the step table: more than ten plateaus
	// spends more time stepping than holding, and the pod would run ten
	// sequential engine executions to deliver it.
	StaircaseStepsMax = 10
	// StaircaseMinDurationSeconds is the per-step hold floor: a
	// staircase of 5x60s is the honest minimum -- shorter holds read as
	// a jagged ramp, not a set of plateaus.
	StaircaseMinDurationSeconds = 60
)

// RampupSeconds is the ramp-up the mode prescribes for a hold of
// durationSeconds: burst ramps not at all, soak warms up briefly, and ramp
// takes a fifth of the window clamped to [60s, 600s]. An unknown mode
// (or the empty advanced marker) has no policy and yields 0 -- callers
// validate the mode before relying on this.
func RampupSeconds(m Mode, durationSeconds int) int {
	switch m {
	case ModeBurst:
		return 0
	case ModeSoak:
		return soakWarmupSeconds
	case ModeStaircase:
		// The step edges ARE the shape: every step enters at its full
		// rate, so no ramp-up inside a step.
		return 0
	case ModeRamp:
		r := durationSeconds / rampWindowDivisor
		if r < rampMinSeconds {
			return rampMinSeconds
		}
		if r > rampMaxSeconds {
			return rampMaxSeconds
		}
		return r
	default:
		return 0
	}
}

// Suggested-threshold constants. Like the ramp-up policy above, each is
// a product decision argued in the phase-97 spec and named so a tuning
// pass can find and reason about every bound in one place.
const (
	// burstSuggestedErrorRate is the cold-start error budget: a blast's
	// purpose is surviving the first second, so ordinary traffic is held
	// to the plain 1% any load test answer should meet.
	burstSuggestedErrorRate = 0.01
	// burstSuggestedP95MS is a burst's latency ceiling: tight, because
	// queue buildup during the cold start is exactly what the mode hunts.
	burstSuggestedP95MS = 500
	// rampSuggestedErrorRate matches burst's budget: rising load should
	// not buy error allowance.
	rampSuggestedErrorRate = 0.01
	// rampSuggestedP95MS is looser than burst's: progressive load
	// legitimately degrades latency as the rate climbs, and a ceiling as
	// tight as burst's would fail every honest ramp.
	rampSuggestedP95MS = 800
	// soakSuggestedErrorRate is half the others: a long steady hold
	// amplifies rare failures, and leak hunting assumes a quiet system.
	soakSuggestedErrorRate = 0.005
	// soakSuggestedP95MS sits between the two: warmed up, steady, but
	// hours of sustained pressure forgive more than a cold blast.
	soakSuggestedP95MS = 600
	// soakThroughputFloor is the share of its target rate a soak must
	// actually sustain -- a soak that silently undershoots is measuring
	// the wrong load. 0.9 leaves honest headroom for scheduling jitter.
	soakThroughputFloor = 0.9
	// staircaseSuggestedErrorRate matches the ordinary budget: finding
	// the breaking plateau does not buy error allowance below it.
	staircaseSuggestedErrorRate = 0.01
	// staircaseSuggestedP95MS is the widest latency ceiling in the table:
	// the point is finding the breaking plateau, and alerts should flag
	// breakage, not proximity to it.
	staircaseSuggestedP95MS = 1000
)

// SuggestedThresholds returns the mode's default health contract: the
// threshold rows a run of that mode is naturally judged by, in the
// criteria evaluator's own grammar so a stored suggestion can actually
// grade a report. The soak contract adds a throughput floor at
// soakThroughputFloor × targetQPS; a non-positive targetQPS (an
// unlimited-rate entry) states no floor -- there is no rate to undershoot.
// Rows carry only Metric/Comparison/Value: the caller owns scenario
// binding and persistence. An unknown mode (or the empty advanced marker)
// has no contract and yields nil -- callers validate the mode before
// relying on this, the same convention as RampupSeconds.
func SuggestedThresholds(m Mode, targetQPS float64) []threshold.Threshold {
	switch m {
	case ModeBurst:
		return []threshold.Threshold{
			{Metric: threshold.MetricErrorRate, Comparison: threshold.ComparisonLT, Value: burstSuggestedErrorRate},
			{Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: burstSuggestedP95MS},
		}
	case ModeStaircase:
		return []threshold.Threshold{
			{Metric: threshold.MetricErrorRate, Comparison: threshold.ComparisonLT, Value: staircaseSuggestedErrorRate},
			{Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: staircaseSuggestedP95MS},
		}
	case ModeRamp:
		return []threshold.Threshold{
			{Metric: threshold.MetricErrorRate, Comparison: threshold.ComparisonLT, Value: rampSuggestedErrorRate},
			{Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: rampSuggestedP95MS},
		}
	case ModeSoak:
		rows := []threshold.Threshold{
			{Metric: threshold.MetricErrorRate, Comparison: threshold.ComparisonLT, Value: soakSuggestedErrorRate},
			{Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: soakSuggestedP95MS},
		}
		if targetQPS > 0 {
			rows = append(rows, threshold.Threshold{
				Metric: threshold.MetricThroughputQPS, Comparison: threshold.ComparisonGT,
				Value: targetQPS * soakThroughputFloor,
			})
		}
		return rows
	default:
		return nil
	}
}

// StaircaseStep is one plateau of a staircase shape: the rate the step
// holds and the virtual users holding it.
type StaircaseStep struct {
	// Index is the step's position, 0-based.
	Index int
	// Throughput is the step's target request rate.
	Throughput int
	// Concurrency is the step's virtual-user count.
	Concurrency int
}

// StaircaseSteps expands a resolved staircase entry's ceiling numbers
// into its step table: step i holds rate round(throughput*(i+1)/steps)
// (floored at 1 -- a plateau that generates nothing is not a plateau)
// with ceil(concurrency*(i+1)/steps) users, and the last step holds the
// ceiling exactly. Because the entry's concurrency is the Little's-Law
// count at the ceiling rate (linear in the rate), its exact apportionment
// per step IS Little's Law at each step's rate, up to integer rounding --
// which is why the derivation needs only the stored numbers and no
// latency hint. Rates and users rise monotonically by construction. A
// steps count below two is not a staircase and yields nil; the bounds a
// stored entry must satisfy are loadprofile.Validate's to enforce.
func StaircaseSteps(concurrency, throughput, steps int) []StaircaseStep {
	if steps < StaircaseStepsMin {
		return nil
	}
	out := make([]StaircaseStep, steps)
	for i := 0; i < steps; i++ {
		share := float64(i+1) / float64(steps)
		rate := int(math.Round(float64(throughput) * share))
		if rate < 1 {
			rate = 1
		}
		conc := int(math.Ceil(float64(concurrency) * share))
		if conc < 1 {
			conc = 1
		}
		out[i] = StaircaseStep{Index: i, Throughput: rate, Concurrency: conc}
	}
	// The ceiling step is exact, not rounded: the shape's whole point is
	// finding the ceiling, so the top plateau must hold the stated rate
	// with the full concurrency that was derived for it.
	out[steps-1] = StaircaseStep{Index: steps - 1, Throughput: throughput, Concurrency: concurrency}
	return out
}

// Concurrency-sizing constants, extracted from calibrationapp's step
// sizing so the whole platform has one Little's Law.
const (
	// ConcurrencyFloor is the least concurrency ever requested, so even a
	// low rate keeps enough virtual users to actually reach it.
	ConcurrencyFloor = 20
	// ConcurrencyHeadroom multiplies the Little's-Law minimum
	// (requestedQPS * observedLatency) when sizing from a measured
	// response time -- enough slack to absorb latency jitter without the
	// gross over-provisioning that makes bzt's Constant Throughput Timer
	// undershoot with mostly-idle threads (measured live: 400 VUs
	// undershot 200 QPS by ~13%, 20 VUs held it within 1%).
	ConcurrencyHeadroom = 3.0
)

// Concurrency returns the virtual-user count needed to sustain
// requestedQPS given latencyHintSec, a measured response time in seconds
// -- the Little's-Law sizing calibration's step retry and simplified-mode
// resolution share (phase 90 extracted it from calibrationapp so both read
// one formula).
//
// With a measurement (latencyHintSec > 0) it sizes threads to what the
// load actually needs: ceil(qps * latency * ConcurrencyHeadroom), the
// minimum VUs to sustain the rate plus jitter headroom. No ceiling -- a
// genuinely slow target legitimately needs many threads, and this is
// sized from a real measurement, not a guess.
//
// Without a measurement (latencyHintSec <= 0) it returns
// ConcurrencyFloor: for the calibration search that floor IS the policy
// (a light first-attempt probe, deliberately not sized to the rate --
// sizing an uninformed guess to the rate caused two live failures before
// phase 53). Mode resolution never passes a non-positive hint: its
// fallback default stands in first, so the floor branch is unreachable
// from that path.
func Concurrency(requestedQPS, latencyHintSec float64) int {
	if latencyHintSec <= 0 {
		return ConcurrencyFloor
	}
	c := int(math.Ceil(requestedQPS * latencyHintSec * ConcurrencyHeadroom))
	if c < ConcurrencyFloor {
		return ConcurrencyFloor
	}
	return c
}
