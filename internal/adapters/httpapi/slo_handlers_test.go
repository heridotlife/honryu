package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/app/sloapp"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// newSLORouter builds the full project surface with the SLO service wired
// over the same stores cmd/api uses -- one repository behind every
// interface, the summary router's shape with the SLO service added.
func newSLORouter(t *testing.T) (http.Handler, *fake.ReportStore) {
	t.Helper()
	store := fake.NewStore()
	reports := store.ReportStore
	obj := fake.NewObjectStore()
	h := httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(store).WithReports(reports),
		Scenarios:     scenarioapp.NewService(store, obj),
		Executions:    executionapp.NewService(store, obj, 100),
		SLOs:          sloapp.NewService(store),
		Store:         obj,
		DefaultOwners: []string{"honryu"},
	})
	return h, reports
}

// createSLOFor defines one SLO through the API, returning its id.
func createSLOFor(t *testing.T, h http.Handler, projectID int64, form url.Values) int64 {
	t.Helper()
	rec := postForm(t, h, "/api/projects/"+itoa(projectID)+"/slos", form)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create SLO = %d (%s)", rec.Code, rec.Body.String())
	}
	return decodeID(t, rec)
}

func sloForm(name string) url.Values {
	return url.Values{
		"name":                 {name},
		"target_p95_ms":        {"250"},
		"target_error_rate":    {"0.01"},
		"target_success_ratio": {"0.99"},
	}
}

