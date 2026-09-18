package metricsapp_test

import (
	"context"
	"testing"

	membus "github.com/heridotlife/honryu/internal/adapters/eventbus/memory"
	"github.com/heridotlife/honryu/internal/app/metricsapp"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// setupFanOutRun seeds a two-target fan-out execution with one single-shard
// scenario and an open run, over the same collaborators setup uses.
func setupFanOutRun(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	store := fake.NewStore()
	exe, _ := execution.New("everywhere", 1)
	exe.FanOutTargets = []string{"eu-1", "us-1"}
	executionID, _ := store.CreateExecution(ctx, exe)

	sc, _ := scenario.New("p", 1)
	scenarioID, _ := store.CreateScenario(ctx, sc)
	_ = store.StoreLoadProfile(ctx, executionID, false, []loadprofile.Entry{
		{ScenarioID: scenarioID, Concurrency: 1, Rampup: 1, Engines: 1, Duration: 1},
	})
	runID, _ := store.StartRun(ctx, executionID, "")

	sink := fake.NewMetricsSink()
	bus := membus.New()
	progress := fake.NewReportProgress()
	reports := fake.NewReportStore()
	svc := metricsapp.NewService(store, sink, bus, progress, reports)
	return &env{
		svc: svc, store: store, sink: sink, bus: bus, progress: progress, reports: reports,
		executionID: executionID, scenarioIDs: []int64{scenarioID}, runID: runID,
	}
}

// clusterBatch is one pod's push from a named cluster: the same shard index
// every target cluster's pod carries under full duplication, distinguished
// only by its stream and its cluster.
func clusterBatch(e *env, cluster, stream string, seq, ts int64, samples, failed int64, final bool) fakeBatch {
	return fakeBatch{e: e, cluster: cluster, stream: stream, seq: seq, ts: ts, samples: samples, failed: failed, final: final}
}

// fakeBatch is the input half of clusterBatch, kept as a plain struct so the
// test body reads as the sequence of pushes it is.
type fakeBatch struct {
	e        *env
	cluster  string
	stream   string
	seq      int64
	ts       int64
	samples  int64
	failed   int64
	final    bool
	exitCode int
}

func (f fakeBatch) post(t *testing.T) {
	t.Helper()
	exit := 0
	b := newClusterBatch(f.e, f.cluster, f.stream, f.seq, f.ts, f.samples, f.failed, f.final, &exit)
	if err := f.e.svc.Ingest(context.Background(), b); err != nil {
		t.Fatalf("Ingest (cluster %q): %v", f.cluster, err)
	}
}

// newClusterBatch builds the metrics.Batch a pod in cluster would push.
func newClusterBatch(e *env, cluster, stream string, seq, ts, samples, failed int64, final bool, exit *int) metrics.Batch {
	b := batch(e, 0, ts)
	b.Cluster = cluster
	b.StreamID = stream
	b.Intervals[0].Seq = seq
	b.Intervals[0].Samples = samples
	b.Intervals[0].Succeeded = samples - failed
	b.Intervals[0].Failed = failed
	b.Final = final
	if final {
		b.ExitCode = exit
	}
	return b
}

// A fan-out run's report carries a per-cluster breakdown: one row per target
// cluster that pushed measurements, with its share of samples and failures
// and the run's own outcome. The run-level aggregate stays the whole truth --
// both clusters' samples sum into it.
func TestFanOut_ReportCarriesPerClusterResults(t *testing.T) {
	t.Parallel()
	e := setupFanOutRun(t)

	clusterBatch(e, "eu-1", "eu-s1", 1, 1, 10, 0, true).post(t)
	clusterBatch(e, "us-1", "us-s1", 1, 1, 10, 2, true).post(t)

	rep, err := e.reports.GetReport(context.Background(), e.runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if len(rep.ClusterResults) != 2 {
		t.Fatalf("ClusterResults = %+v, want one row per target cluster", rep.ClusterResults)
	}
	eu, us := rep.ClusterResults[0], rep.ClusterResults[1]
	if eu.Cluster != "eu-1" || eu.Samples != 10 || eu.Failed != 0 {
		t.Errorf("eu-1 row = %+v, want cluster eu-1, 10 samples, 0 failed", eu)
	}
	if us.Cluster != "us-1" || us.Samples != 10 || us.Failed != 2 {
		t.Errorf("us-1 row = %+v, want cluster us-1, 10 samples, 2 failed", us)
	}
	for _, row := range rep.ClusterResults {
		if row.Outcome != rep.Outcome {
			t.Errorf("row %+v outcome = %q, want the run's own %q", row, row.Outcome, rep.Outcome)
		}
	}
	if rep.Achieved.Samples != 20 {
		t.Errorf("aggregate samples = %d, want 20 (both clusters)", rep.Achieved.Samples)
	}
}

// An ordinary single-cluster run grows no cluster_results: the default fleet's
// empty-name tally alone must not turn every legacy report into a fan-out one.
func TestOrdinaryRun_ReportHasNoClusterResults(t *testing.T) {
	t.Parallel()
	e := setup(t, 1)
	ctx := context.Background()

	b := batch(e, 0, 1)
	b.Final = true
	exit := 0
	b.ExitCode = &exit
	if err := e.svc.Ingest(ctx, b); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	rep, err := e.reports.GetReport(ctx, e.runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.ClusterResults != nil {
		t.Errorf("ClusterResults = %+v, want nil on a single-cluster run", rep.ClusterResults)
	}
}

// Two target clusters' pods share shard indexes; the live view must keep
// them apart (the cluster is part of the interval's identity), or the second
// cluster's measurements would be dropped as re-pushes of the first's.
func TestFanOut_LiveViewKeepsClustersApart(t *testing.T) {
	t.Parallel()
	e := setupFanOutRun(t)

	clusterBatch(e, "eu-1", "eu-s1", 1, 1, 10, 0, false).post(t)
	clusterBatch(e, "us-1", "us-s1", 1, 1, 10, 0, false).post(t)

	if got := len(e.sink.Recorded()); got != 2 {
		t.Fatalf("sink recorded %d measurements, want 2 (one per cluster)", got)
	}
	ids := map[string]bool{}
	for _, m := range e.sink.Recorded() {
		ids[m.EngineID] = true
	}
	if !ids["eu-1/0"] || !ids["us-1/0"] {
		t.Errorf("engine ids = %v, want cluster-qualified eu-1/0 and us-1/0", ids)
	}
}
