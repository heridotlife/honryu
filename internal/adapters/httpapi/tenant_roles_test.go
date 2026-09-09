package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/account"
	"github.com/heridotlife/honryu/internal/domain/rbac"
)

// TestListTenantRoles pins GET /api/tenants/{tenant_id}/roles: a
// service-provider admin sees exactly that tenant's grants -- never another
// tenant's, never the global (tenant_id 0) scope -- each carrying the wire
// shape the operator UI renders (subject, email, role, granted_by,
// granted_time).
func TestListTenantRoles(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)
	acme := createTenant(t, f, "acme", "Acme")
	globex := createTenant(t, f, "globex", "Globex")

	// Grants in both tenants plus one global, assigned through the API so
	// granted_by is the acting admin and granted_time is stamped by the store.
	assign := func(tenantID int64, vals url.Values) {
		t.Helper()
		rec := f.req(t, http.MethodPost, "/api/tenants/"+strconv.FormatInt(tenantID, 10)+"/roles", "admin-tok", vals)
		if rec.Code != http.StatusCreated {
			t.Fatalf("assign in %d = %d (%s)", tenantID, rec.Code, rec.Body.String())
		}
	}
	assign(acme, url.Values{"subject": {"alice"}, "email": {"alice@acme.io"}, "role": {rbac.RoleTenantAdmin}})
	assign(acme, url.Values{"subject": {"bob"}, "email": {"bob@acme.io"}, "role": {rbac.RoleTenantEditor}})
	assign(globex, url.Values{"subject": {"carol"}, "email": {"carol@globex.io"}, "role": {rbac.RoleTenantViewer}})
	if rec := f.req(t, http.MethodPost, "/api/roles", "admin-tok",
		url.Values{"subject": {"root"}, "role": {rbac.RoleServiceProviderAdmin}}); rec.Code != http.StatusCreated {
		t.Fatalf("assign global = %d (%s)", rec.Code, rec.Body.String())
	}

	rec := f.req(t, http.MethodGet, "/api/tenants/"+strconv.FormatInt(acme, 10)+"/roles", "admin-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list roles = %d (%s)", rec.Code, rec.Body.String())
	}
	var grants []struct {
		Subject     string    `json:"subject"`
		Email       string    `json:"email"`
		Role        string    `json:"role"`
		GrantedBy   string    `json:"granted_by"`
		GrantedTime time.Time `json:"granted_time"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &grants); err != nil {
		t.Fatalf("decode grants: %v (%s)", err, rec.Body.String())
	}
	if len(grants) != 2 {
		t.Fatalf("grants = %+v, want alice+bob only (carol is globex, root is global)", grants)
	}
	for _, g := range grants {
		if g.Subject == "carol" {
			t.Fatalf("cross-tenant grant leaked: %+v", g)
		}
		if g.GrantedBy != "admin" {
			t.Fatalf("granted_by = %q, want the acting admin", g.GrantedBy)
		}
		if g.GrantedTime.IsZero() {
			t.Fatalf("granted_time not stamped: %+v", g)
		}
	}
	if grants[0].Subject != "alice" || grants[0].Email != "alice@acme.io" || grants[0].Role != rbac.RoleTenantAdmin {
		t.Fatalf("first grant = %+v, want alice's tenant_admin", grants[0])
	}

	// A tenant with no grants is an empty list, not null and not 404.
	rec = f.req(t, http.MethodGet, "/api/tenants/"+strconv.FormatInt(mustCreateEmptyTenant(t, f), 10)+"/roles", "admin-tok", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Fatalf("empty tenant roles = %d %s, want 200 []", rec.Code, rec.Body.String())
	}
}

// mustCreateEmptyTenant makes a tenant with no grants for the empty-list case.
func mustCreateEmptyTenant(t *testing.T, f *rbacFixture) int64 {
	t.Helper()
	return createTenant(t, f, "initech", "Initech")
}

// TestListTenantRoles_Gate pins the tenantAdminGate scoping: a tenant admin
// may list their own tenant's members, but listing another tenant's is 403 --
// the roster of one tenant is not the business of another's admin.
func TestListTenantRoles_Gate(t *testing.T) {
	t.Parallel()
	f := newRBACFixture(t)
	acme := createTenant(t, f, "acme", "Acme")
	globex := createTenant(t, f, "globex", "Globex")

	// Dave is acme's tenant admin.
	f.prov.Register("dave-tok", account.Account{Subject: "dave"})
	assignRole(t, f, acme, "dave", rbac.RoleTenantAdmin)
	assignRole(t, f, acme, "erin", rbac.RoleTenantEditor)

	own := "/api/tenants/" + strconv.FormatInt(acme, 10) + "/roles"
	if rec := f.req(t, http.MethodGet, own, "dave-tok", nil); rec.Code != http.StatusOK {
		t.Fatalf("own tenant list = %d (%s), want 200", rec.Code, rec.Body.String())
	}

	theirs := "/api/tenants/" + strconv.FormatInt(globex, 10) + "/roles"
	if rec := f.req(t, http.MethodGet, theirs, "dave-tok", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("other tenant list = %d (%s), want 403", rec.Code, rec.Body.String())
	}
}
