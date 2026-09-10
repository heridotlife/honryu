package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/heridotlife/honryu/internal/app/calibrationapp"
	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/capacityprofile"
	"github.com/heridotlife/honryu/internal/domain/rbac"
	"github.com/heridotlife/honryu/internal/domain/taurus"
)

type calibrationExecutionResponse struct {
	ExecutionID int64           `json:"execution_id"`
	Name        string          `json:"name"`
	ProjectID   int64           `json:"project_id"`
	Engine      taurus.Executor `json:"engine,omitempty"`
	CPU         string          `json:"cpu"`
	Memory      string          `json:"memory"`
	Criterion   string          `json:"criterion"`
	SeedQPS     float64         `json:"seed_qps"`
	MaxQPS      float64         `json:"max_qps"`
	MaxSteps    int             `json:"max_steps"`
	HoldSeconds int             `json:"hold_seconds"`
}

type calibrationStepResponse struct {
	RequestedQPS   float64 `json:"requested_qps"`
	AchievedQPS    float64 `json:"achieved_qps"`
	Classification string  `json:"classification"`
}

type calibrationResultResponse struct {
	SaturatedBy string  `json:"saturated_by"`
	PerPodQPS   float64 `json:"per_pod_qps"`
}

type calibrationJobResponse struct {
	ID          int64  `json:"id"`
	ExecutionID int64  `json:"execution_id"`
	Phase       string `json:"phase"`
	StepCount   int    `json:"step_count"`
	// NextRequestedQPS is meaningless once Result is set -- omitted then, the
	// same way calibration.Action's own doc explains it.
	NextRequestedQPS float64                    `json:"next_requested_qps,omitempty"`
	Result           *calibrationResultResponse `json:"result,omitempty"`
	FailureReason    string                     `json:"failure_reason,omitempty"`
	CreatedTime      time.Time                  `json:"created_time"`
	Steps            []calibrationStepResponse  `json:"steps"`
}

func toCalibrationJobResponse(job calibrationapp.Job) calibrationJobResponse {
	steps := make([]calibrationStepResponse, len(job.Steps))
	for i, s := range job.Steps {
		steps[i] = calibrationStepResponse{
			RequestedQPS: s.RequestedQPS, AchievedQPS: s.AchievedQPS, Classification: string(s.Classification),
		}
	}
	var result *calibrationResultResponse
	if job.Result != nil {
		result = &calibrationResultResponse{SaturatedBy: string(job.Result.SaturatedBy), PerPodQPS: job.Result.PerPodQPS}
	}
	return calibrationJobResponse{
		ID: job.ID, ExecutionID: job.ExecutionID, Phase: string(job.Phase), StepCount: job.StepCount,
		NextRequestedQPS: job.NextRequestedQPS, Result: result, FailureReason: job.FailureReason,
		CreatedTime: job.CreatedTime, Steps: steps,
	}
}

type capacityProfileResponse struct {
	ScenarioID          int64           `json:"scenario_id"`
	Engine              taurus.Executor `json:"engine"`
	CPU                 string          `json:"cpu"`
	Memory              string          `json:"memory"`
	PerPodQPS           float64         `json:"per_pod_qps"`
	SaturatedBy         string          `json:"saturated_by"`
	ScenarioFingerprint string          `json:"scenario_fingerprint"`
	CalibratedAt        time.Time       `json:"calibrated_at"`
	JobID               int64           `json:"job_id"`
}

func toCapacityProfileResponse(p capacityprofile.CapacityProfile) capacityProfileResponse {
	return capacityProfileResponse{
		ScenarioID: p.ScenarioID, Engine: p.Engine, CPU: p.CPU, Memory: p.Memory,
		PerPodQPS: p.PerPodQPS, SaturatedBy: string(p.SaturatedBy),
		ScenarioFingerprint: p.ScenarioFingerprint, CalibratedAt: p.CalibratedAt, JobID: p.JobID,
	}
}

// fanOutResponse is the fan-out calculator's answer: an engine count only
// ever accompanies StatusOK, so a caller can never read Engines without its
// own caveat sitting right next to it.
type fanOutResponse struct {
	Status  string `json:"status"`
	Engines int    `json:"engines,omitempty"`
}

