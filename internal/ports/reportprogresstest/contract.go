// Package reportprogresstest is the shared conformance suite every
// ReportProgress must pass, fake and real alike.
package reportprogresstest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/ports"
)

// NewProgress builds a store with no runs in it.
type NewProgress func(t *testing.T) ports.ReportProgress

// Run exercises ReportProgress behaviour.
func Run(t *testing.T, newProgress NewProgress) {
	t.Helper()
	ctx := context.Background()

	t.Run("AbsorbAccumulates", func(t *testing.T) {
		p := newProgress(t)
		if err := p.Absorb(ctx, batch(1, 0, "s1", false,
			iv(1, 1000, "probe", 10, 5, 0),
			iv(2, 1001, "probe", 10, 5, 0),
		)); err != nil {
			t.Fatalf("Absorb: %v", err)
		}

		got := restore(t, p, 1)
		if len(got.Labels) != 1 || got.Labels[0].Samples != 20 {
			t.Errorf("labels = %+v, want one with 20 samples", got.Labels)
		}
	})

	// The reason intervals carry a sequence. A push whose response was lost is
	// followed by a superset batch: the intervals that did arrive plus everything
	// read since. The old ones must not be counted twice, and the new ones must
	// not be lost with them.
	t.Run("RetryOfASupersetBatchCountsEachIntervalOnce", func(t *testing.T) {
		p := newProgress(t)
		first := []metrics.Interval{iv(1, 1000, "probe", 10, 5, 0)}
		if err := p.Absorb(ctx, batch(1, 0, "s1", false, first...)); err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		// The sidecar never saw the acknowledgement, so it re-sends the first
		// interval alongside one read since.
		if err := p.Absorb(ctx, batch(1, 0, "s1", false,
			iv(1, 1000, "probe", 10, 5, 0),
			iv(2, 1001, "probe", 7, 3, 0),
		)); err != nil {
			t.Fatalf("Absorb retry: %v", err)
		}

		got := restore(t, p, 1)
		if len(got.Labels) != 1 {
			t.Fatalf("labels = %+v, want one", got.Labels)
		}
		if got.Labels[0].Samples != 17 {
			t.Errorf("samples = %d, want 17 -- the retried interval counted once, the new one kept",
				got.Labels[0].Samples)
		}
	})

	// A batch boundary can fall between two labels of the same second, so a
	// second arriving in two pieces must accumulate to the whole.
	t.Run("ASecondSplitAcrossBatches", func(t *testing.T) {
		p := newProgress(t)
		if err := p.Absorb(ctx, batch(1, 0, "s1", false, iv(1, 1000, "cart", 10, 0, 4))); err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		if err := p.Absorb(ctx, batch(1, 0, "s1", false, iv(2, 1000, "pay", 6, 0, 3))); err != nil {
			t.Fatalf("Absorb: %v", err)
		}

		got := restore(t, p, 1)
		if len(got.Labels) != 2 {
			t.Fatalf("labels = %+v, want both halves of the second", got.Labels)
		}
		var second report.SecondProgress
		for _, s := range got.Seconds {
			if s.Second == 1000 {
				second = s
			}
		}
		if second.Labels != 7 {
			t.Errorf("concurrency at 1000 = %d, want 7 summed across both labels", second.Labels)
		}
	})

	// Shards contribute independently and additively.
	t.Run("ShardsAccumulateTogether", func(t *testing.T) {
		p := newProgress(t)
		for shard := range 3 {
			if err := p.Absorb(ctx, batch(1, shard, "s1", false,
				iv(1, 1000, "probe", 10, 1, 5),
			)); err != nil {
				t.Fatalf("Absorb shard %d: %v", shard, err)
			}
		}
		got := restore(t, p, 1)
		if got.Labels[0].Samples != 30 {
			t.Errorf("samples = %d, want 30 across three shards", got.Labels[0].Samples)
		}
		// Each shard's sequence is its own: three shards at seq 1 are three
		// intervals, not one interval sent thrice.
		if got.Seconds[0].Labels != 15 {
			t.Errorf("concurrency = %d, want 15 summed across shards", got.Seconds[0].Labels)
		}
	})

	// A restarted pod begins its sequence again at one. Treating that as a
	// duplicate would discard everything it measures for the rest of the run.
	t.Run("ARestartedShardIsNotMistakenForADuplicate", func(t *testing.T) {
		p := newProgress(t)
		if err := p.Absorb(ctx, batch(1, 0, "s1", false,
			iv(1, 1000, "probe", 10, 0, 0),
			iv(2, 1001, "probe", 10, 0, 0),
		)); err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		// The pod restarted: new stream, sequence back to one.
		if err := p.Absorb(ctx, batch(1, 0, "s2", false,
			iv(1, 1002, "probe", 8, 0, 0),
		)); err != nil {
			t.Fatalf("Absorb after restart: %v", err)
		}

		got := restore(t, p, 1)
		if got.Labels[0].Samples != 28 {
			t.Errorf("samples = %d, want 28 -- the restarted stream was discarded", got.Labels[0].Samples)
		}
	})

	// A batch that cannot be deduplicated is refused rather than absorbed.
	t.Run("RejectsUnsequencedIntervals", func(t *testing.T) {
		p := newProgress(t)
		unsequenced := batch(1, 0, "s1", false, iv(0, 1000, "probe", 10, 0, 0))
		if err := p.Absorb(ctx, unsequenced); !errors.Is(err, ports.ErrUnsequencedBatch) {
			t.Errorf("Absorb(unsequenced) = %v, want ErrUnsequencedBatch", err)
		}
		got := restore(t, p, 1)
		if len(got.Labels) != 0 {
			t.Errorf("a rejected batch was absorbed anyway: %+v", got.Labels)
		}
	})

	// A run's outcome is derived from its shards' exit codes, so the state has to
	// carry them -- and a shard finished by pod teardown before it could write
	// one is still finished, just without a code to report.
	t.Run("ShardStatesReportFinishedAndExitCode", func(t *testing.T) {
		p := newProgress(t)
		passed := withExitCode(batch(1, 0, "s1", true, iv(1, 1000, "probe", 5, 0, 0)), 0)
		if err := p.Absorb(ctx, passed); err != nil {
			t.Fatalf("Absorb shard 0: %v", err)
		}
		failed := withExitCode(batch(1, 1, "s1", true, iv(1, 1000, "probe", 5, 0, 0)), 3)
		if err := p.Absorb(ctx, failed); err != nil {
			t.Fatalf("Absorb shard 1: %v", err)
		}
		// Torn down before it could write an exit code: still finished.
		if err := p.Absorb(ctx, batch(1, 2, "s1", true, iv(1, 1000, "probe", 5, 0, 0))); err != nil {
			t.Fatalf("Absorb shard 2: %v", err)
		}

		states, err := p.ShardStates(ctx, 1)
		if err != nil {
			t.Fatalf("ShardStates: %v", err)
		}
		if len(states) != 3 {
			t.Fatalf("shard states = %+v, want 3", states)
		}
		byIndex := map[int]ports.ShardState{}
		for _, st := range states {
			byIndex[st.ShardIndex] = st
			if !st.Finished {
				t.Errorf("shard %d not finished", st.ShardIndex)
			}
		}
		if got := byIndex[0].ExitCode; got == nil || *got != 0 {
			t.Errorf("shard 0 exit code = %v, want 0", got)
		}
		if got := byIndex[1].ExitCode; got == nil || *got != 3 {
			t.Errorf("shard 1 exit code = %v, want 3", got)
		}
		if got := byIndex[2].ExitCode; got != nil {
			t.Errorf("shard 2 exit code = %v, want none", got)
		}
	})

	t.Run("SnapshotOfAnUnknownRunIsEmpty", func(t *testing.T) {
		p := newProgress(t)
		got, err := p.Snapshot(ctx, 999)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(got.Labels) != 0 || len(got.Seconds) != 0 || len(got.Signatures) != 0 {
			t.Errorf("snapshot of an unknown run = %+v", got)
		}
		states, err := p.ShardStates(ctx, 999)
		if err != nil || len(states) != 0 {
			t.Errorf("ShardStates(unknown) = %+v, %v", states, err)
		}
	})

	// Working state is discarded once it has become a report, and discarding a
	// run that has none is what a re-run of finalisation does.
	t.Run("DiscardIsIdempotent", func(t *testing.T) {
		p := newProgress(t)
		if err := p.Absorb(ctx, batch(1, 0, "s1", true, iv(1, 1000, "probe", 10, 0, 0))); err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		if err := p.Discard(ctx, 1); err != nil {
			t.Fatalf("Discard: %v", err)
		}
		if err := p.Discard(ctx, 1); err != nil {
			t.Errorf("Discard of an already-discarded run: %v", err)
		}
		got := restore(t, p, 1)
		if len(got.Labels) != 0 {
			t.Errorf("state survived Discard: %+v", got.Labels)
		}
		if states, _ := p.ShardStates(ctx, 1); len(states) != 0 {
			t.Errorf("shard states after Discard = %+v, want none", states)
		}
	})

	// Runs must not bleed into each other: two runs accumulating at once is the
	// normal state of a busy control plane.
	t.Run("RunsAreIsolated", func(t *testing.T) {
		p := newProgress(t)
		if err := p.Absorb(ctx, batch(1, 0, "s1", false, iv(1, 1000, "probe", 10, 0, 0))); err != nil {
			t.Fatalf("Absorb run 1: %v", err)
		}
		if err := p.Absorb(ctx, batch(2, 0, "s1", false, iv(1, 1000, "probe", 99, 0, 0))); err != nil {
			t.Fatalf("Absorb run 2: %v", err)
		}
		if got := restore(t, p, 1).Labels[0].Samples; got != 10 {
			t.Errorf("run 1 samples = %d, want 10", got)
		}
		if got := restore(t, p, 2).Labels[0].Samples; got != 99 {
			t.Errorf("run 2 samples = %d, want 99", got)
		}
	})

	// An execution can bundle several scenarios under one run, each deployed as
	// its own StatefulSet whose ordinals start again at 0 -- shard index alone
	// must not collide across them.
	t.Run("ShardsAreKeyedByScenarioNotIndexAlone", func(t *testing.T) {
		p := newProgress(t)
		// Deliberately the same stream id: before scenario was part of the key,
		// this looked like the same shard continuing its own stream, and the
		// second scenario's interval was silently dropped as an already-seen
		// sequence rather than absorbed as a different pod's data.
		if err := p.Absorb(ctx, batchForScenario(1, 10, 0, "s1", true, iv(1, 1000, "probe", 5, 0, 0))); err != nil {
			t.Fatalf("Absorb scenario 10 shard 0: %v", err)
		}
		if err := p.Absorb(ctx, batchForScenario(1, 20, 0, "s1", true, iv(1, 1000, "probe", 7, 0, 0))); err != nil {
			t.Fatalf("Absorb scenario 20 shard 0: %v", err)
		}

		states, err := p.ShardStates(ctx, 1)
		if err != nil {
			t.Fatalf("ShardStates: %v", err)
		}
		if len(states) != 2 {
			t.Fatalf("shard states = %+v, want 2 -- one per scenario's shard 0", states)
		}
		got := restore(t, p, 1)
		if len(got.Labels) != 1 || got.Labels[0].Samples != 12 {
			t.Errorf("samples = %+v, want 12 (5+7) -- both scenarios' shard 0 must accumulate, not collide", got.Labels)
		}
	})

	// The whole point of persisting the working state: what is read back has to
	// produce the same report as the accumulator that wrote it.
	t.Run("SnapshotRebuildsTheSameReport", func(t *testing.T) {
		p := newProgress(t)
		intervals := []metrics.Interval{
			iv(1, 1000, "cart", 100, 10, 20),
			iv(2, 1000, "pay", 50, 5, 10),
			iv(3, 1001, "cart", 100, 0, 20),
		}
		intervals[0].Errors = []metrics.ErrorGroup{
			{Message: "Not Found", ResponseCode: "404", Count: 10},
		}
		intervals[1].Errors = []metrics.ErrorGroup{
			{Message: "socket: too many open files", Count: 5},
		}
		// Response codes ride the same round trip: a report that said which
		// status each request returned before a restart but not after would be
		// silently wrong in the one dimension a reader can act on.
		intervals[0].ResponseCodes = map[string]int64{"200": 90, "404": 10}
		intervals[1].ResponseCodes = map[string]int64{"200": 45, "502": 5}
		intervals[2].ResponseCodes = map[string]int64{"200": 100}
		if err := p.Absorb(ctx, batch(1, 0, "s1", true, intervals...)); err != nil {
			t.Fatalf("Absorb: %v", err)
		}

		meta := report.Meta{ExecutionID: 1, RunID: 1, Outcome: "failed",
			Requested: report.Load{DurationSeconds: 10}}
		direct := report.NewAccumulator()
		for _, in := range intervals {
			direct.Add(in)
		}
		if got, want := restoreAcc(t, p, 1).Report(meta), direct.Report(meta); !reflect.DeepEqual(got, want) {
			t.Errorf("report from stored state differs:\n got %+v\nwant %+v", got, want)
		}
	})
}

