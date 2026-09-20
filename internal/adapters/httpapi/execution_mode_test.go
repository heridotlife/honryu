// Phase 90's wire contract: PUT /executions/{id}/config accepts mode
// entries (tests[].mode + throughput + duration, nothing else), the
// server resolves them, refusals are 409 with the FanOut status and
// capacity key in the details envelope, nothing persists on refusal, and
// GET echoes mode + resolved numbers. The old payloads -- with no mode
// anywhere -- must keep taking the byte-identical path they always did.
package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/domain/account"
	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/capacityprofile"
	"github.com/heridotlife/honryu/internal/domain/rbac"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// modeStubCapacity scripts the capacity-profile answers a mode PUT
// resolves against. engine/cpu/memory are pre-normalized by the test
// (baseline 500m/512Mi for an ordinary execution); profile nil means
// "nothing calibrated for the key". The scenario's "current" fingerprint
// is fixed at fpCurrent: profiles stamped fpCurrent are fresh, any other
// stamp reads stale.
const fpCurrent = "fp"

type modeStubCapacity struct {
	key       capacityprofile.Key
	profile   *capacityprofile.CapacityProfile
	fanoutAsk []capacityprofile.Key
}

func (c *modeStubCapacity) FanOut(_ context.Context, key capacityprofile.Key, targetQPS float64) (capacityprofile.Result, error) {
	c.fanoutAsk = append(c.fanoutAsk, key)
	if c.profile == nil {
		return capacityprofile.FanOut(nil, targetQPS, ""), nil
	}
	return capacityprofile.FanOut(c.profile, targetQPS, fpCurrent), nil
}

func (c *modeStubCapacity) ProfileFor(_ context.Context, key capacityprofile.Key) (capacityprofile.CapacityProfile, error) {
	if c.profile == nil || key != c.key {
		return capacityprofile.CapacityProfile{}, ports.ErrNotFound
	}
	return *c.profile, nil
}

