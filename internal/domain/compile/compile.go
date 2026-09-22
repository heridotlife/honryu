// Package compile turns Honryu's own model -- an execution, its load profile,
// and the scenarios it runs -- into a Taurus configuration.
//
// This is a pure transformation: no I/O, no clock, no randomness. The caller
// resolves storage keys to in-pod paths and hands them in, so the same inputs
// always compile to byte-identical YAML, which is what makes the generated
// config reviewable and the golden tests meaningful.
package compile

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
)

// Compilation errors. Callers compare with errors.Is.
var (
	ErrNoEntries        = errors.New("compile: load profile has no entries")
	ErrEngineRequired   = errors.New("compile: an engine must be selected")
	ErrScenarioMissing  = errors.New("compile: load profile references a scenario that was not supplied")
	ErrScriptRequired   = errors.New("compile: native scenario needs its script path")
	ErrRequestsRequired = errors.New("compile: portable scenario needs requests")
)

// ScenarioInput is one scenario and the artefacts it runs with. Paths are
// in-pod paths already resolved by the caller.
type ScenarioInput struct {
	Scenario scenario.Scenario
	// ScriptPath is where a native scenario's artefact is mounted.
	ScriptPath string
	// DefaultAddress is the target a portable scenario's requests are relative to.
	DefaultAddress string
	// Requests is the workload of a portable scenario.
	Requests []taurus.Request
	// Headers, Timeout and KeepAlive are a portable scenario's own fragment
	// settings, carried through to the engine. They merge beneath the
	// execution-level Input.Headers so telemetry's trace-context headers
	// win on collision (see compileScenario).
	Headers   map[string]string
	Timeout   taurus.Duration
	KeepAlive *bool
	// DataPaths are CSV data files mounted for the scenario.
	DataPaths []string
}

// Input is everything needed to compile one execution.
type Input struct {
	Execution execution.Execution
	Profile   loadprofile.Profile
	// Engine is the executor chosen for this execution. Every scenario in the
	// profile must be able to run on it.
	Engine taurus.Executor
	// Scenarios is keyed by scenario id, matching the profile's entries.
	Scenarios map[int64]ScenarioInput
	// Headers are applied scenario-wide by the engine on every request of the
	// run. It is how a deploy's trace-context headers (telemetry.Headers)
	// reach the generated load, with zero per-engine code; nil compiles to no
	// headers at all.
	Headers map[string]string
	// Criteria are Taurus pass/fail expressions. When present they become the
	// passfail module, whose outcome bzt reports through exit code 3 -- the
	// signal Honryu turns into an execution's verdict.
	Criteria []string
	// Sequential demands the config's executions run one at a time (phase
	// 98): compile emits bzt's modules.local.sequential flag, which the
	// local provisioner honours by chaining executions finish-then-start.
	// The staircase deploy path sets it for a shard's step blocks. It is
	// only honoured when the compiled config ends up with more than one
	// execution -- a single-execution config gains nothing.
	Sequential bool
}

// Taurus compiles the input into a Taurus configuration. The result is
// validated before it is returned, so a caller never has to run bzt to discover
// that the config was malformed.
func Taurus(in Input) (taurus.Config, error) {
	if in.Engine == "" {
		return taurus.Config{}, ErrEngineRequired
	}
	if len(in.Profile.Tests) == 0 {
		return taurus.Config{}, ErrNoEntries
	}

	cfg := taurus.Config{
		Execution: make([]taurus.Execution, 0, len(in.Profile.Tests)),
		Scenarios: make(map[string]taurus.Scenario, len(in.Profile.Tests)),
	}

	for _, entry := range in.Profile.Tests {
		si, ok := in.Scenarios[entry.ScenarioID]
		if !ok {
			return taurus.Config{}, fmt.Errorf("%w: scenario %d", ErrScenarioMissing, entry.ScenarioID)
		}
		if err := si.Scenario.CanRunOn(in.Engine); err != nil {
			return taurus.Config{}, fmt.Errorf("scenario %q: %w", si.Scenario.Name, err)
		}

		key := scenarioKey(si.Scenario)
		ts, err := compileScenario(si, in.Headers)
		if err != nil {
			return taurus.Config{}, err
		}
		cfg.Scenarios[key] = ts

		cfg.Execution = append(cfg.Execution, taurus.Execution{
			Executor:    in.Engine,
			Concurrency: entry.Concurrency,
			RampUp:      taurus.Duration(time.Duration(entry.Rampup) * time.Second),
			HoldFor:     taurus.Duration(time.Duration(entry.Duration) * time.Second),
			Throughput:  entry.Throughput,
			Scenario:    key,
		})
	}

	if len(in.Criteria) > 0 {
		// Floor assertions ("p95<800ms") compile to their violation form
		// ("p95>=800ms"): bzt's passfail grammar reads criteria as failure
		// conditions, so a literal floor would fail every fast target
		// (phase 104; live run 1 went failed on 73850 clean samples).
		cfg.Reporting = append(cfg.Reporting, taurus.Reporter{
			Module:   "passfail",
			Criteria: FloorCriteriaToViolation(in.Criteria),
		})
	}

	// A sequential compile with multiple executions (the staircase step
	// blocks) carries bzt's own flag for it: without
	// modules.local.sequential the provisioner would start every
	// execution at once and the shape would be a wall, not a staircase.
	if in.Sequential && len(cfg.Execution) > 1 {
		if cfg.Modules == nil {
			cfg.Modules = make(map[string]taurus.Module)
		}
		sequential := true
		cfg.Modules["local"] = taurus.Module{Sequential: &sequential}
	}

	if err := cfg.Validate(); err != nil {
		return taurus.Config{}, fmt.Errorf("compile: produced an invalid config: %w", err)
	}
	return cfg, nil
}

