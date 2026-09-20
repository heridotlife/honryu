// Phase 90: simplified execution modes. An operator states a mode
// (burst/ramp/soak), a target rate, and a duration; StoreConfig resolves
// that into the ordinary entry numbers (concurrency by Little's Law,
// engines by capacity-profile fan-out, ramp-up by mode policy) BEFORE
// Validate+persist, so a stored config is always fully resolved and
// compile never sees a mode at all. This file holds the resolution
// machinery; StoreConfig (service.go) is its only caller.

package executionapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/heridotlife/honryu/internal/domain/capacityprofile"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadmode"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

// ErrModeResolutionUnavailable means a mode entry was stored against a
// service whose mode-resolution collaborators were never wired -- a wiring
// bug (cmd/api always wires them), not an operator input problem.
var ErrModeResolutionUnavailable = errors.New("executionapp: mode resolution sources are not configured")

// ModeResolutionError is the typed refusal a mode entry earns when the
// capacity profile for its key cannot honestly produce an engine count:
// no profile, a stale one, or a profile whose finding is that engines are
// not the limit (target-limited / inconclusive / engine-floor). Carrying
// the status and the key lets the API surface both to the operator, whose
// remediation is almost always "calibrate this scenario first".
type ModeResolutionError struct {
	Status capacityprofile.Status
	Key    capacityprofile.Key
}

// Error names the FanOut status and the capacity key, per the phase 90
// contract: the operator must be able to tell WHICH refusal they hit and
// WHICH scenario/pod-size it was about.
func (e *ModeResolutionError) Error() string {
	return fmt.Sprintf("executionapp: mode config refused: capacity profile status %q for scenario %d on %s (%s CPU / %s memory)",
		e.Status, e.Key.ScenarioID, e.Key.Engine, e.Key.CPU, e.Key.Memory)
}

// CapacityProfiles is the capacity-profile read surface mode resolution
// needs. *calibrationapp.Service satisfies it directly (FanOut and
// ProfileFor, signature-identical), which is why the methods are
// signature-matched to that service rather than shaped fresh: the same
// instance the router already serves answers here, with no second
// implementation to keep honest.
type CapacityProfiles interface {
	// FanOut answers how many engines the key's scenario needs for
	// targetQPS, or -- via Result.Status -- why it cannot.
	FanOut(ctx context.Context, key capacityprofile.Key, targetQPS float64) (capacityprofile.Result, error)
	// ProfileFor returns the stored CapacityProfile for key, or
	// ports.ErrNotFound if none has ever been calibrated for it.
	ProfileFor(ctx context.Context, key capacityprofile.Key) (capacityprofile.CapacityProfile, error)
}

// CalibrationJobs reads the calibration-job ledger. The JobID a capacity
// profile carries is the link from "this profile" to "the execution whose
// runs measured it" -- the repository satisfies this directly.
type CalibrationJobs interface {
	GetCalibrationJob(ctx context.Context, jobID int64) (ports.CalibrationJob, error)
}

// ReportLister reads an execution's settled reports, most recent first.
// The repository satisfies this directly (ports.ReportStore's own shape).
type ReportLister interface {
	ListReports(ctx context.Context, executionID int64, limit int) ([]report.Report, error)
}

// ModeSources bundles the collaborators and knobs mode resolution needs.
// All are read-only; nothing here mutates calibration state.
type ModeSources struct {
	Capacity CapacityProfiles
	Jobs     CalibrationJobs
	Reports  ReportLister
	// LatencyHint is the fallback p95 (as a duration) used to size a mode
	// entry's concurrency when no calibration report is reachable through
	// the chain (profile pruned, report gone, hint unset on the job). The
	// config default (HONRYU_MODE_LATENCY_HINT_MS, 250ms) is a stand-in,
	// never a measurement -- which is why the chain is tried first.
	LatencyHint time.Duration
	// DefaultEngine names the engine an engine-less execution runs on at
	// deploy time; mode resolution must key the capacity profile by the
	// SAME engine the entry will actually run on, or it would fan out a
	// jmeter profile for a k6 run.
	DefaultEngine taurus.Executor
}

// The baseline pod size an execution with no pinned size runs at -- the
// same constants lifecycleapp's quota math uses (baselineEngineCPU /
// baselineEngineMemory). Duplicated rather than exported from there
// because lifecycleapp's are unexported by design; the values are the
// platform's deployment contract, changed only deliberately together.
const (
	modeBaselineEngineCPU    = "500m"
	modeBaselineEngineMemory = "512Mi"
)

