package fake

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/ports"
)

// ReportStore is an in-memory ports.ReportStore for fast use-case tests.
type ReportStore struct {
	mu      sync.Mutex
	reports map[int64]report.Report // runID -> report

	// SaveErr, when set, is returned by SaveReport.
	SaveErr error
	// GetErr, when set, is returned by GetReport instead of its usual result --
	// a transient store failure, distinct from ErrNotFound.
	GetErr error
}

// NewReportStore builds an empty store.
func NewReportStore() *ReportStore {
	return &ReportStore{reports: map[int64]report.Report{}}
}

var _ ports.ReportStore = (*ReportStore)(nil)

// SaveReport stores a report. The first report saved for a run is the one
// that survives -- saving again for the same run is a no-op, not a
// replacement, so two concurrent finalisations racing for the same run cannot
// let the second overwrite the first's verdict.
//
// The annotations (phase 107) are zeroed on save, mirroring the mysql
// adapter: starring and notes are post-hoc, SetStarred/SetNote are their
// only writers.
func (s *ReportStore) SaveReport(_ context.Context, r report.Report) error {
	if s.SaveErr != nil {
		return s.SaveErr
	}
	if err := r.Validate(); err != nil {
		return err
	}
	r.Starred = false
	r.Note = ""
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.reports[r.RunID]; exists {
		return nil
	}
	s.reports[r.RunID] = r
	return nil
}

// GetReport returns a run's report.
func (s *ReportStore) GetReport(_ context.Context, runID int64) (report.Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.GetErr != nil {
		return report.Report{}, s.GetErr
	}
	r, ok := s.reports[runID]
	if !ok {
		return report.Report{}, ports.ErrNotFound
	}
	return r, nil
}

// ListReports returns an execution's reports, most recent first.
func (s *ReportStore) ListReports(_ context.Context, executionID int64, limit int) ([]report.Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []report.Report
	for _, r := range s.reports {
		if r.ExecutionID == executionID {
			out = append(out, r)
		}
	}
	return trim(out, limit), nil
}

// ReportsSince returns reports started at or after the given time.
func (s *ReportStore) ReportsSince(_ context.Context, since time.Time, limit int) ([]report.Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []report.Report
	for _, r := range s.reports {
		if !r.StartedAt.Before(since) {
			out = append(out, r)
		}
	}
	return trim(out, limit), nil
}

// SetStarred stars or unstars a stored run; unknown run -> ErrNotFound.
func (s *ReportStore) SetStarred(_ context.Context, runID int64, starred bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.reports[runID]
	if !ok {
		return ports.ErrNotFound
	}
	r.Starred = starred
	s.reports[runID] = r
	return nil
}

// SetNote writes a run's note in place (last write wins); unknown run ->
// ErrNotFound.
func (s *ReportStore) SetNote(_ context.Context, runID int64, note string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.reports[runID]
	if !ok {
		return ports.ErrNotFound
	}
	r.Note = note
	s.reports[runID] = r
	return nil
}

// ErrorSignatureHistory aggregates every error signature across
// executionID's runs, grouped by (label, response_code, side).
func (s *ReportStore) ErrorSignatureHistory(_ context.Context, executionID int64) ([]report.SignatureHistoryRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	type key struct {
		label, code string
		side        report.Side
	}
	totals := map[key]int64{}
	runs := map[key]map[int64]struct{}{}
	for _, r := range s.reports {
		if r.ExecutionID != executionID {
			continue
		}
		for _, e := range r.Errors {
			k := key{label: e.Label, code: e.ResponseCode, side: e.Side}
			totals[k] += e.Count
			if runs[k] == nil {
				runs[k] = map[int64]struct{}{}
			}
			runs[k][r.RunID] = struct{}{}
		}
	}

	out := make([]report.SignatureHistoryRow, 0, len(totals))
	for k, total := range totals {
		out = append(out, report.SignatureHistoryRow{
			Signature:  report.Signature{Label: k.label, ResponseCode: k.code, Side: k.side},
			TotalCount: total,
			RunCount:   len(runs[k]),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalCount != out[j].TotalCount {
			return out[i].TotalCount > out[j].TotalCount
		}
		if out[i].Side != out[j].Side {
			return out[i].Side < out[j].Side
		}
		if out[i].ResponseCode != out[j].ResponseCode {
			return out[i].ResponseCode < out[j].ResponseCode
		}
		return out[i].Label < out[j].Label
	})
	return out, nil
}

func trim(in []report.Report, limit int) []report.Report {
	sort.Slice(in, func(i, j int) bool {
		if in[i].StartedAt.Equal(in[j].StartedAt) {
			return in[i].RunID > in[j].RunID
		}
		return in[i].StartedAt.After(in[j].StartedAt)
	})
	if limit > 0 && len(in) > limit {
		in = in[:limit]
	}
	return in
}
