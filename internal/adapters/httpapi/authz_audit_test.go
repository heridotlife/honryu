package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/adapters/auth/session"
	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/domain/account"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
)

// The route-authorization audit (Phase 20, spec Approach C): a table maps
// every route in httpapi.Routes() to the authorization decision it must
// enforce. A route added without an entry fails TestAuthzAuditCoversRoutes;
// an entry whose route disappeared fails it too. Every non-public entry is
// probed with an authenticated account that holds no grant at all and must
// be rejected -- an ungated route answers 200/404 and fails the probe
// (AC3).
//
// Decision vocabulary, per the spec's four rules:
//
//	public        rule 0: no user authorization at all. /healthz and
//	              /metrics sit outside /api/; /api/ingest carries its own
//	              pod credential and is exempt from the user middleware.
//	system:admin  rule 4: platform-wide surfaces (/api/admin/*,
//	              /api/clusters/*, /api/usage/*) demand ResourceSystem/
//	              ActionAdmin -- the service provider's admin only.
//	authenticated  rule 0.5: any authenticated account may call it; the
//	              caller only ever sees itself (GET /api/me). The probe
//	              asserts a granted-nothing account still gets 200.
//	scoped-list   rule 1: any authenticated account may call it; the result
//	              set is scoped down by acct.TenantIDs() (precedent:
//	              listProjects). The probe asserts no cross-tenant leak.
//	res:action    rules 2+3: the named resource/action must be granted on
//	              the row's (or the create target's) tenant. "|" separates
//	              alternatives, any one of which suffices.
const (
	decisionPublic      = "public"
	decisionSystemAdmin = "system:admin"
	decisionScopedList  = "scoped-list"
	decisionAuthed      = "authenticated"
)

// authzEntry is one route's required decision plus everything the
// unauthorized probe needs to reach the authorization check with valid
// input: a parseable body/queries and seeded path ids.
type authzEntry struct {
	method   string
	pattern  string
	decision string
	// pending names the Block C task that will gate this route. Entries
	// marked pending are the audit's committed-red list: their probe is
	// skipped with this reason until that task lands, because the route
	// reaches no (or the wrong) authorization decision today. Task 12
	// requires the list to be empty.
	pending string
	// form is an application/x-www-form-urlencoded body for mutations that
	// parse their form before authorizing.
	form url.Values
	// query appends to the probe path.
	query url.Values
	// multipart fields + file build a multipart body for the one route
	// (scenario import) whose upload parse precedes its authorization.
	multipart map[string]string
	file      string
}

