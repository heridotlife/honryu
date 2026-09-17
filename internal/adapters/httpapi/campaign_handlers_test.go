package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/campaignapp"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/reservation"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

func newCampaignRouter(t *testing.T) (http.Handler, *fake.Store) {
	t.Helper()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	h := httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(store),
		Executions:    executionapp.NewService(store, obj, 100),
		Campaigns:     campaignapp.NewService(store, fake.NewScheduler()),
		Store:         obj,
		DefaultOwners: []string{"honryu"},
	})
	return h, store
}

// seedProjectAndExecution creates a project (belonging to tenantID --
// campaignapp.Create now rejects a service whose project belongs to a
// different tenant than the campaign's own) and an execution under it via
// the HTTP API, and returns both ids.
func seedProjectAndExecution(t *testing.T, h http.Handler, name string, tenantID int64) (projectID, executionID int64) {
	t.Helper()
	projectID = decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {name}, "owner": {"honryu"}, "tenant_id": {itoa(tenantID)}}))
	executionID = decodeID(t, postForm(t, h, "/api/executions", url.Values{"name": {"readiness"}, "project_id": {itoa(projectID)}}))
	return projectID, executionID
}

type campaignRow struct {
	ID       int64 `json:"id"`
	TenantID int64 `json:"tenant_id"`
	Services []struct {
		ProjectID   int64 `json:"project_id"`
		ExecutionID int64 `json:"execution_id"`
	} `json:"services"`
	Active bool `json:"active"`
}

