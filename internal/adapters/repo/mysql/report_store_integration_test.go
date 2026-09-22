//go:build integration

package mysql_test

import (
	"context"
	"sync"
	"testing"
	"time"

	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/reportstoretest"
	"github.com/heridotlife/honryu/test/dbtest"
)

func TestMySQLReportStore_Contract(t *testing.T) {
	db := dbtest.StartMySQL(t)
	reportstoretest.Run(t, func(t *testing.T) ports.ReportStore {
		truncateAll(t, db)
		return mysqladapter.NewRepository(db)
	})
}

// A report row written before 0046 added the column reads back with an empty
// correlation id -- the migration's DEFAULT ” is what those rows carry, and a
// read must never turn that into an error or a placeholder.
func TestMySQLReportStore_PreCorrelationRowReadsEmpty(t *testing.T) {
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	store := mysqladapter.NewRepository(db)
	ctx := context.Background()

	// Insert the way a pre-0046 writer would have: no correlation_id column,
	// so the row takes the migration's default.
	if _, err := db.Exec(`INSERT INTO execution_report
		(run_id, execution_id, scenario_id, engine, outcome, started_at, ended_at)
		VALUES (90, 1, 10, 'jmeter', 'passed', '2026-01-01 00:00:00', '2026-01-01 00:01:00')`); err != nil {
		t.Fatalf("insert pre-correlation row: %v", err)
	}

	got, err := store.GetReport(ctx, 90)
	if err != nil {
		t.Fatalf("GetReport(pre-correlation row): %v", err)
	}
	if got.CorrelationID != "" {
		t.Errorf("correlation id = %q, want empty", got.CorrelationID)
	}
	list, err := store.ListReports(ctx, 1, 0)
	if err != nil {
		t.Fatalf("ListReports: %v", err)
	}
	if len(list) != 1 || list[0].CorrelationID != "" {
		t.Errorf("ListReports = %+v, want the old row with an empty correlation id", list)
	}
}

// The conformance suite pins the summary. Error signatures live in their own
// table, so their round trip is asserted here: a stored report has to read back
// as the same evidence, or it is not worth keeping.
func TestMySQLReportStore_ErrorSignaturesRoundTrip(t *testing.T) {
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	store := mysqladapter.NewRepository(db)
	ctx := context.Background()

	want := report.Build(report.Input{
		ExecutionID: 1, ScenarioID: 2, RunID: 40,
		Engine:    taurus.ExecutorJMeter,
		StartedAt: time.Unix(9000, 0).UTC(),
		EndedAt:   time.Unix(9060, 0).UTC(),
		Outcome:   taurus.OutcomeFailed,
		Requested: report.Load{Concurrency: 10, DurationSeconds: 60},
		Intervals: []metrics.Interval{
			{
				Timestamp: 9000, Label: "checkout-cart", Concurrency: 10,
				Samples: 100, Succeeded: 70, Failed: 30,
				Latency: metrics.Histogram{0.01: 100},
				Errors: []metrics.ErrorGroup{
					{Message: "Not Found", ResponseCode: "404", Count: 20},
					{Message: "socket: too many open files", Count: 10},
				},
			},
		},
	})
	if err := store.SaveReport(ctx, want); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	got, err := store.GetReport(ctx, 40)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if len(got.Errors) != len(want.Errors) {
		t.Fatalf("errors = %+v, want %+v", got.Errors, want.Errors)
	}
	for i := range want.Errors {
		if got.Errors[i].Signature != want.Errors[i].Signature {
			t.Errorf("signature %d = %+v, want %+v", i, got.Errors[i].Signature, want.Errors[i].Signature)
		}
		if got.Errors[i].Count != want.Errors[i].Count {
			t.Errorf("count %d = %d, want %d", i, got.Errors[i].Count, want.Errors[i].Count)
		}
		if len(got.Errors[i].Exemplars) != len(want.Errors[i].Exemplars) {
			t.Errorf("exemplars %d = %q, want %q", i, got.Errors[i].Exemplars, want.Errors[i].Exemplars)
		}
	}
	// Attribution is the reason the signatures are kept apart at all.
	if got.Attribution != want.Attribution {
		t.Errorf("attribution = %+v, want %+v", got.Attribution, want.Attribution)
	}
	if got.Attribution.Engine != 10 || got.Attribution.Target != 20 {
		t.Errorf("attribution did not survive storage: %+v", got.Attribution)
	}
}

