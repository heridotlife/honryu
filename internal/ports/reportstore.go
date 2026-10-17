package ports

import (
	"context"
	"time"

	"github.com/heridotlife/honryu/internal/domain/report"
)

// ReportStore persists what a run produced.
//
// Reports are kept because they are the evidence a readiness judgement was made
// on. They must outlive the engines that produced them, the metrics backend's
// retention, and the campaign they belonged to -- so this is durable storage,
// not a query against a metrics system that will have forgotten.
type ReportStore interface {
	// SaveReport stores a run's report. A run has one outcome: the first report
	// saved for a run is the one that survives, and saving again for the same
	// run is a no-op rather than a replacement.
	//
	// This is what makes it safe for a run's natural completion and a
	// concurrent Honryu-initiated Stop/Purge to race to finalise the same run:
	// both may compute a report and both call SaveReport, but only the first to
	// actually persist wins, and the caller that lost can still discard its
	// working state unconditionally rather than needing to know which one it
	// was. A plain replace-on-conflict would let whichever call happened to
	// commit last silently overwrite the other's verdict.
	SaveReport(ctx context.Context, r report.Report) error
	// GetReport returns a run's report, or ErrNotFound.
	GetReport(ctx context.Context, runID int64) (report.Report, error)
	// ListReports returns an execution's reports, most recent first, so a
	// service owner can see how it has behaved over time.
	//
	// A limit of zero or less means no limit. Note that this is the opposite of
	// SQL's LIMIT 0, which returns nothing -- an implementation must omit the
	// clause rather than pass the value through.
	ListReports(ctx context.Context, executionID int64, limit int) ([]report.Report, error)
	// ReportsSince returns reports across all executions started at or after
	// the given time, which is what trend analytics reads. The boundary is
	// inclusive; limit behaves as it does for ListReports.
	ReportsSince(ctx context.Context, since time.Time, limit int) ([]report.Report, error)
	// ErrorSignatureHistory aggregates every error signature across
	// executionID's runs, grouped by (label, response_code, side): the summed
	// count across all runs, and how many distinct runs each signature
	// appeared in. Ordered dominant-first (total count descending), matching
	// how a single run's own signatures are ordered.
	ErrorSignatureHistory(ctx context.Context, executionID int64) ([]report.SignatureHistoryRow, error)
	// SetStarred stars or unstars a stored run (phase 107): a starred run is
	// a RunCompare baseline candidate. Post-hoc annotation -- SaveReport
	// always stores a run unstarred, so this is the only writer of the flag.
	// Returns ErrNotFound for a run with no stored report.
	SetStarred(ctx context.Context, runID int64, starred bool) error
	// SetNote writes a run's free-text note (phase 107): why a run matters,
	// in place -- last write wins, no history. The handler layer bounds the
	// length (2000 runes); the store persists whatever it is given. Returns
	// ErrNotFound for a run with no stored report.
	SetNote(ctx context.Context, runID int64, note string) error
}
