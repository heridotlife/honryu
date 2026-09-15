package httpapi

import (
	"net/http"
	"time"

	"github.com/heridotlife/honryu/internal/domain/rbac"
	"github.com/heridotlife/honryu/internal/domain/slo"
)

// sloResponse is the JSON wire shape for an SLO. The targets are pointers so
// an untracked metric is null on the wire -- the same nil-means-untracked
// shape the domain and the storage both speak.
type sloResponse struct {
	ID                 int64     `json:"id"`
	ProjectID          int64     `json:"project_id"`
	Name               string    `json:"name"`
	TargetP95MS        *float64  `json:"target_p95_ms"`
	TargetErrorRate    *float64  `json:"target_error_rate"`
	TargetSuccessRatio *float64  `json:"target_success_ratio"`
	CreatedTime        time.Time `json:"created_time"`
}

func toSLOResponse(s slo.SLO) sloResponse {
	return sloResponse{
		ID:                 s.ID,
		ProjectID:          s.ProjectID,
		Name:               s.Name,
		TargetP95MS:        s.TargetP95MS,
		TargetErrorRate:    s.TargetErrorRate,
		TargetSuccessRatio: s.TargetSuccessRatio,
		CreatedTime:        s.CreatedTime,
	}
}

// sloGate rejects the request unless the SLO service is wired. It returns
// true when the handler may proceed. The gate runs before the project
// authorization so a service-less router answers 404 "not configured"
// rather than probing projects it was never going to grade -- the same
// contract every optional service follows.
func (h *handlers) sloGate(w http.ResponseWriter) bool {
	if h.deps.SLOs == nil {
		writeError(w, http.StatusNotFound, "slos not configured")
		return false
	}
	return true
}

// createSLO defines a service-level objective for a project. At least one
// target must be set (the domain's Validate, surfaced as 400 with the stated
// reason), and the per-project name must be unused (409 -- the fix is a
// different name, not a retry).
func (h *handlers) createSLO(w http.ResponseWriter, r *http.Request) {
	if !h.sloGate(w) {
		return
	}
	projectID, ok := pathInt(r, "project_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	if err := h.authorizeProject(r.Context(), projectID, rbac.ActionCreate); err != nil {
		respondError(w, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form")
		return
	}
	obj := slo.SLO{
		ProjectID: projectID,
		Name:      r.PostForm.Get("name"),
	}
	if v, ok, err := formFloat(r.PostForm, "target_p95_ms"); err != nil {
		writeError(w, http.StatusBadRequest, "invalid target_p95_ms")
		return
	} else if ok {
		obj.TargetP95MS = &v
	}
	if v, ok, err := formFloat(r.PostForm, "target_error_rate"); err != nil {
		writeError(w, http.StatusBadRequest, "invalid target_error_rate")
		return
	} else if ok {
		obj.TargetErrorRate = &v
	}
	if v, ok, err := formFloat(r.PostForm, "target_success_ratio"); err != nil {
		writeError(w, http.StatusBadRequest, "invalid target_success_ratio")
		return
	} else if ok {
		obj.TargetSuccessRatio = &v
	}
	created, err := h.deps.SLOs.Create(r.Context(), obj)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSLOResponse(created))
}

// listSLOs returns a project's SLOs in definition order.
func (h *handlers) listSLOs(w http.ResponseWriter, r *http.Request) {
	if !h.sloGate(w) {
		return
	}
	projectID, ok := pathInt(r, "project_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	if err := h.authorizeProject(r.Context(), projectID, rbac.ActionRead); err != nil {
		respondError(w, err)
		return
	}
	objs, err := h.deps.SLOs.List(r.Context(), projectID)
	if err != nil {
		respondError(w, err)
		return
	}
	out := make([]sloResponse, 0, len(objs))
	for _, obj := range objs {
		out = append(out, toSLOResponse(obj))
	}
	writeJSON(w, http.StatusOK, out)
}

// deleteSLO removes one of a project's SLOs. The service scopes the delete
// by project, so an SLO id under a foreign project's path is a plain 404 --
// not this project's to delete.
func (h *handlers) deleteSLO(w http.ResponseWriter, r *http.Request) {
	if !h.sloGate(w) {
		return
	}
	projectID, ok := pathInt(r, "project_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	id, ok := pathInt(r, "slo_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid slo id")
		return
	}
	if err := h.authorizeProject(r.Context(), projectID, rbac.ActionDelete); err != nil {
		respondError(w, err)
		return
	}
	if err := h.deps.SLOs.Delete(r.Context(), projectID, id); err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// sloMetricResponse is one metric's grade on the budget wire: the target,
// what the window measured, whether it stayed within, and how much of the
// budget is left -- the SLO's own formula, documented on the domain type.
type sloMetricResponse struct {
	Metric             string   `json:"metric"`
	Target             float64  `json:"target"`
	Actual             *float64 `json:"actual"`
	Compliant          bool     `json:"compliant"`
	BudgetRemainingPct *float64 `json:"budget_remaining_pct"`
}

// sloBudgetResponse is the wire shape of GET
// /api/projects/{project_id}/slos/{slo_id}/budget: one SLO's grade over one
// window. metrics is always an array -- one line per configured target -- and
// run_count is how many eligible reports the window held, so a "compliant"
// verdict with no runs can never be mistaken for a measured one.
type sloBudgetResponse struct {
	SLOID       int64               `json:"slo_id"`
	Name        string              `json:"name"`
	Window      string              `json:"window"`
	WindowStart time.Time           `json:"window_start"`
	WindowEnd   time.Time           `json:"window_end"`
	RunCount    int                 `json:"run_count"`
	Compliant   bool                `json:"compliant"`
	Metrics     []sloMetricResponse `json:"metrics"`
}

// sloBudget serves one SLO's budget over ?window= (1d/7d/30d, default 7d --
// a week is long enough to smooth one bad run and short enough to still be
// news).
func (h *handlers) sloBudget(w http.ResponseWriter, r *http.Request) {
	if !h.sloGate(w) {
		return
	}
	projectID, ok := pathInt(r, "project_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	id, ok := pathInt(r, "slo_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid slo id")
		return
	}
	if err := h.authorizeProject(r.Context(), projectID, rbac.ActionRead); err != nil {
		respondError(w, err)
		return
	}
	window := r.URL.Query().Get("window")
	if window == "" {
		window = slo.DefaultWindow
	}
	out, err := h.deps.SLOs.Budget(r.Context(), projectID, id, window, time.Now())
	if err != nil {
		respondError(w, err)
		return
	}
	resp := sloBudgetResponse{
		SLOID: out.SLOID, Name: out.SLO.Name, Window: out.Window,
		WindowStart: out.WindowStart, WindowEnd: out.WindowEnd,
		RunCount: out.RunCount, Compliant: out.Budget.Compliant,
		Metrics: make([]sloMetricResponse, 0, len(out.Budget.Metrics)),
	}
	for _, m := range out.Budget.Metrics {
		resp.Metrics = append(resp.Metrics, sloMetricResponse{
			Metric: m.Metric, Target: m.Target, Actual: m.Actual,
			Compliant: m.Compliant, BudgetRemainingPct: m.BudgetRemainingPct,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}
