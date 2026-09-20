package compile_test

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/heridotlife/honryu/internal/domain/compile"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
)

var update = flag.Bool("update", false, "rewrite golden files")

func portableInput() compile.Input {
	return compile.Input{
		Execution: execution.Execution{ID: 7, Name: "supersale-readiness", ProjectID: 3},
		Engine:    taurus.ExecutorJMeter,
		Profile: loadprofile.Profile{Tests: []loadprofile.Entry{{
			ScenarioID: 11, Concurrency: 50, Rampup: 30, Duration: 600, Engines: 4,
		}}},
		Scenarios: map[int64]compile.ScenarioInput{
			11: {
				Scenario:       mustScenario("checkout"),
				DefaultAddress: "http://checkout.svc",
				Requests: []taurus.Request{
					{URL: "/cart", Method: "GET"},
					{URL: "/checkout", Method: "POST"},
				},
			},
		},
	}
}

func mustScenario(name string) scenario.Scenario {
	s, err := scenario.New(name, 3)
	if err != nil {
		panic(err)
	}
	s.ID = 11
	return s
}

// TestTaurus_FragmentHeadersTelemetryWins is G8's focused collision test:
// the compiled scenario's headers must carry the fragment's X-Auth, the
// execution's traceparent and baggage, and NOT the fragment's traceparent.
// The golden fixture pins the whole YAML; this pins the semantics in a
// failure message a human can read.
func TestTaurus_FragmentHeadersTelemetryWins(t *testing.T) {
	t.Parallel()
	in := portableInput()
	in.Headers = map[string]string{
		"traceparent": "00-telemetry-wins-01",
		"baggage":     "run-id=7",
	}
	keepalive := false
	si := in.Scenarios[11]
	si.Headers = map[string]string{
		"traceparent": "00-fragment-loses-01",
		"X-Auth":      "fragment-token",
	}
	si.Timeout = taurus.Duration(90 * 1e9)
	si.KeepAlive = &keepalive
	in.Scenarios[11] = si

	cfg, err := compile.Taurus(in)
	if err != nil {
		t.Fatalf("Taurus: %v", err)
	}
	key := "checkout-11"
	ts, ok := cfg.Scenarios[key]
	if !ok {
		t.Fatalf("scenario %q missing from compiled config: %v", key, cfg.Scenarios)
	}
	if got := ts.Headers["traceparent"]; got != "00-telemetry-wins-01" {
		t.Errorf("traceparent = %q, telemetry must win the collision", got)
	}
	if got := ts.Headers["baggage"]; got != "run-id=7" {
		t.Errorf("baggage = %q, want the execution-level value", got)
	}
	if got := ts.Headers["X-Auth"]; got != "fragment-token" {
		t.Errorf("X-Auth = %q, fragment headers must survive the merge", got)
	}
	if ts.Timeout != taurus.Duration(90*1e9) {
		t.Errorf("timeout = %v, want the fragment's 90s", ts.Timeout)
	}
	if ts.KeepAlive == nil || *ts.KeepAlive != false {
		t.Errorf("keepalive = %v, want the fragment's false", ts.KeepAlive)
	}
}

func mustNativeScenario(name string, engine taurus.Executor) scenario.Scenario {
	s, err := scenario.NewNative(name, 3, engine)
	if err != nil {
		panic(err)
	}
	s.ID = 11
	return s
}

