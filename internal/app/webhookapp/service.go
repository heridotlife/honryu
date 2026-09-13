// Package webhookapp is the run-completion webhook use-case: it
// administers the endpoints registered per project, and once a run's
// report is saved it fans a run.completed event out to each enabled
// endpoint.
//
// Honryu delivers a notification, it does not become the receiver's mail
// server -- the same propagates-not-traces law the correlation id follows.
// Delivery is best-effort with bounded retries on a background worker of
// its own; a slow, broken, or absent receiver can never delay or fail the
// run it is being told about. metricsapp holds the completion hook
// (Service.RunCompleted), so every finalisation path -- natural
// completion, Stop/Purge, the orphan sweep -- notifies through this one
// entry point.
package webhookapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/webhook"
	"github.com/heridotlife/honryu/internal/ports"
)

// EventRunCompleted is the event name every delivery carries; a receiver
// that only cares about finished runs can switch on it and ignore whatever
// events a later phase might add.
const EventRunCompleted = "run.completed"

// Delivery bounds. Attempts/timeout/backoff are deliberately small and
// fixed: a webhook is a notification, not a transaction -- three tries
// over ~10s covers a blip, and anything still failing is dropped with a
// log line, because a receiver that is down must not accumulate work that
// delays the runs behind it.
const (
	deliverAttempts = 3
	deliverTimeout  = 10 * time.Second
	deliverBackoff  = 5 * time.Second
	// workerCount caps concurrent deliveries so a burst of completions
	// cannot open one connection per webhook against a single receiver.
	workerCount = 4
	// defaultQueueCapacity bounds the pending-delivery buffer; overflow is
	// dropped with a log line, never allowed to block a run's finalisation.
	defaultQueueCapacity = 64
)

// SignatureHeader carries the delivery's HMAC. Its value is
// "sha256=<hex>" so a receiver can pick the hash without parsing a bare
// digest, the same shape GitHub's webhook signatures use.
const SignatureHeader = "X-Honryu-Signature"

// Repo is the persistence webhookapp needs: the webhook registry itself,
// plus the execution's configured pass/fail criteria so a delivery can say
// which thresholds the run tripped, not only that it tripped something.
type Repo interface {
	ports.WebhookStore
	// CriteriaFor returns the execution's currently configured criteria,
	// evaluated against the delivered report's own measurements.
	CriteriaFor(ctx context.Context, executionID int64) ([]string, error)
}

// Service implements the webhook use-cases: registration CRUD plus the
// run-completion fan-out.
type Service struct {
	repo Repo
	// client is dedicated to deliveries so its transport cannot be shared
	// with a caller's request path in a way that couples their timeouts.
	client *http.Client
	log    *slog.Logger
	// timeout bounds one attempt; backoff is the wait between attempts.
	timeout time.Duration
	backoff time.Duration

	// queue carries pending deliveries to the workers. Bounded: a full
	// queue drops rather than blocks, because the run being finalised must
	// never wait on a receiver.
	queue chan delivery
	// sink, when non-nil, is the deploy-wide digest sink (WithDigestSink,
	// phase 60): one https endpoint every fired report.digest is also
	// POSTed to, alongside the firing project's own webhooks.
	sink *webhook.Webhook
	// slack, when non-nil, is the deploy-wide Slack transport (WithSlackSink,
	// phase 62): the digest is re-rendered as Slack's {"text": ...} message
	// shape and posted here after the raw-JSON targets. Secret-less by
	// construction -- Slack's token lives in the URL path, so there is
	// nothing to sign.
	slack *webhook.Webhook
	// email, when non-nil, is the deploy-wide SMTP transport (WithEmailSink,
	// phase 62): the digest rides net/smtp to the relay as a plain message
	// after the HTTP targets.
	email *emailSink
	// dial opens the email transport's relay connection, bounded by the
	// per-attempt context. A seam, not a global: tests hand back scripted
	// connections, and nothing else in the process shares it.
	dial dialFunc
	// emailTLS, when set, is the STARTTLS client config for the email
	// transport (WithEmailTLSConfig) -- for deployments pinning their own
	// trust roots and for tests facing a relay whose chain the process does
	// not trust. Nil means Go's default verification against the relay's
	// hostname.
	emailTLS *tls.Config
	// startOnce starts the workers lazily on the first delivery, so a
	// deployment with no webhooks registered runs zero background
	// goroutines.
	startOnce sync.Once
}

