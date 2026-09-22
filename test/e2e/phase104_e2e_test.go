//go:build e2e

// Phase 104's full-stack pins, one per fix:
//
//   - Quota defaults: a tenant with NO quota row triggers out of the box
//     (the live marketplace finding: every trigger was refused with
//     ceiling=0 until an admin PUT a quota).
//   - Startup-safe criteria: an execution configured with a floor
//     assertion ("p95<800ms") compiles to its violation condition in the
//     shard configs bzt runs, and a clean flat run then finishes with
//     outcome=passed and a served report that does not list the assertion
//     among the failed criteria.
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

	"gopkg.in/yaml.v3"

	membus "github.com/heridotlife/honryu/internal/adapters/eventbus/memory"
	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
	"github.com/heridotlife/honryu/internal/adapters/storage/local"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/app/metricsapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/quotaapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/app/tenantapp"
	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports/fake"
	"github.com/heridotlife/honryu/test/dbtest"
)

// phase104Env is the phase 7 stack plus the two dependencies the quota
// finding needs wired the way cmd/api wires them: the tenant service (so a
// tenant can be created over the API) and the quota service on lifecycle
// (so Trigger admits through the reservation ledger).
type phase104Env struct {
	client *http.Client
	srvURL string
	repo   *mysqladapter.Repository
	sched  *fake.Scheduler
}

func setupPhase104(t *testing.T) *phase104Env {
	t.Helper()
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	obj := local.New(t.TempDir(), "")
	sched := fake.NewScheduler()
	sink := fake.NewMetricsSink()
	bus := membus.New()

	collector := metricsapp.NewService(repo, sink, bus, repo, repo)
	quota := quotaapp.NewService(repo)
	lifecycle := lifecycleapp.NewService(repo, sched, obj, lifecycleapp.StaticImage("jmeter")).WithMetrics(collector).WithQuota(quota)

	router := httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(repo),
		Scenarios:     scenarioapp.NewService(repo, obj),
		Executions:    executionapp.NewService(repo, obj, 500),
		Tenants:       tenantapp.NewService(repo, repo, repo),
		Lifecycle:     lifecycle,
		Store:         obj,
		Metrics:       collector,
		Reports:       repo,
		IngestToken:   "engine-token",
		DefaultOwners: []string{"honryu"},
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return &phase104Env{client: srv.Client(), srvURL: srv.URL, repo: repo, sched: sched}
}

