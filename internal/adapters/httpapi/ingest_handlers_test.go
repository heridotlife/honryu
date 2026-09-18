package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	membus "github.com/heridotlife/honryu/internal/adapters/eventbus/memory"
	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/authapp"
	"github.com/heridotlife/honryu/internal/app/metricsapp"
	"github.com/heridotlife/honryu/internal/domain/clusterregistry"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

const engineToken = "engine-s3cret"

type ingestEnv struct {
	h           http.Handler
	sink        *fake.MetricsSink
	store       *fake.Store
	executionID int64
	scenarioID  int64
	runID       int64
}

func newIngestEnv(t *testing.T, deps func(*httpapi.Deps, *fake.Store)) ingestEnv {
	t.Helper()
	ctx := context.Background()
	store := fake.NewStore()

	exe, _ := execution.New("peak", 1)
	executionID, _ := store.CreateExecution(ctx, exe)
	sc, _ := scenario.New("probe", 1)
	scenarioID, _ := store.CreateScenario(ctx, sc)
	_ = store.StoreLoadProfile(ctx, executionID, false, []loadprofile.Entry{
		{ScenarioID: scenarioID, Concurrency: 1, Rampup: 1, Engines: 1, Duration: 10},
	})
	runID, _ := store.StartRun(ctx, executionID, "")

	sink := fake.NewMetricsSink()
	d := httpapi.Deps{
		Metrics:     metricsapp.NewService(store, sink, membus.New(), fake.NewReportProgress(), fake.NewReportStore()),
		IngestToken: engineToken,
	}
	if deps != nil {
		deps(&d, store)
	}
	return ingestEnv{h: httpapi.NewRouter(d), sink: sink, store: store, executionID: executionID, scenarioID: scenarioID, runID: runID}
}

// newByocEnv is an ingest environment with the per-cluster credential path
// wired: the fake store resolves token hashes and loads executions, exactly as
// cmd/api wires the mysql repository.
func newByocEnv(t *testing.T) ingestEnv {
	t.Helper()
	return newIngestEnv(t, func(d *httpapi.Deps, store *fake.Store) {
		d.IngestTokens = store
		d.ExecutionCluster = store
	})
}

// registerCluster registers a BYOC cluster holding token, returning its name.
func (e ingestEnv) registerCluster(t *testing.T, name, token string) string {
	t.Helper()
	c := clusterregistry.Cluster{
		Name: name, Origin: clusterregistry.OriginBYOC, Namespace: "honryu",
		IngestURL: "https://ingest.example", SidecarImage: "sidecar:1", SecretRef: "sec-" + name,
	}
	if err := e.store.CreateCluster(context.Background(), c); err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	if err := e.store.SetClusterIngestTokenHash(context.Background(), name, clusterregistry.HashToken(token)); err != nil {
		t.Fatalf("set token hash: %v", err)
	}
	return name
}

// routeExecution creates an execution routed to cluster ("" = default fleet)
// with an active run, and returns a valid batch for it.
func (e ingestEnv) routeExecution(t *testing.T, cluster string) metrics.Batch {
	t.Helper()
	ctx := context.Background()
	exe, err := execution.New("routed", 1)
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	exe.Cluster = cluster
	id, err := e.store.CreateExecution(ctx, exe)
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}
	_ = e.store.StoreLoadProfile(ctx, id, false, []loadprofile.Entry{
		{ScenarioID: e.scenarioID, Concurrency: 1, Rampup: 1, Engines: 1, Duration: 10},
	})
	runID, err := e.store.StartRun(ctx, id, "")
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	return metrics.Batch{ExecutionID: id, ScenarioID: e.scenarioID, RunID: runID, Intervals: []metrics.Interval{{
		Seq: 1, Timestamp: 1, Label: "probe", Concurrency: 2, Samples: 9, Succeeded: 9,
		Latency: metrics.Histogram{0.01: 9},
	}}}
}