// WithModeSources wires the simplified-mode resolution collaborators.
// Without them, a mode entry is refused (ErrModeResolutionUnavailable)
// and advanced entries behave exactly as before. Returns the receiver
// for chaining, the calibrationapp WithRunner convention.
func (s *Service) WithModeSources(m ModeSources) *Service {
	if m.Capacity != nil {
		s.modes = m
	}
	return s
}

// ResolvedDiff is one entry's resolved numbers: the four fields a mode
// derivation owns. A re-resolve reports the Before/After pair so the
// caller can show exactly what changed.
type ResolvedDiff struct {
	Engines     int `json:"engines"`
	Concurrency int `json:"concurrency"`
	Rampup      int `json:"rampup"`
	Throughput  int `json:"throughput"`
}

// ReResolveEntry is one config entry's re-resolution verdict: the entry's
// identity, its stored numbers (Before), and the numbers just persisted
// (After). Changed is false for advanced entries (never touched) and for
// mode entries whose current calibration derives exactly what the stored
// snapshot already held.
type ReResolveEntry struct {
	ScenarioID int64        `json:"scenario_id"`
	Name       string       `json:"name"`
	Mode       string       `json:"mode,omitempty"`
	Before     ResolvedDiff `json:"before"`
	After      ResolvedDiff `json:"after"`
	Changed    bool         `json:"changed"`
}

// ReResolveResult is the whole config's re-resolution report, one entry
// per stored config entry, in stored order.
type ReResolveResult struct {
	Entries []ReResolveEntry `json:"entries"`
}

func resolvedDiff(e loadprofile.Entry) ResolvedDiff {
	return ResolvedDiff{Engines: e.Engines, Concurrency: e.Concurrency, Rampup: e.Rampup, Throughput: e.Throughput}
}

// ReResolveConfig re-runs the stored config's mode entries through the
// CURRENT resolution chain and persists the refreshed config as a new
// version (phase 91): a mode entry snapshots its derivation at PUT time,
// so a later recalibration leaves it stale -- and nothing else in the
// pipeline may re-derive at run time (compile's byte-identical
// reproducibility contract). The stored profile goes back through
// StoreConfig whole, so every existing guarantee applies unchanged:
// entries without a mode are re-validated and re-persisted
// byte-identically, mode entries re-resolve against the current
// capacity-profile FanOut and latency hint, the engine limit still
// guards, and the persist is the same atomic replace every config upload
// is. A refusal (profile missing/stale) returns before anything is
// persisted. The result echoes each entry's old and new resolved numbers
// so the caller can show a diff.
func (s *Service) ReResolveConfig(ctx context.Context, executionID int64) (ReResolveResult, error) {
	stored, err := s.GetConfig(ctx, executionID)
	if err != nil {
		return ReResolveResult{}, err
	}
	before := make([]loadprofile.Entry, len(stored.Content.Tests))
	copy(before, stored.Content.Tests)
	// resolveModes mutates the tests slice in place, so the snapshot above
	// must precede the store; StoreConfig re-reads nothing, and a refusal
	// returns before the persist.
	if err := s.StoreConfig(ctx, executionID, stored.Content); err != nil {
		return ReResolveResult{}, err
	}
	// After comes from a fresh read, not the mutated slice: the report is
	// what is now stored, which is what a later deploy will run.
	after, err := s.GetConfig(ctx, executionID)
	if err != nil {
		return ReResolveResult{}, err
	}
	out := make([]ReResolveEntry, 0, len(after.Content.Tests))
	for i, a := range after.Content.Tests {
		entry := ReResolveEntry{
			ScenarioID: a.ScenarioID, Name: a.Name, Mode: a.Mode,
			After: resolvedDiff(a),
		}
		if i < len(before) {
			entry.Before = resolvedDiff(before[i])
			entry.Changed = entry.Before != entry.After
		}
		out = append(out, entry)
	}
	return ReResolveResult{Entries: out}, nil
}

