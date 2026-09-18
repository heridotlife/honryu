package loadmode

import (
	"errors"
	"math"
	"testing"
)

func TestParseMode(t *testing.T) {
	for _, m := range []Mode{ModeBurst, ModeRamp, ModeSoak} {
		got, err := ParseMode(string(m))
		if err != nil || got != m {
			t.Errorf("ParseMode(%q) = %q, %v; want %q, nil", m, got, err, m)
		}
	}
	for _, s := range []string{"", "BURST", "soack", "steady", "0"} {
		if _, err := ParseMode(s); !errors.Is(err, ErrModeInvalid) {
			t.Errorf("ParseMode(%q) err = %v, want ErrModeInvalid", s, err)
		}
	}
}

func TestValid(t *testing.T) {
	for _, m := range []Mode{ModeBurst, ModeRamp, ModeSoak} {
		if !Valid(m) {
			t.Errorf("Valid(%q) = false, want true", m)
		}
	}
	// The empty string is the advanced marker, not a mode: callers must
	// route it around this package, never through it.
	for _, m := range []Mode{"", "burst ", "Soak"} {
		if Valid(m) {
			t.Errorf("Valid(%q) = true, want false", m)
		}
	}
}

func TestRampupSeconds(t *testing.T) {
	cases := []struct {
		mode Mode
		d    int
		want int
		why  string
	}{
		{ModeBurst, 600, 0, "burst: cold start is the subject"},
		{ModeBurst, 10, 0, "burst: even a short window gets no ramp"},
		{ModeSoak, 3600, soakWarmupSeconds, "soak: fixed warmup regardless of window"},
		{ModeSoak, 300, soakWarmupSeconds, "soak: same warmup for a short soak"},
		{ModeRamp, 3600, 600, "ramp: d/5 clamped at the 600s ceiling"},
		{ModeRamp, 7200, 600, "ramp: hour-plus runs still cap at 600s"},
		{ModeRamp, 600, 120, "ramp: 10m window -> 2m ramp (spec's worked example)"},
		{ModeRamp, 300, 60, "ramp: d/5 below the floor clamps up to 60s"},
		{ModeRamp, 100, 60, "ramp: 20s of rise is indistinguishable from burst"},
		{ModeRamp, 301, 60, "ramp: integer division 60"},
		{ModeRamp, 302, 60, "ramp: integer division 60 (302/5=60.4 -> 60)"},
		{ModeRamp, 305, 61, "ramp: integer division 61"},
		{"", 600, 0, "advanced: no policy"},
		{"nonsense", 600, 0, "unknown: no policy, caller validated first"},
	}
	for _, tc := range cases {
		if got := RampupSeconds(tc.mode, tc.d); got != tc.want {
			t.Errorf("RampupSeconds(%q, %d) = %d, want %d (%s)", tc.mode, tc.d, got, tc.want, tc.why)
		}
	}
}

func TestConcurrency(t *testing.T) {
	cases := []struct {
		qps, latency float64
		want         int
		why          string
	}{
		// Spec's worked example: 500 rps at a 250ms p95 -> 375 VUs.
		{500, 0.25, 375, "little's law 500x0.25x3.0"},
		{200, 0.2, 120, "little's law 200x0.2x3.0"},
		{1, 2.0, 20, "ceil(6) floors at 20"},
		{0.5, 0.01, 20, "tiny load floors at 20"},
		// Ceil, not truncate: a fractional minimum must not lose a user
		// (both above the floor so the ceil itself is what is pinned; an
		// "exact" product is deliberately not pinned -- float64 makes
		// 100*0.07*3 21.000000000000004, and the ceil of that is 22).
		{100, 0.0701, 22, "ceil(21.03)=22, not 21"},
		{100, 0.07033, 22, "ceil(21.099)=22"},
		// No measurement: the calibration probe's floor policy.
		{4000, 0, ConcurrencyFloor, "unmeasured -> floor"},
		{4000, -1, ConcurrencyFloor, "negative hint -> floor"},
	}
	for _, tc := range cases {
		if got := Concurrency(tc.qps, tc.latency); got != tc.want {
			t.Errorf("Concurrency(%g, %g) = %d, want %d (%s)", tc.qps, tc.latency, got, tc.want, tc.why)
		}
	}
	// A slow target legitimately needs many threads; no ceiling exists.
	if got := Concurrency(1000, 5.0); got != int(math.Ceil(1000*5.0*ConcurrencyHeadroom)) {
		t.Errorf("Concurrency(1000, 5.0) = %d, want the unfloored little's-law count", got)
	}
}