// A report row written before 0077 added the column reads back with no
// soak trend -- the migration's NULL is what those rows carry, and a read
// must never turn that into an error or a zeroed trend.
func TestMySQLReportStore_PreSoakTrendRowReadsNone(t *testing.T) {
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	store := mysqladapter.NewRepository(db)
	ctx := context.Background()

	// Insert the way a pre-0077 writer would have: no soak_trend column, so
	// the row takes the migration's NULL default.
	if _, err := db.Exec(`INSERT INTO execution_report
		(run_id, execution_id, scenario_id, engine, outcome, started_at, ended_at)
		VALUES (91, 1, 10, 'jmeter', 'passed', '2026-01-01 00:00:00', '2026-01-01 00:01:00')`); err != nil {
		t.Fatalf("insert pre-soak-trend row: %v", err)
	}

	got, err := store.GetReport(ctx, 91)
	if err != nil {
		t.Fatalf("GetReport(pre-soak-trend row): %v", err)
	}
	if got.SoakTrend != nil {
		t.Errorf("soak trend = %+v, want none on a pre-0077 row", got.SoakTrend)
	}
	list, err := store.ListReports(ctx, 1, 0)
	if err != nil {
		t.Fatalf("ListReports: %v", err)
	}
	if len(list) != 1 || list[0].SoakTrend != nil {
		t.Errorf("ListReports = %+v, want the old row with no trend", list)
	}
}

// Phase 98's lesson made a test: every field a report grows must survive
// SaveReport -> GetReport, because a column the summary projection forgets
// reads as "derived but dropped". The soak trend rides the same round trip
// with its verdict and all three figures -- and since phase 103, with its
// per-label breakdown nested inside the same JSON column (no new column):
// a two-label window whose leaking checkout is diluted by a flat,
// nine-tenths-of-traffic browse, so the aggregate stays quiet while the
// label trend fires.
func TestMySQLReportStore_SoakTrendRoundTrip(t *testing.T) {
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	store := mysqladapter.NewRepository(db)
	ctx := context.Background()

	// A soak-shaped run: 150 seconds, checkout rising 0.1s -> 0.4s at 10
	// samples/s, browse flat 0.2s at 100 samples/s -- built through the
	// genuine Accumulator so what is stored is what a run would produce.
	intervals := make([]metrics.Interval, 0, 300)
	for i := range 150 {
		ts := 9000 + int64(i)
		lat := 0.1 + 0.3*float64(i)/149
		intervals = append(intervals,
			metrics.Interval{
				Timestamp: ts, Label: "checkout", Concurrency: 2,
				Samples: 10, Succeeded: 10, Latency: metrics.Histogram{lat: 10},
			},
			metrics.Interval{
				Timestamp: ts, Label: "browse", Concurrency: 2,
				Samples: 100, Succeeded: 100, Latency: metrics.Histogram{0.2: 100},
			})
	}
	want := report.Build(report.Input{
		ExecutionID: 1, ScenarioID: 2, RunID: 92,
		Engine:    taurus.ExecutorJMeter,
		StartedAt: time.Unix(9000, 0).UTC(), EndedAt: time.Unix(9150, 0).UTC(),
		Outcome:   taurus.OutcomePassed,
		Requested: report.Load{Concurrency: 5, Throughput: 10, DurationSeconds: 150},
		Intervals: intervals,
	})
	if want.SoakTrend == nil || want.SoakTrend.LeakSuspected {
		t.Fatalf("fixture lost its quiet aggregate: %+v", want.SoakTrend)
	}
	var checkout *report.LabelSoakTrend
	for i := range want.SoakTrend.Labels {
		if want.SoakTrend.Labels[i].Label == "checkout" {
			checkout = &want.SoakTrend.Labels[i]
		}
	}
	if checkout == nil || !checkout.LeakSuspected {
		t.Fatalf("fixture lost checkout's leak: %+v", want.SoakTrend.Labels)
	}
	if err := store.SaveReport(ctx, want); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	got, err := store.GetReport(ctx, 92)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if got.SoakTrend == nil {
		t.Fatalf("soak trend did not survive storage: %+v", got)
	}
	// Compared field-by-field with a tolerance, not DeepEqual: the figures
	// are a genuine accumulator's non-round doubles, and MySQL's JSON
	// column stores them as DOUBLE whose decimal rendering is not
	// guaranteed to round-trip every value to the last ULP. The verdicts
	// and the label set are exact; only the rendering may drift a hair.
	if !sameSoakTrend(got.SoakTrend, want.SoakTrend) {
		t.Errorf("soak trend = %+v, want %+v", *got.SoakTrend, *want.SoakTrend)
	}
}

