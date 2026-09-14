package projectapp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// summaryFixture builds a project with two scenarios, a two-template
// catalog, two executions, and three runs whose trend verdicts are known:
// run 1 hit its requested 100/s, run 2 (same execution, same requested
// load) fell short of it -- a regressed point -- and run 3 lives in the
// other execution at a different requested rate and hit it.
func summaryFixture(t *testing.T) (*fake.Store, *fake.ReportStore, int64) {
	t.Helper()
	store := fake.NewStore()
	reports := fake.NewReportStore()
	ctx := context.Background()

	projectID, err := store.CreateProject(ctx, mustSummaryProject(t))
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	for _, name := range []string{"smoke", "peak"} {
		sc, err := scenario.New(name, projectID)
		if err != nil {
			t.Fatalf("scenario.New(%s): %v", name, err)
		}
		if _, err := store.CreateScenario(ctx, sc); err != nil {
			t.Fatalf("CreateScenario: %v", err)
		}
	}
	for _, name := range []string{"tmpl-a", "tmpl-b"} {
		tmpl, err := scenario.NewTemplate(name, name)
		if err != nil {
			t.Fatalf("NewTemplate(%s): %v", name, err)
		}
		if _, err := store.CreateScenario(ctx, tmpl); err != nil {
			t.Fatalf("CreateScenario(template): %v", err)
		}
	}

	execA := summaryExecution(t, "checkout", projectID, time.Unix(100, 0))
	execB := summaryExecution(t, "billing", projectID, time.Unix(200, 0))
	execAID, err := store.CreateExecution(ctx, execA)
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
	execBID, err := store.CreateExecution(ctx, execB)
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}

	// run 1 (older, exec A): requested 100/s, achieved 100/s -- the baseline.
	saveSummaryReport(t, reports, summaryRun{
		executionID: execAID, runID: 1, startedAt: time.Unix(1000, 0),
		requested: 100, samples: 100, outcome: taurus.OutcomePassed,
	})
	// run 2 (newer, exec A, comparable with run 1): achieved 50/s -- regressed.
	saveSummaryReport(t, reports, summaryRun{
		executionID: execAID, runID: 2, startedAt: time.Unix(2000, 0),
		requested: 100, samples: 50, outcome: taurus.OutcomeFailed,
	})
	// run 3 (exec B, newest overall, nothing comparable before it): hit.
	saveSummaryReport(t, reports, summaryRun{
		executionID: execBID, runID: 3, startedAt: time.Unix(3000, 0),
		requested: 200, samples: 200, outcome: taurus.OutcomePassed,
	})
	return store, reports, projectID
}

func mustSummaryProject(t *testing.T) project.Project {
	t.Helper()
	p, err := project.New("dash", "honryu", "7")
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	return p
}

func summaryExecution(t *testing.T, name string, projectID int64, created time.Time) execution.Execution {
	t.Helper()
	e, err := execution.New(name, projectID)
	if err != nil {
		t.Fatalf("execution.New: %v", err)
	}
	e.CreatedTime = created
	return e
}

// summaryRun is one report.Input's dashboard-relevant fields. The single
// one-second interval makes the measured span exactly 1s, so the achieved
// rate equals samples: a run hits its request when samples >= 95% of it and
// falls short (regressed, given a comparable hit predecessor) below that.
type summaryRun struct {
	executionID int64
	runID       int64
	startedAt   time.Time
	requested   float64
	samples     int64
	outcome     taurus.Outcome
}

func saveSummaryReport(t *testing.T, rs *fake.ReportStore, r summaryRun) {
	t.Helper()
	rep := report.Build(report.Input{
		ExecutionID: r.executionID, RunID: r.runID,
		Engine:    taurus.ExecutorJMeter,
		StartedAt: r.startedAt, EndedAt: r.startedAt.Add(30 * time.Second),
		Outcome:   r.outcome,
		Requested: report.Load{Concurrency: 10, Throughput: r.requested, DurationSeconds: 30},
		Intervals: []metrics.Interval{{
			Timestamp: r.startedAt.Unix(), Label: "load", Samples: r.samples,
			Succeeded: r.samples, Failed: 0,
		}},
	})
	if err := rs.SaveReport(context.Background(), rep); err != nil {
		t.Fatalf("SaveReport(run %d): %v", r.runID, err)
	}
}

