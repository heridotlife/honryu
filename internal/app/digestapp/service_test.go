package digestapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/digestapp"
	"github.com/heridotlife/honryu/internal/domain/digest"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// delivery records what the fake Deliverer was asked to send.
type delivery struct {
	projectID int64
	event     string
	body      []byte
}

// fakeDeliverer records deliveries; failErr, when set, fails every call so
// tests can prove a delivery failure never fails the fire.
type fakeDeliverer struct {
	calls   []delivery
	failErr error
}

func (f *fakeDeliverer) DeliverEvent(_ context.Context, projectID int64, event string, body []byte) error {
	f.calls = append(f.calls, delivery{projectID: projectID, event: event, body: body})
	return f.failErr
}

// fixture is a store seeded with two executions under one project (plus an
// unrelated project's execution), ready for runs to be saved into.
type fixture struct {
	store  *fake.Store
	svc    *digestapp.Service
	deliv  *fakeDeliverer
	projA  int64
	projB  int64
	execA1 int64 // "checkout" under project A
	execA2 int64 // "search" under project A
	execB1 int64 // under project B: never A's business
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store := fake.NewStore()
	f := &fixture{store: store, deliv: &fakeDeliverer{}}
	f.svc = digestapp.NewService(store).WithDeliverer(f.deliv)
	f.projA = mkProject(t, store, "alpha")
	f.projB = mkProject(t, store, "beta")
	f.execA1 = mkExecution(t, store, "checkout", f.projA)
	f.execA2 = mkExecution(t, store, "search", f.projA)
	f.execB1 = mkExecution(t, store, "foreign", f.projB)
	return f
}

func mkProject(t *testing.T, store *fake.Store, name string) int64 {
	t.Helper()
	id, err := store.CreateProject(context.Background(), project.Project{Name: name, Owner: "op"})
	if err != nil {
		t.Fatalf("CreateProject(%s): %v", name, err)
	}
	return id
}

func mkExecution(t *testing.T, store *fake.Store, name string, projectID int64) int64 {
	t.Helper()
	exe, err := execution.New(name, projectID)
	if err != nil {
		t.Fatalf("execution.New(%s): %v", name, err)
	}
	id, err := store.CreateExecution(context.Background(), exe)
	if err != nil {
		t.Fatalf("CreateExecution(%s): %v", name, err)
	}
	return id
}

// saveRun stores one finished run's report. started is the report's own
// clock -- what window membership is decided on.
func saveRun(t *testing.T, store *fake.Store, executionID int64, runID int, started time.Time, outcome taurus.Outcome) {
	t.Helper()
	rep := report.Report{
		ExecutionID: executionID, RunID: int64(runID),
		StartedAt: started, EndedAt: started.Add(5 * time.Minute),
		Outcome: outcome,
	}
	if err := store.SaveReport(context.Background(), rep); err != nil {
		t.Fatalf("SaveReport(run %d): %v", runID, err)
	}
}

// window is a fixed, readable digest window: [2026-03-01, 2026-03-02).
func window() (time.Time, time.Time) {
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	return start, start.Add(24 * time.Hour)
}

// TestBuildDigestAggregatesRunsInWindow pins the aggregation: only runs
// whose reports started inside [start, end) count -- the boundary run at
// exactly windowEnd belongs to the NEXT digest, an earlier run to a
// previous one -- mixed outcomes land in their buckets, threshold
// failures mirror the failed bucket, and each execution contributes name,
// run count, and its worst outcome. Executions with no runs in the window
// (and other projects' runs) contribute nothing.
func TestBuildDigestAggregatesRunsInWindow(t *testing.T) {
	f := newFixture(t)
	start, end := window()
	in := start.Add(2 * time.Hour)
	saveRun(t, f.store, f.execA1, 1, in, taurus.OutcomePassed)
	saveRun(t, f.store, f.execA1, 2, in.Add(time.Hour), taurus.OutcomeFailed)
	saveRun(t, f.store, f.execA1, 3, in.Add(2*time.Hour), taurus.OutcomeAborted)
	saveRun(t, f.store, f.execA2, 4, in.Add(3*time.Hour), taurus.OutcomePassed)
	// Out of window: before the start, exactly at the end (the next
	// digest's opening boundary), and another project's entirely.
	saveRun(t, f.store, f.execA1, 5, start.Add(-time.Hour), taurus.OutcomeFailed)
	saveRun(t, f.store, f.execA1, 6, end, taurus.OutcomeFailed)
	saveRun(t, f.store, f.execB1, 7, in, taurus.OutcomeFailed)

	d, err := f.svc.BuildDigest(context.Background(), f.projA, digest.PeriodDaily, start, end)
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	var p digestapp.Payload
	if err := json.Unmarshal(d.Payload, &p); err != nil {
		t.Fatalf("decode payload: %v (%s)", err, d.Payload)
	}

	if p.Event != digestapp.EventDigest || p.ProjectID != f.projA || p.Period != digest.PeriodDaily {
		t.Errorf("identity = %q/%d/%q", p.Event, p.ProjectID, p.Period)
	}
	if !p.WindowStart.Equal(start) || !p.WindowEnd.Equal(end) {
		t.Errorf("window = [%v, %v], want the requested bounds", p.WindowStart, p.WindowEnd)
	}
	if p.RunsTotal != 4 {
		t.Errorf("runs_total = %d, want 4 (window members only)", p.RunsTotal)
	}
	wantOutcomes := digestapp.ByOutcome{Passed: 2, Failed: 1, Aborted: 1}
	if p.ByOutcome != wantOutcomes {
		t.Errorf("by_outcome = %+v, want %+v", p.ByOutcome, wantOutcomes)
	}
	if p.ThresholdFailures != 1 {
		t.Errorf("threshold_failures = %d, want the 1 failed run", p.ThresholdFailures)
	}
	wantExecs := []digestapp.ExecutionSummary{
		{ExecutionID: f.execA1, Name: "checkout", Runs: 3, WorstOutcome: string(taurus.OutcomeFailed)},
		{ExecutionID: f.execA2, Name: "search", Runs: 1, WorstOutcome: string(taurus.OutcomePassed)},
	}
	if !reflect.DeepEqual(p.Executions, wantExecs) {
		t.Errorf("executions = %+v, want %+v", p.Executions, wantExecs)
	}
}

