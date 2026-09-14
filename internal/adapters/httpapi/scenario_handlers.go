package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/domain/rbac"
	"github.com/heridotlife/honryu/internal/domain/scenario"
)

type planResponse struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	ProjectID   int64     `json:"project_id"`
	CreatedTime time.Time `json:"created_time"`
	// Phase 65: templates ride the same wire shape (the catalog list and
	// every scenario response carry the flag), so the SPA can badge a
	// template without a second endpoint.
	IsTemplate   bool                  `json:"is_template"`
	TemplateName string                `json:"template_name,omitempty"`
	TestFile     *scenarioapp.FileRef  `json:"test_file"`
	Data         []scenarioapp.FileRef `json:"data"`
}

func (h *handlers) getScenario(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "scenario_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scenario id")
		return
	}
	if err := h.authorizeScenario(r, id, rbac.ActionRead); err != nil {
		respondError(w, err)
		return
	}
	p, err := h.deps.Scenarios.Get(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	files, err := h.deps.Scenarios.Files(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	resp := toScenarioResponse(p)
	resp.TestFile = files.TestFile
	resp.Data = files.Data
	writeJSON(w, http.StatusOK, resp)
}

func (h *handlers) createScenario(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form")
		return
	}
	projectID, err := strconv.ParseInt(r.PostForm.Get("project_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid project_id")
		return
	}
	if err := h.authorizeCreateScenario(r.Context(), projectID); err != nil {
		respondError(w, err)
		return
	}
	p, err := h.deps.Scenarios.Create(r.Context(), r.PostForm.Get("name"), projectID)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toScenarioResponse(p))
}

// importScenario creates a scenario from an uploaded JMeter plan and returns
// the inspector's findings alongside it. The findings are part of the success
// response rather than a warning log: a plan that runs differently under Honryu
// than it did under Shibuya is exactly what the importing user needs told.
func (h *handlers) importScenario(w http.ResponseWriter, r *http.Request) {
	file, header, err := parseUpload(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read upload")
		return
	}
	defer func() { _ = file.Close() }()

	projectID, err := strconv.ParseInt(r.FormValue("project_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid project_id")
		return
	}
	if err := h.authorizeCreateScenario(r.Context(), projectID); err != nil {
		respondError(w, err)
		return
	}

	name := r.FormValue("name")
	if name == "" {
		name = strings.TrimSuffix(header.Filename, ".jmx")
	}

	res, err := h.deps.Scenarios.ImportJMX(r.Context(), name, projectID, header.Filename, file)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"scenario": toScenarioResponse(res.Scenario),
		"report":   res.Report,
	})
}

func (h *handlers) deleteScenario(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "scenario_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scenario id")
		return
	}
	if err := h.authorizeScenario(r, id, rbac.ActionDelete); err != nil {
		respondError(w, err)
		return
	}
	if err := h.deps.Scenarios.Delete(r.Context(), id); err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "scenario deleted"})
}

func (h *handlers) listScenarioFiles(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "scenario_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scenario id")
		return
	}
	if err := h.authorizeScenario(r, id, rbac.ActionRead); err != nil {
		respondError(w, err)
		return
	}
	files, err := h.deps.Scenarios.Files(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (h *handlers) uploadScenarioFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "scenario_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scenario id")
		return
	}
	if err := h.authorizeScenario(r, id, rbac.ActionUpdate); err != nil {
		respondError(w, err)
		return
	}
	file, header, err := parseUpload(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid file upload")
		return
	}
	defer func() { _ = file.Close() }()
	if err := h.deps.Scenarios.UploadFile(r.Context(), id, header.Filename, file); err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "uploaded"})
}

// setScenarioRequests uploads a portable scenario's declarative workload.
// Two bodies are accepted: a raw text/yaml (or application/x-yaml) request
// body -- the editor's path, byte-preserving by construction -- and the
// original multipart file upload, which keeps working unchanged.
// scenarioapp.SetRequests does the parsing and validation, so this handler
// only moves bytes.
func (h *handlers) setScenarioRequests(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "scenario_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scenario id")
		return
	}
	if err := h.authorizeScenario(r, id, rbac.ActionUpdate); err != nil {
		respondError(w, err)
		return
	}
	raw, err := readRequestsBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read requests")
		return
	}
	if err := h.deps.Scenarios.SetRequests(r.Context(), id, raw); err != nil {
		// A rejected fragment carries line-anchored diagnostics so the
		// editor can point at the broken row. Any other failure (unknown
		// scenario, native scenario) keeps writeError's envelope.
		var inv *scenarioapp.InvalidRequestsError
		if errors.As(err, &inv) {
			writeDiagnostics(w, http.StatusBadRequest, inv.Err.Error(), inv.Diagnostics)
			return
		}
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "requests stored"})
}