func TestTaurus_Golden(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input func() compile.Input
	}{
		{"portable_jmeter", portableInput},
		{
			"native_k6_with_script",
			func() compile.Input {
				in := portableInput()
				in.Engine = taurus.ExecutorK6
				in.Scenarios[11] = compile.ScenarioInput{
					Scenario:   mustNativeScenario("checkout", taurus.ExecutorK6),
					ScriptPath: "/honryu/scenario/11/checkout.js",
				}
				return in
			},
		},
		{
			"with_criteria_and_data",
			func() compile.Input {
				in := portableInput()
				in.Criteria = []string{"failures>1% for 30s, stop as failed", "p95>500ms, continue as failed"}
				si := in.Scenarios[11]
				si.DataPaths = []string{"/honryu/execution/7/users.csv"}
				in.Scenarios[11] = si
				return in
			},
		},
		{
			"multiple_scenarios",
			func() compile.Input {
				in := portableInput()
				second := mustScenario("search")
				second.ID = 12
				in.Scenarios[12] = compile.ScenarioInput{
					Scenario:       second,
					DefaultAddress: "http://search.svc",
					Requests:       []taurus.Request{{URL: "/q", Method: "GET"}},
				}
				in.Profile.Tests = append(in.Profile.Tests, loadprofile.Entry{
					ScenarioID: 12, Concurrency: 10, Rampup: 5, Duration: 60, Engines: 1,
				})
				return in
			},
		},
		{
			// G8: the fragment's own headers/timeout/keepalive must reach
			// the engine, and the execution-level telemetry headers
			// (traceparent, baggage) must win the collision with the
			// fragment's traceparent while the rest of the fragment's
			// headers survive.
			"fragment_settings_telemetry_wins",
			func() compile.Input {
				in := portableInput()
				in.Headers = map[string]string{
					"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
					"baggage":     "run-id=7",
				}
				keepalive := true
				si := in.Scenarios[11]
				si.Headers = map[string]string{
					"traceparent": "00-fragment-should-lose-this-collision-01",
					"X-Auth":      "fragment-token",
				}
				si.Timeout = taurus.Duration(90 * 1e9)
				si.KeepAlive = &keepalive
				in.Scenarios[11] = si
				return in
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := compile.Taurus(tc.input())
			if err != nil {
				t.Fatalf("Taurus: %v", err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("compiled config does not validate: %v", err)
			}
			got, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			golden := filepath.Join("testdata", tc.name+".yaml")
			if *update {
				if err := os.WriteFile(golden, got, 0o600); err != nil {
					t.Fatalf("write golden: %v", err)
				}
			}
			want, err := os.ReadFile(golden) //nolint:gosec // fixed test path
			if err != nil {
				t.Fatalf("read golden (run with -update to create): %v", err)
			}
			if string(got) != string(want) {
				t.Errorf("compiled config differs from %s\n--- got ---\n%s\n--- want ---\n%s", golden, got, want)
			}
		})
	}
}

func TestTaurus_LabelsComeFromHonryu(t *testing.T) {
	t.Parallel()

	in := portableInput()
	// The caller supplied no labels at all.
	cfg, err := compile.Taurus(in)
	if err != nil {
		t.Fatalf("Taurus: %v", err)
	}
	reqs := cfg.Scenarios["checkout-11"].Requests
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	for _, r := range reqs {
		if r.Label == "" {
			t.Errorf("request %s has no label; labels must be assigned by Honryu", r.URL)
		}
		if !strings.HasPrefix(r.Label, "checkout") {
			t.Errorf("label %q is not derived from the scenario name", r.Label)
		}
	}
	if reqs[0].Label == reqs[1].Label {
		t.Errorf("labels are not distinct: %q", reqs[0].Label)
	}
}

func TestTaurus_DurationsUseProfileSeconds(t *testing.T) {
	t.Parallel()

	cfg, err := compile.Taurus(portableInput())
	if err != nil {
		t.Fatalf("Taurus: %v", err)
	}
	e := cfg.Execution[0]
	if got, _ := e.RampUp.MarshalYAML(); got != "30s" {
		t.Errorf("ramp-up = %v, want 30s", got)
	}
	if got, _ := e.HoldFor.MarshalYAML(); got != "10m" {
		t.Errorf("hold-for = %v, want 10m", got)
	}
	if e.Concurrency != 50 {
		t.Errorf("concurrency = %d, want 50", e.Concurrency)
	}
}

