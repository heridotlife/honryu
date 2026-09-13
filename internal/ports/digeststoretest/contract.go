// Package digeststoretest is the shared conformance suite every
// ReportDigestStore must pass, fake and real alike.
package digeststoretest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/digest"
	"github.com/heridotlife/honryu/internal/ports"
)

// NewStore builds a store with no digests in it.
type NewStore func(t *testing.T) ports.ReportDigestStore

// Run exercises ReportDigestStore behaviour.
func Run(t *testing.T, newStore NewStore) {
	t.Helper()
	ctx := context.Background()

	// mk builds a stored digest for a project and period; tests vary the
	// fields they care about.
	mk := func(projectID int64, period digest.Period, start, end time.Time) digest.Digest {
		return digest.Digest{
			ProjectID: projectID, Period: period,
			WindowStart: start, WindowEnd: end,
			Payload: []byte(`{"event":"report.digest"}`),
		}
	}

	t.Run("SaveAssignsIDAndStamp", func(t *testing.T) {
		s := newStore(t)
		start := time.Unix(1700_000_000, 0).UTC()
		id, err := s.SaveDigest(ctx, mk(7, digest.PeriodDaily, start, start.Add(24*time.Hour)))
		if err != nil {
			t.Fatalf("SaveDigest: %v", err)
		}
		if id <= 0 {
			t.Errorf("id = %d, want a storage-assigned positive id", id)
		}
		got, err := s.ListDigestsByProject(ctx, 7, 0)
		if err != nil {
			t.Fatalf("ListDigestsByProject: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("list = %d digests, want 1", len(got))
		}
		if got[0].ID != id {
			t.Errorf("stored id = %d, want %d", got[0].ID, id)
		}
		if got[0].CreatedTime.IsZero() {
			t.Error("created_time is zero; the issuing UI shows it")
		}
		// The payload is the delivered bytes: it must survive storage
		// verbatim, not normalised, or receivers and the in-app feed would
		// disagree about what was sent.
		if string(got[0].Payload) != `{"event":"report.digest"}` {
			t.Errorf("payload = %q, want the exact bytes stored", got[0].Payload)
		}
		if !got[0].WindowStart.Equal(start) || !got[0].WindowEnd.Equal(start.Add(24*time.Hour)) {
			t.Errorf("window = [%v, %v], want the stored bounds", got[0].WindowStart, got[0].WindowEnd)
		}
	})

	t.Run("ListNewestFirstScopedAndLimited", func(t *testing.T) {
		s := newStore(t)
		base := time.Unix(1700_000_000, 0).UTC()
		// Project 7 gets two digests (older first), project 8 gets one;
		// neither project's rows may leak into the other's list.
		first, err := s.SaveDigest(ctx, mk(7, digest.PeriodDaily, base, base.Add(24*time.Hour)))
		if err != nil {
			t.Fatalf("SaveDigest(7, first): %v", err)
		}
		second, err := s.SaveDigest(ctx, mk(7, digest.PeriodDaily, base.Add(24*time.Hour), base.Add(48*time.Hour)))
		if err != nil {
			t.Fatalf("SaveDigest(7, second): %v", err)
		}
		if _, err := s.SaveDigest(ctx, mk(8, digest.PeriodDaily, base, base.Add(24*time.Hour))); err != nil {
			t.Fatalf("SaveDigest(8): %v", err)
		}
		got, err := s.ListDigestsByProject(ctx, 7, 0)
		if err != nil {
			t.Fatalf("ListDigestsByProject: %v", err)
		}
		if len(got) != 2 || got[0].ID != second || got[1].ID != first {
			t.Errorf("list(7) = ids %v, want newest-first [%d %d]", idsOf(got), second, first)
		}
		limited, err := s.ListDigestsByProject(ctx, 7, 1)
		if err != nil {
			t.Fatalf("ListDigestsByProject(limit 1): %v", err)
		}
		if len(limited) != 1 || limited[0].ID != second {
			t.Errorf("list(7, limit 1) = ids %v, want only the newest %d", idsOf(limited), second)
		}
		other, err := s.ListDigestsByProject(ctx, 8, 0)
		if err != nil {
			t.Fatalf("ListDigestsByProject(8): %v", err)
		}
		if len(other) != 1 || other[0].ProjectID != 8 {
			t.Errorf("list(8) = %v, want only its own", other)
		}
	})

	t.Run("LastDigestWindowEndScopedByProjectAndPeriod", func(t *testing.T) {
		s := newStore(t)
		base := time.Unix(1700_000_000, 0).UTC()
		dailyEnd := base.Add(24 * time.Hour)
		if _, found, err := s.LastDigestWindowEnd(ctx, 7, digest.PeriodDaily); err != nil || found {
			t.Fatalf("first read = (%v, %v, %v), want (zero, false, nil) before any fire", base, found, err)
		}
		if _, err := s.SaveDigest(ctx, mk(7, digest.PeriodDaily, base, dailyEnd)); err != nil {
			t.Fatalf("SaveDigest(daily): %v", err)
		}
		// A weekly digest for the same project must not serve the daily
		// bookmark: switching period starts a fresh history.
		if _, found, err := s.LastDigestWindowEnd(ctx, 7, digest.PeriodWeekly); err != nil || found {
			t.Fatalf("weekly read = found %v err %v, want found=false (period-scoped)", found, err)
		}
		// Nor may another project's digest serve as this one's bookmark.
		if _, found, err := s.LastDigestWindowEnd(ctx, 8, digest.PeriodDaily); err != nil || found {
			t.Fatalf("project 8 read = found %v err %v, want found=false (project-scoped)", found, err)
		}
		got, found, err := s.LastDigestWindowEnd(ctx, 7, digest.PeriodDaily)
		if err != nil || !found {
			t.Fatalf("daily read = (%v, %v, %v), want the stored end", got, found, err)
		}
		if !got.Equal(dailyEnd) {
			t.Errorf("window_end = %v, want %v", got, dailyEnd)
		}
		// A second, later digest moves the bookmark to the newest row's end.
		laterEnd := base.Add(48 * time.Hour)
		if _, err := s.SaveDigest(ctx, mk(7, digest.PeriodDaily, dailyEnd, laterEnd)); err != nil {
			t.Fatalf("SaveDigest(second): %v", err)
		}
		got, found, err = s.LastDigestWindowEnd(ctx, 7, digest.PeriodDaily)
		if err != nil || !found {
			t.Fatalf("second daily read = (%v, %v, %v), want found", got, found, err)
		}
		if !got.Equal(laterEnd) {
			t.Errorf("window_end after second save = %v, want the newest %v", got, laterEnd)
		}
	})

	// Rows are born pending whatever the caller's Digest said, and only
	// MarkDigestDelivery moves them off pending -- the store, not the
	// caller, owns the status lifecycle.
	t.Run("SaveDigestAlwaysBornPending", func(t *testing.T) {
		s := newStore(t)
		start := time.Unix(1700_000_000, 0).UTC()
		pre := mk(7, digest.PeriodDaily, start, start.Add(24*time.Hour))
		pre.DeliveryStatus = digest.DeliveryDelivered // a caller's claim: ignored
		id, err := s.SaveDigest(ctx, pre)
		if err != nil {
			t.Fatalf("SaveDigest: %v", err)
		}
		rows, err := s.ListDigestsByProject(ctx, 7, 0)
		if err != nil {
			t.Fatalf("ListDigestsByProject: %v", err)
		}
		if rows[0].DeliveryStatus != digest.DeliveryPending {
			t.Errorf("saved status = %q, want pending regardless of the caller's value", rows[0].DeliveryStatus)
		}
		if rows[0].DeliveredAt != nil {
			t.Errorf("saved delivered_at = %v, want nil", rows[0].DeliveredAt)
		}
		_ = id
	})

	t.Run("MarkDigestDeliveryPendingToDelivered", func(t *testing.T) {
		s := newStore(t)
		start := time.Unix(1700_000_000, 0).UTC()
		id, err := s.SaveDigest(ctx, mk(7, digest.PeriodDaily, start, start.Add(24*time.Hour)))
		if err != nil {
			t.Fatalf("SaveDigest: %v", err)
		}
		at := start.Add(25 * time.Hour)
		if err := s.MarkDigestDelivery(ctx, id, digest.DeliveryDelivered, at); err != nil {
			t.Fatalf("MarkDigestDelivery(delivered): %v", err)
		}
		rows, err := s.ListDigestsByProject(ctx, 7, 0)
		if err != nil {
			t.Fatalf("ListDigestsByProject: %v", err)
		}
		if rows[0].DeliveryStatus != digest.DeliveryDelivered {
			t.Errorf("status = %q, want delivered", rows[0].DeliveryStatus)
		}
		if rows[0].DeliveredAt == nil || !rows[0].DeliveredAt.Equal(at) {
			t.Errorf("delivered_at = %v, want %v", rows[0].DeliveredAt, at)
		}
	})

	t.Run("MarkDigestDeliveryPendingToFailed", func(t *testing.T) {
		s := newStore(t)
		start := time.Unix(1700_000_000, 0).UTC()
		id, err := s.SaveDigest(ctx, mk(7, digest.PeriodDaily, start, start.Add(24*time.Hour)))
		if err != nil {
			t.Fatalf("SaveDigest: %v", err)
		}
		at := start.Add(25 * time.Hour)
		if err := s.MarkDigestDelivery(ctx, id, digest.DeliveryFailed, at); err != nil {
			t.Fatalf("MarkDigestDelivery(failed): %v", err)
		}
		rows, err := s.ListDigestsByProject(ctx, 7, 0)
		if err != nil {
			t.Fatalf("ListDigestsByProject: %v", err)
		}
		if rows[0].DeliveryStatus != digest.DeliveryFailed {
			t.Errorf("status = %q, want failed", rows[0].DeliveryStatus)
		}
		if rows[0].DeliveredAt != nil {
			t.Errorf("delivered_at = %v, want nil (nothing confirmed)", rows[0].DeliveredAt)
		}
	})

	t.Run("MarkDigestDeliveryUnknownIDNotFound", func(t *testing.T) {
		s := newStore(t)
		if err := s.MarkDigestDelivery(ctx, 4242, digest.DeliveryDelivered, time.Now()); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("MarkDigestDelivery(unknown) = %v, want ports.ErrNotFound", err)
		}
	})

}

func idsOf(rows []digest.Digest) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}
