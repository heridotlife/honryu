package ports

import (
	"context"
	"time"
)

// RoleGrant is a single persisted assignment of a role to a subject, optionally
// scoped to a tenant. A nil TenantID is a global (service-provider) grant.
type RoleGrant struct {
	Subject   string
	Email     string
	RoleName  string
	TenantID  *int64
	GrantedBy string
}

// RoleGrantEntry is a persisted grant as read back from the store: the grant
// plus the audit metadata the store stamps. It backs tenant rosters
// (ListTenantRoles), which surface who holds what and who said so.
type RoleGrantEntry struct {
	Subject     string
	Email       string
	RoleName    string
	GrantedBy   string
	GrantedTime time.Time
}

// RoleGrants is the resolved set of a subject's persisted grants: global role
// names plus per-tenant role names. It maps directly onto account.Account.
type RoleGrants struct {
	Global  []string
	Tenants map[int64][]string
}

// RoleAssignmentRepository persists role grants for subjects.
type RoleAssignmentRepository interface {
	// AssignRole records a grant. It is idempotent: re-granting the same
	// subject/role/tenant is a no-op rather than an error.
	AssignRole(ctx context.Context, g RoleGrant) error
	// RevokeRole removes a grant. Revoking a grant that does not exist is a
	// no-op. A nil tenantID targets the global grant.
	RevokeRole(ctx context.Context, subject, roleName string, tenantID *int64) error
	// RolesFor resolves all grants held by a subject.
	RolesFor(ctx context.Context, subject string) (RoleGrants, error)
	// ListTenantRoles returns a tenant's grants -- the roster -- ordered by
	// subject then role. The global scope is never included. A tenant with no
	// grants yields an empty slice; tenant existence is the caller's check.
	ListTenantRoles(ctx context.Context, tenantID int64) ([]RoleGrantEntry, error)
}
