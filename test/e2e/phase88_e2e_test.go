//go:build e2e

// Phase 88's full-stack fan-out pin: a two-cluster scheduler behind the
// real router + MySQL, two BYOC clusters registered over the public API,
// one fan-out execution created with fanout_targets -- then deploy ->
// trigger -> per-cluster ingest -> ONE finalized run whose report carries
// both clusters' shares. Everything between the HTTP client and the JSON
// columns is the production path.
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	membus "github.com/heridotlife/honryu/internal/adapters/eventbus/memory"
	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
	"github.com/heridotlife/honryu/internal/adapters/scheduler/k8s"
	"github.com/heridotlife/honryu/internal/adapters/secretbox"
	"github.com/heridotlife/honryu/internal/adapters/storage/local"
	"github.com/heridotlife/honryu/internal/app/clusterapp"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/app/metricsapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
	"github.com/heridotlife/honryu/test/dbtest"
)

// routerScheduler routes by ClusterRef to one fake.Scheduler per cluster --
// the in-memory stand-in for the k8s Router's per-cluster bound schedulers.
// Without it a single fake.Scheduler would answer for both clusters and the
// per-cluster assertions below would be comparing one pool to itself.
type routerScheduler struct {
	mu       sync.Mutex
	clusters map[ports.ClusterRef]*fake.Scheduler
}

func newRouterScheduler(names ...string) *routerScheduler {
	rs := &routerScheduler{clusters: map[ports.ClusterRef]*fake.Scheduler{}}
	for _, n := range names {
		rs.clusters[ports.ClusterRef(n)] = fake.NewScheduler()
	}
	return rs
}

func (r *routerScheduler) sched(cluster ports.ClusterRef) (*fake.Scheduler, error) {
	s, ok := r.clusters[cluster]
	if !ok {
		return nil, fmt.Errorf("routerScheduler: unknown cluster %q", cluster)
	}
	return s, nil
}

func (r *routerScheduler) DeployScenario(ctx context.Context, spec ports.DeploySpec) error {
	s, err := r.sched(spec.Cluster)
	if err != nil {
		return err
	}
	return s.DeployScenario(ctx, spec)
}

func (r *routerScheduler) ExecutionStatus(ctx context.Context, cluster ports.ClusterRef, executionID int64, scenarios []ports.ScenarioRef) (ports.ExecutionStatus, error) {
	s, err := r.sched(cluster)
	if err != nil {
		return ports.ExecutionStatus{}, err
	}
	return s.ExecutionStatus(ctx, cluster, executionID, scenarios)
}

func (r *routerScheduler) EngineDetail(ctx context.Context, cluster ports.ClusterRef, projectID, executionID int64) (ports.ExecutionDetail, error) {
	s, err := r.sched(cluster)
	if err != nil {
		return ports.ExecutionDetail{}, err
	}
	return s.EngineDetail(ctx, cluster, projectID, executionID)
}

func (r *routerScheduler) PurgeExecution(ctx context.Context, cluster ports.ClusterRef, executionID int64) error {
	s, err := r.sched(cluster)
	if err != nil {
		return err
	}
	return s.PurgeExecution(ctx, cluster, executionID)
}

func (r *routerScheduler) PodLog(ctx context.Context, cluster ports.ClusterRef, executionID, scenarioID int64, shard int) (string, error) {
	s, err := r.sched(cluster)
	if err != nil {
		return "", err
	}
	return s.PodLog(ctx, cluster, executionID, scenarioID, shard)
}

func (r *routerScheduler) DeployedExecutions(ctx context.Context, cluster ports.ClusterRef) (map[int64]time.Time, error) {
	s, err := r.sched(cluster)
	if err != nil {
		return nil, err
	}
	return s.DeployedExecutions(ctx, cluster)
}

func (r *routerScheduler) NodePools(ctx context.Context, cluster ports.ClusterRef) ([]ports.NodePool, error) {
	s, err := r.sched(cluster)
	if err != nil {
		return nil, err
	}
	return s.NodePools(ctx, cluster)
}

type phase88Env struct {
	client *http.Client
	url    string
	repo   *mysqladapter.Repository
	sched  *routerScheduler
}

