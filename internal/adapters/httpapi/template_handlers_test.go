package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// Phase 65 handler tests: the template catalog (GET /api/templates) and
// instantiate (POST /api/scenarios/{id}/instantiate), exercised through the
// fake-backed router the same way every other route is (legacy no-auth mode;
// the RBAC gates are the audit's business).

const templateFragment = "default-address: https://httpbin.org\nrequests:\n    - method: GET\n      url: /get\n"

type templateRouter struct {
	handler    http.Handler
	store      *fake.Store
	templateID int64
	ordinaryID int64
	projectID  int64
}

// newTemplateRouter seeds one project (owner matching DefaultOwners, the
// legacy create path's whole decision), one template with the baseline
// fragment, and one ordinary scenario in that project. Returns the ids the
// tests address.
func newTemplateRouter(t *testing.T) templateRouter {
	t.Helper()
	store := fake.NewStore()
	obj := fake.NewObjectStore()

	ctx := context.Background()
	pid, err := store.CreateProject(ctx, project.Project{Name: "tests-p", Owner: "honryu"})
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}

	tpl, err := scenario.NewTemplate("HTTPbin baseline", "httpbin-baseline")
	if err != nil {
		t.Fatalf("seed NewTemplate: %v", err)
	}
	tid, err := store.CreateScenario(ctx, tpl)
	if err != nil {
		t.Fatalf("seed template: %v", err)
	}
	if err := store.SetScenarioRequests(ctx, tid, []byte(templateFragment)); err != nil {
		t.Fatalf("seed template fragment: %v", err)
	}

	svc := scenarioapp.NewService(store, obj)
	ord, err := svc.Create(ctx, "smoke", pid)
	if err != nil {
		t.Fatalf("seed ordinary scenario: %v", err)
	}

	handler := httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(store),
		Scenarios:     svc,
		Store:         obj,
		DefaultOwners: []string{"honryu"},
	})
	return templateRouter{handler: handler, store: store, templateID: tid, ordinaryID: ord.ID, projectID: pid}
}

func (tr templateRouter) doJSON(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	tr.handler.ServeHTTP(rec, req)
	return rec
}

func TestListTemplates_ReturnsOnlyTemplates(t *testing.T) {
	t.Parallel()
	tr := newTemplateRouter(t)

	rec := tr.doJSON(t, http.MethodGet, "/api/templates", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var got []struct {
		ID           int64  `json:"id"`
		Name         string `json:"name"`
		IsTemplate   bool   `json:"is_template"`
		TemplateName string `json:"template_name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v (%s)", err, rec.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("templates = %+v, want exactly the one template (the ordinary scenario must not appear)", got)
	}
	if got[0].ID != tr.templateID || got[0].Name != "HTTPbin baseline" {
		t.Errorf("template = %+v, want the seeded httpbin-baseline row", got[0])
	}
	if !got[0].IsTemplate || got[0].TemplateName != "httpbin-baseline" {
		t.Errorf("is_template=%v template_name=%q, want true/httpbin-baseline", got[0].IsTemplate, got[0].TemplateName)
	}
}

func TestListTemplates_EmptyCatalogIsAnArray(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	handler := httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(store),
		Scenarios:     scenarioapp.NewService(store, obj),
		Store:         obj,
		DefaultOwners: []string{"honryu"},
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/templates", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// Go marshals a nil slice as null; the catalog must be a real array so
	// the picker can map it without a null guard.
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("empty catalog body = %q, want []", body)
	}
}

func TestInstantiateTemplate_ClonesAndAppliesTargetOverride(t *testing.T) {
	t.Parallel()
	tr := newTemplateRouter(t)

	body := `{"name":"checkout-baseline","project_id":` + id(tr.projectID) + `,"overrides":{"target_url":"http://checkout.svc"}}`
	rec := tr.doJSON(t, http.MethodPost, "/api/scenarios/"+id(tr.templateID)+"/instantiate", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var created struct {
		ID           int64  `json:"id"`
		Name         string `json:"name"`
		ProjectID    int64  `json:"project_id"`
		IsTemplate   bool   `json:"is_template"`
		TemplateName string `json:"template_name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode body: %v (%s)", err, rec.Body.String())
	}
	if created.ID == 0 || created.ID == tr.templateID {
		t.Fatalf("created id = %d, want a fresh scenario id", created.ID)
	}
	if created.Name != "checkout-baseline" || created.ProjectID != tr.projectID {
		t.Errorf("created = %+v, want the requested name and project", created)
	}
	if created.IsTemplate || created.TemplateName != "" {
		t.Errorf("clone is_template=%v template_name=%q, want an ordinary scenario", created.IsTemplate, created.TemplateName)
	}

	// The override landed in the clone's fragment: default-address rewritten
	// and the request carried over (the override path re-marshals the parsed
	// fragment, so key order is the struct's, not the template's original --
	// the semantically identical YAML, not the bytes).
	req := httptest.NewRequest(http.MethodGet, "/api/scenarios/"+id(created.ID)+"/requests", nil)
	gotRec := httptest.NewRecorder()
	tr.handler.ServeHTTP(gotRec, req)
	if gotRec.Code != http.StatusOK {
		t.Fatalf("clone requests status = %d, want 200", gotRec.Code)
	}
	if got := gotRec.Body.String(); got != "default-address: http://checkout.svc\nrequests:\n    - url: /get\n      method: GET\n" {
		t.Errorf("clone fragment = %q, want the rewritten fragment", got)
	}

	// The template's own fragment is untouched (read straight from the
	// store: the template itself has no project, so the scenario-scoped
	// HTTP read is not the lens for it in legacy mode).
	tplRaw, err := tr.store.GetScenarioRequests(context.Background(), tr.templateID)
	if err != nil {
		t.Fatalf("template fragment read: %v", err)
	}
	if string(tplRaw) != templateFragment {
		t.Errorf("template fragment mutated: %q", tplRaw)
	}
}

