// Package thresholdstoretest is the shared conformance suite every
// ThresholdStore must pass, fake and real alike.
package thresholdstoretest

import (
	"context"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/threshold"
	"github.com/heridotlife/honryu/internal/ports"
)

// NewStore builds a store with no thresholds in it.
type NewStore func(t *testing.T) ports.ThresholdStore

// Run exercises ThresholdStore behaviour.
func Run(t *testing.T, newStore NewStore) {
	t.Helper()
	ctx := context.Background()

	// mk builds a legal p95 ceiling for a scenario.
	mk := func(scenarioID int64, ms float64) threshold.Threshold {
		return threshold.Threshold{ScenarioID: scenarioID, Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: ms}
	}

	t.Run("ListEmptyIsNotNilAndScoped", func(t *testing.T) {
		s := newStore(t)
		got, err := s.ListThresholdsForScenario(ctx, 7)
		if err != nil {
			t.Fatalf("ListThresholdsForScenario: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("list = %d thresholds, want 0", len(got))
		}
	})

	t.Run("ReplaceAssignsIDsAndStampsAndListsInOrder", func(t *testing.T) {
		s := newStore(t)
		got, err := s.ReplaceThresholds(ctx, 7, []threshold.Threshold{
			mk(7, 300),
			{ScenarioID: 7, Metric: threshold.MetricErrorRate, Comparison: threshold.ComparisonLT, Value: 0.01},
		})
		if err != nil {
			t.Fatalf("ReplaceThresholds: %v", err)
		}
		if len(got) != 2 || got[0].ID <= 0 || got[1].ID <= 0 || got[0].ID == got[1].ID {
			t.Fatalf("replace = %+v, want two distinct storage-assigned ids in caller order", got)
		}
		if got[0].Value != 300 || got[1].Metric != threshold.MetricErrorRate {
			t.Errorf("stored definitions drifted: %+v", got)
		}
		for _, th := range got {
			if th.CreatedTime.IsZero() {
				t.Errorf("threshold %d has a zero created_time; the editor shows it", th.ID)
			}
		}
		listed, err := s.ListThresholdsForScenario(ctx, 7)
		if err != nil {
			t.Fatalf("ListThresholdsForScenario: %v", err)
		}
		if len(listed) != 2 || listed[0].ID != got[0].ID || listed[1].ID != got[1].ID {
			t.Errorf("list = %+v, want the replaced set in definition order", listed)
		}
	})

	t.Run("ReplaceIsIdempotentForIdenticalDefinitions", func(t *testing.T) {
		s := newStore(t)
		first, err := s.ReplaceThresholds(ctx, 7, []threshold.Threshold{mk(7, 300), mk(7, 500)})
		if err != nil {
			t.Fatalf("first replace: %v", err)
		}
		// The same set saved again keeps the same rows: an editor's
		// no-change save must not churn ids (and so must not orphan the
		// results already recorded against them).
		second, err := s.ReplaceThresholds(ctx, 7, []threshold.Threshold{mk(7, 300), mk(7, 500)})
		if err != nil {
			t.Fatalf("second replace: %v", err)
		}
		if len(second) != 2 || second[0].ID != first[0].ID || second[1].ID != first[1].ID {
			t.Errorf("idempotent replace = ids [%d %d], want [%d %d] preserved",
				second[0].ID, second[1].ID, first[0].ID, first[1].ID)
		}
	})

	t.Run("ReplaceRemovesGoneDefinitionsAndScopes", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.ReplaceThresholds(ctx, 7, []threshold.Threshold{mk(7, 300), mk(7, 500)}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// A scenario with its own set is untouched by scenario 7's replace.
		if _, err := s.ReplaceThresholds(ctx, 8, []threshold.Threshold{mk(8, 900)}); err != nil {
			t.Fatalf("other scenario: %v", err)
		}
		got, err := s.ReplaceThresholds(ctx, 7, []threshold.Threshold{mk(7, 250)})
		if err != nil {
			t.Fatalf("narrowing replace: %v", err)
		}
		if len(got) != 1 || got[0].Value != 250 {
			t.Errorf("replace = %+v, want exactly the new definition", got)
		}
		other, err := s.ListThresholdsForScenario(ctx, 8)
		if err != nil || len(other) != 1 || other[0].Value != 900 {
			t.Errorf("scenario 8's set = %+v (%v), want its own row untouched", other, err)
		}
	})

	t.Run("ReplaceToEmptyClears", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.ReplaceThresholds(ctx, 7, []threshold.Threshold{mk(7, 300)}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		got, err := s.ReplaceThresholds(ctx, 7, nil)
		if err != nil {
			t.Fatalf("clearing replace: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("clearing replace returned %+v, want empty", got)
		}
		listed, err := s.ListThresholdsForScenario(ctx, 7)
		if err != nil || len(listed) != 0 {
			t.Errorf("list after clear = %+v (%v), want empty", listed, err)
		}
	})

	t.Run("SaveAndReadResultsRoundTripNilFields", func(t *testing.T) {
		s := newStore(t)
		stored, err := s.ReplaceThresholds(ctx, 7, []threshold.Threshold{mk(7, 300)})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		th := stored[0]
		observed := 250.0
		satisfied := true
		missing := threshold.Result{
			ThresholdID: th.ID, ExecutionID: 1, RunID: 43,
			Metric: th.Metric, Comparison: th.Comparison, Value: th.Value,
			// A percentile the report never measured: unknown, with a
			// reason, never a fabricated fail.
			Reason: "no p95 latency in the report",
		}
		evaluated := threshold.Result{
			ThresholdID: th.ID, ExecutionID: 1, RunID: 42,
			Metric: th.Metric, Comparison: th.Comparison, Value: th.Value,
			Observed: &observed, Satisfied: &satisfied,
		}
		if err := s.SaveThresholdResults(ctx, []threshold.Result{evaluated, missing}); err != nil {
			t.Fatalf("SaveThresholdResults: %v", err)
		}
		for runID := int64(42); runID <= 43; runID++ {
			got, err := s.ThresholdResultsForRun(ctx, runID)
			if err != nil {
				t.Fatalf("ThresholdResultsForRun(%d): %v", runID, err)
			}
			if len(got) != 1 {
				t.Fatalf("run %d results = %d, want 1", runID, len(got))
			}
			g := got[0]
			if g.ThresholdID != th.ID || g.ExecutionID != 1 || g.RunID != runID {
				t.Errorf("identity = %d/%d/%d, want %d/1/%d", g.ThresholdID, g.ExecutionID, g.RunID, th.ID, runID)
			}
			if g.Metric != th.Metric || g.Comparison != th.Comparison || g.Value != th.Value {
				t.Errorf("snapshot = %s %s %v, want the definition as evaluated", g.Metric, g.Comparison, g.Value)
			}
			if g.EvaluatedAt.IsZero() {
				t.Errorf("run %d result has a zero evaluated_at", runID)
			}
		}
		got42, _ := s.ThresholdResultsForRun(ctx, 42)
		if got42[0].Observed == nil || *got42[0].Observed != 250 || got42[0].Satisfied == nil || !*got42[0].Satisfied {
			t.Errorf("run 42 result = %+v, want observed 250 satisfied true", got42[0])
		}
		got43, _ := s.ThresholdResultsForRun(ctx, 43)
		if got43[0].Observed != nil || got43[0].Satisfied != nil || got43[0].Reason == "" {
			t.Errorf("run 43 result = %+v, want nil observed/satisfied and a reason", got43[0])
		}
	})

	t.Run("ResultsAreScopedByRun", func(t *testing.T) {
		s := newStore(t)
		stored, err := s.ReplaceThresholds(ctx, 7, []threshold.Threshold{mk(7, 300)})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		satisfied := true
		if err := s.SaveThresholdResults(ctx, []threshold.Result{{
			ThresholdID: stored[0].ID, ExecutionID: 1, RunID: 42,
			Metric: stored[0].Metric, Comparison: stored[0].Comparison, Value: stored[0].Value,
			Satisfied: &satisfied,
		}}); err != nil {
			t.Fatalf("SaveThresholdResults: %v", err)
		}
		got, err := s.ThresholdResultsForRun(ctx, 99)
		if err != nil {
			t.Fatalf("ThresholdResultsForRun(99): %v", err)
		}
		if len(got) != 0 {
			t.Errorf("run 99 results = %+v, want none", got)
		}
	})

	t.Run("SaveResultsReplacesPreviousRunResults", func(t *testing.T) {
		s := newStore(t)
		stored, err := s.ReplaceThresholds(ctx, 7, []threshold.Threshold{mk(7, 300)})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		th := stored[0]
		row := func(satisfied bool) threshold.Result {
			return threshold.Result{
				ThresholdID: th.ID, ExecutionID: 1, RunID: 42,
				Metric: th.Metric, Comparison: th.Comparison, Value: th.Value,
				Satisfied: &satisfied,
			}
		}
		if err := s.SaveThresholdResults(ctx, []threshold.Result{row(true)}); err != nil {
			t.Fatalf("first save: %v", err)
		}
		if err := s.SaveThresholdResults(ctx, []threshold.Result{row(false)}); err != nil {
			t.Fatalf("second save: %v", err)
		}
		got, err := s.ThresholdResultsForRun(ctx, 42)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if len(got) != 1 || got[0].Satisfied == nil || *got[0].Satisfied {
			t.Errorf("after re-save = %+v, want exactly the newest evaluation", got)
		}
	})

	t.Run("EvaluatedAtSurvivesRoundTripWhenCallerStampsIt", func(t *testing.T) {
		s := newStore(t)
		stored, err := s.ReplaceThresholds(ctx, 7, []threshold.Threshold{mk(7, 300)})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		stamp := time.Now().UTC().Add(-time.Hour).Round(time.Second)
		satisfied := true
		if err := s.SaveThresholdResults(ctx, []threshold.Result{{
			ThresholdID: stored[0].ID, ExecutionID: 1, RunID: 42,
			Metric: stored[0].Metric, Comparison: stored[0].Comparison, Value: stored[0].Value,
			Satisfied: &satisfied, EvaluatedAt: stamp,
		}}); err != nil {
			t.Fatalf("SaveThresholdResults: %v", err)
		}
		got, err := s.ThresholdResultsForRun(ctx, 42)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if got[0].EvaluatedAt.IsZero() {
			t.Error("evaluated_at is zero after save; the run page shows it")
		}
	})
}
