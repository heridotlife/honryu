// Phase 80's handler pins: the three scenario-version endpoints through the
// real router with the fake store -- list/get/restore happy paths, the
// unknown-version 404s, and the restore-append law as the API sees it: the
// pre-restore state becomes the newest version and no earlier version's
// snapshot ever changes.
package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

func newVersioningRouter(t *testing.T) (http.Handler, *fake.Store) {
	t.Helper()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	scenarios := scenarioapp.NewService(store, obj).WithVersions(store)
	h := httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(store),
		Scenarios:     scenarios,
		Store:         obj,
		DefaultOwners: []string{"honryu"},
	})
	return h, store
}

func getVersioning(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func postEmpty(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decode %s: %v (%s)", out, err, rec.Body.String())
	}
}

// seedVersionedScenario drives the API itself -- create the project and the
// scenario, save a fragment, overwrite it -- so the history the pins read is
// the one the write path actually captured. Returns the scenario id.
func seedVersionedScenario(t *testing.T, h http.Handler) int64 {
	t.Helper()
	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	scenarioID := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"audited"}, "project_id": {itoa(projectID)}}))

	frag1 := url.Values{}
	req := httptest.NewRequest(http.MethodPut, "/api/scenarios/"+itoa(scenarioID)+"/requests",
		origStringReader("default-address: http://example.com\nrequests:\n  - url: /one\n"))
	req.Header.Set("Content-Type", "text/yaml")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT requests v1: %d (%s)", rec.Code, rec.Body.String())
	}
	_ = frag1

	frag2 := httptest.NewRequest(http.MethodPut, "/api/scenarios/"+itoa(scenarioID)+"/requests",
		origStringReader("default-address: http://example.com\nrequests:\n  - url: /two\n"))
	frag2.Header.Set("Content-Type", "text/yaml")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, frag2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("PUT requests v2: %d (%s)", rec2.Code, rec2.Body.String())
	}
	return scenarioID
}

func origStringReader(s string) *segReader { return &segReader{s: s} }

// segReader is a tiny io.Reader over a string, named to stay out of the
// way of anything a shared test helper already defines.
type segReader struct{ s string }

func (r *segReader) Read(p []byte) (int, error) {
	if len(r.s) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.s)
	r.s = r.s[n:]
	return n, nil
}

func TestScenarioVersions_ListGetRestore(t *testing.T) {
	h, _ := newVersioningRouter(t)
	scenarioID := seedVersionedScenario(t, h)
	base := "/api/scenarios/" + itoa(scenarioID)

	// List: creation (v1) plus the two fragment saves, newest first.
	rec := getVersioning(t, h, base+"/versions")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET versions: %d (%s)", rec.Code, rec.Body.String())
	}
	var list []struct {
		ID        int64   `json:"id"`
		Version   int     `json:"version"`
		CreatedBy *string `json:"created_by"`
	}
	decodeJSON(t, rec, &list)
	if len(list) != 3 || list[0].Version != 3 || list[1].Version != 2 || list[2].Version != 1 {
		t.Fatalf("list = %+v, want v3,v2,v1 newest first", list)
	}
	// No-auth mode: the actor is null, honestly unknown.
	for _, row := range list {
		if row.CreatedBy != nil {
			t.Errorf("v%d created_by = %q, want null (no principal in legacy mode)", row.Version, *row.CreatedBy)
		}
	}

	// Detail: v1's snapshot carries the shape at creation -- no requests.
	rec = getVersioning(t, h, base+"/versions/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET version 1: %d (%s)", rec.Code, rec.Body.String())
	}
	var v1 struct {
		Version  int `json:"version"`
		Snapshot struct {
			Name     string   `json:"name"`
			Requests string   `json:"requests"`
			Data     []string `json:"data"`
		} `json:"snapshot"`
	}
	decodeJSON(t, rec, &v1)
	if v1.Version != 1 || v1.Snapshot.Name != "audited" || v1.Snapshot.Requests != "" {
		t.Errorf("v1 = %+v, want the creation-time shape", v1)
	}
	if v1.Snapshot.Data == nil {
		t.Error("v1 data is null; the wire shape promises an array")
	}
}