func compileScenario(si ScenarioInput, headers map[string]string) (taurus.Scenario, error) {
	if si.Scenario.Kind == scenario.KindNative {
		if si.ScriptPath == "" {
			return taurus.Scenario{}, fmt.Errorf("%w: scenario %q", ErrScriptRequired, si.Scenario.Name)
		}
		return taurus.Scenario{
			Script:      si.ScriptPath,
			Headers:     headers,
			DataSources: dataSources(si.DataPaths),
		}, nil
	}

	if len(si.Requests) == 0 {
		return taurus.Scenario{}, fmt.Errorf("%w: scenario %q", ErrRequestsRequired, si.Scenario.Name)
	}
	reqs := make([]taurus.Request, len(si.Requests))
	copy(reqs, si.Requests)
	for i := range reqs {
		// Labels are assigned by Honryu rather than left to the engine. Engine
		// defaults differ -- JMeter echoes the configured label, apiritif and k6
		// report the URL -- so an unlabelled request would produce metrics that
		// cannot be compared across engines or across runs.
		if reqs[i].Label == "" {
			reqs[i].Label = requestLabel(si.Scenario, reqs[i], i)
		}
	}
	// The fragment's own headers merge BENEATH the execution-level headers:
	// telemetry's traceparent and baggage must win on collision, or the
	// fragment could silently break trace correlation for the whole run.
	// Both sources empty -> nil, keeping the "no headers compile to none"
	// invariant (and omitempty's clean YAML).
	var merged map[string]string
	if len(si.Headers)+len(headers) > 0 {
		merged = make(map[string]string, len(si.Headers)+len(headers))
		for k, v := range si.Headers {
			merged[k] = v
		}
		for k, v := range headers {
			merged[k] = v
		}
	}
	return taurus.Scenario{
		DefaultAddress: si.DefaultAddress,
		Headers:        merged,
		Requests:       reqs,
		DataSources:    dataSources(si.DataPaths),
		Timeout:        si.Timeout,
		KeepAlive:      si.KeepAlive,
	}, nil
}

func dataSources(paths []string) []taurus.DataSource {
	if len(paths) == 0 {
		return nil
	}
	out := make([]taurus.DataSource, 0, len(paths))
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	for _, p := range sorted {
		out = append(out, taurus.DataSource{Path: p})
	}
	return out
}

// scenarioKey names a scenario within the config. The id keeps it unique when
// two scenarios share a name; the slug keeps the generated YAML readable, which
// matters because the config is retrievable for debugging.
func scenarioKey(s scenario.Scenario) string {
	return fmt.Sprintf("%s-%d", slug(s.Name), s.ID)
}

// requestLabel is stable across runs: it depends only on the scenario and the
// request's position and target, never on execution order or time.
func requestLabel(s scenario.Scenario, r taurus.Request, i int) string {
	if target := slug(r.URL); target != "" {
		return fmt.Sprintf("%s-%s", slug(s.Name), target)
	}
	return fmt.Sprintf("%s-%d", slug(s.Name), i)
}

// slug reduces a string to lowercase alphanumerics and single hyphens, so it is
// safe in a YAML key and in a metric label.
func slug(in string) string {
	var b strings.Builder
	lastHyphen := true // suppresses a leading hyphen
	for _, r := range strings.ToLower(in) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastHyphen = false
		case !lastHyphen:
			b.WriteRune('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}
