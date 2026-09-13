package digestapp_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/digestapp"
	"github.com/heridotlife/honryu/internal/app/webhookapp"
	"github.com/heridotlife/honryu/internal/domain/digest"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/domain/webhook"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// digestReceiver is the minimal outbound receiver the end-to-end pins need:
// it records every POST's body and signature and answers with its script.
// (webhookapp's own richer receiver lives in that package's tests; this one
// stays digest-shaped.)
type digestReceiver struct {
	mu       sync.Mutex
	got      [][]byte
	sigs     []string
	statuses []int
	srv      *httptest.Server
}

func newDigestReceiver(statuses ...int) *digestReceiver {
	rc := &digestReceiver{statuses: statuses}
	rc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rc.mu.Lock()
		n := len(rc.got)
		rc.got = append(rc.got, body)
		rc.sigs = append(rc.sigs, r.Header.Get(webhookapp.SignatureHeader))
		rc.mu.Unlock()
		if n < len(rc.statuses) {
			w.WriteHeader(rc.statuses[n])
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	return rc
}

func (rc *digestReceiver) calls() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.got)
}

// lastBody/lastSig are the newest POST's payload and signature.
func (rc *digestReceiver) lastBody() []byte {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.got[len(rc.got)-1]
}

func (rc *digestReceiver) lastSig() string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.sigs[len(rc.sigs)-1]
}

// signature re-computes the expected X-Honryu-Signature for body under
// secret -- the same "sha256=<hex>" shape the receiver side verifies with.
func signature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// registerHook puts a receiver URL straight into the registry: Create's
// https-only rule is the service's to enforce at registration time, so an
// http test server can stand in for the receiver (the fake store does not
// validate -- the same shortcut webhookapp's tests take).
func registerHook(t *testing.T, store *fake.Store, projectID int64, url, secret string) {
	t.Helper()
	if _, err := store.CreateWebhook(context.Background(), webhook.Webhook{
		ProjectID: projectID, URL: url, Secret: secret, Enabled: true,
	}); err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}
}

// e2eFixture is one project with a run in the window and the full
// production delivery composition: the real webhookapp service (short
// bounds, so a failing receiver exhausts its retries in milliseconds) as
// the digest deliverer -- what both cmd/api and cmd/scheduler wire, no
// recorder stand-in.
type e2eFixture struct {
	store  *fake.Store
	svc    *digestapp.Service
	proj   int64
	execID int64
	now    time.Time
}

func newE2EFixture(t *testing.T) *e2eFixture {
	t.Helper()
	store := fake.NewStore()
	f := &e2eFixture{store: store, now: windowStart()}
	f.proj = mkProject(t, store, "e2e")
	f.execID = mkExecution(t, store, "checkout", f.proj)
	saveRun(t, store, f.execID, 1, f.now.Add(time.Hour), taurus.OutcomePassed)
	hooks := webhookapp.NewService(store).WithBackoff(time.Millisecond).WithTimeout(2 * time.Second)
	f.svc = digestapp.NewService(store).WithDeliverer(hooks)
	return f
}

// windowStart is the e2e window's start; helper-shaped so the fixture reads
// like the unit tests' window() without colliding with it.
func windowStart() time.Time {
	return time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
}

// TestFinalizeToDeliveredEndToEnd pins the full path at the seam production
// composes: Fire (the finalize point) stores the row, the real webhook
// machinery POSTs the stored bytes to the project's receiver -- signed,
// verbatim -- and the row reads delivered with the fire time as the
// confirmation stamp. One test, the whole story: store -> deliver -> record.
func TestFinalizeToDeliveredEndToEnd(t *testing.T) {
	f := newE2EFixture(t)
	rc := newDigestReceiver()
	defer rc.srv.Close()
	registerHook(t, f.store, f.proj, rc.srv.URL+"/hook", "e2e-secret")

	fireAt := f.now.Add(24 * time.Hour)
	d, err := f.svc.Fire(context.Background(), f.proj, digest.PeriodDaily, fireAt)
	if err != nil {
		t.Fatalf("Fire: %v", err)
	}
	if rc.calls() != 1 {
		t.Fatalf("receiver saw %d POSTs, want 1", rc.calls())
	}
	if string(rc.lastBody()) != string(d.Payload) {
		t.Errorf("delivered body = %s, want the stored payload verbatim", rc.lastBody())
	}
	if want := signature("e2e-secret", d.Payload); rc.lastSig() != want {
		t.Errorf("signature = %q, want %q", rc.lastSig(), want)
	}
	rows, err := f.svc.ListForProject(context.Background(), f.proj, 0)
	if err != nil {
		t.Fatalf("ListForProject: %v", err)
	}
	if rows[0].DeliveryStatus != digest.DeliveryDelivered {
		t.Errorf("row status = %q, want delivered", rows[0].DeliveryStatus)
	}
	if rows[0].DeliveredAt == nil || !rows[0].DeliveredAt.Equal(fireAt) {
		t.Errorf("row delivered_at = %v, want the fire time %v", rows[0].DeliveredAt, fireAt)
	}
}

// TestFinalizeToFailedEndToEnd: a receiver that never answers 2xx exhausts
// the shared retry bounds; the fire still succeeds (the row is the source
// of truth) and the row reads failed -- the operator's signal that this
// window never reached anyone and will never be re-sent.
func TestFinalizeToFailedEndToEnd(t *testing.T) {
	f := newE2EFixture(t)
	rc := newDigestReceiver(500, 500, 500) // deliverAttempts: give up after
	defer rc.srv.Close()
	registerHook(t, f.store, f.proj, rc.srv.URL+"/hook", "")

	if _, err := f.svc.Fire(context.Background(), f.proj, digest.PeriodDaily, f.now.Add(24*time.Hour)); err != nil {
		t.Fatalf("Fire = %v, want success despite delivery failure", err)
	}
	if rc.calls() != 3 {
		t.Errorf("receiver saw %d POSTs, want the bounded 3", rc.calls())
	}
	rows, err := f.svc.ListForProject(context.Background(), f.proj, 0)
	if err != nil {
		t.Fatalf("ListForProject: %v", err)
	}
	if rows[0].DeliveryStatus != digest.DeliveryFailed {
		t.Errorf("row status = %q, want failed", rows[0].DeliveryStatus)
	}
	if rows[0].DeliveredAt != nil {
		t.Errorf("row delivered_at = %v, want nil (nothing confirmed)", rows[0].DeliveredAt)
	}
}

// TestFinalizeWithoutTargetsStaysPendingEndToEnd: no registered hooks and
// no configured sink means the delivery lane had nothing to do; the stored
// digest must read pending -- "not attempted", not "lost".
func TestFinalizeWithoutTargetsStaysPendingEndToEnd(t *testing.T) {
	f := newE2EFixture(t)

	if _, err := f.svc.Fire(context.Background(), f.proj, digest.PeriodDaily, f.now.Add(24*time.Hour)); err != nil {
		t.Fatalf("Fire: %v", err)
	}
	rows, err := f.svc.ListForProject(context.Background(), f.proj, 0)
	if err != nil {
		t.Fatalf("ListForProject: %v", err)
	}
	if rows[0].DeliveryStatus != digest.DeliveryPending {
		t.Errorf("row status = %q, want pending (nothing was attempted)", rows[0].DeliveryStatus)
	}
	if rows[0].DeliveredAt != nil {
		t.Errorf("row delivered_at = %v, want nil", rows[0].DeliveredAt)
	}
}
