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
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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

// DeliverEvent synchronously POSTs an already-serialised event payload to
// each of the project's enabled webhooks. Deliver's generic sibling: the
// same signing, bounds, and HTTP client as a run.completed delivery, for
// events another use-case mints whole (phase 42: report.digest, whose
// payload is the digest use-case's to build and store verbatim). The
// caller's bytes are posted as-is -- the signature covers exactly what was
// stored, so a receiver can cross-check a digest row against its delivery.
//
// Every enabled webhook is attempted however earlier ones answer; the
// first failure is returned, the rest logged, mirroring Deliver's
// error aggregation. Synchronous on purpose: the callers are background
// loops (the digest scheduler), not request paths, so wanting the outcome
// is free.
func (s *Service) DeliverEvent(ctx context.Context, projectID int64, event string, body []byte) error {
	hooks, err := s.repo.ListWebhooksByProject(ctx, projectID)
	if err != nil {
		return err
	}
	var firstErr error
	for _, w := range hooks {
		if !w.Enabled {
			continue
		}
		if err := s.deliverOne(ctx, w, body); err != nil {
			s.log.Warn("webhook: event delivery failed", "event", event, "webhook_id", w.ID, "url", redact(w.URL), "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
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

// redact keeps query strings and fragments out of delivery log lines --
// they are the part of a receiver URL most likely to carry a token.
func redact(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		return raw[:i]
	}
	return raw
}
