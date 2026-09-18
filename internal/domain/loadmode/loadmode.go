// Package loadmode is the simplified execution-mode domain: the three
// modes an operator may state instead of hand-tuning concurrency, engines,
// and ramp-up (burst / ramp / soak), the ramp-up policy each mode implies,
// and the Little's-Law concurrency formula shared by mode resolution and
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
)

// ErrModeInvalid means a string that is neither a known mode nor the
// empty/absent advanced marker was offered where a mode was expected.
var ErrModeInvalid = errors.New("loadmode: mode must be one of burst, ramp, soak")

// Valid reports whether m is one of the three named modes. The empty
// string is deliberately NOT valid here: callers treat it as "advanced"
// (no mode) before ever reaching this package, and conflating the two
// would let an absent mode silently resolve.
func Valid(m Mode) bool {
	return m == ModeBurst || m == ModeRamp || m == ModeSoak
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