// createCalibration configures a new CalibrateEngine execution: an ordinary
// execution pinned to one pod size, bound to one scenario copied from a
// source execution that already runs it, with its target-health criterion
// and search bounds recorded up front. Its search begins with a separate
// trigger call.
func (h *handlers) createCalibration(w http.ResponseWriter, r *http.Request) {
	if !h.calibrationsConfigured(w) {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form")
		return
	}
	projectID, err := strconv.ParseInt(r.PostForm.Get("project_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid project_id")
		return
	}
	if err := h.authorizeCreateExecution(r.Context(), projectID); err != nil {
		respondError(w, err)
		return
	}

	spec := calibration.Spec{
		Criterion: r.PostForm.Get("criterion"),
		CPU:       r.PostForm.Get("cpu"),
		Memory:    r.PostForm.Get("memory"),
	}
	if v, ok, ferr := formFloat(r.PostForm, "seed_qps"); ferr != nil {
		writeError(w, http.StatusBadRequest, ferr.Error())
		return
	} else if ok {
		spec.SeedQPS = v
	}
	if v, ok, ferr := formFloat(r.PostForm, "max_qps"); ferr != nil {
		writeError(w, http.StatusBadRequest, ferr.Error())
		return
	} else if ok {
		spec.MaxQPS = v
	}
	if v, ok, ferr := formInt(r.PostForm, "max_steps"); ferr != nil {
		writeError(w, http.StatusBadRequest, ferr.Error())
		return
	} else if ok {
		spec.MaxSteps = v
	}
	if v, ok, ferr := formInt(r.PostForm, "hold_seconds"); ferr != nil {
		writeError(w, http.StatusBadRequest, ferr.Error())
		return
	} else if ok {
		spec.HoldSeconds = v
	}

	name := r.PostForm.Get("name")
	engine := taurus.Executor(r.PostForm.Get("engine"))
	// The scenario binding's source: both ids are required -- a calibration
	// that named no scenario would be born the dead shell this endpoint
	// existed to stop creating (phase 41).
	scenarioID, err := strconv.ParseInt(r.PostForm.Get("scenario_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid scenario_id")
		return
	}
	sourceExecutionID, err := strconv.ParseInt(r.PostForm.Get("source_execution_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid source_execution_id")
		return
	}
	executionID, err := h.deps.Calibrations.Create(r.Context(), name, projectID, engine, spec, sourceExecutionID, scenarioID)
	if err != nil {
		respondError(w, err)
		return
	}
	// SpecFor re-reads what Create actually persisted -- WithDefaults'
	// filled-in values, not the (possibly zero) ones this request supplied.
	resolved, err := h.deps.Calibrations.SpecFor(r.Context(), executionID)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, calibrationExecutionResponse{
		ExecutionID: executionID, Name: name, ProjectID: projectID, Engine: engine,
		CPU: resolved.CPU, Memory: resolved.Memory, Criterion: resolved.Criterion,
		SeedQPS: resolved.SeedQPS, MaxQPS: resolved.MaxQPS, MaxSteps: resolved.MaxSteps, HoldSeconds: resolved.HoldSeconds,
	})
}

// triggerCalibration starts a fresh calibration search over an
// already-configured CalibrateEngine execution -- a Pending job a
// controller (cmd/calibrator, or cmd/scheduler hosting the same loop) will
// pick up and advance.
func (h *handlers) triggerCalibration(w http.ResponseWriter, r *http.Request) {
	if !h.calibrationsConfigured(w) {
		return
	}
	executionID, ok := pathInt(r, "execution_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid execution id")
		return
	}
	if err := h.authorizeExecution(r, executionID, rbac.ActionCreate); err != nil {
		respondError(w, err)
		return
	}
	jobID, err := h.deps.Calibrations.Trigger(r.Context(), executionID)
	if err != nil {
		respondError(w, err)
		return
	}
	job, err := h.deps.Calibrations.Get(r.Context(), jobID)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toCalibrationJobResponse(job))
}

