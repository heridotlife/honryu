package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/digestapp"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/app/webhookapp"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

func newFullRouter(t *testing.T) http.Handler {
	t.Helper()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	return httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(store),
		Scenarios:     scenarioapp.NewService(store, obj),
		Executions:    executionapp.NewService(store, obj, 100),
		Webhooks:      webhookapp.NewService(store),
		Digests:       digestapp.NewService(store),
		Store:         obj,
		DefaultOwners: []string{"honryu"},
	})
}

func postForm(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func putMultipart(t *testing.T, h http.Handler, path, filename, content string) *httptest.ResponseRecorder {
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

	req := httptest.NewRequest(http.MethodPut, path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeID(t *testing.T, rec *httptest.ResponseRecorder) int64 {
	t.Helper()
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode id: %v (%s)", err, rec.Body.String())
	}
	return out.ID
}

func TestProjectScenarioExecutionFlow(t *testing.T) {
	t.Parallel()
	h := newFullRouter(t)

	// Create a project.
	rec := postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project = %d (%s)", rec.Code, rec.Body.String())
	}
	projectID := decodeID(t, rec)

	// Create a scenario under the project.
	rec = postForm(t, h, "/api/scenarios", url.Values{"name": {"smoke"}, "project_id": {itoa(projectID)}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create scenario = %d (%s)", rec.Code, rec.Body.String())
	}
	scenarioID := decodeID(t, rec)

	// Upload a JMX test file to the scenario.
	rec = putMultipart(t, h, "/api/scenarios/"+itoa(scenarioID)+"/files", "scenario.jmx", "<jmx/>")
	if rec.Code != http.StatusOK {
		t.Fatalf("upload scenario file = %d (%s)", rec.Code, rec.Body.String())
	}

	// Scenario detail now reports the test file.
	rec = do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID))
	if rec.Code != http.StatusOK {
		t.Fatalf("get scenario = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "scenario.jmx") {
		t.Fatalf("scenario detail missing test file: %s", rec.Body.String())
	}

	// Download the uploaded artifact.
	rec = do(t, h, http.MethodGet, "/api/files/scenario/"+itoa(scenarioID)+"/scenario.jmx")
	if rec.Code != http.StatusOK || rec.Body.String() != "<jmx/>" {
		t.Fatalf("download = %d %q", rec.Code, rec.Body.String())
	}

	// Create a execution and store its execution config.
	rec = postForm(t, h, "/api/executions", url.Values{"name": {"peak"}, "project_id": {itoa(projectID)}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create execution = %d (%s)", rec.Code, rec.Body.String())
	}
	collID := decodeID(t, rec)

	configYAML := fmt.Sprintf(`multi-test:
  collectionid: %d
  csv_split: true
  tests:
    - testid: %d
      concurrency: 10
      rampup: 1
      engines: 2
      duration: 60
`, collID, scenarioID)
	rec = putMultipart(t, h, "/api/executions/"+itoa(collID)+"/config", "config.yaml", configYAML)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload config = %d (%s)", rec.Code, rec.Body.String())
	}

	// The execution now reports its execution scenario.
	rec = do(t, h, http.MethodGet, "/api/executions/"+itoa(collID))
	if rec.Code != http.StatusOK {
		t.Fatalf("get execution = %d", rec.Code)
	}
	var coll struct {
		LoadProfile []struct {
			ScenarioID int64 `json:"scenario_id"`
			Engines    int   `json:"engines"`
		} `json:"load_profile"`
		CSVSplit bool `json:"csv_split"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &coll); err != nil {
		t.Fatalf("decode execution: %v", err)
	}
	if len(coll.LoadProfile) != 1 || coll.LoadProfile[0].ScenarioID != scenarioID || coll.LoadProfile[0].Engines != 2 {
		t.Fatalf("execution plans = %+v", coll.LoadProfile)
	}
	if !coll.CSVSplit {
		t.Fatalf("csv_split not persisted")
	}

	// The scenario is now in use: deleting it is a 409.
	rec = do(t, h, http.MethodDelete, "/api/scenarios/"+itoa(scenarioID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete in-use scenario = %d, want 409", rec.Code)
	}

	// Deleting the project while it has children is a 409.
	rec = do(t, h, http.MethodDelete, "/api/projects/"+itoa(projectID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete non-empty project = %d, want 409", rec.Code)
	}
}

func TestCreateProject_ForbiddenOwner(t *testing.T) {
	t.Parallel()
	h := newFullRouter(t)
	rec := postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"someone-else"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create project (foreign owner) = %d, want 403", rec.Code)
	}
}

func TestCreateScenario_InvalidName_400(t *testing.T) {
	t.Parallel()
	h := newFullRouter(t)
	rec := postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}})
	projectID := decodeID(t, rec)

	rec = postForm(t, h, "/api/scenarios", url.Values{"name": {""}, "project_id": {itoa(projectID)}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create scenario (empty name) = %d, want 400", rec.Code)
	}
}

func TestConfigUpload_EngineLimit_400(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	h := httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(store),
		Scenarios:     scenarioapp.NewService(store, obj),
		Executions:    executionapp.NewService(store, obj, 1), // limit of 1 engine
		Store:         obj,
		DefaultOwners: []string{"honryu"},
	})

	pr := postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}})
	projectID := decodeID(t, pr)
	pl := postForm(t, h, "/api/scenarios", url.Values{"name": {"smoke"}, "project_id": {itoa(projectID)}})
	scenarioID := decodeID(t, pl)
	cl := postForm(t, h, "/api/executions", url.Values{"name": {"peak"}, "project_id": {itoa(projectID)}})
	collID := decodeID(t, cl)

	configYAML := fmt.Sprintf("multi-test:\n  collectionid: %d\n  tests:\n    - testid: %d\n      concurrency: 1\n      rampup: 1\n      engines: 5\n      duration: 60\n", collID, scenarioID)
	rec := putMultipart(t, h, "/api/executions/"+itoa(collID)+"/config", "config.yaml", configYAML)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("config over engine limit = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// TestListExecutions covers GET /api/executions: only the caller's projects'
// executions come back, newest first, and an operator with no projects sees
// an empty list rather than every execution.
func TestListExecutions(t *testing.T) {
	t.Parallel()
	h := newFullRouter(t)

	// One visible project with three executions; ordering is what the list
	// contract pins here (scoping itself is pinned at the repository
	// contract, which runs against the fake and real MySQL alike).
	rec := postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project = %d (%s)", rec.Code, rec.Body.String())
	}

	mkExec := func(name string) int64 {
		rec := postForm(t, h, "/api/executions", url.Values{"name": {name}, "project_id": {"1"}})
		if rec.Code != http.StatusCreated {
			t.Fatalf("create execution %q = %d (%s)", name, rec.Code, rec.Body.String())
		}
		return decodeID(t, rec)
	}
	first := mkExec("first")
	mkExec("middle")
	last := mkExec("last")

	rec = do(t, h, http.MethodGet, "/api/executions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	var got []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		ProjectID int64  `json:"project_id"`
		Kind      string `json:"kind"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v (%s)", err, rec.Body.String())
	}
	if len(got) != 3 {
		t.Fatalf("executions = %+v, want all three in newest-first order", got)
	}
	if got[0].ID != last || got[2].ID != first {
		t.Fatalf("order = [%d %d %d], want newest first (last, middle, first)", got[0].ID, got[1].ID, got[2].ID)
	}
	// Phase 39: the summary carries Kind too, so list consumers can tell
	// calibration executions apart without fetching each one.
	for _, e := range got {
		if e.Kind != "normal" {
			t.Errorf("execution %d kind = %q, want normal (created the ordinary way)", e.ID, e.Kind)
		}
	}

	// Empty ownership: no visible projects, empty list -- never a 500 or a
	// full dump. (Router built with a different owner set.)
	emptyRouter := httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(fake.NewStore()),
		Executions:    executionapp.NewService(fake.NewStore(), fake.NewObjectStore(), 100),
		DefaultOwners: []string{"nobody"},
	})
	rec = do(t, emptyRouter, http.MethodGet, "/api/executions")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty status = %d", rec.Code)
	}
	var none []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &none); err != nil || len(none) != 0 {
		t.Fatalf("empty list = %v err=%v, want []", none, err)
	}
}