// resolveModes fills every mode entry's derived numbers in place:
// engines from capacity-profile fan-out, concurrency by Little's Law
// from the calibration report's p95 (fallback: the configured hint),
// ramp-up from the mode policy. Called BEFORE Profile.Validate so the
// persisted row is an ordinary fully-resolved entry -- the mode rides
// along as provenance only. Any values the caller sent in the derived
// fields are overwritten: the server is the only resolver.
//
// Entries without a mode are untouched, byte-for-byte: the advanced path
// StoreConfig always took does not even read the mode sources.
func (s *Service) resolveModes(ctx context.Context, coll execution.Execution, entries []loadprofile.Entry) error {
	var needed bool
	for i := range entries {
		if entries[i].Mode != "" {
			needed = true
			break
		}
	}
	if !needed {
		return nil
	}
	if s.modes.Capacity == nil {
		return fmt.Errorf("%w: a mode entry needs capacity profiles", ErrModeResolutionUnavailable)
	}

	for i := range entries {
		e := &entries[i]
		if e.Mode == "" {
			continue
		}
		mode, err := loadmode.ParseMode(e.Mode)
		if err != nil {
			// Surface the loadprofile sentinel so the wire layer maps the
			// same 400 an invalid mode would earn at Validate, with the
			// offending value named.
			return fmt.Errorf("%w: %q", loadprofile.ErrModeInvalid, e.Mode)
		}
		if e.Throughput <= 0 {
			// Resolution needs a rate to size from; Validate would refuse
			// this too, but not before FanOut had divided by it.
			return fmt.Errorf("%w: got %d", loadprofile.ErrModeThroughput, e.Throughput)
		}
		if mode == loadmode.ModeStaircase && e.Steps == 0 {
			// The staircase's one optional input: an unstated step count
			// takes the default HERE, before Validate, so the persisted
			// entry always carries the count the shape actually resolved
			// with -- the step table downstream derives from it.
			e.Steps = loadmode.StaircaseStepsDefault
		}

		key := modeCapacityKey(coll, e.ScenarioID, s.modes.DefaultEngine)
		result, err := s.modes.Capacity.FanOut(ctx, key, float64(e.Throughput))
		if err != nil {
			return err
		}
		if result.Status != capacityprofile.StatusOK {
			return &ModeResolutionError{Status: result.Status, Key: key}
		}

		hint := s.latencyHintSeconds(ctx, key)
		e.Engines = result.Engines
		e.Concurrency = loadmode.Concurrency(float64(e.Throughput), hint)
		// Every engine pod must run at least one virtual user: shard.Plan
		// silently clamps engines to concurrency, and a mode entry that
		// asked for N pods must never silently run fewer. Little's Law
		// sizes the aggregate need; this floor reconciles it with the pod
		// count the profile prescribed (only a very slow target against a
		// very high per-pod rate can trip it).
		if e.Concurrency < e.Engines {
			e.Concurrency = e.Engines
		}
		e.Rampup = loadmode.RampupSeconds(mode, e.Duration)
	}
	return nil
}

// modeCapacityKey is the capacity-profile key a mode entry resolves
// against: the entry's scenario, the engine the execution will actually
// run on (its own, else the deployment default -- the same resolution
// deploy applies), and its pinned pod size, else the baseline every
// unpinned execution already runs at.
func modeCapacityKey(coll execution.Execution, scenarioID int64, defaultEngine taurus.Executor) capacityprofile.Key {
	cpu, memory := coll.CPU, coll.Memory
	if cpu == "" {
		cpu = modeBaselineEngineCPU
	}
	if memory == "" {
		memory = modeBaselineEngineMemory
	}
	engine := coll.Engine
	if engine == "" {
		engine = defaultEngine
	}
	return capacityprofile.Key{ScenarioID: scenarioID, Engine: engine, CPU: cpu, Memory: memory}
}

// latencyHintSeconds is the response-time measurement a mode entry's
// concurrency is sized from: the latest settled report of the calibration
// execution that produced this key's capacity profile -- a real p95 of
// this exact scenario on this engine and pod size. Every break in the
// chain (no profile, pruned report, empty latency) falls back to the
// configured default rather than failing the request: the hint sizes
// threads, and a stand-in beats refusing an otherwise-good config.
// Failures of the underlying reads are swallowed for the same reason --
// only the FanOut verdict above is strict.
func (s *Service) latencyHintSeconds(ctx context.Context, key capacityprofile.Key) float64 {
	if s.modes.Jobs == nil || s.modes.Reports == nil {
		return s.modes.LatencyHint.Seconds()
	}
	profile, err := s.modes.Capacity.ProfileFor(ctx, key)
	if err != nil || profile.JobID == 0 {
		return s.modes.LatencyHint.Seconds()
	}
	job, err := s.modes.Jobs.GetCalibrationJob(ctx, profile.JobID)
	if err != nil {
		return s.modes.LatencyHint.Seconds()
	}
	reports, err := s.modes.Reports.ListReports(ctx, job.ExecutionID, 1)
	if err != nil || len(reports) == 0 {
		return s.modes.LatencyHint.Seconds()
	}
	if p95 := reports[0].Latency[95]; p95 > 0 {
		return p95
	}
	return s.modes.LatencyHint.Seconds()
}
