package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/heridotlife/honryu/internal/domain/rbac"
	"github.com/heridotlife/honryu/internal/domain/tenant"
	"github.com/heridotlife/honryu/internal/ports"
)

// tenantResponse is the JSON wire shape for a Tenant.
type tenantResponse struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	Status      string    `json:"status"`
	CreatedTime time.Time `json:"created_time"`
}

func toTenantResponse(t tenant.Tenant) tenantResponse {
	return tenantResponse{
		ID:          t.ID,
		Name:        t.Name,
		DisplayName: t.DisplayName,
		Status:      t.Status,
		CreatedTime: t.CreatedTime,
	}
}

// tenantAdminGate rejects the request unless tenants are configured and the
// caller may administer them. It returns true when the handler may proceed.
// Routes keyed by a {tenant_id} path parameter scope the check to that
// tenant (Phase 20): a tenant's own admin holds tenant:admin there, and the
// previously unscoped check could never see it -- spec bug 3, where all 13
// routes behind this gate admitted only service_provider_admin. Routes
// without one (create/list tenants, global role grants) keep the unscoped
// check: a tenant admin may not mint tenants or platform-wide admins.
func (h *handlers) tenantAdminGate(w http.ResponseWriter, r *http.Request) bool {
	if h.deps.Tenants == nil {
		writeError(w, http.StatusNotFound, "tenants not configured")
		return false
	}
	var tenantID *int64
	if id, err := strconv.ParseInt(r.PathValue("tenant_id"), 10, 64); err == nil {
		tenantID = &id
	}
	if err := h.authorizeAdmin(r.Context(), tenantID); err != nil {
		respondError(w, err)
		return false
	}
	return true
}

// audit records an administrative action, attributing it to the authenticated
// caller. It is a no-op when auditing is not configured.
func (h *handlers) audit(ctx context.Context, action, target, detail string) {
	if h.deps.Audit == nil {
		return
	}
	_ = h.deps.Audit.Record(ctx, ports.AuditEvent{
		Actor:  accountFrom(ctx).Subject,
		Action: action,
		Target: target,
		Detail: detail,
	})
}

// authorizeAdmin permits tenant/role administration. Under RBAC it requires a
// service-provider admin; when tenantID is set -- the route itself names the
// tenant being administered -- a tenant:admin grant scoped to that tenant
// also qualifies, which is what a tenant's own RoleTenantAdmin holds.
// In no-auth mode the operator has full access.
func (h *handlers) authorizeAdmin(ctx context.Context, tenantID *int64) error {
	if !h.rbacEnabled() {
		return nil
	}
	dec := h.deps.Auth.Authorize(accountFrom(ctx), rbac.Request{Resource: rbac.ResourceTenant, Action: rbac.ActionAdmin, TenantID: tenantID})
	if !dec.Allowed {
		return errForbidden
	}
	return nil
}

func (h *handlers) createTenant(w http.ResponseWriter, r *http.Request) {
	if !h.tenantAdminGate(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form")
		return
	}
	t, err := h.deps.Tenants.Create(r.Context(), r.PostForm.Get("name"), r.PostForm.Get("display_name"))
	if err != nil {
		respondError(w, err)
		return
	}
	h.audit(r.Context(), "tenant.create", t.Name, "")
	writeJSON(w, http.StatusCreated, toTenantResponse(t))
}

func (h *handlers) listTenants(w http.ResponseWriter, r *http.Request) {
	if !h.tenantAdminGate(w, r) {
		return
	}
	tenants, err := h.deps.Tenants.List(r.Context())
	if err != nil {
		respondError(w, err)
		return
	}
	out := make([]tenantResponse, 0, len(tenants))
	for _, t := range tenants {
		out = append(out, toTenantResponse(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) getTenant(w http.ResponseWriter, r *http.Request) {
	if !h.tenantAdminGate(w, r) {
		return
	}
	id, ok := pathInt(r, "tenant_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid tenant id")
		return
	}
	t, err := h.deps.Tenants.Get(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTenantResponse(t))
}

func (h *handlers) setTenantStatus(w http.ResponseWriter, r *http.Request) {
	if !h.tenantAdminGate(w, r) {
		return
	}
	id, ok := pathInt(r, "tenant_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid tenant id")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form")
		return
	}
	status := r.PostForm.Get("status")
	if err := h.deps.Tenants.SetStatus(r.Context(), id, status); err != nil {
		respondError(w, err)
		return
	}
	h.audit(r.Context(), "tenant.status", strconv.FormatInt(id, 10), status)
	writeJSON(w, http.StatusOK, map[string]string{"message": "status updated"})
}

// setTenantQuota sets a tenant's per-cluster engine quota ceiling. cluster
// defaults to "" (the implicit default cluster, until Phase 8's registry
// exists) when the form omits it.
func (h *handlers) setTenantQuota(w http.ResponseWriter, r *http.Request) {
	if !h.tenantAdminGate(w, r) {
		return
	}
	id, ok := pathInt(r, "tenant_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid tenant id")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form")
		return
	}
	ceiling, err := strconv.Atoi(r.PostForm.Get("ceiling"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid ceiling")
		return
	}
	cluster := r.PostForm.Get("cluster")
	if err := h.deps.Tenants.SetQuota(r.Context(), id, cluster, ceiling); err != nil {
		respondError(w, err)
		return
	}
	h.audit(r.Context(), "tenant.quota", strconv.FormatInt(id, 10), cluster)
	writeJSON(w, http.StatusOK, map[string]string{"message": "quota updated"})
}

// getTenantQuota returns a tenant's per-cluster engine quota ceiling (0 if
// never configured). cluster defaults to "" when the query omits it.
func (h *handlers) getTenantQuota(w http.ResponseWriter, r *http.Request) {
	if !h.tenantAdminGate(w, r) {
		return
	}
	id, ok := pathInt(r, "tenant_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid tenant id")
		return
	}
	cluster := r.URL.Query().Get("cluster")
	ceiling, err := h.deps.Tenants.GetQuota(r.Context(), id, cluster)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cluster": cluster, "ceiling": ceiling})
}

