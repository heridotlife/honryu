package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/run"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

type lifecycleEnv struct {
	h           http.Handler
	store       *fake.Store
	sched       *fake.Scheduler
	executionID int64
	scenarioID  int64
	owner       string
}

// newLifecycleEnv wires a router with the lifecycle service and seeds an owned
// execution with one scenario (JMX test file) and a stored execution config.
func newLifecycleEnv(t *testing.T, owner string) lifecycleEnv {
	t.Helper()
	return newLifecycleEnvWithTimings(t, owner, time.Millisecond, 10*time.Millisecond)
}

// newLifecycleEnvWithTimings is newLifecycleEnv with the trigger readiness
// poll/timeout wired by the caller, for tests that need the wait to relate to
// an outside clock (e.g. http.Server.WriteTimeout).
func newLifecycleEnvWithTimings(t *testing.T, owner string, poll, timeout time.Duration) lifecycleEnv {
	t.Helper()
	ctx := context.Background()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	sched := fake.NewScheduler()

	h := httpapi.NewRouter(httpapi.Deps{
		Projects:            projectapp.NewService(store),
		Scenarios:           scenarioapp.NewService(store, obj),
		Executions:          executionapp.NewService(store, obj, 100),
		Lifecycle:           lifecycleapp.NewService(store, sched, obj, lifecycleapp.StaticImage("img")),
		Store:               obj,
		DefaultOwners:       []string{"honryu"},
		TriggerReadyPoll:    poll,
		TriggerReadyTimeout: timeout,
	})

	p, _ := project.New("web", owner, "")
	projectID, _ := store.CreateProject(ctx, p)
	coll, _ := execution.New("peak", projectID)
	executionID, _ := store.CreateExecution(ctx, coll)
	pl, _ := scenario.NewNative("smoke", projectID, taurus.ExecutorJMeter)
	scenarioID, _ := store.CreateScenario(ctx, pl)
	if err := store.AddScenarioFile(ctx, scenarioID, "test.jmx", true); err != nil {
		t.Fatalf("add test file: %v", err)
	}
	_ = obj.Upload(ctx, fmt.Sprintf("scenario/%d/test.jmx", scenarioID), strings.NewReader("<jmx/>"))
	if err := store.StoreLoadProfile(ctx, executionID, false, []loadprofile.Entry{
		{Name: "p", ScenarioID: scenarioID, Concurrency: 5, Rampup: 1, Engines: 2, Duration: 10},
	}); err != nil {
		t.Fatalf("store exec: %v", err)
	}
	return lifecycleEnv{h: h, store: store, sched: sched, executionID: executionID, scenarioID: scenarioID, owner: owner}
}

func TestLifecycleHTTP_DeployTriggerStatusStopPurge(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t, "honryu")
	base := "/api/executions/" + itoa(e.executionID)

	if rec := do(t, e.h, http.MethodPost, base+"/deploy"); rec.Code != http.StatusOK {
		t.Fatalf("deploy = %d (%s)", rec.Code, rec.Body.String())
	}

	// Status shows deployed engines.
	rec := do(t, e.h, http.MethodGet, base+"/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var st lifecycleapp.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if st.Phase != run.PhaseDeployed || st.PoolSize != 2 {
		t.Fatalf("status = %+v, want deployed/2", st)
	}

	if rec := do(t, e.h, http.MethodPost, base+"/trigger"); rec.Code != http.StatusOK {
		t.Fatalf("trigger = %d (%s)", rec.Code, rec.Body.String())
	}
	// No engine call on trigger under Taurus: a pod generates load from the
	// moment it starts, so trigger records that the run is under way.

	// Engines detail and pod log.
	if rec := do(t, e.h, http.MethodGet, base+"/engines"); rec.Code != http.StatusOK {
		t.Fatalf("engines = %d", rec.Code)
	}
	if rec := do(t, e.h, http.MethodGet, base+"/scenarios/"+itoa(e.scenarioID)+"/logs"); rec.Code != http.StatusOK {
		t.Fatalf("logs = %d", rec.Code)
	}

	if rec := do(t, e.h, http.MethodPost, base+"/stop"); rec.Code != http.StatusOK {
		t.Fatalf("stop = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := do(t, e.h, http.MethodPost, base+"/purge"); rec.Code != http.StatusOK {
		t.Fatalf("purge = %d", rec.Code)
	}
}

func TestLifecycleHTTP_TriggerBeforeDeployConflicts(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t, "honryu")
	base := "/api/executions/" + itoa(e.executionID)
	start := time.Now()
	if rec := do(t, e.h, http.MethodPost, base+"/trigger"); rec.Code != http.StatusConflict {
		t.Fatalf("trigger before deploy = %d, want 409", rec.Code)
	}
	// The 409 is the readiness wait expiring (10ms env default), not the
	// pre-wait immediate rejection -- and never the unbounded default.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("trigger conflict took %s, the readiness wait is not bounded", elapsed)
	}
}

