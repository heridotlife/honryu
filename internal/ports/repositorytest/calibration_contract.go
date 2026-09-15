package repositorytest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

// CalibrationWorldRepo is the surface the by-project listing contract needs
// beyond the calibration ledger itself: a job's project comes from its
// execution and its display name from its scenario, so the contract seeds
// those rows the same way a real deployment has them
// (scenario_execution_contract's cross-aggregate precedent).
type CalibrationWorldRepo interface {
	ports.CalibrationJobRepository
	ports.ScenarioRepository
	ports.ExecutionRepository
}

// NewCalibrationJobRepo builds a fresh, empty CalibrationJobRepository --
// with enough of its world to seed scenarios and executions -- for one test.
type NewCalibrationJobRepo func(t *testing.T) CalibrationWorldRepo

// RunCalibrationJobRepositoryContract pins the behaviour every
// CalibrationJobRepository must share.
func RunCalibrationJobRepositoryContract(t *testing.T, newRepo NewCalibrationJobRepo) {
	t.Helper()

	t.Run("CreateAndGet", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		id, err := repo.CreateCalibrationJob(ctx, 42, 7)
		if err != nil {
			t.Fatalf("CreateCalibrationJob: %v", err)
		}
		if id <= 0 {
			t.Fatalf("CreateCalibrationJob id = %d, want > 0", id)
		}

		got, err := repo.GetCalibrationJob(ctx, id)
		if err != nil {
			t.Fatalf("GetCalibrationJob: %v", err)
		}
		if got.ID != id || got.ExecutionID != 42 {
			t.Fatalf("GetCalibrationJob = %+v, want id=%d execution_id=42", got, id)
		}
		if got.ScenarioID != 7 {
			t.Fatalf("ScenarioID = %d, want 7 round-tripped", got.ScenarioID)
		}

		// A 0 scenario is the legacy/unknown marker (the column's NULL),
		// stored and read back as 0 -- not an error.
		legacyID, err := repo.CreateCalibrationJob(ctx, 43, 0)
		if err != nil {
			t.Fatalf("CreateCalibrationJob(legacy): %v", err)
		}
		legacy, err := repo.GetCalibrationJob(ctx, legacyID)
		if err != nil {
			t.Fatalf("GetCalibrationJob(legacy): %v", err)
		}
		if legacy.ScenarioID != 0 {
			t.Fatalf("legacy ScenarioID = %d, want 0", legacy.ScenarioID)
		}
		if got.Phase != calibration.PhasePending {
			t.Fatalf("Phase = %q, want pending", got.Phase)
		}
		if got.StepCount != 0 || got.Result != nil {
			t.Fatalf("fresh job = %+v, want step_count=0 result=nil", got)
		}
		if got.CreatedTime.IsZero() {
			t.Fatal("CreatedTime is zero, want set")
		}
	})

	t.Run("GetMissingReturnsNotFound", func(t *testing.T) {
		repo := newRepo(t)
		if _, err := repo.GetCalibrationJob(context.Background(), 999); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("GetCalibrationJob(missing) = %v, want ErrNotFound", err)
		}
	})

	t.Run("ListCalibrationJobsByExecutionMostRecentFirstAndScoped", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		first, err := repo.CreateCalibrationJob(ctx, 1, 0)
		if err != nil {
			t.Fatalf("CreateCalibrationJob (first): %v", err)
		}
		second, err := repo.CreateCalibrationJob(ctx, 1, 0)
		if err != nil {
			t.Fatalf("CreateCalibrationJob (second): %v", err)
		}
		if _, err := repo.CreateCalibrationJob(ctx, 2, 0); err != nil {
			t.Fatalf("CreateCalibrationJob (other execution): %v", err)
		}

		got, err := repo.ListCalibrationJobsByExecution(ctx, 1)
		if err != nil {
			t.Fatalf("ListCalibrationJobsByExecution: %v", err)
		}
		if len(got) != 2 || got[0].ID != second || got[1].ID != first {
			t.Fatalf("ListCalibrationJobsByExecution(1) = %+v, want [second, first] most-recent-first", got)
		}
	})

	t.Run("ListCalibrationJobsByProjectScopesJoinsAndWindows", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		// Two projects, each with its own scenario and execution, plus a
		// second execution under project 1 carrying the legacy unknown
		// scenario (0/NULL) -- the name join must survive it.
		checkout, err := scenario.New("checkout", 1)
		if err != nil {
			t.Fatalf("scenario.New(checkout): %v", err)
		}
		checkoutID, err := repo.CreateScenario(ctx, checkout)
		if err != nil {
			t.Fatalf("CreateScenario(checkout): %v", err)
		}
		search, err := scenario.New("search", 2)
		if err != nil {
			t.Fatalf("scenario.New(search): %v", err)
		}
		searchID, err := repo.CreateScenario(ctx, search)
		if err != nil {
			t.Fatalf("CreateScenario(search): %v", err)
		}
		exeA1 := execution.Execution{Name: "calib-a1", ProjectID: 1, Engine: taurus.ExecutorK6}
		exeA1ID, err := repo.CreateExecution(ctx, exeA1)
		if err != nil {
			t.Fatalf("CreateExecution(a1): %v", err)
		}
		exeA2 := execution.Execution{Name: "calib-a2", ProjectID: 1, Engine: taurus.ExecutorJMeter}
		exeA2ID, err := repo.CreateExecution(ctx, exeA2)
		if err != nil {
			t.Fatalf("CreateExecution(a2): %v", err)
		}
		exeB1 := execution.Execution{Name: "calib-b1", ProjectID: 2, Engine: taurus.ExecutorK6}
		exeB1ID, err := repo.CreateExecution(ctx, exeB1)
		if err != nil {
			t.Fatalf("CreateExecution(b1): %v", err)
		}

		// Project 1 gets one finished search and one operational failure;
		// project 2 gets its own job that must never leak into 1's list.
		doneID, err := repo.CreateCalibrationJob(ctx, exeA1ID, checkoutID)
		if err != nil {
			t.Fatalf("CreateCalibrationJob(done): %v", err)
		}
		// The updated job is derived from the persisted one -- the only shape
		// the port's real caller (calibrationapp) ever records -- so identity
		// and bookkeeping fields like CreatedTime survive the replace.
		persisted, err := repo.GetCalibrationJob(ctx, doneID)
		if err != nil {
			t.Fatalf("GetCalibrationJob(done): %v", err)
		}
		finished := persisted
		finished.Phase = calibration.PhaseDone
		finished.StepCount = 1
		finished.BracketLoRequested = 10
		finished.BracketLoAchieved = 9.5
		finished.Result = &calibration.Result{SaturatedBy: calibration.SaturatedByNeither, PerPodQPS: 9.5}
		if err := repo.RecordStep(ctx, doneID,
			calibration.Step{RequestedQPS: 10, AchievedQPS: 9.5, Classification: calibration.ClassificationClean},
			finished); err != nil {
			t.Fatalf("RecordStep(done): %v", err)
		}
		failedID, err := repo.CreateCalibrationJob(ctx, exeA2ID, 0)
		if err != nil {
			t.Fatalf("CreateCalibrationJob(failed): %v", err)
		}
		if err := repo.MarkFailed(ctx, failedID, "step deploy failed"); err != nil {
			t.Fatalf("MarkFailed: %v", err)
		}
		if _, err := repo.CreateCalibrationJob(ctx, exeB1ID, searchID); err != nil {
			t.Fatalf("CreateCalibrationJob(other project): %v", err)
		}

		// A generous window around creation time: the contract pins scoping,
		// joining, and ordering, not clock precision (created_time is the
		// store's clock, and the MySQL column is second-grained).
		now := time.Now()
		start, end := now.Add(-time.Hour), now.Add(time.Hour)

		got, err := repo.ListCalibrationJobsByProject(ctx, 1, start, end)
		if err != nil {
			t.Fatalf("ListCalibrationJobsByProject(1): %v", err)
		}
		if len(got) != 2 || got[0].ID != failedID || got[1].ID != doneID {
			t.Fatalf("ListCalibrationJobsByProject(1) = %+v, want [failed, done] most-recent-first", got)
		}
		if got[0].Phase != calibration.PhaseFailed || got[0].FailureReason != "step deploy failed" || got[0].Result != nil {
			t.Fatalf("failed summary = %+v, want phase failed, reason carried, no result", got[0])
		}
		if got[0].ScenarioID != 0 || got[0].ScenarioName != "" {
			t.Fatalf("legacy summary scenario = (%d, %q), want (0, \"\")", got[0].ScenarioID, got[0].ScenarioName)
		}
		if got[1].ScenarioName != "checkout" {
			t.Fatalf("done summary scenario name = %q, want the joined \"checkout\"", got[1].ScenarioName)
		}
		if got[1].Result == nil || got[1].Result.PerPodQPS != 9.5 || got[1].Result.SaturatedBy != calibration.SaturatedByNeither {
			t.Fatalf("done summary result = %+v, want the terminal verdict round-tripped", got[1].Result)
		}

		gotB, err := repo.ListCalibrationJobsByProject(ctx, 2, start, end)
		if err != nil {
			t.Fatalf("ListCalibrationJobsByProject(2): %v", err)
		}
		if len(gotB) != 1 || gotB[0].ScenarioName != "search" {
			t.Fatalf("ListCalibrationJobsByProject(2) = %+v, want only project 2's own job", gotB)
		}

		// A strictly-past window holds no jobs: empty, and never nil -- the
		// digest serializes the slice verbatim, and a receiver renders an
		// empty array, not null.
		past, err := repo.ListCalibrationJobsByProject(ctx, 1, now.Add(-3*time.Hour), now.Add(-2*time.Hour))
		if err != nil {
			t.Fatalf("ListCalibrationJobsByProject(past): %v", err)
		}
		if past == nil || len(past) != 0 {
			t.Fatalf("ListCalibrationJobsByProject(past) = %#v, want empty non-nil", past)
		}
	})

	t.Run("ClaimNextStepReturnsAPendingJob", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id, err := repo.CreateCalibrationJob(ctx, 1, 0)
		if err != nil {
			t.Fatalf("CreateCalibrationJob: %v", err)
		}

		job, found, err := repo.ClaimNextStep(ctx, at(0), time.Hour)
		if err != nil {
			t.Fatalf("ClaimNextStep: %v", err)
		}
		if !found || job.ID != id {
			t.Fatalf("ClaimNextStep = %+v, %v, want the created job, true", job, found)
		}
	})

	t.Run("ClaimNextStepNoneDueIsNotAnError", func(t *testing.T) {
		repo := newRepo(t)
		_, found, err := repo.ClaimNextStep(context.Background(), at(0), time.Hour)
		if err != nil {
			t.Fatalf("ClaimNextStep: %v", err)
		}
		if found {
			t.Fatal("ClaimNextStep found = true, want false on an empty repo")
		}
	})

	// A claimed job is not claimable again until its lease expires -- the
	// mechanism that lets two controller replicas race ClaimNextStep
	// without ever driving the same job's step concurrently.
	t.Run("ClaimNextStepRespectsAnUnexpiredLease", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id, err := repo.CreateCalibrationJob(ctx, 1, 0)
		if err != nil {
			t.Fatalf("CreateCalibrationJob: %v", err)
		}

		if _, found, err := repo.ClaimNextStep(ctx, at(0), time.Hour); err != nil || !found {
			t.Fatalf("first claim = found:%v, err:%v, want true, nil", found, err)
		}
		if _, found, err := repo.ClaimNextStep(ctx, at(30), time.Hour); err != nil || found {
			t.Fatalf("second claim (lease unexpired) = found:%v, err:%v, want false, nil", found, err)
		}
		// Once the lease has expired, the same job becomes claimable again.
		job, found, err := repo.ClaimNextStep(ctx, at(0).Add(2*time.Hour), time.Hour)
		if err != nil || !found || job.ID != id {
			t.Fatalf("third claim (lease expired) = %+v, %v, %v, want the same job, true, nil", job, found, err)
		}
	})

	t.Run("ClaimNextStepExcludesDoneAndFailedJobs", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		doneID, err := repo.CreateCalibrationJob(ctx, 1, 0)
		if err != nil {
			t.Fatalf("CreateCalibrationJob (done): %v", err)
		}
		failedID, err := repo.CreateCalibrationJob(ctx, 1, 0)
		if err != nil {
			t.Fatalf("CreateCalibrationJob (failed): %v", err)
		}

		if err := repo.RecordStep(ctx, doneID,
			calibration.Step{RequestedQPS: 10, AchievedQPS: 10, Classification: calibration.ClassificationClean},
			ports.CalibrationJob{Phase: calibration.PhaseDone, Result: &calibration.Result{SaturatedBy: calibration.SaturatedByEngine, PerPodQPS: 10}},
		); err != nil {
			t.Fatalf("RecordStep: %v", err)
		}
		if err := repo.MarkFailed(ctx, failedID, "deploy failed"); err != nil {
			t.Fatalf("MarkFailed: %v", err)
		}

		_, found, err := repo.ClaimNextStep(ctx, at(0), time.Hour)
		if err != nil {
			t.Fatalf("ClaimNextStep: %v", err)
		}
		if found {
			t.Fatal("ClaimNextStep found a job, want none -- both are terminal")
		}
	})

	t.Run("RecordStepUpdatesStateAppendsHistoryAndClearsTheClaim", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id, err := repo.CreateCalibrationJob(ctx, 1, 0)
		if err != nil {
			t.Fatalf("CreateCalibrationJob: %v", err)
		}
		if _, found, err := repo.ClaimNextStep(ctx, at(0), time.Hour); err != nil || !found {
			t.Fatalf("ClaimNextStep: found=%v, err=%v", found, err)
		}

		step := calibration.Step{RequestedQPS: 10, AchievedQPS: 10, Classification: calibration.ClassificationClean}
		updated := ports.CalibrationJob{
			Phase: calibration.PhaseBracketing, StepCount: 1,
			BracketLoRequested: 10, BracketLoAchieved: 10, NextRequestedQPS: 20,
		}
		if err := repo.RecordStep(ctx, id, step, updated); err != nil {
			t.Fatalf("RecordStep: %v", err)
		}

		got, err := repo.GetCalibrationJob(ctx, id)
		if err != nil {
			t.Fatalf("GetCalibrationJob: %v", err)
		}
		if got.Phase != calibration.PhaseBracketing || got.StepCount != 1 || got.NextRequestedQPS != 20 {
			t.Fatalf("GetCalibrationJob after RecordStep = %+v, want the updated state", got)
		}
		if got.BracketLoRequested != 10 || got.BracketLoAchieved != 10 {
			t.Fatalf("bracket = %+v, want lo=10/10", got)
		}

		steps, err := repo.StepsFor(ctx, id)
		if err != nil {
			t.Fatalf("StepsFor: %v", err)
		}
		if len(steps) != 1 || steps[0] != step {
			t.Fatalf("StepsFor = %+v, want [%+v]", steps, step)
		}

		// The claim was cleared: a still-in-progress job is claimable again.
		if _, found, err := repo.ClaimNextStep(ctx, at(0), time.Hour); err != nil || !found {
			t.Fatalf("re-claim after RecordStep: found=%v, err=%v, want true, nil (claim was cleared)", found, err)
		}
	})

	t.Run("RecordStepStoresATerminalResult", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id, err := repo.CreateCalibrationJob(ctx, 1, 0)
		if err != nil {
			t.Fatalf("CreateCalibrationJob: %v", err)
		}

		result := &calibration.Result{SaturatedBy: calibration.SaturatedByTarget, PerPodQPS: 42.5}
		if err := repo.RecordStep(ctx, id,
			calibration.Step{RequestedQPS: 42.5, AchievedQPS: 42.5, Classification: calibration.ClassificationTargetSaturated},
			ports.CalibrationJob{Phase: calibration.PhaseDone, StepCount: 3, Result: result},
		); err != nil {
			t.Fatalf("RecordStep: %v", err)
		}

		got, err := repo.GetCalibrationJob(ctx, id)
		if err != nil {
			t.Fatalf("GetCalibrationJob: %v", err)
		}
		if got.Phase != calibration.PhaseDone || got.Result == nil {
			t.Fatalf("GetCalibrationJob = %+v, want done with a result", got)
		}
		if got.Result.SaturatedBy != calibration.SaturatedByTarget || got.Result.PerPodQPS != 42.5 {
			t.Fatalf("Result = %+v, want %+v", got.Result, result)
		}
	})

	t.Run("RecordStepMissingJobReturnsNotFound", func(t *testing.T) {
		repo := newRepo(t)
		err := repo.RecordStep(context.Background(), 999,
			calibration.Step{RequestedQPS: 1, AchievedQPS: 1, Classification: calibration.ClassificationClean},
			ports.CalibrationJob{Phase: calibration.PhaseBracketing})
		if !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("RecordStep(missing) = %v, want ErrNotFound", err)
		}
	})

	t.Run("StepsForEmptyUntilAnyRecorded", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id, err := repo.CreateCalibrationJob(ctx, 1, 0)
		if err != nil {
			t.Fatalf("CreateCalibrationJob: %v", err)
		}
		steps, err := repo.StepsFor(ctx, id)
		if err != nil {
			t.Fatalf("StepsFor: %v", err)
		}
		if len(steps) != 0 {
			t.Fatalf("StepsFor(no steps yet) = %+v, want none", steps)
		}
	})

	t.Run("MarkFailedSetsPhaseAndReasonAndClearsTheClaim", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id, err := repo.CreateCalibrationJob(ctx, 1, 0)
		if err != nil {
			t.Fatalf("CreateCalibrationJob: %v", err)
		}
		if _, found, err := repo.ClaimNextStep(ctx, at(0), time.Hour); err != nil || !found {
			t.Fatalf("ClaimNextStep: found=%v, err=%v", found, err)
		}

		if err := repo.MarkFailed(ctx, id, "deploy failed: image not found"); err != nil {
			t.Fatalf("MarkFailed: %v", err)
		}

		got, err := repo.GetCalibrationJob(ctx, id)
		if err != nil {
			t.Fatalf("GetCalibrationJob: %v", err)
		}
		if got.Phase != calibration.PhaseFailed || got.FailureReason != "deploy failed: image not found" {
			t.Fatalf("GetCalibrationJob = %+v, want failed with the reason", got)
		}

		if _, found, err := repo.ClaimNextStep(ctx, at(0), time.Hour); err != nil || found {
			t.Fatalf("ClaimNextStep after MarkFailed: found=%v, err=%v, want false, nil", found, err)
		}
	})

	t.Run("MarkFailedMissingJobReturnsNotFound", func(t *testing.T) {
		repo := newRepo(t)
		if err := repo.MarkFailed(context.Background(), 999, "boom"); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("MarkFailed(missing) = %v, want ErrNotFound", err)
		}
	})
}