// sameSoakTrend compares two trends for the storage round trip: verdicts
// and label identity exact, figures within a rendering tolerance (a
// fraction of a microsecond -- far below anything a reader could see).
func sameSoakTrend(got, want *report.SoakTrend) bool {
	if got.LeakSuspected != want.LeakSuspected || len(got.Labels) != len(want.Labels) {
		return false
	}
	if !nearly(got.FirstHalfMs, want.FirstHalfMs) ||
		!nearly(got.SecondHalfMs, want.SecondHalfMs) ||
		!nearly(got.SlopeMsPerMin, want.SlopeMsPerMin) {
		return false
	}
	for i := range want.Labels {
		g, w := got.Labels[i], want.Labels[i]
		if g.Label != w.Label || g.LeakSuspected != w.LeakSuspected {
			return false
		}
		if !nearly(g.FirstHalfMs, w.FirstHalfMs) ||
			!nearly(g.SecondHalfMs, w.SecondHalfMs) ||
			!nearly(g.SlopeMsPerMin, w.SlopeMsPerMin) {
			return false
		}
	}
	return true
}

// nearly reports got within ±1e-9 of want.
func nearly(got, want float64) bool {
	return got-want >= -1e-9 && got-want <= 1e-9
}

// A re-saved run keeps its first attempt's signatures rather than replacing
// them with a second, differing attempt's. A run has one outcome, decided
// once by whichever finalisation actually persisted first; a later save
// racing with (or retrying after) it must not overwrite what already stood.
func TestMySQLReportStore_ResaveKeepsTheFirstAttemptsSignatures(t *testing.T) {
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	store := mysqladapter.NewRepository(db)
	ctx := context.Background()

	build := func(errs ...metrics.ErrorGroup) report.Report {
		return report.Build(report.Input{
			ExecutionID: 1, RunID: 50, Outcome: taurus.OutcomeFailed,
			StartedAt: time.Unix(9000, 0).UTC(),
			EndedAt:   time.Unix(9060, 0).UTC(),
			Requested: report.Load{DurationSeconds: 60},
			Intervals: []metrics.Interval{
				{Timestamp: 9000, Label: "probe", Samples: 100, Failed: 10, Errors: errs},
			},
		})
	}

	first := build(
		metrics.ErrorGroup{Message: "Not Found", ResponseCode: "404", Count: 5},
		metrics.ErrorGroup{Message: "Bad Gateway", ResponseCode: "502", Count: 5},
	)
	if err := store.SaveReport(ctx, first); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	second := build(metrics.ErrorGroup{Message: "Not Found", ResponseCode: "404", Count: 7})
	if err := store.SaveReport(ctx, second); err != nil {
		t.Fatalf("SaveReport again: %v", err)
	}

	got, err := store.GetReport(ctx, 50)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if len(got.Errors) != 2 {
		t.Fatalf("errors = %+v, want the first attempt's two signatures, untouched", got.Errors)
	}
	byCode := map[string]int64{}
	for _, e := range got.Errors {
		byCode[e.ResponseCode] = e.Count
	}
	if byCode["404"] != 5 || byCode["502"] != 5 {
		t.Errorf("errors = %+v, want the first attempt's counts (404:5, 502:5), not the second's", got.Errors)
	}
}

