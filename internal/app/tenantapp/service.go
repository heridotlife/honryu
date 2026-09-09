// Package tenantapp is the multi-tenancy administration use-case: it creates and
// lists tenants, changes their lifecycle status, and grants or revokes
// code-defined roles to subjects (globally or scoped to a tenant).
package tenantapp

import (
	"context"
	"errors"

	"github.com/heridotlife/honryu/internal/domain/rbac"
	"github.com/heridotlife/honryu/internal/domain/tenant"
	"github.com/heridotlife/honryu/internal/ports"
)

// ErrUnknownRole is returned when a grant names a role absent from the catalog.
var ErrUnknownRole = errors.New("tenantapp: unknown role")

// ErrGlobalRoleScoped is returned when a tenant-scoped role is granted globally,
// or a global-only role is granted within a tenant.
var ErrGlobalRoleScoped = errors.New("tenantapp: role scope mismatch")

// ErrCeilingInvalid is returned when a quota ceiling is negative.
var ErrCeilingInvalid = errors.New("tenantapp: quota ceiling must not be negative")

// Service implements the tenant/role administration use-cases.
type Service struct {
	tenants      ports.TenantRepository
	roles        ports.RoleAssignmentRepository
	reservations ports.ReservationRepository
	catalog      map[string]rbac.Role
}

// NewService wires the tenant service against the default role catalog.
func NewService(tenants ports.TenantRepository, roles ports.RoleAssignmentRepository, reservations ports.ReservationRepository) *Service {
	return &Service{tenants: tenants, roles: roles, reservations: reservations, catalog: rbac.DefaultCatalog()}
}

// Create validates and persists a new tenant, returning the stored row.
func (s *Service) Create(ctx context.Context, name, displayName string) (tenant.Tenant, error) {
	tn, err := tenant.New(name, displayName)
	if err != nil {
		return tenant.Tenant{}, err
	}
	id, err := s.tenants.CreateTenant(ctx, tn)
	if err != nil {
		return tenant.Tenant{}, err
	}
	return s.tenants.GetTenant(ctx, id)
}

// Get returns a tenant by id.
func (s *Service) Get(ctx context.Context, id int64) (tenant.Tenant, error) {
	return s.tenants.GetTenant(ctx, id)
}

// List returns all tenants.
func (s *Service) List(ctx context.Context) ([]tenant.Tenant, error) {
	return s.tenants.ListTenants(ctx)
}

// SetStatus validates and updates a tenant's lifecycle status.
func (s *Service) SetStatus(ctx context.Context, id int64, status string) error {
	if status != tenant.StatusActive && status != tenant.StatusSuspended {
		return tenant.ErrStatusInvalid
	}
	return s.tenants.SetTenantStatus(ctx, id, status)
}

// SetQuota sets a tenant's per-cluster engine quota ceiling. An unset ceiling
// reads as 0 (GetQuota) -- nothing runs until a ceiling is explicitly
// configured, never an accidental unlimited default.
func (s *Service) SetQuota(ctx context.Context, tenantID int64, cluster string, ceiling int) error {
	if ceiling < 0 {
		return ErrCeilingInvalid
	}
	if _, err := s.tenants.GetTenant(ctx, tenantID); err != nil {
		return err
	}
	return s.reservations.SetCeiling(ctx, tenantID, cluster, ceiling)
}

// GetQuota returns a tenant's per-cluster engine quota ceiling (0 if never
// configured).
func (s *Service) GetQuota(ctx context.Context, tenantID int64, cluster string) (int, error) {
	if _, err := s.tenants.GetTenant(ctx, tenantID); err != nil {
		return 0, err
	}
	return s.reservations.GetCeiling(ctx, tenantID, cluster)
}

// AssignRole grants a role to a subject after checking the role exists, its
// scope matches (tenant-scoped roles need a tenant, global roles must not have
// one), and any named tenant exists.
func (s *Service) AssignRole(ctx context.Context, g ports.RoleGrant) error {
	if err := s.validateGrant(ctx, g); err != nil {
		return err
	}
	return s.roles.AssignRole(ctx, g)
}

// RevokeRole removes a role grant from a subject.
func (s *Service) RevokeRole(ctx context.Context, subject, roleName string, tenantID *int64) error {
	return s.roles.RevokeRole(ctx, subject, roleName, tenantID)
}

// RolesFor resolves the grants held by a subject.
func (s *Service) RolesFor(ctx context.Context, subject string) (ports.RoleGrants, error) {
	return s.roles.RolesFor(ctx, subject)
}

// ListTenantRoles returns a tenant's member roster: every grant scoped to
// that tenant, with who granted it and when. An unknown tenant is
// ErrNotFound; a known tenant with no grants is an empty roster.
func (s *Service) ListTenantRoles(ctx context.Context, tenantID int64) ([]ports.RoleGrantEntry, error) {
	if _, err := s.tenants.GetTenant(ctx, tenantID); err != nil {
		return nil, err
	}
	return s.roles.ListTenantRoles(ctx, tenantID)
}

func (s *Service) validateGrant(ctx context.Context, g ports.RoleGrant) error {
	role, ok := s.catalog[g.RoleName]
	if !ok {
		return ErrUnknownRole
	}
	if role.TenantScoped && g.TenantID == nil {
		return ErrGlobalRoleScoped
	}
	if !role.TenantScoped && g.TenantID != nil {
		return ErrGlobalRoleScoped
	}
	if g.TenantID != nil {
		if _, err := s.tenants.GetTenant(ctx, *g.TenantID); err != nil {
			return err
		}
	}
	return nil
}
