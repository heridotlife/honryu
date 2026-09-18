package lifecycleapp_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/reservation"
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

// setupFanOut seeds a project, a two-target tenanted fan-out execution, and
// one native scenario with `engines` shards, over a two-cluster scheduler.
// The object store is returned too: a purge's log-capture assertions read the
// cluster-qualified keys the capture wrote.
func setupFanOut(t *testing.T, engines int) (*fanoutScheduler, *usageRecorder, *lifecycleapp.Service, *fake.Store, *fake.ObjectStore, int64, int64) {
	t.Helper()
	ctx := context.Background()
	store := fake.NewStore()

	p, _ := project.New("web", "honryu", "")
	projectID, _ := store.CreateProject(ctx, p)
	coll, _ := execution.New("everywhere", projectID)
	coll.FanOutTargets = []string{"eu-1", "us-1"}
	// Tenanted, so Trigger's quota path engages (multi-tenancy is opt-in).
	tenant := int64(7)
	coll.TenantID = &tenant
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
	return sched, usage, svc, store, obj, executionID, scenarioID
}

// A fan-out deploy puts the FULL shard set on EVERY target cluster: N
// clusters × S shards = N×S pods, the "run everywhere" duplication.
func TestFanOut_DeployDuplicatesFullShardSetPerTarget(t *testing.T) {
	t.Parallel()
	sched, _, svc, _, _, executionID, _ := setupFanOut(t, 2)
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
	sched, usage, svc, store, _, executionID, _ := setupFanOut(t, 2)
	ctx := context.Background()

	if err := svc.Deploy(ctx, executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	// eu-1's pods never come up: its next status reads report nothing
	// deployed, the "just deployed" startup a real cluster shows. Two reads
	// are consumed per Trigger attempt (the summed status, then the
	// per-cluster readiness gate) -- both must see the gap.
	sched.clusters["eu-1"].NotReadyCalls = 2

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

// quotaRecorder records the per-cluster reservations Trigger makes, and can
// be told to refuse one cluster -- the partial-fan-out admission failure.
type quotaRecorder struct {
	mu       sync.Mutex
	reserved []string // cluster names, in admission order
	released int
	failOn   string
	failErr  error
}

func (q *quotaRecorder) Reserve(_ context.Context, _ int64, cluster string, _ int, _, _ time.Time, _ int64) (reservation.Reservation, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.failOn == cluster {
		return reservation.Reservation{}, q.failErr
	}
	q.reserved = append(q.reserved, cluster)
	return reservation.Reservation{}, nil
}

func (q *quotaRecorder) Release(_ context.Context, _ int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.released++
	return nil
}

// A fan-out trigger reserves quota on EVERY target cluster -- a tenant's
// ceiling is per cluster, and the duplicated shard set occupies each target's
// capacity. A refusal partway releases the reservations already made, so a
// partial fan-out holds no capacity it does not use.
func TestFanOut_QuotaReservedPerTargetCluster(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	over := errors.New("quota exceeded")
	cases := []struct {
		name      string
		failOn    string
		wantRefs  []string
		wantRel   int
		wantPanic string
	}{
		{name: "both admitted", wantRefs: []string{"eu-1", "us-1"}},
		{name: "second refused", failOn: "us-1", wantRefs: []string{"eu-1"}, wantRel: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sched, _, svc, store, _, executionID, _ := setupFanOut(t, 1)
			_ = sched
			quota := &quotaRecorder{failOn: tc.failOn, failErr: over}
			svc.WithQuota(quota)

			if tc.failOn == "" {
				if err := svc.Deploy(ctx, executionID); err != nil {
					t.Fatalf("Deploy: %v", err)
				}
			} else {
				// Deploy first, then break only the second admission.
				if err := svc.Deploy(ctx, executionID); err != nil {
					t.Fatalf("Deploy: %v", err)
				}
				quota.mu.Lock()
				quota.failOn = tc.failOn
				quota.failErr = over
				quota.mu.Unlock()
			}
			err := svc.Trigger(ctx, executionID)
			if tc.failOn != "" {
				if !errors.Is(err, over) {
					t.Fatalf("Trigger err = %v, want the quota refusal", err)
				}
			} else if err != nil {
				t.Fatalf("Trigger: %v", err)
			}
			if got, want := strings.Join(quota.reserved, ","), strings.Join(tc.wantRefs, ","); got != want {
				t.Fatalf("reserved clusters = %q, want %q", got, want)
			}
			if quota.released != tc.wantRel {
				t.Errorf("releases = %d, want %d", quota.released, tc.wantRel)
			}
			if tc.failOn != "" {
				if _, running, rerr := store.CurrentRun(ctx, executionID); rerr != nil || running {
					t.Fatalf("refused trigger left a run open: running=%v err=%v", running, rerr)
				}
			}
		})
	}
}

// A fan-out purge removes engines on EVERY target cluster, and captures each
// cluster's shard logs under its own cluster-qualified key.
func TestFanOut_PurgeCleansEveryTargetCluster(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sched, _, svc, store, obj, executionID, scenarioID := setupFanOut(t, 1)

	if err := svc.Deploy(ctx, executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := svc.Trigger(ctx, executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	runID, _, err := store.CurrentRun(ctx, executionID)
	if err != nil {
		t.Fatalf("CurrentRun: %v", err)
	}

	if err := svc.Purge(ctx, executionID); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	for _, cluster := range []ports.ClusterRef{"eu-1", "us-1"} {
		deployed, err := sched.DeployedExecutions(ctx, cluster)
		if err != nil {
			t.Fatalf("DeployedExecutions(%s): %v", cluster, err)
		}
		if len(deployed) != 0 {
			t.Errorf("cluster %q still has %d deployed executions after purge", cluster, len(deployed))
		}
	}
	// Each cluster's shard log lands under its own key: the same shard index
	// exists on both clusters, and a shared key would let one overwrite the
	// other's evidence.
	eu := lifecycleapp.RunShardClusterKey(runID, scenarioID, 0, "eu-1", "log")
	us := lifecycleapp.RunShardClusterKey(runID, scenarioID, 0, "us-1", "log")
	if _, err := obj.Download(ctx, eu); err != nil {
		t.Errorf("eu-1 log capture missing: %v", err)
	}
	if _, err := obj.Download(ctx, us); err != nil {
		t.Errorf("us-1 log capture missing: %v", err)
	}
}

// Status sums a fan-out execution's pool across its target clusters, without
// the per-cluster readiness gate a trigger pays for -- a half-deployed
// fan-out is Deploy progress, not a status error.
func TestFanOut_StatusSumsAcrossClusters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, _, svc, _, _, executionID, _ := setupFanOut(t, 2)

	st, err := svc.Status(ctx, executionID)
	if err != nil {
		t.Fatalf("Status before deploy: %v", err)
	}
	if st.PoolSize != 0 {
		t.Fatalf("pool before deploy = %d, want 0", st.PoolSize)
	}

	if err := svc.Deploy(ctx, executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	st, err = svc.Status(ctx, executionID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	// 2 shards × 2 clusters.
	if st.PoolSize != 4 {
		t.Fatalf("pool = %d, want 4", st.PoolSize)
	}
}
