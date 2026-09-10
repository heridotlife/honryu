package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	auditmem "github.com/heridotlife/honryu/internal/adapters/audit/memory"
	"github.com/heridotlife/honryu/internal/adapters/auth/session"
	"github.com/heridotlife/honryu/internal/adapters/auth/token"
	membus "github.com/heridotlife/honryu/internal/adapters/eventbus/memory"
	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/adminapp"
	"github.com/heridotlife/honryu/internal/app/authapp"
	"github.com/heridotlife/honryu/internal/app/calibrationapp"
	"github.com/heridotlife/honryu/internal/app/campaignapp"
	"github.com/heridotlife/honryu/internal/app/digestapp"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/quotaapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/app/scheduleapp"
	"github.com/heridotlife/honryu/internal/app/tenantapp"
	"github.com/heridotlife/honryu/internal/app/usageapp"
	"github.com/heridotlife/honryu/internal/app/webhookapp"
	"github.com/heridotlife/honryu/internal/domain/account"
	"github.com/heridotlife/honryu/internal/domain/clusterregistry"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/rbac"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// rbacFixture wires a full RBAC-enabled router over one shared fake store.
type rbacFixture struct {
	router   http.Handler
	store    *fake.Store
	sched    *fake.Scheduler
	prov     *token.Provider
	sessions *session.Provider
	audit    *auditmem.Log
	reports  *fake.ReportStore
	shares   *fake.ShareStore
	bus      *membus.Bus
	obj      *fake.ObjectStore
}

func newRBACFixture(t *testing.T) *rbacFixture {
	t.Helper()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	sched := fake.NewScheduler()
	prov := token.New()
	// Seed the admin principal and its global grant.
	prov.Register("admin-tok", account.Account{Subject: "admin", Email: "admin@x"})
	if err := store.AssignRole(context.Background(), ports.RoleGrant{
		Subject: "admin", RoleName: rbac.RoleServiceProviderAdmin,
	}); err != nil {
		t.Fatalf("seed admin grant: %v", err)
	}
	auth := authapp.NewService(prov, store, true)
	// The demo-session surface, as a deployment with demo mode on would wire
	// it. Deps.Auth still uses the token provider: the session endpoints are
	// public, so both coexist in the audit fixture.
	sessions, err := session.New([]byte("rbac-fixture-session-key"), []session.Profile{
		{ID: "auditor", Name: "Auditor", Subject: "auditor"},
	})
	if err != nil {
		t.Fatalf("session provider: %v", err)
	}
	audit := auditmem.New(nil)
	reports := fake.NewReportStore()
	shares := fake.NewShareStore()
	// The series endpoint's interval store: wired the way cmd/api wires it,
	// so the audit probe reaches the report:read gate rather than a 404.
	series := fake.NewReportProgress()
	bus := membus.New()
	lifecycle := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage("honryu/jmeter:latest"))
	quota := quotaapp.NewService(store)
	campaigns := campaignapp.NewService(store, sched)
	scenarios := scenarioapp.NewService(store, obj)
	calibrations := calibrationapp.NewService(store).WithFingerprint(scenarios)
	router := httpapi.NewRouter(httpapi.Deps{
		Projects:     projectapp.NewService(store),
		Scenarios:    scenarios,
		Executions:   executionapp.NewService(store, obj, 100),
		Lifecycle:    lifecycle,
		Tenants:      tenantapp.NewService(store, store, store),
		Admin:        adminapp.NewService(store, sched, lifecycle).WithCampaigns(campaigns),
		Schedules:    scheduleapp.NewService(store, quota),
		Campaigns:    campaigns,
		Calibrations: calibrations,
		Webhooks:     webhookapp.NewService(store),
		Digests:      digestapp.NewService(store),
		Store:        obj,
		Reports:      reports,
		Shares:       shares,
		Series:       series,
		Usage:        usageapp.NewService(store),
		Events:       bus,
		Reservations: store,
		Auth:         auth,
		Audit:        audit,
		// A no-op stub so the platform-admin gate is reached (a nil dep would
		// 404 before the RBAC check).
		Clusters: &stubClusterService{list: func() ([]clusterregistry.Cluster, error) { return nil, nil }},
		Sessions: sessions,
	})
	return &rbacFixture{router: router, store: store, sched: sched, prov: prov, sessions: sessions, audit: audit, reports: reports, shares: shares, bus: bus, obj: obj}
}

func (f *rbacFixture) req(t *testing.T, method, path, tok string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, r)
	return rec
}

func TestRBAC_UnauthenticatedRejected(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)
	rec := f.req(t, http.MethodGet, "/api/projects", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (%s)", rec.Code, rec.Body.String())
	}
	// Health remains public.
	if rec := f.req(t, http.MethodGet, "/healthz", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", rec.Code)
	}
}

func TestRBAC_TenantScopedProjectFiltering(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	// Admin creates two tenants.
	acme := createTenant(t, f, "acme", "Acme")
	globex := createTenant(t, f, "globex", "Globex")

	// Admin grants alice editor in acme, bob editor in globex.
	f.prov.Register("alice-tok", account.Account{Subject: "alice"})
	f.prov.Register("bob-tok", account.Account{Subject: "bob"})
	assignRole(t, f, acme, "alice", rbac.RoleTenantEditor)
	assignRole(t, f, globex, "bob", rbac.RoleTenantEditor)

	// Admin creates a project in each tenant.
	createProjectInTenant(t, f, "acme-web", "team-a", acme)
	createProjectInTenant(t, f, "globex-web", "team-b", globex)

	// Administrative actions are audited with the acting subject.
	if events := f.audit.Events(); len(events) == 0 {
		t.Fatal("no audit events recorded")
	} else {
		for _, e := range events {
			if e.Actor != "admin" {
				t.Fatalf("audit actor = %q, want admin (event %+v)", e.Actor, e)
			}
		}
	}

	// Alice sees only acme's project.
	if names := listProjectNames(t, f, "alice-tok"); !equalSet(names, []string{"acme-web"}) {
		t.Fatalf("alice sees %v, want [acme-web]", names)
	}
	// Bob sees only globex's project.
	if names := listProjectNames(t, f, "bob-tok"); !equalSet(names, []string{"globex-web"}) {
		t.Fatalf("bob sees %v, want [globex-web]", names)
	}
	// Admin (service-provider) sees both.
	if names := listProjectNames(t, f, "admin-tok"); !equalSet(names, []string{"acme-web", "globex-web"}) {
		t.Fatalf("admin sees %v, want both", names)
	}
}

func TestRBAC_ViewerDeniedWrite(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)
	acme := createTenant(t, f, "acme", "Acme")

	f.prov.Register("val-tok", account.Account{Subject: "val"})
	assignRole(t, f, acme, "val", rbac.RoleTenantViewer)

	// A viewer may not create a project in the tenant.
	form := url.Values{"name": {"nope"}, "owner": {"team-a"}, "tenant_id": {strconv.FormatInt(acme, 10)}}
	rec := f.req(t, http.MethodPost, "/api/projects", "val-tok", form)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer create status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

