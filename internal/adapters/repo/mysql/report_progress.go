package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/ports"
)

var _ ports.ReportProgress = (*Repository)(nil)

// Absorb merges a shard's new intervals into the run's working state.
//
// Everything happens in one transaction, and the shard's row is locked first.
// Several pods push at once, and two absorbing the same shard concurrently --
// the retry of a push whose response was lost, arriving beside the push that
// followed it -- would otherwise both read the same watermark and both count
// the overlap.
func (r *Repository) Absorb(ctx context.Context, b ports.ProgressBatch) error {
	if err := b.Validate(); err != nil {
		return err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	watermark, err := lockShard(ctx, tx, b)
	if err != nil {
		return err
	}

	// Accumulate only what is new, then merge that much into the run. Building a
	// small accumulator first means the domain decides what a measurement means
	// and this file only decides how to add it to what is stored. The same
	// fresh intervals also extend the run's permanent series (see
	// mergeSeriesSeconds), inside this transaction so a retry's re-sent
	// intervals -- already skipped by the watermark -- can never be counted
	// there twice.
	fresh := report.NewAccumulator()
	highest := watermark
	freshIntervals := make([]metrics.Interval, 0, len(b.Intervals))
	for _, iv := range b.Intervals {
		if iv.Seq <= watermark {
			continue // already absorbed; a retry re-sent it
		}
		fresh.Add(iv)
		freshIntervals = append(freshIntervals, iv)
		if iv.Seq > highest {
			highest = iv.Seq
		}
	}

	if len(freshIntervals) > 0 {
		if err := mergeProgress(ctx, tx, b.RunID, fresh.Snapshot()); err != nil {
			return err
		}
		if err := mergeSeriesSeconds(ctx, tx, b.RunID, freshIntervals); err != nil {
			return err
		}
	}
	// COALESCE keeps whatever was already stored when this batch carries no
	// code -- a shard's exit code arrives once, on its final batch, and must
	// not be overwritten by a later call that has none.
	if _, err := tx.ExecContext(ctx,
		`UPDATE report_progress_shard SET seq=?, finished=finished|?, exit_code=COALESCE(?, exit_code)
		 WHERE run_id=? AND scenario_id=? AND shard_index=?`,
		highest, boolToInt(b.Final), nullPtr(b.ExitCode), b.RunID, b.ScenarioID, b.ShardIndex); err != nil {
		return fmt.Errorf("mysql: advance shard progress: %w", err)
	}
	return tx.Commit()
}

// lockShard takes the shard's row for update and returns the sequence already
// absorbed from the batch's stream.
//
// A shard is identified by (run, scenario, shard index): shard index alone is
// a StatefulSet ordinal scoped to one scenario's own pods and repeats across
// every scenario an execution bundles into one run.
func lockShard(ctx context.Context, tx *sql.Tx, b ports.ProgressBatch) (int64, error) {
	// The insert makes the row exist so it can be locked; the select then takes
	// the lock whether this call created it or another did.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO report_progress_shard (run_id, scenario_id, shard_index, stream_id, seq)
		 VALUES (?,?,?,?,0) ON DUPLICATE KEY UPDATE run_id=run_id`,
		b.RunID, b.ScenarioID, b.ShardIndex, b.StreamID); err != nil {
		return 0, fmt.Errorf("mysql: open shard progress: %w", err)
	}

	var (
		streamID string
		seq      int64
	)
	if err := tx.QueryRowContext(ctx,
		`SELECT stream_id, seq FROM report_progress_shard
		 WHERE run_id=? AND scenario_id=? AND shard_index=? FOR UPDATE`,
		b.RunID, b.ScenarioID, b.ShardIndex).Scan(&streamID, &seq); err != nil {
		return 0, fmt.Errorf("mysql: lock shard progress: %w", err)
	}

	// A different stream is a restarted pod, whose sequences begin again at one.
	// Keeping the old watermark would discard the rest of its run.
	if streamID != b.StreamID {
		if _, err := tx.ExecContext(ctx,
			`UPDATE report_progress_shard SET stream_id=?, seq=0
			 WHERE run_id=? AND scenario_id=? AND shard_index=?`,
			b.StreamID, b.RunID, b.ScenarioID, b.ShardIndex); err != nil {
			return 0, fmt.Errorf("mysql: reset shard stream: %w", err)
		}
		return 0, nil
	}
	return seq, nil
}

// mergeProgress adds one batch's worth of accumulation to what is stored.
func mergeProgress(ctx context.Context, tx *sql.Tx, runID int64, s report.Snapshot) error {
	// Concurrency is a plain sum, so the database can do it without reading.
	for _, sec := range s.Seconds {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO report_progress_second (run_id, second, engine_concurrency, label_concurrency)
			 VALUES (?,?,?,?)
			 ON DUPLICATE KEY UPDATE
				engine_concurrency=engine_concurrency+VALUES(engine_concurrency),
				label_concurrency=label_concurrency+VALUES(label_concurrency)`,
			runID, sec.Second, sec.Engine, sec.Labels); err != nil {
			return fmt.Errorf("mysql: merge second %d: %w", sec.Second, err)
		}
	}

	// Buckets and exemplars need the old value to merge against, so these are
	// read under the shard lock already held.
	if err := mergeLabels(ctx, tx, runID, s.Labels); err != nil {
		return err
	}
	if err := mergeSignatures(ctx, tx, runID, s.Signatures); err != nil {
		return err
	}
	return nil
}

