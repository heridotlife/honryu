// Package slotest is the shared conformance suite every SLOStore must pass,
// fake and real alike.
package slotest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/slo"
	"github.com/heridotlife/honryu/internal/ports"
)

// NewStore builds a store with no SLOs in it.
type NewStore func(t *testing.T) ports.SLOStore

// Run exercises SLOStore behaviour.
func Run(t *testing.T, newStore NewStore) {
	t.Helper()
	ctx := context.Background()

	// mk builds a valid SLO for a project; tests vary the fields they care
	// about.
	mk := func(projectID int64, name string) slo.SLO {
		p95 := 250.0
		return slo.SLO{ProjectID: projectID, Name: name, TargetP95MS: &p95}
	}

	t.Run("CreateAssignsIDAndStamp", func(t *testing.T) {
		s := newStore(t)
		id, err := s.CreateSLO(ctx, mk(7, "checkout p95"))
		if err != nil {
			t.Fatalf("CreateSLO: %v", err)
		}
		if id <= 0 {
			t.Errorf("id = %d, want a storage-assigned positive id", id)
		}
		got, err := s.ListSLOsByProject(ctx, 7)
		if err != nil {
			t.Fatalf("ListSLOsByProject: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("list = %d SLOs, want 1", len(got))
		}
		if got[0].ID != id || got[0].Name != "checkout p95" {
			t.Errorf("stored = id %d name %q, want id %d name %q", got[0].ID, got[0].Name, id, "checkout p95")
		}
		if got[0].TargetP95MS == nil || *got[0].TargetP95MS != 250 {
			t.Errorf("target_p95_ms = %v, want 250", got[0].TargetP95MS)
		}
		if got[0].TargetErrorRate != nil || got[0].TargetSuccessRatio != nil {
			t.Errorf("unset targets came back non-nil: %v %v", got[0].TargetErrorRate, got[0].TargetSuccessRatio)
		}
		if got[0].CreatedTime.IsZero() {
			t.Errorf("created_time is zero; the issuing UI shows it")
		}
	})

	t.Run("ListIsScopedByProjectAndOldestFirst", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.CreateSLO(ctx, mk(7, "first")); err != nil {
			t.Fatalf("CreateSLO: %v", err)
		}
		if _, err := s.CreateSLO(ctx, mk(7, "second")); err != nil {
			t.Fatalf("CreateSLO: %v", err)
		}
		if _, err := s.CreateSLO(ctx, mk(8, "elsewhere")); err != nil {
			t.Fatalf("CreateSLO: %v", err)
		}
		got, err := s.ListSLOsByProject(ctx, 7)
		if err != nil {
			t.Fatalf("ListSLOsByProject: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("list = %d SLOs, want 2 (project 8's stays out)", len(got))
		}
		if got[0].Name != "first" || got[1].Name != "second" {
			t.Errorf("order = [%s %s], want definition order [first second]", got[0].Name, got[1].Name)
		}
		if _, err := s.ListSLOsByProject(ctx, 99); err != nil {
			t.Errorf("list for a project with none: %v, want an empty non-error read", err)
		}
	})

	t.Run("GetScopesByProject", func(t *testing.T) {
		s := newStore(t)
		id, err := s.CreateSLO(ctx, mk(7, "checkout"))
		if err != nil {
			t.Fatalf("CreateSLO: %v", err)
		}
		got, err := s.GetSLO(ctx, 7, id)
		if err != nil {
			t.Fatalf("GetSLO: %v", err)
		}
		if got.ID != id {
			t.Errorf("got id %d, want %d", got.ID, id)
		}
		if _, err := s.GetSLO(ctx, 8, id); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("GetSLO under a foreign project = %v, want ErrNotFound", err)
		}
		if _, err := s.GetSLO(ctx, 7, id+1); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("GetSLO of an absent id = %v, want ErrNotFound", err)
		}
	})

	t.Run("DeleteScopesByProject", func(t *testing.T) {
		s := newStore(t)
		id, err := s.CreateSLO(ctx, mk(7, "checkout"))
		if err != nil {
			t.Fatalf("CreateSLO: %v", err)
		}
		if err := s.DeleteSLO(ctx, 8, id); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("delete under a foreign project = %v, want ErrNotFound", err)
		}
		if err := s.DeleteSLO(ctx, 7, id); err != nil {
			t.Fatalf("DeleteSLO: %v", err)
		}
		if err := s.DeleteSLO(ctx, 7, id); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("second delete = %v, want ErrNotFound", err)
		}
		got, err := s.ListSLOsByProject(ctx, 7)
		if err != nil {
			t.Fatalf("ListSLOsByProject: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("list after delete = %d SLOs, want 0", len(got))
		}
	})

	t.Run("CreatedTimeSurvivesRoundTrip", func(t *testing.T) {
		s := newStore(t)
		stamp := time.Now().UTC().Add(-time.Hour).Round(time.Second)
		obj := mk(7, "stamped")
		obj.CreatedTime = stamp
		// The store assigns its own stamp on create (storage is the
		// authority), so only the persisted row's non-zero-ness is pinned
		// here, not the caller's value.
		id, err := s.CreateSLO(ctx, obj)
		if err != nil {
			t.Fatalf("CreateSLO: %v", err)
		}
		got, err := s.GetSLO(ctx, 7, id)
		if err != nil {
			t.Fatalf("GetSLO: %v", err)
		}
		if got.CreatedTime.IsZero() {
			t.Error("created_time is zero after create; the issuing UI shows it")
		}
	})

	// Note: the unique (project_id, name) rule is enforced by each
	// implementation's own storage (the mysql unique key, the use-case's
	// pre-check for the fake, whose map would otherwise overwrite). It is
	// deliberately NOT pinned here, because the port's contract routes
	// duplicates through the driver error on mysql and through the
	// use-case layer everywhere else -- the behaviour the HTTP contract
	// pins is the 409, tested at the service boundary.
}
