package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// newSummaryRouter builds the full project surface over a caller-owned store
// plus a report store the summary read composes -- the same wiring shape
// cmd/api uses (Projects.WithReports over one repository).
func newSummaryRouter(t *testing.T) (http.Handler, *fake.ReportStore) {
	t.Helper()
	store := fake.NewStore()
	reports := fake.NewReportStore()
	obj := fake.NewObjectStore()
	h := httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(store).WithReports(reports),
		Scenarios:     scenarioapp.NewService(store, obj),
		Executions:    executionapp.NewService(store, obj, 100),
		Store:         obj,
		DefaultOwners: []string{"honryu"},
	})
	return h, reports
}

// createExecutionForSummary provisions an execution under the project the
// way the phase 1 flow tests do.
func createExecutionForSummary(t *testing.T, h http.Handler, projectID int64) int64 {
	t.Helper()
	rec := postForm(t, h, "/api/executions", url.Values{"name": {"peak"}, "project_id": {itoa(projectID)}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create execution = %d (%s)", rec.Code, rec.Body.String())
	}
	return decodeID(t, rec)
}

// summaryRunAt saves one report directly into the fake store: the handler
// composes from storage, so seeding skips the lifecycle. The single
// one-second interval makes the achieved rate exactly `samples` per second.
func summaryRunAt(t *testing.T, rs *fake.ReportStore, executionID, runID int64, startedAt time.Time, requested float64, samples int64, outcome taurus.Outcome) {
	t.Helper()
	rep := report.Build(report.Input{
		ExecutionID: executionID, RunID: runID,
		Engine:    taurus.ExecutorJMeter,
		StartedAt: startedAt, EndedAt: startedAt.Add(30 * time.Second),
		Outcome:   outcome,
		Requested: report.Load{Concurrency: 10, Throughput: requested, DurationSeconds: 30},
		Intervals: []metrics.Interval{{
			Timestamp: startedAt.Unix(), Label: "load", Samples: samples,
			Succeeded: samples, Failed: 0,
		}},
	})
	if err := rs.SaveReport(context.Background(), rep); err != nil {
		t.Fatalf("SaveReport(run %d): %v", runID, err)
	}
}

func TestProjectSummary_ServesComposedDashboard(t *testing.T) {
	t.Parallel()
	h, reports := newSummaryRouter(t)
	projectID := createProjectForWebhooks(t, h, "dash")
	executionID := createExecutionForSummary(t, h, projectID)

	// Two comparable runs: run 1 hit its requested 10/s (the baseline), run
	// 2 fell short of it -- the regressed point the KPI counts.
	summaryRunAt(t, reports, executionID, 1, time.Unix(1000, 0), 10, 10, taurus.OutcomePassed)
	summaryRunAt(t, reports, executionID, 2, time.Unix(2000, 0), 10, 4, taurus.OutcomeFailed)

	rec := do(t, h, http.MethodGet, "/api/projects/"+itoa(projectID)+"/summary")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET summary = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		ProjectID         int64  `json:"project_id"`
		TotalExecutions   int    `json:"total_executions"`
		LastExecutionTime string `json:"last_execution_time"`
		ScenarioCount     int    `json:"scenario_count"`
		TemplateCount     int    `json:"template_count"`
		RegressedCount    int    `json:"regressed_count"`
		LastRun           *struct {
			RunID   int64  `json:"run_id"`
			Outcome string `json:"outcome"`
		} `json:"last_run"`
		ThroughputSeries []struct {
			RunID              int64   `json:"run_id"`
			AchievedThroughput float64 `json:"achieved_throughput"`
			Regressed          bool    `json:"regressed"`
		} `json:"throughput_series"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.ProjectID != projectID || got.TotalExecutions != 1 {
		t.Errorf("identity = %+v, want project %d with 1 execution", got, projectID)
	}
	if got.LastExecutionTime == "" {
		t.Error("last_execution_time absent, want the execution's creation time")
	}
	if got.RegressedCount != 1 {
		t.Errorf("regressed_count = %d, want 1 (run 2 only)", got.RegressedCount)
	}
	if got.LastRun == nil || got.LastRun.RunID != 2 || got.LastRun.Outcome != "failed" {
		t.Errorf("last_run = %+v, want run 2 failed", got.LastRun)
	}
	if len(got.ThroughputSeries) != 2 {
		t.Fatalf("throughput_series = %+v, want 2 points", got.ThroughputSeries)
	}
	// Oldest first: run 1 (clean), run 2 (regressed, 4/s achieved).
	if got.ThroughputSeries[0].RunID != 1 || got.ThroughputSeries[0].Regressed {
		t.Errorf("series[0] = %+v, want run 1 unflagged", got.ThroughputSeries[0])
	}
	if got.ThroughputSeries[1].RunID != 2 || !got.ThroughputSeries[1].Regressed {
		t.Errorf("series[1] = %+v, want run 2 flagged", got.ThroughputSeries[1])
	}
	if got.ThroughputSeries[1].AchievedThroughput != 4 {
		t.Errorf("series[1].achieved = %f, want 4", got.ThroughputSeries[1].AchievedThroughput)
	}
}

func TestProjectSummary_EmptyProjectIsTheEmptyState(t *testing.T) {
	t.Parallel()
	h, _ := newSummaryRouter(t)
	projectID := createProjectForWebhooks(t, h, "empty-dash")

	rec := do(t, h, http.MethodGet, "/api/projects/"+itoa(projectID)+"/summary")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET summary = %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"total_executions":0`,
		`"last_run":null`,
		`"throughput_series":[]`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %s: %s", want, body)
		}
	}
}

func TestProjectSummary_LimitCapsTheSeries(t *testing.T) {
	t.Parallel()
	h, reports := newSummaryRouter(t)
	projectID := createProjectForWebhooks(t, h, "dash-limit")
	executionID := createExecutionForSummary(t, h, projectID)
	for i, runID := range []int64{1, 2, 3} {
		summaryRunAt(t, reports, executionID, runID, time.Unix(int64(1000+i*100), 0), 0, int64(10+i), taurus.OutcomePassed)
	}

	rec := do(t, h, http.MethodGet, "/api/projects/"+itoa(projectID)+"/summary?limit=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET summary?limit=2 = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		ThroughputSeries []struct {
			RunID int64 `json:"run_id"`
		} `json:"throughput_series"`
		RegressedCount int `json:"regressed_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(got.ThroughputSeries) != 2 {
		t.Fatalf("series = %+v, want 2 points", got.ThroughputSeries)
	}
	if got.ThroughputSeries[0].RunID != 2 || got.ThroughputSeries[1].RunID != 3 {
		t.Errorf("series = %+v, want the two newest runs [2 3]", got.ThroughputSeries)
	}
}

func TestProjectSummary_UnknownProjectIs404(t *testing.T) {
	t.Parallel()
	h, _ := newSummaryRouter(t)

	rec := do(t, h, http.MethodGet, "/api/projects/4242/summary")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET summary for unknown project = %d (%s), want 404", rec.Code, rec.Body.String())
	}
}
