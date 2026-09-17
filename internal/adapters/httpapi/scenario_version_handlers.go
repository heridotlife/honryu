package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/heridotlife/honryu/internal/domain/rbac"
	"github.com/heridotlife/honryu/internal/ports"
)

// scenarioVersionResponse is one row of the version-history wire: identity
// and stamp only. created_by is null when no principal was reachable at
// capture time (the legacy no-auth path) -- "unknown", never a fabricated
// name. The snapshot itself rides only the single-version fetch, so the
// history list stays cheap.
type scenarioVersionResponse struct {
	ID          int64     `json:"id"`
	ScenarioID  int64     `json:"scenario_id"`
	Version     int       `json:"version"`
	CreatedTime time.Time `json:"created_time"`
	CreatedBy   *string   `json:"created_by"`
}

func toScenarioVersionResponse(m ports.ScenarioVersionMeta) scenarioVersionResponse {
	return scenarioVersionResponse{
		ID:          m.ID,
		ScenarioID:  m.ScenarioID,
		Version:     m.Version,
		CreatedTime: m.CreatedTime,
		CreatedBy:   m.CreatedBy,
	}
}

func toScenarioVersionResponses(list []ports.ScenarioVersionMeta) []scenarioVersionResponse {
	out := make([]scenarioVersionResponse, 0, len(list))
	for _, m := range list {
		out = append(out, toScenarioVersionResponse(m))
	}
	return out
}

// scenarioVersionDetailResponse is the single-version wire: the metadata
// plus the full snapshot as it was captured -- the scenario row's own
// fields plus test_file, data (names), and the requests fragment.
type scenarioVersionDetailResponse struct {
	scenarioVersionResponse
	Snapshot ports.ScenarioSnapshot `json:"snapshot"`
}

// versionsGate rejects the request unless the scenario service carries a
// version store, before any authorization: a versioning-less router
// answers 404 "not configured" -- the same contract every optional
// service follows.
func (h *handlers) versionsGate(w http.ResponseWriter) bool {
	if h.deps.Scenarios == nil || !h.deps.Scenarios.VersionsEnabled() {
		writeError(w, http.StatusNotFound, "version history not configured")
		return false
	}
	return true
}

// listScenarioVersions serves GET /api/scenarios/{scenario_id}/versions:
// the scenario's edit history, newest first -- number, stamp, actor. The
// snapshots stay behind the single-version fetch so a long history lists
// without shipping every full scenario.
func (h *handlers) listScenarioVersions(w http.ResponseWriter, r *http.Request) {
	if !h.versionsGate(w) {
		return
	}
	id, ok := pathInt(r, "scenario_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scenario id")
		return
	}
	if err := h.authorizeScenario(r, id, rbac.ActionRead); err != nil {
		respondError(w, err)
		return
	}
	list, err := h.deps.Scenarios.ListVersions(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toScenarioVersionResponses(list))
}

// getScenarioVersion serves GET /api/scenarios/{scenario_id}/versions/{version}:
// one version's full snapshot -- what the scenario looked like at that
// version, the as-of read the history list points into.
func (h *handlers) getScenarioVersion(w http.ResponseWriter, r *http.Request) {
	if !h.versionsGate(w) {
		return
	}
	id, version, ok := h.versionPath(w, r)
	if !ok {
		return
	}
	if err := h.authorizeScenario(r, id, rbac.ActionRead); err != nil {
		respondError(w, err)
		return
	}
	v, err := h.deps.Scenarios.ScenarioVersion(r.Context(), id, version)
	if err != nil {
		respondScenarioVersionError(w, err)
		return
	}
	resp := scenarioVersionDetailResponse{
		scenarioVersionResponse: toScenarioVersionResponse(v.ScenarioVersionMeta),
		Snapshot:                v.Snapshot,
	}
	writeJSON(w, http.StatusOK, resp)
}

// restoreScenarioVersion serves POST
// /api/scenarios/{scenario_id}/versions/{version}/restore: the named
// version's snapshot becomes the live scenario. Append-only, never a
// silent overwrite: the pre-restore state is captured as a NEW version
// before the snapshot is applied, so a restore is itself a version and the
// operator can undo it by restoring that one. The response names both.
func (h *handlers) restoreScenarioVersion(w http.ResponseWriter, r *http.Request) {
	if !h.versionsGate(w) {
		return
	}
	id, version, ok := h.versionPath(w, r)
	if !ok {
		return
	}
	if err := h.authorizeScenario(r, id, rbac.ActionUpdate); err != nil {
		respondError(w, err)
		return
	}
	res, err := h.deps.Scenarios.Restore(r.Context(), id, version)
	if err != nil {
		respondScenarioVersionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message":       "restored",
		"restored_from": res.RestoredFrom,
		"version":       res.Version,
	})
}

// versionPath parses the scenario and version path segments shared by the
// two versioned routes. A non-numeric version is the caller's mistake
// (400), not a lookup miss (404).
func (h *handlers) versionPath(w http.ResponseWriter, r *http.Request) (int64, int, bool) {
	id, ok := pathInt(r, "scenario_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scenario id")
		return 0, 0, false
	}
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid version")
		return 0, 0, false
	}
	return id, version, true
}

// respondScenarioVersionError maps the version use-cases' sentinels onto
// the wire: unknown version is 404 with a reason that names the version
// (not the generic not-found), an unwired store is the gate's 404, and
// everything else keeps the standard mapping (unknown scenario 404
// included).
func respondScenarioVersionError(w http.ResponseWriter, err error) {
	if errors.Is(err, ports.ErrScenarioVersionNotFound) {
		writeError(w, http.StatusNotFound, "version not found")
		return
	}
	respondError(w, err)
}
