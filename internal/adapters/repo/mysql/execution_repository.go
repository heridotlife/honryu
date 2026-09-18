package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

const executionColumns = "id, name, project_id, engine, kind, cpu, memory, cluster, fanout_targets, csv_split, tenant_id, created_by, updated_by, created_time"

// CreateExecution inserts c and returns its auto-assigned ID.
func (r *Repository) CreateExecution(ctx context.Context, c execution.Execution) (int64, error) {
	targets, err := encodeFanOutTargets(c.FanOutTargets)
	if err != nil {
		return 0, err
	}
	res, err := r.db.ExecContext(ctx,
		"INSERT INTO execution (name, project_id, engine, kind, cpu, memory, cluster, fanout_targets, csv_split, tenant_id, created_by, updated_by)"+
			" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		c.Name, c.ProjectID, string(c.Engine), string(c.Kind), c.CPU, c.Memory, c.Cluster,
		targets, boolToInt(c.CSVSplit), nullPtr(c.TenantID), nullString(c.CreatedBy), nullString(c.UpdatedBy),
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

// encodeFanOutTargets marshals a fan-out target list for the fanout_targets
// JSON column: nil (no fan-out) stays NULL, a set list becomes a JSON array.
func encodeFanOutTargets(targets []string) (any, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(targets)
	if err != nil {
		return nil, fmt.Errorf("mysql: encode fanout targets: %w", err)
	}
	return raw, nil
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

// LatestRunsForScenarios answers a whole scenario list's last-run column in
// ONE query. For each listed scenario it reads the newest execution bound to
// it -- created_time desc, id desc, the ListExecutionsByScenario order,
// picked by a NOT EXISTS anti-join, the repo's subquery idiom -- LEFT JOINed
// to that execution's newest report, started_at desc, run_id desc, the
// ListReports order, picked the same way. The report is joined by execution
// alone, never filtered by the report's own scenario_id: that is exactly
// the probe the scenario detail page makes per row (the execution's newest
// report, limit 1), so the list and the detail page can never disagree
// about one scenario.
//
// Absence is the honest empty: a scenario with no execution contributes no
// row, and a newest execution with no report yet LEFT JOINs to NULL columns
// that are dropped below -- both surface as a missing map key, "no verdict
// yet", never a fabricated one.
func (r *Repository) LatestRunsForScenarios(ctx context.Context, scenarioIDs []int64) (map[int64]execution.LastRun, error) {
	out := make(map[int64]execution.LastRun, len(scenarioIDs))
	if len(scenarioIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(scenarioIDs))
	args := make([]any, 0, len(scenarioIDs))
	for i, id := range scenarioIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	// #nosec G201 -- placeholders are fixed "?" tokens; scenarioIDs are bound params.
	query := fmt.Sprintf(`SELECT es.scenario_id, es.execution_id, r.outcome, r.started_at
		FROM execution_scenario es
		JOIN execution e ON e.id = es.execution_id
		LEFT JOIN execution_report r
			ON r.execution_id = es.execution_id
			AND NOT EXISTS (SELECT 1 FROM execution_report newer
				WHERE newer.execution_id = r.execution_id
				  AND (newer.started_at > r.started_at
				    OR (newer.started_at = r.started_at AND newer.run_id > r.run_id)))
		WHERE es.scenario_id IN (%s)
		  AND NOT EXISTS (SELECT 1
			  FROM execution_scenario newer_es
			  JOIN execution newer_e ON newer_e.id = newer_es.execution_id
			  WHERE newer_es.scenario_id = es.scenario_id
			    AND (newer_e.created_time > e.created_time
				  OR (newer_e.created_time = e.created_time AND newer_e.id > e.id)))`,
		strings.Join(placeholders, ","))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("mysql: latest runs for scenarios: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var scenarioID, executionID int64
		var outcome sql.NullString
		var startedAt sql.NullTime
		if scanErr := rows.Scan(&scenarioID, &executionID, &outcome, &startedAt); scanErr != nil {
			return nil, fmt.Errorf("mysql: scan latest run: %w", scanErr)
		}
		// NULLs mean the newest execution has not finalised a report yet:
		// no verdict, so no map entry.
		if !outcome.Valid || !startedAt.Valid {
			continue
		}
		out[scenarioID] = execution.LastRun{
			ExecutionID: executionID,
			Outcome:     taurus.Outcome(outcome.String),
			StartedAt:   startedAt.Time,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate latest runs: %w", err)
	}
	return out, nil
}

// ListExecutionsByScenario returns every execution whose load profile binds
// scenarioID -- a row in execution_scenario, the execution↔scenario link --
// newest first (created_time desc, id desc, the ListExecutionsByProjects
// order). An unknown scenario joins nothing and returns an empty list, not
// an error. The link is read through an IN-subquery so the shared bare
// executionColumns projection stays unambiguous.
func (r *Repository) ListExecutionsByScenario(ctx context.Context, scenarioID int64) ([]execution.Execution, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+executionColumns+" FROM execution WHERE id IN"+
			" (SELECT execution_id FROM execution_scenario WHERE scenario_id = ?)"+
			" ORDER BY created_time DESC, id DESC", scenarioID)
	if err != nil {
		return nil, fmt.Errorf("mysql: list executions by scenario: %w", err)
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

// ExecutionsWithActiveRunOnCluster returns the ids of executions that
// currently have an active run (an execution_run row) and run on cluster:
// the executions whose own cluster is cluster, and -- phase 88 -- every
// fan-out execution whose target list names it (a fan-out run mid-flight
// holds the target's engines exactly as a single-cluster one does, which is
// what the cluster delete guard and the Clusters page read this for).
// JSON_CONTAINS matches the quoted name inside the fanout_targets array;
// NULL (every ordinary execution) never contains anything. Ordered by id.
func (r *Repository) ExecutionsWithActiveRunOnCluster(ctx context.Context, cluster string) ([]int64, error) {
	// #nosec G201 -- no interpolation: a fixed statement with bound params.
	rows, err := r.db.QueryContext(ctx,
		"SELECT e.id FROM execution e JOIN execution_run r ON r.execution_id = e.id"+
			" WHERE e.cluster = ? OR JSON_CONTAINS(e.fanout_targets, JSON_QUOTE(?)) ORDER BY e.id",
		cluster, cluster)
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
			"INSERT INTO execution_scenario (execution_id, scenario_id, concurrency, rampup, duration, engines, throughput, csv_split, mode)"+
				" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			executionID, ep.ScenarioID, ep.Concurrency, ep.Rampup, ep.Duration, ep.Engines, ep.Throughput, boolToInt(ep.CSVSplit), nullableMode(ep.Mode),
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
			"INSERT INTO execution_scenario (execution_id, scenario_id, concurrency, rampup, duration, engines, throughput, csv_split, mode)"+
				" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			executionID, ep.ScenarioID, ep.Concurrency, ep.Rampup, ep.Duration, ep.Engines, ep.Throughput, boolToInt(ep.CSVSplit), nullableMode(ep.Mode),
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
		"SELECT scenario_id, concurrency, rampup, duration, engines, throughput, csv_split, mode FROM execution_scenario WHERE execution_id = ?", executionID)
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
			mode     sql.NullString
		)
		if scanErr := rows.Scan(&ep.ScenarioID, &ep.Concurrency, &ep.Rampup, &ep.Duration, &engines,
			&ep.Throughput, &csvSplit, &mode); scanErr != nil {
			return nil, fmt.Errorf("mysql: scan execution scenario: %w", scanErr)
		}
		ep.Engines = int(engines.Int64)
		ep.CSVSplit = csvSplit != 0
		// NULL mode = an advanced entry, the only kind before phase 90.
		ep.Mode = mode.String
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
		c            execution.Execution
		engine       string
		kind         string
		fanoutTarget []byte // JSON array or NULL; []byte (not sql.RawBytes) because RawBytes is illegal on Row.Scan
		csvSplit     int64
		tenantID     sql.NullInt64
		createdBy    sql.NullString
		updatedBy    sql.NullString
	)
	if err := s.Scan(&c.ID, &c.Name, &c.ProjectID, &engine, &kind, &c.CPU, &c.Memory, &c.Cluster, &fanoutTarget,
		&csvSplit, &tenantID, &createdBy, &updatedBy, &c.CreatedTime); err != nil {
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
	if len(fanoutTarget) > 0 {
		if err := json.Unmarshal(fanoutTarget, &c.FanOutTargets); err != nil {
			return execution.Execution{}, fmt.Errorf("mysql: decode fanout targets: %w", err)
		}
	}
	return c, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullableMode maps an entry's mode provenance onto the column's NULL
// convention: an advanced entry (empty mode) stores NULL, a mode entry
// stores its name. any-typed nil, the shape database/sql wants for a NULL
// bind parameter.
func nullableMode(mode string) any {
	if mode == "" {
		return nil
	}
	return mode
}