// TestBuildDigestErrorOutcomeCountsButHasNoBucket: an engine-error run is
// part of the window (runs_total, its execution's worst outcome) yet lands
// in no by_outcome bucket -- a digest reports what the load did, and a run
// that never ran did not pass, fail, or abort.
func TestBuildDigestErrorOutcomeCountsButHasNoBucket(t *testing.T) {
	f := newFixture(t)
	start, end := window()
	saveRun(t, f.store, f.execA1, 1, start.Add(time.Hour), taurus.OutcomeError)

	d, err := f.svc.BuildDigest(context.Background(), f.projA, digest.PeriodDaily, start, end)
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	p, err := digestapp.DecodePayload(d.Payload)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if p.RunsTotal != 1 {
		t.Errorf("runs_total = %d, want the error run counted", p.RunsTotal)
	}
	if p.ByOutcome != (digestapp.ByOutcome{}) {
		t.Errorf("by_outcome = %+v, want all-zero buckets", p.ByOutcome)
	}
	if len(p.Executions) != 1 || p.Executions[0].WorstOutcome != string(taurus.OutcomeError) {
		t.Errorf("executions = %+v, want the error run as the worst outcome", p.Executions)
	}
}

// TestBuildDigestRejectsUnknownPeriod: the period grammar is validated
// before any read, so a garbage word cannot mint a row the window math
// would then mis-time.
func TestBuildDigestRejectsUnknownPeriod(t *testing.T) {
	f := newFixture(t)
	start, end := window()
	if _, err := f.svc.BuildDigest(context.Background(), f.projA, digest.Period("hourly"), start, end); !errors.Is(err, digest.ErrPeriodInvalid) {
		t.Fatalf("BuildDigest(hourly) = %v, want ErrPeriodInvalid", err)
	}
}

// TestFireWindowMath pins the bookmark rule: the first digest of a period
// reaches back exactly one period; every later one continues from the last
// same-period digest's window_end -- tiling the timeline without gaps or
// overlaps.
func TestFireWindowMath(t *testing.T) {
	f := newFixture(t)
	day1 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	// Two hours before the first fire: inside the first window, which
	// reaches back a full day.
	saveRun(t, f.store, f.execA1, 1, day1.Add(-2*time.Hour), taurus.OutcomePassed)

	first, err := f.svc.Fire(context.Background(), f.projA, digest.PeriodDaily, day1)
	if err != nil {
		t.Fatalf("first Fire: %v", err)
	}
	if !first.WindowStart.Equal(day1.Add(-24*time.Hour)) || !first.WindowEnd.Equal(day1) {
		t.Errorf("first window = [%v, %v], want [now-24h, now]", first.WindowStart, first.WindowEnd)
	}
	if p, err := digestapp.DecodePayload(first.Payload); err != nil || p.RunsTotal != 1 {
		t.Errorf("first digest runs_total = %d (err %v), want the 1 run inside its window", p.RunsTotal, err)
	}
	if first.ID == 0 {
		t.Error("first digest id = 0, want the stored row's")
	}

	day2 := day1.Add(24 * time.Hour)
	second, err := f.svc.Fire(context.Background(), f.projA, digest.PeriodDaily, day2)
	if err != nil {
		t.Fatalf("second Fire: %v", err)
	}
	if !second.WindowStart.Equal(day1) {
		t.Errorf("second window starts %v, want the first digest's window_end %v", second.WindowStart, day1)
	}
	// A run inside the third window [day2, day3): only the bookmark
	// decides membership now, not the reach-back.
	saveRun(t, f.store, f.execA1, 2, day2.Add(time.Hour), taurus.OutcomeFailed)
	third, err := f.svc.Fire(context.Background(), f.projA, digest.PeriodDaily, day2.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("third Fire: %v", err)
	}
	p, err := digestapp.DecodePayload(third.Payload)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if p.RunsTotal != 1 {
		t.Errorf("third digest runs_total = %d, want 1 (the run inside its bookmarked window)", p.RunsTotal)
	}
}