func TestRBAC_NonAdminCannotManageTenants(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)
	f.prov.Register("nobody-tok", account.Account{Subject: "nobody"})

	rec := f.req(t, http.MethodPost, "/api/tenants", "nobody-tok",
		url.Values{"name": {"x"}, "display_name": {"X"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin create tenant status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

func TestRBAC_RoleRevocation(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)
	acme := createTenant(t, f, "acme", "Acme")
	f.prov.Register("carol-tok", account.Account{Subject: "carol"})
	assignRole(t, f, acme, "carol", rbac.RoleTenantEditor)
	createProjectInTenant(t, f, "acme-web", "team-a", acme)

	if names := listProjectNames(t, f, "carol-tok"); len(names) != 1 {
		t.Fatalf("before revoke carol sees %v, want 1", names)
	}

	// Revoke carol's grant.
	path := "/api/tenants/" + strconv.FormatInt(acme, 10) + "/roles?subject=carol&role=" + rbac.RoleTenantEditor
	if rec := f.req(t, http.MethodDelete, path, "admin-tok", nil); rec.Code != http.StatusOK {
		t.Fatalf("revoke status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if names := listProjectNames(t, f, "carol-tok"); len(names) != 0 {
		t.Fatalf("after revoke carol sees %v, want none", names)
	}
}

func TestRBAC_TenantCRUDEndpoints(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)
	id := createTenant(t, f, "acme", "Acme")

	// List.
	rec := f.req(t, http.MethodGet, "/api/tenants", "admin-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list tenants status = %d (%s)", rec.Code, rec.Body.String())
	}
	var list []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("list = %v, err = %v", list, err)
	}

	// Get one.
	rec = f.req(t, http.MethodGet, "/api/tenants/"+strconv.FormatInt(id, 10), "admin-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get tenant status = %d (%s)", rec.Code, rec.Body.String())
	}

	// Suspend via PATCH, then confirm.
	rec = f.req(t, http.MethodPatch, "/api/tenants/"+strconv.FormatInt(id, 10), "admin-tok",
		url.Values{"status": {"SUSPENDED"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d (%s)", rec.Code, rec.Body.String())
	}
	rec = f.req(t, http.MethodGet, "/api/tenants/"+strconv.FormatInt(id, 10), "admin-tok", nil)
	var got struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Status != "SUSPENDED" {
		t.Fatalf("status = %q, want SUSPENDED", got.Status)
	}

	// Invalid status is a 400.
	rec = f.req(t, http.MethodPatch, "/api/tenants/"+strconv.FormatInt(id, 10), "admin-tok",
		url.Values{"status": {"BOGUS"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status = %d, want 400", rec.Code)
	}

	// Missing tenant is a 404; malformed id is a 400.
	if rec := f.req(t, http.MethodGet, "/api/tenants/999", "admin-tok", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get missing = %d, want 404", rec.Code)
	}
	if rec := f.req(t, http.MethodGet, "/api/tenants/abc", "admin-tok", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("get malformed id = %d, want 400", rec.Code)
	}
}

func TestRBAC_GlobalRoleEndpoints(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)
	f.prov.Register("newadmin-tok", account.Account{Subject: "newadmin"})

	// Grant a global service-provider admin role.
	rec := f.req(t, http.MethodPost, "/api/roles", "admin-tok",
		url.Values{"subject": {"newadmin"}, "role": {rbac.RoleServiceProviderAdmin}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("assign global status = %d (%s)", rec.Code, rec.Body.String())
	}
	// newadmin can now administer tenants.
	if rec := f.req(t, http.MethodGet, "/api/tenants", "newadmin-tok", nil); rec.Code != http.StatusOK {
		t.Fatalf("newadmin list tenants = %d, want 200", rec.Code)
	}

	// Revoke it; newadmin loses admin access.
	rec = f.req(t, http.MethodDelete, "/api/roles?subject=newadmin&role="+rbac.RoleServiceProviderAdmin, "admin-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke global status = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := f.req(t, http.MethodGet, "/api/tenants", "newadmin-tok", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("revoked newadmin list = %d, want 403", rec.Code)
	}
}

func TestRBAC_RoleGrantBadRequests(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)
	acme := createTenant(t, f, "acme", "Acme")

	// Missing subject on assign.
	rec := f.req(t, http.MethodPost, "/api/tenants/"+strconv.FormatInt(acme, 10)+"/roles", "admin-tok",
		url.Values{"role": {rbac.RoleTenantEditor}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("assign missing subject = %d, want 400", rec.Code)
	}
	// Unknown role → 400.
	rec = f.req(t, http.MethodPost, "/api/tenants/"+strconv.FormatInt(acme, 10)+"/roles", "admin-tok",
		url.Values{"subject": {"x"}, "role": {"wizard"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("assign unknown role = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	// Missing subject/role on revoke.
	rec = f.req(t, http.MethodDelete, "/api/tenants/"+strconv.FormatInt(acme, 10)+"/roles?subject=x", "admin-tok", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("revoke missing role = %d, want 400", rec.Code)
	}
	// Malformed tenant id in path.
	rec = f.req(t, http.MethodPost, "/api/tenants/abc/roles", "admin-tok",
		url.Values{"subject": {"x"}, "role": {rbac.RoleTenantEditor}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("assign malformed tenant id = %d, want 400", rec.Code)
	}
}

func TestRBAC_TenantsNotConfigured(t *testing.T) {
	t.Parallel()
	// A legacy (RBAC-disabled) router without a Tenants service returns 404 for
	// the tenant endpoints rather than pretending they exist.
	router, _ := newTestRouter(t)
	rec := do(t, router, http.MethodGet, "/api/tenants")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("tenants without service = %d, want 404", rec.Code)
	}
}

// --- helpers ---------------------------------------------------------------

func createTenant(t *testing.T, f *rbacFixture, name, display string) int64 {
	t.Helper()
	rec := f.req(t, http.MethodPost, "/api/tenants", "admin-tok",
		url.Values{"name": {name}, "display_name": {display}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create tenant %q status = %d (%s)", name, rec.Code, rec.Body.String())
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode tenant: %v", err)
	}
	return out.ID
}

func assignRole(t *testing.T, f *rbacFixture, tenantID int64, subject, role string) {
	t.Helper()
	path := "/api/tenants/" + strconv.FormatInt(tenantID, 10) + "/roles"
	rec := f.req(t, http.MethodPost, path, "admin-tok",
		url.Values{"subject": {subject}, "role": {role}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("assign %s to %s status = %d (%s)", role, subject, rec.Code, rec.Body.String())
	}
}

func createProjectInTenant(t *testing.T, f *rbacFixture, name, owner string, tenantID int64) {
	t.Helper()
	form := url.Values{"name": {name}, "owner": {owner}, "tenant_id": {strconv.FormatInt(tenantID, 10)}}
	rec := f.req(t, http.MethodPost, "/api/projects", "admin-tok", form)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project %q status = %d (%s)", name, rec.Code, rec.Body.String())
	}
}

func listProjectNames(t *testing.T, f *rbacFixture, tok string) []string {
	t.Helper()
	rec := f.req(t, http.MethodGet, "/api/projects", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list projects status = %d (%s)", rec.Code, rec.Body.String())
	}
	var out []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	names := make([]string, 0, len(out))
	for _, p := range out {
		names = append(names, p.Name)
	}
	return names
}

func equalSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, v := range seen {
		if v != 0 {
			return false
		}
	}
	return true
}

// createProjectInTenantReturningID is createProjectInTenant plus the
// project's assigned id, needed to create an execution under it.
func createProjectInTenantReturningID(t *testing.T, f *rbacFixture, name, owner string, tenantID int64) int64 {
	t.Helper()
	form := url.Values{"name": {name}, "owner": {owner}, "tenant_id": {strconv.FormatInt(tenantID, 10)}}
	rec := f.req(t, http.MethodPost, "/api/projects", "admin-tok", form)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project %q status = %d (%s)", name, rec.Code, rec.Body.String())
	}
	return decodeID(t, rec)
}

// putMultipartAuth is putMultipart (router_phase1_test.go) plus a bearer
// token, needed once RBAC requires every /api/ request to authenticate.
func putMultipartAuth(t *testing.T, f *rbacFixture, path, tok, filename, content string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_ = mw.Close()

	r := httptest.NewRequest(http.MethodPut, path, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, r)
	return rec
}

// Bug 1 of the spec's "three live bugs": tenant_viewer holds
// project/execution/scenario:read+list and no update -- before Phase 20 every
// gated execution route demanded project:update, so a viewer was locked out
// of executions entirely rather than read-only on them (the ungated reads
// were the only thing keeping the role usable at all). With
// authorizeExecution parameterized by action, GET /api/executions/{id} asks
// ResourceExecution/read and admits the viewer, while DELETE still demands
// an action only editors hold. The editor's 200 proves the 403 is the role,
// not a broken route.
func TestRBAC_ViewerCanReadButNotDeleteExecution(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	acme := createTenant(t, f, "acme", "Acme")
	f.prov.Register("carol-tok", account.Account{Subject: "carol"})
	assignRole(t, f, acme, "carol", rbac.RoleTenantViewer)
	f.prov.Register("bob-tok", account.Account{Subject: "bob"})
	assignRole(t, f, acme, "bob", rbac.RoleTenantEditor)

	projectID := createProjectInTenantReturningID(t, f, "acme-web", "team-a", acme)
	executionID := decodeID(t, f.req(t, http.MethodPost, "/api/executions", "admin-tok",
		url.Values{"name": {"peak"}, "project_id": {strconv.FormatInt(projectID, 10)}}))
	path := "/api/executions/" + strconv.FormatInt(executionID, 10)

	// The viewer reads the execution (200), not a 403 dressed as security.
	if rec := f.req(t, http.MethodGet, path, "carol-tok", nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer GET execution = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	// But cannot delete it.
	if rec := f.req(t, http.MethodDelete, path, "carol-tok", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer DELETE execution = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	// An editor still can -- the route works; only the role differs.
	if rec := f.req(t, http.MethodDelete, path, "bob-tok", nil); rec.Code != http.StatusOK {
		t.Fatalf("editor DELETE execution = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

// createSchedule's tenant_id is a client-declared value, not derived from
// anything already authorized by authorizeExecution -- authorizeScheduleTenant
// must independently verify the caller is authorized for the specific
// tenant named, the same rule createProject already applies to its own
// client-declared tenant_id. Without it, alice (authorized only in acme, her
// own tenant) could attribute a schedule's quota to globex, a tenant she has
// no relationship to at all.
func TestRBAC_CreateScheduleRequiresAuthorizationForTheDeclaredTenant(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	acme := createTenant(t, f, "acme", "Acme")
	globex := createTenant(t, f, "globex", "Globex")
	f.prov.Register("alice-tok", account.Account{Subject: "alice"})
	assignRole(t, f, acme, "alice", rbac.RoleTenantEditor)

	// Admin sets up alice's project, scenario, execution, and load profile in
	// acme -- only the createSchedule call itself is under test.
	projectID := createProjectInTenantReturningID(t, f, "acme-web", "team-a", acme)
	scenarioID := decodeID(t, f.req(t, http.MethodPost, "/api/scenarios", "admin-tok",
		url.Values{"name": {"smoke"}, "project_id": {strconv.FormatInt(projectID, 10)}}))
	executionID := decodeID(t, f.req(t, http.MethodPost, "/api/executions", "admin-tok",
		url.Values{"name": {"peak"}, "project_id": {strconv.FormatInt(projectID, 10)}}))
	configYAML := fmt.Sprintf(`multi-test:
  collectionid: %d
  tests:
    - testid: %d
      concurrency: 10
      rampup: 1
      engines: 2
      duration: 30
`, executionID, scenarioID)
	path := "/api/executions/" + strconv.FormatInt(executionID, 10) + "/config"
	if rec := putMultipartAuth(t, f, path, "admin-tok", "config.yaml", configYAML); rec.Code != http.StatusOK {
		t.Fatalf("upload config = %d (%s)", rec.Code, rec.Body.String())
	}

	fireAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	schedulePath := "/api/executions/" + strconv.FormatInt(executionID, 10) + "/schedules"

	// Alice, authorized only in acme, cannot attribute a schedule to globex.
	rec := f.req(t, http.MethodPost, schedulePath, "alice-tok", url.Values{
		"tenant_id": {strconv.FormatInt(globex, 10)}, "kind": {"one_shot"}, "fire_at": {fireAt},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("alice create schedule for globex = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}

	// Alice can attribute one to acme, her own tenant.
	rec = f.req(t, http.MethodPost, schedulePath, "alice-tok", url.Values{
		"tenant_id": {strconv.FormatInt(acme, 10)}, "kind": {"one_shot"}, "fire_at": {fireAt},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("alice create schedule for acme = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
}

// createCampaign's tenant_id is a URL path parameter, not a client-declared
// form field, but authorization must still be checked against it
// specifically -- and against RoleCampaignManager, not merely
// RoleTenantEditor (which authorizeExecution/authorizeProject alone would
// satisfy for managing the underlying project). A tenant editor with no
// campaign_manager grant must be denied even for their own tenant, since a
// campaign freezes other teams' work for its window.
func TestRBAC_CreateCampaignRequiresCampaignManagerForTheDeclaredTenant(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	acme := createTenant(t, f, "acme", "Acme")
	globex := createTenant(t, f, "globex", "Globex")
	f.prov.Register("alice-tok", account.Account{Subject: "alice"})
	assignRole(t, f, acme, "alice", rbac.RoleTenantEditor)
	f.prov.Register("pm-tok", account.Account{Subject: "pm"})
	assignRole(t, f, acme, "pm", rbac.RoleCampaignManager)

	projectID := createProjectInTenantReturningID(t, f, "acme-web", "team-a", acme)
	executionID := decodeID(t, f.req(t, http.MethodPost, "/api/executions", "admin-tok",
		url.Values{"name": {"readiness"}, "project_id": {strconv.FormatInt(projectID, 10)}}))

	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	form := url.Values{
		"name": {"Supersale"}, "window_start": {start}, "window_end": {end},
		"service_project_id":   {strconv.FormatInt(projectID, 10)},
		"service_execution_id": {strconv.FormatInt(executionID, 10)},
	}

	// Alice (tenant editor, can manage the underlying project) still cannot
	// create a campaign -- that authority requires campaign_manager.
	acmePath := "/api/tenants/" + strconv.FormatInt(acme, 10) + "/campaigns"
	rec := f.req(t, http.MethodPost, acmePath, "alice-tok", form)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("editor create campaign = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}

	// The campaign manager can create one for their own tenant...
	rec = f.req(t, http.MethodPost, acmePath, "pm-tok", form)
	if rec.Code != http.StatusCreated {
		t.Fatalf("campaign manager create campaign (own tenant) = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}

	// ...but not for globex, a tenant they hold no grant in.
	globexPath := "/api/tenants/" + strconv.FormatInt(globex, 10) + "/campaigns"
	rec = f.req(t, http.MethodPost, globexPath, "pm-tok", form)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("campaign manager create campaign (foreign tenant) = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

// authorizeScheduleTenant now asks ResourceSchedule/create (Phase 20): the
// permission model can finally express "may reserve this tenant's capacity"
// separately from "may edit its projects". A viewer (schedule:read+list
// only) is denied; an editor (schedule:write) is admitted; an outsider
// stays denied.
func TestRBAC_CreateScheduleRequiresScheduleCreate(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	acme := createTenant(t, f, "acme", "Acme")
	f.prov.Register("carol-tok", account.Account{Subject: "carol"})
	assignRole(t, f, acme, "carol", rbac.RoleTenantViewer)
	f.prov.Register("bob-tok", account.Account{Subject: "bob"})
	assignRole(t, f, acme, "bob", rbac.RoleTenantEditor)

	executionID, schedulePath := seedScheduledExecution(t, f, acme, "peak")
	_ = executionID
	fireAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	form := url.Values{
		"tenant_id": {strconv.FormatInt(acme, 10)}, "kind": {"one_shot"}, "fire_at": {fireAt},
	}

	// The viewer holds schedule:read+list, not schedule:create.
	if rec := f.req(t, http.MethodPost, schedulePath, "carol-tok", form); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer create schedule = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	// The editor holds schedule:write.
	if rec := f.req(t, http.MethodPost, schedulePath, "bob-tok", form); rec.Code != http.StatusCreated {
		t.Fatalf("editor create schedule = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
}

// seedScheduledExecution provisions the project/scenario/execution/config
// chain a schedule needs (scheduleapp.Create reads the load profile to
// reserve quota), as the admin, and returns the execution id and its
// schedules path.
func seedScheduledExecution(t *testing.T, f *rbacFixture, tenantID int64, name string) (int64, string) {
	t.Helper()
	projectID := createProjectInTenantReturningID(t, f, "sched-web", "team-a", tenantID)
	scenarioID := decodeID(t, f.req(t, http.MethodPost, "/api/scenarios", "admin-tok",
		url.Values{"name": {"smoke"}, "project_id": {strconv.FormatInt(projectID, 10)}}))
	executionID := decodeID(t, f.req(t, http.MethodPost, "/api/executions", "admin-tok",
		url.Values{"name": {name}, "project_id": {strconv.FormatInt(projectID, 10)}}))
	configYAML := fmt.Sprintf(`multi-test:
  collectionid: %d
  tests:
    - testid: %d
      concurrency: 10
      rampup: 1
      engines: 2
      duration: 30
`, executionID, scenarioID)
	path := "/api/executions/" + strconv.FormatInt(executionID, 10) + "/config"
	if rec := putMultipartAuth(t, f, path, "admin-tok", "config.yaml", configYAML); rec.Code != http.StatusOK {
		t.Fatalf("upload config = %d (%s)", rec.Code, rec.Body.String())
	}
	return executionID, "/api/executions/" + strconv.FormatInt(executionID, 10) + "/schedules"
}

// Bug 2 of the spec's "three live bugs": campaign_manager gets 403 on its own
// campaign's verdict, because authorizeAnyParticipatingProject demanded
// project:update on a participating project and the PM holds project:read
// only -- the PM could create the event and could not read its result. The
// check now accepts campaign:read on the campaign's own tenant first, while
// the participating-project fallback (now project:read) still serves a
// service owner with no PM grant.
func TestRBAC_CampaignManagerReadsOwnCampaignVerdict(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	acme := createTenant(t, f, "acme", "Acme")
	f.prov.Register("dave-tok", account.Account{Subject: "dave"})
	assignRole(t, f, acme, "dave", rbac.RoleCampaignManager)
	f.prov.Register("bob-tok", account.Account{Subject: "bob"})
	assignRole(t, f, acme, "bob", rbac.RoleTenantEditor)
	f.prov.Register("outsider-tok", account.Account{Subject: "outsider"})

	projectID := createProjectInTenantReturningID(t, f, "acme-web", "team-a", acme)
	executionID := decodeID(t, f.req(t, http.MethodPost, "/api/executions", "admin-tok",
		url.Values{"name": {"readiness"}, "project_id": {strconv.FormatInt(projectID, 10)}}))

	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	created := decodeID(t, f.req(t, http.MethodPost, "/api/tenants/"+strconv.FormatInt(acme, 10)+"/campaigns", "dave-tok",
		url.Values{
			"name": {"Supersale"}, "window_start": {start}, "window_end": {end},
			"service_project_id":   {strconv.FormatInt(projectID, 10)},
			"service_execution_id": {strconv.FormatInt(executionID, 10)},
		}))
	verdictPath := "/api/campaigns/" + strconv.FormatInt(created, 10) + "/verdict"

	// The PM reads the verdict of the event they run (bug 2 fixed).
	if rec := f.req(t, http.MethodGet, verdictPath, "dave-tok", nil); rec.Code != http.StatusOK {
		t.Fatalf("campaign manager verdict = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	// The participating-project fallback still admits a service owner.
	if rec := f.req(t, http.MethodGet, verdictPath, "bob-tok", nil); rec.Code != http.StatusOK {
		t.Fatalf("editor (participating project) verdict = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	// Someone with no relationship to the campaign or its projects is still
	// denied.
	if rec := f.req(t, http.MethodGet, verdictPath, "outsider-tok", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("outsider verdict = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

// Phase 20 Block B regression checkpoint (task 7): the spec's "three live
// bugs", each with its own rbacFixture test in this file --
//   bug 1: TestRBAC_Regression_ViewerIsReadOnlyNotLockedOut (below) and
//          TestRBAC_ViewerCanReadButNotDeleteExecution (task 5)
//   bug 2: TestRBAC_CampaignManagerReadsOwnCampaignVerdict (task 6)
//   bug 3: TestRBAC_Regression_TenantAdminAdministersOwnTenant (below)
// All three green is the gate block C builds on.

// Bug 1 in full: a tenant_viewer is read-only on executions, not locked out
// of them. Every read succeeds; every lifecycle mutation (deploy, trigger,
// stop, purge), config write, and delete is a 403 -- the pre-Phase-20
// alternative was an all-403 wall that looked exactly like enforcement.
func TestRBAC_Regression_ViewerIsReadOnlyNotLockedOut(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	acme := createTenant(t, f, "acme", "Acme")
	f.prov.Register("carol-tok", account.Account{Subject: "carol"})
	assignRole(t, f, acme, "carol", rbac.RoleTenantViewer)

	executionID, _ := seedScheduledExecution(t, f, acme, "peak")
	base := "/api/executions/" + strconv.FormatInt(executionID, 10)

	// Reads succeed (bug 1's core: the viewer sees the execution).
	if rec := f.req(t, http.MethodGet, base, "carol-tok", nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer GET execution = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	// Every mutation 403s: lifecycle actions (ResourceRun), config write,
	// file mutation, and delete (ResourceExecution).
	for _, mut := range []struct{ method, path string }{
		{http.MethodPost, base + "/deploy"},
		{http.MethodPost, base + "/trigger"},
		{http.MethodPost, base + "/stop"},
		{http.MethodPost, base + "/purge"},
		{http.MethodDelete, base},
	} {
		if rec := f.req(t, mut.method, mut.path, "carol-tok", nil); rec.Code != http.StatusForbidden {
			t.Fatalf("viewer %s %s = %d, want 403 (%s)", mut.method, mut.path, rec.Code, rec.Body.String())
		}
	}
	configYAML := fmt.Sprintf("multi-test:\n  collectionid: %d\n  tests:\n    - testid: %d\n      concurrency: 10\n", executionID, executionID)
	if rec := putMultipartAuth(t, f, base+"/config", "carol-tok", "config.yaml", configYAML); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer PUT config = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

// Bug 3: a tenant_admin could not administer its own tenant -- it held no
// ResourceTenant permission and tenantAdminGate's check carried no tenant
// scope, so all 13 routes behind it (including the reservation calendar the
// Reservations page depends on) admitted only service_provider_admin. With
// tenant:admin on the role and the gate scoped to the path's tenant, the
// tenant's own admin reaches its quota; a foreign tenant's admin does not;
// and tenant-global routes still require the service-provider admin.
func TestRBAC_Regression_TenantAdminAdministersOwnTenant(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	acme := createTenant(t, f, "acme", "Acme")
	globex := createTenant(t, f, "globex", "Globex")
	f.prov.Register("ta-tok", account.Account{Subject: "ta"})
	assignRole(t, f, globex, "ta", rbac.RoleTenantAdmin)

	// The globex admin reaches globex's quota route (AC7).
	globexQuota := "/api/tenants/" + strconv.FormatInt(globex, 10) + "/quota"
	if rec := f.req(t, http.MethodGet, globexQuota, "ta-tok", nil); rec.Code != http.StatusOK {
		t.Fatalf("tenant_admin own-tenant quota = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	// Not acme's -- the grant is scoped to the tenant, never global.
	acmeQuota := "/api/tenants/" + strconv.FormatInt(acme, 10) + "/quota"
	if rec := f.req(t, http.MethodGet, acmeQuota, "ta-tok", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("tenant_admin foreign-tenant quota = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	// And not the platform-wide listing either: that stays with the
	// service-provider admin.
	if rec := f.req(t, http.MethodGet, "/api/tenants", "ta-tok", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("tenant_admin list all tenants = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

// Calibration reuses ordinary project/execution authorization
// (authorizeProject/authorizeExecution) rather than a dedicated resource --
// this proves that reuse actually gates the new routes, the same way it
// already gates project/execution/schedule ones, for a caller with no
// relationship to the owning tenant at all.
func TestRBAC_CalibrationRoutesRequireProjectAuthorization(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	acme := createTenant(t, f, "acme", "Acme")
	globex := createTenant(t, f, "globex", "Globex")
	f.prov.Register("acme-editor-tok", account.Account{Subject: "acme-editor"})
	assignRole(t, f, acme, "acme-editor", rbac.RoleTenantEditor)
	f.prov.Register("globex-editor-tok", account.Account{Subject: "globex-editor"})
	assignRole(t, f, globex, "globex-editor", rbac.RoleTenantEditor)

	acmeProject := createProjectInTenantReturningID(t, f, "acme-web", "team-a", acme)
	globexProject := createProjectInTenantReturningID(t, f, "globex-web", "team-b", globex)

	// The source execution the acme editor's calibration binds from: a
	// scenario and an execution running it, both under acme (Create copies
	// that binding -- phase 41). Seeded through the store; the config
	// upload path has its own authorization tests.
	acmeScenario := decodeID(t, f.req(t, http.MethodPost, "/api/scenarios", "acme-editor-tok",
		url.Values{"name": {"smoke"}, "project_id": {strconv.FormatInt(acmeProject, 10)}}))
	acmeSource := decodeID(t, f.req(t, http.MethodPost, "/api/executions", "acme-editor-tok",
		url.Values{"name": {"peak"}, "project_id": {strconv.FormatInt(acmeProject, 10)}}))
	if err := f.store.StoreLoadProfile(context.Background(), acmeSource, false,
		[]loadprofile.Entry{{ScenarioID: acmeScenario, Engines: 1, Concurrency: 1, Duration: 30}}); err != nil {
		t.Fatalf("StoreLoadProfile(source): %v", err)
	}

	// The globex editor cannot create a calibration under an acme project.
	form := url.Values{
		"project_id": {strconv.FormatInt(acmeProject, 10)}, "name": {"calib"}, "engine": {"jmeter"},
		"criterion": {"failures>5%"}, "cpu": {"1"}, "memory": {"512Mi"},
		"scenario_id":         {strconv.FormatInt(acmeScenario, 10)},
		"source_execution_id": {strconv.FormatInt(acmeSource, 10)},
	}
	rec := f.req(t, http.MethodPost, "/api/calibrations", "globex-editor-tok", form)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create calibration under a foreign project = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}

	// The acme editor can, for their own project.
	rec = f.req(t, http.MethodPost, "/api/calibrations", "acme-editor-tok", form)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create calibration under own project = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var created struct {
		ExecutionID int64 `json:"execution_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}

	// Triggering it is gated the same way (authorizeExecution): the globex
	// editor cannot trigger an execution under acme's project either.
	triggerPath := "/api/executions/" + strconv.FormatInt(created.ExecutionID, 10) + "/calibration/trigger"
	rec = f.req(t, http.MethodPost, triggerPath, "globex-editor-tok", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("trigger by a foreign editor = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	rec = f.req(t, http.MethodPost, triggerPath, "acme-editor-tok", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("trigger by the owning editor = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}

	// A globex project's calibration route we never used above -- confirm
	// createCalibration also 403s when globexProject itself is targeted by
	// someone with no relationship to it at all (belt-and-suspenders: this
	// is the same assertion as the first one, phrased against the other
	// tenant's own project to rule out an accidental tenant-ID mixup).
	f.prov.Register("outsider-tok", account.Account{Subject: "outsider"})
	form2 := url.Values{
		"project_id": {strconv.FormatInt(globexProject, 10)}, "name": {"calib"}, "engine": {"jmeter"},
		"criterion": {"failures>5%"}, "cpu": {"1"}, "memory": {"512Mi"},
	}
	rec = f.req(t, http.MethodPost, "/api/calibrations", "outsider-tok", form2)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create calibration by an outsider = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

// The read-your-own-resource bucket (task 9): every previously ungated GET
// now asks <resource>:read against the row's tenant. The negative is the
// audit's job; this is the positive -- a tenant_viewer reads all of them
// (200), while a caller with no relationship to the tenant is denied on
// each. Without the positive half, a 403-wall would look exactly like
// enforcement (spec bug 1's lesson).
func TestRBAC_ViewerReadsOwnResourceBucket(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	acme := createTenant(t, f, "acme", "Acme")
	f.prov.Register("carol-tok", account.Account{Subject: "carol"})
	assignRole(t, f, acme, "carol", rbac.RoleTenantViewer)
	f.prov.Register("outsider-tok", account.Account{Subject: "outsider"})

	executionID, _ := seedScheduledExecution(t, f, acme, "peak")
	scenarioID := decodeID(t, f.req(t, http.MethodPost, "/api/scenarios", "admin-tok",
		url.Values{"name": {"smoke"}, "project_id": {"1"}}))
	projectPath := "/api/projects/1"
	scenarioPath := "/api/scenarios/" + strconv.FormatInt(scenarioID, 10)
	execPath := "/api/executions/" + strconv.FormatInt(executionID, 10)

	reads := []string{
		projectPath, scenarioPath, scenarioPath + "/files",
		execPath, execPath + "/files", execPath + "/config",
		execPath + "/status", execPath + "/engines",
	}
	for _, path := range reads {
		if rec := f.req(t, http.MethodGet, path, "carol-tok", nil); rec.Code != http.StatusOK {
			t.Fatalf("viewer GET %s = %d, want 200 (%s)", path, rec.Code, rec.Body.String())
		}
	}
	for _, path := range reads {
		if rec := f.req(t, http.MethodGet, path, "outsider-tok", nil); rec.Code != http.StatusForbidden {
			t.Fatalf("outsider GET %s = %d, want 403 (%s)", path, rec.Code, rec.Body.String())
		}
	}
}

// The run/report bucket (task 10): all seven report routes authorize
// rbac.ResourceReport/ActionRead against the owning execution's tenant --
// execution-keyed routes directly, run-keyed routes (run report, shard
// log/config, export) by resolving the run's execution first. tenant_viewer
// and campaign_manager both hold report:read; a caller with no relationship
// to the tenant is denied on every one.
func TestRBAC_ReportRoutesRequireReportRead(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	acme := createTenant(t, f, "acme", "Acme")
	f.prov.Register("carol-tok", account.Account{Subject: "carol"})
	assignRole(t, f, acme, "carol", rbac.RoleTenantViewer)
	f.prov.Register("dave-tok", account.Account{Subject: "dave"})
	assignRole(t, f, acme, "dave", rbac.RoleCampaignManager)
	f.prov.Register("outsider-tok", account.Account{Subject: "outsider"})

	executionID, _ := seedScheduledExecution(t, f, acme, "peak")
	scenarioID := decodeID(t, f.req(t, http.MethodPost, "/api/scenarios", "admin-tok",
		url.Values{"name": {"smoke"}, "project_id": {"1"}}))

	ctx := context.Background()
	runID, err := f.store.StartRun(ctx, executionID, "trace-1")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	now := time.Now().UTC()
	if err := f.reports.SaveReport(ctx, report.Report{
		ExecutionID: executionID, RunID: runID, ScenarioID: scenarioID,
		Outcome: taurus.OutcomePassed, StartedAt: now, EndedAt: now,
	}); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	if err := f.obj.Upload(ctx, lifecycleapp.RunShardKey(runID, scenarioID, 0, "log"), strings.NewReader("engine output")); err != nil {
		t.Fatalf("Upload shard log: %v", err)
	}

	exec := "/api/executions/" + strconv.FormatInt(executionID, 10)
	runBase := "/api/runs/" + strconv.FormatInt(runID, 10) + "/scenarios/" + strconv.FormatInt(scenarioID, 10)
	reads := []string{
		exec + "/reports", exec + "/trend", exec + "/error-signatures",
		"/api/runs/" + strconv.FormatInt(runID, 10) + "/report",
		"/api/runs/" + strconv.FormatInt(runID, 10) + "/export?format=json",
		runBase + "/shards/0/log",
	}
	// The viewer and the campaign manager both hold report:read in acme.
	for _, tok := range []string{"carol-tok", "dave-tok"} {
		for _, path := range reads {
			if rec := f.req(t, http.MethodGet, path, tok, nil); rec.Code != http.StatusOK {
				t.Fatalf("%s GET %s = %d, want 200 (%s)", tok, path, rec.Code, rec.Body.String())
			}
		}
	}
	// Shard config has no stored object (only the log was uploaded), so an
	// authorized reader reaches the store and gets a 404 -- proof the route
	// was reached, not blocked -- while the outsider is stopped at 403
	// before the store is ever consulted.
	if rec := f.req(t, http.MethodGet, runBase+"/shards/0/config", "carol-tok", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("viewer GET shard config = %d, want 404 (authorized, object absent)", rec.Code)
	}
	for _, path := range append(reads, runBase+"/shards/0/config") {
		if rec := f.req(t, http.MethodGet, path, "outsider-tok", nil); rec.Code != http.StatusForbidden {
			t.Fatalf("outsider GET %s = %d, want 403 (%s)", path, rec.Code, rec.Body.String())
		}
	}
}

// The remaining bucket (task 11): platform-wide surfaces are system:admin,
// the file download dispatches on kind, and the SSE stream authorizes once
// at open. AC4: Alice (service_provider_admin) reads the cluster registry
// and usage; Bob/Carol/Dave -- every tenant role -- are 403 on both.
func TestRBAC_PlatformSurfacesRequireSystemAdmin(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	acme := createTenant(t, f, "acme", "Acme")
	f.prov.Register("bob-tok", account.Account{Subject: "bob"})
	assignRole(t, f, acme, "bob", rbac.RoleTenantEditor)
	f.prov.Register("carol-tok", account.Account{Subject: "carol"})
	assignRole(t, f, acme, "carol", rbac.RoleTenantViewer)
	f.prov.Register("dave-tok", account.Account{Subject: "dave"})
	assignRole(t, f, acme, "dave", rbac.RoleCampaignManager)

	for _, path := range []string{"/api/clusters", "/api/usage/history", "/api/usage/summary", "/api/admin/executions", "/api/admin/nodes"} {
		if rec := f.req(t, http.MethodGet, path, "admin-tok", nil); rec.Code != http.StatusOK {
			t.Fatalf("alice GET %s = %d, want 200 (%s)", path, rec.Code, rec.Body.String())
		}
		for _, tok := range []string{"bob-tok", "carol-tok", "dave-tok"} {
			if rec := f.req(t, http.MethodGet, path, tok, nil); rec.Code != http.StatusForbidden {
				t.Fatalf("%s GET %s = %d, want 403 (%s)", tok, path, rec.Code, rec.Body.String())
			}
		}
	}
}

// AC8: the reservation calendar is schedule:list on the path's tenant --
// Dave (campaign_manager in tenants 1 and 2, nothing in 3) reads the plan
// of every tenant he coordinates and is denied in the one he holds no
// grant in; Carol (tenant_viewer, schedule:read+list) reads her own
// tenant's calendar.
func TestRBAC_ReservationsFollowScheduleList(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	acme := createTenant(t, f, "acme", "Acme")
	globex := createTenant(t, f, "globex", "Globex")
	initech := createTenant(t, f, "initech", "Initech")
	f.prov.Register("dave-tok", account.Account{Subject: "dave"})
	assignRole(t, f, acme, "dave", rbac.RoleCampaignManager)
	assignRole(t, f, globex, "dave", rbac.RoleCampaignManager)
	f.prov.Register("carol-tok", account.Account{Subject: "carol"})
	assignRole(t, f, acme, "carol", rbac.RoleTenantViewer)

	for _, tenant := range []int64{acme, globex} {
		path := "/api/tenants/" + strconv.FormatInt(tenant, 10) + "/reservations"
		if rec := f.req(t, http.MethodGet, path, "dave-tok", nil); rec.Code != http.StatusOK {
			t.Fatalf("dave GET reservations (coordinated tenant %d) = %d, want 200 (%s)", tenant, rec.Code, rec.Body.String())
		}
	}
	// Tenant 3: no grant, no calendar.
	outsider := "/api/tenants/" + strconv.FormatInt(initech, 10) + "/reservations"
	if rec := f.req(t, http.MethodGet, outsider, "dave-tok", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("dave GET reservations (tenant he holds nothing in) = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	// The viewer reads her own tenant's calendar (schedule:read+list).
	own := "/api/tenants/" + strconv.FormatInt(acme, 10) + "/reservations"
	if rec := f.req(t, http.MethodGet, own, "carol-tok", nil); rec.Code != http.StatusOK {
		t.Fatalf("carol GET own-tenant reservations = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if rec := f.req(t, http.MethodGet, outsider, "carol-tok", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("carol GET foreign-tenant reservations = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

// /api/files/{kind}/{id}/{name} dispatches on kind to the scenario or
// execution read check, and the SSE stream authorizes once at open before
// upgrading -- EventSource cannot carry a header, so the decision must not
// depend on one.
func TestRBAC_FilesAndStreamDispatchByResource(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	acme := createTenant(t, f, "acme", "Acme")
	f.prov.Register("carol-tok", account.Account{Subject: "carol"})
	assignRole(t, f, acme, "carol", rbac.RoleTenantViewer)
	f.prov.Register("outsider-tok", account.Account{Subject: "outsider"})

	projectID := createProjectInTenantReturningID(t, f, "acme-web", "team-a", acme)
	scenarioID := decodeID(t, f.req(t, http.MethodPost, "/api/scenarios", "admin-tok",
		url.Values{"name": {"smoke"}, "project_id": {strconv.FormatInt(projectID, 10)}}))
	executionID := decodeID(t, f.req(t, http.MethodPost, "/api/executions", "admin-tok",
		url.Values{"name": {"peak"}, "project_id": {strconv.FormatInt(projectID, 10)}}))

	// A stored scenario artifact: the viewer downloads it, the outsider is
	// denied before the object store is consulted.
	key := fmt.Sprintf("scenario/%d/plan.jmx", scenarioID)
	if err := f.obj.Upload(context.Background(), key, strings.NewReader("<jmx/>")); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	scenarioFile := "/api/files/scenario/" + strconv.FormatInt(scenarioID, 10) + "/plan.jmx"
	if rec := f.req(t, http.MethodGet, scenarioFile, "carol-tok", nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer GET scenario file = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if rec := f.req(t, http.MethodGet, scenarioFile, "outsider-tok", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("outsider GET scenario file = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	// Execution-kind dispatch: no object stored, so the authorized viewer
	// reaches the store (404) while the outsider is stopped at 403.
	execFile := "/api/files/execution/" + strconv.FormatInt(executionID, 10) + "/data.csv"
	if rec := f.req(t, http.MethodGet, execFile, "carol-tok", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("viewer GET execution file = %d, want 404 (authorized, object absent)", rec.Code)
	}
	if rec := f.req(t, http.MethodGet, execFile, "outsider-tok", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("outsider GET execution file = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}

	// The stream: the viewer's request is accepted as SSE (200 +
	// text/event-stream before any event), the outsider's rejected at open.
	// The request context is cancelled shortly after the open so the
	// handler's select returns instead of streaming forever.
	stream := "/api/executions/" + strconv.FormatInt(executionID, 10) + "/stream"
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	r := httptest.NewRequest(http.MethodGet, stream, nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer carol-tok")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("viewer stream = %d %q, want 200 text/event-stream", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := f.req(t, http.MethodGet, stream, "outsider-tok", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("outsider stream = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

// seedExecutionInTenant is seedScheduledExecution plus the scenario id, for
// tests that need to author a valid config body against the seeded rows.
func seedExecutionInTenant(t *testing.T, f *rbacFixture, tenantID int64, name string) (executionID, scenarioID int64, schedPath string) {
	t.Helper()
	executionID, schedPath = seedScheduledExecution(t, f, tenantID, name)
	// seedScheduledExecution created its scenario as the tenant's only one;
	// resolve it through the project's listing rather than duplicating the
	// chain. The project is the tenant's only project (id order).
	rec := f.req(t, http.MethodGet, "/api/projects", "admin-tok", nil)
	var projects []struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		TenantID *int64 `json:"tenant_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &projects); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	for _, p := range projects {
		if p.TenantID != nil && *p.TenantID == tenantID {
			scenarioID = decodeID(t, f.req(t, http.MethodPost, "/api/scenarios", "admin-tok",
				url.Values{"name": {"extra"}, "project_id": {strconv.FormatInt(p.ID, 10)}}))
			return executionID, scenarioID, schedPath
		}
	}
	t.Fatalf("no project found in tenant %d", tenantID)
	return 0, 0, ""
}

// The four-persona proof (task 12 -- the A→B→C gate): Alice
// (service_provider_admin) administers the platform; Bob (tenant_editor)
// runs load tests in tenant 1; Carol (tenant_viewer) reads his results and
// mutates nothing; Dave (campaign_manager in tenants 1 and 2) coordinates
// both calendars without edit rights anywhere -- until composition grants
// him tenant_editor in his own tenant, which flips exactly the writes
// there (AC10's second half) and leaves tenant 1 untouched. AC4, AC5, AC7,
// AC8, and AC10 in one place.
func TestRBAC_FourPersonas_AuditMatrix(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	acme := createTenant(t, f, "acme", "Acme")
	globex := createTenant(t, f, "globex", "Globex")
	initech := createTenant(t, f, "initech", "Initech")

	// Personas: Alice is the fixture's admin-tok.
	const aliceTok = "admin-tok"
	f.prov.Register("bob-tok", account.Account{Subject: "bob"})
	assignRole(t, f, acme, "bob", rbac.RoleTenantEditor)
	f.prov.Register("carol-tok", account.Account{Subject: "carol"})
	assignRole(t, f, acme, "carol", rbac.RoleTenantViewer)
	f.prov.Register("dave-tok", account.Account{Subject: "dave"})
	assignRole(t, f, acme, "dave", rbac.RoleCampaignManager)
	assignRole(t, f, globex, "dave", rbac.RoleCampaignManager)
	// AC7's subject: a tenant's own admin.
	f.prov.Register("erin-tok", account.Account{Subject: "erin"})
	assignRole(t, f, acme, "erin", rbac.RoleTenantAdmin)

	acmeExec, _, acmeSched := seedExecutionInTenant(t, f, acme, "peak")
	globexExec, globexScenario, globexSched := seedExecutionInTenant(t, f, globex, "pm-own")

	// AC4: platform surfaces are Alice's alone.
	for _, path := range []string{"/api/clusters", "/api/usage/summary"} {
		if rec := f.req(t, http.MethodGet, path, aliceTok, nil); rec.Code != http.StatusOK {
			t.Fatalf("alice GET %s = %d, want 200 (%s)", path, rec.Code, rec.Body.String())
		}
		for _, tok := range []string{"bob-tok", "carol-tok", "dave-tok"} {
			if rec := f.req(t, http.MethodGet, path, tok, nil); rec.Code != http.StatusForbidden {
				t.Fatalf("%s GET %s = %d, want 403 (%s)", tok, path, rec.Code, rec.Body.String())
			}
		}
	}

	// AC5: Carol reads Bob's execution, and cannot delete it.
	acmePath := "/api/executions/" + strconv.FormatInt(acmeExec, 10)
	if rec := f.req(t, http.MethodGet, acmePath, "carol-tok", nil); rec.Code != http.StatusOK {
		t.Fatalf("carol GET execution = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if rec := f.req(t, http.MethodDelete, acmePath, "carol-tok", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("carol DELETE execution = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}

	// AC7: tenant_admin reaches its own tenant's quota.
	quota := "/api/tenants/" + strconv.FormatInt(acme, 10) + "/quota"
	if rec := f.req(t, http.MethodGet, quota, "erin-tok", nil); rec.Code != http.StatusOK {
		t.Fatalf("tenant_admin GET own quota = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	// AC8: Dave reads the calendar of every tenant he coordinates, not the
	// one he holds nothing in.
	for _, tenant := range []int64{acme, globex} {
		path := "/api/tenants/" + strconv.FormatInt(tenant, 10) + "/reservations"
		if rec := f.req(t, http.MethodGet, path, "dave-tok", nil); rec.Code != http.StatusOK {
			t.Fatalf("dave GET tenant %d reservations = %d, want 200 (%s)", tenant, rec.Code, rec.Body.String())
		}
	}
	if rec := f.req(t, http.MethodGet, "/api/tenants/"+strconv.FormatInt(initech, 10)+"/reservations", "dave-tok", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("dave GET tenant 3 reservations = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}

	// AC10, first half: in a tenant Dave oversees but does not own, he sees
	// the plan and can change nothing.
	fireAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if rec := f.req(t, http.MethodGet, acmeSched, "dave-tok", nil); rec.Code != http.StatusOK {
		t.Fatalf("dave GET acme schedules = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if rec := f.req(t, http.MethodPost, acmeSched, "dave-tok", url.Values{
		"tenant_id": {strconv.FormatInt(acme, 10)}, "kind": {"one_shot"}, "fire_at": {fireAt},
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("dave POST acme schedules = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	acmeConfig := fmt.Sprintf("multi-test:\n  collectionid: %d\n  tests:\n    - testid: %d\n      concurrency: 10\n", acmeExec, acmeExec)
	if rec := putMultipartAuth(t, f, acmePath+"/config", "dave-tok", "config.yaml", acmeConfig); rec.Code != http.StatusForbidden {
		t.Fatalf("dave PUT acme config = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}

	// AC10, second half: composition. Grant Dave tenant_editor in his own
	// tenant (globex) -- the same subject, a second grant -- and exactly the
	// writes flip there, while tenant 1 stays read-only for him.
	assignRole(t, f, globex, "dave", rbac.RoleTenantEditor)
	if rec := f.req(t, http.MethodPost, globexSched, "dave-tok", url.Values{
		"tenant_id": {strconv.FormatInt(globex, 10)}, "kind": {"one_shot"}, "fire_at": {fireAt},
	}); rec.Code != http.StatusCreated {
		t.Fatalf("composed dave POST globex schedules = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	globexPath := "/api/executions/" + strconv.FormatInt(globexExec, 10)
	globexConfig := fmt.Sprintf("multi-test:\n  collectionid: %d\n  tests:\n    - testid: %d\n      concurrency: 10\n      rampup: 1\n      engines: 2\n      duration: 30\n", globexExec, globexScenario)
	if rec := putMultipartAuth(t, f, globexPath+"/config", "dave-tok", "config.yaml", globexConfig); rec.Code != http.StatusOK {
		t.Fatalf("composed dave PUT globex config = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	// And acme is unchanged: the grant is scoped, never global.
	if rec := f.req(t, http.MethodPost, acmeSched, "dave-tok", url.Values{
		"tenant_id": {strconv.FormatInt(acme, 10)}, "kind": {"one_shot"}, "fire_at": {fireAt},
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("dave POST acme schedules after composition = %d, want still 403 (%s)", rec.Code, rec.Body.String())
	}
}

// Task 16 / AC9 (Block D): campaign CRUD end to end. Dave, the
// campaign_manager, edits his own campaign's preparation definition -- 200
// while the window is future, 409 the moment it has opened -- and can abort
// it himself without the platform kill-switch. The cross-tenant list
// (GET /api/campaigns) shows him every tenant he coordinates and no tenant
// he doesn't. AC6 is re-confirmed on the *edited* campaign now that CRUD
// exists: the PM reads the verdict of the event he runs.
func TestRBAC_CampaignManagerEditsAbortsAndListsAcrossTenants(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)

	acme := createTenant(t, f, "acme", "Acme")
	globex := createTenant(t, f, "globex", "Globex")
	initech := createTenant(t, f, "initech", "Initech")
	f.prov.Register("dave-tok", account.Account{Subject: "dave"})
	assignRole(t, f, acme, "dave", rbac.RoleCampaignManager)
	assignRole(t, f, globex, "dave", rbac.RoleCampaignManager)
	f.prov.Register("bob-tok", account.Account{Subject: "bob"})
	assignRole(t, f, acme, "bob", rbac.RoleTenantEditor)
	f.prov.Register("nobody-tok", account.Account{Subject: "nobody"})

	// A project + designated execution per tenant, so each tenant can host
	// a real campaign.
	seed := func(tenant int64, projectName string) (int64, int64) {
		projectID := createProjectInTenantReturningID(t, f, projectName, "team-a", tenant)
		executionID := decodeID(t, f.req(t, http.MethodPost, "/api/executions", "admin-tok",
			url.Values{"name": {projectName + "-exec"}, "project_id": {strconv.FormatInt(projectID, 10)}}))
		return projectID, executionID
	}
	acmeProject, acmeExec := seed(acme, "acme-web")
	globexProject, globexExec := seed(globex, "globex-web")
	initechProject, initechExec := seed(initech, "initech-web")

	createCampaign := func(tok string, tenant int64, projectID, executionID int64, name string, startOffset, endOffset time.Duration) int64 {
		start := time.Now().Add(startOffset).UTC().Format(time.RFC3339)
		end := time.Now().Add(endOffset).UTC().Format(time.RFC3339)
		return decodeID(t, f.req(t, http.MethodPost, "/api/tenants/"+strconv.FormatInt(tenant, 10)+"/campaigns", tok,
			url.Values{
				"name": {name}, "window_start": {start}, "window_end": {end},
				"service_project_id":   {strconv.FormatInt(projectID, 10)},
				"service_execution_id": {strconv.FormatInt(executionID, 10)},
			}))
	}

	// AC9's two sides: a campaign still in preparation (window +1h..+2h)
	// and a live one (window -1h..+1h).
	future := createCampaign("dave-tok", acme, acmeProject, acmeExec, "Future-event", time.Hour, 2*time.Hour)
	live := createCampaign("dave-tok", acme, acmeProject, acmeExec, "Live-event", -time.Hour, time.Hour)

	editForm := url.Values{
		"name":                 {"Future-event-v2"},
		"window_start":         {time.Now().Add(90 * time.Minute).UTC().Format(time.RFC3339)},
		"window_end":           {time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)},
		"service_project_id":   {strconv.FormatInt(acmeProject, 10)},
		"service_execution_id": {strconv.FormatInt(acmeExec, 10)},
	}

	// AC9: 200 while the window is future.
	path := func(id int64) string { return "/api/campaigns/" + strconv.FormatInt(id, 10) }
	rec := f.req(t, http.MethodPut, path(future), "dave-tok", editForm)
	if rec.Code != http.StatusOK {
		t.Fatalf("dave PUT future campaign = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var edited struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &edited); err != nil || edited.Name != "Future-event-v2" {
		t.Fatalf("PUT response = %q (%v), want the edited name", rec.Body.String(), err)
	}

	// AC9: 409 once the window has opened -- a live campaign's definition
	// is what freeze and the verdict key off, so it is frozen.
	rec = f.req(t, http.MethodPut, path(live), "dave-tok", editForm)
	if rec.Code != http.StatusConflict {
		t.Fatalf("dave PUT live campaign = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}

	// Editing demands campaign:update on the campaign's own tenant: a
	// tenant editor of that same tenant is still denied.
	rec = f.req(t, http.MethodPut, path(future), "bob-tok", editForm)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("editor PUT campaign = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}

	// AC6, re-confirmed end to end on the edited campaign: the PM reads
	// the verdict of the event he runs.
	if rec := f.req(t, http.MethodGet, path(future)+"/verdict", "dave-tok", nil); rec.Code != http.StatusOK {
		t.Fatalf("dave GET edited campaign verdict = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	// Abort is the PM's own control (campaign:delete): Dave closes his own
	// campaign without the platform kill-switch...
	rec = f.req(t, http.MethodPost, path(future)+"/abort", "dave-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("dave abort own campaign = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	rec = f.req(t, http.MethodGet, path(future), "dave-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("dave GET aborted campaign = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Active    bool       `json:"active"`
		AbortedAt *time.Time `json:"aborted_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode aborted campaign: %v", err)
	}
	if got.Active || got.AbortedAt == nil {
		t.Fatalf("aborted campaign = active:%v aborted_at:%v, want active:false with a timestamp", got.Active, got.AbortedAt)
	}

	// ...and an editor of the same tenant cannot abort it either.
	rec = f.req(t, http.MethodPost, path(live)+"/abort", "bob-tok", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("editor abort campaign = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}

	// The cross-tenant view: Dave coordinates acme and globex, so both
	// tenants' campaigns appear; initech, where he holds nothing, does not.
	globexCampaign := createCampaign("admin-tok", globex, globexProject, globexExec, "Globex-event", time.Hour, 2*time.Hour)
	initechCampaign := createCampaign("admin-tok", initech, initechProject, initechExec, "Initech-event", time.Hour, 2*time.Hour)
	_ = globexCampaign

	rec = f.req(t, http.MethodGet, "/api/campaigns", "dave-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("dave GET /api/campaigns = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Future-event-v2", "Live-event", "Globex-event"} {
		if !strings.Contains(body, want) {
			t.Fatalf("dave GET /api/campaigns missing %q (a tenant he coordinates): %s", want, body)
		}
	}
	if strings.Contains(body, "Initech-event") {
		t.Fatalf("dave GET /api/campaigns leaked a campaign of a tenant he holds nothing in: %s", body)
	}
	_ = initechCampaign

	// An account with no tenant role sees an empty list, never a leak.
	rec = f.req(t, http.MethodGet, "/api/campaigns", "nobody-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("nobody GET /api/campaigns = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var listed []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("nobody GET /api/campaigns decode: %v (%s)", err, rec.Body.String())
	}
	if len(listed) != 0 {
		t.Fatalf("nobody GET /api/campaigns = %d campaigns, want 0", len(listed))
	}
}

// demoRouter wires the router exactly as a demo-mode deployment does: the
// session provider is BOTH the auth provider (cookie verification) and the
// session issuer (minting), with RBAC on.
func demoRouter(t *testing.T) http.Handler {
	t.Helper()
	prov, err := session.New([]byte("demo-test-signing-key"), []session.Profile{
		{
			ID: "dave", Name: "Dave", Subject: "dave",
			Tenants: map[int64][]string{1: {rbac.RoleCampaignManager}, 2: {rbac.RoleCampaignManager}},
		},
	})
	if err != nil {
		t.Fatalf("session provider: %v", err)
	}
	store := fake.NewStore()
	return httpapi.NewRouter(httpapi.Deps{
		Projects: projectapp.NewService(store),
		Auth:     authapp.NewService(prov, store, true),
		Sessions: prov,
	})
}

// cookieValue extracts the cookie's value from a Set-Cookie header line.
func cookieValue(t *testing.T, setCookie string) string {
	t.Helper()
	raw := strings.SplitN(setCookie, ";", 2)[0]
	_, value, ok := strings.Cut(raw, "=")
	if !ok || value == "" {
		t.Fatalf("Set-Cookie %q has no value", setCookie)
	}
	return value
}

// TestRBAC_DemoSessionLifecycle is AC13 end to end: POST /api/session mints
// the HttpOnly cookie, a subsequent authenticated request succeeds (both
// /api/me and a real API route), DELETE /api/session expires it, and the
// next /api/me -- now cookieless, as the browser obeyed Max-Age=0 -- is 401.
func TestRBAC_DemoSessionLifecycle(t *testing.T) {
	t.Parallel()
	router := demoRouter(t)

	serve := func(method, path, cookie, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: session.CookieName, Value: cookie})
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		return rec
	}

	// Before any session: /api/me is 401, unauthenticated.
	if rec := serve(http.MethodGet, "/api/me", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /api/me = %d, want 401", rec.Code)
	}

	// Mint: the cookie carries every security attribute the spec demands.
	rec := serve(http.MethodPost, "/api/session", "", `{"profile":"dave"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST /api/session = %d (%s), want 204", rec.Code, rec.Body.String())
	}
	setCookie := rec.Header().Get("Set-Cookie")
	for _, want := range []string{
		session.CookieName + "=", "Path=/", "HttpOnly", "SameSite=Strict", "Secure", "Max-Age=28800",
	} {
		if !strings.Contains(setCookie, want) {
			t.Fatalf("Set-Cookie %q missing %q", setCookie, want)
		}
	}
	value := cookieValue(t, setCookie)

	// Authenticated: /api/me reflects Dave, and a real gated route admits
	// the cookie -- the same credential EventSource will carry (AC11's
	// mechanism, proven at the HTTP boundary).
	rec = serve(http.MethodGet, "/api/me", value, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated /api/me = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var me struct {
		Subject     string              `json:"subject"`
		Tenants     map[string][]string `json:"tenants"`
		Permissions map[string][]string `json:"permissions"`
		Demo        bool                `json:"demo"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode /api/me: %v", err)
	}
	if me.Subject != "dave" || !me.Demo {
		t.Fatalf("/api/me = %+v, want dave with demo=true", me)
	}
	if len(me.Tenants) != 2 || len(me.Tenants["1"]) != 1 {
		t.Fatalf("/api/me tenants = %v, want campaign_manager in 1 and 2", me.Tenants)
	}
	if len(me.Permissions["campaign"]) == 0 {
		t.Fatalf("/api/me permissions missing campaign: %v", me.Permissions)
	}
	if rec := serve(http.MethodGet, "/api/projects", value, ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/projects with session cookie = %d, want 200", rec.Code)
	}

	// Logout: the cookie expires (Max-Age=0).
	rec = serve(http.MethodDelete, "/api/session", value, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /api/session = %d, want 204", rec.Code)
	}
	if setCookie = rec.Header().Get("Set-Cookie"); !strings.Contains(setCookie, "Max-Age=0") {
		t.Fatalf("logout Set-Cookie %q does not expire", setCookie)
	}

	// The next request carries no cookie -- the SPA shows the picker.
	if rec := serve(http.MethodGet, "/api/me", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("post-logout /api/me = %d, want 401", rec.Code)
	}
}

// TestRBAC_DemoSessionUnknownProfile pins the mint failure modes: an unknown
// persona 404s, and the endpoints 404 outright when demo mode is off (the
// audit fixture's Sessions-less twin, i.e. every non-demo deployment).
func TestRBAC_DemoSessionUnknownProfile(t *testing.T) {
	t.Parallel()
	router := demoRouter(t)
	r := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(`{"profile":"mallory"}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /api/session unknown profile = %d, want 404", rec.Code)
	}
	if rec.Header().Get("Set-Cookie") != "" {
		t.Fatal("failed mint set a cookie")
	}

	// Demo off: no Sessions dep, the endpoints are simply not there.
	store := fake.NewStore()
	off := httpapi.NewRouter(httpapi.Deps{Auth: authapp.NewService(token.New(), store, true)})
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/session"},
		{http.MethodGet, "/api/session/profiles"},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		off.ServeHTTP(rec, r)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s with demo off = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
}