func TestCreateCampaign_AdmitsAndReturnsCreated(t *testing.T) {
	t.Parallel()
	h, _ := newCampaignRouter(t)
	projectA, execA := seedProjectAndExecution(t, h, "service-a", 7)
	projectB, execB := seedProjectAndExecution(t, h, "service-b", 7)

	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	rec := postForm(t, h, "/api/tenants/7/campaigns", url.Values{
		"name":                 {"Supersale 11.11"},
		"window_start":         {start},
		"window_end":           {end},
		"service_project_id":   {itoa(projectA), itoa(projectB)},
		"service_execution_id": {itoa(execA), itoa(execB)},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create campaign = %d (%s)", rec.Code, rec.Body.String())
	}

	var got campaignRow
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.ID <= 0 || got.TenantID != 7 {
		t.Fatalf("campaign = %+v, want id > 0 and tenant_id 7", got)
	}
	if len(got.Services) != 2 {
		t.Fatalf("services = %+v, want 2", got.Services)
	}
	if got.Active {
		t.Error("a campaign whose window has not started yet must not be active")
	}
}

// The invariant campaignapp.Create enforces: a service's designated
// execution must actually belong to the project it's registered under.
func TestCreateCampaign_RejectsMismatchedExecutionProject(t *testing.T) {
	t.Parallel()
	h, _ := newCampaignRouter(t)
	_, execA := seedProjectAndExecution(t, h, "service-a", 7)
	projectB, _ := seedProjectAndExecution(t, h, "service-b", 7)

	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	rec := postForm(t, h, "/api/tenants/7/campaigns", url.Values{
		"name":                 {"c"},
		"window_start":         {start},
		"window_end":           {end},
		"service_project_id":   {itoa(projectB)},
		"service_execution_id": {itoa(execA)}, // belongs to a different project
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create campaign (mismatched execution) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCreateCampaign_RejectsInvalidWindow(t *testing.T) {
	t.Parallel()
	h, _ := newCampaignRouter(t)
	projectA, execA := seedProjectAndExecution(t, h, "service-a", 7)

	start := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(time.Hour).UTC().Format(time.RFC3339) // before start
	rec := postForm(t, h, "/api/tenants/7/campaigns", url.Values{
		"name":                 {"c"},
		"window_start":         {start},
		"window_end":           {end},
		"service_project_id":   {itoa(projectA)},
		"service_execution_id": {itoa(execA)},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create campaign (inverted window) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCreateCampaign_RejectsMismatchedServicePairCounts(t *testing.T) {
	t.Parallel()
	h, _ := newCampaignRouter(t)
	projectA, execA := seedProjectAndExecution(t, h, "service-a", 7)

	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	rec := postForm(t, h, "/api/tenants/7/campaigns", url.Values{
		"name":                 {"c"},
		"window_start":         {start},
		"window_end":           {end},
		"service_project_id":   {itoa(projectA), "999"},
		"service_execution_id": {itoa(execA)}, // one fewer than service_project_id
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create campaign (mismatched pair counts) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestListCampaigns_ScopesByTenant(t *testing.T) {
	t.Parallel()
	h, _ := newCampaignRouter(t)
	project7, exec7 := seedProjectAndExecution(t, h, "service-a", 7)
	project9, exec9 := seedProjectAndExecution(t, h, "service-a-tenant-9", 9)

	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	form7 := url.Values{
		"name": {"c"}, "window_start": {start}, "window_end": {end},
		"service_project_id": {itoa(project7)}, "service_execution_id": {itoa(exec7)},
	}
	form9 := url.Values{
		"name": {"c"}, "window_start": {start}, "window_end": {end},
		"service_project_id": {itoa(project9)}, "service_execution_id": {itoa(exec9)},
	}
	if rec := postForm(t, h, "/api/tenants/7/campaigns", form7); rec.Code != http.StatusCreated {
		t.Fatalf("create campaign (tenant 7) = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := postForm(t, h, "/api/tenants/9/campaigns", form9); rec.Code != http.StatusCreated {
		t.Fatalf("create campaign (tenant 9) = %d (%s)", rec.Code, rec.Body.String())
	}

	rec := do(t, h, http.MethodGet, "/api/tenants/7/campaigns")
	if rec.Code != http.StatusOK {
		t.Fatalf("list campaigns = %d (%s)", rec.Code, rec.Body.String())
	}
	var got []campaignRow
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].TenantID != 7 {
		t.Fatalf("list campaigns(tenant 7) = %+v, want exactly one tenant-7 campaign", got)
	}
}

func TestGetCampaign_RoundTrips(t *testing.T) {
	t.Parallel()
	h, _ := newCampaignRouter(t)
	projectA, execA := seedProjectAndExecution(t, h, "service-a", 7)

	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	create := postForm(t, h, "/api/tenants/7/campaigns", url.Values{
		"name": {"c"}, "window_start": {start}, "window_end": {end},
		"service_project_id": {itoa(projectA)}, "service_execution_id": {itoa(execA)},
	})
	id := decodeID(t, create)

	rec := do(t, h, http.MethodGet, "/api/campaigns/"+itoa(id))
	if rec.Code != http.StatusOK {
		t.Fatalf("get campaign = %d (%s)", rec.Code, rec.Body.String())
	}
	var got campaignRow
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != id {
		t.Fatalf("get campaign id = %d, want %d", got.ID, id)
	}
}

func TestGetCampaign_MissingReturnsNotFound(t *testing.T) {
	t.Parallel()
	h, _ := newCampaignRouter(t)
	rec := do(t, h, http.MethodGet, "/api/campaigns/999")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get missing campaign = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

// pathInt rejects a non-numeric id before touching h.deps.Campaigns at all.
func TestCampaignHandlers_InvalidID_400(t *testing.T) {
	t.Parallel()
	h, _ := newCampaignRouter(t)
	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/tenants/x/campaigns"},
		{http.MethodGet, "/api/tenants/x/campaigns"},
		{http.MethodGet, "/api/campaigns/x"},
		{http.MethodGet, "/api/campaigns/x/verdict"},
	}
	for _, tc := range cases {
		if rec := do(t, h, tc.method, tc.path); rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d, want 400", tc.method, tc.path, rec.Code)
		}
	}
}

func TestCampaignHandlers_CampaignsNotConfigured(t *testing.T) {
	t.Parallel()
	h := httpapi.NewRouter(httpapi.Deps{}) // no Campaigns wired
	rec := do(t, h, http.MethodGet, "/api/tenants/7/campaigns")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("list campaigns (not configured) = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/campaigns/1/verdict")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get verdict (not configured) = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

func TestGetCampaignVerdict_ReturnsPerServiceOutcomeAndOverallGo(t *testing.T) {
	t.Parallel()
	h, store := newCampaignRouter(t)
	ctx := context.Background()
	projectID, execID := seedProjectAndExecution(t, h, "service-a", 7)

	if err := store.SaveReport(ctx, report.Report{ExecutionID: execID, RunID: 1, Outcome: taurus.OutcomePassed}); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	start := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	create := postForm(t, h, "/api/tenants/7/campaigns", url.Values{
		"name": {"c"}, "window_start": {start}, "window_end": {end},
		"service_project_id": {itoa(projectID)}, "service_execution_id": {itoa(execID)},
	})
	id := decodeID(t, create)

	rec := do(t, h, http.MethodGet, "/api/campaigns/"+itoa(id)+"/verdict")
	if rec.Code != http.StatusOK {
		t.Fatalf("get verdict = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		CampaignID int64 `json:"campaign_id"`
		Go         bool  `json:"go"`
		Services   []struct {
			ExecutionID     int64  `json:"execution_id"`
			HasReport       bool   `json:"has_report"`
			Outcome         string `json:"outcome"`
			FailingCriteria []struct {
				Criterion string `json:"criterion"`
			} `json:"failing_criteria"`
		} `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.CampaignID != id || !got.Go {
		t.Fatalf("verdict = %+v, want campaign_id %d and go:true", got, id)
	}
	if len(got.Services) != 1 || !got.Services[0].HasReport || got.Services[0].Outcome != "passed" {
		t.Fatalf("verdict services = %+v", got.Services)
	}
	// failing_criteria is omitempty and correctly absent (decodes as nil)
	// for a passed service with nothing to name -- see the failing-service
	// case (verdict_test.go) for the populated shape.
	if len(got.Services[0].FailingCriteria) != 0 {
		t.Fatalf("failing_criteria = %+v, want none for a passed service", got.Services[0].FailingCriteria)
	}
}

// A service that passed its criteria but fell short of its target QPS flips
// the campaign to no-go, and the API response names the shortfall.
func TestGetCampaignVerdict_ShortOfTargetQPS_OverallNoGo(t *testing.T) {
	t.Parallel()
	h, store := newCampaignRouter(t)
	ctx := context.Background()
	projectID, execID := seedProjectAndExecution(t, h, "service-a", 7)

	if err := store.SaveReport(ctx, report.Report{
		ExecutionID: execID, RunID: 1, Outcome: taurus.OutcomePassed,
		Requested: report.Load{Throughput: 100}, Achieved: report.Load{Throughput: 60},
	}); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	start := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	create := postForm(t, h, "/api/tenants/7/campaigns", url.Values{
		"name": {"c"}, "window_start": {start}, "window_end": {end},
		"service_project_id": {itoa(projectID)}, "service_execution_id": {itoa(execID)},
	})
	id := decodeID(t, create)

	rec := do(t, h, http.MethodGet, "/api/campaigns/"+itoa(id)+"/verdict")
	if rec.Code != http.StatusOK {
		t.Fatalf("get verdict = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Go       bool `json:"go"`
		Services []struct {
			Outcome             string  `json:"outcome"`
			ShortOfTargetQPS    bool    `json:"short_of_target_qps"`
			RequestedThroughput float64 `json:"requested_throughput"`
			AchievedThroughput  float64 `json:"achieved_throughput"`
		} `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.Go {
		t.Fatalf("verdict.go = true, want false (short of target QPS): %+v", got)
	}
	if len(got.Services) != 1 || got.Services[0].Outcome != "passed" {
		t.Fatalf("verdict services = %+v, want the passed outcome preserved", got.Services)
	}
	sv := got.Services[0]
	if !sv.ShortOfTargetQPS || sv.RequestedThroughput != 100 || sv.AchievedThroughput != 60 {
		t.Fatalf("service verdict = %+v, want short_of_target_qps naming requested 100 / achieved 60", sv)
	}
}

// The comparison endpoint resolves the tenant's most-recent-prior ended
// campaign as the default baseline and classifies the shared project.
func TestGetCampaignComparison_DefaultBaseline(t *testing.T) {
	t.Parallel()
	h, store := newCampaignRouter(t)
	ctx := context.Background()
	projectA, execOlder := seedProjectAndExecution(t, h, "service-a", 7)

	if err := store.SaveReport(ctx, report.Report{ExecutionID: execOlder, RunID: 1, Outcome: taurus.OutcomeFailed}); err != nil {
		t.Fatalf("SaveReport (older): %v", err)
	}
	olderStart := time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	olderEnd := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	olderID := decodeID(t, postForm(t, h, "/api/tenants/7/campaigns", url.Values{
		"name": {"older"}, "window_start": {olderStart}, "window_end": {olderEnd},
		"service_project_id": {itoa(projectA)}, "service_execution_id": {itoa(execOlder)},
	}))

	execNewer := decodeID(t, postForm(t, h, "/api/executions", url.Values{"name": {"rerun"}, "project_id": {itoa(projectA)}}))
	if err := store.SaveReport(ctx, report.Report{ExecutionID: execNewer, RunID: 2, Outcome: taurus.OutcomePassed}); err != nil {
		t.Fatalf("SaveReport (newer): %v", err)
	}
	targetStart := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	targetEnd := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	targetID := decodeID(t, postForm(t, h, "/api/tenants/7/campaigns", url.Values{
		"name": {"target"}, "window_start": {targetStart}, "window_end": {targetEnd},
		"service_project_id": {itoa(projectA)}, "service_execution_id": {itoa(execNewer)},
	}))

	rec := do(t, h, http.MethodGet, "/api/campaigns/"+itoa(targetID)+"/comparison")
	if rec.Code != http.StatusOK {
		t.Fatalf("get comparison = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		CampaignID         int64 `json:"campaign_id"`
		HasBaseline        bool  `json:"has_baseline"`
		BaselineCampaignID int64 `json:"baseline_campaign_id"`
		Services           []struct {
			ProjectID int64  `json:"project_id"`
			Status    string `json:"status"`
		} `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if !got.HasBaseline || got.BaselineCampaignID != olderID {
		t.Fatalf("comparison = %+v, want the older campaign (%d) as the default baseline", got, olderID)
	}
	if len(got.Services) != 1 || got.Services[0].ProjectID != projectA || got.Services[0].Status != "improved" {
		t.Fatalf("comparison services = %+v, want project %d classified improved", got.Services, projectA)
	}
}

// ?baseline=<id> overrides the default baseline resolution.
func TestGetCampaignComparison_ExplicitBaselineOverride(t *testing.T) {
	t.Parallel()
	h, store := newCampaignRouter(t)
	ctx := context.Background()
	projectA, execA := seedProjectAndExecution(t, h, "service-a", 7)
	if err := store.SaveReport(ctx, report.Report{ExecutionID: execA, RunID: 1, Outcome: taurus.OutcomePassed}); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	start := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	explicitID := decodeID(t, postForm(t, h, "/api/tenants/7/campaigns", url.Values{
		"name": {"explicit-baseline"}, "window_start": {start}, "window_end": {end},
		"service_project_id": {itoa(projectA)}, "service_execution_id": {itoa(execA)},
	}))
	targetID := decodeID(t, postForm(t, h, "/api/tenants/7/campaigns", url.Values{
		"name": {"target"}, "window_start": {start}, "window_end": {end},
		"service_project_id": {itoa(projectA)}, "service_execution_id": {itoa(execA)},
	}))

	rec := do(t, h, http.MethodGet, "/api/campaigns/"+itoa(targetID)+"/comparison?baseline="+itoa(explicitID))
	if rec.Code != http.StatusOK {
		t.Fatalf("get comparison = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		BaselineCampaignID int64 `json:"baseline_campaign_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.BaselineCampaignID != explicitID {
		t.Fatalf("baseline_campaign_id = %d, want the explicit override %d", got.BaselineCampaignID, explicitID)
	}
}

// A campaign with no prior campaign in its tenant returns an explanatory
// empty comparison: has_baseline false, no services, no error.
func TestGetCampaignComparison_NoPriorCampaign_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	h, store := newCampaignRouter(t)
	ctx := context.Background()
	projectA, execA := seedProjectAndExecution(t, h, "service-a", 7)
	if err := store.SaveReport(ctx, report.Report{ExecutionID: execA, RunID: 1, Outcome: taurus.OutcomePassed}); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	start := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	targetID := decodeID(t, postForm(t, h, "/api/tenants/7/campaigns", url.Values{
		"name": {"only"}, "window_start": {start}, "window_end": {end},
		"service_project_id": {itoa(projectA)}, "service_execution_id": {itoa(execA)},
	}))

	rec := do(t, h, http.MethodGet, "/api/campaigns/"+itoa(targetID)+"/comparison")
	if rec.Code != http.StatusOK {
		t.Fatalf("get comparison = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		HasBaseline bool          `json:"has_baseline"`
		Services    []interface{} `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.HasBaseline {
		t.Fatalf("comparison = %+v, want has_baseline false (no prior campaign)", got)
	}
	if len(got.Services) != 0 {
		t.Fatalf("comparison services = %+v, want empty", got.Services)
	}
}

func TestGetCampaignComparison_MissingCampaignReturnsNotFound(t *testing.T) {
	t.Parallel()
	h, _ := newCampaignRouter(t)
	rec := do(t, h, http.MethodGet, "/api/campaigns/999/comparison")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get comparison (missing) = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

func TestGetCampaignComparison_InvalidBaselineQueryParam(t *testing.T) {
	t.Parallel()
	h, store := newCampaignRouter(t)
	ctx := context.Background()
	projectA, execA := seedProjectAndExecution(t, h, "service-a", 7)
	if err := store.SaveReport(ctx, report.Report{ExecutionID: execA, RunID: 1, Outcome: taurus.OutcomePassed}); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	start := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	targetID := decodeID(t, postForm(t, h, "/api/tenants/7/campaigns", url.Values{
		"name": {"c"}, "window_start": {start}, "window_end": {end},
		"service_project_id": {itoa(projectA)}, "service_execution_id": {itoa(execA)},
	}))

	rec := do(t, h, http.MethodGet, "/api/campaigns/"+itoa(targetID)+"/comparison?baseline=not-a-number")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("get comparison (invalid baseline) = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestGetCampaignVerdict_FailedServiceNamesFailingCriteria(t *testing.T) {
	t.Parallel()
	h, store := newCampaignRouter(t)
	ctx := context.Background()
	projectID, execID := seedProjectAndExecution(t, h, "service-a", 7)

	if err := store.SetExecutionCriteria(ctx, execID, []string{"failures>10%"}); err != nil {
		t.Fatalf("SetExecutionCriteria: %v", err)
	}
	if err := store.SaveReport(ctx, report.Report{
		ExecutionID: execID, RunID: 1, Outcome: taurus.OutcomeFailed, ErrorRate: 0.20,
	}); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	start := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	create := postForm(t, h, "/api/tenants/7/campaigns", url.Values{
		"name": {"c"}, "window_start": {start}, "window_end": {end},
		"service_project_id": {itoa(projectID)}, "service_execution_id": {itoa(execID)},
	})
	id := decodeID(t, create)

	rec := do(t, h, http.MethodGet, "/api/campaigns/"+itoa(id)+"/verdict")
	if rec.Code != http.StatusOK {
		t.Fatalf("get verdict = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Go       bool `json:"go"`
		Services []struct {
			Outcome         string `json:"outcome"`
			FailingCriteria []struct {
				Criterion string `json:"criterion"`
			} `json:"failing_criteria"`
		} `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.Go {
		t.Fatal("verdict.go = true, want false -- the service failed")
	}
	if len(got.Services) != 1 || got.Services[0].Outcome != "failed" {
		t.Fatalf("verdict services = %+v", got.Services)
	}
	if len(got.Services[0].FailingCriteria) != 1 || got.Services[0].FailingCriteria[0].Criterion != "failures>10%" {
		t.Fatalf("failing_criteria = %+v, want [failures>10%%]", got.Services[0].FailingCriteria)
	}
}

func TestGetCampaignVerdict_ReportsOtherLoadExcludingOwnService(t *testing.T) {
	t.Parallel()
	h, store := newCampaignRouter(t)
	ctx := context.Background()
	projectID, execID := seedProjectAndExecution(t, h, "service-a", 7)
	_, otherExecID := seedProjectAndExecution(t, h, "other", 7)

	if err := store.SaveReport(ctx, report.Report{ExecutionID: execID, RunID: 1, Outcome: taurus.OutcomePassed}); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	windowStart := time.Now().Add(-time.Hour).UTC()
	windowEnd := time.Now().Add(time.Hour).UTC()
	create := postForm(t, h, "/api/tenants/7/campaigns", url.Values{
		"name": {"c"}, "window_start": {windowStart.Format(time.RFC3339)}, "window_end": {windowEnd.Format(time.RFC3339)},
		"service_project_id": {itoa(projectID)}, "service_execution_id": {itoa(execID)},
	})
	id := decodeID(t, create)

	// The designated execution's own reservation must not appear as "other"
	// load; a different execution's overlapping reservation should.
	if _, err := store.CreateReservation(ctx, reservation.Reservation{
		TenantID: 7, EngineCount: 1, Start: windowStart, End: windowEnd, ExecutionID: execID,
	}); err != nil {
		t.Fatalf("CreateReservation designated: %v", err)
	}
	if _, err := store.CreateReservation(ctx, reservation.Reservation{
		TenantID: 7, EngineCount: 4, Start: windowStart, End: windowEnd, ExecutionID: otherExecID,
	}); err != nil {
		t.Fatalf("CreateReservation other: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/campaigns/"+itoa(id)+"/verdict")
	if rec.Code != http.StatusOK {
		t.Fatalf("get verdict = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		OtherLoad []struct {
			ExecutionID int64 `json:"execution_id"`
			EngineCount int   `json:"engine_count"`
		} `json:"other_load"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(got.OtherLoad) != 1 || got.OtherLoad[0].ExecutionID != otherExecID || got.OtherLoad[0].EngineCount != 4 {
		t.Fatalf("other_load = %+v, want exactly one entry for execution %d with engine_count 4", got.OtherLoad, otherExecID)
	}
}

func TestGetCampaignVerdict_MissingCampaignReturnsNotFound(t *testing.T) {
	t.Parallel()
	h, _ := newCampaignRouter(t)
	rec := do(t, h, http.MethodGet, "/api/campaigns/999/verdict")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get verdict (missing) = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}
func createCampaignForUpdate(t *testing.T, h http.Handler) (id, projectA, execA int64) {
	t.Helper()
	projectA, execA = seedProjectAndExecution(t, h, "upd-a", 7)
	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	rec := postForm(t, h, "/api/tenants/7/campaigns", url.Values{
		"name":                 {"Update Me"},
		"window_start":         {start},
		"window_end":           {end},
		"service_project_id":   {itoa(projectA)},
		"service_execution_id": {itoa(execA)},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create campaign = %d (%s)", rec.Code, rec.Body.String())
	}
	return decodeID(t, rec), projectA, execA
}

func TestUpdateCampaign_RoundTrips(t *testing.T) {
	t.Parallel()
	h, _ := newCampaignRouter(t)
	id, projectA, execA := createCampaignForUpdate(t, h)

	start := time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(4 * time.Hour).UTC().Format(time.RFC3339)
	rec := putForm(t, h, "/api/campaigns/"+itoa(id), url.Values{
		"name":                 {"Updated Name"},
		"window_start":         {start},
		"window_end":           {end},
		"service_project_id":   {itoa(projectA)},
		"service_execution_id": {itoa(execA)},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update campaign = %d (%s)", rec.Code, rec.Body.String())
	}
	var got campaignRow
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.ID != id {
		t.Errorf("update returned id %d, want %d", got.ID, id)
	}
}

func TestUpdateCampaign_RejectsInvalidWindow(t *testing.T) {
	t.Parallel()
	h, _ := newCampaignRouter(t)
	id, _, _ := createCampaignForUpdate(t, h)

	rec := putForm(t, h, "/api/campaigns/"+itoa(id), url.Values{
		"name":                 {"x"},
		"window_start":         {"not-a-time"},
		"window_end":           {"not-a-time"},
		"service_project_id":   {},
		"service_execution_id": {},
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid window_start = %d (%s), want 400", rec.Code, rec.Body.String())
	}

	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	rec = putForm(t, h, "/api/campaigns/"+itoa(id), url.Values{
		"name":                 {"x"},
		"window_start":         {start},
		"window_end":           {"not-a-time"},
		"service_project_id":   {},
		"service_execution_id": {},
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid window_end = %d (%s), want 400", rec.Code, rec.Body.String())
	}
}

func TestUpdateCampaign_MissingReturns404(t *testing.T) {
	t.Parallel()
	h, _ := newCampaignRouter(t)
	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	rec := putForm(t, h, "/api/campaigns/9999", url.Values{
		"name":                 {"ghost"},
		"window_start":         {start},
		"window_end":           {end},
		"service_project_id":   {},
		"service_execution_id": {},
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("update missing = %d (%s), want 404", rec.Code, rec.Body.String())
	}
}

func TestAbortCampaign_ClosesIt(t *testing.T) {
	t.Parallel()
	h, _ := newCampaignRouter(t)
	id, _, _ := createCampaignForUpdate(t, h)

	rec := do(t, h, http.MethodPost, "/api/campaigns/"+itoa(id)+"/abort")
	if rec.Code != http.StatusOK {
		t.Fatalf("abort = %d (%s)", rec.Code, rec.Body.String())
	}
	var got campaignRow
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.Active {
		t.Error("aborted campaign must not be active")
	}

	// Aborting again still returns the campaign (idempotent read-back).
	rec = do(t, h, http.MethodPost, "/api/campaigns/"+itoa(id)+"/abort")
	if rec.Code != http.StatusOK {
		t.Errorf("second abort = %d (%s), want 200", rec.Code, rec.Body.String())
	}
}

func TestAbortCampaign_MissingReturns404(t *testing.T) {
	t.Parallel()
	h, _ := newCampaignRouter(t)
	rec := do(t, h, http.MethodPost, "/api/campaigns/9999/abort")
	if rec.Code != http.StatusNotFound {
		t.Errorf("abort missing = %d (%s), want 404", rec.Code, rec.Body.String())
	}
}
