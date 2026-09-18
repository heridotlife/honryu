package lifecycleapp_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/run"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// fanoutScheduler is a ports.Scheduler that routes by ClusterRef to a
// per-cluster fake.Scheduler -- the in-memory stand-in for the k8s Router's
// per-cluster bound schedulers. Deployments are recorded per cluster, which
// is exactly what the fan-out assertions need to see.
type fanoutScheduler struct {
	mu        sync.Mutex
	clusters  map[ports.ClusterRef]*fake.Scheduler
	deployLog []ports.DeploySpec
}

func newFanoutScheduler(names ...string) *fanoutScheduler {
	fs := &fanoutScheduler{clusters: map[ports.ClusterRef]*fake.Scheduler{}}
	for _, n := range names {
		fs.clusters[ports.ClusterRef(n)] = fake.NewScheduler()
	}
	return fs
}

func (f *fanoutScheduler) sched(cluster ports.ClusterRef) (*fake.Scheduler, error) {
	s, ok := f.clusters[cluster]
	if !ok {
		return nil, fmt.Errorf("fanoutScheduler: unknown cluster %q", cluster)
	}
	return s, nil
}

func (f *fanoutScheduler) DeployScenario(ctx context.Context, spec ports.DeploySpec) error {
	s, err := f.sched(spec.Cluster)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.deployLog = append(f.deployLog, spec)
	f.mu.Unlock()
	return s.DeployScenario(ctx, spec)
}

func (f *fanoutScheduler) ExecutionStatus(ctx context.Context, cluster ports.ClusterRef, executionID int64, scenarios []ports.ScenarioRef) (ports.ExecutionStatus, error) {
	s, err := f.sched(cluster)
	if err != nil {
		return ports.ExecutionStatus{}, err
	}
	return s.ExecutionStatus(ctx, cluster, executionID, scenarios)
}

func (f *fanoutScheduler) EngineDetail(ctx context.Context, cluster ports.ClusterRef, projectID, executionID int64) (ports.ExecutionDetail, error) {
	s, err := f.sched(cluster)
	if err != nil {
		return ports.ExecutionDetail{}, err
	}
	return s.EngineDetail(ctx, cluster, projectID, executionID)
}

func (f *fanoutScheduler) PurgeExecution(ctx context.Context, cluster ports.ClusterRef, executionID int64) error {
	s, err := f.sched(cluster)
	if err != nil {
		return err
	}
	return s.PurgeExecution(ctx, cluster, executionID)
}

func (f *fanoutScheduler) PodLog(ctx context.Context, cluster ports.ClusterRef, executionID, scenarioID int64, shard int) (string, error) {
	s, err := f.sched(cluster)
	if err != nil {
		return "", err
	}
	return s.PodLog(ctx, cluster, executionID, scenarioID, shard)
}

func (f *fanoutScheduler) DeployedExecutions(ctx context.Context, cluster ports.ClusterRef) (map[int64]time.Time, error) {
	s, err := f.sched(cluster)
	if err != nil {
		return nil, err
	}
	return s.DeployedExecutions(ctx, cluster)
}

func (f *fanoutScheduler) NodePools(ctx context.Context, cluster ports.ClusterRef) ([]ports.NodePool, error) {
	s, err := f.sched(cluster)
	if err != nil {
		return nil, err
	}
	return s.NodePools(ctx, cluster)
}

// deploysFor returns the deploy specs recorded for one cluster.
func (f *fanoutScheduler) deploysFor(cluster ports.ClusterRef) []ports.DeploySpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ports.DeploySpec
	for _, d := range f.deployLog {
		if d.Cluster == cluster {
			out = append(out, d)
		}
	}
	return out
}

// usageRecorder captures the usage launch a Trigger opens.
type usageRecorder struct {
	mu      sync.Mutex
	started map[int64]int // executionID -> engines
}

func newUsageRecorder() *usageRecorder {
	return &usageRecorder{started: map[int64]int{}}
}

func (u *usageRecorder) RecordStart(_ context.Context, executionID int64, _ string, engines, _ int) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.started[executionID] = engines
	return nil
}

func (u *usageRecorder) RecordFinish(context.Context, int64, int) error { return nil }

func (u *usageRecorder) enginesFor(executionID int64) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.started[executionID]
}

