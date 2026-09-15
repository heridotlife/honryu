package mysql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/heridotlife/honryu/internal/domain/slo"
	"github.com/heridotlife/honryu/internal/ports"
)

var _ ports.SLOStore = (*Repository)(nil)

// sloColumns is the SLO projection, shared by every read so a column added
// to one query cannot be forgotten in another. The three targets are
// nullable (an SLO may pin any subset), so all three scan through
// sql.NullFloat64.
const sloColumns = `id, project_id, name, target_p95_ms, target_error_rate, target_success_ratio, created_time`

// CreateSLO stores an objective and returns its AUTO_INCREMENT id.
// created_time is the database's to assign (DEFAULT CURRENT_TIMESTAMP); the
// caller has already validated the object, and the (project_id, name) unique
// key backs the use-case's duplicate pre-check for concurrent creates.
func (r *Repository) CreateSLO(ctx context.Context, s slo.SLO) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO project_slo (project_id, name, target_p95_ms, target_error_rate, target_success_ratio) VALUES (?,?,?,?,?)`,
		s.ProjectID, s.Name, nullPtr(s.TargetP95MS), nullPtr(s.TargetErrorRate), nullPtr(s.TargetSuccessRatio),
	)
	if err != nil {
		return 0, fmt.Errorf("mysql: create slo: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("mysql: create slo: %w", err)
	}
	return id, nil
}

// ListSLOsByProject returns the project's SLOs in definition order (id
// ascending); the unique key's leftmost column serves the filter.
func (r *Repository) ListSLOsByProject(ctx context.Context, projectID int64) ([]slo.SLO, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+sloColumns+" FROM project_slo WHERE project_id=? ORDER BY id", projectID)
	if err != nil {
		return nil, fmt.Errorf("mysql: list slos: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []slo.SLO
	for rows.Next() {
		got, err := scanSLO(rows)
		if err != nil {
			return nil, fmt.Errorf("mysql: scan slo: %w", err)
		}
		out = append(out, got)
	}
	return out, rows.Err()
}

// GetSLO returns one SLO, or ports.ErrNotFound when no such row exists under
// projectID (including when it exists under a different project: not this
// project's to read).
func (r *Repository) GetSLO(ctx context.Context, projectID, id int64) (slo.SLO, error) {
	got, err := scanSLO(r.db.QueryRowContext(ctx,
		"SELECT "+sloColumns+" FROM project_slo WHERE id=? AND project_id=?", id, projectID))
	if err != nil {
		if err == sql.ErrNoRows {
			return slo.SLO{}, ports.ErrNotFound
		}
		return slo.SLO{}, fmt.Errorf("mysql: get slo: %w", err)
	}
	return got, nil
}

// DeleteSLO removes one SLO, or ports.ErrNotFound when nothing matched --
// RowsAffected, not the error, is the arbiter: a DELETE that matched nothing
// succeeds silently in SQL.
func (r *Repository) DeleteSLO(ctx context.Context, projectID, id int64) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM project_slo WHERE id=? AND project_id=?`, id, projectID)
	if err != nil {
		return fmt.Errorf("mysql: delete slo: %w", err)
	}
	return webhookRowsAffected(res, "delete slo")
}

// scanSLO reads one SLO row; every target column is nullable.
func scanSLO(s rowScanner) (slo.SLO, error) {
	var (
		got    slo.SLO
		p95    sql.NullFloat64
		errate sql.NullFloat64
		sratio sql.NullFloat64
	)
	if err := s.Scan(&got.ID, &got.ProjectID, &got.Name, &p95, &errate, &sratio, &got.CreatedTime); err != nil {
		return slo.SLO{}, err
	}
	got.TargetP95MS = nullFloat(p95)
	got.TargetErrorRate = nullFloat(errate)
	got.TargetSuccessRatio = nullFloat(sratio)
	return got, nil
}

// nullFloat converts a nullable ratio/target column to the domain's
// nil-means-untracked pointer shape.
func nullFloat(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}
