//go:build integration

package mysql_test

import (
	"context"
	"testing"

	"github.com/heridotlife/honryu/migrations"

	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/test/dbtest"
)

// TestMySQLScenarioVersionsBackfill pins migration 0070 against real MySQL:
// every scenario that exists when it runs becomes version 1, with the full
// snapshot shape the Go type unmarshals -- row fields plus the file records
// and the stored requests fragment. The statement is read from
// migrations.FS -- the exact bytes 0070 ships -- so the test tracks the
// migration, not a copy of it.
func TestMySQLScenarioVersionsBackfill(t *testing.T) {
	db := dbtest.StartMySQL(t) // applies all migrations, 0070 included
	truncateAll(t, db)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()

	// A native scenario with a test file, a data file, and a tenant-less
	// project row behind it -- the richest shape the backfill must capture.
	pid, err := repo.CreateProject(ctx, project.Project{Name: "p"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	native, err := scenario.NewNative("scripted", pid, taurus.ExecutorK6)
	if err != nil {
		t.Fatalf("build native scenario: %v", err)
	}
	sid, err := repo.CreateScenario(ctx, native)
	if err != nil {
		t.Fatalf("create scenario: %v", err)
	}
	if err := repo.AddScenarioFile(ctx, sid, "load.js", true); err != nil {
		t.Fatalf("add test file: %v", err)
	}
	if err := repo.AddScenarioFile(ctx, sid, "users.csv", false); err != nil {
		t.Fatalf("add data file: %v", err)
	}
	if err := repo.SetScenarioRequests(ctx, sid, []byte("requests:\n  - url: /health\n")); err != nil {
		t.Fatalf("store requests: %v", err)
	}

	// A bare portable scenario: empty files, no requests, no actor.
	bare, err := scenario.New("bare", pid)
	if err != nil {
		t.Fatalf("build bare scenario: %v", err)
	}
	bareID, err := repo.CreateScenario(ctx, bare)
	if err != nil {
		t.Fatalf("create bare scenario: %v", err)
	}

	// Re-execute 0070 verbatim, from the embedded migrations.
	stmt, err := migrations.FS.ReadFile("0070_scenario_versions_backfill.sql")
	if err != nil {
		t.Fatalf("read 0070 from migrations.FS: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(stmt)); err != nil {
		t.Fatalf("exec 0070: %v", err)
	}

	// The rich scenario's v1 snapshot round-trips into the Go type with
	// every joined field in place.
	v1, err := repo.ScenarioVersion(ctx, sid, 1)
	if err != nil {
		t.Fatalf("ScenarioVersion(1): %v", err)
	}
	snap := v1.Snapshot
	if snap.Name != "scripted" || snap.ProjectID != pid || snap.Kind != string(scenario.KindNative) || snap.Engine != string(taurus.ExecutorK6) {
		t.Errorf("v1 row fields = %+v, want the scenario's own", snap)
	}
	if snap.TestFile != "load.js" {
		t.Errorf("v1 test_file = %q, want load.js (joined from scenario_test_file)", snap.TestFile)
	}
	if len(snap.Data) != 1 || snap.Data[0] != "users.csv" {
		t.Errorf("v1 data = %v, want [users.csv] (joined from scenario_data)", snap.Data)
	}
	if snap.Requests != "requests:\n  - url: /health\n" {
		t.Errorf("v1 requests = %q, want the stored fragment (joined from scenario_requests)", snap.Requests)
	}
	if v1.CreatedTime.IsZero() {
		t.Error("v1 created_time is zero; the history list shows it")
	}

	// The bare scenario still backfills: empty file records, an array (not
	// null) for data, and a no-actor created_by.
	bv1, err := repo.ScenarioVersion(ctx, bareID, 1)
	if err != nil {
		t.Fatalf("bare ScenarioVersion(1): %v", err)
	}
	if bv1.Snapshot.Name != "bare" || bv1.Snapshot.TestFile != "" || bv1.Snapshot.Requests != "" {
		t.Errorf("bare v1 = %+v, want the bare row with empty file fields", bv1.Snapshot)
	}
	if len(bv1.Snapshot.Data) != 0 {
		t.Errorf("bare v1 data = %v, want an empty list", bv1.Snapshot.Data)
	}
	if bv1.CreatedBy != nil {
		t.Errorf("bare v1 created_by = %q, want nil (no principal existed)", *bv1.CreatedBy)
	}

	// Versions list as exactly one row per scenario, newest (only) first.
	for _, id := range []int64{sid, bareID} {
		list, err := repo.ListScenarioVersions(ctx, id)
		if err != nil {
			t.Fatalf("ListScenarioVersions(%d): %v", id, err)
		}
		if len(list) != 1 || list[0].Version != 1 {
			t.Errorf("list for %d = %+v, want exactly v1", id, list)
		}
	}

	// Re-applying is a no-op (the WHERE NOT EXISTS guard): still one
	// version per scenario afterwards.
	if _, err := db.ExecContext(ctx, string(stmt)); err != nil {
		t.Fatalf("re-exec 0070: %v", err)
	}
	list, err := repo.ListScenarioVersions(ctx, sid)
	if err != nil || len(list) != 1 {
		t.Errorf("list after re-apply = %+v (%v), want still exactly v1", list, err)
	}
}
