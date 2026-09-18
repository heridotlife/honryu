package metricsapp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"

	"github.com/heridotlife/honryu/internal/domain/engine"
	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

// Ingest errors. Callers compare with errors.Is.
var (
	// ErrNoActiveRun means the execution is not running. A pod that outlived its
	// run must not contribute to whatever runs next.
	ErrNoActiveRun = errors.New("metricsapp: execution has no active run")
	// ErrStaleRun means the batch belongs to a run that has since ended.
	ErrStaleRun = errors.New("metricsapp: batch belongs to a finished run")
)

// seen remembers which intervals a run has already absorbed, so a batch that
// arrives twice is counted once.
//
// The sidecar keeps a failed push pending and retries it, which is what makes
// a brief control-plane outage survivable -- and what guarantees duplicates.
// Without this, a retried batch would double every counter it carried.
type seen struct {
	mu   sync.Mutex
	runs map[int64]map[string]struct{} // executionID -> interval key
}

func newSeen() *seen {
	return &seen{runs: map[int64]map[string]struct{}{}}
}

// mark records an interval and reports whether it is new.
func (s *seen) mark(executionID int64, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, ok := s.runs[executionID]
	if !ok {
		keys = map[string]struct{}{}
		s.runs[executionID] = keys
	}
	if _, dup := keys[key]; dup {
		return false
	}
	keys[key] = struct{}{}
	return true
}

// forget drops an execution's history once its run is over, so a long-lived
// controller does not accumulate every interval it has ever seen.
func (s *seen) forget(executionID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runs, executionID)
}

// clusterCounts is one cluster's share of a run's measurements.
type clusterCounts struct {
	Samples int64
	Failed  int64
}

// clusterTally accumulates a run's measurements per load origin, for the
// fan-out report's per-cluster breakdown (phase 88). Like seen it is
// in-memory: a control-plane restart loses the tally, and a run finalised
// after that lands with no cluster_results -- the run's own aggregate is
// still exact, only the origin split is gone. Counted inside the same
// dedup gate the live view uses, so a retried batch does not double it.
type clusterTally struct {
	mu   sync.Mutex
	runs map[int64]map[string]clusterCounts // runID -> cluster -> counts
}

func newClusterTally() *clusterTally {
	return &clusterTally{runs: map[int64]map[string]clusterCounts{}}
}

// add folds one interval into its cluster's share of runID.
func (c *clusterTally) add(runID int64, cluster string, in metrics.Interval) {
	c.mu.Lock()
	defer c.mu.Unlock()
	counts, ok := c.runs[runID]
	if !ok {
		counts = map[string]clusterCounts{}
		c.runs[runID] = counts
	}
	cur := counts[cluster]
	cur.Samples += in.Samples
	cur.Failed += in.Failed
	counts[cluster] = cur
}

// snapshot copies runID's per-cluster counts, ordered by cluster name so a
// report's rows are deterministic.
func (c *clusterTally) snapshot(runID int64) []report.ClusterResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	counts, ok := c.runs[runID]
	if !ok {
		return nil
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]report.ClusterResult, 0, len(names))
	for _, name := range names {
		out = append(out, report.ClusterResult{
			Cluster: name, Samples: counts[name].Samples, Failed: counts[name].Failed,
		})
	}
	return out
}

// forget drops a finished run's tally, the same lifecycle seen follows.
func (c *clusterTally) forget(runID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.runs, runID)
}

// Ingest absorbs one engine pod's measurements.
//
// Batches arrive by push from a sidecar inside the pod. Nothing here reaches
// back into a cluster, which is what lets an execution run somewhere the control
// plane cannot address.
//
// Shards contribute independently and additively, so a pod that dies mid-run
// simply stops contributing: the aggregate stays valid, describing the load that
// was actually produced rather than becoming corrupt.
func (s *Service) Ingest(ctx context.Context, batch metrics.Batch) error {
	runID, running, err := s.repo.CurrentRun(ctx, batch.ExecutionID)
	if err != nil {
		return err
	}
	if !running {
		// A Final with no open run is not just noise to reject: it is the
		// control plane's only reliable "these engines already finished"
		// signal (pods stay Ready forever after bzt exits, so the scheduler
		// cannot see it). Record it for Trigger's stranded-run guard, then
		// reject the push exactly as before -- the sidecar retries, and the
		// overwrite-keyed record keeps that harmless (one event).
		if batch.Final {
			oc := ports.OrphanCompletion{
				ExecutionID: batch.ExecutionID, ScenarioID: batch.ScenarioID,
				ShardIndex: batch.ShardIndex, ExitCode: batch.ExitCode, FinishedAt: s.now(),
			}
			if err := s.repo.RecordOrphanCompletion(ctx, oc); err != nil {
				return err
			}
		}
		return fmt.Errorf("%w: execution %d", ErrNoActiveRun, batch.ExecutionID)
	}
	// A pod from an earlier run must not pollute the current one. This is the
	// case that matters after a re-deploy: the old pods are still dying while
	// the new ones start.
	if batch.RunID != 0 && batch.RunID != runID {
		return fmt.Errorf("%w: batch run %d, current run %d", ErrStaleRun, batch.RunID, runID)
	}

	progressBatch := ports.ProgressBatch{
		RunID: runID, ScenarioID: batch.ScenarioID, ShardIndex: batch.ShardIndex, StreamID: batch.StreamID,
		Cluster: batch.Cluster, Final: batch.Final, ExitCode: batch.ExitCode, Intervals: batch.Intervals,
	}
	// Validated before anything is forwarded: a batch this malformed can only
	// come from a sidecar older than the control plane, and rejecting it after
	// the live view already published it would leave that view showing data the
	// permanent record refused.
	if err := progressBatch.Validate(); err != nil {
		return err
	}

	for _, in := range batch.Intervals {
		key := intervalKey(batch, in)
		if !s.seen.mark(batch.ExecutionID, key) {
			// Already absorbed; a retry of a batch that did arrive.
			continue
		}
		s.clusterTally.add(runID, batch.Cluster, in)
		s.record(batch, in, runID)
	}

	// The permanent record is accumulated independently of the live-view dedup
	// above: ReportProgress keeps its own per-shard sequence, exact across a
	// control-plane restart in a way the in-memory seen map is not.
	if err := s.progress.Absorb(ctx, progressBatch); err != nil {
		return err
	}

	if !batch.Final {
		return nil
	}
	done, err := s.allShardsFinished(ctx, batch.ExecutionID, runID)
	if err != nil {
		return err
	}
	if !done {
		return nil
	}
	if err := s.finalizeCompleted(ctx, batch.ExecutionID, runID); err != nil {
		return err
	}
	// The run is over and its report is written; its intervals cannot arrive
	// again, so stop remembering them.
	s.seen.forget(batch.ExecutionID)
	s.clusterTally.forget(runID)
	return nil
}