// storedLabel is one label's read-back state during a merge: the JSON
// columns that can only be merged in Go, read under the row lock.
type storedLabel struct {
	latency metrics.Histogram
	codes   map[string]int64
}

// mergeCodes adds a batch's response-code counts to what is stored. Both nil
// stays nil -- "no codes reported" is not "every code zero times", and a NULL
// column must not become {} for a label whose engine reports no codes.
func mergeCodes(stored, delta map[string]int64) map[string]int64 {
	if len(stored) == 0 && len(delta) == 0 {
		return nil
	}
	out := make(map[string]int64, len(stored)+len(delta))
	for code, n := range stored {
		out[code] = n
	}
	for code, n := range delta {
		out[code] += n
	}
	return out
}

// mergeLabels folds a batch's worth of measurements into every label's stored
// progress, in three round trips total no matter how many distinct labels the
// batch touches -- rather than three per label, which turned a batch touching
// 20-30 labels into 60-90 sequential round trips while the shard's row lock
// from Absorb was held.
//
// samples/failed are additive in SQL (`col=col+VALUES(col)`), which MySQL
// serializes on its own. latency is a JSON histogram that can only be merged
// in Go, so every row is locked first: two shards flushing the same label at
// nearly the same moment (routine, since shards flush independently) would
// otherwise both read the same pre-update histogram, each merge their own
// delta on top of it, and the second write would silently discard the first
// shard's buckets -- corrupting the run's latency percentiles with no error or
// signal that it happened. Locking every row for the read-merge-write
// serializes that instead of losing one.
//
// The lock order two concurrent calls take is the primary key order MySQL's
// own index range scan visits, not the order labels happen to appear in a
// batch -- so two shards locking an overlapping set of labels in one query
// each cannot deadlock on it the way locking them one at a time, in whatever
// order a batch happened to list them, could.
func mergeLabels(ctx context.Context, tx *sql.Tx, runID int64, labels []report.LabelProgress) error {
	if len(labels) == 0 {
		return nil
	}

	// The insert makes every row exist so it can be locked; the select then
	// takes the lock on all of them, whether this call created them or another
	// did.
	openArgs := make([]any, 0, len(labels)*2)
	openRows := make([]string, len(labels))
	inClause := make([]string, len(labels))
	inArgs := make([]any, 0, len(labels)+1)
	inArgs = append(inArgs, runID)
	for i, l := range labels {
		openRows[i] = "(?,?)"
		openArgs = append(openArgs, runID, l.Label)
		inClause[i] = "?"
		inArgs = append(inArgs, l.Label)
	}
	// #nosec G202 -- the concatenated span is strings.Join over openRows,
	// whose every element is the literal "(?,?)" assigned above. It is a
	// placeholder list sized to len(labels), which is the one thing a
	// prepared statement cannot parameterise. Every value still travels in
	// openArgs as a bound parameter; no caller-controlled text reaches the
	// SQL text. Same reasoning for each remaining G202 in this file.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO report_progress_label (run_id, label) VALUES `+strings.Join(openRows, ",")+`
		 ON DUPLICATE KEY UPDATE run_id=run_id`,
		openArgs...); err != nil {
		return fmt.Errorf("mysql: open label progress: %w", err)
	}

	// #nosec G202 -- inClause elements are the literal "?"; values bound via inArgs.
	rows, err := tx.QueryContext(ctx,
		`SELECT label, latency, codes FROM report_progress_label
		 WHERE run_id=? AND label IN (`+strings.Join(inClause, ",")+`) FOR UPDATE`,
		inArgs...)
	if err != nil {
		return fmt.Errorf("mysql: lock label progress: %w", err)
	}
	stored := make(map[string]storedLabel, len(labels))
	for rows.Next() {
		var (
			label      string
			latencyRaw []byte
			codesRaw   []byte
		)
		if err := rows.Scan(&label, &latencyRaw, &codesRaw); err != nil {
			_ = rows.Close()
			return fmt.Errorf("mysql: scan label progress: %w", err)
		}
		var sl storedLabel
		if err := decodeJSON(latencyRaw, &sl.latency); err != nil {
			_ = rows.Close()
			return fmt.Errorf("mysql: decode label latency: %w", err)
		}
		if err := decodeJSON(codesRaw, &sl.codes); err != nil {
			_ = rows.Close()
			return fmt.Errorf("mysql: decode label codes: %w", err)
		}
		stored[label] = sl
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	mergeRows := make([]string, len(labels))
	mergeArgs := make([]any, 0, len(labels)*6)
	for i, l := range labels {
		merged := metrics.Histogram{}
		merged.Merge(stored[l.Label].latency)
		merged.Merge(l.Latency)
		latency, err := json.Marshal(merged)
		if err != nil {
			return fmt.Errorf("mysql: encode label latency: %w", err)
		}
		mergedCodes := mergeCodes(stored[l.Label].codes, l.Codes)
		var codes any // nil map -> SQL NULL, exactly the pre-0054 row shape
		if mergedCodes != nil {
			codes, err = json.Marshal(mergedCodes)
			if err != nil {
				return fmt.Errorf("mysql: encode label codes: %w", err)
			}
		}
		mergeRows[i] = "(?,?,?,?,?,?)"
		mergeArgs = append(mergeArgs, runID, l.Label, l.Samples, l.Failed, latency, codes)
	}
	// #nosec G202 -- mergeRows elements are the literal "(?,?,?,?,?,?)"; values bound via mergeArgs.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO report_progress_label (run_id, label, samples, failed, latency, codes) VALUES `+strings.Join(mergeRows, ",")+`
		 ON DUPLICATE KEY UPDATE
			samples=samples+VALUES(samples), failed=failed+VALUES(failed), latency=VALUES(latency), codes=VALUES(codes)`,
		mergeArgs...); err != nil {
		return fmt.Errorf("mysql: merge labels: %w", err)
	}
	return nil
}