// The actual race this fixes: a run's natural completion and a concurrent
// Honryu-initiated Stop/Purge both compute a report for the same run and both
// call SaveReport. Only one persists; the other's insert must lose to the
// primary key, not overwrite the winner.
func TestMySQLReportStore_ConcurrentSavesForTheSameRunDoNotOverwrite(t *testing.T) {
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	store := mysqladapter.NewRepository(db)
	ctx := context.Background()

	build := func(outcome taurus.Outcome) report.Report {
		return report.Build(report.Input{
			ExecutionID: 1, RunID: 60, Outcome: outcome,
			StartedAt: time.Unix(9000, 0).UTC(),
			EndedAt:   time.Unix(9060, 0).UTC(),
			Requested: report.Load{DurationSeconds: 60},
			Intervals: []metrics.Interval{
				{Timestamp: 9000, Label: "probe", Samples: 100, Succeeded: 100},
			},
		})
	}

	const attempts = 8
	var wg sync.WaitGroup
	errs := make([]error, attempts)
	var start sync.WaitGroup
	start.Add(1)
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait()
			outcome := taurus.OutcomePassed
			if i%2 == 0 {
				outcome = taurus.OutcomeAborted
			}
			errs[i] = store.SaveReport(ctx, build(outcome))
		}(i)
	}
	start.Done()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("SaveReport attempt %d: %v", i, err)
		}
	}

	got, err := store.GetReport(ctx, 60)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if got.Outcome != taurus.OutcomePassed && got.Outcome != taurus.OutcomeAborted {
		t.Fatalf("outcome = %q, want one of the attempts' outcomes", got.Outcome)
	}
	// The real assertion: exactly one report exists for this run, whichever
	// attempt actually won -- not a mix, and not silently corrupted by a race.
	list, err := store.ListReports(ctx, 1, 0)
	if err != nil {
		t.Fatalf("ListReports: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("reports for execution 1 = %d, want exactly 1", len(list))
	}
}

// A run whose engine died before a single request completed still has to be
// storable: that report is the only evidence of what went wrong, and it is
// exactly the run someone will come looking for.
func TestMySQLReportStore_RunThatProducedNoSamples(t *testing.T) {
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	store := mysqladapter.NewRepository(db)
	ctx := context.Background()

	want := report.Build(report.Input{
		ExecutionID: 1, RunID: 70, Outcome: taurus.OutcomeError,
		StartedAt: time.Unix(9000, 0).UTC(),
		EndedAt:   time.Unix(9005, 0).UTC(),
		Requested: report.Load{Concurrency: 10, DurationSeconds: 60},
		Intervals: []metrics.Interval{{
			Timestamp: 9000, Label: "probe",
			Errors: []metrics.ErrorGroup{
				{Message: "Taurus internal exception: ToolError", Count: 1},
			},
		}},
	})
	if err := store.SaveReport(ctx, want); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	got, err := store.GetReport(ctx, 70)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if got.Achieved.Samples != 0 {
		t.Errorf("samples = %d, want 0", got.Achieved.Samples)
	}
	if len(got.Errors) != 1 || got.Errors[0].Side != report.SideEngine {
		t.Fatalf("errors = %+v, want one engine-side failure", got.Errors)
	}
	// The failure is the engine's, so nothing here indicts the target.
	if got.Attribution.Engine != 1 || got.Attribution.Target != 0 {
		t.Errorf("attribution = %+v, want the failure on the engine", got.Attribution)
	}
}