// allShardsFinished reports whether every shard the execution's load profile
// called for has said it will send no more, which is how a run is known to be
// complete without asking a cluster.
func (s *Service) allShardsFinished(ctx context.Context, executionID, runID int64) (bool, error) {
	states, err := s.progress.ShardStates(ctx, runID)
	if err != nil {
		return false, err
	}
	finished := 0
	for _, st := range states {
		if st.Finished {
			finished++
		}
	}
	profile, err := s.repo.LoadProfileFor(ctx, executionID)
	if err != nil {
		return false, err
	}
	planned := 0
	for _, e := range profile {
		planned += e.Engines
	}
	if planned == 0 {
		return false, nil
	}
	// Fan-out (phase 88): every target cluster runs the full shard set, so
	// the run is finished only when every shard on EVERY cluster finished --
	// without the multiplier, the first cluster to finish would close the
	// run while the others still loaded.
	exe, err := s.repo.GetExecution(ctx, executionID)
	if err != nil {
		return false, err
	}
	if exe.IsFanOut() {
		planned *= len(exe.FanOutTargets)
	}
	return finished >= planned, nil
}

// exitCodeUnknown stands in for a finished shard whose exit code never
// arrived: torn down before bzt could write it (see metrics.Batch.ExitCode).
// It is not a code bzt would ever produce, so it rolls up through
// taurus.OutcomeFromExitCode's default case to OutcomeError -- the same
// "no evidence" treatment CombineOutcomes already gives a run with no exit
// codes at all. Simply omitting such a shard would let the rest of the
// shards' codes decide the outcome as though this one had never run.
const exitCodeUnknown = -1

// finalizeCompleted rolls up every shard's exit code into the run's outcome and
// finalises it. Called only once every shard has finished on its own -- a run
// Honryu stopped itself is finalised as an abort instead, by Finalize.
func (s *Service) finalizeCompleted(ctx context.Context, executionID, runID int64) error {
	states, err := s.progress.ShardStates(ctx, runID)
	if err != nil {
		return err
	}
	codes := make([]int, 0, len(states))
	for _, st := range states {
		if st.ExitCode == nil {
			codes = append(codes, exitCodeUnknown)
			continue
		}
		codes = append(codes, *st.ExitCode)
	}
	return s.finalize(ctx, executionID, runID, taurus.CombineOutcomes(codes))
}

// intervalKey identifies one measurement uniquely within a run: which pod,
// which second, which request. The cluster is part of the pod's identity
// under fan-out (phase 88): two target clusters' pods share shard indexes,
// so without it the second cluster's live-view measurements would be
// mistaken for re-pushes of the first's and dropped.
func intervalKey(b metrics.Batch, in metrics.Interval) string {
	return b.Cluster + "|" +
		strconv.Itoa(b.ShardIndex) + "|" +
		strconv.FormatInt(b.ScenarioID, 10) + "|" +
		strconv.FormatInt(in.Timestamp, 10) + "|" + in.Label
}

// record forwards one interval to the metrics sink and the event bus.
//
// The sink and the SSE stream still speak the per-measurement shape the agent
// protocol used, so an interval is expressed in those terms: its average latency
// stands in for the samples it summarises. The buckets it carries are what
// matter for percentiles, and those are aggregated separately.
func (s *Service) record(b metrics.Batch, in metrics.Interval, runID int64) {
	status := "200"
	if in.Failed > 0 && in.Succeeded == 0 {
		status = "500"
	}
	// Under fan-out, shard indexes repeat across target clusters; the
	// cluster prefix keeps two pods' live-view series apart. An ordinary
	// run's ids are unchanged, byte for byte.
	engineID := strconv.Itoa(b.ShardIndex)
	if b.Cluster != "" {
		engineID = b.Cluster + "/" + engineID
	}
	m := engine.Metric{
		Label:       in.Label,
		Latency:     in.Latency.Percentile(50),
		Threads:     float64(in.Concurrency),
		Status:      status,
		ExecutionID: strconv.FormatInt(b.ExecutionID, 10),
		ScenarioID:  strconv.FormatInt(b.ScenarioID, 10),
		EngineID:    engineID,
		RunID:       strconv.FormatInt(runID, 10),
	}
	s.sink.Record(m)
	s.bus.Publish(b.ExecutionID, m)
}