// authzAuditTable is the intended matrix for the whole API surface, in
// router.go's declaration order. Route-specific resolutions per the spec:
// /api/files/{kind}/{id}/{name} dispatches on kind to the scenario or
// execution read check; the SSE stream authorizes once at open; verdict and
// comparison accept campaign:read on the campaign's tenant OR read on a
// participating project; /api/usage/* is system:admin because
// ports.LaunchRecord has no tenant dimension to scope by; the reservations
// calendar is scheduled capacity materialized, so reading it is
// schedule:list on the path's tenant (Approach B).
var authzAuditTable = []authzEntry{
	{method: "GET", pattern: "/healthz", decision: decisionPublic},
	{method: "GET", pattern: "/metrics", decision: decisionPublic},

	// The session endpoints are public because selecting a persona IS the
	// authentication (spec Approach E); /api/me merely reflects the
	// authenticated caller back.
	{method: "GET", pattern: "/api/session/profiles", decision: decisionPublic},
	{method: "POST", pattern: "/api/session", decision: decisionPublic},
	{method: "DELETE", pattern: "/api/session", decision: decisionPublic},
	{method: "GET", pattern: "/api/me", decision: decisionAuthed},

	// Phase 37: the APM link-out templates carry no run data and no
	// secrets -- a URL shape the operator already chose to serve to every
	// operator's browser -- so any authenticated caller may read them.
	{method: "GET", pattern: "/api/apm-links", decision: decisionAuthed},

	{method: "GET", pattern: "/api/projects", decision: decisionScopedList},
	{method: "POST", pattern: "/api/projects", decision: "project:create",
		form: url.Values{"name": {"x"}, "owner": {"team-x"}}},
	{method: "GET", pattern: "/api/projects/{project_id}", decision: "project:read"},
	{method: "DELETE", pattern: "/api/projects/{project_id}", decision: "project:delete"},

	{method: "POST", pattern: "/api/scenarios", decision: "scenario:create",
		form: url.Values{"name": {"s"}, "project_id": {"{project_id}"}}},
	{method: "POST", pattern: "/api/scenarios/import", decision: "scenario:create",
		multipart: map[string]string{"project_id": "{project_id}", "name": "s"}, file: "plan.jmx"},
	{method: "GET", pattern: "/api/scenarios/{scenario_id}", decision: "scenario:read"},
	{method: "DELETE", pattern: "/api/scenarios/{scenario_id}", decision: "scenario:delete"},
	{method: "GET", pattern: "/api/scenarios/{scenario_id}/files", decision: "scenario:read"},
	{method: "PUT", pattern: "/api/scenarios/{scenario_id}/files", decision: "scenario:update"},
	{method: "DELETE", pattern: "/api/scenarios/{scenario_id}/files", decision: "scenario:delete",
		query: url.Values{"filename": {"plan.jmx"}}},
	{method: "GET", pattern: "/api/scenarios/{scenario_id}/requests", decision: "scenario:read"},
	{method: "POST", pattern: "/api/scenarios/{scenario_id}/requests/validate", decision: "scenario:update"},
	{method: "PUT", pattern: "/api/scenarios/{scenario_id}/requests", decision: "scenario:update"},

	{method: "POST", pattern: "/api/executions", decision: "execution:create",
		form: url.Values{"name": {"e"}, "project_id": {"{project_id}"}}},
	{method: "GET", pattern: "/api/executions", decision: decisionScopedList},
	{method: "GET", pattern: "/api/executions/{execution_id}", decision: "execution:read"},
	{method: "DELETE", pattern: "/api/executions/{execution_id}", decision: "execution:delete"},
	{method: "GET", pattern: "/api/executions/{execution_id}/files", decision: "execution:read"},
	{method: "PUT", pattern: "/api/executions/{execution_id}/files", decision: "execution:update"},
	{method: "DELETE", pattern: "/api/executions/{execution_id}/files", decision: "execution:delete",
		query: url.Values{"filename": {"data.csv"}}},
	{method: "PUT", pattern: "/api/executions/{execution_id}/config", decision: "execution:update"},
	{method: "GET", pattern: "/api/executions/{execution_id}/config", decision: "execution:read"},

	{method: "POST", pattern: "/api/executions/{execution_id}/deploy", decision: "run:create"},
	{method: "POST", pattern: "/api/executions/{execution_id}/trigger", decision: "run:create"},
	{method: "POST", pattern: "/api/executions/{execution_id}/stop", decision: "run:update"},
	{method: "POST", pattern: "/api/executions/{execution_id}/purge", decision: "run:delete"},
	{method: "GET", pattern: "/api/executions/{execution_id}/status", decision: "execution:read"},
	{method: "GET", pattern: "/api/executions/{execution_id}/engines", decision: "execution:read"},
	{method: "GET", pattern: "/api/executions/{execution_id}/scenarios/{scenario_id}/logs", decision: "execution:read"},
	{method: "GET", pattern: "/api/executions/{execution_id}/stream", decision: "execution:read"},

	{method: "POST", pattern: "/api/executions/{execution_id}/schedules", decision: "schedule:create"},
	{method: "GET", pattern: "/api/executions/{execution_id}/schedules", decision: "execution:read"},
	{method: "DELETE", pattern: "/api/executions/{execution_id}/schedules/{schedule_id}", decision: "execution:delete"},

	{method: "GET", pattern: "/api/executions/{execution_id}/reports", decision: "report:read"},
	{method: "GET", pattern: "/api/executions/{execution_id}/trend", decision: "report:read"},
	{method: "GET", pattern: "/api/executions/{execution_id}/error-signatures", decision: "report:read"},
	{method: "GET", pattern: "/api/runs/{run_id}/report", decision: "report:read"},
	{method: "GET", pattern: "/api/runs/{run_id}/series", decision: "report:read"},
	{method: "GET", pattern: "/api/runs/{run_id}/export", decision: "report:read",
		query: url.Values{"format": {"json"}}},
	{method: "GET", pattern: "/api/runs/{run_id}/scenarios/{scenario_id}/shards/{shard}/log", decision: "report:read"},
	{method: "GET", pattern: "/api/runs/{run_id}/scenarios/{scenario_id}/shards/{shard}/config", decision: "report:read"},

	// Phase 34: issuing, listing and revoking share links is report-read
	// material (the same grant that shows the report); the public fetch
	// answers to the token alone.
	{method: "POST", pattern: "/api/runs/{run_id}/share", decision: "report:read"},
	{method: "GET", pattern: "/api/runs/{run_id}/share", decision: "report:read"},
	{method: "DELETE", pattern: "/api/runs/{run_id}/share/{token}", decision: "report:read"},
	{method: "GET", pattern: "/api/share/{token}", decision: decisionPublic},

	{method: "GET", pattern: "/api/usage/history", decision: decisionSystemAdmin},
	{method: "GET", pattern: "/api/usage/summary", decision: decisionSystemAdmin},

	{method: "GET", pattern: "/api/admin/executions", decision: decisionSystemAdmin},
	{method: "GET", pattern: "/api/admin/nodes", decision: decisionSystemAdmin},
	{method: "POST", pattern: "/api/admin/abort", decision: decisionSystemAdmin},

	{method: "POST", pattern: "/api/tenants", decision: "tenant:admin"},
	{method: "GET", pattern: "/api/tenants", decision: "tenant:admin"},
	{method: "GET", pattern: "/api/tenants/{tenant_id}", decision: "tenant:admin"},
	{method: "PATCH", pattern: "/api/tenants/{tenant_id}", decision: "tenant:admin"},
	{method: "PUT", pattern: "/api/tenants/{tenant_id}/quota", decision: "tenant:admin"},
	{method: "GET", pattern: "/api/tenants/{tenant_id}/quota", decision: "tenant:admin"},
	{method: "GET", pattern: "/api/tenants/{tenant_id}/reservations", decision: "schedule:list"},
	{method: "GET", pattern: "/api/tenants/{tenant_id}/roles", decision: "tenant:admin"},
	{method: "POST", pattern: "/api/tenants/{tenant_id}/roles", decision: "tenant:admin"},
	{method: "DELETE", pattern: "/api/tenants/{tenant_id}/roles", decision: "tenant:admin",
		query: url.Values{"subject": {"x"}, "role": {"tenant_editor"}}},
	{method: "POST", pattern: "/api/roles", decision: "tenant:admin",
		form: url.Values{"subject": {"x"}, "role": {"tenant_editor"}}},
	{method: "DELETE", pattern: "/api/roles", decision: "tenant:admin",
		query: url.Values{"subject": {"x"}, "role": {"tenant_editor"}}},

	{method: "POST", pattern: "/api/clusters", decision: decisionSystemAdmin,
		form: url.Values{"name": {"edge"}}},
	{method: "GET", pattern: "/api/clusters", decision: decisionSystemAdmin},
	{method: "POST", pattern: "/api/clusters/{name}/rotate-ingest-token", decision: decisionSystemAdmin},
	{method: "GET", pattern: "/api/clusters/{name}", decision: decisionSystemAdmin},
	{method: "PUT", pattern: "/api/clusters/{name}", decision: decisionSystemAdmin},
	{method: "DELETE", pattern: "/api/clusters/{name}", decision: decisionSystemAdmin},

	{method: "POST", pattern: "/api/tenants/{tenant_id}/campaigns", decision: "campaign:create"},
	{method: "GET", pattern: "/api/tenants/{tenant_id}/campaigns", decision: "campaign:list"},
	{method: "GET", pattern: "/api/campaigns", decision: decisionScopedList},
	{method: "GET", pattern: "/api/campaigns/{campaign_id}", decision: "campaign:read"},
	{method: "PUT", pattern: "/api/campaigns/{campaign_id}", decision: "campaign:update",
		form: url.Values{"name": {"Supersale 12.12"}, "window_start": {"2030-01-01T00:00:00Z"},
			"window_end":           {"2030-01-02T00:00:00Z"},
			"service_project_id":   {"{project_id}"},
			"service_execution_id": {"{execution_id}"}}},
	{method: "POST", pattern: "/api/campaigns/{campaign_id}/abort", decision: "campaign:delete"},
	{method: "GET", pattern: "/api/campaigns/{campaign_id}/verdict", decision: "campaign:read|project:read"},
	{method: "GET", pattern: "/api/campaigns/{campaign_id}/comparison", decision: "campaign:read|project:read"},

	{method: "POST", pattern: "/api/calibrations", decision: "execution:create",
		form: url.Values{"project_id": {"{project_id}"}, "name": {"calib"}, "engine": {"jmeter"},
			"criterion": {"failures>5%"}, "cpu": {"1"}, "memory": {"512Mi"}}},
	{method: "POST", pattern: "/api/executions/{execution_id}/calibration/trigger", decision: "execution:create"},
	{method: "GET", pattern: "/api/calibrations/{job_id}", decision: "execution:read"},
	{method: "GET", pattern: "/api/scenarios/{scenario_id}/capacity-profile", decision: "scenario:read"},
	{method: "GET", pattern: "/api/scenarios/{scenario_id}/capacity-profile/fanout", decision: "scenario:read"},

	{method: "GET", pattern: "/api/files/{kind}/{id}/{name}", decision: "scenario:read|execution:read"},

	{method: "POST", pattern: "/api/ingest", decision: decisionPublic},
}

