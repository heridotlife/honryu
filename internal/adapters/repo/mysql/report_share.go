package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/ports"
)

var _ ports.ShareStore = (*Repository)(nil)

// shareColumns is the run_share projection, shared by every read so a column
// added to one query cannot be forgotten in another.
const shareColumns = `id, run_id, token, created_by, created_time, expires_time`

// CreateShare records a newly minted share token. The row's id and
// created_time are the database's to assign (AUTO_INCREMENT / DEFAULT
// CURRENT_TIMESTAMP); the caller already holds everything it answered the
// issue request with.
func (r *Repository) CreateShare(ctx context.Context, runID int64, token, createdBy string, expires *time.Time) error {
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO run_share (run_id, token, created_by, expires_time) VALUES (?,?,?,?)`,
		runID, token, nullString(createdBy), expires,
	); err != nil {
		return fmt.Errorf("mysql: create share: %w", err)
	}
	return nil
}

// GetShareByToken resolves a token to its share, or ports.ErrNotFound. An
// expired row is still returned -- expiry is fetch-time policy, not storage
// state (see report.ShareToken.ExpiredAt).
func (r *Repository) GetShareByToken(ctx context.Context, token string) (report.ShareToken, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+shareColumns+" FROM run_share WHERE token=?", token)
	got, err := scanShare(row)
	if errors.Is(err, sql.ErrNoRows) {
		return report.ShareToken{}, ports.ErrNotFound
	}
	if err != nil {
		return report.ShareToken{}, fmt.Errorf("mysql: get share: %w", err)
	}
	return got, nil
}

// ListSharesByRun returns a run's links in issue order (id ascending, which
// is the order they were minted).
func (r *Repository) ListSharesByRun(ctx context.Context, runID int64) ([]report.ShareToken, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+shareColumns+" FROM run_share WHERE run_id=? ORDER BY id", runID)
	if err != nil {
		return nil, fmt.Errorf("mysql: list shares: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []report.ShareToken
	for rows.Next() {
		got, err := scanShare(rows)
		if err != nil {
			return nil, fmt.Errorf("mysql: scan share: %w", err)
		}
		out = append(out, got)
	}
	return out, rows.Err()
}

// DeleteShare removes one token, or ports.ErrNotFound when no such token
// exists for the run. RowsAffected, not the error, is the arbiter: a DELETE
// that matched nothing succeeds silently in SQL.
func (r *Repository) DeleteShare(ctx context.Context, runID int64, token string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM run_share WHERE run_id=? AND token=?`, runID, token)
	if err != nil {
		return fmt.Errorf("mysql: delete share: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mysql: delete share: %w", err)
	}
	if n == 0 {
		return ports.ErrNotFound
	}
	return nil
}

// scanShare reads one run_share row. created_by and expires_time are
// nullable (no-auth mode mints no creator; a link may never expire), so
// both scan through their sql.Null forms rather than bare zero values.
func scanShare(s rowScanner) (report.ShareToken, error) {
	var (
		got       report.ShareToken
		createdBy sql.NullString
		expires   sql.NullTime
	)
	if err := s.Scan(&got.ID, &got.RunID, &got.Token, &createdBy, &got.CreatedTime, &expires); err != nil {
		return report.ShareToken{}, err
	}
	got.CreatedBy = createdBy.String
	if expires.Valid {
		t := expires.Time
		got.Expires = &t
	}
	return got, nil
}
