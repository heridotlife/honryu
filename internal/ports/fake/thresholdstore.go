package fake

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/heridotlife/honryu/internal/domain/threshold"
	"github.com/heridotlife/honryu/internal/ports"
)

// ThresholdStore is an in-memory ports.ThresholdStore for fast use-case
// tests.
type ThresholdStore struct {
	mu        sync.Mutex
	threshold map[int64]threshold.Threshold // by id
	results   map[int64][]threshold.Result  // by run id
	seq       int64

	// ListErr, when set, is returned by ListThresholdsForScenario.
	ListErr error
	// ReplaceErr, when set, is returned by ReplaceThresholds.
	ReplaceErr error
	// SaveResultsErr, when set, is returned by SaveThresholdResults.
	SaveResultsErr error
	// ResultsErr, when set, is returned by ThresholdResultsForRun.
	ResultsErr error
}

// NewThresholdStore builds an empty store.
func NewThresholdStore() *ThresholdStore {
	return &ThresholdStore{
		threshold: map[int64]threshold.Threshold{},
		results:   map[int64][]threshold.Result{},
	}
}

var _ ports.ThresholdStore = (*ThresholdStore)(nil)

// ListThresholdsForScenario returns the scenario's thresholds, oldest first.
func (s *ThresholdStore) ListThresholdsForScenario(_ context.Context, scenarioID int64) ([]threshold.Threshold, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ListErr != nil {
		return nil, s.ListErr
	}
	var out []threshold.Threshold
	for _, th := range s.threshold {
		if th.ScenarioID == scenarioID {
			out = append(out, th)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ReplaceThresholds diffs the incoming definitions against the stored set:
// verbatim-matching definitions keep their row identity, gone ones are
// deleted, new ones are inserted. Returns the stored set in caller order.
func (s *ThresholdStore) ReplaceThresholds(_ context.Context, scenarioID int64, defs []threshold.Threshold) ([]threshold.Threshold, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ReplaceErr != nil {
		return nil, s.ReplaceErr
	}
	// Pair each incoming definition with an existing row it matches
	// verbatim (existing rows consumed in id order, so pairing is
	// deterministic when duplicates exist).
	existing := make([]threshold.Threshold, 0)
	for _, th := range s.threshold {
		if th.ScenarioID == scenarioID {
			existing = append(existing, th)
		}
	}
	sort.Slice(existing, func(i, j int) bool { return existing[i].ID < existing[j].ID })
	matched := make(map[int64]bool) // existing ids reused
	kept := make([]threshold.Threshold, 0, len(defs))
	for _, def := range defs {
		def.ScenarioID = scenarioID
		reused := false
		for _, ex := range existing {
			if matched[ex.ID] {
				continue
			}
			if ex.Metric == def.Metric && ex.Comparison == def.Comparison && ex.Value == def.Value {
				matched[ex.ID] = true
				kept = append(kept, ex)
				reused = true
				break
			}
		}
		if !reused {
			s.seq++
			def.ID = s.seq
			def.CreatedTime = time.Now().UTC()
			s.threshold[def.ID] = def
			kept = append(kept, def)
		}
	}
	// Everything under this scenario that did not match is gone; its
	// results cascade with it.
	keptIDs := make(map[int64]bool, len(kept))
	for _, th := range kept {
		keptIDs[th.ID] = true
	}
	for _, ex := range existing {
		if !matched[ex.ID] {
			delete(s.threshold, ex.ID)
			for runID, rows := range s.results {
				keptRows := rows[:0]
				for _, r := range rows {
					if keptIDs[r.ThresholdID] {
						keptRows = append(keptRows, r)
					}
				}
				s.results[runID] = keptRows
			}
		}
	}
	out := make([]threshold.Threshold, len(kept))
	copy(out, kept)
	return out, nil
}

// SaveThresholdResults records one run's evaluation, replacing any previous
// results for the same runs.
func (s *ThresholdStore) SaveThresholdResults(_ context.Context, results []threshold.Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.SaveResultsErr != nil {
		return s.SaveResultsErr
	}
	byRun := make(map[int64][]threshold.Result)
	for _, r := range results {
		if r.EvaluatedAt.IsZero() {
			r.EvaluatedAt = time.Now().UTC()
		}
		byRun[r.RunID] = append(byRun[r.RunID], r)
	}
	for runID, rows := range byRun {
		s.results[runID] = rows
	}
	return nil
}

// ThresholdResultsForRun returns the run's stored results in insertion
// order. Always non-nil.
func (s *ThresholdStore) ThresholdResultsForRun(_ context.Context, runID int64) ([]threshold.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ResultsErr != nil {
		return nil, s.ResultsErr
	}
	rows := s.results[runID]
	out := make([]threshold.Result, len(rows))
	copy(out, rows)
	return out, nil
}
