package loadprofile_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"

	"github.com/heridotlife/honryu/internal/domain/loadprofile"
)

func validScenario(scenarioID int64, engines, concurrency int) loadprofile.Entry {
	return loadprofile.Entry{
		Name:        "p",
		ScenarioID:  scenarioID,
		Engines:     engines,
		Concurrency: concurrency,
		Rampup:      1,
		Duration:    60,
	}
}

func TestExecutionScenario_Validate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		ep      loadprofile.Entry
		wantErr error
	}{
		{"valid", validScenario(1, 2, 10), nil},
		{"no scenario id", loadprofile.Entry{Engines: 1, Concurrency: 1, Duration: 1}, loadprofile.ErrScenarioRequired},
		{"zero engines", loadprofile.Entry{ScenarioID: 1, Engines: 0, Concurrency: 1, Duration: 1}, loadprofile.ErrEnginesInvalid},
		{"zero concurrency", loadprofile.Entry{ScenarioID: 1, Engines: 1, Concurrency: 0, Duration: 1}, loadprofile.ErrConcurrencyInvalid},
		{"zero duration", loadprofile.Entry{ScenarioID: 1, Engines: 1, Concurrency: 1, Duration: 0}, loadprofile.ErrDurationInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.ep.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestExecutionExecution_Validate_And_TotalEngines(t *testing.T) {
	t.Parallel()

	ec := loadprofile.Profile{
		ExecutionID: 5,
		Tests: []loadprofile.Entry{
			validScenario(1, 2, 10),
			validScenario(2, 3, 10),
		},
	}
	if err := ec.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := ec.TotalEngines(); got != 5 {
		t.Fatalf("TotalEngines = %d, want 5", got)
	}
}

func TestExecutionExecution_Validate_Errors(t *testing.T) {
	t.Parallel()

	empty := loadprofile.Profile{ExecutionID: 5}
	if err := empty.Validate(); !errors.Is(err, loadprofile.ErrNoScenarios) {
		t.Fatalf("empty Validate = %v, want ErrNoScenarios", err)
	}

	bad := loadprofile.Profile{
		ExecutionID: 5,
		Tests:       []loadprofile.Entry{validScenario(1, 0, 1)}, // zero engines
	}
	if err := bad.Validate(); !errors.Is(err, loadprofile.ErrEnginesInvalid) {
		t.Fatalf("bad Validate = %v, want ErrEnginesInvalid", err)
	}
}

// Phase 90: the mode provenance field's validation rules -- a mode entry
// must name a known mode and carry a positive target rate (a rate-defined
// entry cannot be "unlimited"), while an empty mode is the ordinary
// advanced path Validate always took.
func TestEntry_ValidateMode(t *testing.T) {
	t.Parallel()
	resolved := func(mode string) loadprofile.Entry {
		return loadprofile.Entry{
			ScenarioID: 1, Engines: 4, Concurrency: 375, Rampup: 120,
			Duration: 600, Throughput: 500, Mode: mode,
		}
	}
	cases := []struct {
		name    string
		ep      loadprofile.Entry
		wantErr error
	}{
		{"advanced (empty mode) unchanged", func() loadprofile.Entry {
			e := resolved("")
			e.Throughput = 0 // unlimited stays legal without a mode
			return e
		}(), nil},
		{"burst resolved", resolved("burst"), nil},
		{"ramp resolved", resolved("ramp"), nil},
		{"soak resolved", resolved("soak"), nil},
		{"unknown mode", resolved("steady"), loadprofile.ErrModeInvalid},
		{"mode with unlimited throughput", func() loadprofile.Entry {
			e := resolved("soak")
			e.Throughput = 0
			return e
		}(), loadprofile.ErrModeThroughput},
		{"mode with negative throughput", func() loadprofile.Entry {
			e := resolved("burst")
			e.Throughput = -1
			return e
		}(), loadprofile.ErrThroughputInvalid}, // the sign rule fires first
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.ep.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// Phase 90's wire compatibility proof: mode is omitempty in both codecs,
// so an entry without it serializes exactly as it always did, and a
// mode-carrying entry round-trips its provenance intact.
func TestEntry_ModeJSONYAMLRoundTrip(t *testing.T) {
	t.Parallel()
	advanced := loadprofile.Entry{Name: "a", ScenarioID: 7, Engines: 2, Concurrency: 10, Rampup: 5, Duration: 60}
	if js, err := json.Marshal(advanced); err != nil {
		t.Fatalf("marshal advanced: %v", err)
	} else if strings.Contains(string(js), "mode") {
		t.Fatalf("advanced entry JSON mentions mode: %s", js)
	}
	ym, err := yaml.Marshal(advanced)
	if err != nil {
		t.Fatalf("yaml marshal advanced: %v", err)
	}
	if strings.Contains(string(ym), "mode") {
		t.Fatalf("advanced entry YAML mentions mode: %s", ym)
	}

	modeEntry := advanced
	modeEntry.Mode = "soak"
	modeEntry.Throughput = 500
	js, err := json.Marshal(modeEntry)
	if err != nil {
		t.Fatalf("marshal mode entry: %v", err)
	}
	var back loadprofile.Entry
	if err := json.Unmarshal(js, &back); err != nil {
		t.Fatalf("unmarshal mode entry: %v", err)
	}
	if back.Mode != "soak" || back.Throughput != 500 {
		t.Fatalf("JSON round trip = %+v, want mode soak / throughput 500", back)
	}
	ym, err = yaml.Marshal(modeEntry)
	if err != nil {
		t.Fatalf("yaml marshal mode entry: %v", err)
	}
	back = loadprofile.Entry{}
	if err := yaml.Unmarshal(ym, &back); err != nil {
		t.Fatalf("yaml unmarshal mode entry: %v", err)
	}
	if back.Mode != "soak" {
		t.Fatalf("YAML round trip mode = %q, want soak", back.Mode)
	}

	// A mode entry arriving on the wire states only mode/throughput/
	// duration; the numbers it leaves blank must decode as zero (the
	// server fills them before Validate+persist, never Validate here).
	var stated loadprofile.Entry
	if err := json.Unmarshal([]byte(`{"name":"t","scenario_id":9,"mode":"ramp","throughput":250,"duration":600}`), &stated); err != nil {
		t.Fatalf("unmarshal stated mode entry: %v", err)
	}
	if stated.Mode != "ramp" || stated.Concurrency != 0 || stated.Engines != 0 || stated.Rampup != 0 {
		t.Fatalf("stated mode entry decoded = %+v, want blank numbers with mode ramp", stated)
	}
}

// --- Phase 98: staircase provenance + step-count validation -----------------

func TestEntryValidateStaircase(t *testing.T) {
	t.Parallel()
	base := func() loadprofile.Entry {
		// A fully-resolved staircase entry as StoreConfig would persist
		// it: steps inside bounds, duration above the per-step floor.
		return loadprofile.Entry{
			ScenarioID: 1, Concurrency: 30, Rampup: 0, Engines: 2,
			Throughput: 20, Duration: 600, Mode: "staircase", Steps: 5,
		}
	}
	cases := []struct {
		name string
		mut  func(*loadprofile.Entry)
		want error
	}{
		{name: "resolved staircase entry is valid", mut: func(*loadprofile.Entry) {}, want: nil},
		{
			name: "minimum shape: two steps",
			mut:  func(e *loadprofile.Entry) { e.Steps = 2 },
			want: nil,
		},
		{
			name: "maximum shape: ten steps",
			mut:  func(e *loadprofile.Entry) { e.Steps = 10 },
			want: nil,
		},
		{
			name: "one step is not a staircase",
			mut:  func(e *loadprofile.Entry) { e.Steps = 1 },
			want: loadprofile.ErrStepsInvalid,
		},
		{
			name: "zero steps is unforgivable after resolution defaulted it",
			mut:  func(e *loadprofile.Entry) { e.Steps = 0 },
			want: loadprofile.ErrStepsInvalid,
		},
		{
			name: "eleven steps is a jagged ramp, not a staircase",
			mut:  func(e *loadprofile.Entry) { e.Steps = 11 },
			want: loadprofile.ErrStepsInvalid,
		},
		{
			name: "per-step hold below 60s reads as a jagged ramp",
			mut:  func(e *loadprofile.Entry) { e.Duration = 59 },
			want: loadprofile.ErrStaircaseDuration,
		},
		{
			name: "steps on a non-staircase entry is meaningless",
			mut:  func(e *loadprofile.Entry) { e.Mode = "burst"; e.Steps = 5 },
			want: loadprofile.ErrStepsInvalid,
		},
		{
			name: "steps on an advanced entry is refused too",
			mut: func(e *loadprofile.Entry) {
				e.Mode = ""
				e.Steps = 3
			},
			want: loadprofile.ErrStepsInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := base()
			tc.mut(&e)
			err := e.Validate()
			if tc.want == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Validate() = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestEntryStepsRoundTrip(t *testing.T) {
	t.Parallel()
	e := loadprofile.Entry{
		ScenarioID: 1, Concurrency: 30, Rampup: 0, Engines: 2,
		Throughput: 20, Duration: 600, Mode: "staircase", Steps: 4,
	}
	js, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(js), `"steps":4`) {
		t.Fatalf("JSON = %s, want steps carried on the wire", js)
	}
	var back loadprofile.Entry
	if err := json.Unmarshal(js, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Steps != 4 || back.Mode != "staircase" {
		t.Fatalf("round trip = %+v, want steps 4 / staircase", back)
	}
	// A stated staircase entry without steps decodes zero (resolution
	// defaults it before Validate, exactly like the other blank numbers).
	var stated loadprofile.Entry
	if err := json.Unmarshal([]byte(`{"name":"t","scenario_id":9,"mode":"staircase","throughput":25,"duration":120}`), &stated); err != nil {
		t.Fatalf("unmarshal stated entry: %v", err)
	}
	if stated.Steps != 0 || stated.Mode != "staircase" {
		t.Fatalf("stated entry decoded = %+v, want zero steps with mode staircase", stated)
	}
}

func TestLongestDurationSecondsCountsStaircaseSteps(t *testing.T) {
	t.Parallel()
	p := loadprofile.Profile{Tests: []loadprofile.Entry{
		{Concurrency: 30, Rampup: 0, Engines: 2, Duration: 120, Steps: 5}, // 5x120s
		{Concurrency: 5, Rampup: 30, Engines: 1, Duration: 300},          // 330s
	}}
	// The staircase occupies its pods for steps x hold, not one hold --
	// a quota reservation that covered a single hold would under-reserve
	// four fifths of the run.
	if got := p.LongestDurationSeconds(); got != 600 {
		t.Fatalf("LongestDurationSeconds() = %d, want 600 (5 steps x 120s)", got)
	}
}