// mergeSignatures is mergeLabels' counterpart for a run's error signatures:
// count is additive in SQL, exemplars is a JSON array that can only be merged
// in Go, so every row is locked first for the same reason -- two shards
// reporting the same signature concurrently must not let one's exemplar
// wordings silently overwrite the other's. Batched the same way and for the
// same reason.
func mergeSignatures(ctx context.Context, tx *sql.Tx, runID int64, sigs []report.ErrorSignature) error {
	if len(sigs) == 0 {
		return nil
	}

	openArgs := make([]any, 0, len(sigs)*4)
	openRows := make([]string, len(sigs))
	inClause := make([]string, len(sigs))
	inArgs := make([]any, 0, len(sigs)*3+1)
	inArgs = append(inArgs, runID)
	for i, sig := range sigs {
		openRows[i] = "(?,?,?,?)"
		openArgs = append(openArgs, runID, sig.Label, sig.ResponseCode, string(sig.Side))
		inClause[i] = "(?,?,?)"
		inArgs = append(inArgs, sig.Label, sig.ResponseCode, string(sig.Side))
	}
	// #nosec G202 -- openRows elements are the literal "(?,?,?,?)"; values bound via openArgs.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO report_progress_signature (run_id, label, response_code, side) VALUES `+strings.Join(openRows, ",")+`
		 ON DUPLICATE KEY UPDATE run_id=run_id`,
		openArgs...); err != nil {
		return fmt.Errorf("mysql: open signature progress: %w", err)
	}

	// #nosec G202 -- inClause elements are the literal "(?,?,?)"; values bound via inArgs.
	rows, err := tx.QueryContext(ctx,
		`SELECT label, response_code, side, exemplars FROM report_progress_signature
		 WHERE run_id=? AND (label, response_code, side) IN (`+strings.Join(inClause, ",")+`) FOR UPDATE`,
		inArgs...)
	if err != nil {
		return fmt.Errorf("mysql: lock signature progress: %w", err)
	}
	stored := make(map[report.Signature][]string, len(sigs))
	for rows.Next() {
		var (
			key  report.Signature
			side string
			raw  []byte
		)
		if err := rows.Scan(&key.Label, &key.ResponseCode, &side, &raw); err != nil {
			_ = rows.Close()
			return fmt.Errorf("mysql: scan signature progress: %w", err)
		}
		key.Side = report.Side(side)
		var exemplars []string
		if err := decodeJSON(raw, &exemplars); err != nil {
			_ = rows.Close()
			return fmt.Errorf("mysql: decode exemplars: %w", err)
		}
		stored[key] = exemplars
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	mergeRows := make([]string, len(sigs))
	mergeArgs := make([]any, 0, len(sigs)*6)
	for i, sig := range sigs {
		// The domain owns how many wordings are kept and how long they may be;
		// re-merging through it keeps that bound in one place.
		merged := report.MergeExemplars(stored[sig.Signature], sig.Exemplars)
		exemplars, err := json.Marshal(merged)
		if err != nil {
			return fmt.Errorf("mysql: encode exemplars: %w", err)
		}
		mergeRows[i] = "(?,?,?,?,?,?)"
		mergeArgs = append(mergeArgs, runID, sig.Label, sig.ResponseCode, string(sig.Side), sig.Count, exemplars)
	}
	// #nosec G202 -- mergeRows elements are the literal "(?,?,?,?,?,?)"; values bound via mergeArgs.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO report_progress_signature (run_id, label, response_code, side, count, exemplars) VALUES `+strings.Join(mergeRows, ",")+`
		 ON DUPLICATE KEY UPDATE count=count+VALUES(count), exemplars=VALUES(exemplars)`,
		mergeArgs...); err != nil {
		return fmt.Errorf("mysql: merge signatures: %w", err)
	}
	return nil
}

