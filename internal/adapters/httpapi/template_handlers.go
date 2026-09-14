package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/domain/rbac"
)

// maxInstantiateBody bounds the instantiate request: a name, a project id,
// and at most one override URL.
const maxInstantiateBody = 1 << 13

// listTemplates serves the template catalog (GET /api/templates): every
// scenario flagged as a template, in id order. Templates are global starting
// points -- no project, no tenant -- so the list is not scoped the way
// project lists are. RBAC mode gates it on scenario:list at the global scope
// (a nil tenant: templates belong to none); legacy no-auth mode has nothing
// to gate -- the operator owns everything, the same stance every legacy
// route takes.
func (h *handlers) listTemplates(w http.ResponseWriter, r *http.Request) {
	if h.rbacEnabled() {
		if err := h.authorize(r.Context(), "", nil, rbac.ResourceScenario, rbac.ActionList); err != nil {
			respondError(w, err)
			return
		}
	}
	tpls, err := h.deps.Scenarios.Templates(r.Context())
	if err != nil {
		respondError(w, err)
		return
	}
	out := make([]planResponse, 0, len(tpls))
	for _, p := range tpls {
		out = append(out, toScenarioResponse(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// instantiateTemplate clones a template into a fresh, ordinary scenario
// (POST /api/scenarios/{scenario_id}/instantiate) and returns it (201).
//
// The override surface is deliberately tiny, and this is its documentation:
// the body carries `name` (required, the clone's own name) and `project_id`
// (required, where the clone lives), plus at most `overrides.target_url`
// (optional) -- the base URL the cloned fragment's requests resolve against.
// Everything else about the template (its requests, its portable kind) is
// cloned verbatim; a template with more to customize needs an override
// surface this phase does not have. scenarioapp.Instantiate owns the cloning
// rules; this handler only moves the request in and the new scenario out.
//
// Authorization runs in two steps: scenario:read on the template BEFORE the
// body is parsed (the audit's ungranted probe must 403 on the read, not 400
// on a body it was never going to be allowed to use), then the ordinary
// create-in-project check once the body names the target project.
func (h *handlers) instantiateTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "scenario_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scenario id")
		return
	}
	tpl, err := h.deps.Scenarios.Get(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	if h.rbacEnabled() {
		// A template carries no tenant, so this demands a global grant --
		// deliberate: the clone's destination is gated separately, below,
		// on the target project once the body names it.
		if err := h.authorize(r.Context(), "", tpl.TenantID, rbac.ResourceScenario, rbac.ActionRead); err != nil {
			respondError(w, err)
			return
		}
	}

	var body struct {
		Name      string `json:"name"`
		ProjectID int64  `json:"project_id"`
		Overrides struct {
			TargetURL string `json:"target_url"`
		} `json:"overrides"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxInstantiateBody)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, `body must be JSON {"name", "project_id", "overrides":{"target_url"}}`)
		return
	}
	if err := h.authorizeCreateScenario(r.Context(), body.ProjectID); err != nil {
		respondError(w, err)
		return
	}

	sc, err := h.deps.Scenarios.Instantiate(r.Context(), id, scenarioapp.InstantiateInput{
		Name:      body.Name,
		ProjectID: body.ProjectID,
		Overrides: scenarioapp.InstantiateOverrides{TargetURL: body.Overrides.TargetURL},
	})
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toScenarioResponse(sc))
}
