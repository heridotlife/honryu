package sloapp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/sloapp"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/slo"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// newService builds the service over fresh fakes, plus the report store so a
// test can seed runs. The composition shape matches cmd/api's: one store
// behind every interface.
func newService(t *testing.T) (*sloapp.Service, *fake.Store, *fake.ReportStore) {
	t.Helper()
	store := fake.NewStore() // embeds the SLO registry alongside projects
	reports := store.ReportStore
	return sloapp.NewService(store), store, reports
}

func f(v float64) *float64 { return &v }

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// newProject provisions project 1 (the first fake-assigned id) the way the
// domain does, so the store's own constructor rules stay in force.
func newProject(t *testing.T, store *fake.Store) {
	t.Helper()
	p, err := project.New("honryu", "honryu", "42")
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	if _, err := store.CreateProject(context.Background(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
}

// newExecution provisions an execution under projectID (fake ids start at 1,
// so the first execution created after one project is execution 1).
func newExecution(t *testing.T, store *fake.Store, projectID int64) {
	t.Helper()
	e, err := execution.New("peak", projectID)
	if err != nil {
		t.Fatalf("execution.New: %v", err)
	}
	if _, err := store.CreateExecution(context.Background(), e); err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
}

// seedRun saves one report directly into the fake store, the way the summary
// and digest tests do -- the service composes from storage, so seeding skips
// the lifecycle. p95Sec is the run's p95 latency in seconds (reports' unit).
func seedRun(t *testing.T, rs *fake.ReportStore, executionID, runID int64, startedAt time.Time, outcome taurus.Outcome, p95Sec, errorRate float64) {
	t.Helper()
	rep := report.Report{
		ExecutionID: executionID, RunID: runID,
		StartedAt: startedAt, EndedAt: startedAt.Add(30 * time.Second),
		Outcome:   outcome,
		ErrorRate: errorRate,
		Latency:   report.Percentiles{95: p95Sec},
	}
	if err := rs.SaveReport(context.Background(), rep); err != nil {
		t.Fatalf("SaveReport(run %d): %v", runID, err)
	}
}

func mkSLO(projectID int64) slo.SLO {
	return slo.SLO{
		ProjectID: projectID, Name: "checkout",
		TargetP95MS: f(200), TargetErrorRate: f(0.01), TargetSuccessRatio: f(0.99),
	}
}

func TestCreate_ValidatesAndPersists(t *testing.T) {
	t.Parallel()
	svc, store, _ := newService(t)
	ctx := context.Background()

	newProject(t, store)
	created, err := svc.Create(ctx, mkSLO(1))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Error("created.ID is zero, want the storage-assigned id")
	}
	got, err := svc.List(ctx, 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("List = %v (%v), want one SLO", got, err)
	}
	if _, err := svc.Get(ctx, 1, created.ID); err != nil {
		t.Errorf("Get: %v", err)
	}
	if err := svc.Delete(ctx, 1, created.ID); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, 1, created.ID); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
}

func TestCreate_RejectsInvalid(t *testing.T) {
	t.Parallel()
	svc, store, _ := newService(t)
	ctx := context.Background()
	newProject(t, store)

	if _, err := svc.Create(ctx, slo.SLO{ProjectID: 1, Name: "no targets"}); !errors.Is(err, slo.ErrNoTargets) {
		t.Errorf("Create(no targets) = %v, want ErrNoTargets", err)
	}
	if _, err := svc.Create(ctx, mkSLO(99)); err != nil {
		// A foreign project id is not the service's to police (the HTTP
		// layer authorizes first); Create only validates the object.
		t.Logf("Create(foreign project) = %v (acceptable: validation only)", err)
	}
}

