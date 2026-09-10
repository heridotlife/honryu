package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/calibrationapp"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/capacityprofile"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

func newCalibrationRouter(t *testing.T) (h http.Handler, store *fake.Store, obj *fake.ObjectStore) {
	t.Helper()
	store = fake.NewStore()
	obj = fake.NewObjectStore()
	scenarios := scenarioapp.NewService(store, obj)
	calibrations := calibrationapp.NewService(store).WithFingerprint(scenarios)
	h = httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(store),
		Scenarios:     scenarios,
		Executions:    executionapp.NewService(store, obj, 100),
		Calibrations:  calibrations,
		Store:         obj,
		DefaultOwners: []string{"honryu"},
	})
	return h, store, obj
}

// seedCalibrationSource creates the ordinary execution a calibration
// binds its scenario from: an execution running scenarioID, seeded through
// the store (the config-upload HTTP path has its own tests).
func seedCalibrationSource(t *testing.T, h http.Handler, store *fake.Store, projectID, scenarioID int64) int64 {
	t.Helper()
	sourceID := decodeID(t, postForm(t, h, "/api/executions", url.Values{"name": {"peak"}, "project_id": {itoa(projectID)}}))
	entries := []loadprofile.Entry{{ScenarioID: scenarioID, Engines: 3, Concurrency: 40, Rampup: 15, Duration: 45, Throughput: 777}}
	if err := store.StoreLoadProfile(context.Background(), sourceID, false, entries); err != nil {
		t.Fatalf("StoreLoadProfile(source): %v", err)
	}
	return sourceID
}

// seedCalibration configures a fresh CalibrateEngine execution via the HTTP
// API and returns its execution id, bound at creation to scenarioID (copied
// from a source execution that runs it -- Create's own binding, phase 41),
// alongside the project and scenario ids, for the job/capacity-profile/
// fanout tests.
func seedCalibration(t *testing.T, h http.Handler, store *fake.Store) (projectID, executionID, scenarioID int64) {
	t.Helper()
	projectID = decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	scenarioID = decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"target"}, "project_id": {itoa(projectID)}}))
	sourceID := seedCalibrationSource(t, h, store, projectID, scenarioID)
	rec := postForm(t, h, "/api/calibrations", url.Values{
		"project_id": {itoa(projectID)}, "name": {"calib"}, "engine": {"jmeter"},
		"criterion": {"failures>5%"}, "cpu": {"1"}, "memory": {"512Mi"},
		"scenario_id": {itoa(scenarioID)}, "source_execution_id": {itoa(sourceID)},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create calibration = %d (%s)", rec.Code, rec.Body.String())
	}
	executionID = decodeExecutionID(t, rec)
	return projectID, executionID, scenarioID
}

// decodeExecutionID reads calibrationExecutionResponse's execution_id --
// unlike every other create endpoint's response, this one has no top-level
// "id" field, so the shared decodeID helper does not apply.
func decodeExecutionID(t *testing.T, rec *httptest.ResponseRecorder) int64 {
	t.Helper()
	var out struct {
		ExecutionID int64 `json:"execution_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode execution_id: %v (%s)", err, rec.Body.String())
	}
	return out.ExecutionID
}

func TestCreateCalibration_AdmitsAndReturnsCreated(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	scenarioID := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"target"}, "project_id": {itoa(projectID)}}))
	sourceID := seedCalibrationSource(t, h, store, projectID, scenarioID)

	rec := postForm(t, h, "/api/calibrations", url.Values{
		"project_id": {itoa(projectID)}, "name": {"calib"}, "engine": {"jmeter"},
		"criterion": {"failures>5%"}, "cpu": {"1"}, "memory": {"512Mi"},
		"seed_qps": {"5"}, "max_qps": {"500"}, "max_steps": {"10"}, "hold_seconds": {"15"},
		"scenario_id": {itoa(scenarioID)}, "source_execution_id": {itoa(sourceID)},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create calibration = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		ExecutionID int64   `json:"execution_id"`
		ProjectID   int64   `json:"project_id"`
		Engine      string  `json:"engine"`
		CPU         string  `json:"cpu"`
		Memory      string  `json:"memory"`
		Criterion   string  `json:"criterion"`
		SeedQPS     float64 `json:"seed_qps"`
		MaxQPS      float64 `json:"max_qps"`
		MaxSteps    int     `json:"max_steps"`
		HoldSeconds int     `json:"hold_seconds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.ExecutionID <= 0 || got.ProjectID != projectID || got.Engine != "jmeter" {
		t.Fatalf("calibration = %+v, want a real execution id under project %d", got, projectID)
	}
	if got.CPU != "1" || got.Memory != "512Mi" || got.Criterion != "failures>5%" {
		t.Fatalf("calibration pod/criterion = %+v", got)
	}
	if got.SeedQPS != 5 || got.MaxQPS != 500 || got.MaxSteps != 10 || got.HoldSeconds != 15 {
		t.Fatalf("calibration bounds = %+v, want the supplied overrides echoed back", got)
	}
}