// TestGetExecutionIncludesKind pins phase 39's wire contract on the
// single-execution GET: the response exposes the execution's Kind
// ("normal" vs "calibrate_engine") so the SPA can gate calibration UI on
// it -- before, the frontend saw only the engine and mounted the Capacity
// card on ordinary executions, whose Calibrate button could only ever
// earn a 400 from calibrationapp.Trigger.
func TestGetExecutionIncludesKind(t *testing.T) {
	t.Parallel()
	// newCalibrationRouter (calibration_handlers_test.go) wires the
	// Calibrations dep, so both kinds can be created over HTTP.
	h, store, _ := newCalibrationRouter(t)
	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))

	kindOf := func(executionID int64) string {
		t.Helper()
		rec := do(t, h, http.MethodGet, "/api/executions/"+itoa(executionID))
		if rec.Code != http.StatusOK {
			t.Fatalf("get execution %d = %d (%s)", executionID, rec.Code, rec.Body.String())
		}
		var got struct {
			Engine string `json:"engine"`
			Kind   string `json:"kind"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode execution %d: %v (%s)", executionID, err, rec.Body.String())
		}
		return got.Kind
	}

	normalID := decodeID(t, postForm(t, h, "/api/executions", url.Values{
		"name": {"soak"}, "project_id": {itoa(projectID)}, "engine": {"jmeter"},
	}))
	if kind := kindOf(normalID); kind != "normal" {
		t.Errorf("ordinary execution kind = %q, want normal", kind)
	}

	_, calibID, _ := seedCalibration(t, h, store)
	if kind := kindOf(calibID); kind != "calibrate_engine" {
		t.Errorf("calibration execution kind = %q, want calibrate_engine", kind)
	}
}

// TestGetScenarioRequests covers GET /api/scenarios/{id}/requests: the
// fragment comes back byte-for-byte as text/yaml, 404 when nothing was ever
// uploaded, and 409 for a native scenario.
func TestGetScenarioRequests(t *testing.T) {
	t.Parallel()
	h := newFullRouter(t)

	// Project + portable scenario.
	rec := postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project = %d (%s)", rec.Code, rec.Body.String())
	}
	rec = postForm(t, h, "/api/scenarios", url.Values{"name": {"checkout"}, "project_id": {"1"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create scenario = %d (%s)", rec.Code, rec.Body.String())
	}
	scenarioID := decodeID(t, rec)

	// Nothing uploaded yet -> 404.
	if rec := do(t, h, http.MethodGet, "/api/scenarios/"+strconv.FormatInt(scenarioID, 10)+"/requests"); rec.Code != http.StatusNotFound {
		t.Fatalf("no-upload status = %d (%s), want 404", rec.Code, rec.Body.String())
	}

	// Upload a fragment with a comment, unusual key order, and an unmodelled
	// key -- exactly what a byte-preserving round trip must keep.
	fragment := "# hand-written header comment\n" +
		"think-time: 2s  # unmodelled key\n" +
		"default-address: http://target.local\n" +
		"requests:\n" +
		"- url: /cart\n" +
		"  method: POST\n"
	if rec := putMultipart(t, h, "/api/scenarios/"+strconv.FormatInt(scenarioID, 10)+"/requests", "requests.yaml", fragment); rec.Code != http.StatusOK {
		t.Fatalf("upload = %d (%s)", rec.Code, rec.Body.String())
	}

	// Fetch: byte-identical, text/yaml content type.
	rec = do(t, h, http.MethodGet, "/api/scenarios/"+strconv.FormatInt(scenarioID, 10)+"/requests")
	if rec.Code != http.StatusOK {
		t.Fatalf("fetch = %d (%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/yaml") {
		t.Fatalf("Content-Type = %q, want text/yaml", ct)
	}
	if rec.Body.String() != fragment {
		t.Fatalf("round trip not byte-identical:\n got: %q\nwant: %q", rec.Body.String(), fragment)
	}

	// Native scenario -> 409, same stance as the PUT (import is multipart).
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "plan.jmx")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	plan := `<?xml version="1.0" encoding="UTF-8"?>
<jmeterTestPlan version="1.2" properties="5.0" jmeter="5.6.3">
  <hashTree>
    <TestPlan guiclass="TestPlanGui" testclass="TestPlan" testname="Checkout journey" enabled="true"/>
    <hashTree/>
  </hashTree>
</jmeterTestPlan>`
	if _, err := fw.Write([]byte(plan)); err != nil {
		t.Fatalf("write jmx: %v", err)
	}
	if err := mw.WriteField("project_id", "1"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := mw.WriteField("name", "native-one"); err != nil {
		t.Fatalf("write name: %v", err)
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/scenarios/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("import = %d (%s)", rec.Code, rec.Body.String())
	}
	var importRes struct {
		Scenario struct {
			ID int64 `json:"id"`
		} `json:"scenario"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &importRes); err != nil {
		t.Fatalf("decode import response: %v (%s)", err, rec.Body.String())
	}
	nativeID := importRes.Scenario.ID
	if rec := do(t, h, http.MethodGet, "/api/scenarios/"+strconv.FormatInt(nativeID, 10)+"/requests"); rec.Code != http.StatusConflict {
		t.Fatalf("native fetch status = %d (%s), want 409", rec.Code, rec.Body.String())
	}

	// Unknown scenario -> 404.
	if rec := do(t, h, http.MethodGet, "/api/scenarios/424242/requests"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown scenario status = %d, want 404", rec.Code)
	}
}