func TestScenarioVersions_RestoreAppendsAndRewinds(t *testing.T) {
	h, _ := newVersioningRouter(t)
	scenarioID := seedVersionedScenario(t, h)
	base := "/api/scenarios/" + itoa(scenarioID)

	// The v1 snapshot before the restore: the baseline the append-only law
	// protects.
	var v1Before struct {
		Snapshot struct {
			Requests string `json:"requests"`
		} `json:"snapshot"`
	}
	decodeJSON(t, getVersioning(t, h, base+"/versions/1"), &v1Before)

	// Restore to version 1: the fragment rewinds away.
	rec := postEmpty(t, h, base+"/versions/1/restore")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST restore: %d (%s)", rec.Code, rec.Body.String())
	}
	var res struct {
		Message      string `json:"message"`
		RestoredFrom int    `json:"restored_from"`
		Version      int    `json:"version"`
	}
	decodeJSON(t, rec, &res)
	if res.RestoredFrom != 1 {
		t.Errorf("restored_from = %d, want 1", res.RestoredFrom)
	}
	if res.Version != 4 {
		t.Errorf("version = %d, want 4 (the pre-restore capture)", res.Version)
	}

	// The append-only law, through the API: the pre-restore state (with
	// fragment /two) is now the NEWEST version, and v1 is unchanged.
	var v4 struct {
		Snapshot struct {
			Requests string `json:"requests"`
		} `json:"snapshot"`
	}
	decodeJSON(t, getVersioning(t, h, base+"/versions/4"), &v4)
	if v4.Snapshot.Requests != "default-address: http://example.com\nrequests:\n  - url: /two\n" {
		t.Errorf("v4 requests = %q, want the pre-restore fragment", v4.Snapshot.Requests)
	}
	var v1After struct {
		Snapshot struct {
			Requests string `json:"requests"`
		} `json:"snapshot"`
	}
	decodeJSON(t, getVersioning(t, h, base+"/versions/1"), &v1After)
	if v1After.Snapshot.Requests != v1Before.Snapshot.Requests {
		t.Errorf("v1 changed across the restore: %q -> %q", v1Before.Snapshot.Requests, v1After.Snapshot.Requests)
	}

	// The live scenario is back to v1's shape: no stored fragment.
	live := getVersioning(t, h, base+"/requests")
	if live.Code != http.StatusNotFound {
		t.Errorf("GET requests after restore = %d, want 404 (fragment rewound away)", live.Code)
	}

	// The list grew by one and never shrank or reordered.
	var list []struct {
		Version int `json:"version"`
	}
	decodeJSON(t, getVersioning(t, h, base+"/versions"), &list)
	if len(list) != 4 || list[0].Version != 4 {
		t.Errorf("list = %+v, want v4..v1 (append-only)", list)
	}
}

func TestScenarioVersions_UnknownVersionIs404(t *testing.T) {
	h, _ := newVersioningRouter(t)
	scenarioID := seedVersionedScenario(t, h)
	base := "/api/scenarios/" + itoa(scenarioID)

	if rec := getVersioning(t, h, base+"/versions/9"); rec.Code != http.StatusNotFound {
		t.Errorf("GET version 9 = %d (%s), want 404", rec.Code, rec.Body.String())
	}
	if rec := postEmpty(t, h, base+"/versions/9/restore"); rec.Code != http.StatusNotFound {
		t.Errorf("POST restore 9 = %d (%s), want 404", rec.Code, rec.Body.String())
	}
	// A failed restore records nothing: history is untouched.
	var list []struct {
		Version int `json:"version"`
	}
	decodeJSON(t, getVersioning(t, h, base+"/versions"), &list)
	if len(list) != 3 {
		t.Errorf("list after failed restore = %+v, want the same three versions", list)
	}
}

func TestScenarioVersions_NonNumericVersionIs400(t *testing.T) {
	h, _ := newVersioningRouter(t)
	scenarioID := seedVersionedScenario(t, h)
	base := "/api/scenarios/" + itoa(scenarioID)

	if rec := getVersioning(t, h, base+"/versions/notanumber"); rec.Code != http.StatusBadRequest {
		t.Errorf("GET version notanumber = %d, want 400 (caller mistake, not a lookup miss)", rec.Code)
	}
}

func TestScenarioVersions_UnwiredStoreIs404(t *testing.T) {
	// A router whose scenario service carries no version store answers the
	// version endpoints with the optional-service 404, before anything else.
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	h := httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(store),
		Scenarios:     scenarioapp.NewService(store, obj),
		Store:         obj,
		DefaultOwners: []string{"honryu"},
	})
	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	scenarioID := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"s"}, "project_id": {itoa(projectID)}}))
	base := "/api/scenarios/" + itoa(scenarioID)

	if rec := getVersioning(t, h, base+"/versions"); rec.Code != http.StatusNotFound {
		t.Errorf("GET versions unwired = %d, want 404", rec.Code)
	}
}
func TestScenarioVersions_BadIDAndUnknownRestore(t *testing.T) {
	h, _ := newVersioningRouter(t)

	// Non-numeric scenario id -> 400, never a 500.
	if rec := getVersioning(t, h, "/api/scenarios/notanumber/versions"); rec.Code != http.StatusBadRequest {
		t.Errorf("list bad id = %d, want 400", rec.Code)
	}
	// Valid shape, nonexistent scenario -> the store's not-found mapping.
	if rec := getVersioning(t, h, "/api/scenarios/999/versions"); rec.Code != http.StatusNotFound {
		t.Errorf("list unknown scenario = %d, want 404", rec.Code)
	}
	if rec := getVersioning(t, h, "/api/scenarios/999/versions/1"); rec.Code != http.StatusNotFound {
		t.Errorf("get unknown scenario = %d, want 404", rec.Code)
	}

	// Restore on a real scenario but unknown version -> 404.
	sid := seedVersionedScenario(t, h)
	if rec := postEmpty(t, h, "/api/scenarios/"+itoa(sid)+"/versions/99/restore"); rec.Code != http.StatusNotFound {
		t.Errorf("restore unknown version = %d (%s), want 404", rec.Code, rec.Body.String())
	}
	// Non-numeric version segment -> 400.
	if rec := postEmpty(t, h, "/api/scenarios/"+itoa(sid)+"/versions/x/restore"); rec.Code != http.StatusBadRequest {
		t.Errorf("restore bad version = %d, want 400", rec.Code)
	}
}
