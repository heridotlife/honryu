package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/heridotlife/honryu/internal/ports"
)

const scenarioVersionColumns = "id, scenario_id, version, created_time, created_by"

// AppendScenarioVersion records snapshot as scenarioID's next version
// (max+1) and returns the number assigned.
//
// The number is computed and inserted inside one transaction, under
// SELECT ... FOR UPDATE: the range lock on the (scenario_id, version)
// unique key serialises two concurrent appends for the same scenario (each
// computes max+1 against a locked read), while appends for other scenarios
// take different key ranges and proceed. The UNIQUE key is the storage
// backstop if the lock is ever bypassed; an empty createdBy is stored NULL.
func (r *Repository) AppendScenarioVersion(ctx context.Context, scenarioID int64, snapshot ports.ScenarioSnapshot, createdBy string) (int, error) {
	blob, err := json.Marshal(snapshot)
	if err != nil {
		return 0, fmt.Errorf("mysql: marshal scenario snapshot: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("mysql: append scenario version: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var next int
	err = tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) + 1 FROM scenario_versions WHERE scenario_id = ? FOR UPDATE",
		scenarioID).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("mysql: next scenario version: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO scenario_versions (scenario_id, version, snapshot, created_by) VALUES (?, ?, ?, ?)",
		scenarioID, next, blob, nullString(createdBy)); err != nil {
		return 0, fmt.Errorf("mysql: insert scenario version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("mysql: append scenario version: %w", err)
	}
	return next, nil
}

// ListScenarioVersions returns the scenario's versions, newest first.
// Always non-nil.
func (r *Repository) ListScenarioVersions(ctx context.Context, scenarioID int64) ([]ports.ScenarioVersionMeta, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+scenarioVersionColumns+" FROM scenario_versions WHERE scenario_id = ? ORDER BY version DESC",
		scenarioID)
	if err != nil {
		return nil, fmt.Errorf("mysql: list scenario versions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ports.ScenarioVersionMeta, 0)
	for rows.Next() {
		meta, scanErr := scanScenarioVersionMeta(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("mysql: scan scenario version: %w", scanErr)
		}
		out = append(out, meta)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate scenario versions: %w", err)
	}
	return out, nil
}

// ScenarioVersion returns one version, snapshot included:
// ports.ErrScenarioVersionNotFound when the scenario has no such version.
func (r *Repository) ScenarioVersion(ctx context.Context, scenarioID int64, version int) (ports.ScenarioVersion, error) {
	var (
		v       ports.ScenarioVersion
		by      sql.NullString
		blob    []byte
		created time.Time
	)
	err := r.db.QueryRowContext(ctx,
		"SELECT "+scenarioVersionColumns+", snapshot FROM scenario_versions WHERE scenario_id = ? AND version = ?",
		scenarioID, version).Scan(&v.ID, &v.ScenarioID, &v.Version, &created, &by, &blob)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.ScenarioVersion{}, ports.ErrScenarioVersionNotFound
	}
	if err != nil {
		return ports.ScenarioVersion{}, fmt.Errorf("mysql: get scenario version: %w", err)
	}
	v.CreatedTime = created
	if by.Valid {
		name := by.String
		v.CreatedBy = &name
	}
	if err := json.Unmarshal(blob, &v.Snapshot); err != nil {
		return ports.ScenarioVersion{}, fmt.Errorf("mysql: unmarshal scenario snapshot (version %d): %w", version, err)
	}
	return v, nil
}

func scanScenarioVersionMeta(s rowScanner) (ports.ScenarioVersionMeta, error) {
	var (
		meta    ports.ScenarioVersionMeta
		by      sql.NullString
		created time.Time
	)
	if err := s.Scan(&meta.ID, &meta.ScenarioID, &meta.Version, &created, &by); err != nil {
		return ports.ScenarioVersionMeta{}, err
	}
	meta.CreatedTime = created
	if by.Valid {
		name := by.String
		meta.CreatedBy = &name
	}
	return meta, nil
}

var _ ports.ScenarioVersionStore = (*Repository)(nil)
