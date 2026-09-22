//go:build e2e

// Phase 103's full-stack pin: the dilution case that motivated the phase.
// A two-label soak runs through the real ingest path -- checkout rising
// 0.1s -> 0.4s at a tenth of the traffic, browse flat 0.2s at nine tenths
// -- and the aggregate trend stays quiet exactly the way it did before,
// while the report's per-label breakdown flags checkout alone. The label
// tallies ride the whole pipeline: ingest -> report_progress_second's
// label_latency JSON (0085) -> snapshot -> report -> execution_report's
// soak_trend JSON (0077, Labels nested, no new column) -> the served
// endpoint.
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/report"
)

// soakLabelSeconds pushes one shard's whole two-label window as a single
// non-final batch: per second, one checkout row (rising) and one browse row
// (flat), each with its own sample count. The shape a multi-label soak's
// sidecar streams.
func soakLabelSeconds(t *testing.T, e *phase7Env, executionID, scenarioID, runID int64, shard int, base int64, n int) {
	t.Helper()
	intervals := make([]metrics.Interval, 0, n*2)
	for i := range n {
		ts := base + int64(i)
		checkout := 0.1 + 0.3*float64(i)/float64(n-1)
		intervals = append(intervals,
			metrics.Interval{
				Seq: int64(i*2 + 1), Timestamp: ts, Label: "checkout",
				Concurrency: 1, Samples: 5, Succeeded: 5,
				Latency: metrics.Histogram{checkout: 5},
			},
			metrics.Interval{
				Seq: int64(i*2 + 2), Timestamp: ts, Label: "browse",
				Concurrency: 1, Samples: 45, Succeeded: 45,
				Latency: metrics.Histogram{0.2: 45},
			})
	}
	postSoakBatch(t, e, executionID, scenarioID, runID, shard, false, intervals)
}

// soakLabelFinal closes one shard with the window's last second, both
// labels continuing their curves -- the run finalises with the final
// second's tallies included.
func soakLabelFinal(t *testing.T, e *phase7Env, executionID, scenarioID, runID int64, shard int, ts int64) {
	t.Helper()
	postSoakBatch(t, e, executionID, scenarioID, runID, shard, true, []metrics.Interval{
		{
			Seq: 5000, Timestamp: ts, Label: "checkout",
			Concurrency: 1, Samples: 5, Succeeded: 5,
			Latency: metrics.Histogram{0.4: 5},
		},
		{
			Seq: 5001, Timestamp: ts, Label: "browse",
			Concurrency: 1, Samples: 45, Succeeded: 45,
			Latency: metrics.Histogram{0.2: 45},
		},
	})
}

func postSoakBatch(t *testing.T, e *phase7Env, executionID, scenarioID, runID int64, shard int, final bool, intervals []metrics.Interval) {
	t.Helper()
	b := metrics.Batch{
		ExecutionID: executionID, ScenarioID: scenarioID, RunID: runID,
		ShardIndex: shard, StreamID: "s0", Intervals: intervals,
	}
	if final {
		exit := 0
		b.Final, b.ExitCode = true, &exit
	}
	body, err := json.Marshal(b)
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
		t.Fatalf("ingest soak batch (shard %d): %v", shard, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest soak batch (shard %d) = %d, want 202", shard, resp.StatusCode)
	}
}

func TestPhase103_PerLabelSoakLeakEndToEnd(t *testing.T) {
	e := setupPhase90(t)
	ctx := context.Background()

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

	// --- the diluted soak: checkout rising 0.1s -> 0.4s at 5 samples/s,
	// browse flat 0.2s at 45 samples/s. The aggregate's halves read
	// ~197ms -> ~213ms (ratio ~1.08, under 1.5x) -- quiet, exactly the
	// pre-phase-103 blind spot -- while checkout's own trend reads
	// ~175ms -> ~325ms and fires.
	execID := postForm(t, e.client, e.srvURL+"/api/executions", url.Values{"name": {"soak-diluted"}, "project_id": {itoa(projectID)}})
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
	for _, sh := range spec.Shards {
		soakLabelSeconds(t, e, execID, scenarioID, runID, sh.Index, 5000, 150)
		soakLabelFinal(t, e, execID, scenarioID, runID, sh.Index, 5150)
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

	// The store's copy: aggregate quiet, checkout flagged, browse not.
	tr := rep.SoakTrend
	t.Logf("diluted report: trend=%+v samples=%d", tr, rep.Achieved.Samples)
	if tr == nil {
		t.Fatalf("no soak trend on a 151-second window: %+v", rep)
	}
	if tr.LeakSuspected {
		t.Errorf("aggregate suspected the diluted leak: %+v", tr)
	}
	var checkout, browse *report.LabelSoakTrend
	for i := range tr.Labels {
		switch tr.Labels[i].Label {
		case "checkout":
			checkout = &tr.Labels[i]
		case "browse":
			browse = &tr.Labels[i]
		}
	}
	if checkout == nil || browse == nil {
		t.Fatalf("label trends = %+v, want both checkout and browse", tr.Labels)
	}
	if !checkout.LeakSuspected {
		t.Errorf("checkout's own trend did not suspect the leak: %+v", checkout)
	}
	if got := checkout.FirstHalfMs; !approxRange(got, 175, 8) {
		t.Errorf("checkout first half = %v ms, want ~175", got)
	}
	if got := checkout.SecondHalfMs; !approxRange(got, 325, 8) {
		t.Errorf("checkout second half = %v ms, want ~325", got)
	}
	if got := checkout.SlopeMsPerMin; !approxRange(got, 120, 8) {
		t.Errorf("checkout slope = %v ms/min, want ~120", got)
	}
	if browse.LeakSuspected {
		t.Errorf("browse's flat trend suspected a leak: %+v", browse)
	}
	if got := browse.FirstHalfMs; !approxRange(got, 200, 2) {
		t.Errorf("browse first half = %v ms, want ~200", got)
	}

	// And the served copy: the same report over HTTP, Labels riding the
	// soak_trend JSON column out to a reader.
	var served report.Report
	getJSON(t, e.client, e.srvURL+"/api/runs/"+itoa(runID)+"/report", http.StatusOK, &served)
	if served.SoakTrend == nil || len(served.SoakTrend.Labels) != len(tr.Labels) {
		t.Fatalf("served report labels = %+v, want the stored %+v", served.SoakTrend, tr)
	}
	var servedCheckout *report.LabelSoakTrend
	for i := range served.SoakTrend.Labels {
		if served.SoakTrend.Labels[i].Label == "checkout" {
			servedCheckout = &served.SoakTrend.Labels[i]
		}
	}
	if servedCheckout == nil || !servedCheckout.LeakSuspected {
		t.Errorf("served report lost checkout's leak: %+v", served.SoakTrend.Labels)
	}
}
