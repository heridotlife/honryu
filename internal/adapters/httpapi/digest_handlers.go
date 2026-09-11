package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/heridotlife/honryu/internal/app/digestapp"
	"github.com/heridotlife/honryu/internal/domain/digest"
	"github.com/heridotlife/honryu/internal/domain/rbac"
	"github.com/heridotlife/honryu/internal/ports"
)

// digestConfigResponse is the wire shape of GET/PUT
// /api/projects/{project_id}/digest: the firing configuration as the
// scheduler sees it. last_fired is absent when no digest has fired yet.
type digestConfigResponse struct {
	ProjectID int64      `json:"project_id"`
	Period    string     `json:"period"`
	Enabled   bool       `json:"enabled"`
	LastFired *time.Time `json:"last_fired,omitempty"`
}

// digestRowResponse is one stored digest in the feed: the window, and the
// payload's verdict fields decoded -- the feed serves the same numbers the
// webhook delivery carried, without re-aggregating.
type digestRowResponse struct {
	ID                int64                        `json:"id"`
	Period            string                       `json:"period"`
	WindowStart       time.Time                    `json:"window_start"`
	WindowEnd         time.Time                    `json:"window_end"`
	RunsTotal         int                          `json:"runs_total"`
	ByOutcome         digestapp.ByOutcome          `json:"by_outcome"`
	ThresholdFailures int                          `json:"threshold_failures"`
	Executions        []digestapp.ExecutionSummary `json:"executions"`
}

// defaultDigestLimit caps the feed's default page and the maximum a caller
// may ask for: a project accrues at most one digest per period per fire,
// so ten is a history and a hundred is an archive.
const (
	defaultDigestLimit = 10
	maxDigestLimit     = 100
)

// parseDigestPeriod validates the form's period word, answering the 400
// itself so every route shares one message.
func parseDigestPeriod(w http.ResponseWriter, raw string) (digest.Period, bool) {
	period, err := digest.ParsePeriod(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "period must be daily or weekly")
		return "", false
	}
	return period, true
}

// digestGate rejects the request unless the digest service is wired. It
// returns true when the handler may proceed, the same contract every
// optional service follows (404 "not configured" before any authorization).
func (h *handlers) digestGate(w http.ResponseWriter) bool {
	if h.deps.Digests == nil {
		writeError(w, http.StatusNotFound, "digests not configured")
		return false
	}
	return true
}

// setDigestConfig is the digest schedule's "on" path: upsert the period
// and enable firing. The project-update grant matches the webhook routes'
// -- administering what a project broadcasts is an update to the project,
// not merely a read of it.
func (h *handlers) setDigestConfig(w http.ResponseWriter, r *http.Request) {
	if !h.digestGate(w) {
		return
	}
	projectID, ok := pathInt(r, "project_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	if err := h.authorizeProject(r.Context(), projectID, rbac.ActionUpdate); err != nil {
		respondError(w, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form")
		return
	}
	period, ok := parseDigestPeriod(w, r.PostForm.Get("period"))
	if !ok {
		return
	}
	if err := h.deps.Digests.SetSchedule(r.Context(), projectID, period); err != nil {
		respondError(w, err)
		return
	}
	h.writeDigestConfig(w, r, projectID)
}

// getDigestConfig returns the project's digest configuration, 404 when
// none is set (which is how the UI reads "off").
func (h *handlers) getDigestConfig(w http.ResponseWriter, r *http.Request) {
	if !h.digestGate(w) {
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
	h.writeDigestConfig(w, r, projectID)
}

// writeDigestConfig answers with the project's current configuration as
// stored -- an upsert answers with what landed, a read with what is, so
// both paths share one resolver for the 200/404 split.
func (h *handlers) writeDigestConfig(w http.ResponseWriter, r *http.Request, projectID int64) {
	sched, err := h.deps.Digests.GetSchedule(r.Context(), projectID)
	if err != nil {
		// A project with no digest configuration is the normal "off" state,
		// not a server fault -- but the raw repo sentinel ("ports: not
		// found") names an internal package, so intercept it here and speak
		// the resource's own language instead.
		if errors.Is(err, ports.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no digest schedule for this project")
			return
		}
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, digestConfigResponse{
		ProjectID: sched.ProjectID,
		Period:    string(sched.Period),
		Enabled:   sched.Enabled,
		LastFired: sched.LastFired,
	})
}

// deleteDigestConfig is the digest schedule's "off" path: remove the
// configuration entirely. Past digest rows stay -- history is history.
func (h *handlers) deleteDigestConfig(w http.ResponseWriter, r *http.Request) {
	if !h.digestGate(w) {
		return
	}
	projectID, ok := pathInt(r, "project_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	if err := h.authorizeProject(r.Context(), projectID, rbac.ActionUpdate); err != nil {
		respondError(w, err)
		return
	}
	if err := h.deps.Digests.DeleteSchedule(r.Context(), projectID); err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			// The pinned contract (TestDigestConfigUpsertRoundtrip) reads a
			// second delete as 404, but the raw repo sentinel names an
			// internal package -- speak the resource's language instead.
			writeError(w, http.StatusNotFound, "no digest schedule for this project")
			return
		}
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// listDigests serves the project's stored digests, newest first -- the
// in-app feed. limit defaults to 10 and is capped at 100: absent means "a
// page", not "everything".
func (h *handlers) listDigests(w http.ResponseWriter, r *http.Request) {
	if !h.digestGate(w) {
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
	limit := queryInt(r, "limit")
	if limit <= 0 {
		limit = defaultDigestLimit
	}
	if limit > maxDigestLimit {
		limit = maxDigestLimit
	}
	rows, err := h.deps.Digests.ListForProject(r.Context(), projectID, limit)
	if err != nil {
		respondError(w, err)
		return
	}
	out := make([]digestRowResponse, 0, len(rows))
	for _, d := range rows {
		resp := digestRowResponse{
			ID: d.ID, Period: string(d.Period),
			WindowStart: d.WindowStart, WindowEnd: d.WindowEnd,
		}
		// The stored bytes are the contract; a row that fails to decode
		// (impossible from this service's own writes) still lists, with
		// its verdict fields zeroed rather than dropping the row.
		if p, err := digestapp.DecodePayload(d.Payload); err == nil {
			resp.RunsTotal = p.RunsTotal
			resp.ByOutcome = p.ByOutcome
			resp.ThresholdFailures = p.ThresholdFailures
			resp.Executions = p.Executions
		}
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, out)
}
