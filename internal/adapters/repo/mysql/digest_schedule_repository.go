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

var _ ports.DigestScheduleStore = (*Repository)(nil)

// UpsertDigestSchedule stores the project's schedule configuration. The
// insert deliberately does not touch last_fired on conflict: re-enabling
// or switching period keeps the bookmark, so fire times tile from where
// they left off rather than restarting the clock.
func (r *Repository) UpsertDigestSchedule(ctx context.Context, projectID int64, period digest.Period, enabled bool) error {
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO digest_schedule (project_id, period, enabled) VALUES (?,?,?)`+
			` ON DUPLICATE KEY UPDATE period=VALUES(period), enabled=VALUES(enabled)`,
		projectID, string(period), enabled,
	); err != nil {
		return fmt.Errorf("mysql: upsert digest schedule: %w", err)
	}
	return nil
}

// GetDigestSchedule returns the project's schedule, or ports.ErrNotFound
// when none is configured.
func (r *Repository) GetDigestSchedule(ctx context.Context, projectID int64) (digest.Schedule, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT project_id, period, enabled, last_fired FROM digest_schedule WHERE project_id=?`, projectID)
	got, err := scanDigestSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return digest.Schedule{}, ports.ErrNotFound
	}
	if err != nil {
		return digest.Schedule{}, fmt.Errorf("mysql: get digest schedule: %w", err)
	}
	return got, nil
}

// DeleteDigestSchedule removes the project's schedule, or
// ports.ErrNotFound when none exists. RowsAffected, not the error, is the
// arbiter: a DELETE that matched nothing succeeds silently in SQL.
func (r *Repository) DeleteDigestSchedule(ctx context.Context, projectID int64) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM digest_schedule WHERE project_id=?`, projectID)
	if err != nil {
		return fmt.Errorf("mysql: delete digest schedule: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mysql: delete digest schedule: %w", err)
	}
	if n == 0 {
		return ports.ErrNotFound
	}
	return nil
}

// ClaimDueDigestSchedule claims one due, enabled schedule, stamping
// last_fired=now and returning the row. Dueness is per-row (the period
// word cannot be added to a timestamp in SQL), so candidates are read
// most-overdue-first and each claim is a conditional UPDATE whose WHERE
// re-checks dueness via a computed cutoff -- affected rows, the
// ClaimDueOccurrence pattern: a second replica racing the same row stamps
// nothing, sees 0 rows, and moves to the next candidate.
func (r *Repository) ClaimDueDigestSchedule(ctx context.Context, now time.Time) (digest.Schedule, bool, error) {
	// Never-fired rows first, then oldest bookmark: a project that has
	// never been digested is the most overdue there is.
	rows, err := r.db.QueryContext(ctx,
		`SELECT project_id, period, enabled, last_fired FROM digest_schedule`+
			` WHERE enabled=1 ORDER BY last_fired IS NULL DESC, last_fired ASC, project_id ASC`)
	if err != nil {
		return digest.Schedule{}, false, fmt.Errorf("mysql: list due digest schedules: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		candidate, err := scanDigestSchedule(rows)
		if err != nil {
			return digest.Schedule{}, false, fmt.Errorf("mysql: scan digest schedule: %w", err)
		}
		// Skip not-yet-due rows before touching them: the candidate list is
		// ordered most-overdue-first but SQL cannot compute dueness from the
		// period word, so without this the claim would attempt (and roll back)
		// one UPDATE per healthy row on every tick -- write amplification for
		// nothing. The conditional UPDATE below remains the authority.
		if candidate.LastFired != nil && now.Before(candidate.LastFired.Add(candidate.Period.Duration())) {
			continue
		}
		var cutoff any // NULL never matches a real last_fired, so never-fired rows pass the guard
		if candidate.LastFired != nil {
			cutoff = now.Add(-candidate.Period.Duration())
		}
		res, err := r.db.ExecContext(ctx,
			`UPDATE digest_schedule SET last_fired=? WHERE project_id=? AND enabled=1`+
				` AND (last_fired IS NULL OR last_fired <= ?)`,
			now, candidate.ProjectID, cutoff,
		)
		if err != nil {
			return digest.Schedule{}, false, fmt.Errorf("mysql: claim due digest schedule: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return digest.Schedule{}, false, fmt.Errorf("mysql: claim due digest schedule: %w", err)
		}
		if n == 1 {
			// Report the row as claimed, with the stamp the claim wrote.
			stamped := now
			candidate.LastFired = &stamped
			return candidate, true, nil
		}
		// Zero rows: another replica claimed it (or it was disabled)
		// between the read and the guard -- try the next candidate.
	}
	if err := rows.Err(); err != nil {
		return digest.Schedule{}, false, fmt.Errorf("mysql: iterate due digest schedules: %w", err)
	}
	return digest.Schedule{}, false, nil
}

// scanDigestSchedule reads one digest_schedule row. last_fired is nullable
// (never fired); period is validated on the way out so a hand-edited row
// outside the grammar surfaces as an error, not as a Period the scheduler
// would silently mis-time.
func scanDigestSchedule(s rowScanner) (digest.Schedule, error) {
	var (
		got       digest.Schedule
		period    string
		lastFired sql.NullTime
	)
	if err := s.Scan(&got.ProjectID, &period, &got.Enabled, &lastFired); err != nil {
		return digest.Schedule{}, err
	}
	parsed, err := digest.ParsePeriod(period)
	if err != nil {
		return digest.Schedule{}, fmt.Errorf("mysql: digest schedule %d: %w", got.ProjectID, err)
	}
	got.Period = parsed
	if lastFired.Valid {
		t := lastFired.Time
		got.LastFired = &t
	}
	return got, nil
}