// delivery is one webhook POST waiting for a worker: the pre-serialised
// body (identical bytes to what the signature covers) plus the receiver.
type delivery struct {
	hook webhook.Webhook
	body []byte
}

// NewService wires the webhook service.
func NewService(repo Repo) *Service {
	return &Service{
		repo:    repo,
		client:  &http.Client{},
		log:     slog.Default(),
		timeout: deliverTimeout,
		backoff: deliverBackoff,
		queue:   make(chan delivery, defaultQueueCapacity),
		dial: func(ctx context.Context, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		},
	}
}

// WithLogger overrides the delivery logger. Returns the receiver for
// chaining.
func (s *Service) WithLogger(log *slog.Logger) *Service {
	if log != nil {
		s.log = log
	}
	return s
}

// WithTimeout overrides the per-attempt delivery timeout. Returns the
// receiver for chaining.
func (s *Service) WithTimeout(d time.Duration) *Service {
	if d > 0 {
		s.timeout = d
	}
	return s
}

// WithClient overrides the delivery HTTP client. The default is a bare
// &http.Client{} whose transport is dedicated to deliveries so no caller's
// request path can couple its timeouts into them; an override exists for
// tests (a receiver whose TLS chain the process does not trust) and for
// deployments pinning their own trust roots -- an override must stay
// dedicated to deliveries for the same reason. Returns the receiver for
// chaining.
func (s *Service) WithClient(c *http.Client) *Service {
	if c != nil {
		s.client = c
	}
	return s
}

// WithBackoff overrides the wait between delivery attempts. Returns the
// receiver for chaining.
func (s *Service) WithBackoff(d time.Duration) *Service {
	if d > 0 {
		s.backoff = d
	}
	return s
}

// WithQueueCapacity overrides the pending-delivery buffer's size. Returns
// the receiver for chaining.
func (s *Service) WithQueueCapacity(n int) *Service {
	if n > 0 {
		s.queue = make(chan delivery, n)
	}
	return s
}