// auditSeed holds the ids every probe path substitutes, created over HTTP
// by the admin so each route can reach its authorization check against a
// real row.
type auditSeed struct {
	tenant1, tenant2, tenant3                        int64
	projectID, scenarioID, executionID, scheduleID   int64
	runID, campaignID, calibrationExecutionID, jobID int64
	// shareToken is a live share link for runID (phase 34): the public
	// report fetch's probe resolves it.
	shareToken string
}

// seedAuditFixture provisions one of everything a probe path can name, all
// inside tenant 1.
func seedAuditFixture(t *testing.T, f *rbacFixture) auditSeed {
	t.Helper()
	s := auditSeed{}
	s.tenant1 = createTenant(t, f, "acme", "Acme")
	s.tenant2 = createTenant(t, f, "globex", "Globex")
	s.tenant3 = createTenant(t, f, "initech", "Initech")

	s.projectID = createProjectInTenantReturningID(t, f, "acme-web", "team-a", s.tenant1)
	s.scenarioID = decodeID(t, f.req(t, http.MethodPost, "/api/scenarios", "admin-tok",
		url.Values{"name": {"smoke"}, "project_id": {strconv.FormatInt(s.projectID, 10)}}))
	s.executionID = decodeID(t, f.req(t, http.MethodPost, "/api/executions", "admin-tok",
		url.Values{"name": {"peak"}, "project_id": {strconv.FormatInt(s.projectID, 10)}}))

	// The load profile a schedule create reads to reserve quota.
	configYAML := "multi-test:\n  collectionid: " + strconv.FormatInt(s.executionID, 10) +
		"\n  tests:\n    - testid: " + strconv.FormatInt(s.scenarioID, 10) +
		"\n      concurrency: 10\n      rampup: 1\n      engines: 2\n      duration: 30\n"
	if rec := putMultipartAuth(t, f, "/api/executions/"+strconv.FormatInt(s.executionID, 10)+"/config",
		"admin-tok", "config.yaml", configYAML); rec.Code != http.StatusOK {
		t.Fatalf("upload config = %d (%s)", rec.Code, rec.Body.String())
	}

	fireAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	s.scheduleID = decodeID(t, f.req(t, http.MethodPost,
		"/api/executions/"+strconv.FormatInt(s.executionID, 10)+"/schedules", "admin-tok",
		url.Values{"tenant_id": {strconv.FormatInt(s.tenant1, 10)}, "kind": {"one_shot"}, "fire_at": {fireAt}}))

	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	s.campaignID = decodeID(t, f.req(t, http.MethodPost,
		"/api/tenants/"+strconv.FormatInt(s.tenant1, 10)+"/campaigns", "admin-tok",
		url.Values{"name": {"Supersale"}, "window_start": {start}, "window_end": {end},
			"service_project_id":   {strconv.FormatInt(s.projectID, 10)},
			"service_execution_id": {strconv.FormatInt(s.executionID, 10)}}))

	calibRec := f.req(t, http.MethodPost, "/api/calibrations", "admin-tok",
		url.Values{"project_id": {strconv.FormatInt(s.projectID, 10)}, "name": {"calib"}, "engine": {"jmeter"},
			"criterion": {"failures>5%"}, "cpu": {"1"}, "memory": {"512Mi"}})
	if calibRec.Code != http.StatusCreated {
		t.Fatalf("create calibration = %d (%s)", calibRec.Code, calibRec.Body.String())
	}
	var calibCreated struct {
		ExecutionID int64 `json:"execution_id"`
	}
	if err := json.Unmarshal(calibRec.Body.Bytes(), &calibCreated); err != nil {
		t.Fatalf("decode calibration: %v", err)
	}
	s.calibrationExecutionID = calibCreated.ExecutionID
	s.jobID = decodeID(t, f.req(t, http.MethodPost,
		"/api/executions/"+strconv.FormatInt(s.calibrationExecutionID, 10)+"/calibration/trigger", "admin-tok", nil))

	// A finished run and its report, so run-keyed report routes resolve a
	// real owning execution.
	ctx := context.Background()
	var err error
	if s.runID, err = f.store.StartRun(ctx, s.executionID, ""); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	now := time.Now().UTC()
	if err := f.reports.SaveReport(ctx, report.Report{
		ExecutionID: s.executionID, RunID: s.runID, Outcome: taurus.OutcomePassed,
		StartedAt: now, EndedAt: now,
	}); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	// A live share link for that run, seeded the way the issue endpoint
	// would (a fixed token reads better in probe failures than a minted one).
	s.shareToken = strings.Repeat("7", 64)
	if err := f.shares.CreateShare(ctx, s.runID, s.shareToken, "admin", nil); err != nil {
		t.Fatalf("seed share link: %v", err)
	}
	return s
}