func TestTaurus_Errors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		mutate  func(*compile.Input)
		wantErr error
		mention string
	}{
		{
			"engine the scenario cannot run on",
			func(in *compile.Input) { in.Engine = taurus.ExecutorK6 },
			scenario.ErrEngineNeedsScript,
			"k6",
		},
		{
			"native scenario on another engine",
			func(in *compile.Input) {
				in.Scenarios[11] = compile.ScenarioInput{
					Scenario:   mustNativeScenario("checkout", taurus.ExecutorJMeter),
					ScriptPath: "/x.jmx",
				}
				in.Engine = taurus.ExecutorGatling
			},
			scenario.ErrEnginePinned,
			"gatling",
		},
		{
			"profile references an unknown scenario",
			func(in *compile.Input) { in.Profile.Tests[0].ScenarioID = 999 },
			compile.ErrScenarioMissing,
			"999",
		},
		{
			"empty profile",
			func(in *compile.Input) { in.Profile.Tests = nil },
			compile.ErrNoEntries,
			"",
		},
		{
			"no engine selected",
			func(in *compile.Input) { in.Engine = "" },
			compile.ErrEngineRequired,
			"",
		},
		{
			"native scenario without its script",
			func(in *compile.Input) {
				in.Scenarios[11] = compile.ScenarioInput{
					Scenario: mustNativeScenario("checkout", taurus.ExecutorJMeter),
				}
			},
			compile.ErrScriptRequired,
			"checkout",
		},
		{
			"portable scenario without requests",
			func(in *compile.Input) {
				in.Scenarios[11] = compile.ScenarioInput{Scenario: mustScenario("checkout")}
			},
			compile.ErrRequestsRequired,
			"checkout",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := portableInput()
			tc.mutate(&in)
			_, err := compile.Taurus(in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Taurus() = %v, want %v", err, tc.wantErr)
			}
			if tc.mention != "" && !strings.Contains(err.Error(), tc.mention) {
				t.Errorf("error %q does not mention %q", err, tc.mention)
			}
		})
	}
}

// A compiled config must always satisfy the Taurus model's own invariants;
// otherwise the failure surfaces inside a pod instead of here.
func TestTaurus_OutputAlwaysValidates(t *testing.T) {
	t.Parallel()

	for _, engine := range taurus.DeclarativeExecutors() {
		in := portableInput()
		in.Engine = engine
		cfg, err := compile.Taurus(in)
		if err != nil {
			t.Fatalf("Taurus(%s): %v", engine, err)
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("config for %s does not validate: %v", engine, err)
		}
		if cfg.Execution[0].Executor != engine {
			t.Errorf("executor = %q, want %q", cfg.Execution[0].Executor, engine)
		}
	}
}

// Labels have to stay usable as metric labels whatever the scenario or URL is
// called, including names that slug away to nothing.
func TestTaurus_LabelsSurviveAwkwardNames(t *testing.T) {
	t.Parallel()

	in := portableInput()
	s := mustScenario("Check Out / Cart!")
	in.Scenarios[11] = compile.ScenarioInput{
		Scenario:       s,
		DefaultAddress: "http://x",
		Requests: []taurus.Request{
			{URL: "/", Method: "GET"},   // slugs to empty -- falls back to an index
			{URL: "///", Method: "GET"}, // likewise
			{URL: "/A_b-C", Method: "GET"},
		},
	}
	cfg, err := compile.Taurus(in)
	if err != nil {
		t.Fatalf("Taurus: %v", err)
	}

	var key string
	for k := range cfg.Scenarios {
		key = k
	}
	if key != "check-out-cart-11" {
		t.Errorf("scenario key = %q, want %q", key, "check-out-cart-11")
	}

	seen := map[string]bool{}
	for _, r := range cfg.Scenarios[key].Requests {
		if r.Label == "" {
			t.Fatalf("request %q produced an empty label", r.URL)
		}
		for _, c := range r.Label {
			isSafe := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'
			if !isSafe {
				t.Errorf("label %q contains %q, which is unsafe in a metric label", r.Label, c)
			}
		}
		if seen[r.Label] {
			t.Errorf("duplicate label %q", r.Label)
		}
		seen[r.Label] = true
	}
}