// Snapshot returns what a run has accumulated, ready for report.Restore.
func (r *Repository) Snapshot(ctx context.Context, runID int64) (report.Snapshot, error) {
	var s report.Snapshot

	labels, err := r.db.QueryContext(ctx,
		`SELECT label, samples, failed, latency, codes FROM report_progress_label
		 WHERE run_id=? ORDER BY label`, runID)
	if err != nil {
		return s, err
	}
	if s.Labels, err = scanLabelProgress(labels); err != nil {
		return report.Snapshot{}, err
	}

	seconds, err := r.db.QueryContext(ctx,
		`SELECT second, engine_concurrency, label_concurrency FROM report_progress_second
		 WHERE run_id=? ORDER BY second`, runID)
	if err != nil {
		return report.Snapshot{}, err
	}
	if s.Seconds, err = scanSecondProgress(seconds); err != nil {
		return report.Snapshot{}, err
	}

	sigs, err := r.db.QueryContext(ctx,
		`SELECT label, response_code, side, count, exemplars FROM report_progress_signature
		 WHERE run_id=? ORDER BY side, response_code, label`, runID)
	if err != nil {
		return report.Snapshot{}, err
	}
	if s.Signatures, err = scanSignatureProgress(sigs); err != nil {
		return report.Snapshot{}, err
	}
	return s, nil
}