// getCalibrationJob returns a calibration job's current state and full
// step-by-step history -- its status/progress.
func (h *handlers) getCalibrationJob(w http.ResponseWriter, r *http.Request) {
	if !h.calibrationsConfigured(w) {
		return
	}
	jobID, ok := pathInt(r, "job_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid job id")
		return
	}
	job, err := h.deps.Calibrations.Get(r.Context(), jobID)
	if err != nil {
		respondError(w, err)
		return
	}
	if err := h.authorizeExecution(r, job.ExecutionID, rbac.ActionRead); err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toCalibrationJobResponse(job))
}

// parseCapacityProfileKey reads the (engine, cpu, memory) query parameters
// that, together with scenarioID, fully identify one CapacityProfile.
func parseCapacityProfileKey(r *http.Request, scenarioID int64) (capacityprofile.Key, error) {
	q := r.URL.Query()
	engine, cpu, memory := q.Get("engine"), q.Get("cpu"), q.Get("memory")
	if engine == "" || cpu == "" || memory == "" {
		return capacityprofile.Key{}, fmt.Errorf("engine, cpu, and memory query parameters are required")
	}
	return capacityprofile.Key{ScenarioID: scenarioID, Engine: taurus.Executor(engine), CPU: cpu, Memory: memory}, nil
}

// getCapacityProfile returns the stored calibration profile for one exact
// (scenario, engine, cpu, memory) key.
func (h *handlers) getCapacityProfile(w http.ResponseWriter, r *http.Request) {
	if !h.calibrationsConfigured(w) {
		return
	}
	scenarioID, ok := pathInt(r, "scenario_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scenario id")
		return
	}
	if err := h.authorizeScenario(r, scenarioID, rbac.ActionRead); err != nil {
		respondError(w, err)
		return
	}
	key, err := parseCapacityProfileKey(r, scenarioID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	profile, err := h.deps.Calibrations.ProfileFor(r.Context(), key)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toCapacityProfileResponse(profile))
}

// fanOutCapacity turns a target aggregate QPS into a required engine count
// for one (scenario, engine, cpu, memory) key -- or, via the response's own
// status, states clearly why it cannot (no profile, a stale one, or one the
// target rather than the engine limited).
func (h *handlers) fanOutCapacity(w http.ResponseWriter, r *http.Request) {
	if !h.calibrationsConfigured(w) {
		return
	}
	scenarioID, ok := pathInt(r, "scenario_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scenario id")
		return
	}
	if err := h.authorizeScenario(r, scenarioID, rbac.ActionRead); err != nil {
		respondError(w, err)
		return
	}
	key, err := parseCapacityProfileKey(r, scenarioID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	targetQPS, err := strconv.ParseFloat(r.URL.Query().Get("target_qps"), 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid target_qps")
		return
	}
	result, err := h.deps.Calibrations.FanOut(r.Context(), key, targetQPS)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, fanOutResponse{Status: string(result.Status), Engines: result.Engines})
}

// calibrationsConfigured rejects the request with 404 unless calibration is
// wired (Deps.Calibrations != nil), mirroring campaignsConfigured's own
// guard for an optional dependency. Returns true when the handler may
// proceed.
func (h *handlers) calibrationsConfigured(w http.ResponseWriter) bool {
	if h.deps.Calibrations == nil {
		writeError(w, http.StatusNotFound, "calibrations not configured")
		return false
	}
	return true
}

// formFloat reads a form field as float64. Absent (empty) is reported via
// ok=false, not an error -- a caller distinguishes "not supplied" (keep
// Spec.WithDefaults' own default) from "supplied but invalid."
func formFloat(form url.Values, key string) (value float64, ok bool, err error) {
	v := form.Get(key)
	if v == "" {
		return 0, false, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false, fmt.Errorf("invalid %s", key)
	}
	return f, true, nil
}

// formInt is formFloat's integer sibling.
func formInt(form url.Values, key string) (value int, ok bool, err error) {
	v := form.Get(key)
	if v == "" {
		return 0, false, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false, fmt.Errorf("invalid %s", key)
	}
	return n, true, nil
}