func postBatch(t *testing.T, h http.Handler, token string, b metrics.Batch) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func (e ingestEnv) batch() metrics.Batch {
	return metrics.Batch{
		ExecutionID: e.executionID, ScenarioID: e.scenarioID, RunID: e.runID,
		Intervals: []metrics.Interval{{
			Seq: 1, Timestamp: 1, Label: "probe", Concurrency: 2, Samples: 9, Succeeded: 9,
			Latency: metrics.Histogram{0.01: 9},
		}},
	}
}

func TestIngestHTTP_AcceptsABatch(t *testing.T) {
	t.Parallel()
	e := newIngestEnv(t, nil)

	rec := postBatch(t, e.h, engineToken, e.batch())
	if rec.Code != http.StatusAccepted {
		t.Fatalf("ingest = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := len(e.sink.Recorded()); got != 1 {
		t.Errorf("sink recorded %d measurements, want 1", got)
	}
}

func TestIngestHTTP_RejectsBadCredentials(t *testing.T) {
	t.Parallel()
	e := newIngestEnv(t, nil)

	for _, tc := range []struct{ name, token string }{
		{"no token", ""},
		{"wrong token", "not-the-token"},
		{"almost right", engineToken + "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := postBatch(t, e.h, tc.token, e.batch())
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("ingest with %s = %d, want 401", tc.name, rec.Code)
			}
		})
	}
	if got := len(e.sink.Recorded()); got != 0 {
		t.Errorf("unauthenticated pushes contributed %d measurements", got)
	}
}

// A deployment that never configured a credential must reject pushes rather than
// accept anonymous ones.
func TestIngestHTTP_ClosedWhenNoTokenIsConfigured(t *testing.T) {
	t.Parallel()
	e := newIngestEnv(t, func(d *httpapi.Deps, _ *fake.Store) { d.IngestToken = "" })

	if rec := postBatch(t, e.h, "", e.batch()); rec.Code != http.StatusUnauthorized {
		t.Errorf("ingest with no configured token = %d, want 401", rec.Code)
	}
	if rec := postBatch(t, e.h, engineToken, e.batch()); rec.Code != http.StatusUnauthorized {
		t.Errorf("ingest with a guessed token = %d, want 401", rec.Code)
	}
}

// Engine pods hold no user account. With RBAC enabled the user middleware would
// reject every push before it reached the handler, which would silently stop all
// metrics the moment a deployment turned auth on.
func TestIngestHTTP_WorksWhenRBACIsEnabled(t *testing.T) {
	t.Parallel()
	e := newIngestEnv(t, func(d *httpapi.Deps, _ *fake.Store) {
		d.Auth = authapp.NewService(nil, nil, true)
	})

	rec := postBatch(t, e.h, engineToken, e.batch())
	if rec.Code != http.StatusAccepted {
		t.Fatalf("ingest with RBAC enabled = %d (%s), want 202", rec.Code, rec.Body.String())
	}
}