// phase104Run drives one execution end to end: tenant + project + scenario
// + config (with criteria), deploy, trigger, then every shard streams a
// clean flat window (fast samples, all succeeded) and closes with the given
// exit code. Returns the settled report read back from the store.
func phase104Run(t *testing.T, e *phase104Env, name, criterion string, exitCode int) report.Report {
	t.Helper()
	ctx := context.Background()

	// The finding's exact shape: a tenant with no quota row anywhere.
	tenantID := postForm(t, e.client, e.srvURL+"/api/tenants", url.Values{"name": {name + "-tenant"}, "display_name": {name}})
	projectID := postForm(t, e.client, e.srvURL+"/api/projects", url.Values{"name": {name}, "owner": {"honryu"}, "tenant_id": {itoa(tenantID)}})
	scenarioID := postForm(t, e.client, e.srvURL+"/api/scenarios", url.Values{"name": {"target"}, "project_id": {itoa(projectID)}})
	putMultipart(t, e.client, e.srvURL+"/api/scenarios/"+itoa(scenarioID)+"/files", "scenario.jmx", "<jmx/>")

	execID := postForm(t, e.client, e.srvURL+"/api/executions", url.Values{"name": {name}, "project_id": {itoa(projectID)}})
	cfg := fmt.Sprintf("multi-test:\n  collectionid: %d\n  criteria:\n    - %q\n  tests:\n    - testid: %d\n      concurrency: 2\n      rampup: 1\n      engines: 1\n      duration: 10\n",
		execID, criterion, scenarioID)
	putMultipart(t, e.client, e.srvURL+"/api/executions/"+itoa(execID)+"/config", "config.yaml", cfg)

	base := e.srvURL + "/api/executions/" + itoa(execID)
	postAction(t, e.client, base+"/deploy", http.StatusOK)
	postAction(t, e.client, base+"/trigger", http.StatusOK)
	runID, running, err := e.repo.CurrentRun(ctx, execID)
	if err != nil || !running {
		t.Fatalf("CurrentRun after trigger (no quota row exists): running=%v err=%v", running, err)
	}

	spec, ok := e.sched.LastDeploy(execID, scenarioID)
	if !ok {
		t.Fatalf("no deploy recorded for execution %d / scenario %d", execID, scenarioID)
	}
	for _, sh := range spec.Shards {
		phase104Ingest(t, e, execID, scenarioID, runID, sh.Index, exitCode)
	}

	var rep report.Report
	var pollErr error
	for i := 0; i < 25; i++ {
		rep, pollErr = e.repo.GetReport(ctx, runID)
		if pollErr == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if pollErr != nil {
		t.Fatalf("GetReport: %v", pollErr)
	}
	return rep
}

// phase104Ingest streams one shard's clean flat window -- ten seconds, five
// fast all-succeeded samples each -- and closes the shard with exitCode.
func phase104Ingest(t *testing.T, e *phase104Env, executionID, scenarioID, runID int64, shard, exitCode int) {
	t.Helper()
	base := time.Now().Add(-30 * time.Second).Unix()
	intervals := make([]metrics.Interval, 0, 10)
	for i := range 10 {
		intervals = append(intervals, metrics.Interval{
			Seq: int64(i + 1), Timestamp: base + int64(i), Label: "checkout",
			Concurrency: 2, Samples: 5, Succeeded: 5,
			Latency: metrics.Histogram{0.005: 5},
		})
	}
	exit := exitCode
	batches := []metrics.Batch{
		{ExecutionID: executionID, ScenarioID: scenarioID, RunID: runID, ShardIndex: shard, StreamID: "s0", Intervals: intervals},
		{ExecutionID: executionID, ScenarioID: scenarioID, RunID: runID, ShardIndex: shard, StreamID: "s0", Final: true, ExitCode: &exit},
	}
	for _, b := range batches {
		body, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal batch: %v", err)
		}
		req, err := http.NewRequest(http.MethodPost, e.srvURL+"/api/ingest", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("build ingest request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer engine-token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := e.client.Do(req)
		if err != nil {
			t.Fatalf("ingest (shard %d): %v", shard, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("ingest (shard %d) = %d, want 202", shard, resp.StatusCode)
		}
	}
}

// A tenant without a quota row triggers out of the box: the read path
// substitutes the platform default ceiling, so the reservation admits
// instead of refusing with ceiling=0 the way the marketplace seed's first
// execution did.
func TestPhase104_TenantWithoutQuotaRowTriggers(t *testing.T) {
	e := setupPhase104(t)
	rep := phase104Run(t, e, "no-row", "p95<800ms", 0)
	if rep.Outcome != taurus.OutcomePassed {
		t.Fatalf("outcome = %q, want passed -- a rowless tenant must trigger and a clean run must pass", rep.Outcome)
	}
}

// The compiled shard config carries the assertion's violation condition
// (p95>=800ms), not the raw floor phrasing -- bzt's passfail grammar reads
// criteria as failure conditions, so a literal p95<800ms would trip on every
// fast target. And the served report's verdict layer does not list the
// unviolated assertion among failed criteria.
func TestPhase104_FloorCriteriaCompileToViolationAndCleanRunPasses(t *testing.T) {
	e := setupPhase104(t)

	tenantID := postForm(t, e.client, e.srvURL+"/api/tenants", url.Values{"name": {"floor-tenant"}, "display_name": {"Floor"}})
	projectID := postForm(t, e.client, e.srvURL+"/api/projects", url.Values{"name": {"floor"}, "owner": {"honryu"}, "tenant_id": {itoa(tenantID)}})
	scenarioID := postForm(t, e.client, e.srvURL+"/api/scenarios", url.Values{"name": {"target"}, "project_id": {itoa(projectID)}})
	putMultipart(t, e.client, e.srvURL+"/api/scenarios/"+itoa(scenarioID)+"/files", "scenario.jmx", "<jmx/>")
	execID := postForm(t, e.client, e.srvURL+"/api/executions", url.Values{"name": {"floor"}, "project_id": {itoa(projectID)}})
	cfg := fmt.Sprintf("multi-test:\n  collectionid: %d\n  criteria:\n    - %q\n  tests:\n    - testid: %d\n      concurrency: 2\n      rampup: 1\n      engines: 1\n      duration: 10\n",
		execID, "p95<800ms", scenarioID)
	putMultipart(t, e.client, e.srvURL+"/api/executions/"+itoa(execID)+"/config", "config.yaml", cfg)

	base := e.srvURL + "/api/executions/" + itoa(execID)
	postAction(t, e.client, base+"/deploy", http.StatusOK)
	postAction(t, e.client, base+"/trigger", http.StatusOK)
	ctx := context.Background()
	runID, running, err := e.repo.CurrentRun(ctx, execID)
	if err != nil || !running {
		t.Fatalf("CurrentRun after trigger: running=%v err=%v", running, err)
	}

	spec, ok := e.sched.LastDeploy(execID, scenarioID)
	if !ok {
		t.Fatalf("no deploy recorded for execution %d / scenario %d", execID, scenarioID)
	}
	for _, sh := range spec.Shards {
		var cfg taurus.Config
		if err := yaml.Unmarshal(sh.Config, &cfg); err != nil {
			t.Fatalf("shard %d config is not a Taurus config: %v\n%s", sh.Index, err, sh.Config)
		}
		var passfail *taurus.Reporter
		for i := range cfg.Reporting {
			if cfg.Reporting[i].Module == "passfail" {
				passfail = &cfg.Reporting[i]
			}
		}
		if passfail == nil {
			t.Fatalf("shard %d config has no passfail module despite configured criteria:\n%s", sh.Index, sh.Config)
		}
		want := []string{"p95>=800ms"}
		if len(passfail.Criteria) != 1 || passfail.Criteria[0] != want[0] {
			t.Fatalf("shard %d passfail criteria = %v, want the violation form %v", sh.Index, passfail.Criteria, want)
		}
		phase104Ingest(t, e, execID, scenarioID, runID, sh.Index, 0)
	}

	var rep report.Report
	var pollErr error
	for i := 0; i < 25; i++ {
		rep, pollErr = e.repo.GetReport(ctx, runID)
		if pollErr == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if pollErr != nil {
		t.Fatalf("GetReport: %v", pollErr)
	}
	if rep.Outcome != taurus.OutcomePassed {
		t.Fatalf("outcome = %q, want passed", rep.Outcome)
	}

	// The served report layers the criteria verdict over the stored
	// measurements: an unviolated assertion must not be listed as failed.
	req, err := http.NewRequest(http.MethodGet, e.srvURL+"/api/runs/"+itoa(runID)+"/report", nil)
	if err != nil {
		t.Fatalf("build report request: %v", err)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET report = %d (%s)", resp.StatusCode, readAll(t, resp))
	}
	var served struct {
		Outcome         string   `json:"outcome"`
		Criteria        []string `json:"criteria"`
		FailingCriteria []struct {
			Criterion string `json:"criterion"`
			Unparsed  bool   `json:"unparsed"`
		} `json:"failing_criteria"`
	}
	if err := json.Unmarshal([]byte(readAll(t, resp)), &served); err != nil {
		t.Fatalf("served report is not JSON: %v", err)
	}
	if len(served.FailingCriteria) != 0 {
		t.Fatalf("failing_criteria = %+v, want none -- the p95<800ms assertion held on a clean run", served.FailingCriteria)
	}
}
