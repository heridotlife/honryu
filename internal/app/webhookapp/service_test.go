package webhookapp_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/webhookapp"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/domain/webhook"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// captured is one delivery a test server saw: the exact bytes POSTed plus
// the headers that matter to the contract.
type captured struct {
	body   []byte
	sig    string
	uagent string
	ctype  string
}

// receiver is a scripted webhook receiver. Each request is answered with
// statuses[count] (or 200 past the end of the script) and recorded; a nil
// handler means responses follow the script, but tests can also install
// arbitrary behaviour (hangs, slow drains) via onReq.
type receiver struct {
	mu       sync.Mutex
	got      []captured
	statuses []int
	onReq    func(w http.ResponseWriter, r *http.Request, n int)
	srv      *httptest.Server
}

func newReceiver(statuses ...int) *receiver {
	rc := &receiver{statuses: statuses}
	rc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rc.mu.Lock()
		n := len(rc.got)
		rc.got = append(rc.got, captured{
			body:   body,
			sig:    r.Header.Get(webhookapp.SignatureHeader),
			uagent: r.Header.Get("User-Agent"),
			ctype:  r.Header.Get("Content-Type"),
		})
		onReq, statuses := rc.onReq, rc.statuses
		rc.mu.Unlock()
		if onReq != nil {
			onReq(w, r, n)
			return
		}
		status := http.StatusOK
		if n < len(statuses) {
			status = statuses[n]
		}
		w.WriteHeader(status)
	}))
	return rc
}

func (rc *receiver) url() string { return rc.srv.URL + "/hook" }
func (rc *receiver) calls() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.got)
}
func (rc *receiver) last() captured {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.got[len(rc.got)-1]
}
func (rc *receiver) close() { rc.srv.Close() }

// fast builds a service with test-friendly delivery bounds: instant
// backoff and a timeout generous for a local test server.
func fast(t *testing.T, repo *fake.Store) *webhookapp.Service {
	t.Helper()
	return webhookapp.NewService(repo).WithBackoff(time.Millisecond).WithTimeout(2 * time.Second)
}

// finishedRun is a representative stored report: failed outcome, real
// timestamps, achieved load, and an error rate one configured criterion
// ("failures>10%") evaluates as not tripped while another ("p95>500ms")
// trips -- enough surface for the payload's shape assertions to mean
// something.
func finishedRun() report.Report {
	rep := report.Report{
		ExecutionID: 41,
		ScenarioID:  7,
		RunID:       909,
		Engine:      taurus.ExecutorK6,
		StartedAt:   time.Date(2026, 2, 3, 10, 0, 0, 0, time.UTC),
		EndedAt:     time.Date(2026, 2, 3, 10, 5, 0, 0, time.UTC),
		Outcome:     taurus.OutcomeFailed,
		Requested:   report.Load{Concurrency: 10, Throughput: 100, DurationSeconds: 300},
		Achieved:    report.Load{Concurrency: 10, Throughput: 98.5, DurationSeconds: 300, Samples: 17650, Failed: 12},
		ErrorRate:   0.0007,
		// A 95th percentile past half a second: the "p95>500ms" criterion
		// seeded in TestDeliverPayloadShape genuinely trips against this.
		Latency: report.Percentiles{50: 0.05, 95: 0.7},
	}
	return rep
}

