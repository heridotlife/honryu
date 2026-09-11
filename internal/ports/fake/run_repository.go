package fake

import (
	"context"
	"sort"
	"time"

	"github.com/heridotlife/honryu/internal/ports"
)

// StartRun creates the active run for an execution, opening a history row that
// carries the deploy's correlation id.
func (s *Store) StartRun(_ context.Context, executionID int64, correlationID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.currentRun[executionID]; ok {
		return 0, ports.ErrRunActive
	}
	s.runSeq++
	runID := s.runSeq
	s.currentRun[executionID] = runID
	s.runHistory[runID] = &ports.RunRecord{
		RunID: runID, ExecutionID: executionID, StartedTime: s.now(), CorrelationID: correlationID,
	}
	return runID, nil
}

// CurrentRun returns the active run id for an execution.
func (s *Store) CurrentRun(_ context.Context, executionID int64) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runID, ok := s.currentRun[executionID]
	return runID, ok, nil
}

// OpenRuns lists every execution's active run with its start time, oldest
// first, mirroring the mysql adapter's history join.
func (s *Store) OpenRuns(_ context.Context) ([]ports.OpenRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ports.OpenRun, 0, len(s.currentRun))
	for executionID, runID := range s.currentRun {
		or := ports.OpenRun{ExecutionID: executionID, RunID: runID}
		if rec := s.runHistory[runID]; rec != nil {
			or.StartedTime = rec.StartedTime
		}
		out = append(out, or)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedTime.Equal(out[j].StartedTime) {
			return out[i].StartedTime.Before(out[j].StartedTime)
		}
		return out[i].RunID < out[j].RunID
	})
	return out, nil
}

// StopRun clears the active run and stamps its history end time.
func (s *Store) StopRun(_ context.Context, executionID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	runID, ok := s.currentRun[executionID]
	if !ok {
		return nil
	}
	if rec := s.runHistory[runID]; rec != nil && rec.EndTime == nil {
		end := s.now()
		rec.EndTime = &end
	}
	delete(s.currentRun, executionID)
	return nil
}

// RunHistory returns a run's history record.
func (s *Store) RunHistory(_ context.Context, runID int64) (ports.RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.runHistory[runID]
	if !ok {
		return ports.RunRecord{}, ports.ErrNotFound
	}
	return *rec, nil
}

// MarkScenarioRunning records a running scenario (idempotent).
func (s *Store) MarkScenarioRunning(_ context.Context, executionID, scenarioID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.MarkRunningErr != nil {
		return s.MarkRunningErr
	}
	scenarios, ok := s.running[executionID]
	if !ok {
		scenarios = map[int64]time.Time{}
		s.running[executionID] = scenarios
	}
	if _, exists := scenarios[scenarioID]; !exists {
		scenarios[scenarioID] = s.now()
	}
	return nil
}

// ClearScenarioRunning removes a running scenario marker (idempotent).
func (s *Store) ClearScenarioRunning(_ context.Context, executionID, scenarioID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running[executionID], scenarioID)
	if len(s.running[executionID]) == 0 {
		delete(s.running, executionID)
	}
	return nil
}

// RunningScenarios lists every running scenario across executions.
func (s *Store) RunningScenarios(_ context.Context) ([]ports.RunningScenario, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ports.RunningScenario
	for executionID, scenarios := range s.running {
		for scenarioID, started := range scenarios {
			out = append(out, ports.RunningScenario{ExecutionID: executionID, ScenarioID: scenarioID, StartedTime: started})
		}
	}
	sortRunningScenarios(out)
	return out, nil
}

// RunningScenariosByExecution lists running scenarios for one execution.
func (s *Store) RunningScenariosByExecution(_ context.Context, executionID int64) ([]ports.RunningScenario, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ports.RunningScenario
	for scenarioID, started := range s.running[executionID] {
		out = append(out, ports.RunningScenario{ExecutionID: executionID, ScenarioID: scenarioID, StartedTime: started})
	}
	sortRunningScenarios(out)
	return out, nil
}

// LastRun returns the most recently started run for an execution, breaking
// start-time ties by run id so a same-instant pair resolves deterministically.
func (s *Store) LastRun(_ context.Context, executionID int64) (ports.RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest *ports.RunRecord
	for _, rec := range s.runHistory {
		if rec.ExecutionID != executionID {
			continue
		}
		if latest == nil || rec.StartedTime.After(latest.StartedTime) ||
			(rec.StartedTime.Equal(latest.StartedTime) && rec.RunID > latest.RunID) {
			latest = rec
		}
	}
	if latest == nil {
		return ports.RunRecord{}, ports.ErrNotFound
	}
	return *latest, nil
}

func sortRunningScenarios(rps []ports.RunningScenario) {
	sort.Slice(rps, func(i, j int) bool {
		if rps[i].ExecutionID != rps[j].ExecutionID {
			return rps[i].ExecutionID < rps[j].ExecutionID
		}
		return rps[i].ScenarioID < rps[j].ScenarioID
	})
}
