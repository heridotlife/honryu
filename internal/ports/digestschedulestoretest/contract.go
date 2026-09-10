// Package digestschedulestoretest is the shared conformance suite every
// DigestScheduleStore must pass, fake and real alike.
package digestschedulestoretest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/digest"
	"github.com/heridotlife/honryu/internal/ports"
)

// NewStore builds a store with no schedules in it.
type NewStore func(t *testing.T) ports.DigestScheduleStore

// Run exercises DigestScheduleStore behaviour.
func Run(t *testing.T, newStore NewStore) {
	t.Helper()
	ctx := context.Background()

	t0 := time.Unix(1700_000_000, 0).UTC()

	t.Run("UpsertGetRoundtripKeepsBookmark", func(t *testing.T) {
		s := newStore(t)
		if err := s.UpsertDigestSchedule(ctx, 7, digest.PeriodDaily, true); err != nil {
			t.Fatalf("UpsertDigestSchedule: %v", err)
		}
		got, err := s.GetDigestSchedule(ctx, 7)
		if err != nil {
			t.Fatalf("GetDigestSchedule: %v", err)
		}
		if got.ProjectID != 7 || got.Period != digest.PeriodDaily || !got.Enabled || got.LastFired != nil {
			t.Fatalf("schedule = %+v, want daily/enabled/never-fired", got)
		}
		// A claim stamps last_fired; a later upsert (period switch,
		// pause/resume) must keep that bookmark -- fire times tile from
		// where they left off.
		if _, found, err := s.ClaimDueDigestSchedule(ctx, t0); err != nil || !found {
			t.Fatalf("first claim = found %v err %v, want the due row", found, err)
		}
		if err := s.UpsertDigestSchedule(ctx, 7, digest.PeriodWeekly, false); err != nil {
			t.Fatalf("UpsertDigestSchedule(switch): %v", err)
		}
		got, err = s.GetDigestSchedule(ctx, 7)
		if err != nil {
			t.Fatalf("GetDigestSchedule(after switch): %v", err)
		}
		if got.Period != digest.PeriodWeekly || got.Enabled {
			t.Fatalf("switched schedule = %+v, want weekly/disabled", got)
		}
		if got.LastFired == nil || !got.LastFired.Equal(t0) {
			t.Fatalf("last_fired = %v, want the claim's stamp kept across the upsert", got.LastFired)
		}
	})

	t.Run("GetDeleteNotFound", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.GetDigestSchedule(ctx, 7); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("GetDigestSchedule(unknown) = %v, want ErrNotFound", err)
		}
		if err := s.DeleteDigestSchedule(ctx, 7); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("DeleteDigestSchedule(unknown) = %v, want ErrNotFound", err)
		}
		if err := s.UpsertDigestSchedule(ctx, 7, digest.PeriodDaily, true); err != nil {
			t.Fatalf("UpsertDigestSchedule: %v", err)
		}
		if err := s.DeleteDigestSchedule(ctx, 7); err != nil {
			t.Fatalf("DeleteDigestSchedule: %v", err)
		}
		if _, err := s.GetDigestSchedule(ctx, 7); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("GetDigestSchedule(after delete) = %v, want ErrNotFound", err)
		}
	})

	t.Run("ClaimDueFollowsPeriodAndEnabled", func(t *testing.T) {
		s := newStore(t)
		if err := s.UpsertDigestSchedule(ctx, 7, digest.PeriodDaily, true); err != nil {
			t.Fatalf("UpsertDigestSchedule(7): %v", err)
		}
		// Nothing else in the store: the never-fired row is due now.
		row, found, err := s.ClaimDueDigestSchedule(ctx, t0)
		if err != nil || !found {
			t.Fatalf("claim(never fired) = found %v err %v, want found", found, err)
		}
		if row.ProjectID != 7 || row.LastFired == nil || !row.LastFired.Equal(t0) {
			t.Fatalf("claimed = %+v, want project 7 stamped at t0", row)
		}
		// Not due again until a full period passes...
		if _, found, err := s.ClaimDueDigestSchedule(ctx, t0.Add(23*time.Hour)); err != nil || found {
			t.Fatalf("claim(23h later) = found %v err %v, want not due before the period elapses", found, err)
		}
		// ...then due again, stamped with the new now.
		next := t0.Add(24 * time.Hour)
		if _, found, err := s.ClaimDueDigestSchedule(ctx, next); err != nil || !found {
			t.Fatalf("claim(24h later) = found %v err %v, want due", found, err)
		}
		got, err := s.GetDigestSchedule(ctx, 7)
		if err != nil {
			t.Fatalf("GetDigestSchedule: %v", err)
		}
		if got.LastFired == nil || !got.LastFired.Equal(next) {
			t.Fatalf("last_fired = %v, want %v", got.LastFired, next)
		}
		// A disabled row is never due, however stale its bookmark (and with
		// 7 now deleted, nothing else may be picked up in its place).
		if err := s.DeleteDigestSchedule(ctx, 7); err != nil {
			t.Fatalf("DeleteDigestSchedule(7): %v", err)
		}
		if err := s.UpsertDigestSchedule(ctx, 8, digest.PeriodDaily, false); err != nil {
			t.Fatalf("UpsertDigestSchedule(8, disabled): %v", err)
		}
		if _, found, err := s.ClaimDueDigestSchedule(ctx, next.Add(7*24*time.Hour)); err != nil || found {
			t.Fatalf("claim(disabled) = found %v err %v, want nothing due", found, err)
		}
	})

	t.Run("ClaimPicksMostOverdueFirst", func(t *testing.T) {
		s := newStore(t)
		// Project 8 fires once long ago (stale); project 9 has never fired.
		stale := t0.Add(-7 * 24 * time.Hour)
		if err := s.UpsertDigestSchedule(ctx, 8, digest.PeriodDaily, true); err != nil {
			t.Fatalf("UpsertDigestSchedule(8): %v", err)
		}
		if _, found, err := s.ClaimDueDigestSchedule(ctx, stale); err != nil || !found {
			t.Fatalf("stamp 8 = found %v err %v", found, err)
		}
		if err := s.UpsertDigestSchedule(ctx, 9, digest.PeriodDaily, true); err != nil {
			t.Fatalf("UpsertDigestSchedule(9): %v", err)
		}
		// Both are due at t0: 8's bookmark is a week old, but 9 has never
		// fired at all -- a project that has never been digested is the
		// most overdue there is.
		row, found, err := s.ClaimDueDigestSchedule(ctx, t0)
		if err != nil || !found {
			t.Fatalf("claim = found %v err %v", found, err)
		}
		if row.ProjectID != 9 {
			t.Fatalf("claim picked project %d, want never-fired 9 first", row.ProjectID)
		}
	})
}
