package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/digestapp"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/domain/account"
	"github.com/heridotlife/honryu/internal/domain/digest"
	"github.com/heridotlife/honryu/internal/domain/rbac"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// newDigestRouter builds a full router over a caller-owned store, for the
// list test that seeds digest rows directly.
func newDigestRouter(t *testing.T, store *fake.Store, obj *fake.ObjectStore) http.Handler {
	t.Helper()
	return httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(store),
		Executions:    executionapp.NewService(store, obj, 100),
		Digests:       digestapp.NewService(store),
		Store:         obj,
		DefaultOwners: []string{"honryu"},
	})
}

// TestDigestConfigUpsertRoundtrip pins the phase 42 configuration contract:
// PUT upserts and answers the stored config; GET reads it back; a bad
// period word is a 400 with the stated reason; DELETE removes it (and a
// second DELETE 404s); GET after DELETE is the 404 that means "off".
func TestDigestConfigUpsertRoundtrip(t *testing.T) {
	t.Parallel()
	h := newFullRouter(t)
	projectID := createProjectForWebhooks(t, h, "digests")

	rec := putForm(t, h, "/api/projects/"+itoa(projectID)+"/digest", url.Values{"period": {"daily"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("put digest config = %d (%s)", rec.Code, rec.Body.String())
	}
	var cfg struct {
		ProjectID int64  `json:"project_id"`
		Period    string `json:"period"`
		Enabled   bool   `json:"enabled"`
		LastFired string `json:"last_fired"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode config: %v (%s)", err, rec.Body.String())
	}
	if cfg.ProjectID != projectID || cfg.Period != "daily" || !cfg.Enabled {
		t.Errorf("config = %+v, want daily/enabled for the project", cfg)
	}
	if cfg.LastFired != "" {
		t.Errorf("last_fired = %q on a fresh config, want absent", cfg.LastFired)
	}

	// Switching period is the same upsert path.
	rec = putForm(t, h, "/api/projects/"+itoa(projectID)+"/digest", url.Values{"period": {"weekly"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("put weekly = %d (%s)", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode weekly config: %v", err)
	}
	if cfg.Period != "weekly" {
		t.Errorf("period after switch = %q, want weekly", cfg.Period)
	}

	rec = do(t, h, http.MethodGet, "/api/projects/"+itoa(projectID)+"/digest")
	if rec.Code != http.StatusOK {
		t.Fatalf("get config = %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"weekly"`) {
		t.Errorf("get config body = %s, want the stored weekly", rec.Body.String())
	}

	// The period grammar is two words; anything else is the caller's
	// mistake, stated as such.
	rec = putForm(t, h, "/api/projects/"+itoa(projectID)+"/digest", url.Values{"period": {"hourly"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("put hourly = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "daily or weekly") {
		t.Errorf("400 body = %s, want the stated reason", rec.Body.String())
	}

	rec = do(t, h, http.MethodDelete, "/api/projects/"+itoa(projectID)+"/digest")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete config = %d, want 204", rec.Code)
	}
	if rec := do(t, h, http.MethodDelete, "/api/projects/"+itoa(projectID)+"/digest"); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/projects/"+itoa(projectID)+"/digest"); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want the 404 that means off", rec.Code)
	}
}

// TestDigestConfigNoScheduleIsAResource404 pins the anti-leak contract the
// phase 42 anomaly note flagged: a GET for a project with no schedule is a
// 404 whose message names the resource ("no digest schedule for this
// project"), not the internal repo sentinel ("ports: not found") -- the UI
// reads this 404 as the off state, so the message doubles as user-facing
// copy. The configured GET's 200 shape is the roundtrip test's contract;
// here it is re-asserted with a stamped last_fired so the omitempty field's
// present side is pinned too (the stamp rides the store's claim path -- the
// same mark the sweeper's fire leaves behind).
func TestDigestConfigNoScheduleIsAResource404(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	h := newDigestRouter(t, store, fake.NewObjectStore())
	projectID := createProjectForWebhooks(t, h, "digest-404")

	rec := do(t, h, http.MethodGet, "/api/projects/"+itoa(projectID)+"/digest")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get with no schedule = %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no digest schedule for this project") {
		t.Errorf("404 body = %s, want the resource's own message", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "ports") {
		t.Errorf("404 body = %s, must not leak the internal sentinel", rec.Body.String())
	}

	rec = putForm(t, h, "/api/projects/"+itoa(projectID)+"/digest", url.Values{"period": {"daily"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("put config = %d (%s)", rec.Code, rec.Body.String())
	}
	fired := time.Now().Add(-time.Hour).Truncate(time.Second)
	if _, ok, err := store.ClaimDueDigestSchedule(context.Background(), fired); err != nil || !ok {
		t.Fatalf("claim due = (%v, %v), want the fresh schedule claimed", ok, err)
	}
	rec = do(t, h, http.MethodGet, "/api/projects/"+itoa(projectID)+"/digest")
	if rec.Code != http.StatusOK {
		t.Fatalf("get config = %d (%s)", rec.Code, rec.Body.String())
	}
	var cfg struct {
		ProjectID int64      `json:"project_id"`
		Period    string     `json:"period"`
		Enabled   bool       `json:"enabled"`
		LastFired *time.Time `json:"last_fired"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode config: %v (%s)", err, rec.Body.String())
	}
	if cfg.ProjectID != projectID || cfg.Period != "daily" || !cfg.Enabled {
		t.Errorf("config = %+v, want daily/enabled for the project", cfg)
	}
	if cfg.LastFired == nil || !cfg.LastFired.Equal(fired) {
		t.Errorf("last_fired = %v, want the claimed fire stamp %v", cfg.LastFired, fired)
	}
}

// TestDigestListScopedNewestFirstAndLimited: the feed serves only the
// project's rows, newest first, with the payload's verdict fields decoded
// and limit honored (default 10, capped at 100).
func TestDigestListScopedNewestFirstAndLimited(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	h := newDigestRouter(t, store, obj)
	projectID := createProjectForWebhooks(t, h, "feed")
	otherID := createProjectForWebhooks(t, h, "other")

	ctx := context.Background()
	base := time.Unix(1700_000_000, 0).UTC()
	var newestID int64
	for i := 0; i < 3; i++ {
		payload, err := json.Marshal(digestapp.Payload{
			Event: digestapp.EventDigest, ProjectID: projectID, Period: digest.PeriodDaily,
			WindowStart: base.Add(time.Duration(i) * 24 * time.Hour),
			WindowEnd:   base.Add(time.Duration(i+1) * 24 * time.Hour),
			RunsTotal:   i + 1,
		})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		id, err := store.SaveDigest(ctx, digest.Digest{
			ProjectID: projectID, Period: digest.PeriodDaily,
			WindowStart: base, WindowEnd: base.Add(24 * time.Hour),
			Payload: payload,
		})
		if err != nil {
			t.Fatalf("SaveDigest: %v", err)
		}
		newestID = id
	}
	// Another project's digest must not leak into the feed.
	foreign, err := json.Marshal(digestapp.Payload{Event: digestapp.EventDigest, ProjectID: otherID})
	if err != nil {
		t.Fatalf("marshal foreign: %v", err)
	}
	if _, err := store.SaveDigest(ctx, digest.Digest{
		ProjectID: otherID, Period: digest.PeriodDaily, Payload: foreign,
	}); err != nil {
		t.Fatalf("SaveDigest(foreign): %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/projects/"+itoa(projectID)+"/digests")
	if rec.Code != http.StatusOK {
		t.Fatalf("list digests = %d (%s)", rec.Code, rec.Body.String())
	}
	var rows []struct {
		ID        int64 `json:"id"`
		RunsTotal int   `json:"runs_total"`
		ByOutcome struct {
			Passed  int `json:"passed"`
			Failed  int `json:"failed"`
			Aborted int `json:"aborted"`
		} `json:"by_outcome"`
		Executions []struct {
			Name string `json:"name"`
		} `json:"executions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode rows: %v (%s)", err, rec.Body.String())
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (the project's own only)", len(rows))
	}
	if rows[0].ID != newestID {
		t.Errorf("first row id = %d, want the newest %d (newest first)", rows[0].ID, newestID)
	}
	if rows[2].RunsTotal != 1 || rows[1].RunsTotal != 2 || rows[0].RunsTotal != 3 {
		t.Errorf("runs_total order = %d,%d,%d, want payload order 1,2,3 newest-first", rows[2].RunsTotal, rows[1].RunsTotal, rows[0].RunsTotal)
	}

	// ?limit=1 truncates.
	if rec := do(t, h, http.MethodGet, "/api/projects/"+itoa(projectID)+"/digests?limit=1"); rec.Code != http.StatusOK {
		t.Fatalf("list limit 1 = %d", rec.Code)
	} else {
		var limited []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &limited); err != nil {
			t.Fatalf("decode limited: %v", err)
		}
		if len(limited) != 1 {
			t.Errorf("limited rows = %d, want 1", len(limited))
		}
	}
}

// TestRBAC_DigestConfigRequiresProjectUpdate: administering what a project
// broadcasts (the digest schedule) is a project update, not a read -- a
// tenant viewer may read the configuration and the feed but may not PUT or
// DELETE one.
func TestRBAC_DigestConfigRequiresProjectUpdate(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)
	acme := createTenant(t, f, "acme2", "Acme2")
	f.prov.Register("view-tok", account.Account{Subject: "viewer"})
	assignRole(t, f, acme, "viewer", rbac.RoleTenantViewer)
	rec := f.req(t, http.MethodPost, "/api/projects", "admin-tok",
		url.Values{"name": {"acme-digests"}, "owner": {"team-a"}, "tenant_id": {strconv.FormatInt(acme, 10)}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project = %d (%s)", rec.Code, rec.Body.String())
	}
	var proj struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &proj); err != nil {
		t.Fatalf("decode project: %v", err)
	}

	path := "/api/projects/" + strconv.FormatInt(proj.ID, 10) + "/digest"
	// Viewer reads: configuration (404 = off) and feed are open.
	if rec := f.req(t, http.MethodGet, path, "view-tok", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("viewer get config = %d, want 404 (nothing configured)", rec.Code)
	}
	if rec := f.req(t, http.MethodGet, path+"s", "view-tok", nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer list digests = %d, want 200", rec.Code)
	}
	// Viewer writes: forbidden, both directions of the toggle.
	if rec := f.req(t, http.MethodPut, path, "view-tok", url.Values{"period": {"daily"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer put config = %d, want 403", rec.Code)
	}
	if rec := f.req(t, http.MethodDelete, path, "view-tok", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer delete config = %d, want 403", rec.Code)
	}
}
