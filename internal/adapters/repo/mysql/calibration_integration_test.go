//go:build integration

package mysql_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/repositorytest"
	"github.com/heridotlife/honryu/test/dbtest"
)

func TestMySQLCalibrationJobRepository_Contract(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repositorytest.RunCalibrationJobRepositoryContract(t, func(t *testing.T) repositorytest.CalibrationWorldRepo {
		truncateAll(t, db)
		return mysqladapter.NewRepository(db)
	})
}

func TestMySQLCalibrationBounds_SetAndGetRoundTripAndReplace(t *testing.T) {
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()

	if _, err := repo.CalibrationBoundsFor(ctx, 1); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("CalibrationBoundsFor(unconfigured) = %v, want ErrNotFound", err)
	}

	first := ports.CalibrationBounds{SeedQPS: 10, MaxQPS: 1000, MaxSteps: 20, HoldSeconds: 30}
	if err := repo.SetCalibrationBounds(ctx, 1, first); err != nil {
		t.Fatalf("SetCalibrationBounds: %v", err)
	}
	got, err := repo.CalibrationBoundsFor(ctx, 1)
	if err != nil {
		t.Fatalf("CalibrationBoundsFor: %v", err)
	}
	if got != first {
		t.Fatalf("CalibrationBoundsFor = %+v, want %+v", got, first)
	}

	second := ports.CalibrationBounds{SeedQPS: 5, MaxQPS: 500, MaxSteps: 10, HoldSeconds: 15}
	if err := repo.SetCalibrationBounds(ctx, 1, second); err != nil {
		t.Fatalf("SetCalibrationBounds (replace): %v", err)
	}
	got, err = repo.CalibrationBoundsFor(ctx, 1)
	if err != nil {
		t.Fatalf("CalibrationBoundsFor (after replace): %v", err)
	}
	if got != second {
		t.Fatalf("CalibrationBoundsFor (after replace) = %+v, want %+v", got, second)
	}
}

// More than one calibration controller replica may run concurrently (a
// dedicated cmd/calibrator alongside cmd/scheduler optionally hosting the
// same loop, or several cmd/calibrator replicas) -- this drives that race
// for real against MySQL's row locking rather than asserting on the SQL
// shape: N goroutines call ClaimNextStep at nearly the same instant against
// a single pending job, and exactly one must come back with found=true.
func TestMySQLCalibrationJobRepository_ClaimNextStep_ConcurrentClaimsExactlyOneWinner(t *testing.T) {
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()

	jobID, err := repo.CreateCalibrationJob(ctx, 1, 0)
	if err != nil {
		t.Fatalf("CreateCalibrationJob: %v", err)
	}

	const racers = 8
	now := time.Now()
	var wg sync.WaitGroup
	claimed := make([]bool, racers)
	claimedIDs := make([]int64, racers)
	errs := make([]error, racers)
	var start sync.WaitGroup
	start.Add(1)
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait() // maximise the chance every goroutine races at once
			job, found, err := repo.ClaimNextStep(ctx, now, time.Hour)
			claimed[i] = found
			claimedIDs[i] = job.ID
			errs[i] = err
		}(i)
	}
	start.Done()
	wg.Wait()

	wins := 0
	for i := range racers {
		if errs[i] != nil {
			t.Fatalf("racer %d: ClaimNextStep: %v", i, errs[i])
		}
		if claimed[i] {
			wins++
			if claimedIDs[i] != jobID {
				t.Errorf("racer %d claimed job %d, want %d", i, claimedIDs[i], jobID)
			}
		}
	}
	if wins != 1 {
		t.Fatalf("winners = %d, want exactly 1 -- the row lock must let only one racer claim the job", wins)
	}
}

// Once a step is recorded, the claim is cleared -- a still-in-progress job
// (Phase not yet Done/Failed) must become claimable again for its next step,
// proven against real MySQL rather than assumed from the SQL.
func TestMySQLCalibrationJobRepository_RecordStep_ReclaimableAfterwards(t *testing.T) {
	db := dbtest.StartMySQL(t)
	truncateAll(t, db)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()

	jobID, err := repo.CreateCalibrationJob(ctx, 1, 0)
	if err != nil {
		t.Fatalf("CreateCalibrationJob: %v", err)
	}
	if _, found, err := repo.ClaimNextStep(ctx, time.Now(), time.Hour); err != nil || !found {
		t.Fatalf("ClaimNextStep: found=%v, err=%v", found, err)
	}

	if err := repo.RecordStep(ctx, jobID,
		calibration.Step{RequestedQPS: 10, AchievedQPS: 10, Classification: calibration.ClassificationClean},
		ports.CalibrationJob{Phase: calibration.PhaseBracketing, StepCount: 1, BracketLoRequested: 10, BracketLoAchieved: 10, NextRequestedQPS: 20},
	); err != nil {
		t.Fatalf("RecordStep: %v", err)
	}

	job, found, err := repo.ClaimNextStep(ctx, time.Now(), time.Hour)
	if err != nil || !found || job.ID != jobID {
		t.Fatalf("re-claim after RecordStep = %+v, %v, %v, want the same job, true, nil", job, found, err)
	}
	if job.Phase != calibration.PhaseBracketing || job.NextRequestedQPS != 20 {
		t.Fatalf("re-claimed job = %+v, want the state RecordStep persisted", job)
	}
}
