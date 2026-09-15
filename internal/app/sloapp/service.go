// Package sloapp is the service-level-objective use-case: it administers the
// objectives registered per project, and grades them -- computes the budget
// each target has left over a time window of run reports.
//
// The grading reads the same reports surface the trend, digest, and summary
// reads read (ports.ReportStore.ListReports, scoped to the project's
// executions via ListExecutionsByProject) -- no raw SQL, no second accounting
// of what a run did, the composition rule digestapp established (phase 42):
// there is no project column on execution_report to filter by, and the
// project's own surfaces are reached through the project's own executions.
package sloapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/heridotlife/honryu/internal/domain/slo"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

// Business-rule errors. Callers compare with errors.Is.
var (
	// ErrDuplicateName rejects a second SLO with the same name under one
	// project -- the 0066 unique key's rule, checked here so callers get
	// the domain's message before the driver's error code.
	ErrDuplicateName = errors.New("sloapp: an SLO with this name already exists in the project")
)

// Repo is the persistence sloapp needs: the SLO registry itself, plus the
// project's executions and their reports (the same surfaces the digest and
// summary reads compose from).
type Repo interface {
	ports.SLOStore
	ports.ExecutionRepository
	ports.ReportStore
}

// Service implements the SLO use-cases: CRUD plus budget computation.
type Service struct {
	repo Repo
}

// NewService wires a Service to its repository.
func NewService(repo Repo) *Service {
	return &Service{repo: repo}
}

// Create validates and persists a new SLO, returning it with its assigned
// ID. The at-least-one-target rule is the domain's Validate; the per-project
// name uniqueness is checked here (ListSLOsByProject, a project's SLO count
// is small) so a duplicate reads as a domain error rather than a driver
// code -- the storage's unique key stays as the concurrency backstop.
func (s *Service) Create(ctx context.Context, obj slo.SLO) (slo.SLO, error) {
	if err := obj.Validate(); err != nil {
		return slo.SLO{}, err
	}
	existing, err := s.repo.ListSLOsByProject(ctx, obj.ProjectID)
	if err != nil {
		return slo.SLO{}, err
	}
	for _, e := range existing {
		if e.Name == obj.Name {
			return slo.SLO{}, ErrDuplicateName
		}
	}
	id, err := s.repo.CreateSLO(ctx, obj)
	if err != nil {
		return slo.SLO{}, err
	}
	obj.ID = id
	return obj, nil
}

// List returns the project's SLOs in definition order, oldest first.
func (s *Service) List(ctx context.Context, projectID int64) ([]slo.SLO, error) {
	return s.repo.ListSLOsByProject(ctx, projectID)
}

// Get returns one project-scoped SLO, or ports.ErrNotFound.
func (s *Service) Get(ctx context.Context, projectID, id int64) (slo.SLO, error) {
	return s.repo.GetSLO(ctx, projectID, id)
}

// Delete removes one project-scoped SLO, or ports.ErrNotFound.
func (s *Service) Delete(ctx context.Context, projectID, id int64) error {
	return s.repo.DeleteSLO(ctx, projectID, id)
}

// Outcome grades one SLO over one window: the computed budget plus the
// identity a reader needs to know whose budget it is.
type Outcome struct {
	SLO   slo.SLO
	SLOID int64
	// Window is the window word the caller asked for ("7d").
	Window string
	// WindowStart/WindowEnd are the half-open span the reports were
	// filtered by: [start, end).
	WindowStart time.Time
	WindowEnd   time.Time
	// RunCount is how many eligible reports the window held. Aborted runs
	// are excluded: an operator's stop button is not the target's
	// behaviour, the same exclusion the success-ratio denominator makes.
	RunCount int
	Budget   slo.Budget
}

// WindowOutcome is one SLO's grade over an explicit span -- the shape the
// digest builder consumes, whose windows are spans (the digest's own tiling
// [windowStart, windowEnd)), not the named query windows the HTTP endpoint
// serves.
type WindowOutcome struct {
	SLO      slo.SLO
	SLOID    int64
	RunCount int
	Budget   slo.Budget
}