func scanLabelProgress(rows *sql.Rows) ([]report.LabelProgress, error) {
	defer func() { _ = rows.Close() }()
	var out []report.LabelProgress
	for rows.Next() {
		var (
			l          report.LabelProgress
			latencyRaw []byte
			codesRaw   []byte
		)
		if err := rows.Scan(&l.Label, &l.Samples, &l.Failed, &latencyRaw, &codesRaw); err != nil {
			return nil, err
		}
		if err := decodeJSON(latencyRaw, &l.Latency); err != nil {
			return nil, fmt.Errorf("mysql: decode label latency: %w", err)
		}
		if err := decodeJSON(codesRaw, &l.Codes); err != nil {
			return nil, fmt.Errorf("mysql: decode label codes: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func scanSecondProgress(rows *sql.Rows) ([]report.SecondProgress, error) {
	defer func() { _ = rows.Close() }()
	var out []report.SecondProgress
	for rows.Next() {
		var sec report.SecondProgress
		if err := rows.Scan(&sec.Second, &sec.Engine, &sec.Labels); err != nil {
			return nil, err
		}
		out = append(out, sec)
	}
	return out, rows.Err()
}

func scanSignatureProgress(rows *sql.Rows) ([]report.ErrorSignature, error) {
	defer func() { _ = rows.Close() }()
	var out []report.ErrorSignature
	for rows.Next() {
		var (
			sig  report.ErrorSignature
			side string
			raw  []byte
		)
		if err := rows.Scan(&sig.Label, &sig.ResponseCode, &side, &sig.Count, &raw); err != nil {
			return nil, err
		}
		sig.Side = report.Side(side)
		if err := decodeJSON(raw, &sig.Exemplars); err != nil {
			return nil, fmt.Errorf("mysql: decode exemplars: %w", err)
		}
		out = append(out, sig)
	}
	return out, rows.Err()
}

// ShardStates returns each shard seen so far and its completion state.
func (r *Repository) ShardStates(ctx context.Context, runID int64) ([]ports.ShardState, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT scenario_id, shard_index, finished, exit_code FROM report_progress_shard
		 WHERE run_id=? ORDER BY scenario_id, shard_index`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ports.ShardState
	for rows.Next() {
		var (
			st       ports.ShardState
			finished int
			code     sql.NullInt64
		)
		if err := rows.Scan(&st.ScenarioID, &st.ShardIndex, &finished, &code); err != nil {
			return nil, err
		}
		st.Finished = finished != 0
		if code.Valid {
			c := int(code.Int64)
			st.ExitCode = &c
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// Discard drops a run's working state once it has become a report.
func (r *Repository) Discard(ctx context.Context, runID int64) error {
	for _, table := range []string{
		"report_progress_label", "report_progress_second",
		"report_progress_signature", "report_progress_shard",
	} {
		// #nosec G202 -- table comes from the literal slice ranged over just
		// above, not from any caller. runID is bound.
		if _, err := r.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE run_id=?", runID); err != nil {
			return fmt.Errorf("mysql: discard %s: %w", table, err)
		}
	}
	return nil
}