func TestCreate_RejectsDuplicateNameInProject(t *testing.T) {
	t.Parallel()
	svc, store, _ := newService(t)
	ctx := context.Background()
	newProject(t, store)
	if _, err := svc.Create(ctx, mkSLO(1)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Create(ctx, mkSLO(1)); !errors.Is(err, sloapp.ErrDuplicateName) {
		t.Errorf("second Create = %v, want ErrDuplicateName", err)
	}
	// The same name under a different project is fine.
	if _, err := svc.Create(ctx, mkSLO(2)); err != nil {
		t.Errorf("Create(same name, other project) = %v, want nil", err)
	}
}

func TestBudget_GradesTheWindow(t *testing.T) {
	t.Parallel()
	svc, store, reports := newService(t)
	ctx := context.Background()
	newProject(t, store)
	newExecution(t, store, 1)
	obj, err := svc.Create(ctx, slo.SLO{
		ProjectID: 1, Name: "checkout",
		TargetP95MS: f(250), TargetErrorRate: f(0.01), TargetSuccessRatio: f(0.99),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	now := time.Unix(1_800_000_000, 0)
	// Window is 7d: two in-window runs (one pass, one failed) and one old
	// run outside it. The p95 values are exact binary fractions so the
	// means land exactly on the targets (float64-exact arithmetic, the
	// same convention the code itself keeps).
	seedRun(t, reports, 1, 1, now.Add(-24*time.Hour), taurus.OutcomePassed, 0.125, 0)   // 125ms
	seedRun(t, reports, 1, 2, now.Add(-2*time.Hour), taurus.OutcomeFailed, 0.375, 0.02) // 375ms
	seedRun(t, reports, 1, 3, now.Add(-8*24*time.Hour), taurus.OutcomePassed, 0.001, 0) // outside

	out, err := svc.Budget(ctx, 1, obj.ID, slo.Window7d, now)
	if err != nil {
		t.Fatalf("Budget: %v", err)
	}
	if out.RunCount != 2 {
		t.Errorf("run_count = %d, want 2 (the outside run stays out)", out.RunCount)
	}
	if out.WindowEnd != now || !out.WindowStart.Equal(now.Add(-7*24*time.Hour)) {
		t.Errorf("window = [%v, %v), want the 7d span ending at now", out.WindowStart, out.WindowEnd)
	}
	byMetric := map[string]slo.MetricBudget{}
	for _, m := range out.Budget.Metrics {
		byMetric[m.Metric] = m
	}
	// p95: mean(125ms, 375ms) = 250ms -- exactly at target, 0 remaining.
	if p := byMetric[slo.MetricP95MS]; !p.Compliant || p.Actual == nil || *p.Actual != 250 || *p.BudgetRemainingPct != 0 {
		t.Errorf("p95 line = %+v, want compliant at exactly 250ms / 0%%", p)
	}
	// error rate: mean(0, 0.02) = 0.010 -- at target.
	if e := byMetric[slo.MetricErrorRate]; !e.Compliant || e.Actual == nil || abs(*e.Actual-0.01) > 1e-12 || abs(*e.BudgetRemainingPct) > 1e-9 {
		t.Errorf("error-rate line = %+v, want at-target", e)
	}
	// success ratio: 1 passed of 2 non-aborted = 0.5, way under 0.99.
	if sr := byMetric[slo.MetricSuccessRatio]; sr.Compliant || sr.Actual == nil || *sr.Actual != 0.5 {
		t.Errorf("success-ratio line = %+v, want violated at 0.5", sr)
	}
	if out.Budget.Compliant {
		t.Error("budget = compliant, want violated (success ratio burned)")
	}
	worst, ok := out.Budget.Worst()
	if !ok || worst.Metric != slo.MetricSuccessRatio {
		t.Errorf("worst = %+v, want success_ratio", worst)
	}
}

func TestBudget_AbortedRunsAreExcluded(t *testing.T) {
	t.Parallel()
	svc, store, reports := newService(t)
	ctx := context.Background()
	newProject(t, store)
	newExecution(t, store, 1)
	obj, err := svc.Create(ctx, slo.SLO{ProjectID: 1, Name: "s", TargetSuccessRatio: f(0.99)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	now := time.Unix(1_800_000_000, 0)
	seedRun(t, reports, 1, 1, now.Add(-time.Hour), taurus.OutcomeAborted, 9.9, 0.99)
	seedRun(t, reports, 1, 2, now.Add(-time.Hour), taurus.OutcomePassed, 0.1, 0)

	out, err := svc.Budget(ctx, 1, obj.ID, slo.Window1d, now)
	if err != nil {
		t.Fatalf("Budget: %v", err)
	}
	if out.RunCount != 1 {
		t.Errorf("run_count = %d, want 1 (the abort never grades)", out.RunCount)
	}
	if sr := out.Budget.Metrics[0]; !out.Budget.Compliant || sr.Actual == nil || *sr.Actual != 1 {
		t.Errorf("success-ratio line = %+v compliant=%v, want 1.0 compliant", sr, out.Budget.Compliant)
	}
}

func TestBudget_AllAbortedIsNoData(t *testing.T) {
	t.Parallel()
	svc, store, reports := newService(t)
	ctx := context.Background()
	newProject(t, store)
	newExecution(t, store, 1)
	obj, err := svc.Create(ctx, mkSLO(1))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	now := time.Unix(1_800_000_000, 0)
	seedRun(t, reports, 1, 1, now.Add(-time.Hour), taurus.OutcomeAborted, 9.9, 0.99)

	out, err := svc.Budget(ctx, 1, obj.ID, slo.Window1d, now)
	if err != nil {
		t.Fatalf("Budget: %v", err)
	}
	if out.RunCount != 0 || !out.Budget.Compliant {
		t.Errorf("outcome = run_count %d compliant %v, want 0 and vacuously true", out.RunCount, out.Budget.Compliant)
	}
	for _, m := range out.Budget.Metrics {
		if m.Actual != nil || m.BudgetRemainingPct != nil {
			t.Errorf("%s line carries numbers with no data: %+v", m.Metric, m)
		}
	}
}

func TestBudget_EmptyWindowAndUnknownSLO(t *testing.T) {
	t.Parallel()
	svc, store, _ := newService(t)
	ctx := context.Background()
	newProject(t, store)
	obj, err := svc.Create(ctx, mkSLO(1))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	now := time.Unix(1_800_000_000, 0)
	out, err := svc.Budget(ctx, 1, obj.ID, slo.Window30d, now)
	if err != nil {
		t.Fatalf("Budget(empty window): %v", err)
	}
	if out.RunCount != 0 || !out.Budget.Compliant {
		t.Errorf("empty window = run_count %d compliant %v, want 0/true", out.RunCount, out.Budget.Compliant)
	}

	if _, err := svc.Budget(ctx, 1, obj.ID+1, slo.Window7d, now); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("Budget(unknown SLO) = %v, want ErrNotFound", err)
	}
	if _, err := svc.Budget(ctx, 99, obj.ID, slo.Window7d, now); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("Budget(foreign project) = %v, want ErrNotFound (scoped read)", err)
	}
	if _, err := svc.Budget(ctx, 1, obj.ID, "14d", now); !errors.Is(err, slo.ErrWindowInvalid) {
		t.Errorf("Budget(bad window) = %v, want ErrWindowInvalid", err)
	}
}
func TestSLO_StringRendersNameFirst(t *testing.T) {
	t.Parallel()
	s := slo.SLO{Name: "checkout p95", ProjectID: 42}
	if got := s.String(); got != "checkout p95 (project 42)" {
		t.Errorf("String() = %q, want name-first rendering", got)
	}
}

func TestProjectWindowBudgets_GradesEverySLO(t *testing.T) {
	t.Parallel()
	svc, store, reports := newService(t)
	ctx := context.Background()
	newProject(t, store)
	newExecution(t, store, 1)

	// Two SLOs on the same project; one healthy run inside the window.
	if _, err := svc.Create(ctx, mkSLO(1)); err != nil {
		t.Fatalf("Create alpha: %v", err)
	}
	second := mkSLO(1)
	second.Name = "beta"
	if _, err := svc.Create(ctx, second); err != nil {
		t.Fatalf("Create beta: %v", err)
	}
	seedRun(t, reports, 1, 1, time.Now().Add(-time.Minute), taurus.OutcomePassed, 0.15, 0.001)

	out, err := svc.ProjectWindowBudgets(ctx, 1, time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatalf("ProjectWindowBudgets: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("outcomes = %d, want 2 (one per SLO)", len(out))
	}
	for _, o := range out {
		if o.SLOID <= 0 {
			t.Errorf("outcome %+v carries no SLO id", o)
		}
	}
}

func TestProjectWindowBudgets_EmptyProjectReturnsEmpty(t *testing.T) {
	t.Parallel()
	svc, store, _ := newService(t)
	ctx := context.Background()
	newProject(t, store)

	out, err := svc.ProjectWindowBudgets(ctx, 1, time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatalf("ProjectWindowBudgets: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("outcomes = %d, want 0 for project without SLOs", len(out))
	}
}
