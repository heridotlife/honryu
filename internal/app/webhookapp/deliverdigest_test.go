package webhookapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/heridotlife/honryu/internal/domain/webhook"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// register puts a hook straight into the registry: the fake does not
// validate (Create's https-only rule is the service's to enforce), so a
// local http test server can stand in for the receiver.
func register(t *testing.T, repo *fake.Store, projectID int64, url, secret string, enabled bool) {
	t.Helper()
	if _, err := repo.CreateWebhook(context.Background(), webhook.Webhook{
		ProjectID: projectID, URL: url, Secret: secret, Enabled: enabled,
	}); err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}
}

// TestDeliverDigestPostsStoredBytesToEnabledHooks pins the digest lane's
// core contract (phase 42's report.digest, phase 60's DeliverDigest): the
// caller's exact bytes -- the stored digest row -- are POSTed verbatim to
// each enabled webhook of the project, signed when a secret is set, and
// paused hooks or other projects get nothing. One confirmed delivery means
// delivered, even if a sibling failed.
func TestDeliverDigestPostsStoredBytesToEnabledHooks(t *testing.T) {
	on := newReceiver(200)
	defer on.close()
	paused := newReceiver(200)
	defer paused.close()
	repo := fake.NewStore()
	register(t, repo, 7, on.url(), "s3cret", true)
	register(t, repo, 7, paused.url(), "", false)
	register(t, repo, 8, on.url(), "", true) // another project: not this call's to notify

	svc := fast(t, repo)
	body := []byte(`{"event":"report.digest","runs_total":3}`)
	delivered, err := svc.DeliverDigest(context.Background(), 7, body)
	if err != nil {
		t.Fatalf("DeliverDigest: %v", err)
	}
	if !delivered {
		t.Fatal("DeliverDigest = delivered=false, want true (the enabled receiver confirmed)")
	}

	if on.calls() != 1 {
		t.Fatalf("enabled receiver saw %d calls, want 1", on.calls())
	}
	if got := string(on.last().body); got != string(body) {
		t.Errorf("body = %q, want the caller's exact bytes %q", got, body)
	}
	if on.last().sig == "" {
		t.Error("signature header missing; a secret was set")
	}
	if paused.calls() != 0 {
		t.Errorf("paused receiver saw %d calls, want 0", paused.calls())
	}
}

// TestDeliverDigestAttemptsEveryHook: one dead receiver must not stop the
// project's other receiver from being notified, and one healthy receiver is
// enough for the digest to count as delivered -- the per-target failure is
// logged, the record's verdict is "it got out".
func TestDeliverDigestAttemptsEveryHook(t *testing.T) {
	dead := newReceiver(500, 500, 500)
	defer dead.close()
	alive := newReceiver(200)
	defer alive.close()
	repo := fake.NewStore()
	register(t, repo, 7, dead.url(), "", true)
	register(t, repo, 7, alive.url(), "", true)

	svc := fast(t, repo)
	delivered, err := svc.DeliverDigest(context.Background(), 7, []byte(`{}`))
	if err != nil {
		t.Fatalf("DeliverDigest: %v", err)
	}
	if !delivered {
		t.Error("DeliverDigest = delivered=false, want true (one receiver confirmed)")
	}
	if alive.calls() != 1 {
		t.Errorf("healthy receiver saw %d calls after a sibling failed, want 1", alive.calls())
	}
	if dead.calls() != 3 { // deliverAttempts: the shared bounds, not a new policy
		t.Errorf("dead receiver saw %d calls, want the bounded 3", dead.calls())
	}
}

// TestDeliverDigestAllFailed: when no target confirms, the caller learns
// the delivery failed -- that is the (false, err) third of the tri-state
// the digest record's failed status is built on.
func TestDeliverDigestAllFailed(t *testing.T) {
	dead := newReceiver(500, 500, 500)
	defer dead.close()
	repo := fake.NewStore()
	register(t, repo, 7, dead.url(), "", true)

	svc := fast(t, repo)
	delivered, err := svc.DeliverDigest(context.Background(), 7, []byte(`{}`))
	if err == nil {
		t.Fatal("DeliverDigest = nil error, want the dead receiver's failure")
	}
	if delivered {
		t.Error("DeliverDigest = delivered=true, want false (nothing confirmed)")
	}
	if dead.calls() != 3 { // deliverAttempts: retries, then gives up
		t.Errorf("receiver saw %d calls, want the bounded 3", dead.calls())
	}
}

