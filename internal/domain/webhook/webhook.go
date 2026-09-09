// Package webhook models a run-completion webhook: an https endpoint
// registered per project that Honryu POSTs a run.completed event to once
// the run's report is saved. Pure domain: no I/O, no persistence, no
// delivery -- the fan-out lives in internal/app/webhookapp.
package webhook

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

// Length limits, mirroring the 0056_webhook columns so a webhook that
// validates can always be stored (and vice versa: the schema cannot hold
// what validation would reject).
const (
	// MaxURLLength matches url VARCHAR(2048).
	MaxURLLength = 2048
	// MaxSecretLength matches secret VARCHAR(128).
	MaxSecretLength = 128
)

// Validation errors. Callers compare with errors.Is.
var (
	ErrProjectRequired = errors.New("webhook: a valid project id is required")
	ErrURLRequired     = errors.New("webhook: a url is required")
	ErrURLNotHTTPS     = errors.New("webhook: url must be https (plain http would carry the signature and payload in the clear)")
	ErrURLTooLong      = errors.New("webhook: url must be at most 2048 characters")
	ErrSecretTooLong   = errors.New("webhook: secret must be at most 128 characters")
)

// Webhook is one registered receiver: every completed run (pass or fail)
// of any execution in ProjectID gets a POST to URL.
type Webhook struct {
	// ID is the storage-assigned row identity; zero before Create.
	ID int64
	// ProjectID is the project whose runs this webhook observes.
	ProjectID int64
	// URL is the https endpoint deliveries POST to.
	URL string
	// Secret, when set, signs every delivery (HMAC-SHA256 over the raw
	// body, X-Honryu-Signature header). Empty means unsigned deliveries.
	Secret string
	// Enabled is storage state, not fetch-time policy: a paused webhook is
	// skipped because delivery does not list it, so pause/resume is a
	// plain update.
	Enabled bool
	// CreatedBy names the account that registered the endpoint; empty in
	// no-auth mode.
	CreatedBy string
	// CreatedTime is when the endpoint was registered.
	CreatedTime time.Time
}

// Validate checks a webhook's own invariants, independent of persistence.
// https-only is enforced here rather than at delivery time because it is a
// property of the registration, not of any one delivery: a receiver that
// cannot be reached over https must never be registered, since every later
// delivery would carry the HMAC secret's proof over a cleartext channel.
func (w Webhook) Validate() error {
	if w.ProjectID <= 0 {
		return ErrProjectRequired
	}
	raw := strings.TrimSpace(w.URL)
	if raw == "" {
		return ErrURLRequired
	}
	if len(raw) > MaxURLLength {
		return ErrURLTooLong
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ErrURLNotHTTPS
	}
	if u.Scheme != "https" {
		return ErrURLNotHTTPS
	}
	if len(w.Secret) > MaxSecretLength {
		return ErrSecretTooLong
	}
	return nil
}
