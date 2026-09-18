//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

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

// memoryCredStore stands in for the home-cluster Secret store: BYOC
// registration's materialize/read/delete ride a map instead of a k8s client,
// which e2e has no cluster for. Everything else about registration -- parsing
// the kubeconfig, encrypting it, minting the token, storing the entry -- is the
// production path.
type memoryCredStore struct {
	mu      sync.Mutex
	secrets map[string]ports.ClusterCredential
}

func (m *memoryCredStore) Materialize(_ context.Context, name string, cred ports.ClusterCredential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.secrets == nil {
		m.secrets = map[string]ports.ClusterCredential{}
	}
	m.secrets[name] = cred
	return nil
}

func (m *memoryCredStore) Read(_ context.Context, name string) (ports.ClusterCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cred, ok := m.secrets[name]
	if !ok {
		return ports.ClusterCredential{}, k8s.ErrCredentialSecretNotFound
	}
	return cred, nil
}

func (m *memoryCredStore) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.secrets, name)
	return nil
}

type phase12Env struct {
	client *http.Client
	url    string
	repo   *mysqladapter.Repository
}

func setupPhase12(t *testing.T) *phase12Env {
	t.Helper()
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	store := local.New(t.TempDir(), "")
	sched := fake.NewScheduler()
	sink := fake.NewMetricsSink()
	bus := membus.New()

	collector := metricsapp.NewService(repo, sink, bus, repo, repo)
	lifecycle := lifecycleapp.NewService(repo, sched, store, lifecycleapp.StaticImage("jmeter")).WithMetrics(collector)

	// The registration seam stays real: production parser, secret namer, and
	// cipher over the real MySQL registry. Only the probe is faked (nothing
	// is reachable to probe in e2e) and the Secret store rides memory.
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
		Projects:     projectapp.NewService(repo),
		Scenarios:    scenarioapp.NewService(repo, store),
		Executions:   executionapp.NewService(repo, store, 500),
		Lifecycle:    lifecycle,
		Store:        store,
		Metrics:      collector,
		Reports:      repo,
		IngestToken:  "engine-token",
		Clusters:     clusterSvc,
		IngestTokens: repo,
		// The execution-cluster lookup the scoping check consults.
		ExecutionCluster: repo,
		DefaultOwners:    []string{"honryu"},
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return &phase12Env{client: srv.Client(), url: srv.URL, repo: repo}
}

// byocKubeconfig builds a minimal self-contained kubeconfig for cluster: the
// parser demands an embedded CA, a server URL, and a static token, and rejects
// nothing else about their contents.
func byocKubeconfig(cluster string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: byoc
clusters:
- name: target
  cluster:
    server: https://%s.example:6443
    certificate-authority-data: Y2EtY2VydA==
contexts:
- name: byoc
  context:
    cluster: target
    user: robot
users:
- name: robot
  user:
    token: e2e-token
`, cluster)
}

// registerBYOC registers a cluster through the public API and returns the
// one-time ingest token.
func (e *phase12Env) registerBYOC(t *testing.T, name string) string {
	t.Helper()
	form := url.Values{
		"name":          {name},
		"ingest_url":    {"http://ingest"},
		"sidecar_image": {"sidecar:1"},
		"namespace":     {"honryu"},
		"kubeconfig":    {byocKubeconfig(name)},
	}
	resp, err := e.client.PostForm(e.url+"/api/clusters", form)
	if err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register %s = %d, want 201", name, resp.StatusCode)
	}
	var out struct {
		Origin      string `json:"origin"`
		IngestToken string `json:"ingest_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode registration: %v", err)
	}
	if out.Origin != "byoc" || out.IngestToken == "" {
		t.Fatalf("registration %s = origin %q token %q, want byoc + minted token", name, out.Origin, out.IngestToken)
	}
	return out.IngestToken
}

