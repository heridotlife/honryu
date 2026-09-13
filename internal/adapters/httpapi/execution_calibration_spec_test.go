package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/heridotlife/honryu/internal/domain/execution"
)

// Phase 62: the calibration search a calibrate_engine execution was
// configured with rides GET /api/executions/{id} as an additive
// "calibration" object -- the after-the-fact answer to "what was this
// calibration pointed at", reconstructed by the same SpecFor the trigger
// drives steps from.

func TestGetExecutionSurfacesCalibrationSpec(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	_, executionID, _ := seedCalibration(t, h, store)

	// (seedCalibration's execution was created through the HTTP API with no
	// explicit bounds, so the spec here is the WithDefaults one.)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/executions/"+itoa(executionID), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get execution = %d (%s)", rec.Code, rec.Body.String())
	}

	var got struct {
		Kind        string `json:"kind"`
		CPU         string `json:"cpu"`
		Memory      string `json:"memory"`
		Calibration *struct {
			Criterion   string  `json:"criterion"`
			SeedQPS     float64 `json:"seed_qps"`
			MaxQPS      float64 `json:"max_qps"`
			MaxSteps    int     `json:"max_steps"`
			HoldSeconds int     `json:"hold_seconds"`
			CPU         string  `json:"cpu"`
			Memory      string  `json:"memory"`
		} `json:"calibration"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode execution: %v (%s)", err, rec.Body.String())
	}
	if got.Kind != string(execution.KindCalibrateEngine) {
		t.Fatalf("kind = %q, want calibrate_engine", got.Kind)
	}
	if got.Calibration == nil {
		t.Fatalf("calibration spec absent on a configured calibrate_engine execution (%s)", rec.Body.String())
	}
	if got.Calibration.Criterion != "failures>5%" {
		t.Errorf("criterion = %q, want failures>5%%", got.Calibration.Criterion)
	}
	if got.Calibration.SeedQPS != 10 || got.Calibration.MaxQPS != 10000 {
		t.Errorf("bounds = %v..%v, want the 10/10000 defaults", got.Calibration.SeedQPS, got.Calibration.MaxQPS)
	}
	if got.Calibration.CPU != "1" || got.Calibration.Memory != "512Mi" {
		t.Errorf("pod size = %s/%s, want the seeded 1/512Mi", got.Calibration.CPU, got.Calibration.Memory)
	}
}

func TestGetExecutionNormalHasNoCalibrationSection(t *testing.T) {
	t.Parallel()
	h, _, _ := newCalibrationRouter(t)
	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	executionID := decodeID(t, postForm(t, h, "/api/executions", url.Values{"name": {"peak"}, "project_id": {itoa(projectID)}}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/executions/"+itoa(executionID), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get execution = %d (%s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "\"calibration\"") {
		t.Fatalf("normal execution carries a calibration section: %s", rec.Body.String())
	}
}

// A calibrate-kind row whose search was never configured (no bounds
// recorded) serves WITHOUT the section rather than failing -- the spec is
// additive metadata, never a reason to hide the execution.
func TestGetExecutionCalibrateKindWithoutBoundsOmitsSpec(t *testing.T) {
	t.Parallel()
	h, store, _ := newCalibrationRouter(t)
	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	exe, err := execution.New("bare", projectID)
	if err != nil {
		t.Fatalf("execution.New: %v", err)
	}
	exe.Kind = execution.KindCalibrateEngine
	exe.Engine = "jmeter"
	exe.CPU, exe.Memory = "1", "512Mi"
	id, err := store.CreateExecution(context.Background(), exe)
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/executions/"+itoa(id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get execution = %d (%s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "\"calibration\"") {
		t.Fatalf("unconfigured calibrate execution carries a calibration section: %s", rec.Body.String())
	}
}