// setupPhase88 boots the stack over a two-cluster router: real MySQL, real
// router, real cluster registration (phase12's memoryCredStore seam); only
// the k8s API is faked, per cluster.
func setupPhase88(t *testing.T) *phase88Env {
	t.Helper()
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	store := local.New(t.TempDir(), "")
	sched := newRouterScheduler("eu-1", "us-1")
	sink := fake.NewMetricsSink()
	bus := membus.New()

	collector := metricsapp.NewService(repo, sink, bus, repo, repo)
	lifecycle := lifecycleapp.NewService(repo, sched, store, lifecycleapp.StaticImage("jmeter")).WithMetrics(collector)

	cipher, err := secretbox.NewFromHex(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("secretbox: %v", err)
	}
	clusterSvc := clusterapp.NewService(clusterapp.Deps{
		Registry:    repo,
		Prober:      &fake.ClusterProber{},
		Credentials: &memoryCredStore{},
		Runs:        repo,
		Cipher:      cipher,
		Parse:       k8s.ParsePortsKubeconfig,
		SecretName:  k8s.CredentialSecretName,
	})

	router := httpapi.NewRouter(httpapi.Deps{
		Projects:         projectapp.NewService(repo),
		Scenarios:        scenarioapp.NewService(repo, store),
		Executions:       executionapp.NewService(repo, store, 500),
		Lifecycle:        lifecycle,
		Store:            store,
		Metrics:          collector,
		Reports:          repo,
		IngestToken:      "engine-token",
		Clusters:         clusterSvc,
		IngestTokens:     repo,
		ExecutionCluster: repo,
		FanOutRuns:       repo,
		DefaultOwners:    []string{"honryu"},
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return &phase88Env{client: srv.Client(), url: srv.URL, repo: repo, sched: sched}
}

// registerBYOC registers one cluster through the public API, returning its
// one-time ingest token (phase12Env's helper over this env's client/url).
func (e *phase88Env) registerBYOC(t *testing.T, name string) string {
	t.Helper()
	return (&phase12Env{client: e.client, url: e.url}).registerBYOC(t, name)
}

// clusterFinal pushes one cluster's final shard batch under that cluster's
// own ingest token -- the cluster dimension arrives stamped by the
// control plane from the token, never from the body.
func (e *phase88Env) clusterFinal(t *testing.T, token, stream string, executionID, scenarioID, runID, samples int64) {
	t.Helper()
	exit := 0
	body, _ := json.Marshal(metrics.Batch{
		ExecutionID: executionID, ScenarioID: scenarioID, RunID: runID,
		ShardIndex: 0, StreamID: stream, Final: true, ExitCode: &exit,
		Intervals: []metrics.Interval{{
			Seq: 1, Timestamp: 1000, Label: "checkout", Concurrency: 1,
			Samples: samples, Succeeded: samples,
			Latency: metrics.Histogram{0.01: samples},
		}},
	})
	req, err := http.NewRequest(http.MethodPost, e.url+"/api/ingest", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("build ingest request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("ingest (%s): %v", stream, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest (%s) = %d, want 202", stream, resp.StatusCode)
	}
}

// clustersFanOutRunning reads GET /api/clusters and maps cluster name ->
// its fanout_running list (absent field or empty list both omitted).
func (e *phase88Env) clustersFanOutRunning(t *testing.T) map[string][]struct {
	ExecutionID int64  `json:"execution_id"`
	Name        string `json:"name"`
} {
	t.Helper()
	resp, err := e.client.Get(e.url + "/api/clusters")
	if err != nil {
		t.Fatalf("GET /api/clusters: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/clusters = %d", resp.StatusCode)
	}
	var rows []struct {
		Name          string `json:"name"`
		FanOutRunning *[]struct {
			ExecutionID int64  `json:"execution_id"`
			Name        string `json:"name"`
		} `json:"fanout_running"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatalf("decode clusters: %v", err)
	}
	out := map[string][]struct {
		ExecutionID int64  `json:"execution_id"`
		Name        string `json:"name"`
	}{}
	for _, r := range rows {
		if r.FanOutRunning != nil && len(*r.FanOutRunning) > 0 {
			out[r.Name] = *r.FanOutRunning
		}
	}
	return out
}

// The fan-out happy path, end to end: full shard set on both clusters, ONE
// run id, per-cluster ingest under per-cluster tokens, a single report
// carrying both clusters' shares, and the Clusters page's mid-run view.
func TestPhase88_FanOutRunEndToEnd(t *testing.T) {
	e := setupPhase88(t)
	euToken := e.registerBYOC(t, "eu-1")
	usToken := e.registerBYOC(t, "us-1")

	// Seed: project + native scenario + fan-out execution, all over HTTP.
	projectID := postForm(t, e.client, e.url+"/api/projects", url.Values{"name": {"fanout-e2e"}, "owner": {"honryu"}})
	scenarioID := postForm(t, e.client, e.url+"/api/scenarios", url.Values{"name": {"checkout"}, "project_id": {itoa(projectID)}})
	putMultipart(t, e.client, e.url+"/api/scenarios/"+itoa(scenarioID)+"/files", "s.jmx", "<jmx/>")
	executionID := postForm(t, e.client, e.url+"/api/executions", url.Values{
		"name":           {"everywhere"},
		"project_id":     {itoa(projectID)},
		"fanout_targets": {`["eu-1","us-1"]`},
	})
	putMultipart(t, e.client, e.url+"/api/executions/"+itoa(executionID)+"/config", "config.yaml",
		minimalConfig(executionID, scenarioID, ""))

	base := e.url + "/api/executions/" + itoa(executionID)
	postAction(t, e.client, base+"/deploy", http.StatusOK)

	// Per-cluster status scoping: each target answers with its OWN pod
	// (engines: 1 -> one shard per cluster), and a cluster the execution
	// does not run on is a 404.
	var st struct {
		PoolSize int `json:"pool_size"`
	}
	getJSON(t, e.client, base+"/status?cluster=eu-1", http.StatusOK, &st)
	if st.PoolSize != 1 {
		t.Fatalf("eu-1 pool = %d, want 1 (its own shard)", st.PoolSize)
	}
	getJSON(t, e.client, base+"/status?cluster=us-1", http.StatusOK, &st)
	if st.PoolSize != 1 {
		t.Fatalf("us-1 pool = %d, want 1", st.PoolSize)
	}
	getJSON(t, e.client, base+"/status", http.StatusOK, &st)
	if st.PoolSize != 2 {
		t.Fatalf("aggregate pool = %d, want 2 (1 shard x 2 targets)", st.PoolSize)
	}
	getJSON(t, e.client, base+"/status?cluster=ap-1", http.StatusNotFound, nil)

	// No run open yet: no cluster reports a fan-out in progress.
	if running := e.clustersFanOutRunning(t); len(running) != 0 {
		t.Fatalf("fanout_running before trigger = %v, want none", running)
	}

	postAction(t, e.client, base+"/trigger", http.StatusOK)
	runID, running, err := e.repo.CurrentRun(context.Background(), executionID)
	if err != nil || !running {
		t.Fatalf("CurrentRun after trigger: running=%v err=%v", running, err)
	}

	// Mid-run: BOTH target clusters list the fan-out execution.
	runningByCluster := e.clustersFanOutRunning(t)
	for _, cluster := range []string{"eu-1", "us-1"} {
		rows, ok := runningByCluster[cluster]
		if !ok || len(rows) != 1 || rows[0].ExecutionID != executionID || rows[0].Name != "everywhere" {
			t.Fatalf("cluster %s fanout_running = %v, want the everywhere execution", cluster, rows)
		}
	}

	// Each cluster's pod pushes its final under its OWN token: the same
	// shard index, different streams, cluster stamped from the token.
	e.clusterFinal(t, euToken, "eu-s0", executionID, scenarioID, runID, 100)
	e.clusterFinal(t, usToken, "us-s0", executionID, scenarioID, runID, 50)

	// ONE run, finalized, with the per-cluster split readable.
	rep, err := e.repo.GetReport(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if len(rep.ClusterResults) != 2 {
		t.Fatalf("cluster_results = %+v, want one row per target cluster", rep.ClusterResults)
	}
	byCluster := map[string]int64{}
	for _, row := range rep.ClusterResults {
		byCluster[row.Cluster] = row.Samples
	}
	if byCluster["eu-1"] != 100 || byCluster["us-1"] != 50 {
		t.Fatalf("per-cluster samples = %v, want eu-1:100 us-1:50", byCluster)
	}
	if rep.Achieved.Samples != 150 {
		t.Fatalf("aggregate samples = %d, want 150 (both clusters under one run)", rep.Achieved.Samples)
	}

	// The run closed: the mid-run view drains.
	if running := e.clustersFanOutRunning(t); len(running) != 0 {
		t.Fatalf("fanout_running after finalize = %v, want none", running)
	}
}