func TestSLO_CreateListDelete(t *testing.T) {
	t.Parallel()
	h, _ := newSLORouter(t)
	projectID := createProjectForWebhooks(t, h, "slo-crud")

	id := createSLOFor(t, h, projectID, sloForm("checkout"))
	if id <= 0 {
		t.Fatalf("created id = %d, want positive", id)
	}

	rec := do(t, h, http.MethodGet, "/api/projects/"+itoa(projectID)+"/slos")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d (%s)", rec.Code, rec.Body.String())
	}
	var listed []struct {
		ID                 int64    `json:"id"`
		Name               string   `json:"name"`
		TargetP95MS        *float64 `json:"target_p95_ms"`
		TargetErrorRate    *float64 `json:"target_error_rate"`
		TargetSuccessRatio *float64 `json:"target_success_ratio"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v (%s)", err, rec.Body.String())
	}
	if len(listed) != 1 || listed[0].ID != id || listed[0].Name != "checkout" {
		t.Fatalf("list = %+v, want one SLO id %d named checkout", listed, id)
	}
	if listed[0].TargetP95MS == nil || *listed[0].TargetP95MS != 250 {
		t.Errorf("target_p95_ms = %v, want 250", listed[0].TargetP95MS)
	}

	rec = do(t, h, http.MethodDelete, "/api/projects/"+itoa(projectID)+"/slos/"+itoa(id))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d (%s)", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodDelete, "/api/projects/"+itoa(projectID)+"/slos/"+itoa(id))
	if rec.Code != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", rec.Code)
	}
}

func TestSLO_CreateValidation(t *testing.T) {
	t.Parallel()
	h, _ := newSLORouter(t)
	projectID := createProjectForWebhooks(t, h, "slo-validate")

	// No targets at all: 400 with the domain's stated reason.
	rec := postForm(t, h, "/api/projects/"+itoa(projectID)+"/slos", url.Values{"name": {"empty"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("create with no targets = %d (%s), want 400", rec.Code, rec.Body.String())
	}
	// A single target is enough.
	rec = postForm(t, h, "/api/projects/"+itoa(projectID)+"/slos", url.Values{"name": {"ratio-only"}, "target_success_ratio": {"0.99"}})
	if rec.Code != http.StatusCreated {
		t.Errorf("create with one target = %d (%s), want 201", rec.Code, rec.Body.String())
	}
	// Non-numeric target: 400, not silently dropped.
	rec = postForm(t, h, "/api/projects/"+itoa(projectID)+"/slos", url.Values{"name": {"typo"}, "target_p95_ms": {"fast"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("create with non-numeric target = %d, want 400", rec.Code)
	}
	// Duplicate name: 409 -- the fix is a different name.
	rec = postForm(t, h, "/api/projects/"+itoa(projectID)+"/slos", url.Values{"name": {"ratio-only"}, "target_p95_ms": {"100"}})
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate name = %d (%s), want 409", rec.Code, rec.Body.String())
	}
}

func TestSLO_ScopingAndUnknownProject(t *testing.T) {
	t.Parallel()
	h, _ := newSLORouter(t)
	projectID := createProjectForWebhooks(t, h, "slo-scope")
	id := createSLOFor(t, h, projectID, sloForm("checkout"))

	// A foreign project's path never sees project 1's SLO.
	other := createProjectForWebhooks(t, h, "slo-other")
	rec := do(t, h, http.MethodDelete, "/api/projects/"+itoa(other)+"/slos/"+itoa(id))
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-project delete = %d, want 404 (not this project's to delete)", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/api/projects/"+itoa(other)+"/slos/"+itoa(id)+"/budget")
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-project budget = %d, want 404 (not this project's to read)", rec.Code)
	}
	// An unknown project is the repo's own 404.
	rec = do(t, h, http.MethodGet, "/api/projects/999/slos")
	if rec.Code != http.StatusNotFound {
		t.Errorf("list under unknown project = %d, want 404", rec.Code)
	}
}

func TestSLO_BudgetEndpoint(t *testing.T) {
	t.Parallel()
	h, reports := newSLORouter(t)
	projectID := createProjectForWebhooks(t, h, "slo-budget")

	// An execution under the project (flow tests' shape), then two runs
	// seeded straight into the report store -- the seeding skips the
	// lifecycle because the handler composes from storage.
	rec := postForm(t, h, "/api/executions", url.Values{"name": {"peak"}, "project_id": {itoa(projectID)}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create execution = %d (%s)", rec.Code, rec.Body.String())
	}
	executionID := decodeID(t, rec)

	now := time.Now().Add(-time.Hour)
	seed := func(runID int64, outcome taurus.Outcome, p95Sec, errRate float64) {
		t.Helper()
		rep := report.Report{
			ExecutionID: executionID, RunID: runID,
			StartedAt: now, EndedAt: now.Add(30 * time.Second),
			Outcome: outcome, ErrorRate: errRate,
			Latency: report.Percentiles{95: p95Sec},
		}
		if err := reports.SaveReport(context.Background(), rep); err != nil {
			t.Fatalf("SaveReport: %v", err)
		}
	}
	seed(1, taurus.OutcomePassed, 0.125, 0)    // 125ms
	seed(2, taurus.OutcomeFailed, 0.375, 0.02) // 375ms

	id := createSLOFor(t, h, projectID, sloForm("checkout"))
	rec = do(t, h, http.MethodGet, "/api/projects/"+itoa(projectID)+"/slos/"+itoa(id)+"/budget?window=7d")
	if rec.Code != http.StatusOK {
		t.Fatalf("budget = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		SLOID     int64  `json:"slo_id"`
		Name      string `json:"name"`
		Window    string `json:"window"`
		RunCount  int    `json:"run_count"`
		Compliant bool   `json:"compliant"`
		Metrics   []struct {
			Metric             string   `json:"metric"`
			Target             float64  `json:"target"`
			Actual             *float64 `json:"actual"`
			Compliant          bool     `json:"compliant"`
			BudgetRemainingPct *float64 `json:"budget_remaining_pct"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode budget: %v (%s)", err, rec.Body.String())
	}
	if got.SLOID != id || got.Name != "checkout" || got.Window != "7d" {
		t.Errorf("identity = %+v, want slo %d checkout 7d", got, id)
	}
	if got.RunCount != 2 || got.Compliant {
		t.Errorf("run_count/compliant = %d/%v, want 2/false (success ratio burned)", got.RunCount, got.Compliant)
	}
	if len(got.Metrics) != 3 {
		t.Fatalf("metrics = %d lines, want 3 (all targets set)", len(got.Metrics))
	}
	// p95: mean(125, 375) = 250ms, exactly at target, 0 remaining.
	p95 := got.Metrics[0]
	if p95.Metric != "p95_ms" || !p95.Compliant || p95.Actual == nil || *p95.Actual != 250 || *p95.BudgetRemainingPct != 0 {
		t.Errorf("p95 line = %+v, want compliant at exactly 250ms/0%%", p95)
	}
}

