package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

const executionColumns = "id, name, project_id, engine, kind, cpu, memory, cluster, csv_split, tenant_id, created_by, updated_by, created_time"

// CreateExecution inserts c and returns its auto-assigned ID.
func (r *Repository) CreateExecution(ctx context.Context, c execution.Execution) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		"INSERT INTO execution (name, project_id, engine, kind, cpu, memory, cluster, csv_split, tenant_id, created_by, updated_by)"+
			" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		c.Name, c.ProjectID, string(c.Engine), string(c.Kind), c.CPU, c.Memory, c.Cluster,
		boolToInt(c.CSVSplit), nullPtr(c.TenantID), nullString(c.CreatedBy), nullString(c.UpdatedBy),
	)
	if err != nil {
		return 0, fmt.Errorf("mysql: create execution: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("mysql: create execution last id: %w", err)
	}
	return id, nil
}

// GetExecution returns the execution with id, or ports.ErrNotFound.
func (r *Repository) GetExecution(ctx context.Context, id int64) (execution.Execution, error) {
	row := r.db.QueryRowContext(ctx, "SELECT "+executionColumns+" FROM execution WHERE id = ?", id)
	c, err := scanExecution(row)
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Execution{}, ports.ErrNotFound
	}
	if err != nil {
		return execution.Execution{}, fmt.Errorf("mysql: get execution: %w", err)
	}
	return c, nil
}

