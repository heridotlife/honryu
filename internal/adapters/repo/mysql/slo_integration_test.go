//go:build integration

package mysql_test

import (
	"context"
	"testing"

	"github.com/heridotlife/honryu/internal/domain/slo"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/slotest"
	"github.com/heridotlife/honryu/test/dbtest"

	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
)

// The MySQL adapter passes the same conformance suite as the fake, which is
// what keeps it interchangeable with the in-memory store.
func TestMySQLSLOStore_Contract(t *testing.T) {
	db := dbtest.StartMySQL(t)
	slotest.Run(t, func(t *testing.T) ports.SLOStore {
		truncateAll(t, db)
		return mysqladapter.NewRepository(db)
	})
}

// The (project_id, name) unique key is the storage-level backstop of the
// use-case's duplicate pre-check: two concurrent creates with the same name
// under one project race past the check and the driver rejects the second.
func TestMySQLSLOStore_UniqueNamePerProject(t *testing.T) {
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()

	p95 := 250.0
	first := slo.SLO{ProjectID: 7, Name: "checkout", TargetP95MS: &p95}
	if _, err := repo.CreateSLO(ctx, first); err != nil {
		t.Fatalf("CreateSLO: %v", err)
	}
	if _, err := repo.CreateSLO(ctx, first); err == nil {
		t.Error("second CreateSLO with the same (project_id, name) = nil error, want the unique key's rejection")
	}
	// The same name under a different project is a different row.
	other := first
	other.ProjectID = 8
	if _, err := repo.CreateSLO(ctx, other); err != nil {
		t.Errorf("CreateSLO(same name, other project) = %v, want nil", err)
	}

	// Round-trip the nullable targets: an SLO with only one target set
	// reads back with the other two nil, not zero.
	only := slo.SLO{ProjectID: 7, Name: "ratio-only", TargetSuccessRatio: ptr(0.99)}
	id, err := repo.CreateSLO(ctx, only)
	if err != nil {
		t.Fatalf("CreateSLO(ratio-only): %v", err)
	}
	got, err := repo.GetSLO(ctx, 7, id)
	if err != nil {
		t.Fatalf("GetSLO: %v", err)
	}
	if got.TargetSuccessRatio == nil || *got.TargetSuccessRatio != 0.99 {
		t.Errorf("target_success_ratio = %v, want 0.99", got.TargetSuccessRatio)
	}
	if got.TargetP95MS != nil || got.TargetErrorRate != nil {
		t.Errorf("unset targets = %v / %v, want nil / nil", got.TargetP95MS, got.TargetErrorRate)
	}
}

func ptr(v float64) *float64 { return &v }
