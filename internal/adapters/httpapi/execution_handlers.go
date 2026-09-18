package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	yaml "gopkg.in/yaml.v3"

	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/rbac"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

// calibrationSpecResponse is the calibration search a calibrate_engine
// execution was configured with (phase 62): the target-health criterion, the
// search bounds, and the pinned pod size -- the same reconstruction
// calibrationapp.Trigger drives steps from, so an operator reading the
// execution months later sees exactly what the search was pointed at.
type calibrationSpecResponse struct {
	Criterion   string  `json:"criterion"`
	SeedQPS     float64 `json:"seed_qps"`
	MaxQPS      float64 `json:"max_qps"`
	MaxSteps    int     `json:"max_steps"`
	HoldSeconds int     `json:"hold_seconds"`
	CPU         string  `json:"cpu"`
	Memory      string  `json:"memory"`
}

// The single-execution wire shape. Calibration is additive and omitempty:
// present only on calibrate_engine executions whose search was actually
// configured (and only when the deployment has the calibration service at
// all), so every other execution serialises exactly as before.
type executionResponse struct {
	ID            int64                    `json:"id"`
	Name          string                   `json:"name"`
	ProjectID     int64                    `json:"project_id"`
	Engine        taurus.Executor          `json:"engine,omitempty"`
	Kind          execution.Kind           `json:"kind"`
	Cluster       string                   `json:"cluster,omitempty"`
	FanOutTargets []string                 `json:"fanout_targets,omitempty"`
	CSVSplit      bool                     `json:"csv_split"`
	CreatedTime   time.Time                `json:"created_time"`
	LoadProfile   []loadprofile.Entry      `json:"load_profile"`
	Data          []executionapp.FileRef   `json:"data"`
	Calibration   *calibrationSpecResponse `json:"calibration,omitempty"`
}

// executionSummary is the list-item wire shape: identity and metadata only,
// no load profile or file listing -- those belong to the single-execution
// fetch, and embedding them here would turn the list into N config reads.
type executionSummary struct {
	ID            int64           `json:"id"`
	Name          string          `json:"name"`
	ProjectID     int64           `json:"project_id"`
	Engine        taurus.Executor `json:"engine,omitempty"`
	Kind          execution.Kind  `json:"kind"`
	Cluster       string          `json:"cluster,omitempty"`
	FanOutTargets []string        `json:"fanout_targets,omitempty"`
	CreatedTime   time.Time       `json:"created_time"`
}

// listExecutions returns the caller's executions, newest first -- every
// execution of every project the caller may see, not only the ones currently
// running (that narrower view remains GET /api/admin/executions).
func (h *handlers) listExecutions(w http.ResponseWriter, r *http.Request) {
	projects, err := h.visibleProjects(r)
	if err != nil {
		respondError(w, err)
		return
	}
	ids := make([]int64, 0, len(projects))
	for _, p := range projects {
		ids = append(ids, p.ID)
	}
	executions, err := h.deps.Executions.ListForProjects(r.Context(), ids)
	if err != nil {
		respondError(w, err)
		return
	}
	out := make([]executionSummary, 0, len(executions))
	for _, c := range executions {
		out = append(out, toExecutionSummary(c))
	}
	writeJSON(w, http.StatusOK, out)
}

// toExecutionSummary is the one execution-list serialization: every list
// endpoint that serves execution rows (GET /api/executions, and phase 67a's
// GET /api/scenarios/{scenario_id}/executions) must produce the same shape,
// so a caller's parsing never depends on which list a row came from.
func toExecutionSummary(c execution.Execution) executionSummary {
	return executionSummary{
		ID:            c.ID,
		Name:          c.Name,
		ProjectID:     c.ProjectID,
		Engine:        c.Engine,
		Kind:          c.Kind,
		Cluster:       c.Cluster,
		FanOutTargets: c.FanOutTargets,
		CreatedTime:   c.CreatedTime,
	}
}

