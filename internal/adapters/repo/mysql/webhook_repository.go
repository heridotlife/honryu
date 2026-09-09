package mysql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/heridotlife/honryu/internal/domain/webhook"
	"github.com/heridotlife/honryu/internal/ports"
)

var _ ports.WebhookStore = (*Repository)(nil)

// webhookColumns is the webhook projection, shared by every read so a
// column added to one query cannot be forgotten in another.
const webhookColumns = `id, project_id, url, secret, created_by, created_time, enabled`

// CreateWebhook stores a registration and returns its AUTO_INCREMENT id.
// created_time is the database's to assign (DEFAULT CURRENT_TIMESTAMP);
// the caller already holds everything it answered the registration with.
func (r *Repository) CreateWebhook(ctx context.Context, w webhook.Webhook) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO webhook (project_id, url, secret, created_by, enabled) VALUES (?,?,?,?,?)`,
		w.ProjectID, w.URL, nullString(w.Secret), nullString(w.CreatedBy), w.Enabled,
	)
	if err != nil {
		return 0, fmt.Errorf("mysql: create webhook: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("mysql: create webhook: %w", err)
	}
	return id, nil
}

// ListWebhooksByProject returns the project's webhooks in registration
// order (id ascending).
func (r *Repository) ListWebhooksByProject(ctx context.Context, projectID int64) ([]webhook.Webhook, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+webhookColumns+" FROM webhook WHERE project_id=? ORDER BY id", projectID)
	if err != nil {
		return nil, fmt.Errorf("mysql: list webhooks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []webhook.Webhook
	for rows.Next() {
		got, err := scanWebhook(rows)
		if err != nil {
			return nil, fmt.Errorf("mysql: scan webhook: %w", err)
		}
		out = append(out, got)
	}
	return out, rows.Err()
}

// DeleteWebhook removes one webhook, or ports.ErrNotFound when no such row
// exists under projectID. RowsAffected, not the error, is the arbiter: a
// DELETE that matched nothing succeeds silently in SQL.
func (r *Repository) DeleteWebhook(ctx context.Context, projectID, id int64) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM webhook WHERE id=? AND project_id=?`, id, projectID)
	if err != nil {
		return fmt.Errorf("mysql: delete webhook: %w", err)
	}
	return webhookRowsAffected(res, "delete webhook")
}

// SetWebhookEnabled pauses or resumes one webhook, or ports.ErrNotFound
// under the same project-scoping rule as DeleteWebhook.
func (r *Repository) SetWebhookEnabled(ctx context.Context, projectID, id int64, enabled bool) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE webhook SET enabled=? WHERE id=? AND project_id=?`, enabled, id, projectID)
	if err != nil {
		return fmt.Errorf("mysql: set webhook enabled: %w", err)
	}
	return webhookRowsAffected(res, "set webhook enabled")
}

// webhookRowsAffected translates a mutation's RowsAffected into
// ports.ErrNotFound when nothing matched.
func webhookRowsAffected(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mysql: %s: %w", what, err)
	}
	if n == 0 {
		return ports.ErrNotFound
	}
	return nil
}

// scanWebhook reads one webhook row. secret and created_by are nullable
// (deliveries may be unsigned; no-auth mode records no creator), so both
// scan through sql.NullString rather than bare zero values.
func scanWebhook(s rowScanner) (webhook.Webhook, error) {
	var (
		got       webhook.Webhook
		secret    sql.NullString
		createdBy sql.NullString
	)
	if err := s.Scan(&got.ID, &got.ProjectID, &got.URL, &secret, &createdBy, &got.CreatedTime, &got.Enabled); err != nil {
		return webhook.Webhook{}, err
	}
	got.Secret = secret.String
	got.CreatedBy = createdBy.String
	return got, nil
}
