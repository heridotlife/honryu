package webhookapp_test

import (
	"context"
	"errors"
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

// TestDeliverEventPostsStoredBytesToEnabledHooks pins the generic-event
// contract (phase 42's report.digest rides it): the caller's exact bytes
// are POSTed verbatim to each enabled webhook of the project -- signed when
// a secret is set -- and paused hooks or other projects get nothing.
func TestDeliverEventPostsStoredBytesToEnabledHooks(t *testing.T) {
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
	if err := svc.DeliverEvent(context.Background(), 7, "report.digest", body); err != nil {
		t.Fatalf("DeliverEvent: %v", err)
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

// TestDeliverEventAttemptsEveryHookAndReturnsFirstError: one dead receiver
// must not stop the project's other receiver from being notified, and the
// caller still learns the delivery partially failed -- Deliver's own error
// aggregation, applied to events.
func TestDeliverEventAttemptsEveryHookAndReturnsFirstError(t *testing.T) {
	dead := newReceiver(500, 500, 500)
	defer dead.close()
	alive := newReceiver(200)
	defer alive.close()
	repo := fake.NewStore()
	register(t, repo, 7, dead.url(), "", true)
	register(t, repo, 7, alive.url(), "", true)

	svc := fast(t, repo)
	err := svc.DeliverEvent(context.Background(), 7, "report.digest", []byte(`{}`))
	if err == nil {
		t.Fatal("DeliverEvent = nil error, want the dead receiver's failure")
	}
	if alive.calls() != 1 {
		t.Errorf("healthy receiver saw %d calls after a sibling failed, want 1", alive.calls())
	}
	if dead.calls() != 3 { // deliverAttempts: the shared bounds, not a new policy
		t.Errorf("dead receiver saw %d calls, want the bounded 3", dead.calls())
	}
}

// TestDeliverEventRegistryFailure: a failing registry read surfaces as the
// call's error rather than masquerading as success.
func TestDeliverEventRegistryFailure(t *testing.T) {
	repo := fake.NewStore()
	repo.WebhookStore.ListErr = errors.New("registry down")
	svc := fast(t, repo)
	if err := svc.DeliverEvent(context.Background(), 7, "report.digest", []byte(`{}`)); err == nil {
		t.Fatal("DeliverEvent = nil error, want the registry read's failure")
	}
}
