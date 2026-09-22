//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	membus "github.com/heridotlife/honryu/internal/adapters/eventbus/memory"
	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
	"github.com/heridotlife/honryu/internal/adapters/storage/local"
	"github.com/heridotlife/honryu/internal/app/adminapp"
	"github.com/heridotlife/honryu/internal/app/campaignapp"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/app/metricsapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/domain/campaign"
	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/ports/fake"
	"github.com/heridotlife/honryu/test/dbtest"
)

// TestPhase6_CampaignFreezeVerdictEndToEnd drives the full campaign happy
// path in one integrated proof: freeze rejects a participating project's own
// non-designated execution while the window is open, but exempts the
// designated one; an execution already running before the window opened is
// stopped by the drain sweep rather than left alone; the rolled-up verdict
// names a failing service's triggered criteria; and the kill-switch's
// ScopeCampaign tears down every deployed participant and closes the
// campaign.
func TestPhase6_CampaignFreezeVerdictEndToEnd(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	store := local.New(t.TempDir(), "")
	sched := fake.NewScheduler()
	sink := fake.NewMetricsSink()
	bus := membus.New()

	collector := metricsapp.NewService(repo, sink, bus, repo, repo)
	campaigns := campaignapp.NewService(repo, sched)
	lifecycle := lifecycleapp.NewService(repo, sched, store, lifecycleapp.StaticImage("jmeter")).WithMetrics(collector).WithFreeze(campaigns)
	admin := adminapp.NewService(repo, sched, lifecycle).WithCampaigns(campaigns)

	router := httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(repo),
		Scenarios:     scenarioapp.NewService(repo, store),
		Executions:    executionapp.NewService(repo, store, 500),
		Lifecycle:     lifecycle,
		Campaigns:     campaigns,
		Admin:         admin,
		Events:        bus,
		Store:         store,
		Metrics:       collector,
		Reports:       repo,
		IngestToken:   "engine-token",
		DefaultOwners: []string{"honryu"},
	})
	srv := httptest.NewServer(router)
	defer srv.Close()
	client := srv.Client()
	ctx := context.Background()

	// --- service A: a participating project with two executions -- the
	// campaign's designated readiness test, and a second, non-designated
	// execution standing in for "the team's own dedicated test", which the
	// spec's decision #3 says freeze must reject just as much as anyone
	// else's.
	projectA := postForm(t, client, srv.URL+"/api/projects", url.Values{"name": {"service-a"}, "owner": {"honryu"}, "tenant_id": {"9"}})
	scenarioA := postForm(t, client, srv.URL+"/api/scenarios", url.Values{"name": {"checkout"}, "project_id": {itoa(projectA)}})
	putMultipart(t, client, srv.URL+"/api/scenarios/"+itoa(scenarioA)+"/files", "scenario.jmx", "<jmx/>")

	execDesignatedA := postForm(t, client, srv.URL+"/api/executions", url.Values{"name": {"readiness"}, "project_id": {itoa(projectA)}})
	putMultipart(t, client, srv.URL+"/api/executions/"+itoa(execDesignatedA)+"/config", "config.yaml", minimalConfig(execDesignatedA, scenarioA, ""))

	execOtherA := postForm(t, client, srv.URL+"/api/executions", url.Values{"name": {"team-own-test"}, "project_id": {itoa(projectA)}})
	putMultipart(t, client, srv.URL+"/api/executions/"+itoa(execOtherA)+"/config", "config.yaml", minimalConfig(execOtherA, scenarioA, ""))

	// execOtherA starts running before the campaign exists at all, so
	// nothing blocks it yet -- this is what the drain sweep exists for.
	otherBase := srv.URL + "/api/executions/" + itoa(execOtherA)
	postAction(t, client, otherBase+"/deploy", http.StatusOK)
	postAction(t, client, otherBase+"/trigger", http.StatusOK)
	if _, running, err := repo.CurrentRun(ctx, execOtherA); err != nil || !running {
		t.Fatalf("execOtherA CurrentRun before campaign = running:%v, err:%v, want running", running, err)
	}

	// --- service B: a second participating project whose designated
	// execution is configured to fail its own criteria.
	projectB := postForm(t, client, srv.URL+"/api/projects", url.Values{"name": {"service-b"}, "owner": {"honryu"}, "tenant_id": {"9"}})
	scenarioB := postForm(t, client, srv.URL+"/api/scenarios", url.Values{"name": {"checkout"}, "project_id": {itoa(projectB)}})
	putMultipart(t, client, srv.URL+"/api/scenarios/"+itoa(scenarioB)+"/files", "scenario.jmx", "<jmx/>")

	execDesignatedB := postForm(t, client, srv.URL+"/api/executions", url.Values{"name": {"readiness"}, "project_id": {itoa(projectB)}})
	putMultipart(t, client, srv.URL+"/api/executions/"+itoa(execDesignatedB)+"/config", "config.yaml",
		minimalConfig(execDesignatedB, scenarioB, `  criteria:
    - "failures>10%"
`))

	// --- open the campaign's window now, naming both designated executions.
	windowStart := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	windowEnd := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	campaignID := postForm(t, client, srv.URL+"/api/tenants/9/campaigns", url.Values{
		"name":                 {"Launch Readiness"},
		"window_start":         {windowStart},
		"window_end":           {windowEnd},
		"service_project_id":   {itoa(projectA), itoa(projectB)},
		"service_execution_id": {itoa(execDesignatedA), itoa(execDesignatedB)},
	})

	// Each designated execution triggers normally despite the freeze -- the
	// exemption the spec calls out by name.
	designatedABase := srv.URL + "/api/executions/" + itoa(execDesignatedA)
	postAction(t, client, designatedABase+"/deploy", http.StatusOK)
	postAction(t, client, designatedABase+"/trigger", http.StatusOK)
	runA, running, err := repo.CurrentRun(ctx, execDesignatedA)
	if err != nil || !running {
		t.Fatalf("execDesignatedA CurrentRun after trigger: %d, %v, %v", runA, running, err)
	}

	designatedBBase := srv.URL + "/api/executions/" + itoa(execDesignatedB)
	postAction(t, client, designatedBBase+"/deploy", http.StatusOK)
	postAction(t, client, designatedBBase+"/trigger", http.StatusOK)
	runB, running, err := repo.CurrentRun(ctx, execDesignatedB)
	if err != nil || !running {
		t.Fatalf("execDesignatedB CurrentRun after trigger: %d, %v, %v", runB, running, err)
	}

	// --- the drain sweep: everything cmd/scheduler's own drain loop does,
	// via the same exported calls (drainOnce lives in package main and
	// cannot be imported), one tick.
	active, err := campaigns.ActiveCampaigns(ctx)
	if err != nil {
		t.Fatalf("ActiveCampaigns: %v", err)
	}
	// The marketplace seed (0084) pre-creates a 'Supersale Readiness'
	// campaign with a 15-day window, so a fresh DB is never campaign-free.
	// Filter to the campaign this test opened by name.
	var ours *campaign.Campaign
	for i := range active {
		if active[i].Name == "Launch Readiness" {
			ours = &active[i]
		}
	}
	if ours == nil {
		t.Fatalf("ActiveCampaigns = %+v, want our Launch Readiness campaign", active)
	}
	active = active[:0]
	active = append(active, *ours)
	inScope, err := campaigns.InScopeExecutions(ctx, active[0].ID)
	if err != nil {
		t.Fatalf("InScopeExecutions: %v", err)
	}
	if len(inScope) != 1 || inScope[0] != execOtherA {
		t.Fatalf("InScopeExecutions = %+v, want exactly [%d]", inScope, execOtherA)
	}
	for _, executionID := range inScope {
		if _, running, err := repo.CurrentRun(ctx, executionID); err != nil {
			t.Fatalf("CurrentRun(%d) before drain: %v", executionID, err)
		} else if running {
			if err := lifecycle.Stop(ctx, executionID); err != nil {
				t.Fatalf("Stop(%d): %v", executionID, err)
			}
		}
	}
	if _, running, err := repo.CurrentRun(ctx, execOtherA); err != nil || running {
		t.Fatalf("execOtherA CurrentRun after drain = running:%v, err:%v, want stopped", running, err)
	}

	// Now that it's stopped, a fresh attempt to trigger execOtherA is
	// rejected on the freeze check alone -- the window is still open, and
	// it is still not the project's designated execution.
	postAction(t, client, otherBase+"/trigger", http.StatusConflict)

	// --- feed each designated execution's run its outcome: A passes
	// cleanly, B fails with a 30% error rate against its own configured
	// "failures>10%" criterion.
	ingestFinal(t, client, srv.URL, execDesignatedA, scenarioA, runA, 0, 10, 0, nil)
	ingestFinal(t, client, srv.URL, execDesignatedB, scenarioB, runB, 3, 7, 3, []metrics.ErrorGroup{
		{Message: "Request to http://target/cart didn't succeed (500)", ResponseCode: "500", Count: 3},
	})

	// --- the rolled-up verdict names exactly what failed and why.
	var verdict struct {
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
	getJSON(t, client, srv.URL+"/api/campaigns/"+itoa(campaignID)+"/verdict", http.StatusOK, &verdict)
	if verdict.Go {
		t.Fatalf("verdict.go = true, want false -- service B failed: %+v", verdict)
	}
	if len(verdict.Services) != 2 {
		t.Fatalf("verdict services = %+v, want 2", verdict.Services)
	}
	for _, sv := range verdict.Services {
		switch sv.ExecutionID {
		case execDesignatedA:
			if !sv.HasReport || sv.Outcome != "passed" || len(sv.FailingCriteria) != 0 {
				t.Fatalf("service A verdict = %+v, want passed with no failing criteria", sv)
			}
		case execDesignatedB:
			if !sv.HasReport || sv.Outcome != "failed" || len(sv.FailingCriteria) != 1 || sv.FailingCriteria[0].Criterion != "failures>10%" {
				t.Fatalf("service B verdict = %+v, want failed naming failures>10%%", sv)
			}
		default:
			t.Fatalf("unexpected service in verdict: %+v", sv)
		}
	}

	// --- the kill-switch's ScopeCampaign tears down every deployed
	// participant and closes the campaign. That set is three executions, not
	// two: both designated executions (still deployed -- neither was
	// purged), plus execOtherA -- Stop (unlike Purge) only ends its run, it
	// does not remove its pods, so it is still "deployed" and still in scope.
	abortRec, err := client.PostForm(srv.URL+"/api/admin/abort", url.Values{
		"scope": {"campaign"}, "value": {itoa(campaignID)},
	})
	if err != nil {
		t.Fatalf("POST /api/admin/abort: %v", err)
	}
	if abortRec.StatusCode != http.StatusOK {
		t.Fatalf("kill-switch abort = %d", abortRec.StatusCode)
	}
	var abortResp struct {
		Aborted []int64 `json:"aborted"`
	}
	if err := json.NewDecoder(abortRec.Body).Decode(&abortResp); err != nil {
		t.Fatalf("decode abort response: %v", err)
	}
	_ = abortRec.Body.Close()
	wantAborted := []int64{execDesignatedA, execOtherA, execDesignatedB}
	if len(abortResp.Aborted) != len(wantAborted) {
		t.Fatalf("kill-switch aborted = %+v, want %+v", abortResp.Aborted, wantAborted)
	}
	for i, id := range wantAborted {
		if abortResp.Aborted[i] != id {
			t.Fatalf("kill-switch aborted = %+v, want %+v", abortResp.Aborted, wantAborted)
		}
	}

	var campaignAfter struct {
		Active    bool    `json:"active"`
		AbortedAt *string `json:"aborted_at"`
	}
	getJSON(t, client, srv.URL+"/api/campaigns/"+itoa(campaignID), http.StatusOK, &campaignAfter)
	if campaignAfter.Active || campaignAfter.AbortedAt == nil {
		t.Fatalf("campaign after kill-switch abort = %+v, want active:false and aborted_at set", campaignAfter)
	}
}