// A stored report is read back long after whatever wrote it is gone, so a
// column that will not decode has to fail loudly rather than return a report
// silently missing its percentiles or its label breakdown.
func TestMySQLReportStore_UndecodableColumnsError(t *testing.T) {
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	store := mysqladapter.NewRepository(db)
	ctx := context.Background()

	base := report.Build(report.Input{
		ExecutionID: 1, RunID: 80, Outcome: taurus.OutcomePassed,
		StartedAt: time.Unix(9000, 0).UTC(), EndedAt: time.Unix(9030, 0).UTC(),
		Requested: report.Load{DurationSeconds: 30},
	})
	if err := store.SaveReport(ctx, base); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	// Valid JSON, wrong shape: the column accepts it, but it will not unmarshal
	// into the Go type a report expects.
	if _, err := db.Exec(`UPDATE execution_report SET latency='"not-percentiles"' WHERE run_id=80`); err != nil {
		t.Fatalf("corrupt latency: %v", err)
	}
	if _, err := store.GetReport(ctx, 80); err == nil {
		t.Error("GetReport with an undecodable latency column returned no error")
	}

	if _, err := db.Exec(`UPDATE execution_report SET latency='{}', labels='"not-labels"' WHERE run_id=80`); err != nil {
		t.Fatalf("corrupt labels: %v", err)
	}
	if _, err := store.GetReport(ctx, 80); err == nil {
		t.Error("GetReport with an undecodable labels column returned no error")
	}
}

// A limit of zero means "no limit" on the port, where SQL reads LIMIT 0 as "no
// rows". Passing the value straight through would return nothing and look like
// an execution with no history.
func TestMySQLReportStore_ZeroLimitMeansNoLimit(t *testing.T) {
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	store := mysqladapter.NewRepository(db)
	ctx := context.Background()

	base := time.Unix(9000, 0).UTC()
	for i := range 3 {
		r := report.Build(report.Input{
			ExecutionID: 1, RunID: int64(60 + i), Outcome: taurus.OutcomePassed,
			StartedAt: base.Add(time.Duration(i) * time.Minute),
			EndedAt:   base.Add(time.Duration(i)*time.Minute + 30*time.Second),
			Requested: report.Load{DurationSeconds: 30},
		})
		if err := store.SaveReport(ctx, r); err != nil {
			t.Fatalf("SaveReport: %v", err)
		}
	}

	all, err := store.ListReports(ctx, 1, 0)
	if err != nil {
		t.Fatalf("ListReports: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("ListReports(limit 0) = %d reports, want all 3", len(all))
	}

	since, err := store.ReportsSince(ctx, base, 0)
	if err != nil {
		t.Fatalf("ReportsSince: %v", err)
	}
	if len(since) != 3 {
		t.Errorf("ReportsSince(limit 0) = %d reports, want all 3", len(since))
	}
}

// Reports outlive the run, so the store is read long after the writer has gone.
// These drive the DB-error branches.
func TestMySQLReportStore_ErrorsWhenDBClosed(t *testing.T) {
	db := dbtest.StartMySQL(t)
	store := mysqladapter.NewRepository(db)
	ctx := context.Background()
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	valid := report.Report{ExecutionID: 1, RunID: 1, Outcome: taurus.OutcomePassed}
	ops := map[string]func() error{
		"SaveReport":   func() error { return store.SaveReport(ctx, valid) },
		"GetReport":    func() error { _, e := store.GetReport(ctx, 1); return e },
		"ListReports":  func() error { _, e := store.ListReports(ctx, 1, 0); return e },
		"ReportsSince": func() error { _, e := store.ReportsSince(ctx, time.Unix(0, 0), 0); return e },
	}
	for name, op := range ops {
		if err := op(); err == nil {
			t.Errorf("%s on a closed database returned no error", name)
		}
	}
}