func TestCreateCalibration_DefaultsUnsuppliedBounds(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	scenarioID := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"target"}, "project_id": {itoa(projectID)}}))
	sourceID := seedCalibrationSource(t, h, store, projectID, scenarioID)

	rec := postForm(t, h, "/api/calibrations", url.Values{
		"project_id": {itoa(projectID)}, "name": {"calib"}, "engine": {"jmeter"},
		"criterion": {"failures>5%"}, "cpu": {"1"}, "memory": {"512Mi"},
		"scenario_id": {itoa(scenarioID)}, "source_execution_id": {itoa(sourceID)},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create calibration = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		SeedQPS float64 `json:"seed_qps"`
		MaxQPS  float64 `json:"max_qps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SeedQPS != calibration.DefaultSeedQPS || got.MaxQPS != calibration.DefaultMaxQPS {
		t.Fatalf("bounds = %+v, want the defaulted values (SeedQPS %g, MaxQPS %g)", got, calibration.DefaultSeedQPS, calibration.DefaultMaxQPS)
	}
}

func TestCreateCalibration_RejectsAnInvalidSpec(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	scenarioID := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"target"}, "project_id": {itoa(projectID)}}))
	sourceID := seedCalibrationSource(t, h, store, projectID, scenarioID)

	// No criterion supplied.
	rec := postForm(t, h, "/api/calibrations", url.Values{
		"project_id": {itoa(projectID)}, "name": {"calib"}, "engine": {"jmeter"}, "cpu": {"1"}, "memory": {"512Mi"},
		"scenario_id": {itoa(scenarioID)}, "source_execution_id": {itoa(sourceID)},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create calibration (no criterion) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCreateCalibration_RejectsBadNumericOverride(t *testing.T) {
	t.Parallel()
	h, _, _ := newCalibrationRouter(t)
	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))

	rec := postForm(t, h, "/api/calibrations", url.Values{
		"project_id": {itoa(projectID)}, "name": {"calib"}, "engine": {"jmeter"},
		"criterion": {"failures>5%"}, "cpu": {"1"}, "memory": {"512Mi"}, "seed_qps": {"not-a-number"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create calibration (bad seed_qps) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCreateCalibration_RejectsInvalidProjectID(t *testing.T) {
	t.Parallel()
	h, _, _ := newCalibrationRouter(t)
	rec := postForm(t, h, "/api/calibrations", url.Values{
		"project_id": {"not-a-number"}, "name": {"calib"}, "engine": {"jmeter"},
		"criterion": {"failures>5%"}, "cpu": {"1"}, "memory": {"512Mi"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create calibration (bad project_id) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCreateCalibration_BindsTheScenarioAtCreation(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	scenarioID := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"target"}, "project_id": {itoa(projectID)}}))
	sourceID := seedCalibrationSource(t, h, store, projectID, scenarioID)

	rec := postForm(t, h, "/api/calibrations", url.Values{
		"project_id": {itoa(projectID)}, "name": {"calib"}, "engine": {"jmeter"},
		"criterion": {"failures>5%"}, "cpu": {"1"}, "memory": {"512Mi"},
		"seed_qps": {"25"}, "hold_seconds": {"10"},
		"scenario_id": {itoa(scenarioID)}, "source_execution_id": {itoa(sourceID)},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create calibration = %d (%s)", rec.Code, rec.Body.String())
	}
	executionID := decodeExecutionID(t, rec)

	// The execution_scenario row the whole phase exists for: without it a
	// triggered calibration died with run.ErrNoScenarios (execution 14).
	entries, err := store.LoadProfileFor(context.Background(), executionID)
	if err != nil {
		t.Fatalf("LoadProfileFor: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("LoadProfileFor = %+v, want exactly 1 entry (the bound scenario)", entries)
	}
	got := entries[0]
	if got.ScenarioID != scenarioID {
		t.Errorf("ScenarioID = %d, want %d", got.ScenarioID, scenarioID)
	}
	if got.Engines != 1 || got.Throughput != 25 {
		t.Errorf("Engines/Throughput = %d/%d, want 1/25 (one pod at the seed rate)", got.Engines, got.Throughput)
	}
	if got.Duration != 45 {
		t.Errorf("Duration = %d, want max(hold 10, source 45) = 45", got.Duration)
	}
}

func TestCreateCalibration_RequiresTheScenarioBinding(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	scenarioID := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"target"}, "project_id": {itoa(projectID)}}))
	sourceID := seedCalibrationSource(t, h, store, projectID, scenarioID)

	base := url.Values{
		"project_id": {itoa(projectID)}, "name": {"calib"}, "engine": {"jmeter"},
		"criterion": {"failures>5%"}, "cpu": {"1"}, "memory": {"512Mi"},
		"scenario_id": {itoa(scenarioID)}, "source_execution_id": {itoa(sourceID)},
	}
	with := func(mutate func(form url.Values)) url.Values {
		form := url.Values{}
		for k, v := range base {
			form[k] = v
		}
		mutate(form)
		return form
	}
	cases := []struct {
		name string
		form url.Values
	}{
		{"missing scenario_id", with(func(form url.Values) { form.Del("scenario_id") })},
		{"missing source_execution_id", with(func(form url.Values) { form.Del("source_execution_id") })},
		{"non-numeric scenario_id", with(func(form url.Values) { form.Set("scenario_id", "nan") })},
		{"non-numeric source_execution_id", with(func(form url.Values) { form.Set("source_execution_id", "nan") })},
		// A source that runs no entry for the scenario is the service's own
		// ErrSourceScenarioNotBound -- mapped to 400, not a 500.
		{"source does not run the scenario", with(func(form url.Values) { form.Set("scenario_id", itoa(scenarioID+1000)) })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if rec := postForm(t, h, "/api/calibrations", tc.form); rec.Code != http.StatusBadRequest {
				t.Fatalf("create calibration (%s) = %d, want 400 (%s)", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestTriggerCalibration_CreatesAJobAndReturnsIt(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	_, executionID, _ := seedCalibration(t, h, store)

	rec := do(t, h, http.MethodPost, "/api/executions/"+itoa(executionID)+"/calibration/trigger")
	if rec.Code != http.StatusCreated {
		t.Fatalf("trigger calibration = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		ID          int64  `json:"id"`
		ExecutionID int64  `json:"execution_id"`
		Phase       string `json:"phase"`
		StepCount   int    `json:"step_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.ID <= 0 || got.ExecutionID != executionID || got.Phase != "pending" || got.StepCount != 0 {
		t.Fatalf("triggered job = %+v, want a fresh pending job for execution %d", got, executionID)
	}
}

