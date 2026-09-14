package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/ports"
)

var _ ports.CalibrationJobRepository = (*Repository)(nil)

const calibrationJobColumns = "id, execution_id, scenario_id, phase, step_count, bracket_lo_requested, bracket_lo_achieved," +
	" bracket_hi_requested, next_requested_qps, saturated_by, per_pod_qps, failure_reason, created_time"

// CreateCalibrationJob persists a fresh job (PhasePending) for executionID,
// recording scenarioID as the scenario the search calibrates, and returns
// its assigned ID. A 0 scenarioID stores NULL -- the column's unknown/legacy
// marker (0064), never a real scenario id.
func (r *Repository) CreateCalibrationJob(ctx context.Context, executionID, scenarioID int64) (int64, error) {
	var storedScenarioID any
	if scenarioID != 0 {
		storedScenarioID = scenarioID
	}
	res, err := r.db.ExecContext(ctx,
		"INSERT INTO calibration_job (execution_id, scenario_id, phase) VALUES (?, ?, ?)",
		executionID, storedScenarioID, string(calibration.PhasePending))
	if err != nil {
		return 0, fmt.Errorf("mysql: create calibration job: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("mysql: create calibration job last id: %w", err)
	}
	return id, nil
}

// GetCalibrationJob returns the job with id, or ports.ErrNotFound.
func (r *Repository) GetCalibrationJob(ctx context.Context, id int64) (ports.CalibrationJob, error) {
	row := r.db.QueryRowContext(ctx, "SELECT "+calibrationJobColumns+" FROM calibration_job WHERE id = ?", id)
	j, err := scanCalibrationJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.CalibrationJob{}, ports.ErrNotFound
	}
	if err != nil {
		return ports.CalibrationJob{}, fmt.Errorf("mysql: get calibration job: %w", err)
	}
	return j, nil
}

