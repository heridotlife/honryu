package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

var _ ports.ReportStore = (*Repository)(nil)

// reportColumns is the summary projection, shared by every read so a column
// added to one query cannot be forgotten in another.
const reportColumns = `run_id, execution_id, scenario_id, engine, cluster, correlation_id, outcome, started_at, ended_at,
	requested_concurrency, requested_throughput, requested_duration_seconds, requested_steps,
	achieved_concurrency, achieved_throughput, achieved_duration_seconds,
	achieved_samples, achieved_failed, error_rate,
	attribution_target, attribution_engine, attribution_unknown, latency, labels, cluster_results`

// SaveReport stores a run's report. The first report saved for a run is the
// one that survives -- saving again for the same run is a no-op, not a
// replacement.
//
// This is what makes a run's own natural completion and a concurrent
// Honryu-initiated Stop/Purge race safely: both compute a report and both call
// SaveReport, but the run_id primary key lets only the first insert succeed;
// the second hits a duplicate key, which is treated as success rather than an
// error, and the report already stored -- whichever finalisation actually
// finished first -- is left alone. A plain upsert would let whichever call
// commits last silently overwrite the other's verdict.
//
// The summary and its error signatures are written in one transaction. A report
// whose signatures were half-written would attribute a run's failures to a
// mix of two attempts, which is worse than having no report at all: it looks
// complete.
func (r *Repository) SaveReport(ctx context.Context, rep report.Report) error {
	if err := rep.Validate(); err != nil {
		return err
	}
	latency, err := json.Marshal(rep.Latency)
	if err != nil {
		return fmt.Errorf("mysql: encode latency: %w", err)
	}
	labels, err := json.Marshal(rep.Labels)
	if err != nil {
		return fmt.Errorf("mysql: encode labels: %w", err)
	}
	// nil ClusterResults (every single-cluster report) stays NULL, the
	// additive-column convention 0072 documents.
	var clusterResults any
	if rep.ClusterResults != nil {
		if clusterResults, err = json.Marshal(rep.ClusterResults); err != nil {
			return fmt.Errorf("mysql: encode cluster results: %w", err)
		}
	}
	// A stepped request's plateau table rides as JSON the same way (0076):
	// NULL for every flat request, the step rows for a staircase.
	var requestedSteps any
	if rep.Requested.Steps != nil {
		if requestedSteps, err = json.Marshal(rep.Requested.Steps); err != nil {
			return fmt.Errorf("mysql: encode requested steps: %w", err)
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	if _, err := tx.ExecContext(ctx, `INSERT INTO execution_report (`+reportColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		rep.RunID, rep.ExecutionID, rep.ScenarioID, string(rep.Engine), rep.Cluster, rep.CorrelationID, string(rep.Outcome),
		rep.StartedAt.UTC(), rep.EndedAt.UTC(),
		rep.Requested.Concurrency, rep.Requested.Throughput, rep.Requested.DurationSeconds, requestedSteps,
		rep.Achieved.Concurrency, rep.Achieved.Throughput, rep.Achieved.DurationSeconds,
		rep.Achieved.Samples, rep.Achieved.Failed, rep.ErrorRate,
		rep.Attribution.Target, rep.Attribution.Engine, rep.Attribution.Unknown,
		latency, labels, clusterResults,
	); err != nil {
		if isDuplicateKey(err) {
			// Someone else's finalisation already won for this run; nothing to
			// write, and returning success is what lets a caller finalising
			// concurrently discard its working state unconditionally afterward.
			return nil
		}
		return fmt.Errorf("mysql: save report: %w", err)
	}

	for _, e := range rep.Errors {
		exemplars, err := json.Marshal(e.Exemplars)
		if err != nil {
			return fmt.Errorf("mysql: encode exemplars: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO report_error_signature
			(run_id, label, response_code, side, count, share, exemplars)
			VALUES (?,?,?,?,?,?,?)`,
			rep.RunID, e.Label, e.ResponseCode, string(e.Side),
			e.Count, share(e.Count, rep.Achieved.Samples), exemplars,
		); err != nil {
			return fmt.Errorf("mysql: save error signature: %w", err)
		}
	}
	return tx.Commit()
}

// GetReport returns a run's report, or ports.ErrNotFound.
func (r *Repository) GetReport(ctx context.Context, runID int64) (report.Report, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+reportColumns+" FROM execution_report WHERE run_id=?", runID)
	rep, err := scanReport(row)
	if errors.Is(err, sql.ErrNoRows) {
		return report.Report{}, ports.ErrNotFound
	}
	if err != nil {
		return report.Report{}, err
	}
	if rep.Errors, err = r.errorSignatures(ctx, runID); err != nil {
		return report.Report{}, err
	}
	return rep, nil
}

// ListReports returns an execution's reports, most recent first.
func (r *Repository) ListReports(ctx context.Context, executionID int64, limit int) ([]report.Report, error) {
	return r.listReports(ctx,
		"SELECT "+reportColumns+` FROM execution_report WHERE execution_id=?
		 ORDER BY started_at DESC, run_id DESC`+limitClause(limit), executionID)
}

// ReportsSince returns reports across executions started at or after a time.
func (r *Repository) ReportsSince(ctx context.Context, since time.Time, limit int) ([]report.Report, error) {
	return r.listReports(ctx,
		"SELECT "+reportColumns+` FROM execution_report WHERE started_at>=?
		 ORDER BY started_at DESC, run_id DESC`+limitClause(limit), since.UTC())
}

// ErrorSignatureHistory aggregates every error signature across
// executionID's runs, joining report_error_signature back to execution_report
// on run_id to scope by execution (the signature table itself carries no
// execution id). Dominant-first, matching a single run's own ordering.
func (r *Repository) ErrorSignatureHistory(ctx context.Context, executionID int64) ([]report.SignatureHistoryRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT es.label, es.response_code, es.side, SUM(es.count), COUNT(DISTINCT es.run_id)
		FROM report_error_signature es
		JOIN execution_report er ON er.run_id = es.run_id
		WHERE er.execution_id = ?
		GROUP BY es.label, es.response_code, es.side
		ORDER BY SUM(es.count) DESC, es.side, es.response_code, es.label`, executionID)
	if err != nil {
		return nil, fmt.Errorf("mysql: error signature history: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []report.SignatureHistoryRow{}
	for rows.Next() {
		var (
			row  report.SignatureHistoryRow
			side string
		)
		if err := rows.Scan(&row.Label, &row.ResponseCode, &side, &row.TotalCount, &row.RunCount); err != nil {
			return nil, fmt.Errorf("mysql: scan error signature history: %w", err)
		}
		row.Side = report.Side(side)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate error signature history: %w", err)
	}
	return out, nil
}

func (r *Repository) listReports(ctx context.Context, query string, args ...any) ([]report.Report, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []report.Report
	for rows.Next() {
		rep, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rep)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Signatures are fetched per report after the rows are closed: MySQL allows
	// only one active result set per connection, so querying inside the loop
	// would deadlock against the pool under load.
	for i := range out {
		if out[i].Errors, err = r.errorSignatures(ctx, out[i].RunID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// errorSignatures reads a run's failure modes, dominant first, matching the
// order report.Build produces so a stored report reads like a fresh one.
func (r *Repository) errorSignatures(ctx context.Context, runID int64) ([]report.ErrorSignature, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT label, response_code, side, count, exemplars
		FROM report_error_signature WHERE run_id=?
		ORDER BY count DESC, side, response_code, label`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []report.ErrorSignature
	for rows.Next() {
		var (
			e         report.ErrorSignature
			side      string
			exemplars []byte
		)
		if err := rows.Scan(&e.Label, &e.ResponseCode, &side, &e.Count, &exemplars); err != nil {
			return nil, err
		}
		e.Side = report.Side(side)
		if err := decodeJSON(exemplars, &e.Exemplars); err != nil {
			return nil, fmt.Errorf("mysql: decode exemplars: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanReport(s rowScanner) (report.Report, error) {
	var (
		rep             report.Report
		engine, outcome string
		latency, labels []byte
		clusterResults  []byte // JSON array or NULL
		requestedSteps  []byte // JSON step table or NULL (0076)
	)
	if err := s.Scan(
		&rep.RunID, &rep.ExecutionID, &rep.ScenarioID, &engine, &rep.Cluster, &rep.CorrelationID, &outcome,
		&rep.StartedAt, &rep.EndedAt,
		&rep.Requested.Concurrency, &rep.Requested.Throughput, &rep.Requested.DurationSeconds, &requestedSteps,
		&rep.Achieved.Concurrency, &rep.Achieved.Throughput, &rep.Achieved.DurationSeconds,
		&rep.Achieved.Samples, &rep.Achieved.Failed, &rep.ErrorRate,
		&rep.Attribution.Target, &rep.Attribution.Engine, &rep.Attribution.Unknown,
		&latency, &labels, &clusterResults,
	); err != nil {
		return report.Report{}, err
	}
	rep.Engine = taurus.Executor(engine)
	rep.Outcome = taurus.Outcome(outcome)
	if err := decodeJSON(latency, &rep.Latency); err != nil {
		return report.Report{}, fmt.Errorf("mysql: decode latency: %w", err)
	}
	if err := decodeJSON(labels, &rep.Labels); err != nil {
		return report.Report{}, fmt.Errorf("mysql: decode labels: %w", err)
	}
	if err := decodeJSON(clusterResults, &rep.ClusterResults); err != nil {
		return report.Report{}, fmt.Errorf("mysql: decode cluster results: %w", err)
	}
	if err := decodeJSON(requestedSteps, &rep.Requested.Steps); err != nil {
		return report.Report{}, fmt.Errorf("mysql: decode requested steps: %w", err)
	}
	return rep, nil
}

// decodeJSON tolerates a NULL column, which is what an older row or an empty
// value looks like coming back.
func decodeJSON(raw []byte, into any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, into)
}

// share is a signature's portion of the run's requests, stored so trend queries
// can compare runs of different sizes without joining back to the summary.
func share(count int64, samples int64) float64 {
	if samples <= 0 {
		return 0
	}
	return float64(count) / float64(samples)
}

// limitClause appends a row limit only when one was asked for. A limit of zero
// means "no limit" on the port, and SQL reads LIMIT 0 as "no rows" -- the two
// are opposites, so the clause is omitted rather than passed through.
func limitClause(limit int) string {
	if limit <= 0 {
		return ""
	}
	return fmt.Sprintf(" LIMIT %d", limit)
}
