package fake

import (
	"context"
	"sort"
	"sync"

	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/ports"
)

// ReportProgress is an in-memory ports.ReportProgress for fast use-case tests.
// It also fakes ports.IntervalRepository (see interval_repository.go): the
// per-second series Absorb builds is written by the same object, as it is in
// the real adapter.
type ReportProgress struct {
	mu   sync.Mutex
	runs map[int64]*progressRun
	// series is a run's permanent per-second measurements. It is deliberately
	// not part of progressRun: Discard drops working state, and the series
	// outlives finalisation.
	series map[int64]map[int64]*seriesSecond

	// AbsorbErr, when set, is returned by Absorb.
	AbsorbErr error
	// DiscardErr, when set, is returned by Discard instead of clearing anything.
	DiscardErr error
}

type progressRun struct {
	acc    *report.Accumulator
	shards map[shardKey]*shardStream
}

// shardKey identifies a pod within a run. Shard index alone cannot: it is a
// StatefulSet ordinal scoped to one scenario's own pods, and repeats across
// every scenario an execution bundles into one run -- and, since fan-out
// (phase 88), across every target cluster running the duplicated shard set.
type shardKey struct {
	scenarioID int64
	cluster    string
	shardIndex int
}

// shardStream is one pod's position in its own stream.
type shardStream struct {
	streamID string
	// seq is the highest sequence absorbed from this stream.
	seq      int64
	finished bool
	exitCode *int
}

// NewReportProgress builds an empty accumulator store.
func NewReportProgress() *ReportProgress {
	return &ReportProgress{
		runs:   map[int64]*progressRun{},
		series: map[int64]map[int64]*seriesSecond{},
	}
}

var _ ports.ReportProgress = (*ReportProgress)(nil)
var _ ports.IntervalRepository = (*ReportProgress)(nil)

// Absorb merges a shard's new intervals into the run.
func (p *ReportProgress) Absorb(_ context.Context, b ports.ProgressBatch) error {
	if p.AbsorbErr != nil {
		return p.AbsorbErr
	}
	if err := b.Validate(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	run, ok := p.runs[b.RunID]
	if !ok {
		run = &progressRun{acc: report.NewAccumulator(), shards: map[shardKey]*shardStream{}}
		p.runs[b.RunID] = run
	}
	key := shardKey{scenarioID: b.ScenarioID, cluster: b.Cluster, shardIndex: b.ShardIndex}
	shard, ok := run.shards[key]
	if !ok {
		shard = &shardStream{streamID: b.StreamID}
		run.shards[key] = shard
	}
	// A different stream is a restarted pod, whose sequences begin again at one.
	// Holding the old watermark would discard everything it measures from here on.
	if shard.streamID != b.StreamID {
		shard.streamID = b.StreamID
		shard.seq = 0
	}

	var fresh []metrics.Interval
	for _, iv := range b.Intervals {
		if iv.Seq <= shard.seq {
			continue // already absorbed; a retry re-sent it
		}
		run.acc.Add(iv)
		fresh = append(fresh, iv)
		shard.seq = iv.Seq
	}
	p.absorbSeries(b.RunID, fresh)
	if b.Final {
		shard.finished = true
	}
	if b.ExitCode != nil {
		shard.exitCode = b.ExitCode
	}
	return nil
}

// Snapshot returns the run's accumulated state.
func (p *ReportProgress) Snapshot(_ context.Context, runID int64) (report.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	run, ok := p.runs[runID]
	if !ok {
		return report.Snapshot{}, nil
	}
	return run.acc.Snapshot(), nil
}

// ShardStates returns each shard seen so far and its completion state.
func (p *ReportProgress) ShardStates(_ context.Context, runID int64) ([]ports.ShardState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	run, ok := p.runs[runID]
	if !ok {
		return nil, nil
	}
	out := make([]ports.ShardState, 0, len(run.shards))
	for key, shard := range run.shards {
		out = append(out, ports.ShardState{
			ScenarioID: key.scenarioID, Cluster: key.cluster, ShardIndex: key.shardIndex,
			Finished: shard.finished, ExitCode: shard.exitCode,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ScenarioID != out[j].ScenarioID {
			return out[i].ScenarioID < out[j].ScenarioID
		}
		if out[i].Cluster != out[j].Cluster {
			return out[i].Cluster < out[j].Cluster
		}
		return out[i].ShardIndex < out[j].ShardIndex
	})
	return out, nil
}

// Discard drops a run's working state.
func (p *ReportProgress) Discard(_ context.Context, runID int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.DiscardErr != nil {
		return p.DiscardErr
	}
	delete(p.runs, runID)
	return nil
}