func TestIngestHTTP_RejectsMalformedBodies(t *testing.T) {
	t.Parallel()
	e := newIngestEnv(t, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/ingest", bytes.NewReader([]byte("{not json")))
	req.Header.Set("Authorization", "Bearer "+engineToken)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body = %d, want 400", rec.Code)
	}

	// A batch that names no execution cannot be attributed to anything.
	if rec := postBatch(t, e.h, engineToken, metrics.Batch{}); rec.Code != http.StatusBadRequest {
		t.Errorf("batch with no execution = %d, want 400", rec.Code)
	}
}

// The pod retries a push it could not complete, so the same batch arrives twice.
func TestIngestHTTP_IsIdempotent(t *testing.T) {
	t.Parallel()
	e := newIngestEnv(t, nil)

	for i := 0; i < 3; i++ {
		if rec := postBatch(t, e.h, engineToken, e.batch()); rec.Code != http.StatusAccepted {
			t.Fatalf("push %d = %d", i, rec.Code)
		}
	}
	if got := len(e.sink.Recorded()); got != 1 {
		t.Errorf("sink recorded %d measurements after three identical pushes, want 1", got)
	}
}

// A pod that outlived its run keeps pushing until it is killed. That is a state
// conflict, not a server fault: reporting 500 would fill the logs with errors
// that are nobody's bug and hide the real ones.
func TestIngestHTTP_StoppedRunIsAConflictNotAServerError(t *testing.T) {
	t.Parallel()
	e := newIngestEnv(t, nil)

	stale := e.batch()
	stale.RunID = e.runID + 42
	if rec := postBatch(t, e.h, engineToken, stale); rec.Code != http.StatusConflict {
		t.Errorf("push for a finished run = %d, want 409", rec.Code)
	}
}

// A cluster token is scoped: it accepts exactly the batches of executions
// routed to its own cluster. Everything else about the push being well-formed,
// the credential simply does not speak for another fleet.
func TestIngestHTTP_ClusterTokenAcceptsItsOwnExecution(t *testing.T) {
	t.Parallel()
	e := newByocEnv(t)
	e.registerCluster(t, "byoc-a", "tok-a")
	b := e.routeExecution(t, "byoc-a")

	if rec := postBatch(t, e.h, "tok-a", b); rec.Code != http.StatusAccepted {
		t.Fatalf("cluster token push = %d (%s), want 202", rec.Code, rec.Body.String())
	}
	if got := len(e.sink.Recorded()); got != 1 {
		t.Errorf("sink recorded %d measurements, want 1", got)
	}
}

// 401, not 403: an unknown token reveals nothing about which clusters exist,
// and a scoped rejection would be an oracle for names worth guessing.
func TestIngestHTTP_UnknownClusterTokenIsUnauthorized(t *testing.T) {
	t.Parallel()
	e := newByocEnv(t)
	e.registerCluster(t, "byoc-a", "tok-a")

	for _, tc := range []struct{ name, token string }{
		{"never minted", "tok-never-minted"},
		{"wrong cluster", "tok-b-when-only-a-registered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := postBatch(t, e.h, tc.token, e.batch())
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("push with %s = %d, want 401", tc.name, rec.Code)
			}
		})
	}
}