func TestInstantiateTemplate_WithoutOverrideClonesVerbatim(t *testing.T) {
	t.Parallel()
	tr := newTemplateRouter(t)

	rec := tr.doJSON(t, http.MethodPost, "/api/scenarios/"+id(tr.templateID)+"/instantiate",
		`{"name":"plain","project_id":`+id(tr.projectID)+`}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	gotRec := httptest.NewRecorder()
	tr.handler.ServeHTTP(gotRec, httptest.NewRequest(http.MethodGet, "/api/scenarios/"+id(created.ID)+"/requests", nil))
	if got := gotRec.Body.String(); got != templateFragment {
		t.Errorf("clone fragment = %q, want the template's verbatim %q", got, templateFragment)
	}
}

func TestInstantiateTemplate_Errors(t *testing.T) {
	t.Parallel()
	tr := newTemplateRouter(t)
	path := "/api/scenarios/" + id(tr.templateID) + "/instantiate"

	t.Run("ordinary scenario is a 409", func(t *testing.T) {
		t.Parallel()
		rec := tr.doJSON(t, http.MethodPost, "/api/scenarios/"+id(tr.ordinaryID)+"/instantiate",
			`{"name":"x","project_id":`+id(tr.projectID)+`}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown template is a 404", func(t *testing.T) {
		t.Parallel()
		rec := tr.doJSON(t, http.MethodPost, "/api/scenarios/99999/instantiate",
			`{"name":"x","project_id":`+id(tr.projectID)+`}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("malformed body is a 400", func(t *testing.T) {
		t.Parallel()
		rec := tr.doJSON(t, http.MethodPost, path, `{not json`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("missing name is a 400", func(t *testing.T) {
		t.Parallel()
		rec := tr.doJSON(t, http.MethodPost, path, `{"project_id":`+id(tr.projectID)+`}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown project is a 404", func(t *testing.T) {
		t.Parallel()
		// The create-side gate loads the target project before instantiating,
		// so a clone into nowhere is a 404, never a half-created scenario.
		rec := tr.doJSON(t, http.MethodPost, path, `{"name":"x","project_id":424242}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
		}
	})
}

// id renders an int64 id for interpolation into a path or JSON body.
func id(v int64) string {
	return strconv.FormatInt(v, 10)
}
