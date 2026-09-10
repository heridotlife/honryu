//go:build e2e

package e2e_test

import (
	"bytes"
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
	"github.com/heridotlife/honryu/internal/adapters/storage/local"
	"github.com/heridotlife/honryu/internal/app/calibrationapp"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/app/metricsapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/capacityprofile"
	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports/fake"
	"github.com/heridotlife/honryu/test/dbtest"
)

// scriptedStep is one step's scripted settled outcome, fed to the engine via
// a Final ingest batch the same shape phase6_e2e's ingestFinal uses.
// exitCode 0 passes, 3 is a criteria failure (bzt's own convention, already
// established by phase4/phase6's e2e tests).
type scriptedStep struct {
	exitCode          int
	succeeded, failed int64
	errors            []metrics.ErrorGroup
}

// scriptedIngest is the StepRunner's injected "hold" -- instead of actually
// sleeping, it ingests the next queued scripted outcome for whichever
// execution/scenario it is currently pointed at, so that by the time
// RunStep calls Stop+GetReport a real, settled report already exists,
// produced by the genuine metricsapp/report pipeline (not a stand-in).
//
// Errors are recorded, never asserted with t.Fatal from within: sleep runs
// on whatever goroutine called AdvanceOne, and only the goroutine running
// the test itself may fail it (a hard rule for concurrent subtests below).
type scriptedIngest struct {
	client *http.Client
	srvURL string
	repo   *mysqladapter.Repository

	mu                      sync.Mutex
	executionID, scenarioID int64
	steps                   []scriptedStep
	err                     error
}

func (s *scriptedIngest) sleep(time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	if len(s.steps) == 0 {
		s.err = fmt.Errorf("scriptedIngest: no more scripted steps for execution %d", s.executionID)
		return
	}
	step := s.steps[0]
	s.steps = s.steps[1:]
	executionID, scenarioID := s.executionID, s.scenarioID

	runID, running, err := s.repo.CurrentRun(context.Background(), executionID)
	if err != nil {
		s.err = fmt.Errorf("CurrentRun(%d): %w", executionID, err)
		return
	}
	if !running {
		s.err = fmt.Errorf("CurrentRun(%d): not running", executionID)
		return
	}
	if err := ingestFinalNoFatal(s.client, s.srvURL, executionID, scenarioID, runID, step); err != nil {
		s.err = err
	}
}

// checkErr fails the test (from the test's own goroutine) if the last
// sleep() call recorded a failure, and clears it.
func (s *scriptedIngest) checkErr(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	err := s.err
	s.err = nil
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("scriptedIngest: %v", err)
	}
}

