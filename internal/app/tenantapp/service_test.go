package tenantapp_test

import (
	"errors"
	"testing"

	"github.com/heridotlife/honryu/internal/app/tenantapp"
	"github.com/heridotlife/honryu/internal/domain/rbac"
	"github.com/heridotlife/honryu/internal/domain/tenant"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

func newSvc(t *testing.T) (*tenantapp.Service, *fake.Store) {
	t.Helper()
	store := fake.NewStore()
	return tenantapp.NewService(store, store, store), store
}

func TestCreateAndList(t *testing.T) {
	t.Parallel()
	svc, _ := newSvc(t)
	ctx := t.Context()

	tn, err := svc.Create(ctx, "Acme", "Acme Inc")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tn.ID == 0 || tn.Name != "acme" || tn.Status != tenant.StatusActive {
		t.Fatalf("created tenant = %+v", tn)
	}

	got, err := svc.Get(ctx, tn.ID)
	if err != nil || got.Name != "acme" {
		t.Fatalf("Get = %+v, %v", got, err)
	}

	list, err := svc.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %+v, %v", list, err)
	}
}

func TestCreateRejectsInvalid(t *testing.T) {
	t.Parallel()
	svc, _ := newSvc(t)
	if _, err := svc.Create(t.Context(), "ab", "too short"); !errors.Is(err, tenant.ErrNameInvalid) {
		t.Fatalf("Create(short) = %v, want ErrNameInvalid", err)
	}
}

func TestSetStatus(t *testing.T) {
	t.Parallel()
	svc, _ := newSvc(t)
	ctx := t.Context()
	tn, _ := svc.Create(ctx, "acme", "Acme")

	if err := svc.SetStatus(ctx, tn.ID, tenant.StatusSuspended); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	got, _ := svc.Get(ctx, tn.ID)
	if got.Active() {
		t.Fatal("tenant still active after suspend")
	}

	if err := svc.SetStatus(ctx, tn.ID, "BOGUS"); !errors.Is(err, tenant.ErrStatusInvalid) {
		t.Fatalf("SetStatus(bogus) = %v, want ErrStatusInvalid", err)
	}
}

// An unconfigured ceiling reads as 0 -- unconfigured, not unlimited -- until
// explicitly set, and stays scoped per cluster.
func TestQuota(t *testing.T) {
	t.Parallel()
	svc, _ := newSvc(t)
	ctx := t.Context()
	tn, _ := svc.Create(ctx, "acme", "Acme")

	got, err := svc.GetQuota(ctx, tn.ID, "default")
	if err != nil || got != 0 {
		t.Fatalf("GetQuota before configured = %d, %v, want 0, nil", got, err)
	}

	if err := svc.SetQuota(ctx, tn.ID, "default", 5); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}
	got, err = svc.GetQuota(ctx, tn.ID, "default")
	if err != nil || got != 5 {
		t.Fatalf("GetQuota = %d, %v, want 5, nil", got, err)
	}

	if got, err := svc.GetQuota(ctx, tn.ID, "eu-west"); err != nil || got != 0 {
		t.Fatalf("GetQuota(other cluster) = %d, %v, want 0, nil", got, err)
	}
}

func TestSetQuota_RejectsNegative(t *testing.T) {
	t.Parallel()
	svc, _ := newSvc(t)
	ctx := t.Context()
	tn, _ := svc.Create(ctx, "acme", "Acme")

	if err := svc.SetQuota(ctx, tn.ID, "default", -1); !errors.Is(err, tenantapp.ErrCeilingInvalid) {
		t.Fatalf("SetQuota(-1) = %v, want ErrCeilingInvalid", err)
	}
}

func TestSetQuota_UnknownTenant(t *testing.T) {
	t.Parallel()
	svc, _ := newSvc(t)
	if err := svc.SetQuota(t.Context(), 999, "default", 5); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("SetQuota(unknown tenant) = %v, want ErrNotFound", err)
	}
}