// readRequestsBody extracts the fragment bytes from either a raw YAML body or
// a multipart upload. The media type decides: text/yaml and
// application/x-yaml read the body verbatim (byte-preserving by
// construction); anything else goes through the multipart path. A body that
// is neither is rejected by the multipart parse failing.
func readRequestsBody(r *http.Request) ([]byte, error) {
	switch ct := mediaType(r); ct {
	case "text/yaml", "application/x-yaml", "text/x-yaml":
		r.Body = http.MaxBytesReader(nil, r.Body, maxUploadBytes)
		raw, err := io.ReadAll(r.Body) // #nosec G120 -- body bounded above
		if err != nil {
			return nil, err
		}
		return raw, nil
	default:
		file, _, err := parseUpload(r)
		if err != nil {
			return nil, err
		}
		defer func() { _ = file.Close() }()
		return io.ReadAll(io.LimitReader(file, maxUploadBytes))
	}
}

// mediaType parses the Content-Type header down to its bare type/subtype,
// lower-cased, parameters stripped. Empty header yields "".
func mediaType(r *http.Request) string {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return ""
	}
	base, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return ""
	}
	return strings.ToLower(base)
}

// getScenarioRequests returns a portable scenario's stored fragment exactly
// as uploaded -- raw text/yaml, not JSON-wrapped -- so the editor can
// round-trip what it edits. 404 when nothing has ever been uploaded; 409
// for a non-portable scenario, matching SetRequests' own stance.
func (h *handlers) getScenarioRequests(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "scenario_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scenario id")
		return
	}
	if err := h.authorizeScenario(r, id, rbac.ActionRead); err != nil {
		respondError(w, err)
		return
	}
	raw, err := h.deps.Scenarios.Requests(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// #nosec G705 -- Content-Type is text/yaml (set above), so the browser does
	// not interpret the stored fragment as HTML/JS; there is no XSS sink here.
	// Same reasoning as report_handlers.go's runShardObject and
	// lifecycle_handlers.go's pod-log handler, both already annotated this way.
	if _, err := w.Write(raw); err != nil {
		slog.Error("httpapi: failed to write requests fragment", "error", err)
	}
}

// validateScenarioRequests validates a submitted fragment without storing
// it, returning G4's line-anchored diagnostics. It shares one code path
// with setScenarioRequests -- same body reader, same service method
// family -- so validate and deploy cannot disagree: any body this endpoint
// accepts, SetRequests stores.
func (h *handlers) validateScenarioRequests(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "scenario_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scenario id")
		return
	}
	if err := h.authorizeScenario(r, id, rbac.ActionUpdate); err != nil {
		respondError(w, err)
		return
	}
	raw, err := readRequestsBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read requests")
		return
	}
	diags, err := h.deps.Scenarios.ValidateRequests(r.Context(), id, raw)
	if err != nil {
		// A rejected fragment carries line-anchored diagnostics so the
		// editor can point at the broken row -- the same envelope the
		// store path returns. Unknown scenario and native scenario keep
		// the standard error mapping (404 / 409).
		var inv *scenarioapp.InvalidRequestsError
		if errors.As(err, &inv) {
			writeDiagnostics(w, http.StatusBadRequest, inv.Err.Error(), inv.Diagnostics)
			return
		}
		respondError(w, err)
		return
	}
	// Success carries informational findings (G6 populates these later)
	// alongside the verdict, per the OpenAPI contract: a valid fragment
	// is one the store path would accept unchanged.
	if diags == nil {
		diags = []scenarioapp.Diagnostic{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "diagnostics": diags})
}

func (h *handlers) deleteScenarioFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "scenario_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scenario id")
		return
	}
	if err := h.authorizeScenario(r, id, rbac.ActionDelete); err != nil {
		respondError(w, err)
		return
	}
	if err := h.deps.Scenarios.DeleteFile(r.Context(), id, r.URL.Query().Get("filename")); err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "deleted"})
}

// authorizeScenario loads a scenario and verifies the caller may perform
// action on it: ResourceScenario, scoped to the scenario's own tenant (which
// Block A guarantees equals its project's). In legacy mode the project-owner
// check remains the whole decision, exactly as before.
func (h *handlers) authorizeScenario(r *http.Request, scenarioID int64, action rbac.Action) error {
	p, err := h.deps.Scenarios.Get(r.Context(), scenarioID)
	if err != nil {
		return err
	}
	if !h.rbacEnabled() {
		return h.authorizeProject(r.Context(), p.ProjectID, action)
	}
	return h.authorize(r.Context(), "", p.TenantID, rbac.ResourceScenario, action)
}

func toScenarioResponse(p scenario.Scenario) planResponse {
	return planResponse{
		ID:           p.ID,
		Name:         p.Name,
		ProjectID:    p.ProjectID,
		CreatedTime:  p.CreatedTime,
		IsTemplate:   p.IsTemplate,
		TemplateName: p.TemplateName,
		Data:         []scenarioapp.FileRef{},
	}
}
