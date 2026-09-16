package ports

import (
	"context"

	"github.com/heridotlife/honryu/internal/domain/threshold"
)

// ThresholdStore persists a scenario's thresholds and the per-run results of
// grading reports against them.
//
// Thresholds are a projection of an operator's intent, not an aggregate with
// invariants spanning rows: the use-case layer validates every definition and
// owns replace-all semantics, the store records it. Results are per run: a
// later run of the same execution writes its own rows and never touches an
// earlier run's.
type ThresholdStore interface {
	// ListThresholdsForScenario returns the scenario's thresholds in
	// definition order (oldest first). Always non-nil; empty when the
	// scenario defines none.
	ListThresholdsForScenario(ctx context.Context, scenarioID int64) ([]threshold.Threshold, error)
	// ReplaceThresholds atomically swaps the scenario's threshold set for
	// the given definitions: definitions kept verbatim from the current set
	// retain their row identity (so results already recorded against them
	// survive an idempotent re-save), removed ones die (their results
	// cascade), new ones are inserted. Returns the stored set with
	// storage-assigned ids, in the caller's order.
	ReplaceThresholds(ctx context.Context, scenarioID int64, defs []threshold.Threshold) ([]threshold.Threshold, error)
	// SaveThresholdResults records one run's evaluation, replacing any
	// previous results for the same run (evaluation is idempotent: the same
	// report graded twice stores the same rows twice). The report row must
	// already exist -- results reference it.
	SaveThresholdResults(ctx context.Context, results []threshold.Result) error
	// ThresholdResultsForRun returns the run's stored results, definition
	// order preserved (the order evaluation produced them in). Always
	// non-nil; empty when the run's scenario defined no thresholds or the
	// run predates the feature.
	ThresholdResultsForRun(ctx context.Context, runID int64) ([]threshold.Result, error)
}