func TestAssignRole(t *testing.T) {
	t.Parallel()
	svc, _ := newSvc(t)
	ctx := t.Context()
	tn, _ := svc.Create(ctx, "acme", "Acme")

	// Tenant-scoped role within a real tenant.
	if err := svc.AssignRole(ctx, ports.RoleGrant{Subject: "alice", RoleName: rbac.RoleTenantAdmin, TenantID: &tn.ID}); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	got, _ := svc.RolesFor(ctx, "alice")
	if len(got.Tenants[tn.ID]) != 1 {
		t.Fatalf("grants = %+v", got)
	}

	// Global role granted globally.
	if err := svc.AssignRole(ctx, ports.RoleGrant{Subject: "root", RoleName: rbac.RoleServiceProviderAdmin}); err != nil {
		t.Fatalf("AssignRole global: %v", err)
	}

	if err := svc.RevokeRole(ctx, "alice", rbac.RoleTenantAdmin, &tn.ID); err != nil {
		t.Fatalf("RevokeRole: %v", err)
	}
	got, _ = svc.RolesFor(ctx, "alice")
	if len(got.Tenants[tn.ID]) != 0 {
		t.Fatalf("grant not revoked: %+v", got)
	}
}

func TestAssignRoleValidation(t *testing.T) {
	t.Parallel()
	svc, _ := newSvc(t)
	ctx := t.Context()
	tn, _ := svc.Create(ctx, "acme", "Acme")
	missing := int64(999)

	tests := []struct {
		name string
		g    ports.RoleGrant
		want error
	}{
		{"unknown role", ports.RoleGrant{Subject: "a", RoleName: "wizard", TenantID: &tn.ID}, tenantapp.ErrUnknownRole},
		{"tenant role granted globally", ports.RoleGrant{Subject: "a", RoleName: rbac.RoleTenantEditor}, tenantapp.ErrGlobalRoleScoped},
		{"global role granted in tenant", ports.RoleGrant{Subject: "a", RoleName: rbac.RoleServiceProviderAdmin, TenantID: &tn.ID}, tenantapp.ErrGlobalRoleScoped},
		{"tenant does not exist", ports.RoleGrant{Subject: "a", RoleName: rbac.RoleTenantEditor, TenantID: &missing}, ports.ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := svc.AssignRole(ctx, tc.g); !errors.Is(err, tc.want) {
				t.Fatalf("AssignRole = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestListTenantRoles pins the roster use-case: entries come back for the
// named tenant only (never the global scope), and an unknown tenant is
// ErrNotFound rather than a silent empty list.
func TestListTenantRoles(t *testing.T) {
	t.Parallel()
	svc, _ := newSvc(t)
	ctx := t.Context()
	acme, _ := svc.Create(ctx, "acme", "Acme")
	if _, err := svc.Create(ctx, "globex", "Globex"); err != nil {
		t.Fatalf("Create globex: %v", err)
	}

	if err := svc.AssignRole(ctx, ports.RoleGrant{Subject: "alice", Email: "a@x", RoleName: rbac.RoleTenantAdmin, TenantID: &acme.ID, GrantedBy: "root"}); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	if err := svc.AssignRole(ctx, ports.RoleGrant{Subject: "root", RoleName: rbac.RoleServiceProviderAdmin}); err != nil {
		t.Fatalf("AssignRole global: %v", err)
	}

	entries, err := svc.ListTenantRoles(ctx, acme.ID)
	if err != nil {
		t.Fatalf("ListTenantRoles: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want only alice's grant (global scope excluded)", entries)
	}
	got := entries[0]
	if got.Subject != "alice" || got.Email != "a@x" || got.RoleName != rbac.RoleTenantAdmin || got.GrantedBy != "root" {
		t.Fatalf("entry = %+v", got)
	}
	if got.GrantedTime.IsZero() {
		t.Fatal("GrantedTime not stamped")
	}

	missing := int64(999)
	if _, err := svc.ListTenantRoles(ctx, missing); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("ListTenantRoles(unknown) = %v, want ErrNotFound", err)
	}
}