// path substitutes the seeded ids into a route pattern.
func (s auditSeed) path(pattern string) string {
	r := strings.NewReplacer(
		"{tenant_id}", strconv.FormatInt(s.tenant1, 10),
		"{project_id}", strconv.FormatInt(s.projectID, 10),
		"{scenario_id}", strconv.FormatInt(s.scenarioID, 10),
		"{execution_id}", strconv.FormatInt(s.executionID, 10),
		"{schedule_id}", strconv.FormatInt(s.scheduleID, 10),
		"{run_id}", strconv.FormatInt(s.runID, 10),
		"{campaign_id}", strconv.FormatInt(s.campaignID, 10),
		"{job_id}", strconv.FormatInt(s.jobID, 10),
		"{shard}", "0",
		"{name}", "home",
		"{kind}", "scenario",
		"{id}", strconv.FormatInt(s.scenarioID, 10),
		"{token}", s.shareToken,
	)
	return r.Replace(pattern)
}

// TestAuthzAuditCoversRoutes is AC3's enforcement: every route must carry an
// explicit decision, and every decision must name a real route. A new route
// without an entry fails here the moment it is registered.
func TestAuthzAuditCoversRoutes(t *testing.T) {
	t.Parallel()

	table := make(map[string]authzEntry, len(authzAuditTable))
	for _, e := range authzAuditTable {
		key := e.method + " " + e.pattern
		if _, dup := table[key]; dup {
			t.Errorf("duplicate audit entry for %q", key)
		}
		table[key] = e
	}

	for _, r := range httpapi.Routes() {
		key := r.Method + " " + r.Pattern
		e, ok := table[key]
		if !ok {
			t.Errorf("route %q has no authorization entry in authzAuditTable", key)
			continue
		}
		switch e.decision {
		case decisionPublic, decisionSystemAdmin, decisionScopedList, decisionAuthed:
			// vocabulary constants
		default:
			if !validResourceAction(e.decision) {
				t.Errorf("%q: decision %q is not a known decision", key, e.decision)
			}
		}
	}
	for key := range table {
		found := false
		for _, r := range httpapi.Routes() {
			if r.Method+" "+r.Pattern == key {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("audit entry %q matches no registered route", key)
		}
	}
}

// validResourceAction checks a "res:action" decision against the rbac
// vocabulary, so a typo in the table fails loudly instead of silently
// probing nothing.
func validResourceAction(decision string) bool {
	for _, alt := range strings.Split(decision, "|") {
		res, action, ok := strings.Cut(alt, ":")
		if !ok {
			return false
		}
		switch res {
		case "project", "execution", "scenario", "run", "tenant", "campaign", "schedule", "report":
		default:
			return false
		}
		switch rbacAction(action) {
		case true:
		default:
			return false
		}
	}
	return true
}

// rbacAction reports whether action is one of rbac's action strings.
func rbacAction(action string) bool {
	switch action {
	case "create", "read", "update", "delete", "list", "admin":
		return true
	}
	return false
}

// TestAuthzAuditRejectsUngrantedCaller probes every entry with an
// authenticated account holding no role anywhere ("nobody"). Public routes
// must answer without user credentials; scoped lists must not leak the
// seeded tenant's rows; everything else must be a 403 reached before any
// data leaves the handler.
func TestAuthzAuditRejectsUngrantedCaller(t *testing.T) {
	t.Parallel()

	f := newRBACFixture(t)
	seed := seedAuditFixture(t, f)
	f.prov.Register("nobody-tok", account.Account{Subject: "nobody"})

	for _, e := range authzAuditTable {
		t.Run(e.method+" "+e.pattern+" -> "+e.decision, func(t *testing.T) {
			if e.pending != "" {
				t.Skipf("RED until %s gates this route (Phase 20 Block C)", e.pending)
			}
			path := seed.path(e.pattern)
			if len(e.query) > 0 {
				path += "?" + e.query.Encode()
			}
			switch e.decision {
			case decisionPublic:
				probePublic(t, f, e, path, seed)
			case decisionScopedList:
				probeScopedList(t, f, e, path, seed)
			case decisionAuthed:
				// Any authenticated account may call it -- even one holding
				// no grant anywhere.
				rec := probeRequest(t, f, e, path, "nobody-tok", seed)
				if rec.Code != http.StatusOK {
					t.Fatalf("authenticated-but-ungranted caller got %d, want 200 (%s)", rec.Code, rec.Body.String())
				}
			default:
				rec := probeRequest(t, f, e, path, "nobody-tok", seed)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("ungranted caller got %d, want 403 (%s)", rec.Code, rec.Body.String())
				}
			}
		})
	}
}

