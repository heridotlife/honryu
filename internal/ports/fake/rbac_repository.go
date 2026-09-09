package fake

import (
	"context"
	"sort"
	"time"

	"github.com/heridotlife/honryu/internal/domain/tenant"
	"github.com/heridotlife/honryu/internal/ports"
)

// grantRecord is one stored role assignment plus its recorded time, the
// fake's stand-in for role_grant's granted_at column.
type grantRecord struct {
	g  ports.RoleGrant
	at time.Time
}

// --- Tenants ----------------------------------------------------------------

// CreateTenant inserts a tenant, rejecting duplicate names.
func (s *Store) CreateTenant(_ context.Context, t tenant.Tenant) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.tenants {
		if existing.Name == t.Name {
			return 0, ports.ErrFileExists
		}
	}
	s.tenantSeq++
	t.ID = s.tenantSeq
	if t.CreatedTime.IsZero() {
		t.CreatedTime = s.now()
	}
	s.tenants[t.ID] = t
	return t.ID, nil
}

// GetTenant returns the tenant, or ErrNotFound.
func (s *Store) GetTenant(_ context.Context, id int64) (tenant.Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tenants[id]
	if !ok {
		return tenant.Tenant{}, ports.ErrNotFound
	}
	return t, nil
}

// ListTenants returns all tenants ordered by id.
func (s *Store) ListTenants(_ context.Context) ([]tenant.Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]tenant.Tenant, 0, len(s.tenants))
	for _, t := range s.tenants {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// SetTenantStatus updates a tenant's status.
func (s *Store) SetTenantStatus(_ context.Context, id int64, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tenants[id]
	if !ok {
		return ports.ErrNotFound
	}
	t.Status = status
	s.tenants[id] = t
	return nil
}

// --- Role grants ------------------------------------------------------------

// AssignRole records a grant idempotently.
func (s *Store) AssignRole(_ context.Context, g ports.RoleGrant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.grants {
		if sameGrant(existing.g, g.Subject, g.RoleName, g.TenantID) {
			return nil
		}
	}
	s.grants = append(s.grants, grantRecord{g: g, at: s.now()})
	return nil
}

// RevokeRole removes a matching grant, if any.
func (s *Store) RevokeRole(_ context.Context, subject, roleName string, tenantID *int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.grants[:0:0]
	for _, existing := range s.grants {
		if sameGrant(existing.g, subject, roleName, tenantID) {
			continue
		}
		kept = append(kept, existing)
	}
	s.grants = kept
	return nil
}

// RolesFor resolves all grants held by a subject.
func (s *Store) RolesFor(_ context.Context, subject string) (ports.RoleGrants, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := ports.RoleGrants{}
	for _, rec := range s.grants {
		g := rec.g
		if g.Subject != subject {
			continue
		}
		if g.TenantID == nil {
			out.Global = append(out.Global, g.RoleName)
			continue
		}
		if out.Tenants == nil {
			out.Tenants = make(map[int64][]string)
		}
		out.Tenants[*g.TenantID] = append(out.Tenants[*g.TenantID], g.RoleName)
	}
	return out, nil
}

// ListTenantRoles returns a tenant's grants ordered by subject then role.
func (s *Store) ListTenantRoles(_ context.Context, tenantID int64) ([]ports.RoleGrantEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ports.RoleGrantEntry
	for _, rec := range s.grants {
		if rec.g.TenantID == nil || *rec.g.TenantID != tenantID {
			continue
		}
		out = append(out, ports.RoleGrantEntry{
			Subject: rec.g.Subject, Email: rec.g.Email, RoleName: rec.g.RoleName,
			GrantedBy: rec.g.GrantedBy, GrantedTime: rec.at,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Subject != out[j].Subject {
			return out[i].Subject < out[j].Subject
		}
		return out[i].RoleName < out[j].RoleName
	})
	if out == nil {
		out = []ports.RoleGrantEntry{}
	}
	return out, nil
}

// sameGrant reports whether a stored grant matches subject/role/tenant, treating
// nil and matching tenant ids as equal scopes.
func sameGrant(g ports.RoleGrant, subject, roleName string, tenantID *int64) bool {
	if g.Subject != subject || g.RoleName != roleName {
		return false
	}
	switch {
	case g.TenantID == nil && tenantID == nil:
		return true
	case g.TenantID == nil || tenantID == nil:
		return false
	default:
		return *g.TenantID == *tenantID
	}
}