// TestDeliverDigestRetriesSinkAndRecovers: a sink that 500s once is retried
// and the retry lands -- a blip must not cost the delivery.
func TestDeliverDigestRetriesSinkAndRecovers(t *testing.T) {
	flaky := tlsReceiver(500, 200)
	defer flaky.close()
	repo := fake.NewStore()

	svc := fastTLS(t, repo, flaky.srv).WithDigestSink(flaky.url(), "")
	delivered, err := svc.DeliverDigest(context.Background(), 7, []byte(`{}`))
	if err != nil {
		t.Fatalf("DeliverDigest: %v", err)
	}
	if !delivered {
		t.Error("DeliverDigest = delivered=false, want true (the retry confirmed)")
	}
	if flaky.calls() != 2 {
		t.Errorf("flaky sink saw %d calls, want 2 (one 500, one 200)", flaky.calls())
	}
}

// TestDeliverDigestSinkReceivesSignedPayload: the configured sink is
// notified alongside the project's own hooks and its delivery carries the
// same signature scheme -- the sink is a webhook, not a new protocol.
func TestDeliverDigestSinkReceivesSignedPayload(t *testing.T) {
	hook := newReceiver(200)
	defer hook.close()
	sink := tlsReceiver(200)
	defer sink.close()
	repo := fake.NewStore()
	register(t, repo, 7, hook.url(), "", true)

	svc := fastTLS(t, repo, sink.srv).WithDigestSink(sink.url(), "sink-secret")
	body := []byte(`{"event":"report.digest","runs_total":1}`)
	delivered, err := svc.DeliverDigest(context.Background(), 7, body)
	if err != nil || !delivered {
		t.Fatalf("DeliverDigest = (%v, %v), want (true, nil)", delivered, err)
	}
	if sink.calls() != 1 || hook.calls() != 1 {
		t.Fatalf("sink/hook calls = %d/%d, want 1/1", sink.calls(), hook.calls())
	}
	if got := string(sink.last().body); got != string(body) {
		t.Errorf("sink body = %q, want the caller's exact bytes %q", got, body)
	}
	if sink.last().sig == "" {
		t.Error("sink signature header missing; a sink secret was set")
	}
}

// TestDeliverDigestUnconfiguredDeliversNothing: a project with no enabled
// hooks and a deployment with no sink have nothing to notify -- (false,
// nil) is "nothing to do", not a failure, so the digest record stays
// pending rather than flapping to failed.
func TestDeliverDigestUnconfiguredDeliversNothing(t *testing.T) {
	paused := newReceiver(200)
	defer paused.close()
	repo := fake.NewStore()
	register(t, repo, 7, paused.url(), "", false) // paused: not a target

	svc := fast(t, repo)
	delivered, err := svc.DeliverDigest(context.Background(), 7, []byte(`{}`))
	if err != nil {
		t.Fatalf("DeliverDigest: %v", err)
	}
	if delivered {
		t.Error("DeliverDigest = delivered=true, want false (nothing was attempted)")
	}
	if paused.calls() != 0 {
		t.Errorf("paused receiver saw %d calls, want 0", paused.calls())
	}
}

// TestDeliverDigestRegistryFailure: a failing registry read surfaces as the
// call's error rather than masquerading as success or delivery.
func TestDeliverDigestRegistryFailure(t *testing.T) {
	repo := fake.NewStore()
	repo.WebhookStore.ListErr = errors.New("registry down")
	svc := fast(t, repo)
	delivered, err := svc.DeliverDigest(context.Background(), 7, []byte(`{}`))
	if err == nil {
		t.Fatal("DeliverDigest = nil error, want the registry read's failure")
	}
	if delivered {
		t.Error("DeliverDigest = delivered=true, want false (nothing was attempted)")
	}
}

