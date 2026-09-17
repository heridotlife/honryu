//go:build integration

package mysql_test

import (
	"context"

	"testing"

	"github.com/heridotlife/honryu/internal/ports"

	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/test/dbtest"
)

func TestMySQLScenarioVersionStore(t *testing.T) {
	db := dbtest.StartMySQL(t) // applies all migrations
	truncateAll(t, db)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()

	// Setup: a project + scenario
	pid, err := repo.CreateProject(ctx, project.Project{Name: "test-project"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create a native scenario
	native, err := scenario.NewNative("test-scenario", pid, taurus.ExecutorK6)
	if err != nil {
		t.Fatalf("build scenario: %v", err)
	}
	sid, err := repo.CreateScenario(ctx, native)
	if err != nil {
		t.Fatalf("create scenario: %v", err)
	}

	// Create a test file and data file
	if err := repo.AddScenarioFile(ctx, sid, "load.js", true); err != nil {
		t.Fatalf("add test file: %v", err)
	}
	if err := repo.AddScenarioFile(ctx, sid, "users.csv", false); err != nil {
		t.Fatalf("add data file: %v", err)
	}
	if err := repo.SetScenarioRequests(ctx, sid, []byte("requests:\n  - url: /health\n")); err != nil {
		t.Fatalf("store requests: %v", err)
	}

	// 1. AppendScenarioVersion (create v1, then v2)
	v1, err := repo.AppendScenarioVersion(ctx, sid, ports.ScenarioSnapshot{
		ID: sid, Name: "test-scenario", ProjectID: pid,
		Kind: string(scenario.KindNative), Engine: string(taurus.ExecutorK6),
		TestFile: "load.js", Data: []string{"users.csv"},
		Requests:  "requests:\n  - url: /health\n",
		CreatedBy: "alice", UpdatedBy: "alice",
	}, "alice")
	if err != nil {
		t.Fatalf("append v1: %v", err)
	}
	if v1 != 1 {
		t.Errorf("v1 = %d, want 1", v1)
	}

	v2, err := repo.AppendScenarioVersion(ctx, sid, ports.ScenarioSnapshot{
		ID: sid, Name: "test-scenario", ProjectID: pid,
		Kind: string(scenario.KindNative), Engine: string(taurus.ExecutorK6),
		TestFile: "load.js", Data: []string{"users.csv"},
		Requests:  "requests:\n  - url: /health\n  - url: /api\n",
		CreatedBy: "bob", UpdatedBy: "bob",
	}, "bob")
	if err != nil {
		t.Fatalf("append v2: %v", err)
	}
	if v2 != 2 {
		t.Errorf("v2 = %d, want 2", v2)
	}

	// 2. ListScenarioVersions (newest first)
	list, err := repo.ListScenarioVersions(ctx, sid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list = %d, want 2", len(list))
	}
	if list[0].Version != 2 || list[1].Version != 1 {
		t.Errorf("list order = %v, want v2,v1 (newest first)", list)
	}
	if list[0].CreatedBy == nil || *list[0].CreatedBy != "bob" {
		t.Errorf("v2 created_by = %v, want bob", list[0].CreatedBy)
	}
	if list[1].CreatedBy == nil || *list[1].CreatedBy != "alice" {
		t.Errorf("v1 created_by = %v, want alice", list[1].CreatedBy)
	}

	// 3. ScenarioVersion (get full snapshot)
	v1Full, err := repo.ScenarioVersion(ctx, sid, 1)
	if err != nil {
		t.Fatalf("ScenarioVersion(1): %v", err)
	}
	if v1Full.Version != 1 || v1Full.ScenarioID != sid {
		t.Errorf("v1 meta = %d/%d, want 1/%d", v1Full.Version, v1Full.ScenarioID, sid)
	}
	if v1Full.Snapshot.Name != "test-scenario" || v1Full.Snapshot.TestFile != "load.js" {
		t.Errorf("v1 snapshot = %+v, want test-scenario with load.js", v1Full.Snapshot)
	}
	if len(v1Full.Snapshot.Data) != 1 || v1Full.Snapshot.Data[0] != "users.csv" {
		t.Errorf("v1 data = %v, want [users.csv]", v1Full.Snapshot.Data)
	}
	if v1Full.Snapshot.Requests != "requests:\n  - url: /health\n" {
		t.Errorf("v1 requests = %q, want original", v1Full.Snapshot.Requests)
	}

	v2Full, err := repo.ScenarioVersion(ctx, sid, 2)
	if err != nil {
		t.Fatalf("ScenarioVersion(2): %v", err)
	}
	if v2Full.Snapshot.Requests != "requests:\n  - url: /health\n  - url: /api\n" {
		t.Errorf("v2 requests = %q, want updated", v2Full.Snapshot.Requests)
	}

	// 4. Unknown version returns sentinel
	_, err = repo.ScenarioVersion(ctx, sid, 99)
	if err != ports.ErrScenarioVersionNotFound {
		t.Errorf("unknown version err = %v, want ErrScenarioVersionNotFound", err)
	}
	_, err = repo.ScenarioVersion(ctx, 99999, 1)
	if err != ports.ErrScenarioVersionNotFound {
		t.Errorf("other scenario err = %v, want ErrScenarioVersionNotFound", err)
	}

	// 5. AppendOnlyOldRowsNeverChange (v1 identity preserved across appends)
	before, err := repo.ScenarioVersion(ctx, sid, 1)
	if err != nil {
		t.Fatalf("read v1 before v3: %v", err)
	}

	v3, err := repo.AppendScenarioVersion(ctx, sid, ports.ScenarioSnapshot{
		ID: sid, Name: "test-scenario", ProjectID: pid,
		Kind: string(scenario.KindNative), Engine: string(taurus.ExecutorK6),
		TestFile: "load.js", Data: []string{"users.csv", "extra.csv"},
		Requests:  "requests:\n  - url: /v3\n",
		CreatedBy: "carol", UpdatedBy: "carol",
	}, "carol")
	if err != nil {
		t.Fatalf("append v3: %v", err)
	}
	if v3 != 3 {
		t.Errorf("v3 = %d, want 3", v3)
	}

	after, err := repo.ScenarioVersion(ctx, sid, 1)
	if err != nil {
		t.Fatalf("read v1 after v3: %v", err)
	}
	if after.Version != before.Version || after.ID != before.ID || after.CreatedTime != before.CreatedTime ||
		after.Snapshot.Name != before.Snapshot.Name || after.Snapshot.Requests != before.Snapshot.Requests {
		t.Errorf("v1 changed across v3 append: before %+v after %+v", before, after)
	}
	if after.CreatedBy == nil || *after.CreatedBy != "alice" {
		t.Errorf("v1 created_by = %v, want alice unchanged", after.CreatedBy)
	}
}