// probeRequest builds and serves one probe request. The context is
// pre-cancelled so a route that would block (the SSE stream) returns
// immediately in its ungated state instead of hanging the audit. {id}
// markers in form fields and multipart fields are substituted with the
// seeded ids, same as the path.
func probeRequest(t *testing.T, f *rbacFixture, e authzEntry, path, tok string, seed auditSeed) *httptest.ResponseRecorder {
	t.Helper()

	sub := func(v string) string {
		r := strings.NewReplacer(
			"{project_id}", strconv.FormatInt(seed.projectID, 10),
			"{execution_id}", strconv.FormatInt(seed.executionID, 10),
			"{scenario_id}", strconv.FormatInt(seed.scenarioID, 10),
			"{tenant_id}", strconv.FormatInt(seed.tenant1, 10),
		)
		return r.Replace(v)
	}

	var body string
	var contentType string
	if e.multipart != nil {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, v := range e.multipart {
			if err := mw.WriteField(k, sub(v)); err != nil {
				t.Fatalf("WriteField: %v", err)
			}
		}
		fw, err := mw.CreateFormFile("file", e.file)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := fw.Write([]byte("<?xml version=\"1.0\"?>")); err != nil {
			t.Fatalf("write file: %v", err)
		}
		_ = mw.Close()
		body = buf.String()
		contentType = mw.FormDataContentType()
	} else if e.form != nil {
		form := make(url.Values, len(e.form))
		for k, vs := range e.form {
			form[k] = make([]string, len(vs))
			for i, v := range vs {
				form[k][i] = sub(v)
			}
		}
		body = form.Encode()
		contentType = "application/x-www-form-urlencoded"
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: bounded even against a blocking handler
	r := httptest.NewRequest(e.method, path, strings.NewReader(body)).WithContext(ctx)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, r)
	return rec
}

