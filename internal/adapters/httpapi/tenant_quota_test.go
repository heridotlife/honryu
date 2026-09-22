package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/quotaapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/app/tenantapp"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// putForm sends a form-encoded body with an arbitrary method -- postForm
// (router_phase1_test.go) is POST-only, and PUT is what a quota update uses.
func putForm(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newTenantRouter(t *testing.T) http.Handler {
	t.Helper()
	store := fake.NewStore()
	return httpapi.NewRouter(httpapi.Deps{
		Tenants: tenantapp.NewService(store, store, store),
	})
}

// The read answers the platform default before any row exists (defaulted
// true), and setting one round-trips the configured value.
func TestTenantQuota_GetDefaultsAndSetRoundTrips(t *testing.T) {
	t.Parallel()
	h := newTenantRouter(t)

	tenantID := decodeID(t, postForm(t, h, "/api/tenants", url.Values{"name": {"acme"}, "display_name": {"Acme"}}))

	rec := do(t, h, http.MethodGet, "/api/tenants/"+itoa(tenantID)+"/quota")
	if rec.Code != http.StatusOK {
		t.Fatalf("get quota = %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ceiling":10`) || !strings.Contains(rec.Body.String(), `"defaulted":true`) {
		t.Fatalf("get quota before configured = %s, want the default ceiling 10 flagged defaulted", rec.Body.String())
	}

	rec = putForm(t, h, "/api/tenants/"+itoa(tenantID)+"/quota", url.Values{"ceiling": {"5"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("set quota = %d (%s)", rec.Code, rec.Body.String())
	}

	rec = do(t, h, http.MethodGet, "/api/tenants/"+itoa(tenantID)+"/quota")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ceiling":5`) || !strings.Contains(rec.Body.String(), `"defaulted":false`) {
		t.Fatalf("get quota after set = %d %s, want ceiling 5 not defaulted", rec.Code, rec.Body.String())
	}
}

func TestTenantQuota_RejectsNegativeCeiling(t *testing.T) {
	t.Parallel()
	h := newTenantRouter(t)
	tenantID := decodeID(t, postForm(t, h, "/api/tenants", url.Values{"name": {"acme"}, "display_name": {"Acme"}}))

	rec := putForm(t, h, "/api/tenants/"+itoa(tenantID)+"/quota", url.Values{"ceiling": {"-1"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("set negative quota = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

// Phase 104: the quota read reports the EFFECTIVE ceiling -- a tenant with
// no quota row answers the platform default (10) with defaulted:true, so a
// UI can badge it; once a row exists (any value, including 0) the answer is
// the configured ceiling with defaulted:false.
func TestTenantQuota_GetReportsEffectiveCeilingAndDefaulted(t *testing.T) {
	t.Parallel()
	h := newTenantRouter(t)
	tenantID := decodeID(t, postForm(t, h, "/api/tenants", url.Values{"name": {"acme"}, "display_name": {"Acme"}}))

	get := func() struct {
		Cluster   string `json:"cluster"`
		Ceiling   int    `json:"ceiling"`
		Defaulted bool   `json:"defaulted"`
	} {
		t.Helper()
		rec := do(t, h, http.MethodGet, "/api/tenants/"+itoa(tenantID)+"/quota")
		if rec.Code != http.StatusOK {
			t.Fatalf("get quota = %d (%s)", rec.Code, rec.Body.String())
		}
		var body struct {
			Cluster   string `json:"cluster"`
			Ceiling   int    `json:"ceiling"`
			Defaulted bool   `json:"defaulted"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("get quota body = %s: %v", rec.Body.String(), err)
		}
		return body
	}

	if got := get(); got.Ceiling != 10 || !got.Defaulted {
		t.Fatalf("quota before any row = %+v, want the default ceiling 10 with defaulted:true", got)
	}

	if rec := putForm(t, h, "/api/tenants/"+itoa(tenantID)+"/quota", url.Values{"ceiling": {"4"}}); rec.Code != http.StatusOK {
		t.Fatalf("set quota = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := get(); got.Ceiling != 4 || got.Defaulted {
		t.Fatalf("quota after explicit set = %+v, want ceiling 4 with defaulted:false", got)
	}

	// An explicit zero overrides the default too -- a deliberate block, not
	// an unconfigured tenant.
	if rec := putForm(t, h, "/api/tenants/"+itoa(tenantID)+"/quota", url.Values{"ceiling": {"0"}}); rec.Code != http.StatusOK {
		t.Fatalf("set quota to zero = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := get(); got.Ceiling != 0 || got.Defaulted {
		t.Fatalf("quota after explicit zero = %+v, want ceiling 0 with defaulted:false", got)
	}
}

// quotaEnv wires a deploy→trigger router the way cmd/api does -- the real
// quota service attached to lifecycle -- over one shared fake store, and
// seeds a tenant with a ceiling of 1 engine-equivalent, a tenant-scoped
// project, and an execution over a 2-engine profile that inherits the
// tenant (executionapp.Create copies the project's tenant, Phase 20). That
// inheritance is what makes Trigger call quota.Reserve at all, so it is the
// only wiring through which an over-quota reservation can surface here.
type quotaEnv struct {
	h           http.Handler
	store       *fake.Store
	executionID int64
	tenantID    int64
}

func newQuotaEnv(t *testing.T) quotaEnv {
	t.Helper()
	ctx := context.Background()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	sched := fake.NewScheduler()
	quota := quotaapp.NewService(store)

	h := httpapi.NewRouter(httpapi.Deps{
		Projects:            projectapp.NewService(store),
		Scenarios:           scenarioapp.NewService(store, obj),
		Executions:          executionapp.NewService(store, obj, 100),
		Tenants:             tenantapp.NewService(store, store, store),
		Lifecycle:           lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage("img")).WithQuota(quota),
		Store:               obj,
		DefaultOwners:       []string{"honryu"},
		TriggerReadyPoll:    time.Millisecond,
		TriggerReadyTimeout: 10 * time.Millisecond,
	})

	// A tenant with a ceiling of 1 engine for the default cluster ("").
	tenantID := decodeID(t, postForm(t, h, "/api/tenants", url.Values{"name": {"acme"}, "display_name": {"Acme"}}))
	if rec := putForm(t, h, "/api/tenants/"+itoa(tenantID)+"/quota", url.Values{"ceiling": {"1"}}); rec.Code != http.StatusOK {
		t.Fatalf("set ceiling = %d (%s)", rec.Code, rec.Body.String())
	}

	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}, "tenant_id": {itoa(tenantID)}}))
	executionID := decodeID(t, postForm(t, h, "/api/executions", url.Values{"name": {"peak"}, "project_id": {itoa(projectID)}}))

	// A native scenario with a JMX test file, and an execution config whose
	// single entry wants 2 engines -- one more than the ceiling admits.
	sc, err := scenario.NewNative("smoke", projectID, taurus.ExecutorJMeter)
	if err != nil {
		t.Fatalf("new scenario: %v", err)
	}
	scenarioID, err := store.CreateScenario(ctx, sc)
	if err != nil {
		t.Fatalf("create scenario: %v", err)
	}
	if err := store.AddScenarioFile(ctx, scenarioID, "test.jmx", true); err != nil {
		t.Fatalf("add test file: %v", err)
	}
	if err := obj.Upload(ctx, fmt.Sprintf("scenario/%d/test.jmx", scenarioID), strings.NewReader("<jmx/>")); err != nil {
		t.Fatalf("upload test file: %v", err)
	}
	if err := store.StoreLoadProfile(ctx, executionID, false, []loadprofile.Entry{
		{Name: "p", ScenarioID: scenarioID, Concurrency: 5, Rampup: 1, Engines: 2, Duration: 10},
	}); err != nil {
		t.Fatalf("store load profile: %v", err)
	}
	return quotaEnv{h: h, store: store, executionID: executionID, tenantID: tenantID}
}

// An over-quota Trigger is a well-formed request the tenant's ceiling
// refuses, not a server fault: it must surface as 429 naming the quota
// condition, never as an unmapped 500.
func TestTenantQuota_TriggerOverQuotaIs429Not500(t *testing.T) {
	t.Parallel()
	e := newQuotaEnv(t)
	base := "/api/executions/" + itoa(e.executionID)

	// Deploy itself reserves nothing (the reservation is minted at Trigger),
	// so it succeeds under the ceiling.
	if rec := do(t, e.h, http.MethodPost, base+"/deploy"); rec.Code != http.StatusOK {
		t.Fatalf("deploy = %d (%s)", rec.Code, rec.Body.String())
	}

	rec := do(t, e.h, http.MethodPost, base+"/trigger")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over-quota trigger = %d (%s), want 429", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "reservation would exceed tenant quota") {
		t.Fatalf("429 body = %s, want the quota condition named", rec.Body.String())
	}
}

// The 429 carries phase 24's details envelope: the fixed message stays the
// stable contract, and the admission numbers plus the PUT remediation ride
// along in "details" for the operator UI to surface.
func TestTenantQuota_TriggerOverQuotaCarriesDetails(t *testing.T) {
	t.Parallel()
	e := newQuotaEnv(t)
	base := "/api/executions/" + itoa(e.executionID)

	if rec := do(t, e.h, http.MethodPost, base+"/deploy"); rec.Code != http.StatusOK {
		t.Fatalf("deploy = %d (%s)", rec.Code, rec.Body.String())
	}

	rec := do(t, e.h, http.MethodPost, base+"/trigger")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over-quota trigger = %d (%s), want 429", rec.Code, rec.Body.String())
	}
	var body struct {
		Message string `json:"message"`
		Details struct {
			TenantID  int64  `json:"tenant_id"`
			Cluster   string `json:"cluster"`
			Requested int    `json:"requested"`
			Used      int    `json:"used"`
			Ceiling   int    `json:"ceiling"`
			Hint      string `json:"hint"`
		} `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("429 body is not the envelope: %v (%s)", err, rec.Body.String())
	}
	if body.Message != "reservation would exceed tenant quota" {
		t.Fatalf("message = %q, want the fixed contract", body.Message)
	}
	if body.Details.TenantID != e.tenantID || body.Details.Ceiling != 1 || body.Details.Used != 0 {
		t.Fatalf("details = %+v, want the ledger's tenant/ceiling/used", body.Details)
	}
	if body.Details.Requested <= body.Details.Ceiling {
		t.Fatalf("requested = %d, want it over the ceiling (that is why this is a 429)", body.Details.Requested)
	}
	if !strings.Contains(body.Details.Hint, "PUT /api/tenants/{tenant_id}/quota") || !strings.Contains(body.Details.Hint, "ceiling=") {
		t.Fatalf("hint = %q, want the PUT remediation naming the ceiling", body.Details.Hint)
	}
}