// A rate-limited profile must stay rate-limited in the config; losing the
// throughput would let the run go as fast as it could and measure the wrong
// thing entirely.
func TestTaurus_CarriesThroughput(t *testing.T) {
	t.Parallel()

	in := portableInput()
	in.Profile.Tests[0].Throughput = 2500
	cfg, err := compile.Taurus(in)
	if err != nil {
		t.Fatalf("Taurus: %v", err)
	}
	if got := cfg.Execution[0].Throughput; got != 2500 {
		t.Errorf("throughput = %d, want 2500", got)
	}

	// Unlimited must not become a cap of zero requests per second.
	in.Profile.Tests[0].Throughput = 0
	cfg, err = compile.Taurus(in)
	if err != nil {
		t.Fatalf("Taurus: %v", err)
	}
	if got := cfg.Execution[0].Throughput; got != 0 {
		t.Errorf("unlimited throughput became %d", got)
	}
}

// Headers handed to the compiler land on the compiled scenario itself, for
// native and portable alike: this is the seam a deploy's trace-context headers
// are injected through, with zero per-engine code. A nil map must compile to
// no headers at all -- the golden fixtures rely on it.
func TestTaurus_CarriesHeaders(t *testing.T) {
	t.Parallel()

	headers := map[string]string{
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00",
		"baggage":     "honryu.service=3,honryu.execution=7,honryu.run=4bf92f3577b34da6a3ce929d0e0e4736",
	}

	t.Run("portable", func(t *testing.T) {
		t.Parallel()

		in := portableInput()
		in.Headers = headers
		cfg, err := compile.Taurus(in)
		if err != nil {
			t.Fatalf("Taurus: %v", err)
		}
		if got := cfg.Scenarios["checkout-11"].Headers; !reflect.DeepEqual(got, headers) {
			t.Errorf("scenario headers = %v, want %v", got, headers)
		}
	})

	t.Run("native", func(t *testing.T) {
		t.Parallel()

		in := portableInput()
		in.Engine = taurus.ExecutorK6
		in.Scenarios[11] = compile.ScenarioInput{
			Scenario:   mustNativeScenario("checkout", taurus.ExecutorK6),
			ScriptPath: "/honryu/scenario/11/checkout.js",
		}
		in.Headers = headers
		cfg, err := compile.Taurus(in)
		if err != nil {
			t.Fatalf("Taurus: %v", err)
		}
		if got := cfg.Scenarios["checkout-11"].Headers; !reflect.DeepEqual(got, headers) {
			t.Errorf("scenario headers = %v, want %v", got, headers)
		}
	})

	t.Run("nil headers compile to none", func(t *testing.T) {
		t.Parallel()

		cfg, err := compile.Taurus(portableInput())
		if err != nil {
			t.Fatalf("Taurus: %v", err)
		}
		if got := cfg.Scenarios["checkout-11"].Headers; got != nil {
			t.Errorf("scenario headers = %v, want nil", got)
		}
	})
}

// A sequential compile (the staircase deploy path) must carry the bzt
// flag that makes the engine run its executions one at a time, and must
// not invent it for an ordinary single-execution config.
func TestTaurus_SequentialFlag(t *testing.T) {
	t.Parallel()

	in := portableInput()
	in.Sequential = true
	in.Profile.Tests = append(in.Profile.Tests, loadprofile.Entry{
		ScenarioID: 11, Concurrency: 20, Rampup: 0, Duration: 60, Engines: 1, Throughput: 10,
	})
	cfg, err := compile.Taurus(in)
	if err != nil {
		t.Fatalf("Taurus: %v", err)
	}
	mod, ok := cfg.Modules["local"]
	if !ok || mod.Sequential == nil || !*mod.Sequential {
		t.Fatalf("Modules[local] = %+v, want sequential: true", cfg.Modules["local"])
	}
	// The flag marshals in bzt's own grammar: modules.local.sequential.
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), "sequential: true") {
		t.Fatalf("compiled YAML lacks 'sequential: true':\n%s", raw)
	}

	// Single-execution configs (everything before phase 98) stay bare:
	// the golden fixtures' byte-identity must not move.
	single := portableInput()
	single.Sequential = true
	cfg, err = compile.Taurus(single)
	if err != nil {
		t.Fatalf("Taurus(single): %v", err)
	}
	if _, ok := cfg.Modules["local"]; ok {
		t.Fatalf("single-execution config gained modules: %+v", cfg.Modules)
	}
}
