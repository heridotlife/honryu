//go:build integration

// The MySQL-specific fan-out pins (phase 88): the JSON columns and the
// JSON_CONTAINS routing query are SQL-shaped behaviour the in-memory fakes
// cannot lie about -- a malformed array, a missed quote, or a query that
// stops matching after the run closes would only ever show up here.
package mysql_test

import (
	"context"
	"testing"
	"time"

	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/test/dbtest"
)

// fanOutDB boots one migrated, truncated MySQL and returns the repository
// plus a project to hang executions on -- the shared boot the four pins
// below each use once.
func fanOutDB(t *testing.T) (*mysqladapter.Repository, int64) {
	t.Helper()
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	repo := mysqladapter.NewRepository(db)
	p, _ := project.New("fanout-proj", "honryu", "")
	projectID, err := repo.CreateProject(context.Background(), p)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return repo, projectID
}

// mkExecution creates one execution on the seeded project; over mutates it
// before it is saved (e.g. setting FanOutTargets).
func mkExecution(t *testing.T, repo *mysqladapter.Repository, projectID int64, name string, over func(*execution.Execution)) int64 {
	t.Helper()
	coll, err := execution.New(name, projectID)
	if err != nil {
		t.Fatalf("execution.New: %v", err)
	}
	if over != nil {
		over(&coll)
	}
	id, err := repo.CreateExecution(context.Background(), coll)
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
	return id
}

// The fanout_targets JSON column round-trips: nil stays NULL (and reads back
// nil, never an empty array) for an ordinary execution; a fan-out execution's
// target list survives with its order kept.
func TestMySQLFanOut_ExecutionTargetsRoundTrip(t *testing.T) {
	repo, projectID := fanOutDB(t)
	ctx := context.Background()

	fanOutID := mkExecution(t, repo, projectID, "everywhere", func(c *execution.Execution) {
		c.FanOutTargets = []string{"eu-1", "us-1"}
	})
	got, err := repo.GetExecution(ctx, fanOutID)
	if err != nil {
		t.Fatalf("GetExecution (fan-out): %v", err)
	}
	if len(got.FanOutTargets) != 2 || got.FanOutTargets[0] != "eu-1" || got.FanOutTargets[1] != "us-1" {
		t.Fatalf("fanout_targets = %q, want [eu-1 us-1] in order", got.FanOutTargets)
	}

	// The pre-fan-out shape: NULL column, nil field.
	ordinaryID := mkExecution(t, repo, projectID, "plain", nil)
	got, err = repo.GetExecution(ctx, ordinaryID)
	if err != nil {
		t.Fatalf("GetExecution (ordinary): %v", err)
	}
	if got.FanOutTargets != nil {
		t.Fatalf("ordinary fanout_targets = %q, want nil", got.FanOutTargets)
	}
}

// ExecutionsWithActiveRunOnCluster must match a mid-flight fan-out run on
// EVERY target (JSON_CONTAINS over the quoted names) while the run is open,
// and stop matching once it closes -- the cluster delete guard and the
// Clusters page's fan-out-in-progress view both ride this.
func TestMySQLFanOut_ExecutionsWithActiveRunOnCluster(t *testing.T) {
	repo, projectID := fanOutDB(t)
	ctx := context.Background()

	fanOutID := mkExecution(t, repo, projectID, "everywhere", func(c *execution.Execution) {
		c.FanOutTargets = []string{"eu-1", "us-1"}
	})
	if _, err := repo.StartRun(ctx, fanOutID, ""); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for _, cluster := range []string{"eu-1", "us-1"} {
		ids, err := repo.ExecutionsWithActiveRunOnCluster(ctx, cluster)
		if err != nil {
			t.Fatalf("ExecutionsWithActiveRunOnCluster(%s): %v", cluster, err)
		}
		if len(ids) != 1 || ids[0] != fanOutID {
			t.Fatalf("cluster %q active-run ids = %v, want [%d]", cluster, ids, fanOutID)
		}
	}
	// A cluster the fan-out does not target sees nothing.
	ids, err := repo.ExecutionsWithActiveRunOnCluster(ctx, "ap-1")
	if err != nil {
		t.Fatalf("ExecutionsWithActiveRunOnCluster(ap-1): %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("unrelated cluster matched %v, want none", ids)
	}

	if err := repo.StopRun(ctx, fanOutID); err != nil {
		t.Fatalf("StopRun: %v", err)
	}
	ids, err = repo.ExecutionsWithActiveRunOnCluster(ctx, "eu-1")
	if err != nil {
		t.Fatalf("ExecutionsWithActiveRunOnCluster after close: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("closed fan-out run still matched on eu-1: %v", ids)
	}
}

// The cluster_results JSON column round-trips: rows keep their order and
// fields, and a report saved without them reads nil back (NULL column, not
// an empty array the wire would grow).
func TestMySQLFanOut_ReportClusterResultsRoundTrip(t *testing.T) {
	repo, projectID := fanOutDB(t)
	ctx := context.Background()

	fanOutID := mkExecution(t, repo, projectID, "everywhere", func(c *execution.Execution) {
		c.FanOutTargets = []string{"eu-1", "us-1"}
	})
	runID, err := repo.StartRun(ctx, fanOutID, "")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if err := repo.SaveReport(ctx, report.Report{
		ExecutionID: fanOutID, RunID: runID,
		Outcome:   taurus.OutcomePassed,
		StartedAt: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
		EndedAt:   time.Date(2026, 9, 18, 12, 5, 0, 0, time.UTC),
		ClusterResults: []report.ClusterResult{
			{Cluster: "eu-1", Outcome: taurus.OutcomePassed, Samples: 100, Failed: 10},
			{Cluster: "us-1", Outcome: taurus.OutcomePassed, Samples: 50, Failed: 20},
		},
	}); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	got, err := repo.GetReport(ctx, runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if len(got.ClusterResults) != 2 {
		t.Fatalf("cluster_results = %+v, want two rows", got.ClusterResults)
	}
	eu, us := got.ClusterResults[0], got.ClusterResults[1]
	if eu.Cluster != "eu-1" || eu.Samples != 100 || eu.Failed != 10 || eu.Outcome != taurus.OutcomePassed {
		t.Errorf("eu-1 row = %+v, want the saved row", eu)
	}
	if us.Cluster != "us-1" || us.Samples != 50 || us.Failed != 20 {
		t.Errorf("us-1 row = %+v, want the saved row", us)
	}

	// A single-cluster report stays NULL: no phantom empty array.
	ordinaryID := mkExecution(t, repo, projectID, "plain", nil)
	ordRun, err := repo.StartRun(ctx, ordinaryID, "")
	if err != nil {
		t.Fatalf("StartRun (ordinary): %v", err)
	}
	if err := repo.SaveReport(ctx, report.Report{
		ExecutionID: ordinaryID, RunID: ordRun, Outcome: taurus.OutcomePassed,
		StartedAt: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
		EndedAt:   time.Date(2026, 9, 18, 12, 5, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("SaveReport (ordinary): %v", err)
	}
	ord, err := repo.GetReport(ctx, ordRun)
	if err != nil {
		t.Fatalf("GetReport (ordinary): %v", err)
	}
	if ord.ClusterResults != nil {
		t.Fatalf("ordinary report cluster_results = %+v, want nil", ord.ClusterResults)
	}
}
