//go:build e2e

// Phase 97's start-flow pin: the WIRE sequence phase 93's one-click Start
// performs -- deploy an idle execution, then trigger immediately, no gap.
// The countdown the UI inserts between the two POSTs is client-side JS
// with vitest fake-timer coverage already; 10s of wall time here would add
// nothing that suite doesn't see. What only the boot harness can prove is
// that the pair lands as the handler meets it in the wild: a trigger
// arriving while the deploy's pods are still starting, which is exactly
// the race triggerExecution's bounded wait owns. The fake scheduler's
// NotReadyCalls makes that race deterministic -- its withheld status is
// "pods not there yet" (the domain reads a zero pool as idle and refuses
// with a readiness-class conflict), flipping to ready on the next poll
// beat, so the handler must retry within its window for the run to open
// at all. A client that had to retry itself, or a 409, fails this test.
package e2e_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

// TestPhase97_StartFlowDeployThenImmediateTrigger runs phase 90's full
// stack (calibration -> capacity profile -> burst-mode config resolved on
// the wire) and then, where phase 90 deployed and triggered a run it had
// already proven resolved, this test drives the start flow's own sequence
// against the readiness race: no pre-deploy, deploy from idle, trigger in
// the very next statement -- and the pods' first status read reports them
// not there yet. The handler's bounded wait must carry the trigger to 200,
// the run must proceed, and the settled report must carry the numbers the
// mode resolution decided (requested concurrency 60 = 2 engines x 30 VUs,
// the rate and window as stated, both shards' loads achieved).
func TestPhase97_StartFlowDeployThenImmediateTrigger(t *testing.T) {
	e := setupPhase90(t)
	ctx := context.Background()

	projectID := postForm(t, e.client, e.srvURL+"/api/projects", url.Values{"name": {"svc"}, "owner": {"honryu"}})
	scenarioID := postForm(t, e.client, e.srvURL+"/api/scenarios", url.Values{"name": {"target"}, "project_id": {itoa(projectID)}})
	putMultipart(t, e.client, e.srvURL+"/api/scenarios/"+itoa(scenarioID)+"/files", "scenario.jmx", "<jmx/>")

	// --- the capacity premise phase 90 established: an engine-limited
	// calibration at the baseline pod size settles at 10 qps/pod with a
	// real 0.5s p95, so a 20 rps burst entry resolves to 2 engines and
	// ceil(20 * 0.5 * 3.0) = 30 concurrency. Nothing here is new -- it is
	// the preamble the mode config's PUT refuses to run without (409
	// no_profile otherwise).
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
	if got := fanOut(t, e, scenarioID, "jmeter", "500m", "512Mi", "20"); got.Status != "ok" || got.Engines != 2 {
		t.Fatalf("fan out = %+v, want {ok, 2}", got)
	}

	// --- the mode entry: burst, rate and duration only. The server
	// resolves engines (2) and concurrency (30) from the profile above
	// before persisting -- the numbers the report below must judge
	// against, never the three-field statement sent here.
	modeExec := postForm(t, e.client, e.srvURL+"/api/executions", url.Values{"name": {"start-flow"}, "project_id": {itoa(projectID)}})
	modeCfg := fmt.Sprintf("multi-test:\n  collectionid: %d\n  tests:\n    - testid: %d\n      mode: burst\n      throughput: 20\n      duration: 600\n",
		modeExec, scenarioID)
	putMultipart(t, e.client, e.srvURL+"/api/executions/"+itoa(modeExec)+"/config", "config.yaml", modeCfg)

	// --- the pin itself. One startup beat withheld: the deployment
	// exists but its first readiness read reports nothing there yet --
	// the state a real cluster is in between Deploy returning and the
	// pods scheduling. Deploy itself never reads status, so the beat is
	// guaranteed to be the trigger's first attempt, not consumed early.
	e.sched.NotReadyCalls = 1

	base := e.srvURL + "/api/executions/" + itoa(modeExec)
	postAction(t, e.client, base+"/deploy", http.StatusOK)
	// Immediately -- no sleep, no client-side retry loop. This 200 is
	// the handler's bounded wait doing its job over the withheld beat.
	postAction(t, e.client, base+"/trigger", http.StatusOK)

	runID, running, err := e.repo.CurrentRun(ctx, modeExec)
	if err != nil || !running {
		t.Fatalf("CurrentRun after immediate trigger: running=%v err=%v", running, err)
	}
	// The profile planned 2 shards; both engine shards' final batches
	// settle the run, exactly as a deployed pod exiting bzt would.
	shardFinal(t, e, modeExec, scenarioID, runID, 0, 100)
	shardFinal(t, e, modeExec, scenarioID, runID, 1, 50)

	// --- the report judges achieved against the DERIVED numbers:
	// requested concurrency is the plan's accounting shape (engines x
	// concurrency = 2 x 30 -- only derivable from resolution, not from
	// anything this test stated), the rate and window as sent, and both
	// shards' loads achieved.
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
