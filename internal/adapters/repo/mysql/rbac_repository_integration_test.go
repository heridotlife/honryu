//go:build integration

package mysql_test

import (
	"context"
	"testing"

	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
	"github.com/heridotlife/honryu/internal/domain/tenant"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/repositorytest"
	"github.com/heridotlife/honryu/test/dbtest"
)

func mustTenant(t *testing.T) tenant.Tenant {
	t.Helper()
	tn, err := tenant.New("acme", "Acme Inc")
	if err != nil {
		t.Fatalf("tenant.New: %v", err)
	}
	return tn
}

func TestMySQLTenantRepository_Contract(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repositorytest.RunTenantRepositoryContract(t, func(t *testing.T) ports.TenantRepository {
		truncateAll(t, db)
		return mysqladapter.NewRepository(db)
	})
}

func TestMySQLRoleAssignmentRepository_Contract(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repositorytest.RunRoleAssignmentRepositoryContract(t, func(t *testing.T) ports.RoleAssignmentRepository {
		truncateAll(t, db)
		return mysqladapter.NewRepository(db)
	})
}

// TestMySQLRBAC_ErrorsWhenDBClosed drives the DB-error branches of the RBAC repo.
func TestMySQLRBAC_ErrorsWhenDBClosed(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	tid := int64(1)
	ops := map[string]func() error{
		"CreateTenant":    func() error { _, e := repo.CreateTenant(ctx, mustTenant(t)); return e },
		"GetTenant":       func() error { _, e := repo.GetTenant(ctx, 1); return e },
		"ListTenants":     func() error { _, e := repo.ListTenants(ctx); return e },
		"SetTenantStatus": func() error { return repo.SetTenantStatus(ctx, 1, "ACTIVE") },
		"AssignRole":      func() error { return repo.AssignRole(ctx, ports.RoleGrant{Subject: "a", RoleName: "r"}) },
		"RevokeRole":      func() error { return repo.RevokeRole(ctx, "a", "r", &tid) },
		"RolesFor":        func() error { _, e := repo.RolesFor(ctx, "a"); return e },
		"ListTenantRoles": func() error { _, e := repo.ListTenantRoles(ctx, 1); return e },
	}
	for name, op := range ops {
		if err := op(); err == nil {
			t.Errorf("%s on closed db: want error, got nil", name)
		}
	}
}