// The readiness wait at the HTTP boundary: one startup beat is retried over,
// a never-ready pool expires into the same 409 fast, and a client disconnect
// cancels the wait rather than holding it. Every client used to own this
// retry itself (task 121's live finding); now the handler does.
func TestLifecycleHTTP_TriggerWaitsForEngineReadiness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	deployed := func(t *testing.T) (lifecycleEnv, string) {
		t.Helper()
		e := newLifecycleEnv(t, "honryu")
		if err := e.sched.DeployScenario(ctx, ports.DeploySpec{
			ProjectID: 1, ExecutionID: e.executionID, ScenarioID: e.scenarioID,
			// Two shards: the seeded profile asks for Engines: 2, and Trigger
			// counts readiness against that total.
			Shards: []ports.ShardSpec{{Index: 0, Config: []byte("c")}, {Index: 1, Config: []byte("c")}},
		}); err != nil {
			t.Fatalf("DeployScenario: %v", err)
		}
		return e, "/api/executions/" + itoa(e.executionID) + "/trigger"
	}

	t.Run("retries then succeeds", func(t *testing.T) {
		t.Parallel()
		e, path := deployed(t)
		e.sched.NotReadyCalls = 1 // one startup beat, then ready
		if rec := do(t, e.h, http.MethodPost, path); rec.Code != http.StatusOK {
			t.Fatalf("trigger after one not-ready status = %d (%s), want 200", rec.Code, rec.Body.String())
		}
	})

	t.Run("never-ready expires fast", func(t *testing.T) {
		t.Parallel()
		e, path := deployed(t)
		e.sched.NotReadyCalls = 1 << 30
		start := time.Now()
		if rec := do(t, e.h, http.MethodPost, path); rec.Code != http.StatusConflict {
			t.Fatalf("trigger never-ready = %d, want 409", rec.Code)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("trigger wait took %s, far past its 10ms bound", elapsed)
		}
	})

	t.Run("client disconnect cancels the wait", func(t *testing.T) {
		t.Parallel()
		e, path := deployed(t)
		e.sched.NotReadyCalls = 1 << 30

		ctx, cancel := context.WithCancel(ctx)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, nil)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		go func() {
			time.Sleep(5 * time.Millisecond)
			cancel()
		}()
		start := time.Now()
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("trigger cancelled = %d, want 409 (not a hang)", rec.Code)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("cancelled trigger took %s, kept waiting past the disconnect", elapsed)
		}
	})
}

// Regression (phase 24, T7 / phase-23 finding #1): the readiness wait can
// legitimately outlive http.Server.WriteTimeout (defaults: 2m wait vs 15s
// write). A write deadline that expires mid-wait made the final 409
// unwritable -- zero bytes hit the wire, the client saw an empty reply
// (curl exit 52), and the server logged nothing, because net/http swallows
// the failed write. The handler must push the connection's write deadline
// out to cover its own bounded wait so the conflict answer always lands.
// A recorder cannot carry deadlines, so this runs a real server with a
// WriteTimeout shorter than the wait, unlike the recorder-based tests above.
func TestLifecycleHTTP_TriggerWaitOutlivesServerWriteTimeout(t *testing.T) {
	t.Parallel()
	// Timings scaled 10x from the original 5/50/10ms: the property under
	// test (server WriteTimeout < readiness wait) is unchanged, but the
	// SetWriteDeadline override now has 100ms -- not 10ms -- to land before
	// the server's deadline, which survives -race goroutine-scheduling
	// delays on loaded CI runners (the 10ms window flaked once there).
	e := newLifecycleEnvWithTimings(t, "honryu", 50*time.Millisecond, 500*time.Millisecond)
	srv := httptest.NewUnstartedServer(e.h)
	srv.Config.WriteTimeout = 100 * time.Millisecond // < the 500ms wait
	srv.Start()
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/executions/"+itoa(e.executionID)+"/trigger", "", nil)
	if err != nil {
		t.Fatalf("trigger under a server WriteTimeout shorter than the readiness wait: %v -- this is the empty-reply regression", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read trigger body: %v", err)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("trigger = %d (%s), want 409", resp.StatusCode, body)
	}
	if msg := string(body); !strings.Contains(msg, run.ErrNotDeployed.Error()) {
		t.Fatalf("body = %s, want the not-deployed conflict message", msg)
	}
}

func TestLifecycleHTTP_StopWithoutRunConflicts(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t, "honryu")
	base := "/api/executions/" + itoa(e.executionID)
	if rec := do(t, e.h, http.MethodPost, base+"/stop"); rec.Code != http.StatusConflict {
		t.Fatalf("stop without run = %d, want 409", rec.Code)
	}
}