// newModeRouter wires the ordinary test router plus scriptable mode
// sources, and provisions the project/scenario/execution triple a config
// PUT needs. The scenario's requests fragment is not needed: StoreConfig
// validates the scenario exists and belongs to the project, nothing else.
func newModeRouter(t *testing.T, profile *capacityprofile.CapacityProfile) (http.Handler, *modeStubCapacity, string, string) {
	t.Helper()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	cap := &modeStubCapacity{profile: profile}
	h := httpapi.NewRouter(httpapi.Deps{
		Projects:  projectapp.NewService(store),
		Scenarios: scenarioapp.NewService(store, obj),
		Executions: executionapp.NewService(store, obj, 100).WithModeSources(executionapp.ModeSources{
			Capacity:      cap,
			Jobs:          store,
			Reports:       store,
			LatencyHint:   250 * time.Millisecond,
			DefaultEngine: taurus.ExecutorJMeter,
		}),
		Store:         obj,
		DefaultOwners: []string{"honryu"},
	})

	rec := postForm(t, h, "/api/projects", url.Values{"name": {"modeproj"}, "owner": {"honryu"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project = %d (%s)", rec.Code, rec.Body.String())
	}
	rec = postForm(t, h, "/api/scenarios", url.Values{"name": {"modescen"}, "project_id": {"1"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create scenario = %d (%s)", rec.Code, rec.Body.String())
	}
	scenID := decodeID(t, rec)
	rec = postForm(t, h, "/api/executions", url.Values{"name": {"modeexec"}, "project_id": {"1"}, "engine": {"jmeter"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create execution = %d (%s)", rec.Code, rec.Body.String())
	}
	cap.key = capacityprofile.Key{ScenarioID: scenID, Engine: taurus.ExecutorJMeter, CPU: "500m", Memory: "512Mi"}
	return h, cap, strconv.FormatInt(decodeID(t, rec), 10), strconv.FormatInt(scenID, 10)
}

// putConfigJSON PUTs a raw JSON body to the config route.
func putConfigJSON(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// getConfig decodes GET /config's multi-test wrapper.
func getConfig(t *testing.T, h http.Handler, path string) map[string]any {
	t.Helper()
	rec := do(t, h, http.MethodGet, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET config = %d (%s)", rec.Code, rec.Body.String())
	}
	var wrapped struct {
		Content map[string]any `json:"multi-test"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &wrapped); err != nil {
		t.Fatalf("decode config: %v (%s)", err, rec.Body.String())
	}
	return wrapped.Content
}

// configTests returns the wrapper's tests array (nil -- a never-configured
// execution -- reads as empty).
func configTests(t *testing.T, cfg map[string]any) []map[string]any {
	t.Helper()
	raw, _ := cfg["tests"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			t.Fatalf("config test row = %#v, want an object", r)
		}
		out = append(out, m)
	}
	return out
}

// The happy path over the wire: a Simple-mode body (mode + rate +
// duration only) is accepted, resolved server-side, and echoed back by
// GET with the mode provenance and the derived numbers.
func TestPutExecutionConfig_ModeResolvesAndEchoes(t *testing.T) {
	t.Parallel()
	profile := &capacityprofile.CapacityProfile{
		PerPodQPS: 125, SaturatedBy: calibration.SaturatedByEngine,
		ScenarioFingerprint: "fp", JobID: 0, // no report chain: the 250ms fallback sizes threads
	}
	h, cap, execID, scenID := newModeRouter(t, profile)
	path := "/api/executions/" + execID + "/config"

	body := fmt.Sprintf(`{"name":"modeexec","project_id":1,"execution_id":%s,"tests":[{"name":"t","scenario_id":%s,"mode":"burst","throughput":500,"duration":600}]}`, execID, scenID)
	if rec := putConfigJSON(t, h, path, body); rec.Code != http.StatusOK {
		t.Fatalf("mode config put = %d (%s)", rec.Code, rec.Body.String())
	}

	cfg := getConfig(t, h, path)
	tests := configTests(t, cfg)
	if len(tests) != 1 {
		t.Fatalf("tests = %d, want 1", len(tests))
	}
	got := tests[0]
	// Engines ceil(500/125)=4, concurrency ceil(500*0.25*3)=375, ramp-up 0.
	for field, want := range map[string]float64{"engines": 4, "concurrency": 375, "rampup": 0, "throughput": 500, "duration": 600} {
		if got[field] != want {
			t.Errorf("echoed %s = %v, want %v", field, got[field], want)
		}
	}
	if got["mode"] != "burst" {
		t.Errorf("echoed mode = %v, want burst", got["mode"])
	}
	if len(cap.fanoutAsk) != 1 {
		t.Fatalf("fan-out asked %v, want exactly one key", cap.fanoutAsk)
	}
}

// The 409 matrix: every non-ok FanOut status refuses the PUT, names the
// status and the capacity key in the details envelope, carries a
// remediation hint, and persists nothing.
func TestPutExecutionConfig_ModeRefusalMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		profile *capacityprofile.CapacityProfile
		status  string
	}{
		{"no profile", nil, "no_profile"},
		{"stale", &capacityprofile.CapacityProfile{
			PerPodQPS: 100, SaturatedBy: calibration.SaturatedByEngine, ScenarioFingerprint: "fp-old",
		}, "stale"},
		{"target limited", &capacityprofile.CapacityProfile{
			PerPodQPS: 100, SaturatedBy: calibration.SaturatedByTarget, ScenarioFingerprint: "fp",
		}, "target_limited"},
		{"inconclusive", &capacityprofile.CapacityProfile{
			PerPodQPS: 100, SaturatedBy: calibration.SaturatedByNeither, ScenarioFingerprint: "fp",
		}, "inconclusive"},
		{"engine floor", &capacityprofile.CapacityProfile{
			PerPodQPS: 0, SaturatedBy: calibration.SaturatedByEngine, ScenarioFingerprint: "fp",
		}, "engine_floor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, cap, execID, scenID := newModeRouter(t, tc.profile)
			path := "/api/executions/" + execID + "/config"

			body := fmt.Sprintf(`{"name":"modeexec","project_id":1,"execution_id":%s,"tests":[{"name":"t","scenario_id":%s,"mode":"soak","throughput":100,"duration":3600}]}`, execID, scenID)
			rec := putConfigJSON(t, h, path, body)
			if rec.Code != http.StatusConflict {
				t.Fatalf("mode config put = %d, want 409 (%s)", rec.Code, rec.Body.String())
			}
			var env struct {
				Message string         `json:"message"`
				Details map[string]any `json:"details"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode 409 body: %v (%s)", err, rec.Body.String())
			}
			if env.Details["fanout_status"] != tc.status {
				t.Errorf("details fanout_status = %v, want %q", env.Details["fanout_status"], tc.status)
			}
			if !strings.Contains(env.Message, tc.status) {
				t.Errorf("message %q does not name the status %q", env.Message, tc.status)
			}
			if scen, ok := env.Details["scenario_id"].(float64); !ok || scen != float64(cap.key.ScenarioID) {
				t.Errorf("details scenario_id = %v, want %d", env.Details["scenario_id"], cap.key.ScenarioID)
			}
			if env.Details["engine"] != "jmeter" || env.Details["cpu"] != "500m" || env.Details["memory"] != "512Mi" {
				t.Errorf("details capacity key = %v/%v/%v, want jmeter/500m/512Mi", env.Details["engine"], env.Details["cpu"], env.Details["memory"])
			}
			if hint, ok := env.Details["hint"].(string); !ok || !strings.Contains(hint, "calibrate") && !strings.Contains(hint, "Advanced") {
				t.Errorf("details hint = %v, want a remediation naming calibrate or Advanced", env.Details["hint"])
			}

			// Nothing persisted: the profile is still empty.
			cfg := getConfig(t, h, path)
			if tests := configTests(t, cfg); len(tests) != 0 {
				t.Fatalf("tests after refusal = %v, want none persisted", tests)
			}
		})
	}
}

// Old payloads are byte-compatible: a JSON body with no mode field (and a
// multipart YAML one) is stored exactly as sent, and the mode machinery
// is never consulted -- no fan-out call for advanced entries.
func TestPutExecutionConfig_AdvancedPayloadsUnchanged(t *testing.T) {
	t.Parallel()
	profile := &capacityprofile.CapacityProfile{
		PerPodQPS: 100, SaturatedBy: calibration.SaturatedByEngine, ScenarioFingerprint: "fp",
	}
	h, cap, execID, scenID := newModeRouter(t, profile)
	path := "/api/executions/" + execID + "/config"

	// The pre-phase-90 JSON shape, verbatim.
	body := fmt.Sprintf(`{"name":"modeexec","project_id":1,"execution_id":%s,"tests":[{"name":"t","scenario_id":%s,"concurrency":5,"rampup":10,"duration":30,"engines":2}],"csv_split":false}`, execID, scenID)
	if rec := putConfigJSON(t, h, path, body); rec.Code != http.StatusOK {
		t.Fatalf("advanced json put = %d (%s)", rec.Code, rec.Body.String())
	}
	cfg := getConfig(t, h, path)
	tests := configTests(t, cfg)
	if len(tests) != 1 {
		t.Fatalf("tests = %d, want 1", len(tests))
	}
	got := tests[0]
	for field, want := range map[string]float64{"concurrency": 5, "rampup": 10, "duration": 30, "engines": 2} {
		if got[field] != want {
			t.Errorf("advanced %s = %v, want %v (stored exactly as sent)", field, got[field], want)
		}
	}
	if _, present := got["mode"]; present {
		t.Errorf("advanced echo carries mode = %v, want the key absent", got["mode"])
	}

	// The multipart YAML path, unchanged file format.
	mp := fmt.Sprintf("multi-test:\n  collectionid: %s\n  tests:\n    - testid: %s\n      concurrency: 3\n      rampup: 5\n      duration: 10\n      engines: 1\n", execID, scenID)
	if rec := putMultipart(t, h, path, "config.yaml", mp); rec.Code != http.StatusOK {
		t.Fatalf("advanced multipart put = %d (%s)", rec.Code, rec.Body.String())
	}

	if len(cap.fanoutAsk) != 0 {
		t.Fatalf("fan-out asked = %v, want no capacity consultation for advanced payloads", cap.fanoutAsk)
	}
}

// A mode field on the multipart YAML path resolves too: both body forms
// reach the same StoreConfig, so the same resolution applies.
func TestPutExecutionConfig_ModeViaMultipartYAML(t *testing.T) {
	t.Parallel()
	profile := &capacityprofile.CapacityProfile{
		PerPodQPS: 100, SaturatedBy: calibration.SaturatedByEngine, ScenarioFingerprint: "fp",
	}
	h, _, execID, scenID := newModeRouter(t, profile)
	path := "/api/executions/" + execID + "/config"

	mp := fmt.Sprintf("multi-test:\n  collectionid: %s\n  tests:\n    - testid: %s\n      mode: ramp\n      throughput: 200\n      duration: 600\n", execID, scenID)
	if rec := putMultipart(t, h, path, "config.yaml", mp); rec.Code != http.StatusOK {
		t.Fatalf("mode multipart put = %d (%s)", rec.Code, rec.Body.String())
	}
	tests := configTests(t, getConfig(t, h, path))
	if len(tests) != 1 {
		t.Fatalf("tests = %d, want 1", len(tests))
	}
	if tests[0]["mode"] != "ramp" || tests[0]["engines"] != float64(2) || tests[0]["rampup"] != float64(120) {
		t.Fatalf("resolved entry = %v, want ramp/2 engines/120s ramp-up", tests[0])
	}
}

// Invalid mode statements are client errors, not conflicts: 400 with the
// loadprofile sentinel's message.
func TestPutExecutionConfig_ModeInputErrors(t *testing.T) {
	t.Parallel()
	h, _, execID, scenID := newModeRouter(t, nil)
	path := "/api/executions/" + execID + "/config"

	t.Run("unknown mode", func(t *testing.T) {
		t.Parallel()
		body := fmt.Sprintf(`{"name":"modeexec","project_id":1,"execution_id":%s,"tests":[{"name":"t","scenario_id":%s,"mode":"steady","throughput":100,"duration":600}]}`, execID, scenID)
		if rec := putConfigJSON(t, h, path, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("put = %d, want 400 (%s)", rec.Code, rec.Body.String())
		}
	})
	t.Run("mode without a rate", func(t *testing.T) {
		t.Parallel()
		body := fmt.Sprintf(`{"name":"modeexec","project_id":1,"execution_id":%s,"tests":[{"name":"t","scenario_id":%s,"mode":"soak","duration":3600}]}`, execID, scenID)
		if rec := putConfigJSON(t, h, path, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("put = %d, want 400 (%s)", rec.Code, rec.Body.String())
		}
	})
}

// RBAC is unchanged for mode bodies: the config route's
// execution:update gate runs before any resolution -- a viewer's mode PUT
// is a 403 that never consults a capacity profile, exactly as their
// advanced PUT always was.
func TestPutExecutionConfig_ModeRBACUnchanged(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	acme := createTenant(t, f, "acme", "Acme")
	f.prov.Register("carol-tok", account.Account{Subject: "carol"})
	assignRole(t, f, acme, "carol", rbac.RoleTenantViewer)

	projectID := createProjectInTenantReturningID(t, f, "acme-web", "team-a", acme)
	scenarioID := decodeID(t, f.req(t, http.MethodPost, "/api/scenarios", "admin-tok",
		url.Values{"name": {"smoke"}, "project_id": {strconv.FormatInt(projectID, 10)}}))
	executionID := decodeID(t, f.req(t, http.MethodPost, "/api/executions", "admin-tok",
		url.Values{"name": {"peak"}, "project_id": {strconv.FormatInt(projectID, 10)}, "engine": {"jmeter"}}))

	body := fmt.Sprintf(`{"name":"peak","project_id":%d,"execution_id":%d,"tests":[{"name":"t","scenario_id":%d,"mode":"burst","throughput":100,"duration":600}]}`,
		projectID, executionID, scenarioID)
	path := "/api/executions/" + strconv.FormatInt(executionID, 10) + "/config"
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer carol-tok")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer mode config put = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

// Phase 98's wire contract: PUT accepts mode=staircase with an optional
// steps count (defaulted server-side), resolves at the ceiling, and echoes
// the entry with steps riding as provenance. Input refusals (steps out of
// bounds, per-step hold under the 60s floor) are 400s; the 409 matrix
// above covers staircase identically through the mode machinery it shares.
func TestPutExecutionConfig_StaircaseResolvesAndEchoes(t *testing.T) {
	t.Parallel()
	profile := &capacityprofile.CapacityProfile{
		PerPodQPS: 10, SaturatedBy: calibration.SaturatedByEngine,
		ScenarioFingerprint: "fp", JobID: 0, // no report chain: the 250ms fallback sizes threads
	}
	h, _, execID, scenID := newModeRouter(t, profile)
	path := "/api/executions/" + execID + "/config"

	// Stated without steps: the default (5) is what persists.
	body := fmt.Sprintf(`{"name":"modeexec","project_id":1,"execution_id":%s,"tests":[{"name":"t","scenario_id":%s,"mode":"staircase","throughput":20,"duration":300}]}`, execID, scenID)
	if rec := putConfigJSON(t, h, path, body); rec.Code != http.StatusOK {
		t.Fatalf("staircase config put = %d (%s)", rec.Code, rec.Body.String())
	}
	cfg := getConfig(t, h, path)
	tests := configTests(t, cfg)
	if len(tests) != 1 {
		t.Fatalf("tests = %d, want 1", len(tests))
	}
	got := tests[0]
	// Engines ceil(20/10)=2 at the CEILING, concurrency ceil(20*0.25*3)=15
	// floored at the 20-VU minimum, ramp-up 0, steps defaulted to 5.
	for field, want := range map[string]float64{"engines": 2, "concurrency": 20, "rampup": 0, "throughput": 20, "duration": 300, "steps": 5} {
		if got[field] != want {
			t.Errorf("echoed %s = %v, want %v", field, got[field], want)
		}
	}
	if got["mode"] != "staircase" {
		t.Errorf("echoed mode = %v, want staircase", got["mode"])
	}

	// A stated steps count rides along verbatim.
	body = fmt.Sprintf(`{"name":"modeexec","project_id":1,"execution_id":%s,"tests":[{"name":"t","scenario_id":%s,"mode":"staircase","throughput":20,"duration":300,"steps":3}]}`, execID, scenID)
	if rec := putConfigJSON(t, h, path, body); rec.Code != http.StatusOK {
		t.Fatalf("staircase steps put = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := configTests(t, getConfig(t, h, path))[0]["steps"]; got != float64(3) {
		t.Errorf("echoed steps = %v, want 3", got)
	}
}

// The staircase's own input refusals are 400s, the same class as every
// other entry rule -- not 409s, which name an unresolvable capacity
// profile.
func TestPutExecutionConfig_StaircaseInputRefusals(t *testing.T) {
	t.Parallel()
	profile := &capacityprofile.CapacityProfile{
		PerPodQPS: 100, SaturatedBy: calibration.SaturatedByEngine, ScenarioFingerprint: "fp",
	}
	h, _, execID, scenID := newModeRouter(t, profile)
	path := "/api/executions/" + execID + "/config"

	cases := []struct {
		name string
		body string
	}{
		{
			"steps below the minimum",
			fmt.Sprintf(`{"name":"modeexec","project_id":1,"execution_id":%s,"tests":[{"name":"t","scenario_id":%s,"mode":"staircase","throughput":20,"duration":300,"steps":1}]}`, execID, scenID),
		},
		{
			"steps above the maximum",
			fmt.Sprintf(`{"name":"modeexec","project_id":1,"execution_id":%s,"tests":[{"name":"t","scenario_id":%s,"mode":"staircase","throughput":20,"duration":300,"steps":11}]}`, execID, scenID),
		},
		{
			"per-step hold under the 60s floor",
			fmt.Sprintf(`{"name":"modeexec","project_id":1,"execution_id":%s,"tests":[{"name":"t","scenario_id":%s,"mode":"staircase","throughput":20,"duration":30}]}`, execID, scenID),
		},
		{
			"steps on a non-staircase entry",
			fmt.Sprintf(`{"name":"modeexec","project_id":1,"execution_id":%s,"tests":[{"name":"t","scenario_id":%s,"mode":"burst","throughput":20,"duration":300,"steps":5}]}`, execID, scenID),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Sequential on purpose: one execution behind one router, and
			// the shared stub's fan-out log is not safe for parallel
			// writers -- the refusal matrix gets its parallelism from
			// per-case routers instead.
			rec := putConfigJSON(t, h, path, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("put = %d, want 400 (%s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "steps") && !strings.Contains(rec.Body.String(), "duration") {
				t.Errorf("400 body does not name the offending field: %s", rec.Body.String())
			}
		})
	}
}
