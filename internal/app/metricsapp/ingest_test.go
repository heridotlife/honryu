package metricsapp_test

import (
	"context"
	"errors"
	"testing"

	"github.com/heridotlife/honryu/internal/app/metricsapp"
	"github.com/heridotlife/honryu/internal/domain/metrics"
)

func batch(e *env, shard int, tss ...int64) metrics.Batch {
	b := metrics.Batch{
		ExecutionID: e.executionID,
		ScenarioID:  e.scenarioIDs[0],
		RunID:       e.runID,
		ShardIndex:  shard,
		StreamID:    "s1",
	}
	for _, ts := range tss {
		e.seq++
		b.Intervals = append(b.Intervals, metrics.Interval{
			Seq: e.seq, Timestamp: ts, Label: "checkout-cart",
			Concurrency: 5, Samples: 10, Succeeded: 10,
			Latency: metrics.Histogram{0.01: 10},
		})
	}
	return b
}

func TestIngest_ForwardsToSinkAndBus(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)

	sub, unsubscribe := e.bus.Subscribe(e.executionID)
	defer unsubscribe()

	if err := e.svc.Ingest(context.Background(), batch(e, 0, 1, 2)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if got := len(e.sink.Recorded()); got != 2 {
		t.Fatalf("sink recorded %d measurements, want 2", got)
	}
	m := e.sink.Recorded()[0]
	if m.Label != "checkout-cart" || m.ExecutionID == "" || m.RunID == "" || m.EngineID != "0" {
		t.Errorf("measurement not attributed: %+v", m)
	}
	select {
	case <-sub:
	default:
		t.Error("nothing published to the bus for the live stream")
	}
}

// The sidecar retries a batch it could not push, so the same intervals arrive
// twice. Counting them twice would inflate every number the run reports.
func TestIngest_IsIdempotentForRetriedBatches(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	ctx := context.Background()

	b := batch(e, 0, 1, 2, 3)
	if err := e.svc.Ingest(ctx, b); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	if err := e.svc.Ingest(ctx, b); err != nil {
		t.Fatalf("retried Ingest: %v", err)
	}

	if got := len(e.sink.Recorded()); got != 3 {
		t.Errorf("sink recorded %d measurements after a retry, want 3", got)
	}
}

