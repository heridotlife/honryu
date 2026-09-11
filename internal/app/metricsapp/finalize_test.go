package metricsapp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

// A report's Engine reflects which Taurus executor ran the load. Populated
// from the execution's own configured preference; a defaulted execution
// (empty preference, deployment default applies) is a known, narrower gap
// than every report having none at all.
func TestFinalize_PopulatesEngineAndClusterFromTheExecution(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	ctx := context.Background()

	existing, err := e.store.GetExecution(ctx, e.executionID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	// A fresh execution with its engine and cluster set, since setup()'s own
	// execution leaves both empty (the default case the doc comments cover).
	withEngine, err := execution.New("k6-run", existing.ProjectID)
	if err != nil {
		t.Fatalf("execution.New: %v", err)
	}
	withEngine.Engine = taurus.ExecutorK6
	withEngine.Cluster = "prod-eu"
	executionID, err := e.store.CreateExecution(ctx, withEngine)
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
	if err := e.store.StoreLoadProfile(ctx, executionID, false, []loadprofile.Entry{
		{Name: "p", ScenarioID: e.scenarioIDs[0], Concurrency: 1, Rampup: 1, Engines: 1, Duration: 1},
	}); err != nil {
		t.Fatalf("StoreLoadProfile: %v", err)
	}
	runID, err := e.store.StartRun(ctx, executionID, "")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if err := e.svc.Finalize(ctx, executionID, runID); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	rep, err := e.reports.GetReport(ctx, runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.Engine != taurus.ExecutorK6 {
		t.Errorf("engine = %q, want %q", rep.Engine, taurus.ExecutorK6)
	}
	if rep.Cluster != "prod-eu" {
		t.Errorf("cluster = %q, want prod-eu (the load origin)", rep.Cluster)
	}
}

// finalBatch builds a shard's last batch, carrying its exit code.
func finalBatch(e *env, shard int, exitCode int) metrics.Batch {
	b := batch(e, shard, 1, 2)
	b.Final = true
	b.ExitCode = &exitCode
	return b
}

// The whole point of accumulating a run's measurements: once every shard has
// said it is done, the report exists on its own, without anyone calling Stop.
func TestIngest_NaturalCompletionFinalizesTheReport(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	ctx := context.Background()

	if err := e.svc.Ingest(ctx, finalBatch(e, 0, 0)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	rep, err := e.reports.GetReport(ctx, e.runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.ExecutionID != e.executionID || rep.RunID != e.runID {
		t.Fatalf("report identity = %+v", rep)
	}
	if rep.Outcome != taurus.OutcomePassed {
		t.Errorf("outcome = %q, want passed", rep.Outcome)
	}
	if rep.ScenarioID != e.scenarioIDs[0] {
		t.Errorf("scenario id = %d, want %d (single scenario)", rep.ScenarioID, e.scenarioIDs[0])
	}
	if rep.Achieved.Samples == 0 {
		t.Error("report has no achieved samples")
	}

	states, err := e.progress.ShardStates(ctx, e.runID)
	if err != nil {
		t.Fatalf("ShardStates: %v", err)
	}
	if len(states) != 0 {
		t.Errorf("working state survived finalisation: %+v", states)
	}
}

// A sharded run's outcome is the most severe of its shards': one shard's
// criteria failure means the target failed, no matter that the other passed.
func TestIngest_NaturalCompletionCombinesShardExitCodes(t *testing.T) {
	t.Parallel()
	e := setup(t, 2) // one scenario, two shards
	ctx := context.Background()

	if err := e.svc.Ingest(ctx, finalBatch(e, 0, 0)); err != nil {
		t.Fatalf("Ingest shard 0: %v", err)
	}
	if err := e.svc.Ingest(ctx, finalBatch(e, 1, 3)); err != nil {
		t.Fatalf("Ingest shard 1: %v", err)
	}

	rep, err := e.reports.GetReport(ctx, e.runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.Outcome != taurus.OutcomeFailed {
		t.Errorf("outcome = %q, want failed", rep.Outcome)
	}
}

// A shard torn down before bzt could write its exit code is inconclusive, not
// simply absent from the rollup -- otherwise a run where one shard vanished
// and the rest passed would be reported as a clean pass.
func TestIngest_NaturalCompletionTreatsAMissingExitCodeAsInconclusive(t *testing.T) {
	t.Parallel()
	e := setup(t, 2) // one scenario, two shards
	ctx := context.Background()

	if err := e.svc.Ingest(ctx, finalBatch(e, 0, 0)); err != nil {
		t.Fatalf("Ingest shard 0: %v", err)
	}
	missing := batch(e, 1, 1)
	missing.Final = true // no ExitCode: torn down before it could write one
	if err := e.svc.Ingest(ctx, missing); err != nil {
		t.Fatalf("Ingest shard 1: %v", err)
	}

	rep, err := e.reports.GetReport(ctx, e.runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.Outcome != taurus.OutcomeError {
		t.Errorf("outcome = %q, want error (shard 1 never reported an exit code)", rep.Outcome)
	}
}

// An execution can bundle several scenarios under one run, each deployed as
// its own StatefulSet whose shard ordinals start again at 0. Both scenarios'
// shard 0 must accumulate independently, not collide on the same working
// state -- reproduced here through real Ingest calls, deliberately using the
// same stream id for both, which is exactly what made them look like the same
// pod restarting before scenario was part of the key.
func TestIngest_TwoScenariosShard0DoNotCollide(t *testing.T) {
	t.Parallel()
	e := setup(t, 1, 1) // two scenarios, one shard each
	ctx := context.Background()

	code := 0
	for _, scenarioID := range e.scenarioIDs {
		e.seq++
		b := metrics.Batch{
			ExecutionID: e.executionID, ScenarioID: scenarioID, RunID: e.runID,
			ShardIndex: 0, StreamID: "s1", Final: true, ExitCode: &code,
			Intervals: []metrics.Interval{{
				Seq: e.seq, Timestamp: 1000, Label: "checkout-cart",
				Concurrency: 5, Samples: 10, Succeeded: 10,
				Latency: metrics.Histogram{0.01: 10},
			}},
		}
		if err := e.svc.Ingest(ctx, b); err != nil {
			t.Fatalf("Ingest scenario %d: %v", scenarioID, err)
		}
	}

	rep, err := e.reports.GetReport(ctx, e.runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.Outcome != taurus.OutcomePassed {
		t.Errorf("outcome = %q, want passed", rep.Outcome)
	}
	if rep.Achieved.Samples != 20 {
		t.Errorf("achieved samples = %d, want 20 (10 from each scenario's shard 0)", rep.Achieved.Samples)
	}
}

// A run does not finalise until every shard its load profile called for has
// finished -- one shard's Final must not be mistaken for the whole run's.
func TestIngest_DoesNotFinalizeUntilEveryShardIsDone(t *testing.T) {
	t.Parallel()
	e := setup(t, 2)
	ctx := context.Background()

	if err := e.svc.Ingest(ctx, finalBatch(e, 0, 0)); err != nil {
		t.Fatalf("Ingest shard 0: %v", err)
	}
	if _, err := e.reports.GetReport(ctx, e.runID); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("GetReport before every shard finished = %v, want ErrNotFound", err)
	}
}

// Natural completion and a later Honryu-initiated Finalize (teardown, arriving
// after the fact) race to finalise the same run. Whichever gets there first
// decides its outcome, and the report must survive the race unedited.
func TestFinalize_DoesNotOverwriteANaturallyCompletedReport(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	ctx := context.Background()

	if err := e.svc.Ingest(ctx, finalBatch(e, 0, 0)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if err := e.svc.Finalize(ctx, e.executionID, e.runID); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	rep, err := e.reports.GetReport(ctx, e.runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.Outcome != taurus.OutcomePassed {
		t.Errorf("outcome = %q, want the natural completion's passed -- Finalize must not overwrite it with aborted", rep.Outcome)
	}
}

// A shard can finish naturally, with a real bzt exit code, in the instant
// before Stop reaches the same run -- allShardsFinished only fires once every
// shard has, so the run is still open for Finalize to race with. That
// shard's evidence must not be silently discarded just because Stop got
// there: a criteria failure or an engine error is more severe than an
// ordinary abort and must still surface.
func TestFinalize_SurfacesAFailureFromAShardThatAlreadyFinished(t *testing.T) {
	t.Parallel()
	e := setup(t, 2) // one scenario, two shards
	ctx := context.Background()

	if err := e.svc.Ingest(ctx, finalBatch(e, 0, 3)); err != nil { // criteria failed
		t.Fatalf("Ingest shard 0: %v", err)
	}

	if err := e.svc.Finalize(ctx, e.executionID, e.runID); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	rep, err := e.reports.GetReport(ctx, e.runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.Outcome != taurus.OutcomeFailed {
		t.Errorf("outcome = %q, want failed (shard 0 already reported a real criteria failure)", rep.Outcome)
	}
}

// The reverse must also hold: a shard that happened to finish cleanly before
// Stop reached the run does not make the report say "passed" -- the run was
// still stopped before every shard could, and that is what the outcome must
// say regardless of what the lucky shard measured.
func TestFinalize_DoesNotDowngradeAbortedToPassed(t *testing.T) {
	t.Parallel()
	e := setup(t, 2)
	ctx := context.Background()

	if err := e.svc.Ingest(ctx, finalBatch(e, 0, 0)); err != nil { // passed cleanly
		t.Fatalf("Ingest shard 0: %v", err)
	}

	if err := e.svc.Finalize(ctx, e.executionID, e.runID); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	rep, err := e.reports.GetReport(ctx, e.runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.Outcome != taurus.OutcomeAborted {
		t.Errorf("outcome = %q, want aborted (the run was still stopped, whatever shard 0 measured)", rep.Outcome)
	}
}

// An execution can bundle several scenarios under one run. ScenarioID is only
// unambiguous when there is exactly one; the per-request label breakdown
// already covers the rest.
func TestFinalize_MultiScenarioLeavesScenarioIDUnset(t *testing.T) {
	t.Parallel()
	e := setup(t, 1, 1) // two scenarios, one shard each
	ctx := context.Background()

	if err := e.svc.Finalize(ctx, e.executionID, e.runID); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	rep, err := e.reports.GetReport(ctx, e.runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.ScenarioID != 0 {
		t.Errorf("scenario id = %d, want 0 (ambiguous across %d scenarios)", rep.ScenarioID, len(e.scenarioIDs))
	}
	if rep.Outcome != taurus.OutcomeAborted {
		t.Errorf("outcome = %q, want aborted", rep.Outcome)
	}
}

// Concurrency sums exactly as usage accounting already collapses a profile
// (run.VirtualUsers); throughput sums since each scenario's rate is additive;
// duration takes the longest, since the run lasts as long as its longest
// scenario.
func TestFinalize_RequestedLoadCollapsesMultipleScenarios(t *testing.T) {
	t.Parallel()
	e := setup(t, 2, 3) // two scenarios: 2 engines and 3 engines
	ctx := context.Background()

	profile := []loadprofile.Entry{
		{ScenarioID: e.scenarioIDs[0], Concurrency: 10, Engines: 2, Rampup: 1, Duration: 30, Throughput: 100},
		{ScenarioID: e.scenarioIDs[1], Concurrency: 5, Engines: 3, Rampup: 1, Duration: 90, Throughput: 50},
	}
	if err := e.store.StoreLoadProfile(ctx, e.executionID, false, profile); err != nil {
		t.Fatalf("StoreLoadProfile: %v", err)
	}

	if err := e.svc.Finalize(ctx, e.executionID, e.runID); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	rep, err := e.reports.GetReport(ctx, e.runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	// VirtualUsers: 10*2 + 5*3 = 35.
	if rep.Requested.Concurrency != 35 {
		t.Errorf("requested concurrency = %d, want 35", rep.Requested.Concurrency)
	}
	if rep.Requested.Throughput != 150 {
		t.Errorf("requested throughput = %v, want 150", rep.Requested.Throughput)
	}
	if rep.Requested.DurationSeconds != 90 {
		t.Errorf("requested duration = %d, want 90 (the longer scenario)", rep.Requested.DurationSeconds)
	}
}

func TestFinalize_PropagatesAReportStoreSaveFailure(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	e.reports.SaveErr = errors.New("boom")

	if err := e.svc.Finalize(context.Background(), e.executionID, e.runID); err == nil {
		t.Error("Finalize succeeded despite SaveReport failing")
	}
}

// A retry after Discard fails must retry Discard, not skip it because the
// report already exists. Before finalize stopped guarding on "does a report
// already exist", that early return meant a Discard failure orphaned a run's
// working state permanently: any retry saw the report already saved and
// returned before ever calling Discard again.
func TestFinalize_RetryAfterDiscardFailureStillDiscards(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	ctx := context.Background()
	// Seed some working state, so there is something for Discard to actually
	// need to clean up.
	if err := e.svc.Ingest(ctx, batch(e, 0, 1)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	e.progress.DiscardErr = errors.New("boom")

	if err := e.svc.Finalize(context.Background(), e.executionID, e.runID); err == nil {
		t.Fatal("Finalize succeeded despite Discard failing")
	}
	rep, err := e.reports.GetReport(context.Background(), e.runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.Outcome != taurus.OutcomeAborted {
		t.Fatalf("report was not saved before Discard failed: %+v", rep)
	}

	e.progress.DiscardErr = nil
	if err := e.svc.Finalize(context.Background(), e.executionID, e.runID); err != nil {
		t.Fatalf("retried Finalize: %v, want Discard to be retried and succeed", err)
	}
	if states, _ := e.progress.ShardStates(context.Background(), e.runID); len(states) != 0 {
		t.Errorf("working state survived the retried Discard: %+v", states)
	}
}

func TestFinalize_UnknownRunPropagatesRunHistoryError(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)

	if err := e.svc.Finalize(context.Background(), e.executionID, e.runID+999); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("Finalize(unknown run) = %v, want ErrNotFound from RunHistory", err)
	}
}

// The clock a report is stamped with is injectable, so a test can assert on it
// rather than a moving target.
func TestFinalize_UsesTheInjectedClock(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	fixed := time.Unix(5000, 0).UTC()
	e.svc.WithNow(func() time.Time { return fixed })

	if err := e.svc.Finalize(context.Background(), e.executionID, e.runID); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	rep, err := e.reports.GetReport(context.Background(), e.runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if !rep.EndedAt.Equal(fixed) {
		t.Errorf("EndedAt = %v, want %v", rep.EndedAt, fixed)
	}
}

// The report's correlation id is the run's own, read from its history row --
// NOT the execution's pending value, which by finalize time can already point
// at a later deploy. Engine and Cluster already accept that imprecision; for
// the correlation id it would be load-bearing: a report showing a later
// deploy's id deep-links a reader into the wrong run's traffic.
func TestFinalize_CorrelationIDComesFromTheRunNotTheExecution(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	ctx := context.Background()

	const (
		thisRunsID      = "11111111111111111111111111111111" // the deploy that started this run minted
		aLaterDeploysID = "22222222222222222222222222222222" // a redeploy between run and finalize
	)

	// Replace setup()'s blank run with one started under a real correlation id.
	if err := e.store.StopRun(ctx, e.executionID); err != nil {
		t.Fatalf("StopRun(setup's run): %v", err)
	}
	runID, err := e.store.StartRun(ctx, e.executionID, thisRunsID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// Between this run starting and its report finalizing, the execution is
	// re-deployed: the pending id moves on to the new deploy's.
	if err := e.store.SetPendingCorrelationID(ctx, e.executionID, aLaterDeploysID); err != nil {
		t.Fatalf("SetPendingCorrelationID: %v", err)
	}

	if err := e.svc.Finalize(ctx, e.executionID, runID); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	rep, err := e.reports.GetReport(ctx, runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.CorrelationID != thisRunsID {
		t.Fatalf("report correlation = %q, want this run's own %q (not the pending %q)", rep.CorrelationID, thisRunsID, aLaterDeploysID)
	}
}

// recorder is a Notifier that records what it was asked to announce, and
// how many times -- the whole surface the completion hook needs from the
// webhook use-case's side.
type recorder struct {
	calls []recordedRun
}

type recordedRun struct {
	projectID int64
	runID     int64
}

func (r *recorder) RunCompleted(_ context.Context, projectID int64, rep report.Report) {
	r.calls = append(r.calls, recordedRun{projectID: projectID, runID: rep.RunID})
}

// A completed run announces itself through the Notifier with the
// execution's project (webhooks are registered per project) and the report
// exactly as stored -- the notification's payload IS the saved report.
func TestFinalize_AnnouncesCompletionThroughTheNotifier(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	rec := &recorder{}
	e.svc.WithNotifier(rec)
	ctx := context.Background()

	if err := e.svc.Finalize(ctx, e.executionID, e.runID); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("notifier saw %d calls, want 1", len(rec.calls))
	}
	// setup() builds its execution under project 1.
	if rec.calls[0].projectID != 1 {
		t.Errorf("notified project %d, want 1 (the execution's own)", rec.calls[0].projectID)
	}
	if rec.calls[0].runID != e.runID {
		t.Errorf("notified run %d, want %d", rec.calls[0].runID, e.runID)
	}
	// The announcement happens after the report is durable: whatever the
	// notification carries must be readable from the store.
	if _, err := e.reports.GetReport(ctx, e.runID); err != nil {
		t.Fatalf("GetReport after notify: %v", err)
	}
}

// A run finalised twice (natural completion racing a later Stop, or the
// orphan sweep arriving after either) notifies once: the second SaveReport
// is a no-op that keeps the first verdict, and re-announcing would tell a
// receiver the same run "completed" twice with different outcomes.
func TestFinalize_DoesNotReannounceAnAlreadyFinalisedRun(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	rec := &recorder{}
	e.svc.WithNotifier(rec)
	ctx := context.Background()

	if err := e.svc.Finalize(ctx, e.executionID, e.runID); err != nil {
		t.Fatalf("first Finalize: %v", err)
	}
	if err := e.svc.Finalize(ctx, e.executionID, e.runID); err != nil {
		t.Fatalf("second Finalize: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("notifier saw %d calls, want 1 (first finalisation only)", len(rec.calls))
	}
}

// A report that could not be saved is never announced: the notification
// says a report exists, so it must not precede the report.
func TestFinalize_NoAnnouncementWhenSaveReportFails(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	rec := &recorder{}
	e.svc.WithNotifier(rec)
	e.reports.SaveErr = errors.New("store down")
	ctx := context.Background()

	if err := e.svc.Finalize(ctx, e.executionID, e.runID); err == nil {
		t.Fatal("Finalize = nil, want the store error")
	}
	if len(rec.calls) != 0 {
		t.Errorf("notifier saw %d calls, want none (nothing was saved)", len(rec.calls))
	}
}

// A run that completes naturally must close its own run marker. Before it
// did, teardown was the only StopRun caller: a run that finished on its own
// kept an open execution_run row forever (phase 43's wedge -- six executions
// stuck PhaseRunning, every later Trigger 409ing on the corpse marker),
// because the abandoned-run sweep skips runs that already have reports.
func TestIngest_NaturalCompletionClosesTheRunMarker(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	ctx := context.Background()

	if err := e.svc.Ingest(ctx, finalBatch(e, 0, 0)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if _, running, _ := e.store.CurrentRun(ctx, e.executionID); running {
		t.Fatal("natural completion left the run marker open")
	}
	// Closed means end_time stamped too, not just the pointer row gone: the
	// history row is the run's permanent record of how long it stood.
	hist, err := e.store.RunHistory(ctx, e.runID)
	if err != nil {
		t.Fatalf("RunHistory: %v", err)
	}
	if hist.EndTime == nil {
		t.Error("natural completion left the run's history end_time unstamped")
	}
}

// Teardown calls Finalize and then StopRun itself, and the abandoned-run
// sweep does the same -- so after finalize closes the marker, a second
// StopRun must be a quiet no-op, not an error. StopRun's own not-found guard
// (CurrentRun returns ok=false once the marker is gone) is what makes the
// double close safe; this pins it.
func TestFinalize_DoubleCloseIsSafe(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	ctx := context.Background()

	if err := e.svc.Ingest(ctx, finalBatch(e, 0, 0)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	// The lifecycle side of the race: teardown's Finalize arrives after
	// natural completion already closed the marker.
	if err := e.svc.Finalize(ctx, e.executionID, e.runID); err != nil {
		t.Fatalf("Finalize after natural completion: %v", err)
	}
	// ...and then teardown/sweep calls StopRun on its own, over a marker
	// finalize already closed.
	if err := e.store.StopRun(ctx, e.executionID); err != nil {
		t.Fatalf("StopRun after finalize closed the marker: %v", err)
	}
	if _, running, _ := e.store.CurrentRun(ctx, e.executionID); running {
		t.Fatal("marker reopened by the double close")
	}
}

// The marker can rotate while a finalize is in flight: teardown finalizes
// run A while a redeploy has already stopped A and started run B. StopRun
// deletes by execution id, so closing blindly would take B's marker down
// with it and wedge B the same way A was wedged. The closer compares the
// current run id first and leaves a marker that is not the finalized run's.
func TestFinalize_DoesNotCloseANewerRunsMarker(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	ctx := context.Background()

	// The marker rotated past setup()'s run: it was stopped and a new run
	// started, the state a redeploy-during-finalize leaves behind.
	oldRun := e.runID
	if err := e.store.StopRun(ctx, e.executionID); err != nil {
		t.Fatalf("StopRun(setup's run): %v", err)
	}
	newRun, err := e.store.StartRun(ctx, e.executionID, "")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// The stale finalize for the old run still completes -- its report is
	// real and must be written -- but must not touch the new run's marker.
	if err := e.svc.Finalize(ctx, e.executionID, oldRun); err != nil {
		t.Fatalf("Finalize(stale run): %v", err)
	}
	if _, err := e.reports.GetReport(ctx, oldRun); err != nil {
		t.Fatalf("stale run's report: %v", err)
	}
	current, running, _ := e.store.CurrentRun(ctx, e.executionID)
	if !running || current != newRun {
		t.Fatalf("current run = %d, %v; want run %d still open (a newer run's marker is not finalize's to close)", current, running, newRun)
	}
	if hist, _ := e.store.RunHistory(ctx, newRun); hist.EndTime != nil {
		t.Error("stale finalize stamped end_time on the new run's history")
	}
}

// Finalise is natural completion's shared exit, so it owns the idle-clock
// stamp that makes "purge-on-finalize" implicit: a run that finished on its
// own keeps its engines warm exactly one TTL, then the reaper takes them.
// The stamp must happen even when a report already existed (the wedged-run
// heal), because a heal can be the last lifecycle event for hours.
func TestFinalize_StampsEngineActivity(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	ctx := context.Background()

	if err := e.svc.Finalize(ctx, e.executionID, e.runID); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	last, ok, err := e.store.LastActivity(ctx, e.executionID)
	if err != nil || !ok {
		t.Fatalf("LastActivity after Finalize = %v,%v; want true,nil", ok, err)
	}
	if last.IsZero() {
		t.Fatal("LastActivity stamp is zero")
	}

	// A second finalize (the idempotent re-entry) still stamps: whatever
	// finished the run, the engines are provably not in use as of now.
	before := e.store.TouchActivityCount(e.executionID)
	if err := e.svc.Finalize(ctx, e.executionID, e.runID); err != nil {
		t.Fatalf("Finalize (again): %v", err)
	}
	if got := e.store.TouchActivityCount(e.executionID); got != before+1 {
		t.Fatalf("activity touches after second Finalize = %d, want %d", got, before+1)
	}
}