// probePublic asserts the route answers without user credentials:
// /healthz and /metrics with 200; /api/ingest with its own credential
// rejection -- proving the user middleware exempted it (its 401 says
// "invalid ingest credentials", the middleware's says "unauthenticated");
// the demo-session endpoints with a minted/expired cookie. POST /api/session
// additionally proves the security-critical cookie attributes: HttpOnly,
// SameSite=Strict, Secure, Max-Age=8h (spec Approach E).
func probePublic(t *testing.T, f *rbacFixture, e authzEntry, path string, seed auditSeed) {
	t.Helper()
	switch e.pattern {
	case "/healthz", "/metrics":
		rec := probeRequest(t, f, e, path, "", auditSeed{})
		if rec.Code != http.StatusOK {
			t.Fatalf("public route got %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
	case "/api/ingest":
		r := httptest.NewRequest(http.MethodPost, path,
			strings.NewReader(`{"execution_id":`+strconv.FormatInt(seed.executionID, 10)+`}`))
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("ingest without credentials got %d, want its own 401 (%s)", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "unauthenticated") {
			t.Fatalf("ingest was rejected by the user middleware, not its own credential check: %s", rec.Body.String())
		}
	case "/api/session":
		if e.method == http.MethodPost {
			r := httptest.NewRequest(http.MethodPost, path,
				strings.NewReader(`{"profile":"auditor"}`))
			r.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			f.router.ServeHTTP(rec, r)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("POST /api/session got %d, want 204 (%s)", rec.Code, rec.Body.String())
			}
			assertSessionCookie(t, rec.Header().Values("Set-Cookie"), int(session.TTL.Seconds()))
		} else {
			r := httptest.NewRequest(http.MethodDelete, path, nil)
			rec := httptest.NewRecorder()
			f.router.ServeHTTP(rec, r)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("DELETE /api/session got %d, want 204 (%s)", rec.Code, rec.Body.String())
			}
			// Expired: Max-Age=0, so the browser drops it immediately.
			assertSessionCookie(t, rec.Header().Values("Set-Cookie"), 0)
		}
	case "/api/session/profiles":
		r := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/session/profiles got %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "auditor") {
			t.Fatalf("profiles list missing the configured persona: %s", rec.Body.String())
		}
	case "/api/share/{token}":
		// The share fetch serves the linked run's report with no credentials
		// at all -- that is the feature. A bare 200 is not enough: the body
		// must be the seeded run's report, or the route would be public
		// without being the share fetch.
		r := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/share/{token} got %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"run_id":`+strconv.FormatInt(seed.runID, 10)) {
			t.Fatalf("share fetch body is not the seeded run's report: %s", rec.Body.String())
		}
	default:
		t.Fatalf("no public probe defined for %q", e.pattern)
	}
}

// assertSessionCookie pins the security-critical Set-Cookie attributes:
// HttpOnly, SameSite=Strict, Secure, and the wanted Max-Age. These are the
// constraint the spec states verbatim (Approach E), so the audit asserts
// them, not just a 2xx.
func assertSessionCookie(t *testing.T, cookies []string, wantMaxAge int) {
	t.Helper()
	if len(cookies) == 0 {
		t.Fatal("no Set-Cookie header")
	}
	c := cookies[0]
	for _, want := range []string{
		session.CookieName + "=",
		"Path=/",
		"HttpOnly",
		"SameSite=Strict",
		"Secure",
		"Max-Age=" + strconv.Itoa(wantMaxAge),
	} {
		if !strings.Contains(c, want) {
			t.Fatalf("Set-Cookie %q missing %q", c, want)
		}
	}
}

// probeScopedList asserts rule C1's negative: a caller with no grant sees
// an empty list, never another tenant's rows.
func probeScopedList(t *testing.T, f *rbacFixture, e authzEntry, path string, seed auditSeed) {
	t.Helper()
	rec := probeRequest(t, f, e, path, "nobody-tok", seed)
	if rec.Code != http.StatusOK {
		t.Fatalf("scoped list got %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	for _, leak := range []string{"acme-web", "peak", "Supersale"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Fatalf("scoped list leaked %q to an ungranted caller: %s", leak, rec.Body.String())
		}
	}
}

// TestAuthzAuditPendingListIs shrinking: nothing depends on this at runtime;
// it exists so `go test -run TestAuthzAuditPending -v` prints the committed
// red list in one place.
func TestAuthzAuditPendingList(t *testing.T) {
	t.Parallel()
	for _, e := range authzAuditTable {
		if e.pending == "" {
			continue
		}
		t.Logf("RED %s %s -> %s (until %s)", e.method, e.pattern, e.decision, e.pending)
	}
}
