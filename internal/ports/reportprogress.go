package ports

import (
	"context"
	"errors"
	"fmt"

	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/report"
)

// Progress errors. Callers compare with errors.Is.
var (
	// ErrUnsequencedBatch means a batch's intervals carry no sequence, so
	// duplicates in it cannot be recognised.
	ErrUnsequencedBatch = errors.New("ports: batch intervals carry no sequence")
	// ErrProgressRunRequired means the batch names no run to accumulate into.
	ErrProgressRunRequired = errors.New("ports: a valid run id is required")
	// ErrProgressScenarioRequired means the batch names no scenario. Shard
	// index alone cannot key a run's working state -- it repeats across an
	// execution's scenarios -- so a batch without one cannot be absorbed
	// safely.
	ErrProgressScenarioRequired = errors.New("ports: a valid scenario id is required")
)

// ProgressBatch is one shard's push, on its way into a run's working state.
type ProgressBatch struct {
	RunID int64
	// ScenarioID identifies which scenario's StatefulSet this shard belongs to.
	// An execution can bundle several scenarios under one run, each deployed as
	// its own StatefulSet with its own ordinals starting again at 0 -- without
	// this, two scenarios' shard 0 would collide on the same working state.
	ScenarioID int64
	// ShardIndex identifies the pod within its scenario's StatefulSet.
	ShardIndex int
	// Cluster is the pod's load origin (phase 88): a fan-out run's target
	// clusters each run a pod with the same (scenario, shard) indexes, so the
	// cluster is what keeps their working state -- and above all their
	// finished flags -- apart. Empty is the deployment default cluster, the
	// identity every ordinary run's pods keep.
	Cluster string
	// StreamID names the sidecar instance. Sequences count from one per
	// instance, so a change of stream means a restarted pod rather than a
	// duplicate.
	StreamID string
	// Final marks the last batch this shard will send.
	Final bool
	// ExitCode is bzt's exit code, present once the engine has finished on its
	// own. A shard finished by pod teardown before it could write one has none.
	ExitCode *int
	// Intervals are the measurements, each carrying its sequence.
	Intervals []metrics.Interval
}

// ShardState is one shard's completion state within a run.
type ShardState struct {
	// ScenarioID and ShardIndex together identify the shard: ShardIndex alone
	// repeats across an execution's scenarios.
	ScenarioID int64
	ShardIndex int
	// Cluster is the shard's load origin (phase 88): repeats across a
	// fan-out run's target clusters -- the same pod indexes, different
	// clusters -- and empty for the deployment default.
	Cluster string
	// Finished means this shard sent its last batch. It does not imply an exit
	// code is known -- a pod torn down before it could write one still finishes.
	Finished bool
	ExitCode *int
}

// Validate rejects a batch that cannot be absorbed safely.
//
// An interval with no sequence is refused rather than absorbed. Only a sidecar
// older than the control plane can produce one, and absorbing it would mean
// counting a retry's re-sent intervals twice -- into a report that is kept
// forever as the evidence a judgement was made on. Failing loudly is recoverable;
// a quietly inflated error count is not.
func (b ProgressBatch) Validate() error {
	if b.RunID <= 0 {
		return ErrProgressRunRequired
	}
	if b.ScenarioID <= 0 {
		return ErrProgressScenarioRequired
	}
	for _, iv := range b.Intervals {
		if iv.Seq <= 0 {
			return fmt.Errorf("%w: shard %d, second %d, label %q",
				ErrUnsequencedBatch, b.ShardIndex, iv.Timestamp, iv.Label)
		}
	}
	return nil
}

// ReportProgress accumulates a run's measurements while it is still running.
//
// A run's report cannot be built when the run ends unless something kept what
// was measured along the way, and holding it in the pushing process would lose
// it to a restart and scatter it across replicas. This is that state, kept where
// every replica can reach it and bounded by a run's labels and duration rather
// than by every pod-second measured.
//
// It is working state, not a record: once finalised into a report it is
// discarded, and the report is what survives.
type ReportProgress interface {
	// Absorb merges a shard's intervals into the run, skipping any it has
	// already absorbed, and records the shard as finished when the batch is
	// final. Absorbing the same batch twice leaves the same state as once.
	Absorb(ctx context.Context, b ProgressBatch) error
	// Snapshot returns what has accumulated so far, ready for report.Restore.
	Snapshot(ctx context.Context, runID int64) (report.Snapshot, error)
	// ShardStates returns each shard seen so far and its completion state. This
	// is how a run is known to be complete, and how each of its shards ended,
	// without asking a cluster.
	ShardStates(ctx context.Context, runID int64) ([]ShardState, error)
	// Discard drops a run's working state once it has been finalised.
	// Discarding a run with no state is not an error.
	Discard(ctx context.Context, runID int64) error
}
