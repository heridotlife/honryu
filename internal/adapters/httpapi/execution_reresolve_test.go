// Phase 91's re-resolve wire contract: POST /executions/{id}/config/re-resolve
// re-runs the stored config's mode entries through the current calibration,
// answers 200 with the per-entry before/after diff, refuses with the PUT's
// own 409 envelope (nothing persisted) when the profile went missing or
// stale, and gates on execution:update exactly like the config PUT.
package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/heridotlife/honryu/internal/domain/account"
	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/capacityprofile"
	"github.com/heridotlife/honryu/internal/domain/rbac"
)

// reResolve POSTs the action (no body -- the stored config is the input).
func reResolve(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, http.MethodPost, path)
}

// storeModeConfig PUTs a resolved burst 500/600 config (4 engines against a
// 125 rps/pod profile, 375 VUs from the 250ms fallback hint), the happy-path
// fixture every re-resolve test starts from.
func storeModeConfig(t *testing.T, h http.Handler, execID, scenID string) {
	t.Helper()
	body := fmt.Sprintf(`{"name":"modeexec","project_id":1,"execution_id":%s,"tests":[{"name":"t","scenario_id":%s,"mode":"burst","throughput":500,"duration":600}]}`, execID, scenID)
	if rec := putConfigJSON(t, h, "/api/executions/"+execID+"/config", body); rec.Code != http.StatusOK {
		t.Fatalf("mode config put = %d (%s)", rec.Code, rec.Body.String())
	}
}

// The happy path over the wire: after a recalibration (per-pod rate drops to
// 100), the POST refreshes the stored entry and answers with the per-entry
// old/new numbers; GET echoes the refreshed config.
func TestReResolveExecutionConfig_Refresh(t *testing.T) {
	t.Parallel()
	h, cap, execID, scenID := newModeRouter(t, &capacityprofile.CapacityProfile{
		PerPodQPS: 125, SaturatedBy: calibration.SaturatedByEngine, ScenarioFingerprint: "fp",
	})
	storeModeConfig(t, h, execID, scenID)

	// Recalibration: the same key now sustains only 100 rps/pod.
	cap.profile = &capacityprofile.CapacityProfile{
		PerPodQPS: 100, SaturatedBy: calibration.SaturatedByEngine, ScenarioFingerprint: "fp",
	}
	rec := reResolve(t, h, "/api/executions/"+execID+"/config/re-resolve")
	if rec.Code != http.StatusOK {
		t.Fatalf("re-resolve = %d (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Message string `json:"message"`
		Entries []struct {
			ScenarioID int64  `json:"scenario_id"`
			Mode       string `json:"mode"`
			Changed    bool   `json:"changed"`
			Before     struct {
				Engines     int `json:"engines"`
				Concurrency int `json:"concurrency"`
				Rampup      int `json:"rampup"`
				Throughput  int `json:"throughput"`
			} `json:"before"`
			After struct {
				Engines     int `json:"engines"`
				Concurrency int `json:"concurrency"`
				Rampup      int `json:"rampup"`
				Throughput  int `json:"throughput"`
			} `json:"after"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode re-resolve body: %v (%s)", err, rec.Body.String())
	}
	if len(body.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(body.Entries))
	}
	e := body.Entries[0]
	if e.Mode != "burst" || !e.Changed {
		t.Fatalf("entry = %q changed=%v, want the burst entry changed", e.Mode, e.Changed)
	}
	if e.Before.Engines != 4 || e.After.Engines != 5 {
		t.Errorf("engines diff = %d -> %d, want 4 -> 5", e.Before.Engines, e.After.Engines)
	}
	if e.After.Throughput != 500 || e.After.Concurrency != 375 {
		t.Errorf("after = %+v, want the statement's rate kept and 375 VUs (hint unchanged)", e.After)
	}

	// GET echoes the refreshed numbers: the stored config is the new one.
	tests := configTests(t, getConfig(t, h, "/api/executions/"+execID+"/config"))
	if len(tests) != 1 || tests[0]["engines"] != float64(5) || tests[0]["mode"] != "burst" {
		t.Fatalf("stored entry after re-resolve = %v, want 5 engines with burst provenance", tests)
	}
}

// The refusal: a profile that went missing since the store refuses with the
// PUT's own 409 envelope, and the stored config keeps its old snapshot.
func TestReResolveExecutionConfig_Refusal(t *testing.T) {
	t.Parallel()
	h, cap, execID, scenID := newModeRouter(t, &capacityprofile.CapacityProfile{
		PerPodQPS: 125, SaturatedBy: calibration.SaturatedByEngine, ScenarioFingerprint: "fp",
	})
	storeModeConfig(t, h, execID, scenID)

	cap.profile = nil // the calibration was pruned
	rec := reResolve(t, h, "/api/executions/"+execID+"/config/re-resolve")
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-resolve = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	var env struct {
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode 409 body: %v (%s)", err, rec.Body.String())
	}
	if env.Details["fanout_status"] != "no_profile" {
		t.Errorf("details fanout_status = %v, want no_profile", env.Details["fanout_status"])
	}
	if hint, _ := env.Details["hint"].(string); !strings.Contains(hint, "calibrate") {
		t.Errorf("details hint = %v, want the calibrate remediation", env.Details["hint"])
	}

	// Nothing persisted: the old snapshot is still the config.
	tests := configTests(t, getConfig(t, h, "/api/executions/"+execID+"/config"))
	if len(tests) != 1 || tests[0]["engines"] != float64(4) {
		t.Fatalf("stored entry after refusal = %v, want the untouched 4-engine snapshot", tests)
	}
}

// RBAC mirrors the config PUT: a viewer's POST is a 403 that fires before
// the service is consulted -- no resolution, no re-store.
func TestReResolveExecutionConfig_RBACUpdateRequired(t *testing.T) {
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
	if scenarioID == 0 || executionID == 0 {
		t.Fatal("seed scenario/execution failed")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/executions/"+strconv.FormatInt(executionID, 10)+"/config/re-resolve", nil)
	req.Header.Set("Authorization", "Bearer carol-tok")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer re-resolve = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}

	// An editor (the PUT's own role) passes the gate: the action runs and
	// reports the stored config (here: nothing stored yet -> the ordinary
	// no-scenarios client error, proving the gate opened, not a 403).
	f.prov.Register("dave-tok", account.Account{Subject: "dave"})
	assignRole(t, f, acme, "dave", rbac.RoleTenantEditor)
	req = httptest.NewRequest(http.MethodPost, "/api/executions/"+strconv.FormatInt(executionID, 10)+"/config/re-resolve", nil)
	req.Header.Set("Authorization", "Bearer dave-tok")
	rec = httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("editor re-resolve = %d, want the action's own verdict (400, nothing stored) not a gate failure (%s)",
			rec.Code, rec.Body.String())
	}
}