func TestSLO_BudgetWindowParam(t *testing.T) {
	t.Parallel()
	h, _ := newSLORouter(t)
	projectID := createProjectForWebhooks(t, h, "slo-window")
	id := createSLOFor(t, h, projectID, sloForm("checkout"))

	base := "/api/projects/" + itoa(projectID) + "/slos/" + itoa(id) + "/budget"
	// Omitted window: the 7d default.
	rec := do(t, h, http.MethodGet, base)
	if rec.Code != http.StatusOK {
		t.Fatalf("budget(no window) = %d", rec.Code)
	}
	var got struct {
		Window string `json:"window"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Window != "7d" {
		t.Errorf("default window = %q, want 7d", got.Window)
	}
	for _, w := range []string{"1d", "30d"} {
		rec := do(t, h, http.MethodGet, base+"?window="+w)
		if rec.Code != http.StatusOK {
			t.Errorf("budget(window=%s) = %d", w, rec.Code)
		}
	}
	// An unsupported window is the caller's typo: 400.
	rec = do(t, h, http.MethodGet, base+"?window=14d")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("budget(window=14d) = %d, want 400", rec.Code)
	}
}

func TestSLO_BudgetNoRunsIsVacuouslyCompliant(t *testing.T) {
	t.Parallel()
	h, _ := newSLORouter(t)
	projectID := createProjectForWebhooks(t, h, "slo-noruns")
	id := createSLOFor(t, h, projectID, sloForm("checkout"))

	rec := do(t, h, http.MethodGet, "/api/projects/"+itoa(projectID)+"/slos/"+itoa(id)+"/budget?window=1d")
	if rec.Code != http.StatusOK {
		t.Fatalf("budget = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		RunCount  int  `json:"run_count"`
		Compliant bool `json:"compliant"`
		Metrics   []struct {
			Actual             *float64 `json:"actual"`
			BudgetRemainingPct *float64 `json:"budget_remaining_pct"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.RunCount != 0 || !got.Compliant {
		t.Errorf("no-runs budget = run_count %d compliant %v, want 0/true", got.RunCount, got.Compliant)
	}
	for _, m := range got.Metrics {
		if m.Actual != nil || m.BudgetRemainingPct != nil {
			t.Errorf("metric carries numbers with no data: %+v", m)
		}
	}
}
func TestSLO_GateWhenDisabled(t *testing.T) {
	t.Helper()
	store := fake.NewStore()
	reports := store.ReportStore
	obj := fake.NewObjectStore()
	// Router WITHOUT SLOs wired
	h := httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(store).WithReports(reports),
		Scenarios:     scenarioapp.NewService(store, obj),
		Executions:    executionapp.NewService(store, obj, 100),
		SLOs:          nil, // <-- the gate is what we are testing
		Store:         obj,
		DefaultOwners: []string{"honryu"},
	})
	projectID := createProjectForWebhooks(t, h, "slo-gate-disabled")

	// Any SLO endpoint should return 404 "slos not configured"
	rec := do(t, h, "GET", "/api/projects/"+itoa(projectID)+"/slos")
	if rec.Code != http.StatusNotFound {
		t.Errorf("list = %d (%s), want 404", rec.Code, rec.Body.String())
	}
	rec = do(t, h, "POST", "/api/projects/"+itoa(projectID)+"/slos")
	if rec.Code != http.StatusNotFound {
		t.Errorf("create = %d (%s), want 404", rec.Code, rec.Body.String())
	}
}
