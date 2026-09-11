package lifecycleapp_test

import (
	"context"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// reapTTL is the idle threshold every reaper test below uses; idleBeyond
// moves the service clock past it (the activity clock itself is stamped with
// the store's real clock, so the reaper must see a "now" beyond ttl to find
// anything idle).
const reapTTL = time.Hour

func idleBeyond() func() time.Time {
	return func() time.Time { return time.Now().Add(reapTTL + time.Minute) }
}

// runAndFinish drives an execution through deploy -> trigger -> natural
// completion (the marker closes the way metricsapp.finalize does), leaving
// engines deployed and idle -- the state a warm-for-re-runs execution sits
// in until its TTL lapses.
func runAndFinish(t *testing.T, e *env) int64 {
	t.Helper()
	ctx := context.Background()
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	runID, running, err := e.store.CurrentRun(ctx, e.executionID)
	if err != nil || !running {
		t.Fatalf("CurrentRun after Trigger = %d,%v,%v", runID, running, err)
	}
	if err := e.store.StopRun(ctx, e.executionID); err != nil {
		t.Fatalf("StopRun (natural completion's close): %v", err)
	}
	return runID
}

// An execution idle beyond the TTL with pods still present is reaped: pods
// and their logs go, while every persisted record -- the execution row, the
// run's history, the captured log -- survives. That is the whole point of
// the reaper: pod teardown only, NOT the full purge.
func TestReapIdle_TeardownPodsAndKeepRecords(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)
	ctx := context.Background()
	runID := runAndFinish(t, e)

	e.svc.WithNow(idleBeyond())
	reaped, err := e.svc.ReapIdle(ctx, reapTTL, []ports.ClusterRef{""})
	if err != nil {
		t.Fatalf("ReapIdle: %v", err)
	}
	if len(reaped) != 1 || reaped[0] != e.executionID {
		t.Fatalf("reaped = %v, want [%d]", reaped, e.executionID)
	}

	// Pods are gone: no deployed execution remains.
	if deployed, _ := e.sched.DeployedExecutions(ctx, ""); len(deployed) != 0 {
		t.Fatalf("deployed after reap = %v, want none", deployed)
	}
	// The execution row and its run history survive.
	if _, err := e.store.GetExecution(ctx, e.executionID); err != nil {
		t.Fatalf("GetExecution after reap: %v", err)
	}
	if _, err := e.store.RunHistory(ctx, runID); err != nil {
		t.Fatalf("RunHistory after reap: %v", err)
	}
	// Teardown's duties ran: the running-scenario markers Trigger opened are
	// cleared (natural completion never clears them itself).
	if rs, err := e.store.RunningScenariosByExecution(ctx, e.executionID); err != nil || len(rs) != 0 {
		t.Fatalf("running scenarios after reap = %v (%v), want none", rs, err)
	}
	// The last run's logs were captured before the pods that held them went.
	logKey := lifecycleapp.RunShardKey(runID, e.planIDs[0], 0, "log")
	if _, err := e.obj.Download(ctx, logKey); err != nil {
		t.Fatalf("captured engine log at %s: %v", logKey, err)
	}
}

// An open run marker means the engines are not provably idle -- a live run
// may simply be quiet -- so the reaper must skip the execution entirely.
func TestReapIdle_OpenRunMarkerSkips(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)
	ctx := context.Background()
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if _, err := e.store.StartRun(ctx, e.executionID, ""); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	e.svc.WithNow(idleBeyond())
	reaped, err := e.svc.ReapIdle(ctx, reapTTL, []ports.ClusterRef{""})
	if err != nil {
		t.Fatalf("ReapIdle: %v", err)
	}
	if len(reaped) != 0 {
		t.Fatalf("reaped = %v, want none (run marker open)", reaped)
	}
	if deployed, _ := e.sched.DeployedExecutions(ctx, ""); len(deployed) != 1 {
		t.Fatalf("deployed = %v, want the execution untouched", deployed)
	}
}

