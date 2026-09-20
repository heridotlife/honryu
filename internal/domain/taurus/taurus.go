// Package taurus holds the Taurus (bzt) configuration model: the shape Honryu
// compiles executions and scenarios into, and the contract every engine runs
// behind. Field names and yaml keys follow bzt 1.16 exactly -- the point of
// adopting Taurus is that these are the community's terms, so they are not
// renamed here.
//
// Pure domain: no I/O, no serializer imported. Callers marshal with any YAML
// encoder; the struct tags carry the wire format.
package taurus

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Validation errors. Callers compare with errors.Is.
var (
	ErrNoExecution        = errors.New("taurus: at least one execution is required")
	ErrExecutorRequired   = errors.New("taurus: execution requires an executor")
	ErrConcurrencyInvalid = errors.New("taurus: concurrency must be greater than zero")
	ErrScenarioRequired   = errors.New("taurus: execution must reference a scenario")
	ErrScenarioUndefined  = errors.New("taurus: execution references an undefined scenario")
	ErrScenarioEmpty      = errors.New("taurus: scenario needs a script or requests")
	ErrDurationRequired   = errors.New("taurus: execution needs hold-for or iterations")
)

// Duration is a time.Duration that marshals in the form bzt reads ("30s", "5m").
// Honryu carries durations as seconds internally; rendering them through this
// type keeps a bare number from ever reaching a Taurus config, where the unit
// would be ambiguous.
type Duration time.Duration

// MarshalYAML renders the duration as a bzt-compatible string. Values below a
// second round up: bzt has no sub-second resolution for load phases, and
// truncating to "0s" would silently drop the phase.
func (d Duration) MarshalYAML() (any, error) {
	td := time.Duration(d)
	switch {
	case td == 0:
		return "0s", nil
	case td%time.Hour == 0:
		return fmt.Sprintf("%dh", td/time.Hour), nil
	case td%time.Minute == 0:
		return fmt.Sprintf("%dm", td/time.Minute), nil
	case td < time.Second:
		return "1s", nil
	default:
		return fmt.Sprintf("%ds", td/time.Second), nil
	}
}

// UnmarshalYAML accepts the bzt forms ("30s", "5m", "2h") and a bare number of
// seconds, which bzt also allows.
func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf("taurus: duration must be a string or a number of seconds: %w", err)
	}
	if td, err := time.ParseDuration(s); err == nil {
		*d = Duration(td)
		return nil
	}
	// A bare number means seconds to bzt. YAML decodes an unquoted 45 into this
	// same string, so the numeric form has to be handled here rather than in a
	// separate unmarshal attempt.
	secs, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("taurus: %q is neither a duration nor a number of seconds", s)
	}
	*d = Duration(time.Duration(secs) * time.Second)
	return nil
}

// Config is a complete Taurus configuration.
type Config struct {
	Execution []Execution         `yaml:"execution,omitempty" json:"execution,omitempty"`
	Scenarios map[string]Scenario `yaml:"scenarios,omitempty" json:"scenarios,omitempty"`
	Reporting []Reporter          `yaml:"reporting,omitempty" json:"reporting,omitempty"`
	Services  []Service           `yaml:"services,omitempty" json:"services,omitempty"`
	Settings  *Settings           `yaml:"settings,omitempty" json:"settings,omitempty"`
	Modules   map[string]Module   `yaml:"modules,omitempty" json:"modules,omitempty"`
}

// Execution is one load phase: an engine driving one scenario at a given load.
// It is the unit Honryu shards across engine pods.
type Execution struct {
	Executor    Executor `yaml:"executor,omitempty" json:"executor,omitempty"`
	Concurrency int      `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`
	RampUp      Duration `yaml:"ramp-up,omitempty" json:"ramp-up,omitempty"`
	HoldFor     Duration `yaml:"hold-for,omitempty" json:"hold-for,omitempty"`
	Iterations  int      `yaml:"iterations,omitempty" json:"iterations,omitempty"`
	Throughput  int      `yaml:"throughput,omitempty" json:"throughput,omitempty"`
	Scenario    string   `yaml:"scenario,omitempty" json:"scenario,omitempty"`
}

