//go:build e2e

// Phase 90's full-stack pin: calibrate a scenario at the baseline pod size
// (phase7's scripted-ingest harness), then state a burst-mode config over
// the wire -- mode, rate, duration, nothing else -- and prove the server
// resolved it BEFORE persisting: the stored entry carries engines from the
// capacity profile, concurrency from the calibration report's real p95,
// ramp-up from the burst policy, with the mode kept as provenance. A
// deployed run of that config then produces a report whose Requested load
// is exactly the derived numbers -- requested-vs-achieved judged against
// what resolution decided, not what the operator typed.
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
	"github.com/heridotlife/honryu/internal/app/calibrationapp"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/app/metricsapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports/fake"
	"github.com/heridotlife/honryu/test/dbtest"
)

// setupPhase90 mirrors setupPhase7 with exactly one wiring delta: the
// execution service carries mode sources, the same cmd/api wiring -- the
// calibration service answers fan-out, the repository answers the job
// ledger and report reads, and the latency-hint fallback is the config
// default. Everything else (real MySQL, real router, real calibration
// machinery over the fake scheduler) is phase7's stack unchanged, so the
// env type is too.
func setupPhase90(t *testing.T) *phase7Env {
	t.Helper()
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	store := local.New(t.TempDir(), "")
	sched := fake.NewScheduler()
	sink := fake.NewMetricsSink()
	bus := membus.New()

	collector := metricsapp.NewService(repo, sink, bus, repo, repo)
	lifecycle := lifecycleapp.NewService(repo, sched, store, lifecycleapp.StaticImage("jmeter")).WithMetrics(collector)
	scenarios := scenarioapp.NewService(repo, store)

	scripted := &scriptedIngest{repo: repo}
	runner := calibrationapp.NewStepRunner(repo, lifecycle, repo).WithSleep(scripted.sleep)
	calibrations := calibrationapp.NewService(repo).WithRunner(runner).WithFingerprint(scenarios)

	router := httpapi.NewRouter(httpapi.Deps{
		Projects:  projectapp.NewService(repo),
		Scenarios: scenarios,
		Executions: executionapp.NewService(repo, store, 500).WithModeSources(executionapp.ModeSources{
			Capacity:      calibrations,
			Jobs:          repo,
			Reports:       repo,
			LatencyHint:   250 * time.Millisecond,
			DefaultEngine: taurus.ExecutorJMeter,
		}),
		Lifecycle:     lifecycle,
		Calibrations:  calibrations,
		Store:         store,
		Metrics:       collector,
		Reports:       repo,
		IngestToken:   "engine-token",
		DefaultOwners: []string{"honryu"},
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	scripted.client = srv.Client()
	scripted.srvURL = srv.URL
	return &phase7Env{
		client: srv.Client(), srvURL: srv.URL, repo: repo, sched: sched,
		calibrations: calibrations, scenarios: scenarios, scripted: scripted,
	}
}

// shardFinal pushes one ordinary run's final batch for one engine shard --
// the engine-side contract a deployed pod fulfils when bzt exits (phase88's
// clusterFinal without the cluster dimension).
func shardFinal(t *testing.T, e *phase7Env, executionID, scenarioID, runID int64, shard int, samples int64) {
	t.Helper()
	exit := 0
	body, err := json.Marshal(metrics.Batch{
		ExecutionID: executionID, ScenarioID: scenarioID, RunID: runID,
		ShardIndex: shard, StreamID: "s0", Final: true, ExitCode: &exit,
		Intervals: []metrics.Interval{{
			Seq: 1, Timestamp: 1000, Label: "checkout", Concurrency: 1,
			Samples: samples, Succeeded: samples,
			Latency: metrics.Histogram{0.01: samples},
		}},
	})
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
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest (shard %d) = %d, want 202", shard, resp.StatusCode)
	}
}

func TestPhase90_ModeConfigEndToEnd(t *testing.T) {
	e := setupPhase90(t)
	ctx := context.Background()

	projectID := postForm(t, e.client, e.srvURL+"/api/projects", url.Values{"name": {"svc"}, "owner": {"honryu"}})
	scenarioID := postForm(t, e.client, e.srvURL+"/api/scenarios", url.Values{"name": {"target"}, "project_id": {itoa(projectID)}})
	putMultipart(t, e.client, e.srvURL+"/api/scenarios/"+itoa(scenarioID)+"/files", "scenario.jmx", "<jmx/>")

	// --- calibrate at the baseline pod size (500m/512Mi): the capacity
	// key an unpinned execution's mode entry resolves against. Phase7's
	// engine-limited script -- clean seed step, then an engine-short step
	// confirmed by its retry -- leaves perPodQPS 10; every scripted sample
	// lands in a 0.5s bucket, so the settled report's p95 is a real 0.5s
	// for the latency chain to walk profile -> job -> execution -> report.
	calibExec := createCalibration(t, e, projectID, scenarioID, "failures>90%", "500m", "512Mi",
		url.Values{"seed_qps": {"10"}, "max_qps": {"1000"}, "max_steps": {"2"}, "hold_seconds": {"1"}})
	jobID := triggerCalibration(t, e, calibExec)
	e.scripted.executionID, e.scripted.scenarioID = calibExec, scenarioID
	e.scripted.steps = []scriptedStep{
		{exitCode: 0, succeeded: 10, latency: 0.5}, // tick 1 @10: clean
		{exitCode: 0, succeeded: 8, latency: 0.5},  // tick 2 attempt @20: engine-short
		{exitCode: 0, succeeded: 9, latency: 0.5},  // tick 2 retry @20: engine-short, confirmed
	}
	advance(t, e)
	advance(t, e)

	job := getCalibrationJob(t, e, jobID)
	if job.Phase != "done" || job.Result == nil || job.Result.SaturatedBy != "engine" || job.Result.PerPodQPS != 10 {
		t.Fatalf("calibration job = %+v, want engine-limited at 10 qps/pod", job)
	}
	// The profile the mode entry will fan out from: ceil(20/10) = 2 engines.
	if got := fanOut(t, e, scenarioID, "jmeter", "500m", "512Mi", "20"); got.Status != "ok" || got.Engines != 2 {
		t.Fatalf("fan out = %+v, want {ok, 2}", got)
	}

	// --- state a burst-mode config over the wire: mode, rate, duration --
	// none of the numbers the server is supposed to derive.
	modeExec := postForm(t, e.client, e.srvURL+"/api/executions", url.Values{"name": {"mode-run"}, "project_id": {itoa(projectID)}})
	modeCfg := fmt.Sprintf("multi-test:\n  collectionid: %d\n  tests:\n    - testid: %d\n      mode: burst\n      throughput: 20\n      duration: 600\n",
		modeExec, scenarioID)
	putMultipart(t, e.client, e.srvURL+"/api/executions/"+itoa(modeExec)+"/config", "config.yaml", modeCfg)

	// --- the stored entry is fully resolved. Concurrency 30 is
	// ceil(20 rps * 0.5s * 3.0 headroom) from the calibration report's
	// measured p95 -- the 250ms fallback would have floored at 20, so this
	// number proves the chain, not the stand-in. Ramp-up 0 is the burst
	// policy; engines 2 is the fan-out above; the mode rides along.
	want := loadprofile.Entry{
		ScenarioID: scenarioID, Concurrency: 30, Rampup: 0,
		Engines: 2, Throughput: 20, Duration: 600, Mode: "burst",
	}
	entries, err := e.repo.LoadProfileFor(ctx, modeExec)
	if err != nil {
		t.Fatalf("LoadProfileFor: %v", err)
	}
	if len(entries) != 1 || entries[0] != want {
		t.Fatalf("stored entry = %+v, want %+v", entries, want)
	}
	// The wire echoes the same resolved entry back (the web summary card's
	// source), not the three-field statement that was sent.
	var cfg loadprofile.Wrapper
	getJSON(t, e.client, e.srvURL+"/api/executions/"+itoa(modeExec)+"/config", http.StatusOK, &cfg)
	if len(cfg.Content.Tests) != 1 || cfg.Content.Tests[0] != want {
		t.Fatalf("GET config = %+v, want the resolved entry %+v", cfg.Content.Tests, want)
	}

	// --- run it: deploy/trigger, then both engine shards' final batches
	// settle the run (the profile planned 2 shards, so 2 finals).
	base := e.srvURL + "/api/executions/" + itoa(modeExec)
	postAction(t, e.client, base+"/deploy", http.StatusOK)
	postAction(t, e.client, base+"/trigger", http.StatusOK)
	runID, running, err := e.repo.CurrentRun(ctx, modeExec)
	if err != nil || !running {
		t.Fatalf("CurrentRun after trigger: running=%v err=%v", running, err)
	}
	shardFinal(t, e, modeExec, scenarioID, runID, 0, 100)
	shardFinal(t, e, modeExec, scenarioID, runID, 1, 50)

	// --- the report judges achieved against the DERIVED numbers:
	// requested concurrency is run.VirtualUsers' accounting shape
	// (engines x concurrency = 2 x 30), the rate and window as stated.
	rep, err := e.repo.GetReport(ctx, runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.Requested.Concurrency != 60 {
		t.Errorf("requested concurrency = %d, want 60 (2 engines x 30 VUs)", rep.Requested.Concurrency)
	}
	if rep.Requested.Throughput != 20 {
		t.Errorf("requested throughput = %v, want 20", rep.Requested.Throughput)
	}
	if rep.Requested.DurationSeconds != 600 {
		t.Errorf("requested duration = %d, want 600", rep.Requested.DurationSeconds)
	}
	if rep.Achieved.Samples != 150 {
		t.Errorf("achieved samples = %d, want 150 (both shards' loads)", rep.Achieved.Samples)
	}
}