// TestDeliverPayloadShape pins the delivered JSON: the event name, the
// run's identity, the verdict inputs, the evaluated thresholds, and the
// relative report path the receiver builds its own absolute link from.
func TestDeliverPayloadShape(t *testing.T) {
	rc := newReceiver(200)
	defer rc.close()
	repo := fake.NewStore()
	ctx := context.Background()
	// The execution's configured criteria, so the payload's thresholds
	// carry the same verdict layer the run report serves.
	if err := repo.SetExecutionCriteria(ctx, 41, []string{"failures>10%", "p95>500ms"}); err != nil {
		t.Fatalf("SetExecutionCriteria: %v", err)
	}
	svc := fast(t, repo)

	// The hook is a struct literal, not a Create: registration's
	// https-only rule is Create's to enforce (TestCreateRejectsPlainHTTP),
	// while delivery just POSTs to whatever the registry holds -- here a
	// local http test server.
	hook := webhook.Webhook{ID: 1, ProjectID: 7, URL: rc.url(), Enabled: true}
	rep := finishedRun()
	if err := svc.Deliver(context.Background(), rep, []webhook.Webhook{hook}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if rc.calls() != 1 {
		t.Fatalf("receiver saw %d requests, want 1", rc.calls())
	}

	var ev map[string]any
	if err := json.Unmarshal(rc.last().body, &ev); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	want := map[string]any{
		"event":        "run.completed",
		"run_id":       float64(909),
		"execution_id": float64(41),
		"project_id":   float64(7),
		"engine":       "k6",
		"outcome":      "failed",
		"error_rate":   0.0007,
		"report_url":   "/reports/909",
	}
	for k, v := range want {
		if ev[k] != v {
			t.Errorf("payload[%q] = %v, want %v", k, ev[k], v)
		}
	}
	ach, ok := ev["achieved"].(map[string]any)
	if !ok || ach["samples"] != float64(17650) || ach["throughput"] != 98.5 {
		t.Errorf("payload[achieved] = %v, want samples 17650 throughput 98.5", ev["achieved"])
	}
	th, ok := ev["thresholds"].(map[string]any)
	if !ok {
		t.Fatalf("payload[thresholds] = %v, want an object", ev["thresholds"])
	}
	// The execution's configured criteria as-is, and the one this run
	// tripped -- the receiver renders its own verdict line from these.
	if crits, ok := th["criteria"].([]any); !ok || len(crits) != 2 {
		t.Errorf("thresholds.criteria = %v, want the 2 configured criteria", th["criteria"])
	}
	if failing, ok := th["failing"].([]any); !ok || len(failing) != 1 || failing[0] != "p95>500ms" {
		t.Errorf("thresholds.failing = %v, want only p95>500ms", th["failing"])
	}
	if ev["started_time"] != "2026-02-03T10:00:00Z" || ev["ended_time"] != "2026-02-03T10:05:00Z" {
		t.Errorf("times = %v..%v, want the run's own", ev["started_time"], ev["ended_time"])
	}
	if rc.last().ctype != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", rc.last().ctype)
	}
}

