//go:build e2e

// Phase 98's full-stack pin: a staircase stated over the wire (mode,
// ceiling rate, per-step hold, steps) resolves to ONE entry sized at the
// ceiling, deploys as sequential bzt step blocks per pod (the mechanism
// recon's (a): modules.local.sequential), and settles into a report that
// carries the per-step Requested rates -- the phase-90 pattern, with one
// step assertion proving the shape reached the engine and the report.
package e2e_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	yaml "gopkg.in/yaml.v3"

	"github.com/heridotlife/honryu/internal/domain/loadmode"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
)

func TestPhase98_StaircaseEndToEnd(t *testing.T) {
	e := setupPhase90(t)
	ctx := context.Background()

	projectID := postForm(t, e.client, e.srvURL+"/api/projects", url.Values{"name": {"svc"}, "owner": {"honryu"}})
	scenarioID := postForm(t, e.client, e.srvURL+"/api/scenarios", url.Values{"name": {"target"}, "project_id": {itoa(projectID)}})
	putMultipart(t, e.client, e.srvURL+"/api/scenarios/"+itoa(scenarioID)+"/files", "scenario.jmx", "<jmx/>")

	// --- calibrate at the baseline pod size, exactly phase90's script:
	// engine-limited at 10 qps/pod with a measured p95 of 0.5s -- the
	// capacity key and latency chain the staircase resolves against.
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

	// --- state a staircase over the wire: ceiling 20 rps, 60s per step
	// (the floor), 4 steps. None of the derived numbers are sent.
	modeExec := postForm(t, e.client, e.srvURL+"/api/executions", url.Values{"name": {"stairs"}, "project_id": {itoa(projectID)}})
	modeCfg := fmt.Sprintf("multi-test:\n  collectionid: %d\n  tests:\n    - testid: %d\n      mode: staircase\n      throughput: 20\n      duration: 60\n      steps: 4\n",
		modeExec, scenarioID)
	putMultipart(t, e.client, e.srvURL+"/api/executions/"+itoa(modeExec)+"/config", "config.yaml", modeCfg)

	want := loadprofile.Entry{
		ScenarioID: scenarioID, Concurrency: 30, Rampup: 0,
		Engines: 2, Throughput: 20, Duration: 60, Mode: "staircase", Steps: 4,
	}
	entries, err := e.repo.LoadProfileFor(ctx, modeExec)
	if err != nil {
		t.Fatalf("LoadProfileFor: %v", err)
	}
	if len(entries) != 1 || entries[0] != want {
		t.Fatalf("stored entry = %+v, want %+v", entries, want)
	}
	var cfg loadprofile.Wrapper
	getJSON(t, e.client, e.srvURL+"/api/executions/"+itoa(modeExec)+"/config", http.StatusOK, &cfg)
	if len(cfg.Content.Tests) != 1 || cfg.Content.Tests[0] != want {
		t.Fatalf("GET config = %+v, want the resolved entry %+v", cfg.Content.Tests, want)
	}

	// --- deploy and trigger: the fake scheduler holds the compiled shard
	// specs, so the shape can be pinned as it actually reached the pods.
	base := e.srvURL + "/api/executions/" + itoa(modeExec)
	postAction(t, e.client, base+"/deploy", http.StatusOK)
	postAction(t, e.client, base+"/trigger", http.StatusOK)
	runID, running, err := e.repo.CurrentRun(ctx, modeExec)
	if err != nil || !running {
		t.Fatalf("CurrentRun after trigger: running=%v err=%v", running, err)
	}

	// The profile the run finalizes against, read AFTER deploy+trigger:
	// if any lifecycle write clobbered steps, this catches it before the
	// report is blamed.
	entries, err = e.repo.LoadProfileFor(ctx, modeExec)
	if err != nil {
		t.Fatalf("LoadProfileFor after trigger: %v", err)
	}
	t.Logf("profile after trigger: %+v", entries)

	spec, ok := e.sched.LastDeploy(modeExec, scenarioID)
	if !ok {
		t.Fatalf("no deploy recorded for execution %d / scenario %d", modeExec, scenarioID)
	}
	if len(spec.Shards) != 2 {
		t.Fatalf("shards = %d, want 2 (fan-out at the ceiling)", len(spec.Shards))
	}
	table := loadmode.StaircaseSteps(30, 20, 4)
	var shardCfgs []taurus.Config
	for _, sh := range spec.Shards {
		var c taurus.Config
		if err := yaml.Unmarshal(sh.Config, &c); err != nil {
			t.Fatalf("shard %d: unmarshal compiled config: %v", sh.Index, err)
		}
		shardCfgs = append(shardCfgs, c)
		if len(c.Execution) != 4 {
			t.Fatalf("shard %d carries %d executions, want the 4 step blocks", sh.Index, len(c.Execution))
		}
		if mod, ok := c.Modules["local"]; !ok || mod.Sequential == nil || !*mod.Sequential {
			t.Fatalf("shard %d modules.local = %+v, want sequential: true", sh.Index, c.Modules["local"])
		}
	}
	for stepIdx, row := range table {
		rate, conc := 0, 0
		for _, c := range shardCfgs {
			ex := c.Execution[stepIdx]
			rate += ex.Throughput
			conc += ex.Concurrency
			if ex.RampUp != 0 {
				t.Errorf("step %d ramp-up = %v, want 0", stepIdx, ex.RampUp)
			}
			if got := int(time.Duration(ex.HoldFor).Seconds()); got != 60 {
				t.Errorf("step %d hold = %ds, want 60", stepIdx, got)
			}
		}
		if rate != row.Throughput || conc != row.Concurrency {
			t.Errorf("step %d aggregate = %d rps / %d VUs, want %d / %d", stepIdx, rate, conc, row.Throughput, row.Concurrency)
		}
	}

	// --- both shards' finals settle the run; the report carries the
	// per-step Requested rates (the shape as the run is judged by it).
	shardFinal(t, e, modeExec, scenarioID, runID, 0, 100)
	shardFinal(t, e, modeExec, scenarioID, runID, 1, 50)

	// phase90's identical pattern finalizes synchronously inside the last
	// ingest; a short bounded poll only covers scheduler-lag variance.
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
	// The whole Requested statement: if steps are missing but throughput
	// or duration carry staircase-derived values, the finalize ran with a
	// steps-less profile; if everything is zero, a foreign writer saved
	// this report first (SaveReport's first-write-wins kept it).
	t.Logf("report Requested=%+v Achieved.Samples=%d Outcome=%s", rep.Requested, rep.Achieved.Samples, rep.Outcome)

	if rep.Requested.Throughput != 20 {
		t.Errorf("requested throughput = %v, want the stated ceiling 20", rep.Requested.Throughput)
	}
	if rep.Requested.DurationSeconds != 240 {
		t.Errorf("requested duration = %d, want 240 (4 steps x 60s)", rep.Requested.DurationSeconds)
	}
	if len(rep.Requested.Steps) != 4 {
		t.Fatalf("requested steps = %+v, want the 4-row table", rep.Requested.Steps)
	}
	for i, row := range rep.Requested.Steps {
		if want := float64(table[i].Throughput); row.Throughput != want {
			t.Errorf("requested step %d rate = %v, want %v", i, row.Throughput, want)
		}
		if row.DurationSeconds != 60 {
			t.Errorf("requested step %d hold = %d, want 60", i, row.DurationSeconds)
		}
	}
}
