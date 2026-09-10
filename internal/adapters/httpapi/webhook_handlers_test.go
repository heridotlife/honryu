package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// TestWebhookCreateListRoundtrip pins the phase 40 wire contract: a created
// webhook answers 201 with id/url/has_secret/enabled/created_by/
// created_time, the list returns the same shape, and the secret never
// appears in any response body -- write-only by construction.
func TestWebhookCreateListRoundtrip(t *testing.T) {
	t.Parallel()
	h := newFullRouter(t)

	projectID := createProjectForWebhooks(t, h, "web")

	rec := postForm(t, h, "/api/projects/"+itoa(projectID)+"/webhooks",
		url.Values{"url": {"https://hooks.example.com/runs"}, "secret": {"s3cr3t"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create webhook = %d (%s)", rec.Code, rec.Body.String())
	}
	var created struct {
		ID          int64  `json:"id"`
		URL         string `json:"url"`
		HasSecret   bool   `json:"has_secret"`
		Enabled     bool   `json:"enabled"`
		CreatedBy   string `json:"created_by"`
		CreatedTime string `json:"created_time"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v (%s)", err, rec.Body.String())
	}
	if created.URL != "https://hooks.example.com/runs" {
		t.Errorf("url = %q", created.URL)
	}
	if !created.HasSecret || !created.Enabled {
		t.Errorf("has_secret/enabled = %v/%v, want true/true", created.HasSecret, created.Enabled)
	}
	if created.CreatedTime == "" {
		t.Error("created_time missing from create response")
	}
	if strings.Contains(rec.Body.String(), "s3cr3t") {
		t.Fatalf("create response leaked the secret: %s", rec.Body.String())
	}

	rec = do(t, h, http.MethodGet, "/api/projects/"+itoa(projectID)+"/webhooks")
	if rec.Code != http.StatusOK {
		t.Fatalf("list webhooks = %d (%s)", rec.Code, rec.Body.String())
	}
	var listed []struct {
		ID        int64  `json:"id"`
		URL       string `json:"url"`
		HasSecret bool   `json:"has_secret"`
		Enabled   bool   `json:"enabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v (%s)", err, rec.Body.String())
	}
	if len(listed) != 1 || listed[0].ID != created.ID || !listed[0].HasSecret || !listed[0].Enabled {
		t.Fatalf("list = %+v, want the created webhook %d", listed, created.ID)
	}
	if strings.Contains(rec.Body.String(), "s3cr3t") {
		t.Fatalf("list response leaked the secret: %s", rec.Body.String())
	}
}

// TestCreateWebhookRequiresHTTPS: registration is https-only. A cleartext
// URL is rejected with 400 and the stated reason -- never silently accepted
// and quietly never delivered to.
func TestCreateWebhookRequiresHTTPS(t *testing.T) {
	t.Parallel()
	h := newFullRouter(t)
	projectID := createProjectForWebhooks(t, h, "web")

	for _, tc := range []struct {
		name, url, wantMsg string
	}{
		{"http url", "http://hooks.example.com/runs", "url must be https"},
		{"empty url", "", "url is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := postForm(t, h, "/api/projects/"+itoa(projectID)+"/webhooks", url.Values{"url": {tc.url}})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("create webhook = %d (%s), want 400", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantMsg) {
				t.Fatalf("error %q missing %q", rec.Body.String(), tc.wantMsg)
			}
		})
	}
}

// TestDeleteWebhookScopedToProject: a webhook id under the wrong project's
// path is a 404 (the store scopes every mutation by project), and the
// rightful project's delete answers 204 with an empty body.
func TestDeleteWebhookScopedToProject(t *testing.T) {
	t.Parallel()
	h := newFullRouter(t)
	projectA := createProjectForWebhooks(t, h, "a")
	projectB := createProjectForWebhooks(t, h, "b")

	rec := postForm(t, h, "/api/projects/"+itoa(projectA)+"/webhooks", url.Values{"url": {"https://a.example.com/hook"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create webhook = %d (%s)", rec.Code, rec.Body.String())
	}
	id := decodeID(t, rec)

	rec = do(t, h, http.MethodDelete, "/api/projects/"+itoa(projectB)+"/webhooks/"+itoa(id))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-project delete = %d (%s), want 404", rec.Code, rec.Body.String())
	}

	rec = do(t, h, http.MethodDelete, "/api/projects/"+itoa(projectA)+"/webhooks/"+itoa(id))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete webhook = %d (%s), want 204", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "" {
		t.Fatalf("delete body = %q, want empty", rec.Body.String())
	}

	// Gone from the list.
	rec = do(t, h, http.MethodGet, "/api/projects/"+itoa(projectA)+"/webhooks")
	if !strings.Contains(rec.Body.String(), "[]") || strings.Contains(rec.Body.String(), "a.example.com") {
		t.Fatalf("list after delete = %s, want empty array", rec.Body.String())
	}
}

// TestSetWebhookEnabled: the pause/resume toggle answers 200 with the
// standard message envelope, and the state lands (the next list shows it).
func TestSetWebhookEnabled(t *testing.T) {
	t.Parallel()
	h := newFullRouter(t)
	projectID := createProjectForWebhooks(t, h, "web")

	rec := postForm(t, h, "/api/projects/"+itoa(projectID)+"/webhooks", url.Values{"url": {"https://hooks.example.com/runs"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create webhook = %d (%s)", rec.Code, rec.Body.String())
	}
	id := decodeID(t, rec)

	rec = putForm(t, h, "/api/projects/"+itoa(projectID)+"/webhooks/"+itoa(id)+"/enabled", url.Values{"enabled": {"false"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("toggle webhook = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "updated") {
		t.Fatalf("toggle body = %s, want the updated message", rec.Body.String())
	}

	rec = do(t, h, http.MethodGet, "/api/projects/"+itoa(projectID)+"/webhooks")
	var listed []struct {
		ID      int64 `json:"id"`
		Enabled bool  `json:"enabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v (%s)", err, rec.Body.String())
	}
	if len(listed) != 1 || listed[0].Enabled {
		t.Fatalf("list after pause = %+v, want one paused webhook", listed)
	}

	// A non-boolean enabled value is the caller's mistake, not a toggle.
	rec = putForm(t, h, "/api/projects/"+itoa(projectID)+"/webhooks/"+itoa(id)+"/enabled", url.Values{"enabled": {"maybe"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("toggle with bad value = %d (%s), want 400", rec.Code, rec.Body.String())
	}
}

// TestWebhooksNotConfigured: a nil Webhooks dep disables the surface (404),
// the same contract every optional service follows, so a router built
// without the webhook service stays exactly as closed as before phase 40.
func TestWebhooksNotConfigured(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	h := httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(store),
		DefaultOwners: []string{"honryu"},
	})
	projectID := createProjectForWebhooks(t, h, "web")

	rec := do(t, h, http.MethodGet, "/api/projects/"+itoa(projectID)+"/webhooks")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("list webhooks without service = %d (%s), want 404", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "webhooks not configured") {
		t.Fatalf("body = %s, want the not-configured message", rec.Body.String())
	}
}

// createProjectForWebhooks provisions a project owned by the default
// no-auth owner, the way every phase 1 flow test does.
func createProjectForWebhooks(t *testing.T, h http.Handler, name string) int64 {
	t.Helper()
	rec := postForm(t, h, "/api/projects", url.Values{"name": {name}, "owner": {"honryu"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project = %d (%s)", rec.Code, rec.Body.String())
	}
	return decodeID(t, rec)
}
