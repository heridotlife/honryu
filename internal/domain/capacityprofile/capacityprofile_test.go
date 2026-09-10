package capacityprofile_test

import (
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/capacityprofile"
	"github.com/heridotlife/honryu/internal/domain/taurus"
)

func testKey() capacityprofile.Key {
	return capacityprofile.Key{ScenarioID: 1, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi"}
}

func engineLimitedProfile(fingerprint string, perPodQPS float64) *capacityprofile.CapacityProfile {
	return &capacityprofile.CapacityProfile{
		Key: testKey(), PerPodQPS: perPodQPS, SaturatedBy: calibration.SaturatedByEngine,
		ScenarioFingerprint: fingerprint, CalibratedAt: time.Unix(0, 0), JobID: 1,
	}
}

func TestFanOut_NoProfile(t *testing.T) {
	t.Parallel()
	got := capacityprofile.FanOut(nil, 100, "fp1")
	if got.Status != capacityprofile.StatusNoProfile {
		t.Fatalf("Status = %q, want no_profile", got.Status)
	}
	if got.Engines != 0 {
		t.Fatalf("Engines = %d, want 0 alongside no_profile", got.Engines)
	}
}

func TestFanOut_StaleFingerprintTakesPriorityOverEverything(t *testing.T) {
	t.Parallel()
	profile := engineLimitedProfile("old-fingerprint", 50)
	got := capacityprofile.FanOut(profile, 100, "new-fingerprint")
	if got.Status != capacityprofile.StatusStale {
		t.Fatalf("Status = %q, want stale", got.Status)
	}
	if got.Engines != 0 {
		t.Fatalf("Engines = %d, want 0 alongside stale", got.Engines)
	}
}

func TestFanOut_TargetLimited_NoEngineCount(t *testing.T) {
	t.Parallel()
	profile := &capacityprofile.CapacityProfile{
		Key: testKey(), PerPodQPS: 30, SaturatedBy: calibration.SaturatedByTarget,
		ScenarioFingerprint: "fp1",
	}
	got := capacityprofile.FanOut(profile, 300, "fp1")
	if got.Status != capacityprofile.StatusTargetLimited {
		t.Fatalf("Status = %q, want target_limited", got.Status)
	}
	if got.Engines != 0 {
		t.Fatalf("Engines = %d, want 0 -- target-limited never returns a count", got.Engines)
	}
}

// SaturatedByNeither is fresh and real, but the search never confirmed
// either ceiling -- it must not be reported as target_limited (which would
// falsely claim the target was found to be the bottleneck) or as ok (which
// would falsely claim a confirmed number). Phase 44: this is the ONLY road
// to inconclusive left -- the degenerate zero floor split off to
// StatusEngineFloor below.
func TestFanOut_Neither_Inconclusive(t *testing.T) {
	t.Parallel()
	profile := &capacityprofile.CapacityProfile{
		Key: testKey(), PerPodQPS: 5000, SaturatedBy: calibration.SaturatedByNeither,
		ScenarioFingerprint: "fp1",
	}
	got := capacityprofile.FanOut(profile, 100, "fp1")
	if got.Status != capacityprofile.StatusInconclusive {
		t.Fatalf("Status = %q, want inconclusive", got.Status)
	}
	if got.Engines != 0 {
		t.Fatalf("Engines = %d, want 0 alongside inconclusive", got.Engines)
	}
}

func TestFanOut_OK_RoundsEnginesUp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		targetQPS float64
		perPodQPS float64
		want      int
	}{
		{"exact multiple", 100, 25, 4},
		{"rounds up a remainder", 101, 25, 5},
		{"less than one pod's worth still needs one pod", 10, 25, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			profile := engineLimitedProfile("fp1", tt.perPodQPS)
			got := capacityprofile.FanOut(profile, tt.targetQPS, "fp1")
			if got.Status != capacityprofile.StatusOK {
				t.Fatalf("Status = %q, want ok", got.Status)
			}
			if got.Engines != tt.want {
				t.Fatalf("Engines = %d, want %d", got.Engines, tt.want)
			}
		})
	}
}

// Phase 44's split, on the axis that matters: inconclusive discriminates
// on SaturatedBy, never on PerPodQPS alone. A neither-result with a zero
// floor is still "budget exhausted, both ends healthy" -- only an
// engine-saturated zero floor is "the engine saturates below measurable
// load" (engine_floor).
func TestFanOut_NeitherWithZeroFloor_Inconclusive(t *testing.T) {
	t.Parallel()
	profile := &capacityprofile.CapacityProfile{
		Key: testKey(), PerPodQPS: 0, SaturatedBy: calibration.SaturatedByNeither,
		ScenarioFingerprint: "fp1",
	}
	got := capacityprofile.FanOut(profile, 100, "fp1")
	if got.Status != capacityprofile.StatusInconclusive {
		t.Fatalf("Status = %q, want inconclusive -- a neither-result is inconclusive whatever its floor", got.Status)
	}
	if got.Engines != 0 {
		t.Fatalf("Engines = %d, want 0 alongside inconclusive", got.Engines)
	}
}

// The degenerate engine floor (phase 44): the engine saturated at EVERY
// rate the search tried, so nothing was ever sustained -- exec 15's exact
// shape (all steps engine_saturated on a light scenario, PerPodQPS=0,
// SaturatedBy=engine). That is NOT "both ends healthy"; it must not wear
// inconclusive's copy, and it must never divide by zero or claim a count.
func TestFanOut_ZeroFloor_EngineFloor(t *testing.T) {
	t.Parallel()
	profile := engineLimitedProfile("fp1", 0)
	got := capacityprofile.FanOut(profile, 100, "fp1")
	if got.Status != capacityprofile.StatusEngineFloor {
		t.Fatalf("Status = %q, want engine_floor", got.Status)
	}
	if got.Engines != 0 {
		t.Fatalf("Engines = %d, want 0 -- an engine floor never returns a count", got.Engines)
	}
}