// WithDigestSink points the digest lane (DeliverDigest) at the
// deployment's one digest sink, on top of whatever webhooks the firing
// project has registered. An empty url unsets nothing and configures
// nothing -- the unconfigured default. The https-only rule mirrors
// webhook.Validate's (a sink is a webhook without a project row): a
// cleartext or malformed sink is refused here, logged, and never stored,
// with config.Load's gate as the deployment-level enforcement that fails
// startup outright.
func (s *Service) WithDigestSink(raw, secret string) *Service {
	if raw == "" {
		return s
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Scheme != "https" {
		s.log.Error("webhook: digest sink rejected (must be an absolute https URL); digest sink disabled", "url", redact(raw))
		return s
	}
	s.sink = &webhook.Webhook{URL: strings.TrimSpace(raw), Secret: secret}
	return s
}

// WithSlackSink points the digest lane (DeliverDigest) at a Slack incoming
// webhook (phase 62), the digest lane's second transport: the stored digest
// payload is summarised into Slack's {"text": ...} message shape (see
// slackDigestBody) and POSTed there after the raw-JSON targets, while
// project webhooks and the digest sink keep receiving the bytes as stored.
// The same https-only rule as WithDigestSink, for the same reason -- a
// cleartext or malformed target is refused here, logged, and never stored,
// with config.Load's gate as the deployment-level enforcement that fails
// startup outright. No secret: Slack's credential is the URL path itself,
// so the delivery goes out unsigned exactly like a secret-less webhook.
func (s *Service) WithSlackSink(raw string) *Service {
	if raw == "" {
		return s
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Scheme != "https" {
		s.log.Error("webhook: slack sink rejected (must be an absolute https URL); slack digest delivery disabled", "url", redact(raw))
		return s
	}
	s.slack = &webhook.Webhook{URL: strings.TrimSpace(raw)}
	return s
}

// WithEmailSink points the digest lane (DeliverDigest) at an SMTP relay
// (phase 62), the digest lane's third transport: the stored digest payload
// is wrapped as a plain email (buildDigestEmail) and delivered over net/smtp
// after the HTTP targets, with the same attempt/backoff/timeout bounds.
// The URL shape is
//
//	smtp://[user:pass@]host[:port]?from=<sender>&to=<comma-separated recipients>[&allow_insecure=true]
//
// STARTTLS is REQUIRED: the transport refuses a relay that does not offer
// it, with an error naming the allow_insecure=true override -- plaintext
// SMTP is opt-in per relay, never a fallback. (Credentials on an insecure
// connection are additionally refused by net/smtp's own PlainAuth guard,
// which only allows them unencrypted to localhost.) An empty url unsets
// nothing and configures nothing -- the unconfigured default. A malformed
// URL (wrong scheme, no host, no from=/to=, a line break smuggled into an
// address) is refused here, logged with the credentials stripped, and never
// stored, with config.Load's gate as the deployment-level enforcement that
// fails startup outright. Returns the receiver for chaining.
func (s *Service) WithEmailSink(raw string) *Service {
	if raw == "" {
		return s
	}
	sink, err := parseEmailSink(raw)
	if err != nil {
		s.log.Error("webhook: email sink rejected; email digest delivery disabled", "url", redactSMTPURL(raw), "error", err)
		return s
	}
	s.email = sink
	return s
}

// WithEmailTLSConfig overrides the email transport's STARTTLS client
// config. The default is Go's standard certificate verification against
// the relay's hostname; an override exists for deployments pinning their
// own trust roots and for tests (a relay whose chain the process does not
// trust). Returns the receiver for chaining.
func (s *Service) WithEmailTLSConfig(conf *tls.Config) *Service {
	if conf != nil {
		s.emailTLS = conf
	}
	return s
}

// WithEmailDial overrides how the email transport opens its relay
// connection. The default is a plain net.Dialer bounded by the delivery
// attempt's context; an override exists for tests (scripted connections),
// and like the HTTP client it must stay dedicated to deliveries. Returns
// the receiver for chaining.
func (s *Service) WithEmailDial(fn func(ctx context.Context, addr string) (net.Conn, error)) *Service {
	if fn != nil {
		s.dial = fn
	}
	return s
}

// Create registers a webhook for a project. The URL must be https -- a
// cleartext endpoint would carry the HMAC secret's proof (and the payload)
// in the clear, so it is rejected here, at registration, with a clear
// message rather than silently accepted and quietly never delivered to.
func (s *Service) Create(ctx context.Context, projectID int64, url, secret, createdBy string) (webhook.Webhook, error) {
	w := webhook.Webhook{ProjectID: projectID, URL: url, Secret: secret, Enabled: true, CreatedBy: createdBy}
	if err := w.Validate(); err != nil {
		return webhook.Webhook{}, err
	}
	id, err := s.repo.CreateWebhook(ctx, w)
	if err != nil {
		return webhook.Webhook{}, err
	}
	w.ID = id
	return w, nil
}

// List returns a project's webhooks in registration order.
func (s *Service) List(ctx context.Context, projectID int64) ([]webhook.Webhook, error) {
	return s.repo.ListWebhooksByProject(ctx, projectID)
}

// Delete removes one of a project's webhooks.
func (s *Service) Delete(ctx context.Context, projectID, id int64) error {
	return s.repo.DeleteWebhook(ctx, projectID, id)
}

// SetEnabled pauses (false) or resumes (true) one of a project's webhooks.
func (s *Service) SetEnabled(ctx context.Context, projectID, id int64, enabled bool) error {
	return s.repo.SetWebhookEnabled(ctx, projectID, id, enabled)
}

// RunCompleted is the run-completion hook metricsapp calls once a run's
// report is saved: it lists the project's enabled webhooks and queues a
// delivery to each. Fire-and-forget by construction -- the HTTP work
// happens on background workers with their own context, a full queue is
// dropped with a log line, and nothing here can fail the run. A project
// with no enabled webhooks costs one registry read and returns.
func (s *Service) RunCompleted(ctx context.Context, projectID int64, rep report.Report) {
	hooks, err := s.repo.ListWebhooksByProject(ctx, projectID)
	if err != nil {
		// A registry read failing must not fail the run, and must not
		// silently swallow the notification either -- the log line is the
		// operator's only trace that a delivery was lost.
		s.log.Error("webhook: list webhooks for delivery", "project_id", projectID, "run_id", rep.RunID, "error", err)
		return
	}
	enabled := make([]webhook.Webhook, 0, len(hooks))
	for _, w := range hooks {
		if w.Enabled {
			enabled = append(enabled, w)
		}
	}
	if len(enabled) == 0 {
		return
	}
	crits, err := s.repo.CriteriaFor(ctx, rep.ExecutionID)
	if err != nil {
		// Best effort, the criteria layer's own tolerance: a failed read
		// delivers an empty verdict rather than losing the notification.
		s.log.Warn("webhook: read criteria for delivery", "execution_id", rep.ExecutionID, "error", err)
		crits = nil
	}
	s.startOnce.Do(s.startWorkers)
	for _, w := range enabled {
		body, err := buildBody(w.ProjectID, rep, crits)
		if err != nil {
			s.log.Error("webhook: build payload", "run_id", rep.RunID, "error", err)
			continue
		}
		select {
		case s.queue <- delivery{hook: w, body: body}:
		default:
			// The buffer is full: receivers are too slow. Drop with a log
			// line -- blocking here would block the run's finalisation,
			// which is the one thing this use-case must never do.
			s.log.Warn("webhook: delivery queue full; dropping", "webhook_id", w.ID, "run_id", rep.RunID)
		}
	}
}

// startWorkers launches the delivery pool. Workers use their own context
// (context.Background()): a delivery must outlive the request whose run it
// reports -- the caller's context is cancelled the moment the response is
// written, long before a retrying delivery is done.
func (s *Service) startWorkers() {
	for i := 0; i < workerCount; i++ {
		go func() {
			for d := range s.queue {
				if err := s.deliverOne(context.Background(), d.hook, d.body); err != nil {
					s.log.Warn("webhook: delivery abandoned", "webhook_id", d.hook.ID, "url", redact(d.hook.URL), "run_id", runIDOf(d.body), "error", err)
				}
			}
		}()
	}
}

// Deliver synchronously POSTs the run-completed event to each webhook.
// The back door RunCompleted's queue sits in front of: the same delivery
// path, unqueued, for tests and for any caller that wants the outcome.
func (s *Service) Deliver(ctx context.Context, rep report.Report, hooks []webhook.Webhook) error {
	var firstErr error
	for _, w := range hooks {
		crits, err := s.repo.CriteriaFor(ctx, rep.ExecutionID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		body, err := buildBody(w.ProjectID, rep, crits)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := s.deliverOne(ctx, w, body); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// DeliverDigest synchronously POSTs an already-serialised report.digest
// payload to every enabled webhook of the project and, when one is
// configured (WithDigestSink), to the deploy-wide digest sink; a Slack
// transport (WithSlackSink, phase 62) is attempted after them with the
// same digest re-rendered as Slack's message shape. The digest lane's own
// entry point (phase 42 minted the payload, phase 60 added the sink): the
// same signing, bounds, and HTTP client as a run.completed delivery, and
// the caller's bytes are posted as-is -- the signature covers exactly what
// was stored, so a receiver can cross-check a digest row against its
// delivery. (The Slack transport is the one exception: its summary body is
// derived from the stored bytes, because Slack renders messages, not JSON
// documents.) Synchronous on purpose: the caller is a background
// loop (the digest scheduler's finalize), not a request path, so wanting
// the outcome is free.
//
// The answer is a tri-state the digest record's delivery status is built
// on: (true, nil) when at least one target confirmed -- the digest got out;
// (false, err) when targets were attempted and none confirmed, or the
// registry read failed; (false, nil) when there was nothing to notify at
// all, which is "nothing to do", not a failure. Per-target failures are
// logged and never stop the remaining targets; a single healthy receiver is
// enough for delivered.
func (s *Service) DeliverDigest(ctx context.Context, projectID int64, body []byte) (bool, error) {
	hooks, err := s.repo.ListWebhooksByProject(ctx, projectID)
	if err != nil {
		return false, err
	}
	targets := make([]webhook.Webhook, 0, len(hooks)+1)
	for _, w := range hooks {
		if w.Enabled {
			targets = append(targets, w)
		}
	}
	if s.sink != nil {
		targets = append(targets, *s.sink)
	}
	if len(targets) == 0 && s.slack == nil && s.email == nil {
		return false, nil
	}
	var (
		delivered bool
		firstErr  error
	)
	for _, w := range targets {
		if err := s.deliverOne(ctx, w, body); err != nil {
			s.log.Warn("webhook: digest delivery failed", "webhook_id", w.ID, "url", redact(w.URL), "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		delivered = true
	}
	if s.slack != nil {
		if slackBody, ok := slackDigestBody(body); ok {
			if err := s.deliverOne(ctx, *s.slack, slackBody); err != nil {
				s.log.Warn("webhook: digest delivery failed", "transport", "slack", "url", redact(s.slack.URL), "error", err)
				if firstErr == nil {
					firstErr = err
				}
			} else {
				delivered = true
			}
		} else {
			// A stored digest is always digestapp's own JSON, so this means
			// a bug or a corrupted row. Slack was configured and never
			// confirmed: surface a failure rather than let the record sit
			// pending (or pass on a sibling's success) with no trace of why.
			err := fmt.Errorf("webhook: digest payload did not parse; slack transport cannot render")
			s.log.Warn("webhook: digest delivery failed", "transport", "slack", "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if s.email != nil {
		if msg, err := buildDigestEmail(s.email, body, time.Now()); err == nil {
			if err := s.deliverDigestEmail(ctx, s.email, msg); err != nil {
				s.log.Warn("webhook: digest delivery failed", "transport", "email", "relay", s.email.hostport, "error", err)
				if firstErr == nil {
					firstErr = err
				}
			} else {
				delivered = true
			}
		} else {
			// Same honesty as the slack transport above: the email target
			// was configured and never confirmed, so the record must say
			// failed rather than silently pass on a sibling's success.
			s.log.Warn("webhook: digest delivery failed", "transport", "email", "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if delivered {
		return true, nil
	}
	return false, firstErr
}

// deliverOne POSTs body to the webhook with bounded retries: up to
// deliverAttempts tries, each bounded by the delivery timeout, backoff
// between tries. Any 2xx is a success; anything else (non-2xx, timeout,
// transport error) is retried until the attempts are exhausted, then
// abandoned -- returned to the caller, logged by the worker, never
// propagated to the run.
func (s *Service) deliverOne(ctx context.Context, w webhook.Webhook, body []byte) error {
	var lastErr error
	for attempt := 1; attempt <= deliverAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.attempt(ctx, w, body); err != nil {
			lastErr = err
			if attempt < deliverAttempts {
				select {
				case <-time.After(s.backoff):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("webhook: %d attempts to %s all failed: %w", deliverAttempts, redact(w.URL), lastErr)
}

// attempt is one POST. Its context is fresh per try so a timeout consuming
// one attempt does not eat the others'.
func (s *Service) attempt(ctx context.Context, w webhook.Webhook, body []byte) error {
	attemptCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "honryu-webhook/1")
	if w.Secret != "" {
		req.Header.Set(SignatureHeader, sign(w.Secret, body))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain (bounded) so the connection can be reused by the next attempt.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("receiver answered %s", http.StatusText(resp.StatusCode))
	}
	return nil
}

// sign computes the delivery signature: HMAC-SHA256 over the exact bytes
// POSTed, hex-encoded, prefixed with the hash name.
func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// runCompletedEvent is the delivered payload: what a receiver needs to
// route and render a finished run -- its identity, its verdict inputs, and
// where to read the full report. The report_url is a relative path hint
// ("/reports/{run_id}"); the receiver knows its own origin, Honryu does
// not presume to know it.
type runCompletedEvent struct {
	Event       string `json:"event"`
	RunID       int64  `json:"run_id"`
	ExecutionID int64  `json:"execution_id"`
	ProjectID   int64  `json:"project_id"`
	Engine      string `json:"engine,omitempty"`
	Outcome     string `json:"outcome"`
	Achieved    struct {
		Samples    int64   `json:"samples"`
		Throughput float64 `json:"throughput"`
	} `json:"achieved"`
	ErrorRate  float64 `json:"error_rate"`
	Thresholds struct {
		// Criteria is the execution's configured list as-is; Failing names
		// the subset this run tripped (or could not be evaluated).
		Criteria []string `json:"criteria"`
		Failing  []string `json:"failing"`
	} `json:"thresholds"`
	StartedTime time.Time `json:"started_time"`
	EndedTime   time.Time `json:"ended_time"`
	ReportURL   string    `json:"report_url"`
}

// buildBody serialises the run.completed event. Marshal cannot fail on
// this shape (no channels, no cycles, no func fields), but the error is
// propagated rather than assumed away so a future field cannot turn a
// silent-empty delivery into a mystery.
func buildBody(projectID int64, rep report.Report, criteria []string) ([]byte, error) {
	var ev runCompletedEvent
	ev.Event = EventRunCompleted
	ev.RunID = rep.RunID
	ev.ExecutionID = rep.ExecutionID
	ev.ProjectID = projectID
	ev.Engine = string(rep.Engine)
	ev.Outcome = string(rep.Outcome)
	ev.Achieved.Samples = rep.Achieved.Samples
	ev.Achieved.Throughput = rep.Achieved.Throughput
	ev.ErrorRate = rep.ErrorRate
	ev.Thresholds.Criteria = criteria
	if ev.Thresholds.Criteria == nil {
		ev.Thresholds.Criteria = []string{}
	}
	failing := rep.EvaluateCriteria(criteria)
	ev.Thresholds.Failing = make([]string, 0, len(failing))
	for _, fc := range failing {
		ev.Thresholds.Failing = append(ev.Thresholds.Failing, fc.Criterion)
	}
	ev.StartedTime = rep.StartedAt
	ev.EndedTime = rep.EndedAt
	ev.ReportURL = "/reports/" + fmt.Sprint(rep.RunID)
	return json.Marshal(&ev)
}

// runIDOf digs the run id back out of a queued body for the delivery's
// log line; a body that no longer parses logs no id rather than failing
// the log call.
func runIDOf(body []byte) string {
	var ev struct {
		RunID int64 `json:"run_id"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return "?"
	}
	return fmt.Sprint(ev.RunID)
}

// digestView is the delivery layer's own minimal read of a stored
// report.digest payload -- just the fields a Slack summary line needs.
// Deliberately NOT digestapp.Payload: the delivery layer stays decoupled
// from the digest use-case (the same independence runCompletedEvent has in
// reverse), so digestapp can grow fields without this reader caring, and a
// payload older than a new field still renders.
type digestView struct {
	ProjectID   int64     `json:"project_id"`
	Period      string    `json:"period"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	RunsTotal   int       `json:"runs_total"`
	ByOutcome   struct {
		Passed  int `json:"passed"`
		Failed  int `json:"failed"`
		Aborted int `json:"aborted"`
	} `json:"by_outcome"`
	// ThresholdFailures is the number a reader scanning for regressions
	// looks for first -- the Slack summary leads with it.
	ThresholdFailures int `json:"threshold_failures"`
}

// slackDigestBody renders a stored digest payload as Slack's message shape:
// {"text": "<compact summary>"}. Three lines -- which window for which
// project, the run/outcome counts with the threshold failures up front in
// the reader's eye, and where to read the full digest (the same relative
// path hint runCompletedEvent's report_url uses: the receiver knows its own
// origin, Honryu does not presume to know it). Returns false when the bytes
// no longer parse as a digest -- the transport is skipped (logged) rather
// than delivering a garbage or empty message.
func slackDigestBody(body []byte) ([]byte, bool) {
	var v digestView
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, false
	}
	text := fmt.Sprintf(
		"*honryu digest* -- %s window %s → %s\n%d runs · %d passed · %d failed · %d aborted · %d threshold failure%s\nDetails: /executions (project %d)",
		v.Period,
		v.WindowStart.Format("2006-01-02"), v.WindowEnd.Format("2006-01-02"),
		v.RunsTotal, v.ByOutcome.Passed, v.ByOutcome.Failed, v.ByOutcome.Aborted,
		v.ThresholdFailures, pluralSuffix(v.ThresholdFailures),
		v.ProjectID,
	)
	msg, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: text})
	if err != nil {
		return nil, false
	}
	return msg, true
}

// pluralSuffix is "" for one ("1 failure") and "s" otherwise ("0
// failures") -- the only pluralisation a summary line needs.
func pluralSuffix(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// redact keeps query strings and fragments out of delivery log lines --
// they are the part of a receiver URL most likely to carry a token.
func redact(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		return raw[:i]
	}
	return raw
}
