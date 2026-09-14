package repositorytest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/ports"
)

// NewCalibrationJobRepo builds a fresh, empty CalibrationJobRepository for
// one test.
type NewCalibrationJobRepo func(t *testing.T) ports.CalibrationJobRepository

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
