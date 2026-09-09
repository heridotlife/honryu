package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	driver "github.com/go-sql-driver/mysql"

	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

const scenarioColumns = "id, name, project_id, kind, engine, tenant_id, created_by, updated_by, created_time"

const mysqlDupEntry = 1062

// CreateScenario inserts p and returns its auto-assigned ID.
func (r *Repository) CreateScenario(ctx context.Context, p scenario.Scenario) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		"INSERT INTO scenario (name, project_id, kind, engine, tenant_id, created_by, updated_by)"+
			" VALUES (?, ?, ?, ?, ?, ?, ?)",
		p.Name, p.ProjectID, string(p.Kind), string(p.Engine),
		nullPtr(p.TenantID), nullString(p.CreatedBy), nullString(p.UpdatedBy),
	)
	if err != nil {
		return 0, fmt.Errorf("mysql: create scenario: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("mysql: create scenario last id: %w", err)
	}
	return id, nil
}

// GetScenario returns the scenario with id, or ports.ErrNotFound.
func (r *Repository) GetScenario(ctx context.Context, id int64) (scenario.Scenario, error) {
	row := r.db.QueryRowContext(ctx, "SELECT "+scenarioColumns+" FROM scenario WHERE id = ?", id)
	p, err := scanScenario(row)
	if errors.Is(err, sql.ErrNoRows) {
		return scenario.Scenario{}, ports.ErrNotFound
	}
	if err != nil {
		return scenario.Scenario{}, fmt.Errorf("mysql: get scenario: %w", err)
	}
	return p, nil
}

// ListScenariosByProject returns all scenarios belonging to projectID.
func (r *Repository) ListScenariosByProject(ctx context.Context, projectID int64) ([]scenario.Scenario, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT "+scenarioColumns+" FROM scenario WHERE project_id = ?", projectID)
	if err != nil {
		return nil, fmt.Errorf("mysql: list scenarios: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []scenario.Scenario{}
	for rows.Next() {
		p, scanErr := scanScenario(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("mysql: scan scenario: %w", scanErr)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate scenarios: %w", err)
	}
	return out, nil
}

// DeleteScenario removes the scenario with id, or returns ports.ErrNotFound.
func (r *Repository) DeleteScenario(ctx context.Context, id int64) error {
	return execDelete(ctx, r.db, "DELETE FROM scenario WHERE id = ?", id)
}

// AddScenarioFile records a file for the scenario. isTest selects the single JMX slot
// (scenario_test_file) vs a data file (scenario_data). Returns ports.ErrFileExists on
// duplicate.
func (r *Repository) AddScenarioFile(ctx context.Context, scenarioID int64, filename string, isTest bool) error {
	table := "scenario_data"
	if isTest {
		table = "scenario_test_file"
	}
	// #nosec G201 -- table is a fixed internal constant, not user input.
	_, err := r.db.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s (scenario_id, filename) VALUES (?, ?)", table), scenarioID, filename)
	if isDuplicateKey(err) {
		return ports.ErrFileExists
	}
	if err != nil {
		return fmt.Errorf("mysql: add scenario file: %w", err)
	}
	return nil
}

// ScenarioFilesFor returns the scenario's recorded files.
func (r *Repository) ScenarioFilesFor(ctx context.Context, scenarioID int64) (ports.ScenarioFiles, error) {
	var pf ports.ScenarioFiles

	var testFile sql.NullString
	err := r.db.QueryRowContext(ctx, "SELECT filename FROM scenario_test_file WHERE scenario_id = ?", scenarioID).Scan(&testFile)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ports.ScenarioFiles{}, fmt.Errorf("mysql: scenario test file: %w", err)
	}
	pf.TestFile = testFile.String

	rows, err := r.db.QueryContext(ctx, "SELECT filename FROM scenario_data WHERE scenario_id = ?", scenarioID)
	if err != nil {
		return ports.ScenarioFiles{}, fmt.Errorf("mysql: scenario data files: %w", err)
	}
	defer func() { _ = rows.Close() }()
	pf.Data = []string{}
	for rows.Next() {
		var name string
		if scanErr := rows.Scan(&name); scanErr != nil {
			return ports.ScenarioFiles{}, fmt.Errorf("mysql: scan scenario data: %w", scanErr)
		}
		pf.Data = append(pf.Data, name)
	}
	if err := rows.Err(); err != nil {
		return ports.ScenarioFiles{}, fmt.Errorf("mysql: iterate scenario data: %w", err)
	}
	return pf, nil
}

// DeleteScenarioFile removes a file record for the scenario, or ports.ErrNotFound.
func (r *Repository) DeleteScenarioFile(ctx context.Context, scenarioID int64, filename string, isTest bool) error {
	table := "scenario_data"
	if isTest {
		table = "scenario_test_file"
	}
	// #nosec G201 -- table is a fixed internal constant, not user input.
	return execDelete(ctx, r.db, fmt.Sprintf("DELETE FROM %s WHERE scenario_id = ? AND filename = ?", table), scenarioID, filename)
}

// SetScenarioKind records how a scenario's workload is expressed.
func (r *Repository) SetScenarioKind(ctx context.Context, scenarioID int64, kind scenario.Kind, engine taurus.Executor) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE scenario SET kind = ?, engine = ? WHERE id = ?",
		string(kind), string(engine), scenarioID)
	if err != nil {
		return fmt.Errorf("mysql: set scenario kind: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mysql: set scenario kind rows: %w", err)
	}
	// MySQL reports RowsAffected 0 for a no-op UPDATE (values already equal)
	// exactly as for a missing row, so 0 alone cannot mean ErrNotFound:
	// pinning a scenario to the kind it already has must succeed, not 404.
	// Verify the row exists before declaring it gone. Deliberately not the
	// CLIENT_FOUND_ROWS DSN flag -- the DSN is operator-supplied verbatim,
	// and changing its flags would alter RowsAffected semantics for every
	// other statement on the pool.
	if n == 0 {
		var exists bool
		if err := r.db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM scenario WHERE id = ?)", scenarioID).Scan(&exists); err != nil {
			return fmt.Errorf("mysql: set scenario kind check: %w", err)
		}
		if !exists {
			return ports.ErrNotFound
		}
	}
	return nil
}