// ListExecutionsByProject returns all executions belonging to projectID.
func (r *Repository) ListExecutionsByProject(ctx context.Context, projectID int64) ([]execution.Execution, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT "+executionColumns+" FROM execution WHERE project_id = ?", projectID)
	if err != nil {
		return nil, fmt.Errorf("mysql: list executions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []execution.Execution{}
	for rows.Next() {
		c, scanErr := scanExecution(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("mysql: scan execution: %w", scanErr)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate executions: %w", err)
	}
	return out, nil
}

// ListExecutionsByProjects returns all executions belonging to any of
// projectIDs, newest first (created_time desc, id desc tiebreak). An empty
// project list returns an empty result without touching the database -- the
// same guard ListProjectsByOwners keeps, so a scoping bug can never widen
// into "every execution".
func (r *Repository) ListExecutionsByProjects(ctx context.Context, projectIDs []int64) ([]execution.Execution, error) {
	out := []execution.Execution{}
	if len(projectIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(projectIDs))
	args := make([]any, len(projectIDs))
	for i, id := range projectIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	// #nosec G201 -- placeholders are fixed "?" tokens; projectIDs are bound params.
	query := fmt.Sprintf(
		"SELECT %s FROM execution WHERE project_id IN (%s) ORDER BY created_time DESC, id DESC",
		executionColumns, strings.Join(placeholders, ","))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("mysql: list executions by projects: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		c, scanErr := scanExecution(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("mysql: scan execution: %w", scanErr)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate executions: %w", err)
	}
	return out, nil
}

// DeleteExecution removes the execution with id, or ports.ErrNotFound.
// The schema has no FK cascades, so the execution's scenario links go first,
// in the same transaction -- left behind they survive as orphaned
// execution_scenario rows that keep ScenarioInUse true forever, blocking
// every delete of the linked scenario and its project. Other references
// are deliberately not touched: execution_run is the lifecycle's own state
// (cleared by teardown's StopRun), and execution_run_history,
// execution_report, and execution_launch_history are audit history that
// must outlive the execution -- reports of past runs are retained by
// design, not garbage.
func (r *Repository) DeleteExecution(ctx context.Context, id int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mysql: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "DELETE FROM execution_scenario WHERE execution_id = ?", id); err != nil {
		return fmt.Errorf("mysql: clear execution scenarios: %w", err)
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM execution WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("mysql: delete execution: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mysql: delete execution rows: %w", err)
	}
	if n == 0 {
		return ports.ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mysql: commit: %w", err)
	}
	return nil
}

// ExecutionsWithActiveRunOnCluster returns the ids of executions on cluster
// that currently have an active run (an execution_run row), ordered by id.
func (r *Repository) ExecutionsWithActiveRunOnCluster(ctx context.Context, cluster string) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT e.id FROM execution e JOIN execution_run r ON r.execution_id = e.id WHERE e.cluster = ? ORDER BY e.id",
		cluster)
	if err != nil {
		return nil, fmt.Errorf("mysql: executions with active run on cluster: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []int64{}
	for rows.Next() {
		var id int64
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, fmt.Errorf("mysql: scan execution id: %w", scanErr)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate active-run executions: %w", err)
	}
	return out, nil
}

// AddExecutionFile records a data file for the execution, or
// ports.ErrFileExists on duplicate.
func (r *Repository) AddExecutionFile(ctx context.Context, executionID int64, filename string) error {
	_, err := r.db.ExecContext(ctx, "INSERT INTO execution_data (execution_id, filename) VALUES (?, ?)", executionID, filename)
	if isDuplicateKey(err) {
		return ports.ErrFileExists
	}
	if err != nil {
		return fmt.Errorf("mysql: add execution file: %w", err)
	}
	return nil
}

// ExecutionFilesFor returns the execution's data files.
func (r *Repository) ExecutionFilesFor(ctx context.Context, executionID int64) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT filename FROM execution_data WHERE execution_id = ?", executionID)
	if err != nil {
		return nil, fmt.Errorf("mysql: execution files: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []string{}
	for rows.Next() {
		var name string
		if scanErr := rows.Scan(&name); scanErr != nil {
			return nil, fmt.Errorf("mysql: scan execution file: %w", scanErr)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate execution files: %w", err)
	}
	return out, nil
}

// DeleteExecutionFile removes a data file record, or ports.ErrNotFound.
func (r *Repository) DeleteExecutionFile(ctx context.Context, executionID int64, filename string) error {
	return execDelete(ctx, r.db, "DELETE FROM execution_data WHERE execution_id = ? AND filename = ?", executionID, filename)
}

// StoreLoadProfile replaces the execution's execution scenarios and updates
// its csv_split flag atomically. Returns ports.ErrNotFound if the execution
// does not exist.
func (r *Repository) StoreLoadProfile(ctx context.Context, executionID int64, csvSplit bool, scenarios []loadprofile.Entry) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mysql: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM execution WHERE id = ?)", executionID).Scan(&exists); err != nil {
		return fmt.Errorf("mysql: check execution: %w", err)
	}
	if !exists {
		return ports.ErrNotFound
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM execution_scenario WHERE execution_id = ?", executionID); err != nil {
		return fmt.Errorf("mysql: clear execution scenarios: %w", err)
	}
	for _, ep := range scenarios {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO execution_scenario (execution_id, scenario_id, concurrency, rampup, duration, engines, throughput, csv_split)"+
				" VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			executionID, ep.ScenarioID, ep.Concurrency, ep.Rampup, ep.Duration, ep.Engines, ep.Throughput, boolToInt(ep.CSVSplit),
		); err != nil {
			return fmt.Errorf("mysql: insert execution scenario: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE execution SET csv_split = ? WHERE id = ?", boolToInt(csvSplit), executionID); err != nil {
		return fmt.Errorf("mysql: update csv_split: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mysql: commit: %w", err)
	}
	return nil
}

// StoreExecutionConfig replaces the execution's load profile and configured
// criteria together, in one transaction -- executionapp.StoreConfig is the
// only caller of both, and a transient failure between two separate calls
// (StoreLoadProfile succeeding, SetExecutionCriteria then failing) would
// otherwise leave the execution with a new load profile but stale criteria,
// silently mismatched from what the caller believed they replaced.
func (r *Repository) StoreExecutionConfig(ctx context.Context, executionID int64, csvSplit bool, entries []loadprofile.Entry, criteria []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mysql: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM execution WHERE id = ?)", executionID).Scan(&exists); err != nil {
		return fmt.Errorf("mysql: check execution: %w", err)
	}
	if !exists {
		return ports.ErrNotFound
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM execution_scenario WHERE execution_id = ?", executionID); err != nil {
		return fmt.Errorf("mysql: clear execution scenarios: %w", err)
	}
	for _, ep := range entries {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO execution_scenario (execution_id, scenario_id, concurrency, rampup, duration, engines, throughput, csv_split)"+
				" VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			executionID, ep.ScenarioID, ep.Concurrency, ep.Rampup, ep.Duration, ep.Engines, ep.Throughput, boolToInt(ep.CSVSplit),
		); err != nil {
			return fmt.Errorf("mysql: insert execution scenario: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE execution SET csv_split = ? WHERE id = ?", boolToInt(csvSplit), executionID); err != nil {
		return fmt.Errorf("mysql: update csv_split: %w", err)
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM execution_criteria WHERE execution_id = ?", executionID); err != nil {
		return fmt.Errorf("mysql: clear execution criteria: %w", err)
	}
	for _, c := range criteria {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO execution_criteria (execution_id, criterion) VALUES (?, ?)", executionID, c,
		); err != nil {
			return fmt.Errorf("mysql: insert execution criterion: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mysql: commit: %w", err)
	}
	return nil
}

