package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/heridotlife/honryu/internal/domain/threshold"
	"github.com/heridotlife/honryu/internal/ports"
)

var _ ports.ThresholdStore = (*Repository)(nil)

// thresholdColumns is the threshold projection, shared by every read so a
// column added to one query cannot be forgotten in another.
const thresholdColumns = `id, scenario_id, metric, comparison, value, created_time`

// thresholdResultColumns is the result projection, likewise shared. The
// snapshot columns (metric, comparison, value) travel with the row: a result
// is historical evidence, legible even after the definition is edited.
const thresholdResultColumns = `id, threshold_id, execution_id, run_id, metric, comparison, value, observed_value, satisfied, reason, evaluated_at`

// ListThresholdsForScenario returns the scenario's thresholds, oldest first;
// the unique key's leftmost column serves the filter.
func (r *Repository) ListThresholdsForScenario(ctx context.Context, scenarioID int64) ([]threshold.Threshold, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+thresholdColumns+" FROM scenario_thresholds WHERE scenario_id=? ORDER BY id", scenarioID)
	if err != nil {
		return nil, fmt.Errorf("mysql: list scenario thresholds: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []threshold.Threshold
	for rows.Next() {
		got, err := scanThreshold(rows)
		if err != nil {
			return nil, fmt.Errorf("mysql: scan threshold: %w", err)
		}
		out = append(out, got)
	}
	return out, rows.Err()
}

// ReplaceThresholds atomically swaps the scenario's threshold set: verbatim
// matches keep their row identity (so recorded results survive an idempotent
// re-save), gone ones are deleted (their results cascade), new ones are
// inserted. Returns the stored set in caller order.
func (r *Repository) ReplaceThresholds(ctx context.Context, scenarioID int64, defs []threshold.Threshold) ([]threshold.Threshold, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("mysql: replace scenario thresholds: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// The current set, id order, for the verbatim match.
	rows, err := tx.QueryContext(ctx,
		"SELECT "+thresholdColumns+" FROM scenario_thresholds WHERE scenario_id=? ORDER BY id FOR UPDATE", scenarioID)
	if err != nil {
		return nil, fmt.Errorf("mysql: replace scenario thresholds: %w", err)
	}
	var existing []threshold.Threshold
	for rows.Next() {
		got, scanErr := scanThreshold(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("mysql: scan threshold: %w", scanErr)
		}
		existing = append(existing, got)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("mysql: replace scenario thresholds: %w", err)
	}
	_ = rows.Close()

	// Pair each incoming definition with an existing row it matches
	// verbatim; unchanged rows are left exactly as they are.
	matched := make(map[int64]bool, len(existing))
	kept := make([]threshold.Threshold, 0, len(defs))
	for _, def := range defs {
		def.ScenarioID = scenarioID
		reused := false
		for _, ex := range existing {
			if matched[ex.ID] {
				continue
			}
			if ex.Metric == def.Metric && ex.Comparison == def.Comparison && ex.Value == def.Value {
				matched[ex.ID] = true
				kept = append(kept, ex)
				reused = true
				break
			}
		}
		if reused {
			continue
		}
		res, insErr := tx.ExecContext(ctx,
			"INSERT INTO scenario_thresholds (scenario_id, metric, comparison, value) VALUES (?,?,?,?)",
			scenarioID, string(def.Metric), string(def.Comparison), def.Value)
		if insErr != nil {
			return nil, fmt.Errorf("mysql: insert threshold: %w", insErr)
		}
		id, insErr := res.LastInsertId()
		if insErr != nil {
			return nil, fmt.Errorf("mysql: insert threshold: %w", insErr)
		}
		def.ID = id
		// created_time is the database's to assign; read it back so the
		// returned set is the stored one.
		stored, scanErr := scanThreshold(tx.QueryRowContext(ctx,
			"SELECT "+thresholdColumns+" FROM scenario_thresholds WHERE id=?", id))
		if scanErr != nil {
			return nil, fmt.Errorf("mysql: re-read threshold: %w", scanErr)
		}
		kept = append(kept, stored)
	}

	// Everything under this scenario that did not match is gone; the FK's
	// ON DELETE CASCADE takes its results with it.
	for _, ex := range existing {
		if matched[ex.ID] {
			continue
		}
		if _, delErr := tx.ExecContext(ctx, "DELETE FROM scenario_thresholds WHERE id=?", ex.ID); delErr != nil {
			return nil, fmt.Errorf("mysql: delete threshold: %w", delErr)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("mysql: replace scenario thresholds: %w", err)
	}
	out := make([]threshold.Threshold, len(kept))
	copy(out, kept)
	return out, nil
}

// SaveThresholdResults records one run's evaluation, replacing any previous
// results for the same runs. Upsert semantics: the (run_id, threshold_id)
// unique key makes a concurrent re-evaluation of the same run idempotent
// rather than a constraint failure.
func (r *Repository) SaveThresholdResults(ctx context.Context, results []threshold.Result) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mysql: save threshold results: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	byRun := make(map[int64][]threshold.Result)
	for _, res := range results {
		byRun[res.RunID] = append(byRun[res.RunID], res)
	}
	for runID, rows := range byRun {
		if _, delErr := tx.ExecContext(ctx, "DELETE FROM threshold_results WHERE run_id=?", runID); delErr != nil {
			return fmt.Errorf("mysql: save threshold results: %w", delErr)
		}
		for _, res := range rows {
			if _, insErr := tx.ExecContext(ctx,
				`INSERT INTO threshold_results
				 (execution_id, run_id, threshold_id, metric, comparison, value, observed_value, satisfied, reason, evaluated_at)
				 VALUES (?,?,?,?,?,?,?,?,?,COALESCE(?, CURRENT_TIMESTAMP))`,
				res.ExecutionID, res.RunID, res.ThresholdID,
				string(res.Metric), string(res.Comparison), res.Value,
				nullPtr(res.Observed), nullBoolPtr(res.Satisfied), nullString(res.Reason),
				timePtrOrNull(res.EvaluatedAt),
			); insErr != nil {
				return fmt.Errorf("mysql: save threshold result (run %d threshold %d): %w", res.RunID, res.ThresholdID, insErr)
			}
		}
	}
	return tx.Commit()
}

// ThresholdResultsForRun returns the run's stored results, insertion order
// preserved (the order evaluation produced them in). Always non-nil.
func (r *Repository) ThresholdResultsForRun(ctx context.Context, runID int64) ([]threshold.Result, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+thresholdResultColumns+" FROM threshold_results WHERE run_id=? ORDER BY id", runID)
	if err != nil {
		return nil, fmt.Errorf("mysql: threshold results for run: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]threshold.Result, 0)
	for rows.Next() {
		got, err := scanThresholdResult(rows)
		if err != nil {
			return nil, fmt.Errorf("mysql: scan threshold result: %w", err)
		}
		out = append(out, got)
	}
	return out, rows.Err()
}

func scanThreshold(s rowScanner) (threshold.Threshold, error) {
	var (
		th         threshold.Threshold
		metric     string
		comparison string
	)
	if err := s.Scan(&th.ID, &th.ScenarioID, &metric, &comparison, &th.Value, &th.CreatedTime); err != nil {
		return threshold.Threshold{}, err
	}
	th.Metric = threshold.Metric(metric)
	th.Comparison = threshold.Comparison(comparison)
	return th, nil
}

func scanThresholdResult(s rowScanner) (threshold.Result, error) {
	var (
		res        threshold.Result
		rowID      int64
		metric     string
		comparison string
		reason     sql.NullString
		observed   sql.NullFloat64
		satisfied  sql.NullBool
	)
	if err := s.Scan(&rowID, &res.ThresholdID, &res.ExecutionID, &res.RunID,
		&metric, &comparison, &res.Value,
		&observed, &satisfied, &reason, &res.EvaluatedAt); err != nil {
		return threshold.Result{}, err
	}
	res.Metric = threshold.Metric(metric)
	res.Comparison = threshold.Comparison(comparison)
	if observed.Valid {
		v := observed.Float64
		res.Observed = &v
	}
	if satisfied.Valid {
		v := satisfied.Bool
		res.Satisfied = &v
	}
	res.Reason = reason.String
	return res, nil
}

// nullBoolPtr converts an optional bool to a value database/sql binds as
// NULL when unset.
func nullBoolPtr(v *bool) any {
	if v == nil {
		return nil
	}
	return *v
}

// timePtrOrNull passes a zero stamp through as NULL so the column's DEFAULT
// CURRENT_TIMESTAMP applies; a caller-supplied stamp is stored as-is.
func timePtrOrNull(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
