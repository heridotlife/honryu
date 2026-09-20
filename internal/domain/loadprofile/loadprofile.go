// Package loadprofile holds the load configuration for a Execution: which
// scenarios run, with how many engines, and at what concurrency. An Entry is the
// unit that maps onto a Taurus execution block. These types carry yaml tags for
// the uploaded execution config ("multi-test" wrapper) but the package itself
// performs no I/O and imports no serializer.
package loadprofile

import (
	"errors"
	"fmt"

	"github.com/heridotlife/honryu/internal/domain/loadmode"
)

// Validation errors. Callers compare with errors.Is.
var (
	ErrScenarioRequired   = errors.New("loadprofile: a valid scenario id is required")
	ErrEnginesInvalid     = errors.New("loadprofile: engines must be greater than zero")
	ErrConcurrencyInvalid = errors.New("loadprofile: concurrency must be greater than zero")
	ErrDurationInvalid    = errors.New("loadprofile: duration must be greater than zero")
	ErrThroughputInvalid  = errors.New("loadprofile: throughput cannot be negative")
	ErrNoScenarios        = errors.New("loadprofile: at least one scenario is required")
	// ErrModeInvalid and ErrModeThroughput gate the simplified-mode
	// provenance field (phase 90): a mode must be one of the named
	// modes, and a mode entry is rate-defined by construction -- its whole
	// point is a target request rate, so "unlimited" (throughput 0)
	// contradicts the mode itself. ErrStepsInvalid and
	// ErrStaircaseDuration gate the staircase shape (phase 98): steps is
	// a staircase-only field inside 2..10 (resolution defaults an unstated
	// count before Validate runs), and a per-step hold under 60s reads as
	// a jagged ramp, not a set of plateaus.
	ErrModeInvalid       = errors.New("loadprofile: mode must be one of burst, ramp, soak, staircase")
	ErrModeThroughput    = errors.New("loadprofile: a mode entry requires throughput greater than zero")
	ErrStepsInvalid      = errors.New("loadprofile: steps must be between 2 and 10 on a staircase entry")
	ErrStaircaseDuration = errors.New("loadprofile: a staircase entry requires a per-step duration of at least 60 seconds")
)

// Entry is one scenario's load configuration within an execution; it maps onto
// a single Taurus execution block.
type Entry struct {
	Name        string `yaml:"name" json:"name"`
	ScenarioID  int64  `yaml:"testid" json:"scenario_id"`
	Concurrency int    `yaml:"concurrency" json:"concurrency"`
	Rampup      int    `yaml:"rampup" json:"rampup"`
	Engines     int    `yaml:"engines" json:"engines"`
	// Throughput is the target request rate for the entry, shared across its
	// engines. Zero means unlimited, which is what Taurus assumes when the key
	// is absent.
	Throughput int  `yaml:"throughput,omitempty" json:"throughput,omitempty"`
	Duration   int  `yaml:"duration" json:"duration"`
	CSVSplit   bool `yaml:"csv_split" json:"csv_split"`
	// Mode is simplified-mode provenance (phase 90): "burst", "ramp",
	// "soak", or "staircase" when the entry was stated as a mode and
	// resolved server-side (executionapp.StoreConfig fills
	// Concurrency/Engines/Rampup before anything is validated or
	// persisted), empty for an ordinary advanced entry. It rides along
	// for the UI to render and never influences compile, sharding, or
	// quota -- a mode-derived entry is byte-identical to an advanced
	// entry with the same numbers.
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
	// Steps is the staircase step count (phase 98): 2..10 on a
	// staircase entry (resolution defaults an unstated count to 5), zero
	// on everything else. Unlike Mode it is load-bearing downstream: a
	// staircase's whole window is Steps consecutive per-step holds, so
	// quota windows and run durations read it, and deploy expands it into
	// the per-pod step table. Last field on purpose, after Mode: the
	// JSON/YAML marshal order the web editor mirrors appends it last.
	Steps int `yaml:"steps,omitempty" json:"steps,omitempty"`
}

