// Summary: the per-project dashboard read (phase 66). One call composing
// the project's existing surfaces -- its scenarios, its executions, and the
// reports those executions' runs produced -- into the numbers a dashboard
// renders, so the SPA answers "how is this project doing" with one fetch
// instead of fanning out over the executions and reports lists.
//
// The composition rule is digestapp's (phase 42/60): the project's reports
// are reached through ListExecutionsByProject and per-execution ListReports,
// never through new SQL -- there is no project column on execution_report to
// filter by, and duplicating the store's queries would be a second accounting
// of what a run did. Each run's regression verdict comes from the same
// report.BuildTrend the trend endpoint serves, so a dashboard's regressed
// count can never disagree with the trend page's chips.
package projectapp

import (
	"context"
	"sort"
	"time"

	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

// Series bounds, the digest feed's convention: absent means "a page", not
// "everything", and an absurd ask is capped -- a dashboard chart wants the
// recent shape, not the project's whole run history.
const (
	// DefaultSummarySeries is the series length ?limit= omitted buys.
	DefaultSummarySeries = 20
	// MaxSummarySeries caps one summary's series no matter what was asked.
	MaxSummarySeries = 100
)

// ThroughputPoint is one run's contribution to the summary's throughput
// series: the achieved and requested rates the trend endpoint carries for
// the same run, plus its regression verdict. Series order is oldest to
// newest -- chart display order (the SPA sparkline's leftmost point is the
// oldest), the reverse of ListReports' most-recent-first.
type ThroughputPoint struct {
	RunID               int64
	StartedAt           time.Time
	AchievedThroughput  float64
	RequestedThroughput float64
	// Regressed mirrors the trend point's verdict: this run missed its
	// target QPS while its nearest comparable predecessor hit it.
	Regressed bool
}

// LastRun is the project's most recent completed run, as a dashboard's
// status chip reads it.
type LastRun struct {
	RunID     int64
	Outcome   taurus.Outcome
	StartedAt time.Time
}

// Summary is one project's dashboard payload. Counts are exact reads of the
// caller's own data; the run-derived fields are empty/nil when the project
// has no reports (or the service was wired without a report store).
type Summary struct {
	ProjectID int64
	// TotalExecutions is every execution in the project, whatever its runs.
	TotalExecutions int
	// LastExecutionTime is the newest execution's creation time; zero when
	// the project has none.
	LastExecutionTime time.Time
	// ScenarioCount counts the project's runnable (non-template) scenarios.
	ScenarioCount int
	// TemplateCount is the template catalog's size (every scenario flagged
	// is_template, phase 65). Templates are deliberately projectless --
	// portable starting points -- so this is the catalog an operator can
	// instantiate from, not a per-project subset.
	TemplateCount int
	// RegressedCount is the project's runs whose trend point carries the
	// regressed verdict: they missed their target QPS while a comparable
	// predecessor hit it. The number a danger-tinted KPI exists for.
	RegressedCount int
	// LastRun is the most recent run across the project's executions, or
	// nil when none of them has a report yet.
	LastRun *LastRun
	// ThroughputSeries is the project's most recent runs' achieved
	// throughput, oldest to newest, at most the requested series length.
	// Always non-nil, so a chart renders empty rather than noughts.
	ThroughputSeries []ThroughputPoint
}

// WithReports wires the report store the summary read needs. Returns the
// receiver for chaining, digestapp.WithDeliverer-style: an optional
// dependency stays out of NewService's signature, so every existing
// construction site keeps compiling. Without it, Summary serves the
// count-only shape -- the execution and scenario numbers are still exact,
// and the run-derived fields say "none", which is the honest reading of a
// deployment with no report store.
func (s *Service) WithReports(rs ports.ReportStore) *Service {
	if rs != nil {
		s.reports = rs
	}
	return s
}

// Summary composes the project's dashboard payload. seriesLimit caps the
// throughput series (DefaultSummarySeries when non-positive,
// MaxSummarySeries as the ceiling). The unknown-project error is the repo's
// own (ports.ErrNotFound), so the handler's 404 needs no translation.
func (s *Service) Summary(ctx context.Context, projectID int64, seriesLimit int) (Summary, error) {
	if _, err := s.repo.GetProject(ctx, projectID); err != nil {
		return Summary{}, err
	}
	out := Summary{ProjectID: projectID, ThroughputSeries: []ThroughputPoint{}}

	scenarios, err := s.repo.ListScenariosByProject(ctx, projectID)
	if err != nil {
		return Summary{}, err
	}
	for _, sc := range scenarios {
		if !sc.IsTemplate {
			out.ScenarioCount++
		}
	}
	templates, err := s.repo.ListTemplates(ctx)
	if err != nil {
		return Summary{}, err
	}
	out.TemplateCount = len(templates)

	execs, err := s.repo.ListExecutionsByProject(ctx, projectID)
	if err != nil {
		return Summary{}, err
	}
	out.TotalExecutions = len(execs)
	for _, exe := range execs {
		if exe.CreatedTime.After(out.LastExecutionTime) {
			out.LastExecutionTime = exe.CreatedTime
		}
	}

	if s.reports == nil {
		return out, nil // documented degradation: counts only
	}

	// Each run's verdict comes from the same BuildTrend the trend endpoint
	// serves -- per execution, since a trend is an execution's history. The
	// fake and mysql ListReports both return most-recent first, which is the
	// order BuildTrend requires; the explicit sort keeps that contract
	// local instead of trusting every implementer forever.
	regressed := make(map[int64]bool)
	all := make([]report.Report, 0, 4*len(execs))
	for _, exe := range execs {
		reps, err := s.reports.ListReports(ctx, exe.ID, 0)
		if err != nil {
			return Summary{}, err
		}
		sort.Slice(reps, func(i, j int) bool {
			if reps[i].StartedAt.Equal(reps[j].StartedAt) {
				return reps[i].RunID > reps[j].RunID
			}
			return reps[i].StartedAt.After(reps[j].StartedAt)
		})
		for _, p := range report.BuildTrend(reps).Points {
			if p.Regressed {
				regressed[p.RunID] = true
				out.RegressedCount++
			}
		}
		all = append(all, reps...)
	}
	if len(all) == 0 {
		return out, nil
	}

	// One project-wide chronological order (oldest first), the tiebreak
	// matching trim's: a later run of the same second is the newer one.
	sort.Slice(all, func(i, j int) bool {
		if all[i].StartedAt.Equal(all[j].StartedAt) {
			return all[i].RunID < all[j].RunID
		}
		return all[i].StartedAt.Before(all[j].StartedAt)
	})
	newest := all[len(all)-1]
	out.LastRun = &LastRun{RunID: newest.RunID, Outcome: newest.Outcome, StartedAt: newest.StartedAt}

	if seriesLimit <= 0 {
		seriesLimit = DefaultSummarySeries
	}
	if seriesLimit > MaxSummarySeries {
		seriesLimit = MaxSummarySeries
	}
	start := 0
	if len(all) > seriesLimit {
		start = len(all) - seriesLimit
	}
	for _, rep := range all[start:] {
		out.ThroughputSeries = append(out.ThroughputSeries, ThroughputPoint{
			RunID:               rep.RunID,
			StartedAt:           rep.StartedAt,
			AchievedThroughput:  rep.Achieved.Throughput,
			RequestedThroughput: rep.Requested.Throughput,
			Regressed:           regressed[rep.RunID],
		})
	}
	return out, nil
}
