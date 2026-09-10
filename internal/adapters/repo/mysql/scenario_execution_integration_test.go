//go:build integration

package mysql_test

import (
	"context"
	"database/sql"
	"testing"

	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/ports/repositorytest"
	"github.com/heridotlife/honryu/test/dbtest"
)

func TestMySQLScenarioRepository_Contract(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repositorytest.RunScenarioRepositoryContract(t, func(t *testing.T) repositorytest.Repository {
		truncateAll(t, db)
		return mysqladapter.NewRepository(db)
	})
}

// ExecutionsWithActiveRunOnCluster (the delete guard) must only count
// executions on the given cluster that actually have an active run.
func TestMySQLExecutionsWithActiveRunOnCluster(t *testing.T) {
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()

	proj, err := project.New("p", "owner", "1")
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	projectID, err := repo.CreateProject(ctx, proj)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// On prod-eu, with an active run -> counted.
	running, err := repo.CreateExecution(ctx, execution.Execution{Name: "running", ProjectID: projectID, Cluster: "prod-eu"})
	if err != nil {
		t.Fatalf("CreateExecution(running): %v", err)
	}
	if _, err := repo.StartRun(ctx, running, ""); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// On prod-eu, no active run -> not counted.
	if _, err := repo.CreateExecution(ctx, execution.Execution{Name: "idle", ProjectID: projectID, Cluster: "prod-eu"}); err != nil {
		t.Fatalf("CreateExecution(idle): %v", err)
	}
	// On another cluster, active run -> not counted for prod-eu.
	other, err := repo.CreateExecution(ctx, execution.Execution{Name: "other", ProjectID: projectID, Cluster: "prod-us"})
	if err != nil {
		t.Fatalf("CreateExecution(other): %v", err)
	}
	if _, err := repo.StartRun(ctx, other, ""); err != nil {
		t.Fatalf("StartRun(other): %v", err)
	}

	got, err := repo.ExecutionsWithActiveRunOnCluster(ctx, "prod-eu")
	if err != nil {
		t.Fatalf("ExecutionsWithActiveRunOnCluster: %v", err)
	}
	if len(got) != 1 || got[0] != running {
		t.Fatalf("got %v, want [%d] (only the running prod-eu execution)", got, running)
	}
}

func TestMySQLExecutionRepository_Contract(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repositorytest.RunExecutionRepositoryContract(t, func(t *testing.T) repositorytest.Repository {
		truncateAll(t, db)
		return mysqladapter.NewRepository(db)
	})
}

// TestMySQLScenario_TenantAndAuditRoundTrip covers the nullable scenario columns.
func TestMySQLScenario_TenantAndAuditRoundTrip(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()

	tenant := int64(7)
	id, err := repo.CreateScenario(ctx, scenario.Scenario{
		Name: "scoped", ProjectID: 3, TenantID: &tenant, CreatedBy: "okta|a", UpdatedBy: "okta|b",
	})
	if err != nil {
		t.Fatalf("CreateScenario: %v", err)
	}
	got, err := repo.GetScenario(ctx, id)
	if err != nil {
		t.Fatalf("GetScenario: %v", err)
	}
	if got.TenantID == nil || *got.TenantID != tenant || got.CreatedBy != "okta|a" || got.UpdatedBy != "okta|b" {
		t.Fatalf("scenario round trip = %+v", got)
	}
}

// TestMySQLExecution_TenantAndCSVRoundTrip covers the nullable execution
// columns and the csv_split flag on create.
func TestMySQLExecution_TenantAndCSVRoundTrip(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()

	tenant := int64(9)
	id, err := repo.CreateExecution(ctx, execution.Execution{
		Name: "scoped", ProjectID: 3, CSVSplit: true, TenantID: &tenant, CreatedBy: "okta|c", UpdatedBy: "okta|d",
	})
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
	got, err := repo.GetExecution(ctx, id)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.TenantID == nil || *got.TenantID != tenant || !got.CSVSplit || got.CreatedBy != "okta|c" {
		t.Fatalf("execution round trip = %+v", got)
	}
}

// TestMySQLScenarioExecution_ErrorsWhenDBClosed drives the DB-error branches of
// every scenario and execution method by closing the pool first.
func TestMySQLScenarioExecution_ErrorsWhenDBClosed(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	ops := map[string]func() error{
		"CreateScenario":         func() error { _, e := repo.CreateScenario(ctx, scenario.Scenario{Name: "x", ProjectID: 1}); return e },
		"GetScenario":            func() error { _, e := repo.GetScenario(ctx, 1); return e },
		"ListScenariosByProject": func() error { _, e := repo.ListScenariosByProject(ctx, 1); return e },
		"DeleteScenario":         func() error { return repo.DeleteScenario(ctx, 1) },
		"AddScenarioFile":        func() error { return repo.AddScenarioFile(ctx, 1, "a", false) },
		"ScenarioFilesFor":       func() error { _, e := repo.ScenarioFilesFor(ctx, 1); return e },
		"DeleteScenarioFile":     func() error { return repo.DeleteScenarioFile(ctx, 1, "a", false) },
		"ScenarioInUse":          func() error { _, e := repo.ScenarioInUse(ctx, 1); return e },
		"CreateExecution": func() error {
			_, e := repo.CreateExecution(ctx, execution.Execution{Name: "x", ProjectID: 1})
			return e
		},
		"GetExecution":            func() error { _, e := repo.GetExecution(ctx, 1); return e },
		"ListExecutionsByProject": func() error { _, e := repo.ListExecutionsByProject(ctx, 1); return e },
		"DeleteExecution":         func() error { return repo.DeleteExecution(ctx, 1) },
		"AddExecutionFile":        func() error { return repo.AddExecutionFile(ctx, 1, "a") },
		"ExecutionFilesFor":       func() error { _, e := repo.ExecutionFilesFor(ctx, 1); return e },
		"DeleteExecutionFile":     func() error { return repo.DeleteExecutionFile(ctx, 1, "a") },
		"StoreLoadProfile":        func() error { return repo.StoreLoadProfile(ctx, 1, false, nil) },
		"LoadProfileFor":          func() error { _, e := repo.LoadProfileFor(ctx, 1); return e },
	}
	for name, op := range ops {
		if err := op(); err == nil {
			t.Errorf("%s on closed db: want error, got nil", name)
		}
	}
}

func truncateAll(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{
		"project", "scenario", "execution", "execution_scenario",
		"scenario_data", "scenario_test_file", "scenario_requests", "execution_data",
		"execution_run", "execution_run_history", "running_scenario",
		"execution_orphan_completion",
		"execution_launch", "execution_launch_history",
		"tenant", "role_grant",
		"execution_report", "report_error_signature",
		"execution_report_series",
		"run_share",
		"webhook",
		"report_digest", "digest_schedule",
		"report_progress_shard", "report_progress_label",
		"report_progress_second", "report_progress_signature",
		"reservation", "tenant_quota",
		"schedule", "schedule_occurrence", "scheduler_horizon_run",
		"campaign", "campaign_service", "execution_criteria",
		"calibration_job", "calibration_job_step", "capacity_profile",
		"cluster_registry",
	} {
		if _, err := db.Exec("TRUNCATE TABLE " + table); err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}
}