// SetScenarioRequests stores a portable scenario's declarative workload,
// overwriting whatever was stored before -- PUT semantics, no partial patch.
func (r *Repository) SetScenarioRequests(ctx context.Context, scenarioID int64, raw []byte) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO scenario_requests (scenario_id, raw) VALUES (?, ?)"+
			" ON DUPLICATE KEY UPDATE raw = VALUES(raw)",
		scenarioID, raw)
	if err != nil {
		return fmt.Errorf("mysql: set scenario requests: %w", err)
	}
	return nil
}

// GetScenarioRequests returns a portable scenario's stored fragment, or
// ports.ErrNotFound if nothing has been uploaded yet.
func (r *Repository) GetScenarioRequests(ctx context.Context, scenarioID int64) ([]byte, error) {
	var raw []byte
	err := r.db.QueryRowContext(ctx, "SELECT raw FROM scenario_requests WHERE scenario_id = ?", scenarioID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mysql: get scenario requests: %w", err)
	}
	return raw, nil
}

// ScenarioInUse reports whether the scenario is referenced by any execution's
// execution configuration.
func (r *Repository) ScenarioInUse(ctx context.Context, scenarioID int64) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM execution_scenario WHERE scenario_id = ?)", scenarioID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("mysql: scenario in use: %w", err)
	}
	return exists, nil
}

func scanScenario(s rowScanner) (scenario.Scenario, error) {
	var (
		p         scenario.Scenario
		kind      string
		engine    string
		tenantID  sql.NullInt64
		createdBy sql.NullString
		updatedBy sql.NullString
	)
	if err := s.Scan(&p.ID, &p.Name, &p.ProjectID, &kind, &engine,
		&tenantID, &createdBy, &updatedBy, &p.CreatedTime); err != nil {
		return scenario.Scenario{}, err
	}
	p.Kind = scenario.Kind(kind)
	p.Engine = taurus.Executor(engine)
	p.CreatedBy = createdBy.String
	p.UpdatedBy = updatedBy.String
	if tenantID.Valid {
		p.TenantID = &tenantID.Int64
	}
	return p, nil
}

func isDuplicateKey(err error) bool {
	var mysqlErr *driver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlDupEntry
}
