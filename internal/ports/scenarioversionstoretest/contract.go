// Package scenarioversionstoretest is the shared conformance suite every
// ScenarioVersionStore must pass, fake and real alike.
package scenarioversionstoretest

import (
	"context"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/ports"
)

// NewStore builds a store with no versions in it.
type NewStore func(t *testing.T) ports.ScenarioVersionStore

// Run exercises ScenarioVersionStore behaviour.
func Run(t *testing.T, newStore NewStore) {
	t.Helper()
	ctx := context.Background()

	// mk builds a snapshot for scenario sid with distinct field values, so
	// a round-trip that mangles anything is visible.
	mk := func(sid int64, name string) ports.ScenarioSnapshot {
		tenant := int64(4)
		return ports.ScenarioSnapshot{
			ID: sid, Name: name, ProjectID: 2,
			Kind: "native", Engine: "k6",
			TenantID:  &tenant,
			CreatedBy: "op", UpdatedBy: "editor",
			CreatedTime: time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC),
			TestFile:    "load.js",
			Data:        []string{"users.csv", "postcodes.csv"},
			Requests:    "scenarios:\n  load: {requests: []}\n",
		}
	}

	t.Run("ListEmptyIsNotNilAndScoped", func(t *testing.T) {
		s := newStore(t)
		got, err := s.ListScenarioVersions(ctx, 7)
		if err != nil {
			t.Fatalf("ListScenarioVersions: %v", err)
		}
		if got == nil || len(got) != 0 {
			t.Errorf("list = %v, want empty non-nil", got)
		}
	})

	t.Run("AppendAssignsMaxPlusOnePerScenario", func(t *testing.T) {
		s := newStore(t)
		// Two scenarios interleaved: versions are per scenario, never a
		// global counter.
		appends := []struct {
			sid  int64
			want int
		}{{7, 1}, {8, 1}, {7, 2}, {7, 3}, {8, 2}}
		for _, a := range appends {
			got, err := s.AppendScenarioVersion(ctx, a.sid, mk(a.sid, "v"), "")
			if err != nil {
				t.Fatalf("append scenario %d: %v", a.sid, err)
			}
			if got != a.want {
				t.Fatalf("append for scenario %d = v%d, want v%d", a.sid, got, a.want)
			}
		}
	})

	t.Run("SnapshotRoundTrips", func(t *testing.T) {
		s := newStore(t)
		want := mk(7, "audit-me")
		if _, err := s.AppendScenarioVersion(ctx, 7, want, "alice"); err != nil {
			t.Fatalf("append: %v", err)
		}
		got, err := s.ScenarioVersion(ctx, 7, 1)
		if err != nil {
			t.Fatalf("ScenarioVersion: %v", err)
		}
		if got.Version != 1 || got.ScenarioID != 7 {
			t.Errorf("meta = %d/%d, want 1/7", got.Version, got.ScenarioID)
		}
		if got.CreatedTime.IsZero() {
			t.Error("version has a zero created_time; the history list shows it")
		}
		if got.CreatedBy == nil || *got.CreatedBy != "alice" {
			t.Errorf("created_by = %v, want alice", got.CreatedBy)
		}
		snap := got.Snapshot
		if snap.Name != want.Name || snap.ProjectID != want.ProjectID ||
			snap.Kind != want.Kind || snap.Engine != want.Engine ||
			snap.TestFile != want.TestFile || snap.Requests != want.Requests ||
			snap.CreatedBy != want.CreatedBy || !snap.CreatedTime.Equal(want.CreatedTime) {
			t.Errorf("snapshot = %+v, want the appended %+v", snap, want)
		}
		if snap.TenantID == nil || *snap.TenantID != 4 {
			t.Errorf("tenant_id = %v, want 4", snap.TenantID)
		}
		if len(snap.Data) != 2 || snap.Data[0] != "users.csv" || snap.Data[1] != "postcodes.csv" {
			t.Errorf("data = %v, want the two files", snap.Data)
		}
	})

	t.Run("EmptyActorStoresNull", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.AppendScenarioVersion(ctx, 7, mk(7, "x"), ""); err != nil {
			t.Fatalf("append: %v", err)
		}
		got, err := s.ScenarioVersion(ctx, 7, 1)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if got.CreatedBy != nil {
			t.Errorf("created_by = %q, want nil (the honest unknown)", *got.CreatedBy)
		}
	})

	t.Run("ListIsNewestFirstWithSnapshotExcluded", func(t *testing.T) {
		s := newStore(t)
		for _, name := range []string{"first", "second", "third"} {
			if _, err := s.AppendScenarioVersion(ctx, 7, mk(7, name), ""); err != nil {
				t.Fatalf("append %s: %v", name, err)
			}
		}
		got, err := s.ListScenarioVersions(ctx, 7)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("list = %d rows, want 3", len(got))
		}
		for i, want := range []int{3, 2, 1} {
			if got[i].Version != want {
				t.Errorf("row %d = v%d, want v%d (newest first)", i, got[i].Version, want)
			}
		}
	})

	t.Run("UnknownVersionIsSentinel", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.AppendScenarioVersion(ctx, 7, mk(7, "x"), ""); err != nil {
			t.Fatalf("append: %v", err)
		}
		if _, err := s.ScenarioVersion(ctx, 7, 9); err != ports.ErrScenarioVersionNotFound {
			t.Errorf("unknown version err = %v, want ports.ErrScenarioVersionNotFound", err)
		}
		// Another scenario's version 1 is not this scenario's.
		if _, err := s.ScenarioVersion(ctx, 8, 1); err != ports.ErrScenarioVersionNotFound {
			t.Errorf("other scenario's v1 err = %v, want ports.ErrScenarioVersionNotFound", err)
		}
	})

	t.Run("AppendOnlyOldRowsNeverChange", func(t *testing.T) {
		s := newStore(t)
		// The law the restore flow leans on: history is append-only, so a
		// version read before later appends reads identically after them.
		if _, err := s.AppendScenarioVersion(ctx, 7, mk(7, "first"), "alice"); err != nil {
			t.Fatalf("append v1: %v", err)
		}
		before, err := s.ScenarioVersion(ctx, 7, 1)
		if err != nil {
			t.Fatalf("read v1 before: %v", err)
		}
		if _, err := s.AppendScenarioVersion(ctx, 7, mk(7, "second"), "bob"); err != nil {
			t.Fatalf("append v2: %v", err)
		}
		if _, err := s.AppendScenarioVersion(ctx, 7, mk(7, "third"), "carol"); err != nil {
			t.Fatalf("append v3: %v", err)
		}
		after, err := s.ScenarioVersion(ctx, 7, 1)
		if err != nil {
			t.Fatalf("read v1 after: %v", err)
		}
		if after.Version != before.Version || after.ID != before.ID ||
			after.CreatedTime != before.CreatedTime ||
			after.Snapshot.Name != before.Snapshot.Name ||
			after.Snapshot.Requests != before.Snapshot.Requests {
			t.Errorf("v1 changed across appends: before %+v after %+v", before, after)
		}
		if after.CreatedBy == nil || *after.CreatedBy != "alice" {
			t.Errorf("v1 created_by = %v, want alice unchanged", after.CreatedBy)
		}
	})
}