// batch builds a batch for scenario 1 -- every case here but one is about a
// single scenario's own shards, so the scenario id is a fixed, uninteresting
// constant rather than a parameter every call site has to repeat.
func batch(runID int64, shard int, stream string, final bool, intervals ...metrics.Interval) ports.ProgressBatch {
	return batchForScenario(runID, 1, shard, stream, final, intervals...)
}

func batchForScenario(runID, scenarioID int64, shard int, stream string, final bool, intervals ...metrics.Interval) ports.ProgressBatch {
	return ports.ProgressBatch{
		RunID: runID, ScenarioID: scenarioID, ShardIndex: shard, StreamID: stream, Final: final, Intervals: intervals,
	}
}

func withExitCode(b ports.ProgressBatch, code int) ports.ProgressBatch {
	b.ExitCode = &code
	return b
}

func iv(seq, ts int64, label string, samples, failed int64, concurrency int) metrics.Interval {
	return metrics.Interval{
		Seq: seq, Timestamp: ts, Label: label,
		Samples: samples, Failed: failed, Succeeded: samples - failed,
		Concurrency: concurrency,
		Latency:     metrics.Histogram{0.01: samples},
	}
}

func restore(t *testing.T, p ports.ReportProgress, runID int64) report.Snapshot {
	t.Helper()
	got, err := p.Snapshot(context.Background(), runID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return got
}

func restoreAcc(t *testing.T, p ports.ReportProgress, runID int64) *report.Accumulator {
	t.Helper()
	return report.Restore(restore(t, p, runID))
}
