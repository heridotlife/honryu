//go:build e2e

// Phase 99's full-stack pin: a soak stated over the wire (mode, rate,
// duration) runs with per-second intervals pushed as ordinary non-final
// ingest batches -- a growing-latency window settles into a report whose
// SoakTrend suspects the leak with honest halves and slope, and a
// flat-latency control run of the same shape does not. The trend rides
// the whole pipeline: ingest -> report_progress_second's latency tally
// (0078) -> snapshot -> report -> execution_report.soak_trend (0077).
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/report"
)

// soakSeconds pushes one shard's whole window as a single non-final batch:
// one interval per second, every sample of second i in the bucket lat(i)
// returns, exactly the shape a soak's sidecar streams while the run holds.
// The final batch that follows (shardFinal's shape) closes the shard with
// the window's last second, so the run finalises with the trend in state.
func soakSeconds(t *testing.T, e *phase7Env, executionID, scenarioID, runID int64, shard int, base int64, n int, lat func(i int) float64) {
	t.Helper()
	intervals := make([]metrics.Interval, 0, n)
	for i := range n {
		bucket := lat(i)
		intervals = append(intervals, metrics.Interval{
			Seq: int64(i + 1), Timestamp: base + int64(i), Label: "checkout",
			Concurrency: 2, Samples: 5, Succeeded: 5,
			Latency: metrics.Histogram{bucket: 5},
		})
	}
	body, err := json.Marshal(metrics.Batch{
		ExecutionID: executionID, ScenarioID: scenarioID, RunID: runID,
		ShardIndex: shard, StreamID: "s0", Intervals: intervals,
	})
	if err != nil {
		t.Fatalf("marshal soak batch: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, e.srvURL+"/api/ingest", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build ingest request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer engine-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("ingest soak seconds (shard %d): %v", shard, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest soak seconds (shard %d) = %d, want 202", shard, resp.StatusCode)
	}
}

// soakFinal closes one shard with the window's last second, continuing the
// latency curve -- the run's finalisation then computes the trend with the
// final second included, the way a real pod's last flush does.
func soakFinal(t *testing.T, e *phase7Env, executionID, scenarioID, runID int64, shard int, ts int64, lat float64) {
	t.Helper()
	exit := 0
	body, err := json.Marshal(metrics.Batch{
		ExecutionID: executionID, ScenarioID: scenarioID, RunID: runID,
		ShardIndex: shard, StreamID: "s0", Final: true, ExitCode: &exit,
		Intervals: []metrics.Interval{{
			Seq: 1000, Timestamp: ts, Label: "checkout",
			Concurrency: 2, Samples: 5, Succeeded: 5,
			Latency: metrics.Histogram{lat: 5},
		}},
	})
	if err != nil {
		t.Fatalf("marshal final batch: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, e.srvURL+"/api/ingest", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build final request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer engine-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("ingest final (shard %d): %v", shard, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest final (shard %d) = %d, want 202", shard, resp.StatusCode)
	}
}