func (h *handlers) getExecution(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "execution_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid execution id")
		return
	}
	if err := h.authorizeExecution(r, id, rbac.ActionRead); err != nil {
		respondError(w, err)
		return
	}
	c, err := h.deps.Executions.Get(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	cfg, err := h.deps.Executions.GetConfig(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	files, err := h.deps.Executions.Files(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	resp := executionResponse{
		ID:            c.ID,
		Name:          c.Name,
		ProjectID:     c.ProjectID,
		Engine:        c.Engine,
		Kind:          c.Kind,
		Cluster:       c.Cluster,
		FanOutTargets: c.FanOutTargets,
		CSVSplit:      c.CSVSplit,
		CreatedTime:   c.CreatedTime,
		LoadProfile:   cfg.Content.Tests,
		Data:          files,
	}
	// Phase 62: a calibrate_engine execution carries the search it was
	// configured with. Only when the deployment has the calibration service
	// (the same nil gate the calibration routes have); a calibrate-kind row
	// with no recorded bounds (ErrNotFound) serves without the section --
	// the spec is additive metadata, never a reason to hide the execution.
	if c.Kind == execution.KindCalibrateEngine && h.deps.Calibrations != nil {
		spec, err := h.deps.Calibrations.SpecFor(r.Context(), id)
		switch {
		case errors.Is(err, ports.ErrNotFound):
		case err != nil:
			respondError(w, err)
			return
		default:
			resp.Calibration = &calibrationSpecResponse{
				Criterion: spec.Criterion, SeedQPS: spec.SeedQPS, MaxQPS: spec.MaxQPS,
				MaxSteps: spec.MaxSteps, HoldSeconds: spec.HoldSeconds,
				CPU: spec.CPU, Memory: spec.Memory,
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handlers) createExecution(w http.ResponseWriter, r *http.Request) {
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
	fanOutTargets, err := parseFanOutTargets(r.PostForm.Get("fanout_targets"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid fanout_targets: expected a JSON array of cluster names")
		return
	}
	c, err := h.deps.Executions.Create(r.Context(), r.PostForm.Get("name"), projectID,
		taurus.Executor(r.PostForm.Get("engine")), r.PostForm.Get("cluster"), fanOutTargets)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toExecutionResponse(c))
}

// parseFanOutTargets decodes the fanout_targets form field: a JSON array of
// cluster names (e.g. `["eu-1","us-1"]`). Absent or blank means no fan-out --
// an ordinary single-cluster execution, exactly as before the field existed.
// Anything present but not a JSON array of strings is a client error.
func parseFanOutTargets(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var targets []string
	if err := json.Unmarshal([]byte(raw), &targets); err != nil {
		return nil, err
	}
	return targets, nil
}

func (h *handlers) deleteExecution(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "execution_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid execution id")
		return
	}
	if err := h.authorizeExecution(r, id, rbac.ActionDelete); err != nil {
		respondError(w, err)
		return
	}
	if err := h.deps.Executions.Delete(r.Context(), id); err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "execution deleted"})
}

func (h *handlers) listExecutionFiles(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "execution_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid execution id")
		return
	}
	if err := h.authorizeExecution(r, id, rbac.ActionRead); err != nil {
		respondError(w, err)
		return
	}
	files, err := h.deps.Executions.Files(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (h *handlers) uploadExecutionFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "execution_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid execution id")
		return
	}
	if err := h.authorizeExecution(r, id, rbac.ActionUpdate); err != nil {
		respondError(w, err)
		return
	}
	file, header, err := parseUpload(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid file upload")
		return
	}
	defer func() { _ = file.Close() }()
	if err := h.deps.Executions.UploadFile(r.Context(), id, header.Filename, file); err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "uploaded"})
}

func (h *handlers) deleteExecutionFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "execution_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid execution id")
		return
	}
	if err := h.authorizeExecution(r, id, rbac.ActionDelete); err != nil {
		respondError(w, err)
		return
	}
	if err := h.deps.Executions.DeleteFile(r.Context(), id, r.URL.Query().Get("filename")); err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "deleted"})
}

func (h *handlers) uploadExecutionConfig(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "execution_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid execution id")
		return
	}
	if err := h.authorizeExecution(r, id, rbac.ActionUpdate); err != nil {
		respondError(w, err)
		return
	}
	// Two body forms reach the same StoreConfig with the same validation:
	// application/json carries the Profile directly (the editor's shape),
	// anything else stays the historical multipart upload wrapping a
	// multi-test YAML file -- that file format is unchanged.
	if mediaType(r) == "application/json" {
		var profile loadprofile.Profile
		r.Body = http.MaxBytesReader(nil, r.Body, maxUploadBytes)
		if err := json.NewDecoder(r.Body).Decode(&profile); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if err := h.deps.Executions.StoreConfig(r.Context(), id, profile); err != nil {
			respondError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"message": "config stored"})
		return
	}
	file, _, err := parseUpload(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid file upload")
		return
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maxUploadBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read config")
		return
	}
	var wrapper loadprofile.Wrapper
	if err := yaml.Unmarshal(raw, &wrapper); err != nil {
		writeError(w, http.StatusBadRequest, "invalid YAML: "+err.Error())
		return
	}
	if err := h.deps.Executions.StoreConfig(r.Context(), id, wrapper.Content); err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "config stored"})
}

func (h *handlers) getExecutionConfig(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "execution_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid execution id")
		return
	}
	if err := h.authorizeExecution(r, id, rbac.ActionRead); err != nil {
		respondError(w, err)
		return
	}
	cfg, err := h.deps.Executions.GetConfig(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// authorizeExecution loads an execution and verifies the caller may perform
// action on it: ResourceExecution -- the resource Phase 20 finally puts to
// work, where every route previously funneled through authorizeProject's
// project:update -- scoped to the execution's own tenant, which Block A
// guarantees equals its project's. In legacy mode there is no account to
// scope by, so the project-owner check remains the whole decision.
func (h *handlers) authorizeExecution(r *http.Request, executionID int64, action rbac.Action) error {
	c, err := h.deps.Executions.Get(r.Context(), executionID)
	if err != nil {
		return err
	}
	if !h.rbacEnabled() {
		return h.authorizeProject(r.Context(), c.ProjectID, action)
	}
	return h.authorize(r.Context(), "", c.TenantID, rbac.ResourceExecution, action)
}

func toExecutionResponse(c execution.Execution) executionResponse {
	return executionResponse{
		Engine:        c.Engine,
		Kind:          c.Kind,
		Cluster:       c.Cluster,
		FanOutTargets: c.FanOutTargets,
		ID:            c.ID,
		Name:          c.Name,
		ProjectID:     c.ProjectID,
		CSVSplit:      c.CSVSplit,
		CreatedTime:   c.CreatedTime,
		LoadProfile:   []loadprofile.Entry{},
		Data:          []executionapp.FileRef{},
	}
}