// TestSetScenarioRequestsRawYAML covers the raw text/yaml body on the PUT:
// the bytes are stored verbatim, a G2 -> G3 -> G2 round trip returns them
// byte-identical (comments, key order, unmodelled keys), and multipart
// uploads keep working unchanged.
func TestSetScenarioRequestsRawYAML(t *testing.T) {
	t.Parallel()
	h := newFullRouter(t)

	rec := postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project = %d (%s)", rec.Code, rec.Body.String())
	}
	rec = postForm(t, h, "/api/scenarios", url.Values{"name": {"checkout"}, "project_id": {"1"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create scenario = %d (%s)", rec.Code, rec.Body.String())
	}
	scenarioID := strconv.FormatInt(decodeID(t, rec), 10)
	url := "/api/scenarios/" + scenarioID + "/requests"

	// Raw body with every byte-preservation trap: leading comment, unusual
	// key order, inline comment, unmodelled key, trailing newline.
	fragment := "# operator notes: bump think-time after warmup\n" +
		"think-time: 1.5s\n" +
		"default-address: https://target.local\n" +
		"requests:\n" +
		"- url: /login\n" +
		"  method: POST\n" +
		"- url: /cart\n"
	req := httptest.NewRequest(http.MethodPut, url, strings.NewReader(fragment))
	req.Header.Set("Content-Type", "text/yaml; charset=utf-8")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("raw put = %d (%s)", rec.Code, rec.Body.String())
	}

	// Round trip through G2: byte-identical, including the trailing newline.
	rec = do(t, h, http.MethodGet, url)
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != fragment {
		t.Fatalf("round trip not byte-identical:\n got: %q\nwant: %q", got, fragment)
	}

	// application/x-yaml is accepted too.
	alt := "requests:\n- url: /alt\n"
	req = httptest.NewRequest(http.MethodPut, url, strings.NewReader(alt))
	req.Header.Set("Content-Type", "application/x-yaml")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("x-yaml put = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := do(t, h, http.MethodGet, url).Body.String(); got != alt {
		t.Fatalf("x-yaml round trip = %q, want %q", got, alt)
	}

	// Multipart still works unchanged (the pre-existing path).
	mp := putMultipart(t, h, url, "requests.yaml", "requests:\n- url: /multipart\n")
	if mp.Code != http.StatusOK {
		t.Fatalf("multipart put = %d (%s)", mp.Code, mp.Body.String())
	}
	want := "requests:\n- url: /multipart\n"
	if got := do(t, h, http.MethodGet, url).Body.String(); got != want {
		t.Fatalf("multipart round trip = %q, want %q", got, want)
	}

	// Semantically invalid YAML via the raw path is rejected with the same
	// error semantics as the multipart path.
	bad := "requests: []"
	req = httptest.NewRequest(http.MethodPut, url, strings.NewReader(bad))
	req.Header.Set("Content-Type", "text/yaml")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty-requests put = %d (%s), want 400", rec.Code, rec.Body.String())
	}
}