// runSoak deploys and triggers a soak-mode execution, drives every shard's
// window through the ingest path, and returns the settled report.
func runSoak(t *testing.T, e *phase7Env, projectID, scenarioID int64, name string, base int64, lat func(i int) float64, lastLat float64) report.Report {
	t.Helper()
	ctx := context.Background()

	execID := postForm(t, e.client, e.srvURL+"/api/executions", url.Values{"name": {name}, "project_id": {itoa(projectID)}})
	cfg := fmt.Sprintf("multi-test:\n  collectionid: %d\n  tests:\n    - testid: %d\n      mode: soak\n      throughput: 5\n      duration: 150\n",
		execID, scenarioID)
	putMultipart(t, e.client, e.srvURL+"/api/executions/"+itoa(execID)+"/config", "config.yaml", cfg)

	baseURL := e.srvURL + "/api/executions/" + itoa(execID)
	postAction(t, e.client, baseURL+"/deploy", http.StatusOK)
	postAction(t, e.client, baseURL+"/trigger", http.StatusOK)
	runID, running, err := e.repo.CurrentRun(ctx, execID)
	if err != nil || !running {
		t.Fatalf("CurrentRun after trigger: running=%v err=%v", running, err)
	}

	spec, ok := e.sched.LastDeploy(execID, scenarioID)
	if !ok {
		t.Fatalf("no deploy recorded for execution %d / scenario %d", execID, scenarioID)
	}
	// Every shard streams the same window: the trend is a mean, so however
	// many pods the deployment fanned out to, the halves and slope come out
	// as one shard's would.
	for _, sh := range spec.Shards {
		soakSeconds(t, e, execID, scenarioID, runID, sh.Index, base, 150, lat)
		soakFinal(t, e, execID, scenarioID, runID, sh.Index, base+150, lastLat)
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

func TestPhase99_SoakLeakEndToEnd(t *testing.T) {
	e := setupPhase90(t)

	projectID := postForm(t, e.client, e.srvURL+"/api/projects", url.Values{"name": {"svc"}, "owner": {"honryu"}})
	scenarioID := postForm(t, e.client, e.srvURL+"/api/scenarios", url.Values{"name": {"target"}, "project_id": {itoa(projectID)}})
	putMultipart(t, e.client, e.srvURL+"/api/scenarios/"+itoa(scenarioID)+"/files", "scenario.jmx", "<jmx/>")

	// --- calibrate at the baseline pod size, phase90's script: the capacity
	// key a mode config resolves against (an uncalibrated scenario's mode
	// config is refused with no_profile).
	calibExec := createCalibration(t, e, projectID, scenarioID, "failures>90%", "500m", "512Mi",
		url.Values{"seed_qps": {"10"}, "max_qps": {"1000"}, "max_steps": {"2"}, "hold_seconds": {"1"}})
	jobID := triggerCalibration(t, e, calibExec)
	e.scripted.executionID, e.scripted.scenarioID = calibExec, scenarioID
	e.scripted.steps = []scriptedStep{
		{exitCode: 0, succeeded: 10, latency: 0.5},
		{exitCode: 0, succeeded: 8, latency: 0.5},
		{exitCode: 0, succeeded: 9, latency: 0.5},
	}
	advance(t, e)
	advance(t, e)
	job := getCalibrationJob(t, e, jobID)
	if job.Phase != "done" || job.Result == nil || job.Result.PerPodQPS != 10 {
		t.Fatalf("calibration job = %+v, want engine-limited at 10 qps/pod", job)
	}

	// --- the leaking soak: 150 seconds rising 0.1s -> 0.4s (the final
	// batch adds the window's last second at 0.4s). Expect first half
	// ~174ms, second ~325ms, slope ~120ms/min -- and the verdict.
	rising := runSoak(t, e, projectID, scenarioID, "soak-leak", 2000,
		func(i int) float64 { return 0.1 + 0.3*float64(i)/149 }, 0.4)
	t.Logf("rising report: trend=%+v samples=%d outcome=%s",
		rising.SoakTrend, rising.Achieved.Samples, rising.Outcome)
	if rising.SoakTrend == nil {
		t.Fatalf("no soak trend on a 151-second window: %+v", rising)
	}
	if !rising.SoakTrend.LeakSuspected {
		t.Errorf("leak not suspected on the rising window: %+v", rising.SoakTrend)
	}
	if got := rising.SoakTrend.FirstHalfMs; !approxRange(got, 174, 8) {
		t.Errorf("first half = %v ms, want ~174", got)
	}
	if got := rising.SoakTrend.SecondHalfMs; !approxRange(got, 326, 8) {
		t.Errorf("second half = %v ms, want ~326", got)
	}
	if got := rising.SoakTrend.SlopeMsPerMin; !approxRange(got, 120, 8) {
		t.Errorf("slope = %v ms/min, want ~120", got)
	}

	// --- the control: the same window at a flat 0.2s. The trend is still
	// reported (a reader wants the halves), the verdict stays quiet.
	flat := runSoak(t, e, projectID, scenarioID, "soak-flat", 3000,
		func(int) float64 { return 0.2 }, 0.2)
	t.Logf("flat report: trend=%+v samples=%d outcome=%s",
		flat.SoakTrend, flat.Achieved.Samples, flat.Outcome)
	if flat.SoakTrend == nil {
		t.Fatalf("no soak trend on the flat control: %+v", flat)
	}
	if flat.SoakTrend.LeakSuspected {
		t.Errorf("leak suspected on the flat control: %+v", flat.SoakTrend)
	}
	if got := flat.SoakTrend.FirstHalfMs; !approxRange(got, 200, 2) {
		t.Errorf("flat first half = %v ms, want ~200", got)
	}
	if got := flat.SoakTrend.SlopeMsPerMin; math.Abs(got) > 1 {
		t.Errorf("flat slope = %v ms/min, want ~0", got)
	}
}

// approxRange reports got within ±tol of want.
func approxRange(got, want, tol float64) bool {
	return math.Abs(got-want) <= tol
}
