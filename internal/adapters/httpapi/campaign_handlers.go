package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/heridotlife/honryu/internal/app/campaignapp"
	"github.com/heridotlife/honryu/internal/domain/campaign"
	"github.com/heridotlife/honryu/internal/domain/rbac"
)

type serviceResponse struct {
	ProjectID   int64 `json:"project_id"`
	ExecutionID int64 `json:"execution_id"`
}

type campaignResponse struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	TenantID    int64             `json:"tenant_id"`
	WindowStart time.Time         `json:"window_start"`
	WindowEnd   time.Time         `json:"window_end"`
	Services    []serviceResponse `json:"services"`
	// Active/AbortedAt surface campaign.Campaign.IsActive's derived state, so
	// a caller never has to reimplement the window+abort computation itself.
	Active    bool       `json:"active"`
	AbortedAt *time.Time `json:"aborted_at,omitempty"`
}

func toCampaignResponse(c campaign.Campaign, now time.Time) campaignResponse {
	services := make([]serviceResponse, len(c.Services))
	for i, svc := range c.Services {
		services[i] = serviceResponse{ProjectID: svc.ProjectID, ExecutionID: svc.ExecutionID}
	}
	return campaignResponse{
		ID: c.ID, Name: c.Name, TenantID: c.TenantID,
		WindowStart: c.Window.Start, WindowEnd: c.Window.End,
		Services: services, Active: c.IsActive(now), AbortedAt: c.AbortedAt,
	}
}

// parseCampaignServices reads a campaign's participating services from
// paired, index-matched form values (service_project_id[i] designates
// service_execution_id[i] as that project's readiness test) -- the
// form-encoded shape closest to a list of pairs without switching this one
// endpoint to a JSON body while every other mutating route stays
// form-encoded.
func parseCampaignServices(form url.Values) ([]campaign.Service, error) {
	projectIDs := form["service_project_id"]
	executionIDs := form["service_execution_id"]
	if len(projectIDs) != len(executionIDs) {
		return nil, fmt.Errorf("service_project_id and service_execution_id must have the same number of values")
	}
	out := make([]campaign.Service, len(projectIDs))
	for i := range projectIDs {
		projectID, err := strconv.ParseInt(projectIDs[i], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid service_project_id %q", projectIDs[i])
		}
		executionID, err := strconv.ParseInt(executionIDs[i], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid service_execution_id %q", executionIDs[i])
		}
		out[i] = campaign.Service{ProjectID: projectID, ExecutionID: executionID}
	}
	return out, nil
}

// createCampaign defines a PM-owned readiness event: a window and the
// services (projects, each with its designated readiness execution)
// participating in it.
func (h *handlers) createCampaign(w http.ResponseWriter, r *http.Request) {
	if !h.campaignsConfigured(w) {
		return
	}
	tenantID, ok := pathInt(r, "tenant_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid tenant id")
		return
	}
	if err := h.authorizeCampaignTenant(r.Context(), tenantID, rbac.ActionCreate); err != nil {
		respondError(w, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form")
		return
	}
	start, err := time.Parse(time.RFC3339, r.PostForm.Get("window_start"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid window_start: "+err.Error())
		return
	}
	end, err := time.Parse(time.RFC3339, r.PostForm.Get("window_end"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid window_end: "+err.Error())
		return
	}
	services, err := parseCampaignServices(r.PostForm)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c := campaign.Campaign{
		Name:     r.PostForm.Get("name"),
		TenantID: tenantID,
		Window:   campaign.Window{Start: start, End: end},
		Services: services,
	}
	created, err := h.deps.Campaigns.Create(r.Context(), c)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toCampaignResponse(created, time.Now()))
}

// listCampaigns lists every campaign belonging to tenantID.
func (h *handlers) listCampaigns(w http.ResponseWriter, r *http.Request) {
	if !h.campaignsConfigured(w) {
		return
	}
	tenantID, ok := pathInt(r, "tenant_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid tenant id")
		return
	}
	if err := h.authorizeCampaignTenant(r.Context(), tenantID, rbac.ActionList); err != nil {
		respondError(w, err)
		return
	}
	campaigns, err := h.deps.Campaigns.List(r.Context(), tenantID)
	if err != nil {
		respondError(w, err)
		return
	}
	now := time.Now()
	out := make([]campaignResponse, len(campaigns))
	for i, c := range campaigns {
		out[i] = toCampaignResponse(c, now)
	}
	writeJSON(w, http.StatusOK, out)
}

