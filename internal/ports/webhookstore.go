package ports

import (
	"context"

	"github.com/heridotlife/honryu/internal/domain/webhook"
)

// WebhookStore persists the webhook endpoints registered per project.
//
// A webhook is a projection of an operator's registration, not an
// aggregate with invariants spanning rows: create validates the domain
// object (https-only, length limits) and the store records it. Scoping is
// by project everywhere -- a webhook is only ever read, paused, or deleted
// through the project it belongs to, which is what stops one project's
// operator from touching another's receivers even before HTTP
// authorization gets involved.
type WebhookStore interface {
	// CreateWebhook stores w and returns its storage-assigned id. The
	// caller has already validated w.
	CreateWebhook(ctx context.Context, w webhook.Webhook) (int64, error)
	// ListWebhooksByProject returns the project's webhooks in
	// registration order (oldest first). Both enabled and paused rows are
	// returned: the pause state is the caller's to filter on, the issuing
	// UI lists both.
	ListWebhooksByProject(ctx context.Context, projectID int64) ([]webhook.Webhook, error)
	// DeleteWebhook removes one webhook, or ErrNotFound when no such
	// webhook exists under projectID (including when it exists under a
	// different project: not this project's to delete).
	DeleteWebhook(ctx context.Context, projectID, id int64) error
	// SetWebhookEnabled pauses (false) or resumes (true) one webhook, or
	// ErrNotFound under the same project-scoping rule as DeleteWebhook.
	SetWebhookEnabled(ctx context.Context, projectID, id int64, enabled bool) error
}
