//go:build integration

package mysql_test

import (
	"context"
	"testing"

	"github.com/heridotlife/honryu/migrations"

	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/test/dbtest"
)

// TestMySQLCalibrationJobScenarioBackfill pins migration 0065's backfill
// against real MySQL: a legacy calibration_job row (scenario_id NULL, the
// pre-0064 shape) learns its scenario from the execution's single bound
// entry via execution_scenario. The statement is read from migrations.FS --
// the exact bytes 0065 ships -- so the test tracks the migration, not a
// copy of it.
func TestMySQLCalibrationJobScenarioBackfill(t *testing.T) {
	db := dbtest.StartMySQL(t) // applies all migrations, 0064+0065 included
	truncateAll(t, db)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()

	pid, err := repo.CreateProject(ctx, project.Project{Name: "p"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	p, err := scenario.New("target", pid)
	if err != nil {
		t.Fatalf("build scenario: %v", err)
	}
	sid, err := repo.CreateScenario(ctx, p)
	if err != nil {
		t.Fatalf("create scenario: %v", err)
	}
	eid, err := repo.CreateExecution(ctx, execution.Execution{Name: "cal", ProjectID: pid})
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}
	// The calibration's own binding: exactly one scenario entry, the
	// invariant the backfill's straight JOIN relies on.
	if err := repo.StoreLoadProfile(ctx, eid, false, []loadprofile.Entry{
		{Name: "target", ScenarioID: sid, Engines: 1, Concurrency: 10, Duration: 30},
	}); err != nil {
		t.Fatalf("store load profile: %v", err)
	}

	// A legacy row: created the pre-0064 way -- execution_id only,
	// scenario_id NULL (what CreateCalibrationJob(ctx, id, 0) stores).
	jobID, err := repo.CreateCalibrationJob(ctx, eid, 0)
	if err != nil {
		t.Fatalf("create legacy job: %v", err)
	}
	job, err := repo.GetCalibrationJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get legacy job: %v", err)
	}
	if job.ScenarioID != 0 {
		t.Fatalf("pre-backfill ScenarioID = %d, want 0 (the NULL/unknown marker)", job.ScenarioID)
	}

	// Re-execute 0065 verbatim, from the embedded migrations.
	stmt, err := migrations.FS.ReadFile("0065_calibration_job_scenario_backfill.sql")
	if err != nil {
		t.Fatalf("read 0065 from migrations.FS: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(stmt)); err != nil {
		t.Fatalf("exec 0065: %v", err)
	}

	job, err = repo.GetCalibrationJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get backfilled job: %v", err)
	}
	if job.ScenarioID != sid {
		t.Fatalf("post-backfill ScenarioID = %d, want %d -- resolved via execution_scenario", job.ScenarioID, sid)
	}
}