// An active calibration job (not yet done or failed) keeps its engines warm
// on purpose: the search's next step reuses them within minutes, so the
// reaper skips it. A terminal job (done or failed) no longer blocks.
func TestReapIdle_ActiveCalibrationSkips(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)
	ctx := context.Background()
	runAndFinish(t, e)

	jobID, err := e.store.CreateCalibrationJob(ctx, e.executionID)
	if err != nil {
		t.Fatalf("CreateCalibrationJob: %v", err)
	}

	e.svc.WithNow(idleBeyond())
	reaped, err := e.svc.ReapIdle(ctx, reapTTL, []ports.ClusterRef{""})
	if err != nil {
		t.Fatalf("ReapIdle: %v", err)
	}
	if len(reaped) != 0 {
		t.Fatalf("reaped = %v, want none (calibration active)", reaped)
	}

	// The search finishing (phase done) lifts the guard on the next pass.
	terminal := mustTerminalJob(t, e.store, jobID, e.executionID)
	if err := e.store.RecordStep(ctx, jobID, calibration.Step{RequestedQPS: 10, AchievedQPS: 10}, terminal); err != nil {
		t.Fatalf("RecordStep (terminal): %v", err)
	}
	reaped, err = e.svc.ReapIdle(ctx, reapTTL, []ports.ClusterRef{""})
	if err != nil {
		t.Fatalf("ReapIdle after job done: %v", err)
	}
	if len(reaped) != 1 || reaped[0] != e.executionID {
		t.Fatalf("reaped after terminal job = %v, want [%d]", reaped, e.executionID)
	}
}

func mustTerminalJob(t *testing.T, s *fake.Store, jobID, executionID int64) ports.CalibrationJob {
	t.Helper()
	job, err := s.GetCalibrationJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("GetCalibrationJob: %v", err)
	}
	job.Phase = calibration.PhaseDone
	job.ExecutionID = executionID
	return job
}

// TTL zero disables the pass -- the deployment default, so the reaper is
// strictly opt-in.
func TestReapIdle_ZeroTTLDisablesThePass(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)
	ctx := context.Background()
	runAndFinish(t, e)
	e.svc.WithNow(idleBeyond())

	reaped, err := e.svc.ReapIdle(ctx, 0, []ports.ClusterRef{""})
	if err != nil {
		t.Fatalf("ReapIdle(0): %v", err)
	}
	if len(reaped) != 0 {
		t.Fatalf("reaped = %v, want none (disabled)", reaped)
	}
	if deployed, _ := e.sched.DeployedExecutions(ctx, ""); len(deployed) != 1 {
		t.Fatalf("deployed = %v, want the execution untouched", deployed)
	}
}

// A reap must clear the execution's orphaned shard completions: an orphan
// is "this engine already finished" evidence about pods the reap just
// deleted, and stale rows would 409 the next post-redeploy Trigger (the
// live phase-47 incident: reap, deploy 200, trigger 409 "engines already
// finished" for engines that no longer exist). Deploy makes the same clear
// for a new engine generation; the reaper owns the mirror-image one for the
// generation it ends.
func TestReapIdle_ClearsOrphanedCompletions(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)
	ctx := context.Background()
	runAndFinish(t, e)

	// A straggler Final that landed just after natural completion closed the
	// run -- the orphan row the reaped pods leave behind.
	code := 0
	if err := e.store.RecordOrphanCompletion(ctx, ports.OrphanCompletion{
		ExecutionID: e.executionID, ScenarioID: e.planIDs[0], ShardIndex: 0,
		ExitCode: &code, FinishedAt: time.Unix(1000, 0),
	}); err != nil {
		t.Fatalf("RecordOrphanCompletion: %v", err)
	}

	e.svc.WithNow(idleBeyond())
	reaped, err := e.svc.ReapIdle(ctx, reapTTL, []ports.ClusterRef{""})
	if err != nil {
		t.Fatalf("ReapIdle: %v", err)
	}
	if len(reaped) != 1 || reaped[0] != e.executionID {
		t.Fatalf("reaped = %v, want [%d]", reaped, e.executionID)
	}
	if orphans, err := e.store.OrphanCompletions(ctx, e.executionID); err != nil || len(orphans) != 0 {
		t.Fatalf("orphans after reap = %v (%v), want none -- they died with their pods", orphans, err)
	}
}

