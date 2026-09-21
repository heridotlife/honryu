// Package reportstoretest is the shared conformance suite every ReportStore
// must pass, fake and real alike.
package reportstoretest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

// NewStore builds a store with no reports in it.
type NewStore func(t *testing.T) ports.ReportStore

// Run exercises ReportStore behaviour.
func Run(t *testing.T, newStore NewStore) {
	t.Helper()
	ctx := context.Background()

	t.Run("SaveAndGet", func(t *testing.T) {
		s := newStore(t)
		want := sample(1, 10, time.Unix(2000, 0))
		if err := s.SaveReport(ctx, want); err != nil {
			t.Fatalf("SaveReport: %v", err)
		}

		got, err := s.GetReport(ctx, 10)
		if err != nil {
			t.Fatalf("GetReport: %v", err)
		}
		if got.ExecutionID != want.ExecutionID || got.RunID != want.RunID {
			t.Errorf("identity = %+v", got)
		}
		if got.Outcome != want.Outcome || got.Engine != want.Engine {
			t.Errorf("outcome/engine = %q/%q", got.Outcome, got.Engine)
		}
		if got.Cluster != want.Cluster {
			t.Errorf("cluster = %q, want %q", got.Cluster, want.Cluster)
		}
		if got.CorrelationID != want.CorrelationID {
			t.Errorf("correlation id = %q, want %q", got.CorrelationID, want.CorrelationID)
		}
		if got.Achieved.Samples != want.Achieved.Samples || got.ErrorRate != want.ErrorRate {
			t.Errorf("counters = %+v (error rate %v)", got.Achieved, got.ErrorRate)
		}
		// Percentiles are the point of keeping a report: a judgement rests on
		// them, and they cannot be recomputed once the run's buckets are gone.
		if got.Latency[95] != want.Latency[95] {
			t.Errorf("p95 = %v, want %v", got.Latency[95], want.Latency[95])
		}
		if len(got.Labels) != len(want.Labels) {
			t.Fatalf("labels = %d, want %d", len(got.Labels), len(want.Labels))
		}
		if got.Labels[0].Label != want.Labels[0].Label || got.Labels[0].Failed != want.Labels[0].Failed {
			t.Errorf("label summary = %+v", got.Labels[0])
		}
		if !got.StartedAt.Equal(want.StartedAt) {
			t.Errorf("StartedAt = %v, want %v", got.StartedAt, want.StartedAt)
		}
	})

	t.Run("GetMissing", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.GetReport(ctx, 999); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("GetReport(missing) = %v, want ErrNotFound", err)
		}
	})

	// A report with no correlation id is the shape every report saved before
	// the id existed still has on a real database: it has to read back empty,
	// never as an error and never as a zero-looking placeholder.
	t.Run("EmptyCorrelationIDStaysEmpty", func(t *testing.T) {
		s := newStore(t)
		want := sample(1, 11, time.Unix(2000, 0))
		want.CorrelationID = ""
		if err := s.SaveReport(ctx, want); err != nil {
			t.Fatalf("SaveReport: %v", err)
		}
		got, err := s.GetReport(ctx, 11)
		if err != nil {
			t.Fatalf("GetReport: %v", err)
		}
		if got.CorrelationID != "" {
			t.Errorf("correlation id = %q, want empty", got.CorrelationID)
		}
	})

	// A run has one outcome, decided once. The first report saved for a run is
	// the one that survives: natural completion and a concurrent
	// Honryu-initiated Stop/Purge race to finalise the same run, both may
	// compute and save a report, and whichever actually persists first must
	// stand -- a plain replace-on-conflict would let whichever commits last
	// silently overwrite the other's verdict.
	t.Run("SaveKeepsTheFirstReportForARun", func(t *testing.T) {
		s := newStore(t)
		first := sample(1, 10, time.Unix(2000, 0))
		if err := s.SaveReport(ctx, first); err != nil {
			t.Fatalf("SaveReport: %v", err)
		}
		second := first
		second.Outcome = taurus.OutcomeFailed
		second.ErrorRate = 0.5
		if err := s.SaveReport(ctx, second); err != nil {
			t.Fatalf("SaveReport again: %v", err)
		}

		got, err := s.GetReport(ctx, 10)
		if err != nil {
			t.Fatalf("GetReport: %v", err)
		}
		if got.Outcome != taurus.OutcomePassed || got.ErrorRate != first.ErrorRate {
			t.Errorf("second save overwrote the first: %+v", got)
		}
		list, err := s.ListReports(ctx, 1, 0)
		if err != nil {
			t.Fatalf("ListReports: %v", err)
		}
		if len(list) != 1 {
			t.Errorf("execution has %d reports for one run, want 1", len(list))
		}
	})

	t.Run("ListReportsMostRecentFirst", func(t *testing.T) {
		s := newStore(t)
		base := time.Unix(3000, 0)
		for i, at := range []time.Time{base, base.Add(time.Hour), base.Add(2 * time.Hour)} {
			r := sample(1, int64(20+i), at)
			if err := s.SaveReport(ctx, r); err != nil {
				t.Fatalf("SaveReport: %v", err)
			}
		}
		// A different execution's report must not appear.
		if err := s.SaveReport(ctx, sample(2, 99, base)); err != nil {
			t.Fatalf("SaveReport(other execution): %v", err)
		}

		got, err := s.ListReports(ctx, 1, 0)
		if err != nil {
			t.Fatalf("ListReports: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("ListReports = %d reports, want 3", len(got))
		}
		for i := 1; i < len(got); i++ {
			if got[i-1].StartedAt.Before(got[i].StartedAt) {
				t.Fatalf("reports are not most-recent-first: %v then %v",
					got[i-1].StartedAt, got[i].StartedAt)
			}
		}

		limited, err := s.ListReports(ctx, 1, 2)
		if err != nil {
			t.Fatalf("ListReports(limit): %v", err)
		}
		if len(limited) != 2 {
			t.Errorf("limit 2 returned %d", len(limited))
		}
		if !limited[0].StartedAt.Equal(base.Add(2 * time.Hour)) {
			t.Errorf("limit dropped the newest report: %v", limited[0].StartedAt)
		}
	})

	// Trend analytics reads across executions from a point in time.
	t.Run("ReportsSince", func(t *testing.T) {
		s := newStore(t)
		cutoff := time.Unix(5000, 0)
		if err := s.SaveReport(ctx, sample(1, 30, cutoff.Add(-time.Hour))); err != nil {
			t.Fatalf("SaveReport(old): %v", err)
		}
		if err := s.SaveReport(ctx, sample(2, 31, cutoff)); err != nil {
			t.Fatalf("SaveReport(boundary): %v", err)
		}
		if err := s.SaveReport(ctx, sample(3, 32, cutoff.Add(time.Hour))); err != nil {
			t.Fatalf("SaveReport(new): %v", err)
		}

		got, err := s.ReportsSince(ctx, cutoff, 0)
		if err != nil {
			t.Fatalf("ReportsSince: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("ReportsSince = %d reports, want 2 (the boundary is included)", len(got))
		}
		for _, r := range got {
			if r.StartedAt.Before(cutoff) {
				t.Errorf("report from before the cutoff: %v", r.StartedAt)
			}
		}
	})

	t.Run("ErrorSignatureHistory", func(t *testing.T) {
		s := newStore(t)

		withError := func(executionID, runID int64, at time.Time, sig report.Signature, count int64) report.Report {
			r := sample(executionID, runID, at)
			r.Outcome = taurus.OutcomeFailed
			r.Errors = []report.ErrorSignature{{Signature: sig, Count: count}}
			return r
		}
		sig500 := report.Signature{Label: "checkout", ResponseCode: "500", Side: report.SideTarget}
		sig404 := report.Signature{Label: "checkout", ResponseCode: "404", Side: report.SideTarget}

		if err := s.SaveReport(ctx, withError(1, 40, time.Unix(1000, 0), sig500, 3)); err != nil {
			t.Fatalf("SaveReport(run 40): %v", err)
		}
		if err := s.SaveReport(ctx, withError(1, 41, time.Unix(2000, 0), sig500, 5)); err != nil {
			t.Fatalf("SaveReport(run 41): %v", err)
		}
		if err := s.SaveReport(ctx, withError(1, 42, time.Unix(3000, 0), sig404, 1)); err != nil {
			t.Fatalf("SaveReport(run 42): %v", err)
		}
		// A different execution's signature must never appear in execution 1's history.
		if err := s.SaveReport(ctx, withError(9, 43, time.Unix(4000, 0), sig500, 100)); err != nil {
			t.Fatalf("SaveReport(other execution): %v", err)
		}

		got, err := s.ErrorSignatureHistory(ctx, 1)
		if err != nil {
			t.Fatalf("ErrorSignatureHistory: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("ErrorSignatureHistory = %+v, want 2 rows", got)
		}
		// Dominant first: sig500's total (8, across 2 runs) outranks sig404's (1, 1 run).
		if got[0].Label != "checkout" || got[0].ResponseCode != "500" || got[0].TotalCount != 8 || got[0].RunCount != 2 {
			t.Fatalf("ErrorSignatureHistory[0] = %+v, want sig500 totalled 8 across 2 runs", got[0])
		}
		if got[1].ResponseCode != "404" || got[1].TotalCount != 1 || got[1].RunCount != 1 {
			t.Fatalf("ErrorSignatureHistory[1] = %+v, want sig404 totalled 1 across 1 run", got[1])
		}

		empty, err := s.ErrorSignatureHistory(ctx, 999)
		if err != nil {
			t.Fatalf("ErrorSignatureHistory(no reports): %v", err)
		}
		if len(empty) != 0 {
			t.Fatalf("ErrorSignatureHistory(no reports) = %+v, want empty", empty)
		}
	})

	t.Run("RejectsInvalidReports", func(t *testing.T) {
		s := newStore(t)
		bad := sample(1, 10, time.Unix(1, 0))
		bad.RunID = 0
		if err := s.SaveReport(ctx, bad); err == nil {
			t.Error("SaveReport accepted a report with no run")
		}
	})

	// Phase 98's lesson, applied to phase 99's field: a derived value that
	// is present on the built report but missing from the store's write or
	// read path reads as "computed then silently dropped". The soak trend
	// rides SaveReport/GetReport like every other finding, and a run
	// without one (the common case: every window under two minutes) reads
	// back without it rather than as an error or a zeroed trend.
	t.Run("SoakTrendRoundTrips", func(t *testing.T) {
		s := newStore(t)
		want := sample(1, 70, time.Unix(2000, 0))
		want.SoakTrend = &report.SoakTrend{
			FirstHalfMs: 174.5, SecondHalfMs: 325.5, SlopeMsPerMin: 120.8, LeakSuspected: true,
		}
		if err := s.SaveReport(ctx, want); err != nil {
			t.Fatalf("SaveReport: %v", err)
		}
		got, err := s.GetReport(ctx, 70)
		if err != nil {
			t.Fatalf("GetReport: %v", err)
		}
		if got.SoakTrend == nil {
			t.Fatalf("soak trend did not survive storage: %+v", got)
		}
		if *got.SoakTrend != *want.SoakTrend {
			t.Errorf("soak trend = %+v, want %+v", *got.SoakTrend, *want.SoakTrend)
		}

		flat := sample(1, 71, time.Unix(2000, 0))
		if err := s.SaveReport(ctx, flat); err != nil {
			t.Fatalf("SaveReport(no trend): %v", err)
		}
		gotFlat, err := s.GetReport(ctx, 71)
		if err != nil {
			t.Fatalf("GetReport(no trend): %v", err)
		}
		if gotFlat.SoakTrend != nil {
			t.Errorf("report without a trend read back with one: %+v", gotFlat.SoakTrend)
		}
	})
}

// sample is a report with enough substance that a store dropping a field is
// caught rather than passing on the identity alone.
func sample(executionID, runID int64, at time.Time) report.Report {
	return report.Build(report.Input{
		ExecutionID:   executionID,
		ScenarioID:    executionID * 10,
		RunID:         runID,
		Engine:        taurus.ExecutorJMeter,
		Cluster:       "prod-eu",
		CorrelationID: "4bf92f3577b34da6a3ce929d0e0e4736",
		StartedAt:     at,
		EndedAt:       at.Add(time.Minute),
		Outcome:       taurus.OutcomePassed,
		Requested:     report.Load{Concurrency: 50, Throughput: 100, DurationSeconds: 60},
		Intervals: []metrics.Interval{
			{
				Timestamp: at.Unix(), Label: "checkout-cart", Concurrency: 50,
				Samples: 1000, Succeeded: 950, Failed: 50,
				Latency: metrics.Histogram{0.01: 900, 0.2: 90, 1.5: 10},
			},
		},
	})
}