// getCampaign returns one campaign. Authorization is derived from the
// campaign's own recorded TenantID, not any tenant_id the caller might
// supply, the same pattern authorizeSchedule uses for a schedule's
// execution id.
func (h *handlers) getCampaign(w http.ResponseWriter, r *http.Request) {
	if !h.campaignsConfigured(w) {
		return
	}
	id, ok := pathInt(r, "campaign_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid campaign id")
		return
	}
	c, err := h.deps.Campaigns.Get(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	if err := h.authorizeCampaignTenant(r.Context(), c.TenantID, rbac.ActionRead); err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toCampaignResponse(c, time.Now()))
}

type failingCriterionResponse struct {
	Criterion string `json:"criterion"`
	Unparsed  bool   `json:"unparsed,omitempty"`
}

type serviceVerdictResponse struct {
	ProjectID       int64                      `json:"project_id"`
	ExecutionID     int64                      `json:"execution_id"`
	HasReport       bool                       `json:"has_report"`
	Outcome         string                     `json:"outcome,omitempty"`
	FailingCriteria []failingCriterionResponse `json:"failing_criteria,omitempty"`
	// ShortOfTargetQPS names the target-QPS gate: true when the service
	// requested a target throughput and achieved less than 95% of it -- a
	// criteria pass under a fraction of the intended load is not a real go.
	ShortOfTargetQPS    bool    `json:"short_of_target_qps,omitempty"`
	RequestedThroughput float64 `json:"requested_throughput,omitempty"`
	AchievedThroughput  float64 `json:"achieved_throughput,omitempty"`
}