// rotateBYOC rotates cluster's ingest token, returning the new one.
func (e *phase12Env) rotateBYOC(t *testing.T, cluster string) string {
	t.Helper()
	resp, err := e.client.Post(e.url+"/api/clusters/"+cluster+"/rotate-ingest-token", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("rotate %s: %v", cluster, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate %s = %d, want 200", cluster, resp.StatusCode)
	}
	var out struct {
		IngestToken string `json:"ingest_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode rotation: %v", err)
	}
	if out.IngestToken == "" {
		t.Fatalf("rotate %s returned no token", cluster)
	}
	return out.IngestToken
}

// seedRoutedExecution prepares a project + jmx scenario + execution routed to
// cluster (empty = default fleet) with a config, deployed and triggered; the
// returned run is open and ready to absorb batches.
func (e *phase12Env) seedRoutedExecution(t *testing.T, name, cluster string) (executionID, scenarioID, runID int64) {
	t.Helper()
	projectID := postForm(t, e.client, e.url+"/api/projects", url.Values{"name": {name}, "owner": {"honryu"}})
	scenarioID = postForm(t, e.client, e.url+"/api/scenarios", url.Values{"name": {"checkout"}, "project_id": {itoa(projectID)}})
	putMultipart(t, e.client, e.url+"/api/scenarios/"+itoa(scenarioID)+"/files", "s.jmx", "<jmx/>")

	form := url.Values{"name": {"run"}, "project_id": {itoa(projectID)}}
	if cluster != "" {
		form["cluster"] = []string{cluster}
	}
	executionID = postForm(t, e.client, e.url+"/api/executions", form)
	putMultipart(t, e.client, e.url+"/api/executions/"+itoa(executionID)+"/config", "config.yaml",
		minimalConfig(executionID, scenarioID, ""))

	base := e.url + "/api/executions/" + itoa(executionID)
	postAction(t, e.client, base+"/deploy", http.StatusOK)
	postAction(t, e.client, base+"/trigger", http.StatusOK)
	runID, running, err := e.repo.CurrentRun(context.Background(), executionID)
	if err != nil || !running {
		t.Fatalf("%s CurrentRun: running=%v err=%v", name, running, err)
	}
	return executionID, scenarioID, runID
}

// pushBatch posts one interval batch as token would present it, returning the
// verdict without asserting -- the matrix itself decides what is expected.
func (e *phase12Env) pushBatch(t *testing.T, token string, executionID, scenarioID, runID int64) int {
	t.Helper()
	body, err := json.Marshal(metrics.Batch{
		ExecutionID: executionID, ScenarioID: scenarioID, RunID: runID,
		ShardIndex: 0, StreamID: "s0",
		Intervals: []metrics.Interval{{
			Seq: 1, Timestamp: 1000, Label: "checkout", Concurrency: 1,
			Samples: 5, Succeeded: 5, Latency: metrics.Histogram{0.01: 5},
		}},
	})
	if err != nil {
		t.Fatalf("marshal batch: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, e.url+"/api/ingest", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build ingest request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("ingest execution %d: %v", executionID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// The full token x routing matrix over real HTTP + MySQL: a cluster token
// carries exactly its own cluster's executions, the global token still carries
// everything, and rotation is a hard cut.
func TestPhase12_ClusterIngestTokenMatrix(t *testing.T) {
	e := setupPhase12(t)

	tokA := e.registerBYOC(t, "byoc-a")
	tokB := e.registerBYOC(t, "byoc-b")
	if tokA == tokB || tokA == "engine-token" || tokB == "engine-token" {
		t.Fatalf("minted tokens not distinct: a=%q b=%q", tokA, tokB)
	}

	// A registered cluster never surfaces its token again, hashed or not
	// (ingest_url is the address, not the credential).
	get := getJSONBody(t, e.client, e.url+"/api/clusters/byoc-a")
	if strings.Contains(get, "ingest_token") || strings.Contains(get, tokA) {
		t.Fatalf("cluster GET leaks ingest material: %s", get)
	}

	idA, scA, runA := e.seedRoutedExecution(t, "on-a", "byoc-a")
	idB, scB, runB := e.seedRoutedExecution(t, "on-b", "byoc-b")
	idD, scD, runD := e.seedRoutedExecution(t, "on-default", "")

	t.Run("cluster token carries only its own executions", func(t *testing.T) {
		if c := e.pushBatch(t, tokA, idA, scA, runA); c != http.StatusAccepted {
			t.Errorf("byoc-a token + on-a = %d, want 202", c)
		}
		if c := e.pushBatch(t, tokA, idD, scD, runD); c != http.StatusForbidden {
			t.Errorf("byoc-a token + on-default = %d, want 403", c)
		}
		if c := e.pushBatch(t, tokA, idB, scB, runB); c != http.StatusForbidden {
			t.Errorf("byoc-a token + on-b = %d, want 403", c)
		}
		if c := e.pushBatch(t, tokB, idB, scB, runB); c != http.StatusAccepted {
			t.Errorf("byoc-b token + on-b = %d, want 202", c)
		}
		if c := e.pushBatch(t, tokB, idA, scA, runA); c != http.StatusForbidden {
			t.Errorf("byoc-b token + on-a = %d, want 403", c)
		}
	})

	t.Run("global token carries everything", func(t *testing.T) {
		// on-b, not on-a: since phase 88 keys shard progress by load
		// origin, an interval push opens a per-origin shard row, and on-a's
		// row must stay single-origin for the absorption proof below (a
		// default-origin phantom row would roll an unknown exit code into
		// its verdict). on-b carries the same authorization case -- a
		// BYOC-routed execution under the deployment-wide token.
		for _, tc := range []struct {
			name                 string
			executionID          int64
			scenarioID, runIDVal int64
		}{
			{"on-b", idB, scB, runB},
			{"on-default", idD, scD, runD},
		} {
			if c := e.pushBatch(t, "engine-token", tc.executionID, tc.scenarioID, tc.runIDVal); c != http.StatusAccepted {
				t.Errorf("global token + %s = %d, want 202", tc.name, c)
			}
		}
	})

	t.Run("garbage is unauthorized", func(t *testing.T) {
		for _, token := range []string{"garbage", tokA + "x", ""} {
			if c := e.pushBatch(t, token, idA, scA, runA); c != http.StatusUnauthorized {
				t.Errorf("token %q + on-a = %d, want 401", token, c)
			}
		}
	})

	t.Run("rotation is a hard cut", func(t *testing.T) {
		tokA2 := e.rotateBYOC(t, "byoc-a")
		if tokA2 == tokA || tokA2 == tokB {
			t.Fatalf("rotation minted a known token: %q", tokA2)
		}
		if c := e.pushBatch(t, tokA, idA, scA, runA); c != http.StatusUnauthorized {
			t.Errorf("old byoc-a token after rotation = %d, want 401", c)
		}
		if c := e.pushBatch(t, tokA2, idA, scA, runA); c != http.StatusAccepted {
			t.Errorf("new byoc-a token after rotation = %d, want 202", c)
		}
		// Rotation is per cluster: byoc-b's token never noticed.
		if c := e.pushBatch(t, tokB, idB, scB, runB); c != http.StatusAccepted {
			t.Errorf("byoc-b token after byoc-a rotation = %d, want 202", c)
		}

		// Absorption proof: the accepted pushes reached a report. A Final
		// under the rotated token closes on-a's run and the report must
		// reflect it -- authenticated, scoped, absorbed, finalized. on-a's
		// shard stayed single-origin (see the global sweep above), so the
		// exit-code roll-up is exactly its one real pod's.
		exit := 0
		body, _ := json.Marshal(metrics.Batch{
			ExecutionID: idA, ScenarioID: scA, RunID: runA,
			ShardIndex: 0, StreamID: "s0", Final: true, ExitCode: &exit,
			Intervals: []metrics.Interval{{
				Seq: 2, Timestamp: 2000, Label: "checkout", Concurrency: 1,
				Samples: 5, Succeeded: 5, Latency: metrics.Histogram{0.01: 5},
			}},
		})
		req, _ := http.NewRequest(http.MethodPost, e.url+"/api/ingest", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokA2)
		req.Header.Set("Content-Type", "application/json")
		resp, err := e.client.Do(req)
		if err != nil {
			t.Fatalf("final ingest: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("final under rotated token = %d, want 202", resp.StatusCode)
		}

		var rep struct {
			RunID   int64  `json:"run_id"`
			Outcome string `json:"outcome"`
		}
		getJSON(t, e.client, e.url+"/api/runs/"+itoa(runA)+"/report", http.StatusOK, &rep)
		if rep.RunID != runA || rep.Outcome != "passed" {
			t.Fatalf("on-a report = run %d %q, want run %d passed", rep.RunID, rep.Outcome, runA)
		}
	})

	// Hygiene: close and clear what the matrix opened.
	for _, id := range []int64{idB, idD} {
		base := e.url + "/api/executions/" + itoa(id)
		postAction(t, e.client, base+"/stop", http.StatusOK)
		postAction(t, e.client, base+"/purge", http.StatusOK)
	}
	postAction(t, e.client, e.url+"/api/executions/"+itoa(idA)+"/purge", http.StatusOK)
}

// getJSONBody fetches url and returns the raw body, for assertions about what
// must not be in it.
func getJSONBody(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return string(body)
}