// setupFanOut seeds a project, a two-target fan-out execution, and one
// native scenario with `engines` shards, over a two-cluster scheduler.
func setupFanOut(t *testing.T, engines int) (*fanoutScheduler, *usageRecorder, *lifecycleapp.Service, *fake.Store, int64) {
	t.Helper()
	ctx := context.Background()
	store := fake.NewStore()

	p, _ := project.New("web", "honryu", "")
	projectID, _ := store.CreateProject(ctx, p)
	coll, _ := execution.New("everywhere", projectID)
	coll.FanOutTargets = []string{"eu-1", "us-1"}
	executionID, _ := store.CreateExecution(ctx, coll)

	obj := fake.NewObjectStore()
	pl, _ := scenario.NewNative("scenario", projectID, taurus.ExecutorJMeter)
	scenarioID, _ := store.CreateScenario(ctx, pl)
	if err := store.AddScenarioFile(ctx, scenarioID, "test.jmx", true); err != nil {
		t.Fatalf("add test file: %v", err)
	}
	if err := obj.Upload(ctx, fmt.Sprintf("scenario/%d/test.jmx", scenarioID), strings.NewReader("<jmx/>")); err != nil {
		t.Fatalf("upload test file: %v", err)
	}
	if err := store.StoreLoadProfile(ctx, executionID, false, []loadprofile.Entry{
		{Name: "p", ScenarioID: scenarioID, Concurrency: 10, Rampup: 1, Engines: engines, Duration: 30},
	}); err != nil {
		t.Fatalf("store load profile: %v", err)
	}

	sched := newFanoutScheduler("eu-1", "us-1")
	usage := newUsageRecorder()
	svc := lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage(image)).WithUsage(usage)
	return sched, usage, svc, store, executionID
}

// A fan-out deploy puts the FULL shard set on EVERY target cluster: N
// clusters × S shards = N×S pods, the "run everywhere" duplication.
func TestFanOut_DeployDuplicatesFullShardSetPerTarget(t *testing.T) {
	t.Parallel()
	sched, _, svc, _, executionID := setupFanOut(t, 2)
	ctx := context.Background()

	if err := svc.Deploy(ctx, executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	for _, cluster := range []ports.ClusterRef{"eu-1", "us-1"} {
		deploys := sched.deploysFor(cluster)
		if len(deploys) != 1 {
			t.Fatalf("cluster %q saw %d deploys, want 1", cluster, len(deploys))
		}
		if len(deploys[0].Shards) != 2 {
			t.Errorf("cluster %q deploy carries %d shards, want the full set of 2", cluster, len(deploys[0].Shards))
		}
	}
}

// A fan-out trigger opens ONE run across both clusters: both must be fully
// ready -- a cluster whose pods never came up refuses the trigger even
// though the summed pool would cover the profile -- and the usage launch
// records the pods that actually exist (shards × targets).
func TestFanOut_TriggerRequiresEveryClusterReady(t *testing.T) {
	t.Parallel()
	sched, usage, svc, store, executionID := setupFanOut(t, 2)
	ctx := context.Background()

	if err := svc.Deploy(ctx, executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	// eu-1's pods never come up: its next status read reports nothing
	// deployed, the "just deployed" startup a real cluster shows.
	sched.clusters["eu-1"].NotReadyCalls = 1

	err := svc.Trigger(ctx, executionID)
	if err == nil {
		t.Fatal("Trigger with one unready cluster succeeded, want refusal")
	}
	if !strings.Contains(err.Error(), "eu-1") || !strings.Contains(err.Error(), run.ErrEnginesNotReady.Error()) {
		t.Fatalf("Trigger err = %v, want fan-out cluster eu-1 named with %v", err, run.ErrEnginesNotReady)
	}
	if _, running, err := store.CurrentRun(ctx, executionID); err != nil || running {
		t.Fatalf("refused trigger left a run open: running=%v err=%v", running, err)
	}

	// Both clusters ready now: one run, opened once, usage sees 2×2 pods.
	if err := svc.Trigger(ctx, executionID); err != nil {
		t.Fatalf("Trigger (both ready): %v", err)
	}
	runID, running, err := store.CurrentRun(ctx, executionID)
	if err != nil || !running {
		t.Fatalf("CurrentRun after trigger: running=%v err=%v", running, err)
	}
	if runID == 0 {
		t.Fatal("fan-out run id = 0, want a real run")
	}
	if got := usage.enginesFor(executionID); got != 4 {
		t.Errorf("usage engines = %d, want 4 (2 shards × 2 targets)", got)
	}
}