// TestValidateRequestsParity is the G5 acceptance test: any fragment the
// validate endpoint accepts must be accepted by the store path, and any
// fragment it rejects must be rejected there too. Both endpoints share
// readRequestsBody and the scenarioapp validation path, so the pairs below
// prove the seam.
func TestValidateRequestsParity(t *testing.T) {
	t.Parallel()
	h := newFullRouter(t)

	rec := postForm(t, h, "/api/projects", url.Values{"name": {"parity"}, "owner": {"honryu"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project = %d (%s)", rec.Code, rec.Body.String())
	}
	rec = postForm(t, h, "/api/scenarios", url.Values{"name": {"parity"}, "project_id": {"1"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create scenario = %d (%s)", rec.Code, rec.Body.String())
	}
	scenarioID := strconv.FormatInt(decodeID(t, rec), 10)
	storeURL := "/api/scenarios/" + scenarioID + "/requests"
	validateURL := storeURL + "/validate"

	fixtures := map[string]struct {
		body  string
		valid bool
	}{
		"valid fragment": {
			body:  "requests:\n- url: /\n",
			valid: true,
		},
		"empty requests list": {
			body:  "requests: []\n",
			valid: false,
		},
		"missing requests key": {
			body:  "not_requests: true\n",
			valid: false,
		},
		"broken yaml": {
			body:  "requests: [unclosed\n",
			valid: false,
		},
	}

	for name, tc := range fixtures {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, validateURL, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "text/yaml")
			vrec := httptest.NewRecorder()
			h.ServeHTTP(vrec, req)

			req = httptest.NewRequest(http.MethodPut, storeURL, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "text/yaml")
			srec := httptest.NewRecorder()
			h.ServeHTTP(srec, req)

			if tc.valid {
				if vrec.Code != http.StatusOK {
					t.Fatalf("validate rejected valid fragment: %d (%s)", vrec.Code, vrec.Body.String())
				}
				if srec.Code != http.StatusOK {
					t.Fatalf("validate accepted but store rejected: %d (%s)", srec.Code, srec.Body.String())
				}
				return
			}
			if vrec.Code != http.StatusBadRequest {
				t.Fatalf("validate accepted invalid fragment: %d (%s)", vrec.Code, vrec.Body.String())
			}
			if srec.Code != http.StatusBadRequest {
				t.Fatalf("validate rejected but store accepted: %d (%s)", srec.Code, srec.Body.String())
			}
			var vEnv struct {
				Diagnostics []scenarioapp.Diagnostic `json:"diagnostics"`
			}
			if err := json.Unmarshal(vrec.Body.Bytes(), &vEnv); err != nil {
				t.Fatalf("validate response not diagnostics envelope: %v (%s)", err, vrec.Body.String())
			}
			if len(vEnv.Diagnostics) == 0 {
				t.Fatalf("validate rejection carried no diagnostics: %s", vrec.Body.String())
			}
		})
	}
}

// G7: a JSON body on PUT /executions/{id}/config carries the Profile
// directly (json tags of loadprofile.Profile) and reaches the same
// StoreConfig with the same validation as the historical multipart
// multi-test upload -- whose file format is unchanged. Proven by reading
// the stored config back through GET and by the shared error semantics.
func TestPutExecutionConfigJSON(t *testing.T) {
	t.Parallel()
	h := newFullRouter(t)

	rec := postForm(t, h, "/api/projects", url.Values{"name": {"cfgproj"}, "owner": {"honryu"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project = %d (%s)", rec.Code, rec.Body.String())
	}
	rec = postForm(t, h, "/api/executions", url.Values{"name": {"cfgexec"}, "project_id": {"1"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create execution = %d (%s)", rec.Code, rec.Body.String())
	}
	execID := strconv.FormatInt(decodeID(t, rec), 10)

	// A scenario must exist for the config to reference; the execution is
	// in project 1, so the scenario goes there too.
	rec = postForm(t, h, "/api/scenarios", url.Values{"name": {"cfgscen"}, "project_id": {"1"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create scenario = %d (%s)", rec.Code, rec.Body.String())
	}
	scenID := decodeID(t, rec)

	configURL := "/api/executions/" + execID + "/config"

	// JSON path: the Profile shape itself, not wrapped in multi-test.
	body := fmt.Sprintf(`{"name":"cfgexec","project_id":1,"execution_id":%s,"tests":[{"scenario_id":%d,"concurrency":5,"rampup":10,"duration":30,"engines":1}],"csv_split":false}`, execID, scenID)
	req := httptest.NewRequest(http.MethodPut, configURL, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("json config put = %d (%s)", rec.Code, rec.Body.String())
	}

	// Stored: GET returns the same profile the JSON carried.
	rec = do(t, h, http.MethodGet, configURL)
	if rec.Code != http.StatusOK {
		t.Fatalf("config get = %d (%s)", rec.Code, rec.Body.String())
	}

	// Validation parity: the JSON path enforces the same rules as the
	// multipart path -- zero tests is rejected with the service's error.
	badBody := fmt.Sprintf(`{"name":"cfgexec","project_id":1,"execution_id":%s,"tests":[],"csv_split":false}`, execID)
	req = httptest.NewRequest(http.MethodPut, configURL, strings.NewReader(badBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("json config put with zero tests accepted (%s)", rec.Body.String())
	}

	// Multipart path still works unchanged (multi-test YAML wrapper).
	mp := fmt.Sprintf("multi-test:\n  name: cfgexec\n  collectionid: %s\n  projectid: 1\n  tests:\n    - testid: %d\n      concurrency: 3\n      rampup: 5\n      duration: 10\n      engines: 1\n  csv_split: false\n", execID, scenID)
	mrec := putMultipart(t, h, configURL, "config.yaml", mp)
	if mrec.Code != http.StatusOK {
		t.Fatalf("multipart config put = %d (%s)", mrec.Code, mrec.Body.String())
	}
}