// ingestFinalNoFatal is phase6_e2e's ingestFinal, returning an error instead
// of calling t.Fatalf -- needed because sleep() may run on a non-test
// goroutine (the concurrent double-drive proof below).
func ingestFinalNoFatal(client *http.Client, baseURL string, executionID, scenarioID, runID int64, step scriptedStep) error {
	exitCode := step.exitCode
	body, err := json.Marshal(metrics.Batch{
		ExecutionID: executionID, ScenarioID: scenarioID, RunID: runID,
		ShardIndex: 0, StreamID: "s0", Final: true, ExitCode: &exitCode,
		Intervals: []metrics.Interval{{
			Seq: 1, Timestamp: 1000, Label: "target-request",
			Concurrency: 1, Samples: step.succeeded + step.failed, Succeeded: step.succeeded, Failed: step.failed,
			Latency: metrics.Histogram{0.01: step.succeeded},
			Errors:  step.errors,
		}},
	})
	if err != nil {
		return fmt.Errorf("marshal batch: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/ingest", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build ingest request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer engine-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ingest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("ingest status = %d", resp.StatusCode)
	}
	return nil
}

// phase7Env wires the full real stack (MySQL, real lifecycleapp/metricsapp,
// calibrationapp with a real StepRunner over the fake scheduler) exactly
// the way cmd/api + cmd/calibrator wire it in production, except AdvanceOne
// is called directly (it is cmd/calibrator's own loop, not itself
// HTTP-exposed).
type phase7Env struct {
	client       *http.Client
	srvURL       string
	repo         *mysqladapter.Repository
	sched        *fake.Scheduler
	calibrations *calibrationapp.Service
	scenarios    *scenarioapp.Service
	scripted     *scriptedIngest
}

func setupPhase7(t *testing.T) *phase7Env {
	t.Helper()
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	store := local.New(t.TempDir(), "")
	sched := fake.NewScheduler()
	sink := fake.NewMetricsSink()
	bus := membus.New()

	collector := metricsapp.NewService(repo, sink, bus, repo, repo)
	lifecycle := lifecycleapp.NewService(repo, sched, store, lifecycleapp.StaticImage("jmeter")).WithMetrics(collector)
	scenarios := scenarioapp.NewService(repo, store)

	scripted := &scriptedIngest{repo: repo}
	runner := calibrationapp.NewStepRunner(repo, lifecycle, repo).WithSleep(scripted.sleep)
	calibrations := calibrationapp.NewService(repo).WithRunner(runner).WithFingerprint(scenarios)

	router := httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(repo),
		Scenarios:     scenarios,
		Executions:    executionapp.NewService(repo, store, 500),
		Lifecycle:     lifecycle,
		Calibrations:  calibrations,
		Store:         store,
		Metrics:       collector,
		Reports:       repo,
		IngestToken:   "engine-token",
		DefaultOwners: []string{"honryu"},
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	scripted.client = srv.Client()
	scripted.srvURL = srv.URL
	return &phase7Env{
		client: srv.Client(), srvURL: srv.URL, repo: repo, sched: sched,
		calibrations: calibrations, scenarios: scenarios, scripted: scripted,
	}
}

// scenarioFingerprint reads scenarioID's current content fingerprint
// directly -- the same computation calibrationapp.Service.FanOut performs
// internally, exposed here so the "stale" section can seed a profile keyed
// to a real fingerprint without having to drive a full calibration through
// AdvanceOne first.
func scenarioFingerprint(t *testing.T, e *phase7Env, scenarioID int64) string {
	t.Helper()
	fp, err := e.scenarios.ScenarioFingerprint(context.Background(), scenarioID)
	if err != nil {
		t.Fatalf("ScenarioFingerprint(%d): %v", scenarioID, err)
	}
	return fp
}

// createCalibration configures a CalibrateEngine execution via the HTTP
// API, bound at creation to scenarioID: it first creates an ordinary
// source execution running the scenario (PUT .../config), then names both
// ids on the create call, whose own binding copies that entry (phase 41).
// Returns the execution id. extra supplies any of the optional bound
// overrides (seed_qps, max_qps, max_steps, hold_seconds). The copied
// entry's own concurrency/engines/duration/throughput are irrelevant
// beyond passing validation: RunStep rewrites them every step, preserving
// only the scenario id this binds.
func createCalibration(t *testing.T, e *phase7Env, projectID, scenarioID int64, criterion, cpu, memory string, extra url.Values) int64 {
	t.Helper()
	sourceID := postForm(t, e.client, e.srvURL+"/api/executions", url.Values{"name": {"peak"}, "project_id": {itoa(projectID)}})
	sourceConfig := fmt.Sprintf("multi-test:\n  collectionid: %d\n  tests:\n    - testid: %d\n      concurrency: 1\n      rampup: 1\n      engines: 1\n      duration: 5\n",
		sourceID, scenarioID)
	putMultipart(t, e.client, e.srvURL+"/api/executions/"+itoa(sourceID)+"/config", "config.yaml", sourceConfig)

	form := url.Values{
		"project_id": {itoa(projectID)}, "name": {"calib"}, "engine": {"jmeter"},
		"criterion": {criterion}, "cpu": {cpu}, "memory": {memory},
		"scenario_id": {itoa(scenarioID)}, "source_execution_id": {itoa(sourceID)},
	}
	for k, v := range extra {
		form[k] = v
	}
	resp, err := e.client.PostForm(e.srvURL+"/api/calibrations", form)
	if err != nil {
		t.Fatalf("POST /api/calibrations: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create calibration status = %d", resp.StatusCode)
	}
	var out struct {
		ExecutionID int64 `json:"execution_id"`
	}
	decode(t, resp, &out)
	return out.ExecutionID
}

// triggerCalibration starts a fresh job over executionID and returns its id.
func triggerCalibration(t *testing.T, e *phase7Env, executionID int64) int64 {
	t.Helper()
	resp, err := e.client.Post(e.srvURL+"/api/executions/"+itoa(executionID)+"/calibration/trigger", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("trigger calibration: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("trigger calibration status = %d", resp.StatusCode)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	decode(t, resp, &out)
	return out.ID
}

type calibrationJobView struct {
	ID     int64  `json:"id"`
	Phase  string `json:"phase"`
	Result *struct {
		SaturatedBy string  `json:"saturated_by"`
		PerPodQPS   float64 `json:"per_pod_qps"`
	} `json:"result"`
}

func getCalibrationJob(t *testing.T, e *phase7Env, jobID int64) calibrationJobView {
	t.Helper()
	var got calibrationJobView
	getJSON(t, e.client, e.srvURL+"/api/calibrations/"+itoa(jobID), http.StatusOK, &got)
	return got
}

type fanOutView struct {
	Status  string `json:"status"`
	Engines int    `json:"engines"`
}

func fanOut(t *testing.T, e *phase7Env, scenarioID int64, engine, cpu, memory string, targetQPS string) fanOutView {
	t.Helper()
	u := fmt.Sprintf("%s/api/scenarios/%s/capacity-profile/fanout?engine=%s&cpu=%s&memory=%s&target_qps=%s",
		e.srvURL, itoa(scenarioID), engine, cpu, memory, targetQPS)
	var got fanOutView
	getJSON(t, e.client, u, http.StatusOK, &got)
	return got
}

// advance calls AdvanceOne once, failing the test if it errors, finds
// nothing due, or the scripted ingest it drove through sleep() failed.
func advance(t *testing.T, e *phase7Env) {
	t.Helper()
	found, err := e.calibrations.AdvanceOne(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("AdvanceOne: %v", err)
	}
	if !found {
		t.Fatal("AdvanceOne: found = false, want a due job")
	}
	e.scripted.checkErr(t)
}

// TestPhase7_CalibrationEndToEnd drives one full calibration search per
// saturated_by outcome (engine, target, neither) against the fake scheduler
// with scripted per-step reports, proves the resulting CapacityProfile is
// written and the fan-out calculator reads it back correctly, then proves
// fan-out's other two answers: a scenario-content change stales an
// otherwise-fresh profile, and an uncalibrated key reports no_profile.
func TestPhase7_CalibrationEndToEnd(t *testing.T) {
	e := setupPhase7(t)
	ctx := context.Background()

	projectID := postForm(t, e.client, e.srvURL+"/api/projects", url.Values{"name": {"svc"}, "owner": {"honryu"}})
	scenarioID := postForm(t, e.client, e.srvURL+"/api/scenarios", url.Values{"name": {"target"}, "project_id": {itoa(projectID)}})
	putMultipart(t, e.client, e.srvURL+"/api/scenarios/"+itoa(scenarioID)+"/files", "scenario.jmx", "<jmx/>")

	// --- engine-limited: two ticks -- a clean seed step, then an
	// engine-short step confirmed by its mandatory retry (both scripted
	// engine-short, so the retry cannot flip the outcome). MaxSteps=2 forces
	// termination the moment bisecting's own StepCount check fires, without
	// needing to script a full bisection walk.
	engineExec := createCalibration(t, e, projectID, scenarioID, "failures>90%", "1", "512Mi",
		url.Values{"seed_qps": {"10"}, "max_qps": {"1000"}, "max_steps": {"2"}, "hold_seconds": {"1"}})
	engineJob := triggerCalibration(t, e, engineExec)
	e.scripted.executionID, e.scripted.scenarioID = engineExec, scenarioID
	e.scripted.steps = []scriptedStep{
		{exitCode: 0, succeeded: 10, failed: 0}, // tick 1 @10: clean (achieved 10 >= 9.5)
		{exitCode: 0, succeeded: 8, failed: 0},  // tick 2 attempt @20: engine-short (8 < 19)
		{exitCode: 0, succeeded: 9, failed: 0},  // tick 2 retry @20: engine-short, confirmed
	}
	advance(t, e)
	advance(t, e)

	engineJobView := getCalibrationJob(t, e, engineJob)
	if engineJobView.Phase != "done" || engineJobView.Result == nil {
		t.Fatalf("engine-limited job = %+v, want a terminal result", engineJobView)
	}
	if engineJobView.Result.SaturatedBy != "engine" || engineJobView.Result.PerPodQPS != 10 {
		t.Fatalf("engine-limited result = %+v, want engine-saturated at 10", engineJobView.Result)
	}
	if got := fanOut(t, e, scenarioID, "jmeter", "1", "512Mi", "25"); got.Status != "ok" || got.Engines != 3 {
		t.Fatalf("fan out (engine-limited, target 25) = %+v, want {ok, 3} (ceil(25/10))", got)
	}

	// --- target-limited: the engine keeps up (achieved samples cover the
	// requested rate) but enough target-attributed failures (500s) trip
	// "failures>5%" -- terminal on the very first step, no retry (retries
	// only ever apply to an engine-short classification).
	targetExec := createCalibration(t, e, projectID, scenarioID, "failures>5%", "2", "1Gi",
		url.Values{"seed_qps": {"10"}, "hold_seconds": {"1"}})
	targetJob := triggerCalibration(t, e, targetExec)
	e.scripted.executionID, e.scripted.scenarioID = targetExec, scenarioID
	e.scripted.steps = []scriptedStep{
		{exitCode: 3, succeeded: 8, failed: 2, errors: []metrics.ErrorGroup{
			{Message: "Request to http://target/checkout didn't succeed (500)", ResponseCode: "500", Count: 2},
		}},
	}
	advance(t, e)

	targetJobView := getCalibrationJob(t, e, targetJob)
	if targetJobView.Phase != "done" || targetJobView.Result == nil || targetJobView.Result.SaturatedBy != "target" {
		t.Fatalf("target-limited job = %+v, want a terminal target-saturated result", targetJobView)
	}
	if got := fanOut(t, e, scenarioID, "jmeter", "2", "1Gi", "100"); got.Status != "target_limited" || got.Engines != 0 {
		t.Fatalf("fan out (target-limited) = %+v, want target_limited with no engine count", got)
	}

	// --- neither: a clean seed step whose double would breach the search's
	// own MaxQPS ceiling -- terminal, honestly unresolved.
	neitherExec := createCalibration(t, e, projectID, scenarioID, "failures>90%", "1", "1Gi",
		url.Values{"seed_qps": {"10"}, "max_qps": {"15"}, "hold_seconds": {"1"}})
	neitherJob := triggerCalibration(t, e, neitherExec)
	e.scripted.executionID, e.scripted.scenarioID = neitherExec, scenarioID
	e.scripted.steps = []scriptedStep{{exitCode: 0, succeeded: 10, failed: 0}}
	advance(t, e)

	neitherJobView := getCalibrationJob(t, e, neitherJob)
	if neitherJobView.Phase != "done" || neitherJobView.Result == nil || neitherJobView.Result.SaturatedBy != "neither" {
		t.Fatalf("neither job = %+v, want a terminal neither result", neitherJobView)
	}
	if got := fanOut(t, e, scenarioID, "jmeter", "1", "1Gi", "100"); got.Status != "inconclusive" {
		t.Fatalf("fan out (neither) = %+v, want inconclusive", got)
	}

	// --- stale: a fresh scenario, deliberately never bound to any
	// execution's config -- binding a scenario (as every calibration above
	// does) makes it permanently immutable to file changes
	// (scenarioapp.ErrScenarioInUse), so proving a real content change
	// needs one no calibration has touched. Seed a profile keyed to its
	// real fingerprint before the change (as if a calibration had just
	// finished), confirm it reads fresh, then change the content for real
	// and confirm the same key now reads stale.
	staleScenarioID := postForm(t, e.client, e.srvURL+"/api/scenarios", url.Values{"name": {"stale-check"}, "project_id": {itoa(projectID)}})
	putMultipart(t, e.client, e.srvURL+"/api/scenarios/"+itoa(staleScenarioID)+"/files", "scenario.jmx", "<jmx/>")
	fpBeforeChange := scenarioFingerprint(t, e, staleScenarioID)
	staleKey := capacityprofile.Key{ScenarioID: staleScenarioID, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi"}
	if err := e.repo.UpsertCapacityProfile(ctx, capacityprofile.CapacityProfile{
		Key: staleKey, PerPodQPS: 10, SaturatedBy: calibration.SaturatedByEngine,
		ScenarioFingerprint: fpBeforeChange, CalibratedAt: time.Now(),
	}); err != nil {
		t.Fatalf("UpsertCapacityProfile: %v", err)
	}
	if got := fanOut(t, e, staleScenarioID, "jmeter", "1", "512Mi", "25"); got.Status != "ok" {
		t.Fatalf("fan out (before content change) = %+v, want ok", got)
	}
	// A scenario carries at most one test script (scenario.jmx already
	// claims that slot); a data file is the real content change that
	// ScenarioFingerprint also hashes (task 76's own TestFile + Data), so
	// this is the actual change, not a workaround.
	putMultipart(t, e.client, e.srvURL+"/api/scenarios/"+itoa(staleScenarioID)+"/files", "extra.csv", "id,value\n1,2\n")
	if got := fanOut(t, e, staleScenarioID, "jmeter", "1", "512Mi", "25"); got.Status != "stale" {
		t.Fatalf("fan out (after real content change) = %+v, want stale", got)
	}

	// --- no_profile: a key that was never calibrated at all.
	if got := fanOut(t, e, staleScenarioID, "k6", "1", "512Mi", "25"); got.Status != "no_profile" {
		t.Fatalf("fan out (never calibrated) = %+v, want no_profile", got)
	}
}

// TestPhase7_ConcurrentControllersNeverDoubleDriveAJob races several
// simulated controller replicas (cmd/calibrator alongside cmd/scheduler
// optionally hosting the same loop, or several cmd/calibrator replicas) all
// calling AdvanceOne against one due job at nearly the same instant.
// AdvanceOne's own row-locked claim (ClaimNextStep) must let exactly one of
// them actually drive the step -- proven here at the calibrationapp.Service
// level, one layer above the raw-repository proof in
// calibration_integration_test.go.
func TestPhase7_ConcurrentControllersNeverDoubleDriveAJob(t *testing.T) {
	e := setupPhase7(t)
	ctx := context.Background()

	projectID := postForm(t, e.client, e.srvURL+"/api/projects", url.Values{"name": {"svc"}, "owner": {"honryu"}})
	scenarioID := postForm(t, e.client, e.srvURL+"/api/scenarios", url.Values{"name": {"target"}, "project_id": {itoa(projectID)}})
	putMultipart(t, e.client, e.srvURL+"/api/scenarios/"+itoa(scenarioID)+"/files", "scenario.jmx", "<jmx/>")

	execID := createCalibration(t, e, projectID, scenarioID, "failures>5%", "1", "512Mi",
		url.Values{"seed_qps": {"10"}, "hold_seconds": {"1"}})
	jobID := triggerCalibration(t, e, execID)
	e.scripted.executionID, e.scripted.scenarioID = execID, scenarioID
	// Exactly one scripted step: whichever goroutine wins the claim
	// consumes it; every other goroutine must return found=false without
	// ever reaching sleep() at all.
	e.scripted.steps = []scriptedStep{
		{exitCode: 3, succeeded: 8, failed: 2, errors: []metrics.ErrorGroup{
			{Message: "Request to http://target/checkout didn't succeed (500)", ResponseCode: "500", Count: 2},
		}},
	}

	const racers = 4
	now := time.Now()
	var wg sync.WaitGroup
	var start sync.WaitGroup
	start.Add(1)
	found := make([]bool, racers)
	errs := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait()
			f, err := e.calibrations.AdvanceOne(ctx, now)
			found[i] = f
			errs[i] = err
		}(i)
	}
	start.Done()
	wg.Wait()

	wins := 0
	for i := range racers {
		if errs[i] != nil {
			t.Fatalf("racer %d: AdvanceOne: %v", i, errs[i])
		}
		if found[i] {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("winners = %d, want exactly 1 -- the row lock must let only one racer claim and drive the job", wins)
	}
	e.scripted.checkErr(t)

	job := getCalibrationJob(t, e, jobID)
	if job.Phase != "done" || job.Result == nil || job.Result.SaturatedBy != "target" {
		t.Fatalf("job after the race = %+v, want a terminal target-saturated result driven exactly once", job)
	}
}
