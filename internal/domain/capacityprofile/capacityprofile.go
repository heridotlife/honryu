// Package capacityprofile models what a calibration search (see
// internal/domain/calibration) found: one engine pod's sustainable QPS for a
// given scenario and pod size, and the fan-out calculation that turns a
// target aggregate QPS into a required engine count -- or, honestly, a
// stated reason it cannot.
//
// Pure domain: arithmetic only, deterministic, no I/O.
package capacityprofile

import (
	"math"
	"time"

	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/taurus"
)

// Key identifies a CapacityProfile: what one engine pod of this exact size
// running this exact scenario was found to sustain. CPU and Memory are
// ports.DeploySpec's own string format (e.g. "1", "512Mi") -- a profile
// answers "QPS per pod of THIS size", so the size is part of its identity,
// not a detail alongside it.
type Key struct {
	ScenarioID int64
	Engine     taurus.Executor
	CPU        string
	Memory     string
}

// CapacityProfile is a calibration's confirmed-or-floor answer for one Key.
type CapacityProfile struct {
	Key
	// PerPodQPS and SaturatedBy carry exactly the meaning
	// calibration.Result documents: confirmed capacity when SaturatedBy is
	// SaturatedByEngine, a lower bound otherwise.
	PerPodQPS   float64
	SaturatedBy calibration.SaturatedBy
	// ScenarioFingerprint is the scenario's content hash at calibration
	// time. FanOut treats any mismatch against the scenario's current
	// fingerprint as staleness, never as freshness -- a missed content
	// change producing a wrong engine count is the dangerous failure; an
	// unnecessary recalibration is merely wasteful.
	ScenarioFingerprint string
	CalibratedAt        time.Time
	JobID               int64
}

// Status is FanOut's answer about whether -- and how confidently -- it could
// compute an engine count. An engine count is only ever present alongside
// StatusOK, so a consumer can never read a number without its caveat.
type Status string

const (
	// StatusOK means a fresh, engine-limited profile produced a confirmed
	// engine count.
	StatusOK Status = "ok"
	// StatusTargetLimited means the profile is fresh, but one engine pod
	// already overloaded the target during calibration -- more engines
	// would only overload the target harder, so no engine count is
	// returned.
	StatusTargetLimited Status = "target_limited"
	// StatusInconclusive means the profile is fresh, but the search never
	// found either ceiling: it exhausted its own budget (steps/max QPS)
	// with both the engine and the target still healthy -- PerPodQPS is a
	// lower bound with no confirmed limit on either side, so no engine
	// count is returned.
	StatusInconclusive Status = "inconclusive"
	// StatusEngineFloor means the engine saturated at EVERY rate the search
	// tried, all the way down to its lowest step -- nothing was ever
	// sustained, so PerPodQPS is zero and there is no measurable
	// steady-state to fan out from (the scenario is too light for one pod,
	// or the criterion is too strict). Split from StatusInconclusive in
	// phase 44: "engine saturates below measurable load" is the opposite
	// finding from "both ends still healthy", and sharing one status made
	// the copy lie for one of them.
	StatusEngineFloor Status = "engine_floor"
	// StatusStale means a profile exists for this key, but the scenario's
	// content has changed since it was calibrated.
	StatusStale Status = "stale"
	// StatusNoProfile means no calibration has ever been recorded for this
	// key.
	StatusNoProfile Status = "no_profile"
)

// Result is FanOut's answer.
type Result struct {
	Status Status
	// Engines is the required engine count. Meaningful only when Status is
	// StatusOK; zero otherwise.
	Engines int
}

// FanOut computes how many engine pods are needed to sustain targetQPS,
// given profile (nil if no calibration was ever recorded for this key) and
// the scenario's currentFingerprint (to detect staleness).
func FanOut(profile *CapacityProfile, targetQPS float64, currentFingerprint string) Result {
	if profile == nil {
		return Result{Status: StatusNoProfile}
	}
	if profile.ScenarioFingerprint != currentFingerprint {
		return Result{Status: StatusStale}
	}
	switch {
	case profile.SaturatedBy == calibration.SaturatedByTarget:
		return Result{Status: StatusTargetLimited}
	case profile.SaturatedBy == calibration.SaturatedByEngine && profile.PerPodQPS <= 0:
		// Degenerate engine floor: the engine saturated even at the
		// search's lowest rate, so no throughput was ever sustained --
		// there is nothing to divide by and nothing honest to report
		// except the finding itself.
		return Result{Status: StatusEngineFloor}
	case profile.SaturatedBy != calibration.SaturatedByEngine:
		// SaturatedByNeither: no confirmed ceiling on either side to
		// divide by.
		return Result{Status: StatusInconclusive}
	}
	return Result{Status: StatusOK, Engines: int(math.Ceil(targetQPS / profile.PerPodQPS))}
}
