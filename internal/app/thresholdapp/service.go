// Package thresholdapp is the scenario-threshold use-case: it administers the
// k6-style bounds an operator pins to one scenario, and grades a run's report
// against them when the report lands.
//
// The grading reads the stored report -- the same surface the SLO budget
// aggregation reads -- through the domain's Observe, which extracts each
// metric exactly as sloapp does (percentiles out of the merged-bucket map,
// seconds crossed to milliseconds once; the error rate as stored; the
// achieved throughput). There is no second accounting of what a run
// produced.
//
// Grading never changes a run's verdict: the engine's outcome stands, and
// threshold results are additive evidence attached to the run's report.
package thresholdapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/threshold"
	"github.com/heridotlife/honryu/internal/ports"
)

// Repo is the persistence the service needs: the threshold store itself, plus
// the scenario read that proves a threshold set belongs to a real scenario.
type Repo interface {
	ports.ThresholdStore
	GetScenario(ctx context.Context, id int64) (scenario.Scenario, error)
}

// Service implements the threshold use-cases.
type Service struct {
	repo Repo
	now  func() time.Time
}

// NewService wires a Service to its repository.
func NewService(repo Repo) *Service {
	return &Service{repo: repo, now: time.Now}
}

// WithNow overrides the clock an evaluation is stamped with. Returns the
// receiver for chaining.
func (s *Service) WithNow(now func() time.Time) *Service {
	if now != nil {
		s.now = now
	}
	return s
}

// List returns the scenario's thresholds, oldest first. Unknown scenario is
// ports.ErrNotFound, so the HTTP layer's usual 404 mapping applies.
func (s *Service) List(ctx context.Context, scenarioID int64) ([]threshold.Threshold, error) {
	if _, err := s.repo.GetScenario(ctx, scenarioID); err != nil {
		return nil, err
	}
	got, err := s.repo.ListThresholdsForScenario(ctx, scenarioID)
	if err != nil {
		return nil, err
	}
	if got == nil {
		got = []threshold.Threshold{}
	}
	return got, nil
}

// Replace swaps the scenario's threshold set for defs -- replace-all, the
// editor-save semantics: whatever the PUT body carries is the whole truth.
// Every definition is validated before the store is touched, and the store's
// diff keeps unchanged rows' identity (so their recorded results survive an
// idempotent re-save). Returns the stored set with assigned ids, caller
// order. Always non-nil.
func (s *Service) Replace(ctx context.Context, scenarioID int64, defs []threshold.Threshold) ([]threshold.Threshold, error) {
	if _, err := s.repo.GetScenario(ctx, scenarioID); err != nil {
		return nil, err
	}
	for i := range defs {
		defs[i].ScenarioID = scenarioID
		if err := defs[i].Validate(); err != nil {
			return nil, fmt.Errorf("threshold %d: %w", i+1, err)
		}
	}
	stored, err := s.repo.ReplaceThresholds(ctx, scenarioID, defs)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		stored = []threshold.Threshold{}
	}
	return stored, nil
}

// EvaluateRun grades a finalised run's report against its scenario's
// thresholds and stores the results. This is metricsapp's post-finalisation
// hook, reached only by the finalisation that won SaveReport's
// first-write-wins race.
//
// A report with no single owning scenario (an execution bundling several, so
// ScenarioID is the informational zero) grades nothing: thresholds are
// per-scenario, and a bundle's labels cannot be attributed to one. Unknown
// scenario and no-thresholds are both quiet no-ops -- a run predating its
// scenario's first threshold still finalises cleanly, with no results
// recorded, which the report surface reads as an empty (not null) list.
func (s *Service) EvaluateRun(ctx context.Context, rep report.Report) error {
	if rep.ScenarioID <= 0 {
		return nil
	}
	defs, err := s.repo.ListThresholdsForScenario(ctx, rep.ScenarioID)
	if err != nil {
		// An unknown scenario cannot happen for a report built from a real
		// profile row, but a quiet skip beats failing a finalised run's
		// bookkeeping on a dangling id.
		if errors.Is(err, ports.ErrNotFound) {
			return nil
		}
		return err
	}
	if len(defs) == 0 {
		return nil
	}
	now := s.now()
	results := make([]threshold.Result, 0, len(defs))
	for _, def := range defs {
		results = append(results, def.Evaluate(rep, now))
	}
	return s.repo.SaveThresholdResults(ctx, results)
}

// ResultsForRun returns a run's stored threshold results, evaluation order.
// Always non-nil: an empty list is "no thresholds graded this run", never a
// null the client must defend against.
func (s *Service) ResultsForRun(ctx context.Context, runID int64) ([]threshold.Result, error) {
	got, err := s.repo.ThresholdResultsForRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if got == nil {
		got = []threshold.Result{}
	}
	return got, nil
}