// LoadProfileFor returns the execution's current execution scenarios. Scenario names
// are not persisted, so ExecutionScenario.Name is empty.
func (r *Repository) LoadProfileFor(ctx context.Context, executionID int64) ([]loadprofile.Entry, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT scenario_id, concurrency, rampup, duration, engines, throughput, csv_split FROM execution_scenario WHERE execution_id = ?", executionID)
	if err != nil {
		return nil, fmt.Errorf("mysql: execution scenarios: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []loadprofile.Entry{}
	for rows.Next() {
		var (
			ep       loadprofile.Entry
			engines  sql.NullInt64
			csvSplit int64
		)
		if scanErr := rows.Scan(&ep.ScenarioID, &ep.Concurrency, &ep.Rampup, &ep.Duration, &engines,
			&ep.Throughput, &csvSplit); scanErr != nil {
			return nil, fmt.Errorf("mysql: scan execution scenario: %w", scanErr)
		}
		ep.Engines = int(engines.Int64)
		ep.CSVSplit = csvSplit != 0
		out = append(out, ep)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate execution scenarios: %w", err)
	}
	return out, nil
}

// SetExecutionCriteria replaces the execution's configured Taurus pass/fail
// criteria with criteria, atomically, in the given order.
func (r *Repository) SetExecutionCriteria(ctx context.Context, executionID int64, criteria []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mysql: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "DELETE FROM execution_criteria WHERE execution_id = ?", executionID); err != nil {
		return fmt.Errorf("mysql: clear execution criteria: %w", err)
	}
	for _, c := range criteria {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO execution_criteria (execution_id, criterion) VALUES (?, ?)", executionID, c,
		); err != nil {
			return fmt.Errorf("mysql: insert execution criterion: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mysql: commit: %w", err)
	}
	return nil
}

// CriteriaFor returns the execution's currently configured criteria, in the
// order they were set.
func (r *Repository) CriteriaFor(ctx context.Context, executionID int64) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT criterion FROM execution_criteria WHERE execution_id = ? ORDER BY id", executionID)
	if err != nil {
		return nil, fmt.Errorf("mysql: execution criteria: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, fmt.Errorf("mysql: scan execution criterion: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate execution criteria: %w", err)
	}
	return out, nil
}

// SetPendingCorrelationID records the trace id a Deploy minted for the run it
// precedes, overwriting any earlier one (last deploy wins).
func (r *Repository) SetPendingCorrelationID(ctx context.Context, executionID int64, correlationID string) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE execution SET pending_correlation_id = ? WHERE id = ?", correlationID, executionID)
	if err != nil {
		return fmt.Errorf("mysql: set pending correlation id: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mysql: set pending correlation id: %w", err)
	}
	if n == 0 {
		return ports.ErrNotFound
	}
	return nil
}

// PendingCorrelationID returns the id the latest Deploy minted (” when none).
func (r *Repository) PendingCorrelationID(ctx context.Context, executionID int64) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx,
		"SELECT pending_correlation_id FROM execution WHERE id = ?", executionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ports.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("mysql: pending correlation id: %w", err)
	}
	return id, nil
}

// TouchActivity stamps the execution's last-activity clock to now. Affected
// rows are deliberately not checked: NOW() has second precision, so a second
// touch within the same second changes nothing and must not be mistaken for a
// missing row (unlike SetPendingCorrelationID, a touch on a deleted execution
// is a dropped stamp, not a caller error the reaper could act on).
func (r *Repository) TouchActivity(ctx context.Context, executionID int64) error {
	if _, err := r.db.ExecContext(ctx,
		"UPDATE execution SET last_activity_at = NOW() WHERE id = ?", executionID); err != nil {
		return fmt.Errorf("mysql: touch activity: %w", err)
	}
	return nil
}

// LastActivity returns the execution's last-activity stamp; ok is false for
// no row or a never-stamped (NULL) one, either of which leaves the caller to
// fall back to another clock. err is reserved for real read failures.
func (r *Repository) LastActivity(ctx context.Context, executionID int64) (time.Time, bool, error) {
	var last sql.NullTime
	err := r.db.QueryRowContext(ctx,
		"SELECT last_activity_at FROM execution WHERE id = ?", executionID).Scan(&last)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("mysql: last activity: %w", err)
	}
	if !last.Valid {
		return time.Time{}, false, nil
	}
	return last.Time, true, nil
}

func scanExecution(s rowScanner) (execution.Execution, error) {
	var (
		c         execution.Execution
		engine    string
		kind      string
		csvSplit  int64
		tenantID  sql.NullInt64
		createdBy sql.NullString
		updatedBy sql.NullString
	)
	if err := s.Scan(&c.ID, &c.Name, &c.ProjectID, &engine, &kind, &c.CPU, &c.Memory, &c.Cluster, &csvSplit,
		&tenantID, &createdBy, &updatedBy, &c.CreatedTime); err != nil {
		return execution.Execution{}, err
	}
	c.Engine = taurus.Executor(engine)
	c.Kind = execution.Kind(kind)
	c.CSVSplit = csvSplit != 0
	c.CreatedBy = createdBy.String
	c.UpdatedBy = updatedBy.String
	if tenantID.Valid {
		c.TenantID = &tenantID.Int64
	}
	return c, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