func TestLifecycleHTTP_Forbidden(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t, "other-team")
	base := "/api/executions/" + itoa(e.executionID)
	for _, op := range []string{"/deploy", "/trigger", "/stop", "/purge"} {
		if rec := do(t, e.h, http.MethodPost, base+op); rec.Code != http.StatusForbidden {
			t.Errorf("%s on foreign execution = %d, want 403", op, rec.Code)
		}
	}
}

func TestLifecycleHTTP_InvalidIDs(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t, "honryu")
	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/executions/x/deploy"},
		{http.MethodPost, "/api/executions/x/trigger"},
		{http.MethodPost, "/api/executions/x/stop"},
		{http.MethodPost, "/api/executions/x/purge"},
		{http.MethodGet, "/api/executions/x/status"},
		{http.MethodGet, "/api/executions/x/engines"},
		{http.MethodGet, "/api/executions/x/scenarios/1/logs"},
		{http.MethodGet, "/api/executions/1/scenarios/x/logs"},
	}
	for _, tc := range cases {
		if rec := do(t, e.h, tc.method, tc.path); rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d, want 400", tc.method, tc.path, rec.Code)
		}
	}
}

func TestLifecycleHTTP_DeployMissingExecution(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t, "honryu")
	if rec := do(t, e.h, http.MethodPost, "/api/executions/9999/deploy"); rec.Code != http.StatusNotFound {
		t.Fatalf("deploy missing = %d, want 404", rec.Code)
	}
}

func TestLifecycleHTTP_DeployNoScenariosIsBadRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newLifecycleEnv(t, "honryu")
	// A fresh owned execution with no execution config.
	c, _ := e.store.GetExecution(ctx, e.executionID)
	bare, _ := execution.New("bare", c.ProjectID)
	bareID, _ := e.store.CreateExecution(ctx, bare)

	rec := do(t, e.h, http.MethodPost, "/api/executions/"+itoa(bareID)+"/deploy")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deploy no plans = %d, want 400", rec.Code)
	}
}

// A portable scenario deployed under a script-only engine (k6 rejects the
// declarative form) is the caller's configuration: compile catches it inside
// Deploy and the response must be a 400 naming the engine, not a 500.
func TestLifecycleHTTP_DeployScriptOnlyEngineIsBadRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newLifecycleEnv(t, "honryu")

	c, _ := e.store.GetExecution(ctx, e.executionID)
	portable, err := scenario.New("declarative", c.ProjectID)
	if err != nil {
		t.Fatalf("scenario.New: %v", err)
	}
	portableID, err := e.store.CreateScenario(ctx, portable)
	if err != nil {
		t.Fatalf("CreateScenario: %v", err)
	}
	raw := []byte("default-address: http://example.com\nrequests:\n  - url: /checkout\n")
	if err := e.store.SetScenarioRequests(ctx, portableID, raw); err != nil {
		t.Fatalf("SetScenarioRequests: %v", err)
	}

	onK6, _ := execution.New("on-k6", c.ProjectID)
	onK6.Engine = taurus.ExecutorK6
	k6ID, err := e.store.CreateExecution(ctx, onK6)
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
	if err := e.store.StoreLoadProfile(ctx, k6ID, false, []loadprofile.Entry{
		{Name: "p", ScenarioID: portableID, Concurrency: 5, Rampup: 1, Engines: 1, Duration: 10},
	}); err != nil {
		t.Fatalf("StoreLoadProfile: %v", err)
	}

	rec := do(t, e.h, http.MethodPost, "/api/executions/"+itoa(k6ID)+"/deploy")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deploy portable-on-k6 = %d, want 400", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "k6") {
		t.Fatalf("deploy error does not name the engine: %s", body)
	}
}

func TestLifecycleHTTP_EnginesMissingExecution(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t, "honryu")
	if rec := do(t, e.h, http.MethodGet, "/api/executions/9999/engines"); rec.Code != http.StatusNotFound {
		t.Fatalf("engines missing = %d, want 404", rec.Code)
	}
}

func TestLifecycleHTTP_PodLogNotDeployed(t *testing.T) {
	t.Parallel()
	e := newLifecycleEnv(t, "honryu")
	// No deploy: the scenario's engines are unreachable -> 409 conflict.
	rec := do(t, e.h, http.MethodGet, "/api/executions/"+itoa(e.executionID)+"/scenarios/"+itoa(e.scenarioID)+"/logs")
	if rec.Code != http.StatusConflict {
		t.Fatalf("logs not deployed = %d, want 409", rec.Code)
	}
}
