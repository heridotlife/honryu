package fake

import (
	"context"
	"sort"
	"time"

	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/ports"
)

// CreateCalibrationJob persists a fresh job (PhasePending) for executionID,
// recording scenarioID as the scenario the search calibrates (0 stores NULL
// -- unknown), and returns its assigned ID.
func (s *Store) CreateCalibrationJob(_ context.Context, executionID, scenarioID int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calibrationJobSeq++
	job := ports.CalibrationJob{
		ID: s.calibrationJobSeq, ExecutionID: executionID, ScenarioID: scenarioID,
		Phase: calibration.PhasePending, CreatedTime: s.now(),
	}
	s.calibrationJobs[job.ID] = job
	return job.ID, nil
}

// GetCalibrationJob returns the job with id, or ports.ErrNotFound.
func (s *Store) GetCalibrationJob(_ context.Context, id int64) (ports.CalibrationJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.calibrationJobs[id]
	if !ok {
		return ports.CalibrationJob{}, ports.ErrNotFound
	}
	return job, nil
}

// ListCalibrationJobsByExecution returns every job ever run for
// executionID, most recent first.
func (s *Store) ListCalibrationJobsByExecution(_ context.Context, executionID int64) ([]ports.CalibrationJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ports.CalibrationJob{}
	for _, job := range s.calibrationJobs {
		if job.ExecutionID == executionID {
			out = append(out, job)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// ListCalibrationJobsByProject returns the project's calibration jobs whose
// created_time falls in [start, end), most recent first, each carrying its
// scenario's name ("" when the job's scenario is the unknown marker or no
// longer exists) -- the same join the MySQL adapter performs, derived here
// from the store's own execution and scenario maps.
func (s *Store) ListCalibrationJobsByProject(_ context.Context, projectID int64, start, end time.Time) ([]ports.CalibrationJobSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ports.CalibrationJobSummary{}
	for _, job := range s.calibrationJobs {
		exe, ok := s.executions[job.ExecutionID]
		if !ok || exe.ProjectID != projectID {
			continue
		}
		if job.CreatedTime.Before(start) || !job.CreatedTime.Before(end) {
			continue
		}
		summary := ports.CalibrationJobSummary{
			ID: job.ID, ExecutionID: job.ExecutionID, ScenarioID: job.ScenarioID,
			Phase: job.Phase, Result: job.Result, FailureReason: job.FailureReason,
			CreatedTime: job.CreatedTime,
		}
		if scenario, ok := s.scenarios[job.ScenarioID]; ok {
			summary.ScenarioName = scenario.Name
		}
		out = append(out, summary)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// ClaimNextStep locks and returns one non-terminal job whose claim has
// expired, or found=false if none is due.
func (s *Store) ClaimNextStep(_ context.Context, now time.Time, leaseFor time.Duration) (ports.CalibrationJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var candidates []ports.CalibrationJob
	for _, job := range s.calibrationJobs {
		if job.Phase != calibration.PhasePending && job.Phase != calibration.PhaseBracketing && job.Phase != calibration.PhaseBisecting {
			continue
		}
		claimedAt, claimed := s.calibrationClaimedAt[job.ID]
		if claimed && claimedAt.After(now.Add(-leaseFor)) {
			continue
		}
		candidates = append(candidates, job)
	}
	if len(candidates) == 0 {
		return ports.CalibrationJob{}, false, nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	job := candidates[0]
	s.calibrationClaimedAt[job.ID] = now
	return job, true, nil
}

// RecordStep appends step to jobID's history and replaces its persisted
// state with updated, clearing the claim.
func (s *Store) RecordStep(_ context.Context, jobID int64, step calibration.Step, updated ports.CalibrationJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.calibrationJobs[jobID]; !ok {
		return ports.ErrNotFound
	}
	s.calibrationSteps[jobID] = append(s.calibrationSteps[jobID], step)
	updated.ID = jobID
	s.calibrationJobs[jobID] = updated
	delete(s.calibrationClaimedAt, jobID)
	return nil
}

// StepsFor returns every step recorded for jobID, in the order taken.
func (s *Store) StepsFor(_ context.Context, jobID int64) ([]calibration.Step, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]calibration.Step(nil), s.calibrationSteps[jobID]...), nil
}

// MarkFailed ends jobID with PhaseFailed and reason, clearing the claim.
func (s *Store) MarkFailed(_ context.Context, jobID int64, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.calibrationJobs[jobID]
	if !ok {
		return ports.ErrNotFound
	}
	job.Phase = calibration.PhaseFailed
	job.FailureReason = reason
	s.calibrationJobs[jobID] = job
	delete(s.calibrationClaimedAt, jobID)
	return nil
}

// SetCalibrationBounds replaces whatever search bounds are recorded for
// executionID.
func (s *Store) SetCalibrationBounds(_ context.Context, executionID int64, bounds ports.CalibrationBounds) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calibrationBounds[executionID] = bounds
	return nil
}

// CalibrationBoundsFor returns the search bounds recorded for executionID,
// or ports.ErrNotFound if none have been configured.
func (s *Store) CalibrationBoundsFor(_ context.Context, executionID int64) (ports.CalibrationBounds, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.calibrationBounds[executionID]
	if !ok {
		return ports.CalibrationBounds{}, ports.ErrNotFound
	}
	return b, nil
}