// Budget grades the project's SLO over window: the window's eligible reports
// (started in [now-duration, now), aborted runs excluded) aggregated per the
// domain's rules -- p95 and error rate as run-count-weighted means, success
// ratio as passed over non-aborted -- then graded by SLO.Evaluate. Aborts the
// whole read with ports.ErrNotFound when the SLO does not exist under the
// project: a foreign SLO's budget is not this project's to see.
func (s *Service) Budget(ctx context.Context, projectID, sloID int64, window string, now time.Time) (Outcome, error) {
	duration, err := slo.ParseWindow(window)
	if err != nil {
		return Outcome{}, err
	}
	obj, err := s.repo.GetSLO(ctx, projectID, sloID)
	if err != nil {
		return Outcome{}, err
	}
	end := now
	start := now.Add(-duration)
	actual, runCount, err := s.Aggregate(ctx, projectID, start, end)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{
		SLO: obj, SLOID: obj.ID, Window: window,
		WindowStart: start, WindowEnd: end,
		RunCount: runCount,
		Budget:   obj.Evaluate(actual),
	}, nil
}

// Aggregate composes the project's eligible reports over the half-open span
// [start, end) into the SLO domain's Actual, plus the eligible count.
// Exported for the digest builder (phase 68), which grades every SLO the
// project defines over the digest's own tiling window -- the same
// aggregation the named-window endpoint serves, with no second accounting
// of what a run did.
func (s *Service) Aggregate(ctx context.Context, projectID int64, start, end time.Time) (slo.Actual, int, error) {
	return s.aggregate(ctx, projectID, start, end)
}

// ProjectWindowBudgets grades every SLO the project defines over
// [start, end). Always non-nil; empty when the project has no SLOs. This is
// digestapp's SLOGrader -- one call per fire, the digest's own span.
func (s *Service) ProjectWindowBudgets(ctx context.Context, projectID int64, start, end time.Time) ([]WindowOutcome, error) {
	objs, err := s.repo.ListSLOsByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]WindowOutcome, 0, len(objs))
	if len(objs) == 0 {
		return out, nil
	}
	actual, runCount, err := s.Aggregate(ctx, projectID, start, end)
	if err != nil {
		return nil, err
	}
	for _, obj := range objs {
		out = append(out, WindowOutcome{
			SLO: obj, SLOID: obj.ID, RunCount: runCount,
			Budget: obj.Evaluate(actual),
		})
	}
	return out, nil
}

// aggregate composes the window's eligible reports into the SLO domain's
// Actual, project-wide: every execution's reports, filtered to the half-open
// span, aborted runs excluded. The per-run figures are already pod-merged
// (a report's p95 comes from the merged buckets of every pod), so the
// window aggregates are means over those per-run values -- not percentiles
// of percentiles.
func (s *Service) aggregate(ctx context.Context, projectID int64, start, end time.Time) (slo.Actual, int, error) {
	execs, err := s.repo.ListExecutionsByProject(ctx, projectID)
	if err != nil {
		return slo.Actual{}, 0, fmt.Errorf("sloapp: list executions: %w", err)
	}
	var (
		p95s       []float64
		errorRates []float64
		passed     int
		eligible   int
	)
	for _, exe := range execs {
		reps, err := s.repo.ListReports(ctx, exe.ID, 0)
		if err != nil {
			return slo.Actual{}, 0, fmt.Errorf("sloapp: list reports: %w", err)
		}
		for _, rep := range reps {
			if rep.StartedAt.Before(start) || !rep.StartedAt.Before(end) {
				continue
			}
			// Aborted runs measure the operator's patience, not the
			// target's behaviour: a run stopped mid-flight carries a
			// partial window's error rate and a latency sample cut off
			// at the stop. Excluded from every metric, including the
			// success-ratio denominator (only passed/(passed+failed+
			// error) grades the service).
			if rep.Outcome == taurus.OutcomeAborted {
				continue
			}
			eligible++
			if p95, ok := rep.Latency[95]; ok {
				p95s = append(p95s, p95)
			}
			errorRates = append(errorRates, rep.ErrorRate)
			if rep.Outcome == taurus.OutcomePassed {
				passed++
			}
		}
	}
	if eligible == 0 {
		return slo.Actual{}, 0, nil
	}
	// Reports keep latency in seconds; the SLO's target is quoted in
	// milliseconds (the unit operators quote), so the actual crosses over
	// exactly here, once, with the unit named in the metric itself.
	p95MS := make([]float64, len(p95s))
	for i, secs := range p95s {
		p95MS[i] = secs * 1000
	}
	return slo.Actual{
		P95MS:        slo.WindowMean(p95MS),
		ErrorRate:    slo.WindowMean(errorRates),
		SuccessRatio: ratio(passed, eligible),
	}, eligible, nil
}

// ratio is part over whole as a 0..1 float, nil when there was nothing to
// measure -- the aggregate's "no data" shape, which Evaluate reads as a
// vacuous line.
func ratio(part, whole int) *float64 {
	if whole <= 0 {
		return nil
	}
	v := float64(part) / float64(whole)
	return &v
}