// An execution with no pods left is not a candidate at all -- the cluster's
// own deployed set drives the pass, so an already-reaped (or never deployed)
// execution is a no-op rather than an error.
func TestReapIdle_NoPodsIsANoOp(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)
	ctx := context.Background()
	runAndFinish(t, e)
	if err := e.sched.PurgeExecution(ctx, "", e.executionID); err != nil {
		t.Fatalf("PurgeExecution: %v", err)
	}
	e.svc.WithNow(idleBeyond())

	reaped, err := e.svc.ReapIdle(ctx, reapTTL, []ports.ClusterRef{""})
	if err != nil {
		t.Fatalf("ReapIdle: %v", err)
	}
	if len(reaped) != 0 {
		t.Fatalf("reaped = %v, want none (no pods)", reaped)
	}
}

// A deployment older than the TTL whose execution was never activity-stamped
// (deployed before the idle clock existed -- the state today's orphaned pods
// are in) falls back to the pods' own deploy time and is reaped on the first
// pass. The same fallback heals pods whose execution row is gone entirely.
func TestReapIdle_FallsBackToDeployTimeWhenNeverStamped(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)
	ctx := context.Background()

	// Deploy straight through the scheduler, bypassing lifecycleapp.Deploy:
	// pods exist, but no activity stamp was ever made.
	spec := ports.DeploySpec{
		Cluster: "", ProjectID: e.projectID, ExecutionID: e.executionID,
		ScenarioID: e.planIDs[0], Image: image,
		Shards: []ports.ShardSpec{{Index: 0, Config: []byte("cfg")}},
	}
	if err := e.sched.DeployScenario(ctx, spec); err != nil {
		t.Fatalf("DeployScenario: %v", err)
	}
	e.svc.WithNow(idleBeyond())

	reaped, err := e.svc.ReapIdle(ctx, reapTTL, []ports.ClusterRef{""})
	if err != nil {
		t.Fatalf("ReapIdle: %v", err)
	}
	if len(reaped) != 1 || reaped[0] != e.executionID {
		t.Fatalf("reaped = %v, want [%d] (deploy-time fallback)", reaped, e.executionID)
	}
}

func TestReapIdle_HealsPodsWhoseExecutionRowIsGone(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)
	ctx := context.Background()
	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.store.DeleteExecution(ctx, e.executionID); err != nil {
		t.Fatalf("DeleteExecution: %v", err)
	}
	e.svc.WithNow(idleBeyond())

	reaped, err := e.svc.ReapIdle(ctx, reapTTL, []ports.ClusterRef{""})
	if err != nil {
		t.Fatalf("ReapIdle: %v", err)
	}
	if len(reaped) != 1 || reaped[0] != e.executionID {
		t.Fatalf("reaped = %v, want [%d] (orphan pods healed)", reaped, e.executionID)
	}
	if deployed, _ := e.sched.DeployedExecutions(ctx, ""); len(deployed) != 0 {
		t.Fatalf("deployed after heal = %v, want none", deployed)
	}
}

// Fresh activity beats an old deployment: an execution whose engines were
// deployed long ago but touched recently (a warm re-run window) is exactly
// what must NOT be reaped. This is the "activity-aware" half of the reaper.
func TestReapIdle_FreshActivityBeatsOldDeployTime(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)
	ctx := context.Background()

	// The StatefulSet is a TTL-and-a-half old...
	e.sched.Now = func() time.Time { return time.Now().Add(-90 * time.Minute) }
	runAndFinish(t, e)
	e.sched.Now = nil

	// ...but the activity stamps land on the real clock, so a reaper running
	// at real-now-plus-59m (inside the TTL window) finds it not yet idle.
	e.svc.WithNow(func() time.Time { return time.Now().Add(reapTTL - time.Minute) })
	reaped, err := e.svc.ReapIdle(ctx, reapTTL, []ports.ClusterRef{""})
	if err != nil {
		t.Fatalf("ReapIdle: %v", err)
	}
	if len(reaped) != 0 {
		t.Fatalf("reaped = %v, want none (activity fresher than TTL)", reaped)
	}
}