// Scenario is a named workload: either an engine-native script or a declarative
// request list. Only some engines accept the declarative form -- k6, for one, is
// script-only -- which is why Honryu tracks scenario portability separately.
type Scenario struct {
	Script         string            `yaml:"script,omitempty" json:"script,omitempty"`
	DefaultAddress string            `yaml:"default-address,omitempty" json:"default-address,omitempty"`
	Headers        map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Requests       []Request         `yaml:"requests,omitempty" json:"requests,omitempty"`
	DataSources    []DataSource      `yaml:"data-sources,omitempty" json:"data-sources,omitempty"`
	Timeout        Duration          `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	KeepAlive      *bool             `yaml:"keepalive,omitempty" json:"keepalive,omitempty"`
}

// Request is one declarative HTTP request within a scenario.
type Request struct {
	URL     string            `yaml:"url" json:"url"`
	Label   string            `yaml:"label,omitempty" json:"label,omitempty"`
	Method  string            `yaml:"method,omitempty" json:"method,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Body    any               `yaml:"body,omitempty" json:"body,omitempty"`
	Assert  []Assertion       `yaml:"assert,omitempty" json:"assert,omitempty"`
}

// Assertion checks a response; a failed assertion counts as a failed sample.
type Assertion struct {
	Contains []string `yaml:"contains" json:"contains"`
	Subject  string   `yaml:"subject,omitempty" json:"subject,omitempty"`
	Regexp   *bool    `yaml:"regexp,omitempty" json:"regexp,omitempty"`
	Not      *bool    `yaml:"not,omitempty" json:"not,omitempty"`
}

// DataSource attaches a CSV to a scenario.
type DataSource struct {
	Path      string `yaml:"path" json:"path"`
	Delimiter string `yaml:"delimiter,omitempty" json:"delimiter,omitempty"`
	Loop      *bool  `yaml:"loop,omitempty" json:"loop,omitempty"`
	Quoted    *bool  `yaml:"quoted,omitempty" json:"quoted,omitempty"`
}

// Reporter is one entry of the reporting chain. The passfail module carries the
// criteria that decide an execution's verdict; bzt exits 3 when they fail.
type Reporter struct {
	Module   string   `yaml:"module" json:"module"`
	Criteria []string `yaml:"criteria,omitempty" json:"criteria,omitempty"`
	Filename string   `yaml:"filename,omitempty" json:"filename,omitempty"`
	DumpCSV  string   `yaml:"dump-csv,omitempty" json:"dump-csv,omitempty"`
}

// Service is one entry of the services chain (shellexec, monitoring, ...).
type Service struct {
	Module string `yaml:"module" json:"module"`
}

// Settings are engine-wide bzt settings.
type Settings struct {
	ArtifactsDir string `yaml:"artifacts-dir,omitempty" json:"artifacts-dir,omitempty"`
	CheckUpdates *bool  `yaml:"check-updates,omitempty" json:"check-updates,omitempty"`
	Verbose      *bool  `yaml:"verbose,omitempty" json:"verbose,omitempty"`
}

// Module configures a bzt module by alias, e.g. pointing an alias at a class or
// overriding an executor's path.
type Module struct {
	Class string `yaml:"class,omitempty" json:"class,omitempty"`
	Path  string `yaml:"path,omitempty" json:"path,omitempty"`
	// Sequential marks a module-level flag the compiled config relies on
	// (phase 98): bzt's local provisioner runs a config's executions one
	// at a time when modules.local.sequential is true -- the mechanism a
	// staircase's step blocks sequence through. Configs deep-merge in
	// bzt, so setting it never disturbs the alias's class binding.
	Sequential *bool `yaml:"sequential,omitempty" json:"sequential,omitempty"`
}

// Validate checks the invariants bzt would otherwise fail on at runtime, in a
// pod, minutes into a scheduled run. The scenario reference is the join between
// the two halves of a config and the likeliest thing for a compiler bug to get
// wrong, so an undefined reference is named.
func (c Config) Validate() error {
	if len(c.Execution) == 0 {
		return ErrNoExecution
	}
	for i, e := range c.Execution {
		if err := e.validate(c.Scenarios); err != nil {
			return fmt.Errorf("execution %d: %w", i, err)
		}
	}
	for name, s := range c.Scenarios {
		if len(s.Script) == 0 && len(s.Requests) == 0 {
			return fmt.Errorf("scenario %q: %w", name, ErrScenarioEmpty)
		}
	}
	return nil
}

func (e Execution) validate(scenarios map[string]Scenario) error {
	switch {
	case e.Executor == "":
		return ErrExecutorRequired
	case e.Concurrency <= 0:
		return ErrConcurrencyInvalid
	case e.Scenario == "":
		return ErrScenarioRequired
	}
	if _, ok := scenarios[e.Scenario]; !ok {
		return fmt.Errorf("%w: %q", ErrScenarioUndefined, e.Scenario)
	}
	// Without a bound the run never ends; bzt would hold load indefinitely.
	if e.HoldFor == 0 && e.Iterations == 0 {
		return ErrDurationRequired
	}
	return nil
}