// ListCalibrationJobsByExecution returns every job ever run for
// executionID, most recent first.
func (r *Repository) ListCalibrationJobsByExecution(ctx context.Context, executionID int64) ([]ports.CalibrationJob, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+calibrationJobColumns+" FROM calibration_job WHERE execution_id = ? ORDER BY id DESC", executionID)
	if err != nil {
		return nil, fmt.Errorf("mysql: list calibration jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []ports.CalibrationJob{}
	for rows.Next() {
		j, scanErr := scanCalibrationJob(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("mysql: scan calibration job: %w", scanErr)
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate calibration jobs: %w", err)
	}
	return out, nil
}

// ClaimNextStep locks and returns one non-terminal job whose claim has
// expired, or found=false if none is due. See ports.CalibrationJobRepository
// for why the claim is a lease (claimed_at), not the row lock alone: a
// step's real-world run takes minutes, far longer than this transaction.
func (r *Repository) ClaimNextStep(ctx context.Context, now time.Time, leaseFor time.Duration) (ports.CalibrationJob, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ports.CalibrationJob{}, false, fmt.Errorf("mysql: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	staleBefore := now.Add(-leaseFor)
	row := tx.QueryRowContext(ctx,
		"SELECT "+calibrationJobColumns+" FROM calibration_job"+
			" WHERE phase IN (?, ?, ?) AND (claimed_at IS NULL OR claimed_at < ?)"+
			" ORDER BY id LIMIT 1 FOR UPDATE",
		string(calibration.PhasePending), string(calibration.PhaseBracketing), string(calibration.PhaseBisecting), staleBefore)
	job, err := scanCalibrationJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.CalibrationJob{}, false, nil
	}
	if err != nil {
		return ports.CalibrationJob{}, false, fmt.Errorf("mysql: claim next calibration step: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE calibration_job SET claimed_at = ? WHERE id = ?", now, job.ID); err != nil {
		return ports.CalibrationJob{}, false, fmt.Errorf("mysql: mark calibration job claimed: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ports.CalibrationJob{}, false, fmt.Errorf("mysql: commit claim: %w", err)
	}
	return job, true, nil
}

// RecordStep appends step to jobID's history and replaces its persisted
// state with updated, clearing the claim.
func (r *Repository) RecordStep(ctx context.Context, jobID int64, step calibration.Step, updated ports.CalibrationJob) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mysql: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var seq int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM calibration_job_step WHERE job_id = ?", jobID).Scan(&seq); err != nil {
		return fmt.Errorf("mysql: count calibration job steps: %w", err)
	}
	seq++
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO calibration_job_step (job_id, seq, requested_qps, achieved_qps, classification) VALUES (?, ?, ?, ?, ?)",
		jobID, seq, step.RequestedQPS, step.AchievedQPS, string(step.Classification),
	); err != nil {
		return fmt.Errorf("mysql: insert calibration job step: %w", err)
	}

	var saturatedBy, perPodQPS any
	if updated.Result != nil {
		saturatedBy = string(updated.Result.SaturatedBy)
		perPodQPS = updated.Result.PerPodQPS
	}
	res, err := tx.ExecContext(ctx,
		"UPDATE calibration_job SET phase = ?, step_count = ?, bracket_lo_requested = ?, bracket_lo_achieved = ?,"+
			" bracket_hi_requested = ?, next_requested_qps = ?, saturated_by = ?, per_pod_qps = ?, claimed_at = NULL"+
			" WHERE id = ?",
		string(updated.Phase), updated.StepCount, updated.BracketLoRequested, updated.BracketLoAchieved,
		updated.BracketHiRequested, updated.NextRequestedQPS, saturatedBy, perPodQPS, jobID,
	)
	if err != nil {
		return fmt.Errorf("mysql: update calibration job: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mysql: record step rows affected: %w", err)
	}
	if n == 0 {
		return ports.ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mysql: commit record step: %w", err)
	}
	return nil
}

// StepsFor returns every step recorded for jobID, in the order taken.
func (r *Repository) StepsFor(ctx context.Context, jobID int64) ([]calibration.Step, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT requested_qps, achieved_qps, classification FROM calibration_job_step WHERE job_id = ? ORDER BY seq", jobID)
	if err != nil {
		return nil, fmt.Errorf("mysql: calibration job steps: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []calibration.Step{}
	for rows.Next() {
		var (
			step           calibration.Step
			classification string
		)
		if err := rows.Scan(&step.RequestedQPS, &step.AchievedQPS, &classification); err != nil {
			return nil, fmt.Errorf("mysql: scan calibration job step: %w", err)
		}
		step.Classification = calibration.Classification(classification)
		out = append(out, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate calibration job steps: %w", err)
	}
	return out, nil
}

// MarkFailed ends jobID with PhaseFailed and reason, clearing the claim.
func (r *Repository) MarkFailed(ctx context.Context, jobID int64, reason string) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE calibration_job SET phase = ?, failure_reason = ?, claimed_at = NULL WHERE id = ?",
		string(calibration.PhaseFailed), reason, jobID)
	if err != nil {
		return fmt.Errorf("mysql: mark calibration job failed: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mysql: mark failed rows affected: %w", err)
	}
	if n == 0 {
		return ports.ErrNotFound
	}
	return nil
}

func scanCalibrationJob(s rowScanner) (ports.CalibrationJob, error) {
	var (
		j             ports.CalibrationJob
		phase         string
		saturatedBy   sql.NullString
		perPodQPS     sql.NullFloat64
		failureReason sql.NullString
		scenarioID    sql.NullInt64
	)
	if err := s.Scan(&j.ID, &j.ExecutionID, &scenarioID, &phase, &j.StepCount,
		&j.BracketLoRequested, &j.BracketLoAchieved, &j.BracketHiRequested, &j.NextRequestedQPS,
		&saturatedBy, &perPodQPS, &failureReason, &j.CreatedTime); err != nil {
		return ports.CalibrationJob{}, err
	}
	j.Phase = calibration.Phase(phase)
	// NULL reads back as 0, CalibrationJob.ScenarioID's unknown/legacy
	// marker.
	j.ScenarioID = scenarioID.Int64
	if saturatedBy.Valid {
		j.Result = &calibration.Result{SaturatedBy: calibration.SaturatedBy(saturatedBy.String), PerPodQPS: perPodQPS.Float64}
	}
	j.FailureReason = failureReason.String
	return j, nil
}

// SetCalibrationBounds replaces whatever search bounds are recorded for
// executionID.
func (r *Repository) SetCalibrationBounds(ctx context.Context, executionID int64, bounds ports.CalibrationBounds) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO calibration_spec (execution_id, seed_qps, max_qps, max_steps, hold_seconds) VALUES (?, ?, ?, ?, ?)"+
			" ON DUPLICATE KEY UPDATE seed_qps = VALUES(seed_qps), max_qps = VALUES(max_qps),"+
			" max_steps = VALUES(max_steps), hold_seconds = VALUES(hold_seconds)",
		executionID, bounds.SeedQPS, bounds.MaxQPS, bounds.MaxSteps, bounds.HoldSeconds)
	if err != nil {
		return fmt.Errorf("mysql: set calibration bounds: %w", err)
	}
	return nil
}

// CalibrationBoundsFor returns the search bounds recorded for executionID,
// or ports.ErrNotFound if none have been configured.
func (r *Repository) CalibrationBoundsFor(ctx context.Context, executionID int64) (ports.CalibrationBounds, error) {
	var b ports.CalibrationBounds
	err := r.db.QueryRowContext(ctx,
		"SELECT seed_qps, max_qps, max_steps, hold_seconds FROM calibration_spec WHERE execution_id = ?", executionID,
	).Scan(&b.SeedQPS, &b.MaxQPS, &b.MaxSteps, &b.HoldSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.CalibrationBounds{}, ports.ErrNotFound
	}
	if err != nil {
		return ports.CalibrationBounds{}, fmt.Errorf("mysql: calibration bounds for execution %d: %w", executionID, err)
	}
	return b, nil
}