// minimalConfig builds a single-shard, single-engine multi-test profile,
// optionally with an extra top-level field (e.g. "  criteria:\n    - ...\n")
// spliced in right after collectionid.
func minimalConfig(executionID, scenarioID int64, extra string) string {
	return fmt.Sprintf("multi-test:\n  collectionid: %d\n%s  tests:\n    - testid: %d\n      concurrency: 1\n      rampup: 1\n      engines: 1\n      duration: 5\n",
		executionID, extra, scenarioID)
}

// ingestFinal posts one Final shard, mirroring TestPhase4_DiagnosisEndToEnd's
// pattern: exitCode decides the run's outcome (0 passed, 3 criteria-failed),
// independent of whatever error mix the interval itself reports.
func ingestFinal(t *testing.T, client *http.Client, baseURL string, executionID, scenarioID, runID int64, exitCode int, succeeded, failed int64, errors []metrics.ErrorGroup) {
	t.Helper()
	body, _ := json.Marshal(metrics.Batch{
		ExecutionID: executionID, ScenarioID: scenarioID, RunID: runID,
		ShardIndex: 0, StreamID: "s0", Final: true, ExitCode: &exitCode,
		Intervals: []metrics.Interval{{
			Seq: 1, Timestamp: 1000, Label: "checkout-cart",
			Concurrency: 1, Samples: succeeded + failed, Succeeded: succeeded, Failed: failed,
			Latency: metrics.Histogram{0.01: succeeded},
			Errors:  errors,
		}},
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/ingest", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build ingest request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer engine-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("ingest execution %d: %v", executionID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest execution %d = %d", executionID, resp.StatusCode)
	}
}