// TestFireDeliversStoredBytes: firing delivers exactly the stored payload
// as a report.digest event to the project's webhook machinery -- the same
// bytes the feed later serves.
func TestFireDeliversStoredBytes(t *testing.T) {
	f := newFixture(t)
	now, _ := window()
	saveRun(t, f.store, f.execA1, 1, now.Add(time.Hour), taurus.OutcomePassed)

	d, err := f.svc.Fire(context.Background(), f.projA, digest.PeriodDaily, now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("Fire: %v", err)
	}
	if len(f.deliv.calls) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(f.deliv.calls))
	}
	got := f.deliv.calls[0]
	if got.projectID != f.projA || got.event != digestapp.EventDigest {
		t.Errorf("delivery = project %d event %q, want project %d event %q", got.projectID, got.event, f.projA, digestapp.EventDigest)
	}
	if string(got.body) != string(d.Payload) {
		t.Errorf("delivered body = %s, want the stored payload verbatim", got.body)
	}
}

// TestFireDeliveryFailureDoesNotFailFire: the stored row is the source of
// truth. A receiver that is down loses the notification, the fire does not
// -- the same best-effort law run.completed delivery follows.
func TestFireDeliveryFailureDoesNotFailFire(t *testing.T) {
	f := newFixture(t)
	f.deliv.failErr = errors.New("receiver down")
	now, _ := window()

	d, err := f.svc.Fire(context.Background(), f.projA, digest.PeriodDaily, now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("Fire = %v, want success despite delivery failure", err)
	}
	if d.ID == 0 {
		t.Error("digest not stored; the feed must still have it")
	}
}

// TestFireWithoutDelivererStoresOnly: no deliverer wired (the NewService
// default) is a valid deployment -- the in-app feed alone.
func TestFireWithoutDelivererStoresOnly(t *testing.T) {
	store := fake.NewStore()
	proj := mkProject(t, store, "solo")
	mkExecution(t, store, "solo-exec", proj)
	svc := digestapp.NewService(store)
	now, _ := window()
	if _, err := svc.Fire(context.Background(), proj, digest.PeriodWeekly, now); err != nil {
		t.Fatalf("Fire: %v", err)
	}
	rows, err := svc.ListForProject(context.Background(), proj, 0)
	if err != nil {
		t.Fatalf("ListForProject: %v", err)
	}
	if len(rows) != 1 || rows[0].Period != digest.PeriodWeekly {
		t.Fatalf("list = %+v, want the one weekly digest", rows)
	}
}

// TestListForProjectScopesAndOrders: the feed is project-scoped and
// newest-first.
func TestListForProjectScopesAndOrders(t *testing.T) {
	f := newFixture(t)
	day := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	firstA, err := f.svc.Fire(context.Background(), f.projA, digest.PeriodDaily, day)
	if err != nil {
		t.Fatalf("Fire(A, 1): %v", err)
	}
	if _, err := f.svc.Fire(context.Background(), f.projB, digest.PeriodDaily, day); err != nil {
		t.Fatalf("Fire(B): %v", err)
	}
	secondA, err := f.svc.Fire(context.Background(), f.projA, digest.PeriodDaily, day.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("Fire(A, 2): %v", err)
	}

	rows, err := f.svc.ListForProject(context.Background(), f.projA, 0)
	if err != nil {
		t.Fatalf("ListForProject: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != secondA.ID || rows[1].ID != firstA.ID {
		t.Errorf("list(A) = ids %v, want newest-first [%d %d]", idsOf(rows), secondA.ID, firstA.ID)
	}
	limited, err := f.svc.ListForProject(context.Background(), f.projA, 1)
	if err != nil {
		t.Fatalf("ListForProject(1): %v", err)
	}
	if len(limited) != 1 || limited[0].ID != secondA.ID {
		t.Errorf("list(A, 1) = ids %v, want only [%d]", idsOf(limited), secondA.ID)
	}
}

func idsOf(rows []digest.Digest) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}
