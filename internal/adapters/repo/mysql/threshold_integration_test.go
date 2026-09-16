//go:build integration

package mysql_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/threshold"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/thresholdstoretest"
	"github.com/heridotlife/honryu/test/dbtest"

	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
)

// The MySQL adapter passes the same conformance suite as the fake, which is
// what keeps it interchangeable with the in-memory store. The suite speaks in
// scenario 7/8 and execution 1 / runs 42-43; the schema's foreign keys (the
// storage-level backstop the app layer's checks ride on) want those parents
// to exist, so the harness seeds them.
func TestMySQLThresholdStore_Contract(t *testing.T) {
	db := dbtest.StartMySQL(t)
	thresholdstoretest.Run(t, func(t *testing.T) ports.ThresholdStore {
		truncateAll(t, db)
		seedThresholdParents(t, db)
		return mysqladapter.NewRepository(db)
	})
}

// seedThresholdParents inserts the rows the suite's ids refer to: scenarios
// 7 and 8, execution 1, and reports for runs 42 and 43.
func seedThresholdParents(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []int64{7, 8} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO scenario (id, name, project_id, kind) VALUES (?,?,1,'portable')", id, fmt.Sprintf("scenario-%d", id)); err != nil {
			t.Fatalf("seed scenario %d: %v", id, err)
		}
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO execution (id, name, project_id) VALUES (1,'exec-1',1)"); err != nil {
		t.Fatalf("seed execution: %v", err)
	}
	now := time.Now().UTC()
	for _, runID := range []int64{42, 43} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO execution_report (run_id, execution_id, outcome, started_at, ended_at) VALUES (?,1,'passed',?,?)",
			runID, now, now); err != nil {
			t.Fatalf("seed report %d: %v", runID, err)
		}
	}
}

// seedScenarioRow inserts a bare scenario row and returns its id -- the
// suite's threshold rows hang off a scenario.
func seedScenarioRow(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	res, err := db.Exec("INSERT INTO scenario (name, project_id, kind) VALUES (?,?,'portable')", name, 1)
	if err != nil {
		t.Fatalf("seed scenario: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed scenario: %v", err)
	}
	return id
}