// Validate checks a single entry's invariants.
func (ep Entry) Validate() error {
	switch {
	case ep.ScenarioID <= 0:
		return ErrScenarioRequired
	case ep.Engines <= 0:
		return ErrEnginesInvalid
	case ep.Concurrency <= 0:
		return ErrConcurrencyInvalid
	case ep.Duration <= 0:
		return ErrDurationInvalid
	case ep.Throughput < 0:
		return ErrThroughputInvalid
	case ep.Mode != "" && !loadmode.Valid(loadmode.Mode(ep.Mode)):
		return fmt.Errorf("%w: %q", ErrModeInvalid, ep.Mode)
	case ep.Mode != "" && ep.Throughput <= 0:
		return ErrModeThroughput
	case loadmode.Mode(ep.Mode) != loadmode.ModeStaircase && ep.Steps != 0:
		return fmt.Errorf("%w: got %d on a non-staircase entry", ErrStepsInvalid, ep.Steps)
	case loadmode.Mode(ep.Mode) == loadmode.ModeStaircase && (ep.Steps < loadmode.StaircaseStepsMin || ep.Steps > loadmode.StaircaseStepsMax):
		return fmt.Errorf("%w: got %d", ErrStepsInvalid, ep.Steps)
	case loadmode.Mode(ep.Mode) == loadmode.ModeStaircase && ep.Duration < loadmode.StaircaseMinDurationSeconds:
		return fmt.Errorf("%w: got %d", ErrStaircaseDuration, ep.Duration)
	}
	return nil
}

// Profile is the full set of load entries to run for an execution.
type Profile struct {
	Name        string  `yaml:"name" json:"name"`
	ProjectID   int64   `yaml:"projectid" json:"project_id"`
	ExecutionID int64   `yaml:"collectionid" json:"execution_id"`
	Tests       []Entry `yaml:"tests" json:"tests"`
	CSVSplit    bool    `yaml:"csv_split" json:"csv_split"`
	// Criteria are the execution's Taurus pass/fail expressions (e.g.
	// "failures>10%", "p95>500ms"), applied across the whole execution --
	// not per scenario. Optional: an execution with none configured simply
	// has no passfail module compiled in, exactly as before this field
	// existed.
	Criteria []string `yaml:"criteria,omitempty" json:"criteria,omitempty"`
}

// Wrapper is the top-level shape of an uploaded execution config file.
type Wrapper struct {
	Content Profile `yaml:"multi-test" json:"multi-test"`
}

// Validate ensures there is at least one scenario and every scenario is valid.
func (ec Profile) Validate() error {
	if len(ec.Tests) == 0 {
		return ErrNoScenarios
	}
	for i, ep := range ec.Tests {
		if err := ep.Validate(); err != nil {
			return fmt.Errorf("scenario %d (id %d): %w", i, ep.ScenarioID, err)
		}
	}
	return nil
}

// TotalEngines is the sum of engines across all scenarios.
func (ec Profile) TotalEngines() int {
	total := 0
	for _, ep := range ec.Tests {
		total += ep.Engines
	}
	return total
}

// LongestDurationSeconds is the longest scenario's actual run time --
// ramp-up plus hold for an ordinary entry, and per-step hold times the
// step count for a staircase entry, whose pods run every plateau
// sequentially -- matching how compile.Taurus turns those same fields
// into a shard's actual run time. A quota reservation for the whole
// profile should cover exactly this long, not an approximation.
func (ec Profile) LongestDurationSeconds() int {
	longest := 0
	for _, ep := range ec.Tests {
		d := ep.Rampup + ep.Duration
		if ep.Steps > 1 {
			// A staircase holds each of its Steps plateaus for Duration:
			// one hold would under-cover (steps-1)/steps of the run.
			d = ep.Rampup + ep.Duration*ep.Steps
		}
		if d > longest {
			longest = d
		}
	}
	return longest
}
