package calibrationapp

import (
	"testing"

	"github.com/heridotlife/honryu/internal/domain/loadmode"
)

// Phase 90 extracted the Little's-Law concurrency sizing into
// loadmode.Concurrency so the calibration search and simplified-mode
// resolution share one formula. This cross-pin asserts the delegation
// itself: the two functions must agree everywhere, including the
// no-measurement branch and the floor. Internal package on purpose --
// stepConcurrency is unexported, and the pin is about this package's own
// wiring, not its public behaviour.
func TestStepConcurrency_MatchesLoadmode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		qps, latency float64
	}{
		{500, 0.25}, {200, 0.2}, {4000, 0}, {1, 5}, {1000, 0.002},
		{123.4, 0.0777}, {0.5, 0}, {3.25, 1.5},
	} {
		if step, domain := stepConcurrency(tc.qps, tc.latency), loadmode.Concurrency(tc.qps, tc.latency); step != domain {
			t.Errorf("stepConcurrency(%g, %g) = %d, loadmode.Concurrency = %d; the shared formula forked", tc.qps, tc.latency, step, domain)
		}
	}
}