// A cluster token must not carry measurements for executions it does not own:
// neither the default fleet's nor a rival cluster's. The push is
// well-authenticated but out of scope -- 403, not 401, so the operator sees a
// misrouting rather than a guessed credential.
func TestIngestHTTP_ClusterTokenRejectsOutOfWorkScope(t *testing.T) {
	t.Parallel()
	e := newByocEnv(t)
	e.registerCluster(t, "byoc-a", "tok-a")
	e.registerCluster(t, "byoc-b", "tok-b")

	defaultBatch := e.routeExecution(t, "")
	bOnA := e.routeExecution(t, "byoc-a")
	bOnB := e.routeExecution(t, "byoc-b")

	for _, tc := range []struct {
		name  string
		token string
		batch metrics.Batch
	}{
		{"default-fleet execution", "tok-a", defaultBatch},
		{"rival cluster execution", "tok-a", bOnB},
		{"reverse direction", "tok-b", bOnA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := postBatch(t, e.h, tc.token, tc.batch)
			if rec.Code != http.StatusForbidden {
				t.Errorf("push with %s = %d (%s), want 403", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
	if got := len(e.sink.Recorded()); got != 0 {
		t.Errorf("out-of-scope pushes contributed %d measurements", got)
	}
}

// Rotation overwrites the hash, so the old token stops resolving the moment
// the new one is stored -- there is no window where both work.
func TestIngestHTTP_RotationKillsTheOldToken(t *testing.T) {
	t.Parallel()
	e := newByocEnv(t)
	e.registerCluster(t, "byoc-a", "tok-old")
	b := e.routeExecution(t, "byoc-a")

	if rec := postBatch(t, e.h, "tok-old", b); rec.Code != http.StatusAccepted {
		t.Fatalf("old token before rotation = %d, want 202", rec.Code)
	}

	if err := e.store.SetClusterIngestTokenHash(context.Background(), "byoc-a", clusterregistry.HashToken("tok-new")); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rec := postBatch(t, e.h, "tok-old", b); rec.Code != http.StatusUnauthorized {
		t.Errorf("old token after rotation = %d, want 401", rec.Code)
	}
	if rec := postBatch(t, e.h, "tok-new", b); rec.Code != http.StatusAccepted {
		t.Errorf("new token after rotation = %d, want 202", rec.Code)
	}
}

// The deployment-wide engine credential stays exactly as privileged as before
// per-cluster tokens existed: it speaks for every fleet, including BYOC ones.
func TestIngestHTTP_GlobalTokenStillReachesByocExecutions(t *testing.T) {
	t.Parallel()
	e := newByocEnv(t)
	e.registerCluster(t, "byoc-a", "tok-a")
	b := e.routeExecution(t, "byoc-a")

	if rec := postBatch(t, e.h, engineToken, b); rec.Code != http.StatusAccepted {
		t.Errorf("global token push for byoc execution = %d, want 202", rec.Code)
	}
}

// --- Fan-out ingest scoping (phase 88) ----------------------------------------

// fanOutExecution creates a fan-out execution over targets with an active
// run, and returns a valid batch for it.
func (e ingestEnv) fanOutExecution(t *testing.T, targets ...string) metrics.Batch {
	t.Helper()
	ctx := context.Background()
	exe, err := execution.New("fanout", 1)
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	exe.FanOutTargets = targets
	id, err := e.store.CreateExecution(ctx, exe)
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}
	_ = e.store.StoreLoadProfile(ctx, id, false, []loadprofile.Entry{
		{ScenarioID: e.scenarioID, Concurrency: 1, Rampup: 1, Engines: 1, Duration: 10},
	})
	runID, err := e.store.StartRun(ctx, id, "")
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	return metrics.Batch{ExecutionID: id, ScenarioID: e.scenarioID, RunID: runID, Intervals: []metrics.Interval{{
		Seq: 1, Timestamp: 1, Label: "probe", Concurrency: 2, Samples: 9, Succeeded: 9,
		Latency: metrics.Histogram{0.01: 9},
	}}}
}

// A fan-out target cluster's token speaks for the execution exactly as a
// single-cluster match does: every target's engines must be able to push or
// no fan-out run could ever complete.
func TestIngestHTTP_ClusterTokenAcceptsFanOutTarget(t *testing.T) {
	t.Parallel()
	e := newByocEnv(t)
	e.registerCluster(t, "eu-1", "tok-eu")
	e.registerCluster(t, "us-1", "tok-us")
	b := e.fanOutExecution(t, "eu-1", "us-1")

	for _, token := range []string{"tok-eu", "tok-us"} {
		if rec := postBatch(t, e.h, token, b); rec.Code != http.StatusAccepted {
			t.Fatalf("cluster token %q fan-out push status = %d, want 202 (%s)", token, rec.Code, rec.Body.String())
		}
	}
}

// A cluster that is NOT a target still cannot speak for the execution: a
// rival cluster's token is scoped to its own executions, fan-out or not.
func TestIngestHTTP_ClusterTokenRejectsNonTargetCluster(t *testing.T) {
	t.Parallel()
	e := newByocEnv(t)
	e.registerCluster(t, "eu-1", "tok-eu")
	e.registerCluster(t, "rival", "tok-rival")
	b := e.fanOutExecution(t, "eu-1")

	if rec := postBatch(t, e.h, "tok-rival", b); rec.Code != http.StatusForbidden {
		t.Fatalf("rival token fan-out push status = %d, want 403", rec.Code)
	}
}