// slackDigestPayload is a realistic stored digest body -- the same wire
// shape digestapp.Payload marshals -- for the Slack transport tests.
func slackDigestPayload() []byte {
	return []byte(`{` +
		`"event":"report.digest","project_id":7,"period":"daily",` +
		`"window_start":"2026-09-16T00:00:00Z","window_end":"2026-09-17T00:00:00Z",` +
		`"runs_total":4,"by_outcome":{"passed":3,"failed":1,"aborted":0},` +
		`"threshold_failures":1,"executions":[]}`)
}

// TestDeliverDigestSlackSinkReceivesSummary: the Slack transport's contract
// -- the configured slack:// target gets Slack's message shape, not the raw
// payload: {"text": ...} summarising the window (period, dates, run counts,
// threshold failures, where to read the full digest). Unsigned -- Slack's
// credential is the URL path, there is no shared secret to sign with.
func TestDeliverDigestSlackSinkReceivesSummary(t *testing.T) {
	slack := tlsReceiver(200)
	defer slack.close()
	repo := fake.NewStore()

	svc := fastTLS(t, repo, slack.srv).WithSlackSink(slack.url())
	delivered, err := svc.DeliverDigest(context.Background(), 7, slackDigestPayload())
	if err != nil {
		t.Fatalf("DeliverDigest: %v", err)
	}
	if !delivered {
		t.Fatal("DeliverDigest = delivered=false, want true (slack confirmed)")
	}
	if slack.calls() != 1 {
		t.Fatalf("slack receiver saw %d calls, want 1", slack.calls())
	}
	var msg struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(slack.last().body, &msg); err != nil {
		t.Fatalf("slack body did not parse as {\"text\"...}: %v", err)
	}
	for _, want := range []string{
		"daily window 2026-09-16", // which window
		"4 runs",                  // runs count
		"3 passed", "1 failed", "0 aborted",
		"1 threshold failure",     // the regression count
		"/executions (project 7)", // where the full digest lives
	} {
		if !strings.Contains(msg.Text, want) {
			t.Errorf("slack text %q missing %q", msg.Text, want)
		}
	}
	if slack.last().sig != "" {
		t.Errorf("slack delivery carried signature %q; slack targets are secret-less and unsigned", slack.last().sig)
	}
}

// TestDeliverDigestSlackKeepsRawTargetsIntact: slack is an ADDITIONAL
// transport -- project webhooks still get the stored bytes verbatim, and
// both confirming means delivered with a nil error (the happy tri-state).
func TestDeliverDigestSlackKeepsRawTargetsIntact(t *testing.T) {
	hook := newReceiver(200)
	defer hook.close()
	slack := tlsReceiver(200)
	defer slack.close()
	repo := fake.NewStore()
	register(t, repo, 7, hook.url(), "", true)

	svc := fastTLS(t, repo, slack.srv).WithSlackSink(slack.url())
	body := slackDigestPayload()
	delivered, err := svc.DeliverDigest(context.Background(), 7, body)
	if err != nil || !delivered {
		t.Fatalf("DeliverDigest = (%v, %v), want (true, nil)", delivered, err)
	}
	if got := string(hook.last().body); got != string(body) {
		t.Errorf("webhook body = %q, want the stored bytes %q (slack must not re-render other targets)", got, body)
	}
}

// TestDeliverDigestSlackMixedSuccess: a dead project webhook must not fail
// the digest when slack confirms -- a single healthy receiver is enough
// for delivered, the same rule the raw targets follow.
func TestDeliverDigestSlackMixedSuccess(t *testing.T) {
	dead := newReceiver(500, 500, 500)
	defer dead.close()
	slack := tlsReceiver(200)
	defer slack.close()
	repo := fake.NewStore()
	register(t, repo, 7, dead.url(), "", true)

	svc := fastTLS(t, repo, slack.srv).WithSlackSink(slack.url())
	delivered, err := svc.DeliverDigest(context.Background(), 7, slackDigestPayload())
	if err != nil {
		t.Fatalf("DeliverDigest: %v", err)
	}
	if !delivered {
		t.Error("DeliverDigest = delivered=false, want true (slack confirmed)")
	}
	if slack.calls() != 1 {
		t.Errorf("slack saw %d calls, want 1", slack.calls())
	}
}