func TestTriggerCalibration_RejectsANonCalibrationExecution(t *testing.T) {
	t.Parallel()
	h, _, _ := newCalibrationRouter(t)
	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	ordinary := decodeID(t, postForm(t, h, "/api/executions", url.Values{"name": {"peak"}, "project_id": {itoa(projectID)}}))

	rec := do(t, h, http.MethodPost, "/api/executions/"+itoa(ordinary)+"/calibration/trigger")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("trigger (ordinary execution) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestTriggerCalibration_InvalidExecutionID_400(t *testing.T) {
	t.Parallel()
	h, _, _ := newCalibrationRouter(t)
	rec := do(t, h, http.MethodPost, "/api/executions/x/calibration/trigger")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("trigger (bad execution id) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestGetCalibrationJob_RoundTrips(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	_, executionID, _ := seedCalibration(t, h, store)
	jobID := decodeID(t, do(t, h, http.MethodPost, "/api/executions/"+itoa(executionID)+"/calibration/trigger"))

	rec := do(t, h, http.MethodGet, "/api/calibrations/"+itoa(jobID))
	if rec.Code != http.StatusOK {
		t.Fatalf("get calibration job = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		ID    int64  `json:"id"`
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != jobID || got.Phase != "pending" {
		t.Fatalf("job = %+v, want id %d phase pending", got, jobID)
	}
}

func TestGetCalibrationJob_MissingReturnsNotFound(t *testing.T) {
	t.Parallel()
	h, _, _ := newCalibrationRouter(t)
	rec := do(t, h, http.MethodGet, "/api/calibrations/999")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get missing job = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

func TestGetCalibrationJob_InvalidID_400(t *testing.T) {
	t.Parallel()
	h, _, _ := newCalibrationRouter(t)
	rec := do(t, h, http.MethodGet, "/api/calibrations/x")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("get job (bad id) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestGetCapacityProfile_RoundTrips(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	_, _, scenarioID := seedCalibration(t, h, store)
	ctx := context.Background()
	key := capacityprofile.Key{ScenarioID: scenarioID, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi"}
	if err := store.UpsertCapacityProfile(ctx, capacityprofile.CapacityProfile{
		Key: key, PerPodQPS: 42, SaturatedBy: calibration.SaturatedByEngine, ScenarioFingerprint: "fp", JobID: 3,
	}); err != nil {
		t.Fatalf("UpsertCapacityProfile: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID)+"/capacity-profile?engine=jmeter&cpu=1&memory=512Mi")
	if rec.Code != http.StatusOK {
		t.Fatalf("get capacity profile = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		ScenarioID  int64   `json:"scenario_id"`
		PerPodQPS   float64 `json:"per_pod_qps"`
		SaturatedBy string  `json:"saturated_by"`
		JobID       int64   `json:"job_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.ScenarioID != scenarioID || got.PerPodQPS != 42 || got.SaturatedBy != "engine" || got.JobID != 3 {
		t.Fatalf("profile = %+v", got)
	}
}

func TestGetCapacityProfile_MissingReturnsNotFound(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	_, _, scenarioID := seedCalibration(t, h, store)
	rec := do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID)+"/capacity-profile?engine=jmeter&cpu=1&memory=512Mi")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get missing profile = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

func TestGetCapacityProfile_RequiresEngineCPUMemoryQueryParams(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	_, _, scenarioID := seedCalibration(t, h, store)
	rec := do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID)+"/capacity-profile?engine=jmeter&cpu=1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("get profile (missing memory param) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestGetCapacityProfile_InvalidScenarioID_400(t *testing.T) {
	t.Parallel()
	h, _, _ := newCalibrationRouter(t)
	rec := do(t, h, http.MethodGet, "/api/scenarios/x/capacity-profile?engine=jmeter&cpu=1&memory=512Mi")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("get profile (bad scenario id) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestFanOutCapacity_ReturnsOKForAFreshEngineLimitedProfile(t *testing.T) {
	t.Parallel()
	h, store, obj := newCalibrationRouter(t)
	_, _, scenarioID := seedCalibration(t, h, store)
	ctx := context.Background()

	// The scenario's real current fingerprint (no files/requests uploaded),
	// computed the same way calibrationapp.Service.FanOut will -- so this
	// profile reads as fresh, not stale.
	fp, err := scenarioapp.NewService(store, obj).ScenarioFingerprint(ctx, scenarioID)
	if err != nil {
		t.Fatalf("ScenarioFingerprint: %v", err)
	}
	key := capacityprofile.Key{ScenarioID: scenarioID, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi"}
	if err := store.UpsertCapacityProfile(ctx, capacityprofile.CapacityProfile{
		Key: key, PerPodQPS: 50, SaturatedBy: calibration.SaturatedByEngine, ScenarioFingerprint: fp,
	}); err != nil {
		t.Fatalf("UpsertCapacityProfile: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID)+"/capacity-profile/fanout?engine=jmeter&cpu=1&memory=512Mi&target_qps=120")
	if rec.Code != http.StatusOK {
		t.Fatalf("fan out = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Status  string `json:"status"`
		Engines int    `json:"engines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.Status != "ok" || got.Engines != 3 {
		t.Fatalf("fan out = %+v, want {ok, 3} (ceil(120/50))", got)
	}
}

func TestFanOutCapacity_NoProfileReturnsNoProfileStatus(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	_, _, scenarioID := seedCalibration(t, h, store)

	rec := do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID)+"/capacity-profile/fanout?engine=jmeter&cpu=1&memory=512Mi&target_qps=120")
	if rec.Code != http.StatusOK {
		t.Fatalf("fan out (no profile) = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "no_profile" {
		t.Fatalf("status = %q, want no_profile", got.Status)
	}
}

func TestFanOutCapacity_InvalidScenarioID_400(t *testing.T) {
	t.Parallel()
	h, _, _ := newCalibrationRouter(t)
	rec := do(t, h, http.MethodGet, "/api/scenarios/x/capacity-profile/fanout?engine=jmeter&cpu=1&memory=512Mi&target_qps=1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("fan out (bad scenario id) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestFanOutCapacity_RequiresEngineCPUMemoryQueryParams(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	_, _, scenarioID := seedCalibration(t, h, store)
	rec := do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID)+"/capacity-profile/fanout?engine=jmeter&target_qps=1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("fan out (missing cpu/memory params) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestFanOutCapacity_RejectsInvalidTargetQPS(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	_, _, scenarioID := seedCalibration(t, h, store)

	rec := do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID)+"/capacity-profile/fanout?engine=jmeter&cpu=1&memory=512Mi&target_qps=not-a-number")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("fan out (bad target_qps) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

// stubAdvanceRunner is a scripted calibrationapp.Runner used only to drive
// AdvanceOne directly in tests below -- AdvanceOne is not itself
// HTTP-exposed (cmd/calibrator's own job), so exercising the terminal-state
// response shape means calling it against the same store the router reads.
type stubAdvanceRunner struct {
	report report.Report
}

func (r *stubAdvanceRunner) RunStep(context.Context, int64, float64, int, float64) (report.Report, error) {
	return r.report, nil
}

func TestGetCalibrationJob_ShowsResultOnceTerminal(t *testing.T) {
	t.Parallel()
	h, store, obj := newCalibrationRouter(t)
	ctx := context.Background()
	_, executionID, scenarioID := seedCalibration(t, h, store)
	if err := store.StoreLoadProfile(ctx, executionID, false, []loadprofile.Entry{
		{ScenarioID: scenarioID, Engines: 1, Concurrency: 1, Duration: 1},
	}); err != nil {
		t.Fatalf("StoreLoadProfile: %v", err)
	}
	jobID := decodeID(t, do(t, h, http.MethodPost, "/api/executions/"+itoa(executionID)+"/calibration/trigger"))

	// One target-saturated step is terminal immediately.
	runner := &stubAdvanceRunner{report: report.Report{
		Requested: report.Load{Throughput: 10}, Achieved: report.Load{Throughput: 10, Samples: 1000},
		ErrorRate: 0.10, Attribution: report.Attribution{Target: 100},
	}}
	advancer := calibrationapp.NewService(store).WithRunner(runner).WithFingerprint(scenarioapp.NewService(store, obj))
	if _, err := advancer.AdvanceOne(ctx, time.Now()); err != nil {
		t.Fatalf("AdvanceOne: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/calibrations/"+itoa(jobID))
	if rec.Code != http.StatusOK {
		t.Fatalf("get calibration job = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Phase  string `json:"phase"`
		Result *struct {
			SaturatedBy string  `json:"saturated_by"`
			PerPodQPS   float64 `json:"per_pod_qps"`
		} `json:"result"`
		Steps []struct {
			Classification string `json:"classification"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.Phase != "done" || got.Result == nil {
		t.Fatalf("job = %+v, want phase done with a result", got)
	}
	if got.Result.SaturatedBy != "target" || got.Result.PerPodQPS != 10 {
		t.Fatalf("result = %+v, want target-saturated at 10", got.Result)
	}
	if len(got.Steps) != 1 || got.Steps[0].Classification != "target_saturated" {
		t.Fatalf("steps = %+v", got.Steps)
	}
}

func TestFanOutCapacity_StaleWhenScenarioFingerprintMismatches(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	_, _, scenarioID := seedCalibration(t, h, store)
	ctx := context.Background()
	key := capacityprofile.Key{ScenarioID: scenarioID, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi"}
	if err := store.UpsertCapacityProfile(ctx, capacityprofile.CapacityProfile{
		Key: key, PerPodQPS: 50, SaturatedBy: calibration.SaturatedByEngine, ScenarioFingerprint: "stale-fingerprint",
	}); err != nil {
		t.Fatalf("UpsertCapacityProfile: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID)+"/capacity-profile/fanout?engine=jmeter&cpu=1&memory=512Mi&target_qps=120")
	if rec.Code != http.StatusOK {
		t.Fatalf("fan out = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "stale" {
		t.Fatalf("status = %q, want stale", got.Status)
	}
}

func TestFanOutCapacity_TargetLimitedProfileReturnsNoEngineCount(t *testing.T) {
	t.Parallel()
	h, store, obj := newCalibrationRouter(t)
	_, _, scenarioID := seedCalibration(t, h, store)
	ctx := context.Background()

	fp, err := scenarioapp.NewService(store, obj).ScenarioFingerprint(ctx, scenarioID)
	if err != nil {
		t.Fatalf("ScenarioFingerprint: %v", err)
	}
	key := capacityprofile.Key{ScenarioID: scenarioID, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi"}
	if err := store.UpsertCapacityProfile(ctx, capacityprofile.CapacityProfile{
		Key: key, PerPodQPS: 30, SaturatedBy: calibration.SaturatedByTarget, ScenarioFingerprint: fp,
	}); err != nil {
		t.Fatalf("UpsertCapacityProfile: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID)+"/capacity-profile/fanout?engine=jmeter&cpu=1&memory=512Mi&target_qps=120")
	if rec.Code != http.StatusOK {
		t.Fatalf("fan out = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Status  string `json:"status"`
		Engines int    `json:"engines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "target_limited" || got.Engines != 0 {
		t.Fatalf("fan out = %+v, want target_limited with no engine count", got)
	}
}

func TestCalibrationHandlers_CalibrationsNotConfigured(t *testing.T) {
	t.Parallel()
	h := httpapi.NewRouter(httpapi.Deps{}) // no Calibrations wired
	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/calibrations"},
		{http.MethodPost, "/api/executions/1/calibration/trigger"},
		{http.MethodGet, "/api/calibrations/1"},
		{http.MethodGet, "/api/scenarios/1/capacity-profile?engine=jmeter&cpu=1&memory=512Mi"},
		{http.MethodGet, "/api/scenarios/1/capacity-profile/fanout?engine=jmeter&cpu=1&memory=512Mi&target_qps=1"},
	}
	for _, tc := range cases {
		if rec := do(t, h, tc.method, tc.path); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s (not configured) = %d, want 404 (%s)", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}
