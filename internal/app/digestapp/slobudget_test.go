package digestapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/digestapp"
	"github.com/heridotlife/honryu/internal/app/sloapp"
	"github.com/heridotlife/honryu/internal/domain/digest"
	"github.com/heridotlife/honryu/internal/domain/slo"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// stubGrader answers ProjectWindowBudgets with canned grades, recording the
// window it was asked to grade -- the assertion that the digest grades its
// own tiling span, not some other window.
type stubGrader struct {
	calls   int
	gotProj int64
	gotWin  [2]time.Time
	grades  []sloapp.WindowOutcome
	err     error
}

func (s *stubGrader) ProjectWindowBudgets(_ context.Context, projectID int64, start, end time.Time) ([]sloapp.WindowOutcome, error) {
	s.calls++
	s.gotProj = projectID
	s.gotWin = [2]time.Time{start, end}
	if s.err != nil {
		return nil, s.err
	}
	return s.grades, nil
}

// mkSLOInProject defines one objective through the domain, with all three
// targets so the worst-metric pick has competition.
func mkSLOInProject(t *testing.T, store *fake.Store, projectID int64, name string) {
	t.Helper()
	p95 := 200.0
	errate := 0.01
	ratio := 0.99
	_, err := store.CreateSLO(context.Background(), slo.SLO{
		ProjectID: projectID, Name: name,
		TargetP95MS: &p95, TargetErrorRate: &errate, TargetSuccessRatio: &ratio,
	})
	if err != nil {
		t.Fatalf("CreateSLO(%s): %v", name, err)
	}
}

func f64(v float64) *float64 { return &v }

// windowOutcome builds one canned grade: worst = the metric named, with the
// remaining percentage given.
func windowOutcome(id int64, name string, compliant bool, worst string, worstPct float64) sloapp.WindowOutcome {
	budget := slo.Budget{
		Metrics: []slo.MetricBudget{
			{Metric: worst, Target: 1, Actual: f64(0.5), Compliant: compliant, BudgetRemainingPct: f64(worstPct)},
		},
		Compliant: compliant,
	}
	return sloapp.WindowOutcome{
		SLO: slo.SLO{ID: id, ProjectID: 1, Name: name}, SLOID: id,
		RunCount: 3, Budget: budget,
	}
}

func TestBuildDigest_CarriesSLOBudgets(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	grader := &stubGrader{grades: []sloapp.WindowOutcome{
		windowOutcome(11, "checkout", true, "p95_ms", 12.5),
		windowOutcome(12, "signup", false, "error_rate", -40),
	}}
	f.svc = digestapp.NewService(f.store).WithSLOGrader(grader)

	end := time.Unix(1_800_000_000, 0)
	start := end.Add(-24 * time.Hour)
	d, err := f.svc.BuildDigest(context.Background(), f.projA, digest.PeriodDaily, start, end)
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	if grader.calls != 1 || grader.gotProj != f.projA {
		t.Fatalf("grader = %d calls for project %d, want 1 call for project %d", grader.calls, grader.gotProj, f.projA)
	}
	if !grader.gotWin[0].Equal(start) || !grader.gotWin[1].Equal(end) {
		t.Errorf("grader window = [%v, %v), want the digest's own span", grader.gotWin[0], grader.gotWin[1])
	}

	var p digestapp.Payload
	if err := json.Unmarshal(d.Payload, &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(p.SLOBudgets) != 2 {
		t.Fatalf("slo_budgets = %d lines, want 2", len(p.SLOBudgets))
	}
	if p.SLOBudgets[0].Name != "checkout" || !p.SLOBudgets[0].Compliant ||
		p.SLOBudgets[0].WorstMetric != "p95_ms" || *p.SLOBudgets[0].WorstBudgetRemainingPct != 12.5 {
		t.Errorf("line 0 = %+v, want compliant checkout worst p95_ms +12.5", p.SLOBudgets[0])
	}
	if p.SLOBudgets[1].Name != "signup" || p.SLOBudgets[1].Compliant ||
		p.SLOBudgets[1].WorstMetric != "error_rate" || *p.SLOBudgets[1].WorstBudgetRemainingPct != -40 {
		t.Errorf("line 1 = %+v, want violated signup worst error_rate -40", p.SLOBudgets[1])
	}
	// The field is on the wire even before decode: an array, not null.
	var raw map[string]any
	if err := json.Unmarshal(d.Payload, &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if arr, ok := raw["slo_budgets"].([]any); !ok || len(arr) != 2 {
		t.Errorf("raw slo_budgets = %v, want a 2-element array", raw["slo_budgets"])
	}
}

func TestBuildDigest_SLOBudgetsEmptyWithoutGrader(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// SLOs exist but no grader is wired (a deployment that has not opted
	// into grading): the array is present and empty, never null.
	mkSLOInProject(t, f.store, f.projA, "ungraded")

	end := time.Unix(1_800_000_000, 0)
	d, err := f.svc.BuildDigest(context.Background(), f.projA, digest.PeriodDaily, end.Add(-24*time.Hour), end)
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(d.Payload, &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if arr, ok := raw["slo_budgets"].([]any); !ok || len(arr) != 0 {
		t.Errorf("raw slo_budgets = %v, want an empty array", raw["slo_budgets"])
	}
}

func TestBuildDigest_SLOBudgetsGraderErrorFailsBuild(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	grader := &stubGrader{err: errors.New("slo store down")}
	f.svc = digestapp.NewService(f.store).WithSLOGrader(grader)

	end := time.Unix(1_800_000_000, 0)
	_, err := f.svc.BuildDigest(context.Background(), f.projA, digest.PeriodDaily, end.Add(-24*time.Hour), end)
	if err == nil {
		t.Fatal("BuildDigest with a failing grader = nil error, want the build to fail loudly")
	}
}

func TestBuildDigest_SLOBudgetsNoDataLineIsVacuous(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// A compliant grade with no worst metric -- the no-eligible-runs
	// shape the budget endpoint serves, passed through verbatim: the
	// digest never invents numbers the endpoint would not.
	grader := &stubGrader{grades: []sloapp.WindowOutcome{{
		SLO:   slo.SLO{ID: 11, ProjectID: 1, Name: "quiet"},
		SLOID: 11, RunCount: 0,
		Budget: slo.Budget{
			Metrics:   []slo.MetricBudget{{Metric: "p95_ms", Target: 200, Actual: nil, Compliant: true, BudgetRemainingPct: nil}},
			Compliant: true,
		},
	}}}
	f.svc = digestapp.NewService(f.store).WithSLOGrader(grader)

	end := time.Unix(1_800_000_000, 0)
	d, err := f.svc.BuildDigest(context.Background(), f.projA, digest.PeriodDaily, end.Add(-24*time.Hour), end)
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	var p digestapp.Payload
	if err := json.Unmarshal(d.Payload, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(p.SLOBudgets) != 1 {
		t.Fatalf("slo_budgets = %d lines, want 1", len(p.SLOBudgets))
	}
	line := p.SLOBudgets[0]
	if !line.Compliant || line.WorstMetric != "" || line.WorstBudgetRemainingPct != nil {
		t.Errorf("line = %+v, want vacuously compliant with no numbers", line)
	}
}