// TestWithSlackSinkRejectsCleartext: the slack transport is subject to the
// same https-only rule as the digest sink -- a non-https or malformed
// target is refused (never stored) so a mistyped slack://... URL fails
// loudly at startup (config.Load's gate) and defensively here, not as a
// silent undeliverable address.
func TestWithSlackSinkRejectsCleartext(t *testing.T) {
	receiver := newReceiver(200)
	defer receiver.close()
	repo := fake.NewStore()

	for name, url := range map[string]string{
		"cleartext":  "http://" + receiver.srv.Listener.Addr().String() + "/services/T0/B0/xyz",
		"schemeless": "//hooks.slack.com/services/T0/B0/xyz",
		"garbage":    "::not a url",
	} {
		t.Run(name, func(t *testing.T) {
			svc := fast(t, repo).WithSlackSink(url)
			delivered, err := svc.DeliverDigest(context.Background(), 7, slackDigestPayload())
			if err != nil {
				t.Fatalf("DeliverDigest: %v", err)
			}
			if delivered {
				t.Error("DeliverDigest = delivered=true, want false (no valid slack sink was configured)")
			}
			if receiver.calls() != 0 {
				t.Errorf("receiver saw %d calls, want 0 (the slack sink was rejected)", receiver.calls())
			}
		})
	}
}

// TestWithSlackSinkEmptyIsUnconfigured: the empty URL is the unconfigured
// default -- nothing extra is attempted, and a deployment with no targets
// at all stays (false, nil), the pending-not-failed verdict.
func TestWithSlackSinkEmptyIsUnconfigured(t *testing.T) {
	repo := fake.NewStore()
	svc := fast(t, repo).WithSlackSink("")
	delivered, err := svc.DeliverDigest(context.Background(), 7, slackDigestPayload())
	if err != nil {
		t.Fatalf("DeliverDigest: %v", err)
	}
	if delivered {
		t.Error("DeliverDigest = delivered=true, want false (nothing was configured)")
	}
}

// TestDeliverDigestSlackSkipsUnparseablePayload: a body that is no longer
// valid JSON cannot be summarised -- the transport is skipped (logged),
// not used to deliver a garbage message, and the call reports nothing
// delivered when slack was the only target.
func TestDeliverDigestSlackSkipsUnparseablePayload(t *testing.T) {
	slack := tlsReceiver(200)
	defer slack.close()
	repo := fake.NewStore()

	svc := fastTLS(t, repo, slack.srv).WithSlackSink(slack.url())
	delivered, err := svc.DeliverDigest(context.Background(), 7, []byte(`{not json`))
	if err == nil {
		t.Fatal("DeliverDigest = nil error, want the slack target's absence of success")
	}
	if delivered {
		t.Error("DeliverDigest = delivered=true, want false (nothing could be rendered)")
	}
	if slack.calls() != 0 {
		t.Errorf("slack receiver saw %d calls, want 0 (the payload did not parse)", slack.calls())
	}
}

// TestWithDigestSinkRejectsCleartext: a non-https sink URL is refused --
// never stored -- so a mistyped http:// target fails loudly at startup
// (config.Load's gate) and defensively here, not as a silent undeliverable
// address.
func TestWithDigestSinkRejectsCleartext(t *testing.T) {
	receiver := newReceiver(200)
	defer receiver.close()
	repo := fake.NewStore()

	for name, url := range map[string]string{
		"cleartext":  "http://" + receiver.srv.Listener.Addr().String() + "/hook",
		"schemeless": "//example.com/hook",
		"garbage":    "::not a url",
	} {
		t.Run(name, func(t *testing.T) {
			svc := fast(t, repo).WithDigestSink(url, "")
			delivered, err := svc.DeliverDigest(context.Background(), 7, []byte(`{}`))
			if err != nil {
				t.Fatalf("DeliverDigest: %v", err)
			}
			if delivered {
				t.Error("DeliverDigest = delivered=true, want false (no valid sink was configured)")
			}
			if receiver.calls() != 0 {
				t.Errorf("receiver saw %d calls, want 0 (the sink was rejected)", receiver.calls())
			}
		})
	}
}