// TestDeliverSignsHMACWhenSecretSet checks the signature header against an
// independently computed HMAC-SHA256 over the exact bytes POSTed -- not a
// replay of the implementation, the contract a receiver verifies with.
func TestDeliverSignsHMACWhenSecretSet(t *testing.T) {
	rc := newReceiver(200)
	defer rc.close()
	repo := fake.NewStore()
	svc := fast(t, repo)

	hook := webhook.Webhook{ID: 1, ProjectID: 7, URL: rc.url(), Secret: "topsecret", Enabled: true}
	rep := finishedRun()
	if err := svc.Deliver(context.Background(), rep, []webhook.Webhook{hook}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	mac := hmac.New(sha256.New, []byte("topsecret"))
	mac.Write(rc.last().body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got := rc.last().sig; got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
}

// TestDeliverUnsignedWithoutSecret: no secret, no header -- a receiver must
// be able to tell "unsigned" from "signed wrong".
func TestDeliverUnsignedWithoutSecret(t *testing.T) {
	rc := newReceiver(200)
	defer rc.close()
	repo := fake.NewStore()
	svc := fast(t, repo)

	// The hook is a struct literal, not a Create: registration's
	// https-only rule is Create's to enforce (TestCreateRejectsPlainHTTP),
	// while delivery just POSTs to whatever the registry holds -- here a
	// local http test server.
	hook := webhook.Webhook{ID: 1, ProjectID: 7, URL: rc.url(), Enabled: true}
	rep := finishedRun()
	if err := svc.Deliver(context.Background(), rep, []webhook.Webhook{hook}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if rc.last().sig != "" {
		t.Errorf("signature header = %q, want none without a secret", rc.last().sig)
	}
}

// TestDeliverRetriesOnErrorThenSucceeds: two 500s then a 200 is a blip,
// and the third attempt's 2xx is a delivered notification -- bounded
// retries exist exactly for this.
func TestDeliverRetriesOnErrorThenSucceeds(t *testing.T) {
	rc := newReceiver(500, 500, 200)
	defer rc.close()
	repo := fake.NewStore()
	svc := fast(t, repo)

	// The hook is a struct literal, not a Create: registration's
	// https-only rule is Create's to enforce (TestCreateRejectsPlainHTTP),
	// while delivery just POSTs to whatever the registry holds -- here a
	// local http test server.
	hook := webhook.Webhook{ID: 1, ProjectID: 7, URL: rc.url(), Enabled: true}
	rep := finishedRun()
	if err := svc.Deliver(context.Background(), rep, []webhook.Webhook{hook}); err != nil {
		t.Fatalf("Deliver: %v, want success after retries", err)
	}
	if rc.calls() != 3 {
		t.Errorf("receiver saw %d requests, want 3 (two 500s, one 200)", rc.calls())
	}
}

// TestDeliverAbandonedAfterAttemptsExhausted: a receiver that never
// answers 2xx is retried the bounded number of times and then dropped --
// the error is returned (the worker logs it), never retried forever.
func TestDeliverAbandonedAfterAttemptsExhausted(t *testing.T) {
	rc := newReceiver(500, 500, 500, 500, 500)
	defer rc.close()
	repo := fake.NewStore()
	svc := fast(t, repo)

	hook := webhook.Webhook{ID: 1, ProjectID: 7, URL: rc.url(), Enabled: true}
	rep := finishedRun()
	if err := svc.Deliver(context.Background(), rep, []webhook.Webhook{hook}); err == nil {
		t.Fatal("Deliver = nil, want the abandoned-delivery error")
	}
	if rc.calls() != 3 {
		t.Errorf("receiver saw %d requests, want exactly 3 (the bound)", rc.calls())
	}
}

// TestDeliverGivesUpOnTimeout: a receiver that hangs past the per-attempt
// timeout is a dropped delivery, not a stuck goroutine -- each attempt
// gets its own bounded context and the whole delivery still finishes.
func TestDeliverGivesUpOnTimeout(t *testing.T) {
	rc := newReceiver()
	defer rc.close()
	block := make(chan struct{})
	rc.mu.Lock()
	rc.onReq = func(w http.ResponseWriter, _ *http.Request, _ int) {
		<-block
	}
	rc.mu.Unlock()
	repo := fake.NewStore()
	svc := webhookapp.NewService(repo).WithBackoff(time.Millisecond).WithTimeout(25 * time.Millisecond)

	hook := webhook.Webhook{ID: 1, ProjectID: 7, URL: rc.url(), Enabled: true}
	rep := finishedRun()
	done := make(chan error, 1)
	go func() { done <- svc.Deliver(context.Background(), rep, []webhook.Webhook{hook}) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Deliver = nil, want timeout-driven failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Deliver never returned on a hanging receiver")
	}
	close(block)
	if rc.calls() != 3 {
		t.Errorf("receiver saw %d requests, want 3 bounded attempts", rc.calls())
	}
}

// TestCreateRejectsPlainHTTP: a cleartext endpoint would carry the payload
// and the HMAC secret's proof in the clear, so registration refuses it with
// the domain's own error -- and stores nothing.
func TestCreateRejectsPlainHTTP(t *testing.T) {
	repo := fake.NewStore()
	svc := fast(t, repo)

	for _, url := range []string{"http://hooks.example/z", "ftp://x", "not a url"} {
		if _, err := svc.Create(context.Background(), 7, url, "", "dave"); !errors.Is(err, webhook.ErrURLNotHTTPS) {
			t.Errorf("Create(%q) = %v, want ErrURLNotHTTPS", url, err)
		}
	}
	got, err := svc.List(context.Background(), 7)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("rejected registration was stored: %v", got)
	}
}

// TestListScopesByProject: one project's registrations are another's
// silence -- the registry's whole authorization story starts here.
func TestListScopesByProject(t *testing.T) {
	repo := fake.NewStore()
	svc := fast(t, repo)
	ctx := context.Background()

	// Registration URLs are fake https endpoints: this test never
	// delivers, it exercises the registry's scoping.
	if _, err := svc.Create(ctx, 7, "https://hooks.example/a", "", "dave"); err != nil {
		t.Fatalf("Create(7): %v", err)
	}
	if _, err := svc.Create(ctx, 7, "https://hooks.example/b", "", "dave"); err != nil {
		t.Fatalf("Create(7, second): %v", err)
	}
	if _, err := svc.Create(ctx, 8, "https://hooks.example/c", "", "eve"); err != nil {
		t.Fatalf("Create(8): %v", err)
	}

	got, err := svc.List(ctx, 7)
	if err != nil {
		t.Fatalf("List(7): %v", err)
	}
	if len(got) != 2 || got[0].URL != "https://hooks.example/a" || got[1].URL != "https://hooks.example/b" {
		t.Errorf("List(7) = %v, want 2 oldest-first", got)
	}
	other, err := svc.List(ctx, 8)
	if err != nil {
		t.Fatalf("List(8): %v", err)
	}
	if len(other) != 1 || other[0].URL != "https://hooks.example/c" {
		t.Errorf("List(8) = %v, want only its own", other)
	}
}

// TestRunCompletedDeliversToEnabledHooksOnly: the fan-out lists the
// project's registry itself and skips paused rows -- pause is exactly how
// an operator silences a receiver without losing its registration.
func TestRunCompletedDeliversToEnabledHooksOnly(t *testing.T) {
	rc := newReceiver(200)
	defer rc.close()
	repo := fake.NewStore()
	svc := fast(t, repo).WithQueueCapacity(16)
	ctx := context.Background()

	// Seeded straight into the registry (not via Create): the receiver is
	// a local http test server, and registration's https-only rule is a
	// Create-time check, not a delivery-time one.
	seed := func(projectID int64, url string, enabled bool) int64 {
		t.Helper()
		id, err := repo.CreateWebhook(ctx, webhook.Webhook{ProjectID: projectID, URL: url, Enabled: enabled, CreatedBy: "dave"})
		if err != nil {
			t.Fatalf("CreateWebhook(%d, %q): %v", projectID, url, err)
		}
		return id
	}
	on := seed(7, rc.url(), true)
	paused := seed(7, rc.url()+"?paused", false)
	seed(8, rc.url()+"?other", true)

	svc.RunCompleted(ctx, 7, finishedRun())

	deadline := time.Now().Add(5 * time.Second)
	for rc.calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if rc.calls() != 1 {
		t.Fatalf("receiver saw %d requests, want 1 (enabled hook of project 7 only)", rc.calls())
	}
	var ev struct {
		ProjectID int64 `json:"project_id"`
		RunID     int64 `json:"run_id"`
	}
	if err := json.Unmarshal(rc.last().body, &ev); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if ev.ProjectID != 7 || ev.RunID != 909 {
		t.Errorf("payload = project %d run %d, want project 7 run 909", ev.ProjectID, ev.RunID)
	}
	if on == paused {
		t.Fatal("distinct registrations got the same id")
	}
}

// TestRunCompletedNeverBlocks: a receiver that hangs forever cannot delay
// the call that reports the run -- delivery is queued, never awaited. This
// is the invariant the whole use-case exists to preserve.
func TestRunCompletedNeverBlocks(t *testing.T) {
	rc := newReceiver()
	defer rc.close()
	rc.mu.Lock()
	rc.onReq = func(_ http.ResponseWriter, r *http.Request, _ int) { <-r.Context().Done() }
	rc.mu.Unlock()
	repo := fake.NewStore()
	svc := fast(t, repo).WithQueueCapacity(8)
	ctx := context.Background()

	if _, err := repo.CreateWebhook(ctx, webhook.Webhook{ProjectID: 7, URL: rc.url(), Enabled: true}); err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}
	done := make(chan struct{})
	go func() {
		svc.RunCompleted(ctx, 7, finishedRun())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunCompleted blocked on a hanging receiver")
	}
}

// TestRunCompletedDropsWhenQueueFull: a bounded buffer that overflows
// drops with a log line rather than blocking -- with all four workers
// pinned on hanging receivers and a one-slot queue, a third pending
// delivery must be dropped, not buffered, not waited on.
func TestRunCompletedDropsWhenQueueFull(t *testing.T) {
	rc := newReceiver()
	defer rc.close()
	release := make(chan struct{})
	rc.mu.Lock()
	rc.onReq = func(_ http.ResponseWriter, _ *http.Request, _ int) { <-release }
	rc.mu.Unlock()
	repo := fake.NewStore()
	// One-slot queue, default four workers: the first deliveries pin the
	// workers, the queue holds one more, and anything past that drops.
	svc := fast(t, repo).WithQueueCapacity(1)
	ctx := context.Background()

	for i := 0; i < 6; i++ {
		if _, err := repo.CreateWebhook(ctx, webhook.Webhook{
			ProjectID: 7, URL: fmt.Sprintf("%s/%d", rc.url(), i), Enabled: true,
		}); err != nil {
			t.Fatalf("CreateWebhook(%d): %v", i, err)
		}
	}

	svc.RunCompleted(ctx, 7, finishedRun())

	// Whatever the scheduling, at most 4 in-flight + 1 queued = 5 of the 6
	// can ever arrive; the bound is what matters, not which one dropped.
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for rc.calls() < 5 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if n := rc.calls(); n > 5 {
		t.Errorf("receiver saw %d requests, want at most 5 (4 workers + 1 queued)", n)
	}
}

// TestRunCompletedSurvivesRegistryFailure: a broken registry read is
// logged and swallowed -- the run whose completion triggered it is already
// over, and a notification outage must not be able to look like anything
// else failed.
func TestRunCompletedSurvivesRegistryFailure(t *testing.T) {
	repo := fake.NewStore()
	// The embedded-store selector is spelled out: ShareStore has its own
	// ListErr, so the bare field name would be ambiguous on the composite.
	repo.WebhookStore.ListErr = errors.New("registry down")
	svc := fast(t, repo)

	done := make(chan struct{})
	go func() {
		svc.RunCompleted(context.Background(), 7, finishedRun())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunCompleted blocked on a failing registry read")
	}
}
