package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/heridotlife/honryu/internal/domain/digest"
	"github.com/heridotlife/honryu/internal/ports"
)

var _ ports.ReportDigestStore = (*Repository)(nil)

// digestColumns is the report_digest projection, shared by every read so a
// column added to one query cannot be forgotten in another.
const digestColumns = `id, project_id, period, window_start, window_end, payload, created_time`

// SaveDigest stores a fired digest and returns its AUTO_INCREMENT id.
// created_time is the database's to assign (DEFAULT CURRENT_TIMESTAMP);
// payload is stored verbatim -- the row exists so the delivered bytes can be
// re-served exactly, not so they can be normalised.
func (r *Repository) SaveDigest(ctx context.Context, d digest.Digest) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO report_digest (project_id, period, window_start, window_end, payload) VALUES (?,?,?,?,?)`,
		d.ProjectID, string(d.Period), d.WindowStart, d.WindowEnd, d.Payload,
	)
	if err != nil {
		return 0, fmt.Errorf("mysql: save digest: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("mysql: save digest: %w", err)
	}
	return id, nil
}

// ListDigestsByProject returns the project's digests, newest first (id
// descending: ids are assigned in fire order). A limit of zero or less
// omits the LIMIT clause entirely, ListReports' convention rather than
// SQL's own LIMIT 0 (which would return nothing).
func (r *Repository) ListDigestsByProject(ctx context.Context, projectID int64, limit int) ([]digest.Digest, error) {
	query := "SELECT " + digestColumns + " FROM report_digest WHERE project_id=?"
	args := []any{projectID}
	if limit > 0 {
		query += " ORDER BY id DESC LIMIT ?"
		args = append(args, limit)
	} else {
		query += " ORDER BY id DESC"
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("mysql: list digests: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []digest.Digest
	for rows.Next() {
		got, err := scanDigest(rows)
		if err != nil {
			return nil, fmt.Errorf("mysql: scan digest: %w", err)
		}
		out = append(out, got)
	}
	return out, rows.Err()
}

// LastDigestWindowEnd returns the project's most recent digest window_end
// under period, or found=false when that pairing has never fired. The
// newest row wins by id, not window_end: ids are assigned in fire order, so
// a clock step backwards cannot make an older window look like the
// bookmark.
func (r *Repository) LastDigestWindowEnd(ctx context.Context, projectID int64, period digest.Period) (time.Time, bool, error) {
	var end time.Time
	err := r.db.QueryRowContext(ctx,
		`SELECT window_end FROM report_digest WHERE project_id=? AND period=? ORDER BY id DESC LIMIT 1`,
		projectID, string(period),
	).Scan(&end)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("mysql: last digest window end: %w", err)
	}
	return end, true, nil
}

// scanDigest reads one report_digest row. period is validated on the way
// out so a hand-edited row outside the grammar surfaces as an error, not as
// a Period the rest of the code silently mis-times.
func scanDigest(s rowScanner) (digest.Digest, error) {
	var (
		got    digest.Digest
		period string
	)
	if err := s.Scan(&got.ID, &got.ProjectID, &period, &got.WindowStart, &got.WindowEnd, &got.Payload, &got.CreatedTime); err != nil {
		return digest.Digest{}, err
	}
	parsed, err := digest.ParsePeriod(period)
	if err != nil {
		return digest.Digest{}, fmt.Errorf("mysql: digest %d: %w", got.ID, err)
	}
	got.Period = parsed
	return got, nil
}