func TestSummary_ComposesCountsSeriesAndVerdicts(t *testing.T) {
	t.Parallel()
	store, reports, projectID := summaryFixture(t)
	svc := projectapp.NewService(store).WithReports(reports)

	got, err := svc.Summary(context.Background(), projectID, 0)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if got.ProjectID != projectID {
		t.Errorf("ProjectID = %d, want %d", got.ProjectID, projectID)
	}
	if got.TotalExecutions != 2 {
		t.Errorf("TotalExecutions = %d, want 2", got.TotalExecutions)
	}
	// execB was created later; its creation time is the last-execution time.
	if !got.LastExecutionTime.Equal(time.Unix(200, 0)) {
		t.Errorf("LastExecutionTime = %v, want %v", got.LastExecutionTime, time.Unix(200, 0))
	}
	if got.ScenarioCount != 2 {
		t.Errorf("ScenarioCount = %d, want 2", got.ScenarioCount)
	}
	if got.TemplateCount != 2 {
		t.Errorf("TemplateCount = %d, want 2 (the catalog, templates being projectless)", got.TemplateCount)
	}
	if got.RegressedCount != 1 {
		t.Errorf("RegressedCount = %d, want 1 (run 2 only)", got.RegressedCount)
	}
	if got.LastRun == nil || got.LastRun.RunID != 3 || got.LastRun.Outcome != taurus.OutcomePassed {
		t.Errorf("LastRun = %+v, want run 3 passed", got.LastRun)
	}
	if len(got.ThroughputSeries) != 3 {
		t.Fatalf("ThroughputSeries = %+v, want 3 points", got.ThroughputSeries)
	}
	// Oldest to newest: run 1, run 2, run 3.
	wantOrder := []int64{1, 2, 3}
	for i, want := range wantOrder {
		if got.ThroughputSeries[i].RunID != want {
			t.Errorf("series[%d].RunID = %d, want %d", i, got.ThroughputSeries[i].RunID, want)
		}
	}
	if got.ThroughputSeries[1].Regressed != true {
		t.Errorf("series[1].Regressed = false, want true (run 2 fell short of a comparable hit)")
	}
	if got.ThroughputSeries[0].Regressed || got.ThroughputSeries[2].Regressed {
		t.Errorf("runs 1 and 3 must not be flagged: %+v", got.ThroughputSeries)
	}
	if got.ThroughputSeries[2].AchievedThroughput != 200 {
		t.Errorf("series[2].AchievedThroughput = %f, want 200", got.ThroughputSeries[2].AchievedThroughput)
	}
}

func TestSummary_LimitTakesTheNewestPoints(t *testing.T) {
	t.Parallel()
	store, reports, projectID := summaryFixture(t)
	svc := projectapp.NewService(store).WithReports(reports)

	got, err := svc.Summary(context.Background(), projectID, 2)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if len(got.ThroughputSeries) != 2 {
		t.Fatalf("series = %+v, want 2 points", got.ThroughputSeries)
	}
	if got.ThroughputSeries[0].RunID != 2 || got.ThroughputSeries[1].RunID != 3 {
		t.Errorf("series = [%d,%d], want the two newest [2,3]",
			got.ThroughputSeries[0].RunID, got.ThroughputSeries[1].RunID)
	}
	// The regressed count is the project's whole history, not the window's:
	// run 2's verdict counts even when the window starts at run 2... and so
	// would run 1's, had it regressed. Still exactly one here.
	if got.RegressedCount != 1 {
		t.Errorf("RegressedCount = %d, want 1 regardless of the window", got.RegressedCount)
	}
}

func TestSummary_EmptyProjectIsTheEmptyShape(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	svc := projectapp.NewService(store).WithReports(fake.NewReportStore())
	ctx := context.Background()
	projectID, err := store.CreateProject(ctx, mustSummaryProject(t))
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	got, err := svc.Summary(ctx, projectID, 0)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if got.TotalExecutions != 0 || got.ScenarioCount != 0 || got.RegressedCount != 0 {
		t.Errorf("counts = %+v, want all zero", got)
	}
	if !got.LastExecutionTime.IsZero() {
		t.Errorf("LastExecutionTime = %v, want zero", got.LastExecutionTime)
	}
	if got.LastRun != nil {
		t.Errorf("LastRun = %+v, want nil", got.LastRun)
	}
	if got.ThroughputSeries == nil || len(got.ThroughputSeries) != 0 {
		t.Errorf("ThroughputSeries = %#v, want an empty non-nil slice", got.ThroughputSeries)
	}
}

func TestSummary_WithoutReportStoreDegradesToCounts(t *testing.T) {
	t.Parallel()
	store, _, projectID := summaryFixture(t)
	// No WithReports: the count side must stay exact, the run side empty.
	svc := projectapp.NewService(store)

	got, err := svc.Summary(context.Background(), projectID, 0)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if got.TotalExecutions != 2 || got.ScenarioCount != 2 || got.TemplateCount != 2 {
		t.Errorf("counts = %+v, want the exact counts", got)
	}
	if got.LastRun != nil || got.RegressedCount != 0 || len(got.ThroughputSeries) != 0 {
		t.Errorf("run-derived fields = %+v, want empty/nil (no report store wired)", got)
	}
}

func TestSummary_UnknownProjectIsNotFound(t *testing.T) {
	t.Parallel()
	svc := projectapp.NewService(fake.NewStore())

	if _, err := svc.Summary(context.Background(), 4242, 0); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("error = %v, want ports.ErrNotFound", err)
	}
}