// seedExecutionAndReport inserts the execution + report rows a result's FKs
// point at.
func seedExecutionAndReport(t *testing.T, db *sql.DB, executionID, runID int64) {
	t.Helper()
	if _, err := db.Exec("INSERT INTO execution (id, name, project_id) VALUES (?,?,1)", executionID, fmt.Sprintf("exec-%d", executionID)); err != nil {
		t.Fatalf("seed execution: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO execution_report (run_id, execution_id, outcome, started_at, ended_at) VALUES (?,?,?,?,?)",
		runID, executionID, "passed", time.Now().UTC(), time.Now().UTC(),
	); err != nil {
		t.Fatalf("seed report: %v", err)
	}
}

// The result round-trip the shared suite cannot pin without sibling tables:
// results cascade away with their report, their scenario, and with a
// threshold the replace-all removes -- while unchanged thresholds keep their
// identity and their results across an idempotent re-save.
func TestMySQLThresholdStore_ResultLifecycle(t *testing.T) {
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()

	scenarioID := seedScenarioRow(t, db, "thresholded")
	seedExecutionAndReport(t, db, 1, 42)

	stored, err := repo.ReplaceThresholds(ctx, scenarioID, []threshold.Threshold{
		{ScenarioID: scenarioID, Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: 300},
		{ScenarioID: scenarioID, Metric: threshold.MetricErrorRate, Comparison: threshold.ComparisonLT, Value: 0.01},
	})
	if err != nil {
		t.Fatalf("ReplaceThresholds: %v", err)
	}
	satisfied := true
	first := threshold.Result{
		ThresholdID: stored[0].ID, ExecutionID: 1, RunID: 42,
		Metric: stored[0].Metric, Comparison: stored[0].Comparison, Value: stored[0].Value,
		Observed: ptr(250.0), Satisfied: &satisfied,
	}
	second := threshold.Result{
		ThresholdID: stored[1].ID, ExecutionID: 1, RunID: 42,
		Metric: stored[1].Metric, Comparison: stored[1].Comparison, Value: stored[1].Value,
		Satisfied: &satisfied,
	}
	if err := repo.SaveThresholdResults(ctx, []threshold.Result{first, second}); err != nil {
		t.Fatalf("SaveThresholdResults: %v", err)
	}

	// Re-saving the identical set keeps both thresholds' ids -- the
	// editor's no-change save must not orphan recorded results.
	again, err := repo.ReplaceThresholds(ctx, scenarioID, []threshold.Threshold{
		{ScenarioID: scenarioID, Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: 300},
		{ScenarioID: scenarioID, Metric: threshold.MetricErrorRate, Comparison: threshold.ComparisonLT, Value: 0.01},
	})
	if err != nil {
		t.Fatalf("idempotent replace: %v", err)
	}
	if again[0].ID != stored[0].ID || again[1].ID != stored[1].ID {
		t.Fatalf("ids moved: [%d %d] -> [%d %d]", stored[0].ID, stored[1].ID, again[0].ID, again[1].ID)
	}

	// Removing one threshold cascades exactly its results; the survivor's
	// stay.
	if _, err := repo.ReplaceThresholds(ctx, scenarioID, []threshold.Threshold{
		{ScenarioID: scenarioID, Metric: threshold.MetricErrorRate, Comparison: threshold.ComparisonLT, Value: 0.01},
	}); err != nil {
		t.Fatalf("narrowing replace: %v", err)
	}
	got, err := repo.ThresholdResultsForRun(ctx, 42)
	if err != nil {
		t.Fatalf("ThresholdResultsForRun: %v", err)
	}
	if len(got) != 1 || got[0].ThresholdID != stored[1].ID {
		t.Fatalf("results after narrowing replace = %+v, want only the surviving threshold's row", got)
	}
}

// Deleting the report (or its execution, or the scenario) takes the results
// with it -- the FK cascades the shared suite's fake cannot model.
func TestMySQLThresholdStore_ReportDeleteCascades(t *testing.T) {
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()

	scenarioID := seedScenarioRow(t, db, "cascading")
	seedExecutionAndReport(t, db, 1, 42)

	stored, err := repo.ReplaceThresholds(ctx, scenarioID, []threshold.Threshold{
		{ScenarioID: scenarioID, Metric: threshold.MetricErrorRate, Comparison: threshold.ComparisonLT, Value: 0.01},
	})
	if err != nil {
		t.Fatalf("ReplaceThresholds: %v", err)
	}
	satisfied := false
	if err := repo.SaveThresholdResults(ctx, []threshold.Result{{
		ThresholdID: stored[0].ID, ExecutionID: 1, RunID: 42,
		Metric: stored[0].Metric, Comparison: stored[0].Comparison, Value: stored[0].Value,
		Satisfied: &satisfied,
	}}); err != nil {
		t.Fatalf("SaveThresholdResults: %v", err)
	}
	if _, err := db.Exec("DELETE FROM execution_report WHERE run_id=42"); err != nil {
		t.Fatalf("delete report: %v", err)
	}
	got, err := repo.ThresholdResultsForRun(ctx, 42)
	if err != nil {
		t.Fatalf("ThresholdResultsForRun: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("results after report delete = %+v, want none (cascade)", got)
	}
}

// Missing metric in the report: observed_value and satisfied persist as NULL
// and reason as its text -- unknown is stored as unknown, never as a fail.
func TestMySQLThresholdStore_NullRoundTrip(t *testing.T) {
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()

	scenarioID := seedScenarioRow(t, db, "nullable")
	seedExecutionAndReport(t, db, 1, 42)

	stored, err := repo.ReplaceThresholds(ctx, scenarioID, []threshold.Threshold{
		{ScenarioID: scenarioID, Metric: threshold.MetricHTTPP99MS, Comparison: threshold.ComparisonLT, Value: 500},
	})
	if err != nil {
		t.Fatalf("ReplaceThresholds: %v", err)
	}
	if err := repo.SaveThresholdResults(ctx, []threshold.Result{{
		ThresholdID: stored[0].ID, ExecutionID: 1, RunID: 42,
		Metric: stored[0].Metric, Comparison: stored[0].Comparison, Value: stored[0].Value,
		Reason: "no p99 latency in the report",
	}}); err != nil {
		t.Fatalf("SaveThresholdResults: %v", err)
	}
	got, err := repo.ThresholdResultsForRun(ctx, 42)
	if err != nil {
		t.Fatalf("ThresholdResultsForRun: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("results = %d, want 1", len(got))
	}
	if got[0].Observed != nil || got[0].Satisfied != nil {
		t.Errorf("observed/satisfied = %v/%v, want NULL round-tripped", got[0].Observed, got[0].Satisfied)
	}
	if got[0].Reason != "no p99 latency in the report" {
		t.Errorf("reason = %q, want the stored text", got[0].Reason)
	}
}