// Batches may overlap: a retry can carry seconds already absorbed alongside new
// ones. Only the new ones count.
func TestIngest_AbsorbsOnlyTheNewPartOfAnOverlappingBatch(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	ctx := context.Background()

	if err := e.svc.Ingest(ctx, batch(e, 0, 1, 2)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if err := e.svc.Ingest(ctx, batch(e, 0, 2, 3, 4)); err != nil {
		t.Fatalf("Ingest overlapping: %v", err)
	}

	if got := len(e.sink.Recorded()); got != 4 {
		t.Errorf("sink recorded %d measurements, want 4 (seconds 1-4, second 2 once)", got)
	}
}

// Pods report independently, so the same second arrives once per shard. Those
// are different measurements, not duplicates.
func TestIngest_KeepsShardsApart(t *testing.T) {
	t.Parallel()
	e := setup(t, 2)
	ctx := context.Background()

	for shard := 0; shard < 3; shard++ {
		if err := e.svc.Ingest(ctx, batch(e, shard, 1)); err != nil {
			t.Fatalf("Ingest shard %d: %v", shard, err)
		}
	}
	if got := len(e.sink.Recorded()); got != 3 {
		t.Errorf("sink recorded %d measurements, want one per shard", got)
	}
}

// Networks reorder. A batch that arrives late still describes real load and must
// be absorbed rather than discarded for being out of sequence.
func TestIngest_AcceptsOutOfOrderBatches(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	ctx := context.Background()

	if err := e.svc.Ingest(ctx, batch(e, 0, 5, 6)); err != nil {
		t.Fatalf("Ingest later seconds: %v", err)
	}
	if err := e.svc.Ingest(ctx, batch(e, 0, 1, 2)); err != nil {
		t.Fatalf("Ingest earlier seconds: %v", err)
	}
	if got := len(e.sink.Recorded()); got != 4 {
		t.Errorf("sink recorded %d measurements, want 4", got)
	}
}

// A pod that dies mid-run stops contributing. What the others measured stays
// valid: it describes the load that was actually produced.
func TestIngest_SurvivesAShardDyingMidRun(t *testing.T) {
	t.Parallel()
	e := setup(t, 2)
	ctx := context.Background()

	for ts := int64(1); ts <= 3; ts++ {
		if err := e.svc.Ingest(ctx, batch(e, 0, ts)); err != nil {
			t.Fatalf("shard 0 second %d: %v", ts, err)
		}
		if ts < 3 { // shard 1 dies after the second interval
			if err := e.svc.Ingest(ctx, batch(e, 1, ts)); err != nil {
				t.Fatalf("shard 1 second %d: %v", ts, err)
			}
		}
	}

	if got := len(e.sink.Recorded()); got != 5 {
		t.Errorf("sink recorded %d measurements, want 5 (3 from the survivor, 2 before the other died)", got)
	}
}

// After a re-deploy the previous run's pods are still dying while the new ones
// start. Their measurements describe a different run and must not be counted.
func TestIngest_RejectsBatchesFromAFinishedRun(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	ctx := context.Background()

	stale := batch(e, 0, 1)
	stale.RunID = e.runID + 999

	err := e.svc.Ingest(ctx, stale)
	if !errors.Is(err, metricsapp.ErrStaleRun) {
		t.Fatalf("Ingest(stale run) = %v, want ErrStaleRun", err)
	}
	if got := len(e.sink.Recorded()); got != 0 {
		t.Errorf("a stale batch contributed %d measurements", got)
	}
}

func TestIngest_RejectsBatchesWhenNothingIsRunning(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	ctx := context.Background()

	if err := e.store.StopRun(ctx, e.executionID); err != nil {
		t.Fatalf("StopRun: %v", err)
	}
	err := e.svc.Ingest(ctx, batch(e, 0, 1))
	if !errors.Is(err, metricsapp.ErrNoActiveRun) {
		t.Fatalf("Ingest with no run = %v, want ErrNoActiveRun", err)
	}
}

// A final batch ends the pod's contribution, so the intervals it reported can be
// forgotten rather than remembered for the life of the controller. Completion
// closes the run's marker too, so the release is observable one run later:
// the next run reusing the same interval keys must be absorbed, where a leaked
// key would suppress its very first second.
func TestIngest_FinalBatchReleasesDeduplicationState(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	ctx := context.Background()

	b := batch(e, 0, 1)
	b.Final = true
	if err := e.svc.Ingest(ctx, b); err != nil {
		t.Fatalf("Ingest final: %v", err)
	}
	if got := len(e.sink.Recorded()); got != 1 {
		t.Fatalf("sink recorded %d, want 1", got)
	}
	// Completion closed the marker; the execution's next run opens a new one.
	nextRun, err := e.store.StartRun(ctx, e.executionID, "")
	if err != nil {
		t.Fatalf("StartRun(next run): %v", err)
	}

	// State was released, so the same interval under the new run is absorbed
	// again rather than suppressed. That is the correct trade: after a final
	// batch the pod is gone, and holding its keys forever would leak for the
	// controller's lifetime -- and silence the next run's own traffic.
	again := batch(e, 0, 1)
	again.RunID = nextRun
	if err := e.svc.Ingest(ctx, again); err != nil {
		t.Fatalf("Ingest after final: %v", err)
	}
	if got := len(e.sink.Recorded()); got != 2 {
		t.Errorf("sink recorded %d, want 2", got)
	}
}

// An empty batch is what a pod sends when it finishes having already pushed
// everything. It must be accepted, not treated as an error.
func TestIngest_AcceptsAnEmptyFinalBatch(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)

	b := metrics.Batch{
		ExecutionID: e.executionID, ScenarioID: e.scenarioIDs[0],
		RunID: e.runID, ShardIndex: 0,
		Intervals: []metrics.Interval{}, Final: true,
	}
	if err := e.svc.Ingest(context.Background(), b); err != nil {
		t.Fatalf("Ingest(empty final) = %v, want nil", err)
	}
}