func (h *handlers) assignTenantRole(w http.ResponseWriter, r *http.Request) {
	if !h.tenantAdminGate(w, r) {
		return
	}
	id, ok := pathInt(r, "tenant_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid tenant id")
		return
	}
	h.assignRole(w, r, &id)
}

// roleGrantResponse is the JSON wire shape for one entry of a tenant's
// member roster.
type roleGrantResponse struct {
	Subject     string    `json:"subject"`
	Email       string    `json:"email"`
	Role        string    `json:"role"`
	GrantedBy   string    `json:"granted_by"`
	GrantedTime time.Time `json:"granted_time"`
}

// listTenantRoles returns a tenant's member roster: the grants scoped to
// that tenant, each with who granted it and when.
func (h *handlers) listTenantRoles(w http.ResponseWriter, r *http.Request) {
	if !h.tenantAdminGate(w, r) {
		return
	}
	id, ok := pathInt(r, "tenant_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid tenant id")
		return
	}
	entries, err := h.deps.Tenants.ListTenantRoles(r.Context(), id)
	if err != nil {
		respondError(w, err)
		return
	}
	out := make([]roleGrantResponse, 0, len(entries))
	for _, e := range entries {
		out = append(out, roleGrantResponse{
			Subject: e.Subject, Email: e.Email, Role: e.RoleName,
			GrantedBy: e.GrantedBy, GrantedTime: e.GrantedTime,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) assignGlobalRole(w http.ResponseWriter, r *http.Request) {
	if !h.tenantAdminGate(w, r) {
		return
	}
	h.assignRole(w, r, nil)
}

func (h *handlers) assignRole(w http.ResponseWriter, r *http.Request, tenantID *int64) {
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form")
		return
	}
	g := ports.RoleGrant{
		Subject:   r.PostForm.Get("subject"),
		Email:     r.PostForm.Get("email"),
		RoleName:  r.PostForm.Get("role"),
		TenantID:  tenantID,
		GrantedBy: accountFrom(r.Context()).Subject,
	}
	if g.Subject == "" {
		writeError(w, http.StatusBadRequest, "subject is required")
		return
	}
	if err := h.deps.Tenants.AssignRole(r.Context(), g); err != nil {
		respondError(w, err)
		return
	}
	h.audit(r.Context(), "role.assign", g.Subject, g.RoleName)
	writeJSON(w, http.StatusCreated, map[string]string{"message": "role assigned"})
}

func (h *handlers) revokeTenantRole(w http.ResponseWriter, r *http.Request) {
	if !h.tenantAdminGate(w, r) {
		return
	}
	id, ok := pathInt(r, "tenant_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid tenant id")
		return
	}
	h.revokeRole(w, r, &id)
}

func (h *handlers) revokeGlobalRole(w http.ResponseWriter, r *http.Request) {
	if !h.tenantAdminGate(w, r) {
		return
	}
	h.revokeRole(w, r, nil)
}

func (h *handlers) revokeRole(w http.ResponseWriter, r *http.Request, tenantID *int64) {
	subject := r.URL.Query().Get("subject")
	roleName := r.URL.Query().Get("role")
	if subject == "" || roleName == "" {
		writeError(w, http.StatusBadRequest, "subject and role are required")
		return
	}
	if err := h.deps.Tenants.RevokeRole(r.Context(), subject, roleName, tenantID); err != nil {
		respondError(w, err)
		return
	}
	h.audit(r.Context(), "role.revoke", subject, roleName)
	writeJSON(w, http.StatusOK, map[string]string{"message": "role revoked"})
}