type otherLoadResponse struct {
	ExecutionID int64     `json:"execution_id"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	EngineCount int       `json:"engine_count"`
}

type campaignVerdictResponse struct {
	CampaignID int64                    `json:"campaign_id"`
	Services   []serviceVerdictResponse `json:"services"`
	Go         bool                     `json:"go"`
	// OtherLoad names every other execution active in the campaign's
	// tenant during its window -- the minimum mitigation for the residual
	// risk that a non-participating execution could distort the
	// campaign's readiness numbers by contending for shared
	// infrastructure Honryu cannot see or scope around.
	OtherLoad []otherLoadResponse `json:"other_load,omitempty"`
}

func toVerdictResponse(v campaignapp.CampaignVerdict) campaignVerdictResponse {
	services := make([]serviceVerdictResponse, len(v.Services))
	for i, sv := range v.Services {
		criteria := make([]failingCriterionResponse, len(sv.FailingCriteria))
		for j, fc := range sv.FailingCriteria {
			criteria[j] = failingCriterionResponse{Criterion: fc.Criterion, Unparsed: fc.Unparsed}
		}
		services[i] = serviceVerdictResponse{
			ProjectID: sv.ProjectID, ExecutionID: sv.ExecutionID,
			HasReport: sv.HasReport, Outcome: string(sv.Outcome),
			FailingCriteria:     criteria,
			ShortOfTargetQPS:    sv.ShortOfTargetQPS,
			RequestedThroughput: sv.RequestedThroughput,
			AchievedThroughput:  sv.AchievedThroughput,
		}
	}
	otherLoad := make([]otherLoadResponse, len(v.OtherLoad))
	for i, ol := range v.OtherLoad {
		otherLoad[i] = otherLoadResponse{
			ExecutionID: ol.ExecutionID, Start: ol.Start, End: ol.End, EngineCount: ol.EngineCount,
		}
	}
	return campaignVerdictResponse{CampaignID: v.CampaignID, Services: services, Go: v.Go, OtherLoad: otherLoad}
}

// updateCampaign edits a campaign's preparation-time definition: name,
// window, and participating services. Authorization is derived from the
// campaign's own recorded TenantID (like getCampaign), not any caller
// input, and the edit itself is rejected with 409 once the stored window
// has opened (campaignapp.ErrCampaignStarted): a live campaign's
// definition is what freeze, the drain sweep, and the verdict key off, so
// it is frozen the moment it starts (spec Phase 20: preparation-only
// editing).
func (h *handlers) updateCampaign(w http.ResponseWriter, r *http.Request) {
	if !h.campaignsConfigured(w) {
		return
	}
	id, ok := pathInt(r, "campaign_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid campaign id")
		return
	}
	c, err := h.deps.Campaigns.Get(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	if err := h.authorizeCampaignTenant(r.Context(), c.TenantID, rbac.ActionUpdate); err != nil {
		respondError(w, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form")
		return
	}
	start, err := time.Parse(time.RFC3339, r.PostForm.Get("window_start"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid window_start: "+err.Error())
		return
	}
	end, err := time.Parse(time.RFC3339, r.PostForm.Get("window_end"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid window_end: "+err.Error())
		return
	}
	services, err := parseCampaignServices(r.PostForm)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := h.deps.Campaigns.Update(r.Context(), id, campaign.Campaign{
		Name:     r.PostForm.Get("name"),
		Window:   campaign.Window{Start: start, End: end},
		Services: services,
	})
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toCampaignResponse(updated, time.Now()))
}

// abortCampaign closes the campaign now, lifting freeze immediately --
// the PM's own control over the event they run, exposing the existing
// campaignapp.Abort which was previously reachable only through the
// platform kill-switch (POST /api/admin/abort, which additionally tears
// executions down). Gated campaign:delete on the campaign's own tenant.
func (h *handlers) abortCampaign(w http.ResponseWriter, r *http.Request) {
	if !h.campaignsConfigured(w) {
		return
	}
	id, ok := pathInt(r, "campaign_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid campaign id")
		return
	}
	c, err := h.deps.Campaigns.Get(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	if err := h.authorizeCampaignTenant(r.Context(), c.TenantID, rbac.ActionDelete); err != nil {
		respondError(w, err)
		return
	}
	if err := h.deps.Campaigns.Abort(r.Context(), id); err != nil {
		respondError(w, err)
		return
	}
	aborted, err := h.deps.Campaigns.Get(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	h.audit(r.Context(), "campaign.abort", "campaign", strconv.FormatInt(id, 10))
	writeJSON(w, http.StatusOK, toCampaignResponse(aborted, time.Now()))
}

// listAllCampaigns is the PM's cross-tenant view: every campaign of every
// tenant the caller holds any role in, scoped down by acct.TenantIDs()
// (audit rule C1, mirroring listProjects). A service-provider admin (who
// holds no tenant role anywhere) short-circuits to every campaign, the
// same branch visibleProjects gives projects. A caller with neither sees
// an empty list, never another tenant's campaigns.
func (h *handlers) listAllCampaigns(w http.ResponseWriter, r *http.Request) {
	if !h.campaignsConfigured(w) {
		return
	}
	acct := accountFrom(r.Context())
	var campaigns []campaign.Campaign
	var err error
	if acct.HasGlobalRole(rbac.RoleServiceProviderAdmin) {
		// Same short-circuit as visibleProjects: a service-provider
		// admin holds no tenant role anywhere, so TenantIDs() is empty
		// -- scope by role, not by the (empty) tenant list.
		campaigns, err = h.deps.Campaigns.ListAll(r.Context())
	} else {
		campaigns, err = h.deps.Campaigns.ListByTenants(r.Context(), acct.TenantIDs())
	}
	if err != nil {
		respondError(w, err)
		return
	}
	now := time.Now()
	out := make([]campaignResponse, len(campaigns))
	for i, c := range campaigns {
		out[i] = toCampaignResponse(c, now)
	}
	writeJSON(w, http.StatusOK, out)
}

// getCampaignVerdict returns the campaign's rolled-up verdict: per-service
// outcome (and, for a failed service, its named failing criteria), plus
// one overall go/no-go.
//
// Read access accepts either campaign:read on the campaign's own tenant (a
// campaign_manager can read the verdict of the event they run without
// holding edit rights on any participating project -- spec bug 2) or read
// on at least one participating project (a service owner can see their own
// campaign's result without a PM-level grant).
func (h *handlers) getCampaignVerdict(w http.ResponseWriter, r *http.Request) {
	if !h.campaignsConfigured(w) {
		return
	}
	id, ok := pathInt(r, "campaign_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid campaign id")
		return
	}
	c, err := h.deps.Campaigns.Get(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	if err := h.authorizeAnyParticipatingProject(r.Context(), c); err != nil {
		respondError(w, err)
		return
	}
	v, err := h.deps.Campaigns.Verdict(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toVerdictResponse(v))
}

type serviceComparisonResponse struct {
	ProjectID   int64  `json:"project_id"`
	Status      string `json:"status"`
	HasCurrent  bool   `json:"has_current"`
	Go          bool   `json:"go,omitempty"`
	HasBaseline bool   `json:"has_baseline"`
	BaselineGo  bool   `json:"baseline_go,omitempty"`
}

type campaignComparisonResponse struct {
	CampaignID int64 `json:"campaign_id"`
	// HasBaseline is false when the campaign has no resolvable baseline (no
	// prior ended campaign in its tenant, and none given explicitly) --
	// Services is empty in that case, not computed against an empty baseline.
	HasBaseline        bool                        `json:"has_baseline"`
	BaselineCampaignID int64                       `json:"baseline_campaign_id,omitempty"`
	Services           []serviceComparisonResponse `json:"services"`
}

func toComparisonResponse(c campaignapp.CampaignComparison) campaignComparisonResponse {
	services := make([]serviceComparisonResponse, len(c.Services))
	for i, sc := range c.Services {
		services[i] = serviceComparisonResponse{
			ProjectID: sc.ProjectID, Status: string(sc.Status),
			HasCurrent: sc.HasCurrent, Go: sc.Go,
			HasBaseline: sc.HasBaseline, BaselineGo: sc.BaselineGo,
		}
	}
	return campaignComparisonResponse{
		CampaignID: c.CampaignID, HasBaseline: c.HasBaseline,
		BaselineCampaignID: c.BaselineCampaignID, Services: services,
	}
}

// getCampaignComparison compares the campaign's per-service go/no-go against
// a baseline campaign -- an explicit ?baseline=<campaign_id>, or (when
// absent) the tenant's most-recent-prior ended campaign. A campaign with no
// resolvable baseline returns HasBaseline:false with no services, not an
// error.
//
// Authorization mirrors getCampaignVerdict: the caller must be authorized to
// view at least one of the campaign's participating projects.
func (h *handlers) getCampaignComparison(w http.ResponseWriter, r *http.Request) {
	if !h.campaignsConfigured(w) {
		return
	}
	id, ok := pathInt(r, "campaign_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid campaign id")
		return
	}
	c, err := h.deps.Campaigns.Get(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	if err := h.authorizeAnyParticipatingProject(r.Context(), c); err != nil {
		respondError(w, err)
		return
	}
	var baselineID int64
	if raw := r.URL.Query().Get("baseline"); raw != "" {
		baselineID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid baseline campaign id")
			return
		}
	}
	comparison, err := h.deps.Campaigns.Compare(r.Context(), id, baselineID)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toComparisonResponse(comparison))
}

// authorizeAnyParticipatingProject allows the caller through if they may
// read the campaign itself -- campaign:read on the campaign's own tenant,
// the same check authorizeCampaignTenant applies -- or, failing that, at
// least one of its participating projects. The campaign:read branch is the
// Phase 20 fix for spec bug 2: a campaign_manager used to be able to create
// an event and then got 403 on its verdict, because this check demanded
// project:update on a participating project and the PM deliberately holds
// no project edit rights anywhere. The participating-project fallback keeps
// the pre-existing path for service owners, now asking project:read -- a
// verdict is a read, and demanding update locked out every read-only role
// that could otherwise see the campaign's projects.
func (h *handlers) authorizeAnyParticipatingProject(ctx context.Context, c campaign.Campaign) error {
	if h.rbacEnabled() {
		if err := h.authorizeCampaignTenant(ctx, c.TenantID, rbac.ActionRead); err == nil {
			return nil
		}
	}
	for _, svc := range c.Services {
		if err := h.authorizeProject(ctx, svc.ProjectID, rbac.ActionRead); err == nil {
			return nil
		}
	}
	return errForbidden
}

// campaignsConfigured rejects the request with 404 unless campaigns are
// wired (Deps.Campaigns != nil), mirroring tenantAdminGate's own guard for
// an optional dependency. Returns true when the handler may proceed.
func (h *handlers) campaignsConfigured(w http.ResponseWriter) bool {
	if h.deps.Campaigns == nil {
		writeError(w, http.StatusNotFound, "campaigns not configured")
		return false
	}
	return true
}

// authorizeCampaignTenant checks the caller may act on tenantID's campaigns
// with action. Under RBAC this requires a role, global or scoped to
// tenantID specifically, granting rbac.ResourceCampaign/action --
// RoleCampaignManager is the only built-in role with that permission, so an
// ordinary tenant editor/admin (who can manage the same tenant's projects)
// is still denied: a campaign freezes other teams' work, and that authority
// is deliberately not bundled with project edit rights. In no-auth mode the
// operator has full access, matching every other admin-adjacent route.
func (h *handlers) authorizeCampaignTenant(ctx context.Context, tenantID int64, action rbac.Action) error {
	if !h.rbacEnabled() {
		return nil
	}
	dec := h.deps.Auth.Authorize(accountFrom(ctx), rbac.Request{Resource: rbac.ResourceCampaign, Action: action, TenantID: &tenantID})
	if !dec.Allowed {
		return errForbidden
	}
	return nil
}
