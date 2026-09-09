package mysql

import (
	"context"
	"database/sql"

	"github.com/heridotlife/honryu/internal/domain/tenant"
	"github.com/heridotlife/honryu/internal/ports"
)

// globalScope is the tenant_id sentinel for a global (service-provider) grant.
// Using 0 rather than NULL keeps the uniqueness guard on role_grant
// meaningful so re-granting is a no-op.
const globalScope int64 = 0

var (
	_ ports.TenantRepository         = (*Repository)(nil)
	_ ports.RoleAssignmentRepository = (*Repository)(nil)
)

// --- Tenants ----------------------------------------------------------------

// CreateTenant inserts a tenant, mapping a duplicate name to ErrFileExists.
func (r *Repository) CreateTenant(ctx context.Context, t tenant.Tenant) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		"INSERT INTO tenant (name, display_name, status) VALUES (?, ?, ?)",
		t.Name, t.DisplayName, t.Status)
	if err != nil {
		if isDuplicateKey(err) {
			return 0, ports.ErrFileExists
		}
		return 0, err
	}
	return res.LastInsertId()
}

// GetTenant returns the tenant, or ErrNotFound.
func (r *Repository) GetTenant(ctx context.Context, id int64) (tenant.Tenant, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT id, name, display_name, status, created_time FROM tenant WHERE id = ?", id)
	return scanTenant(row)
}

// ListTenants returns all tenants ordered by id.
func (r *Repository) ListTenants(ctx context.Context) ([]tenant.Tenant, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, name, display_name, status, created_time FROM tenant ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []tenant.Tenant
	for rows.Next() {
		t, scanErr := scanTenant(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SetTenantStatus updates a tenant's status, or returns ErrNotFound.
func (r *Repository) SetTenantStatus(ctx context.Context, id int64, status string) error {
	var exists int
	err := r.db.QueryRowContext(ctx, "SELECT 1 FROM tenant WHERE id = ?", id).Scan(&exists)
	if err == sql.ErrNoRows {
		return ports.ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, "UPDATE tenant SET status = ? WHERE id = ?", status, id)
	return err
}

func scanTenant(s rowScanner) (tenant.Tenant, error) {
	var t tenant.Tenant
	if err := s.Scan(&t.ID, &t.Name, &t.DisplayName, &t.Status, &t.CreatedTime); err != nil {
		if err == sql.ErrNoRows {
			return tenant.Tenant{}, ports.ErrNotFound
		}
		return tenant.Tenant{}, err
	}
	return t, nil
}

// --- Role grants ------------------------------------------------------------

// AssignRole records a grant idempotently (INSERT IGNORE over the uniqueness
// guard).
func (r *Repository) AssignRole(ctx context.Context, g ports.RoleGrant) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT IGNORE INTO role_grant (subject, email, role_name, tenant_id, granted_by) VALUES (?, ?, ?, ?, ?)",
		g.Subject, g.Email, g.RoleName, scopeValue(g.TenantID), g.GrantedBy)
	return err
}

// RevokeRole removes a matching grant, if any.
func (r *Repository) RevokeRole(ctx context.Context, subject, roleName string, tenantID *int64) error {
	_, err := r.db.ExecContext(ctx,
		"DELETE FROM role_grant WHERE subject = ? AND role_name = ? AND tenant_id = ?",
		subject, roleName, scopeValue(tenantID))
	return err
}

// RolesFor resolves all grants held by a subject.
func (r *Repository) RolesFor(ctx context.Context, subject string) (ports.RoleGrants, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT role_name, tenant_id FROM role_grant WHERE subject = ?", subject)
	if err != nil {
		return ports.RoleGrants{}, err
	}
	defer func() { _ = rows.Close() }()

	out := ports.RoleGrants{}
	for rows.Next() {
		var (
			roleName string
			tenantID int64
		)
		if err := rows.Scan(&roleName, &tenantID); err != nil {
			return ports.RoleGrants{}, err
		}
		if tenantID == globalScope {
			out.Global = append(out.Global, roleName)
			continue
		}
		if out.Tenants == nil {
			out.Tenants = make(map[int64][]string)
		}
		out.Tenants[tenantID] = append(out.Tenants[tenantID], roleName)
	}
	if err := rows.Err(); err != nil {
		return ports.RoleGrants{}, err
	}
	return out, nil
}

// ListTenantRoles returns a tenant's grants ordered by subject then role.
// The global scope (tenant_id 0) is never included.
func (r *Repository) ListTenantRoles(ctx context.Context, tenantID int64) ([]ports.RoleGrantEntry, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT subject, email, role_name, granted_by, granted_at FROM role_grant WHERE tenant_id = ? ORDER BY subject, role_name",
		tenantID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []ports.RoleGrantEntry{}
	for rows.Next() {
		var e ports.RoleGrantEntry
		if err := rows.Scan(&e.Subject, &e.Email, &e.RoleName, &e.GrantedBy, &e.GrantedTime); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// scopeValue maps a nil tenant scope to the global sentinel.
func scopeValue(tenantID *int64) int64 {
	if tenantID == nil {
		return globalScope
	}
	return *tenantID
}
