// Package digestapp is the periodic report-digest use-case: it aggregates
// every completed run a project's executions produced in a window into one
// payload, stores that payload as the durable digest row, and delivers it to
// the project's webhooks as a report.digest event.
//
// The aggregation reads the same reports surface the Reports page reads
// (ports.ReportStore.ListReports, scoped to the project's executions via
// ListExecutionsByProject) -- no raw SQL, no second accounting of what a run
// did. Delivery rides webhookapp's machinery (same signing, same bounds, no
// duplicate HTTP client) through the Deliverer interface, so a digest is
// delivered exactly like a run.completed event: best-effort, never able to
// fail the firing that produced it. The stored row is the source of truth;
// a receiver that was down reads the next one.
package digestapp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/heridotlife/honryu/internal/domain/digest"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

// EventDigest is the event name every digest delivery carries, the sibling
// of webhookapp's run.completed: a receiver that only wants summaries
// switches on it and ignores per-run events.
const EventDigest = "report.digest"

// ByOutcome counts the window's runs per outcome. The three keys the wire
// contract names are the three a digest reader acts on; an error-outcome
// run (engine or infra failed) still counts in runs_total and its
// execution's worst_outcome, it just has no bucket here -- a digest says
// what the load did, and "the engine never ran" is neither a pass nor a
// threshold failure.
type ByOutcome struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Aborted int `json:"aborted"`
}

// ExecutionSummary is one execution's share of the window: how many of its
// runs landed in the window and the worst way any of them ended.
type ExecutionSummary struct {
	ExecutionID  int64  `json:"execution_id"`
	Name         string `json:"name"`
	Runs         int    `json:"runs"`
	WorstOutcome string `json:"worst_outcome"`
}

// Payload is the report.digest event body: what a receiver needs to render
// one window of a project's load-testing at a glance. Field names are the
// wire contract -- the same bytes go to webhook receivers and into the
// stored row.
type Payload struct {
	Event       string        `json:"event"`
	ProjectID   int64         `json:"project_id"`
	Period      digest.Period `json:"period"`
	WindowStart time.Time     `json:"window_start"`
	WindowEnd   time.Time     `json:"window_end"`
	RunsTotal   int           `json:"runs_total"`
	ByOutcome   ByOutcome     `json:"by_outcome"`
	// ThresholdFailures counts the window's runs whose configured Taurus
	// criteria tripped (failed outcome) -- the number a reader scanning for
	// regressions looks for first.
	ThresholdFailures int                `json:"threshold_failures"`
	Executions        []ExecutionSummary `json:"executions"`
}

// outcomeSeverity orders outcomes worst-ward for WorstOutcome: a passed run
// cannot mask an aborted one, an aborted one cannot mask a failed one. An
// error outcome ranks worst -- "the run could not even run" is the loudest
// thing a window can say.
var outcomeSeverity = map[taurus.Outcome]int{
	taurus.OutcomePassed:  1,
	taurus.OutcomeAborted: 2,
	taurus.OutcomeFailed:  3,
	taurus.OutcomeError:   4,
}

// Repo is the persistence digestapp needs: the project's executions and
// their reports (the same surface the Reports page reads), plus the digest
// rows themselves.
type Repo interface {
	ports.ExecutionRepository
	ports.ReportStore
	ports.ReportDigestStore
}

// Deliverer is the delivery sink a digest rides on -- webhookapp satisfies
// it, delivering with the same signing and bounds as a run.completed event.
// An interface, not an import, so the use-case stays testable against a
// recorder and the delivery mechanism stays webhookapp's to own.
type Deliverer interface {
	DeliverEvent(ctx context.Context, projectID int64, event string, body []byte) error
}

// Service implements the digest use-cases: building, firing, and listing.
type Service struct {
	repo      Repo
	deliverer Deliverer
	log       *slog.Logger
}

// NewService wires the digest service. Without a Deliverer (the default),
// Fire stores the digest but delivers nothing -- the in-app feed alone.
func NewService(repo Repo) *Service {
	return &Service{repo: repo, log: slog.Default()}
}

// WithDeliverer sets the webhook delivery sink. Returns the receiver for
// chaining.
func (s *Service) WithDeliverer(d Deliverer) *Service {
	if d != nil {
		s.deliverer = d
	}
	return s
}

// WithLogger overrides the service logger. Returns the receiver for
// chaining.
func (s *Service) WithLogger(log *slog.Logger) *Service {
	if log != nil {
		s.log = log
	}
	return s
}

// BuildDigest aggregates the project's runs whose reports started in
// [windowStart, windowEnd) into the digest payload -- without storing or
// delivering anything. Pure read: the same window always aggregates the
// same numbers, which is what makes the stored row's payload reproducible
// after the fact.
func (s *Service) BuildDigest(ctx context.Context, projectID int64, period digest.Period, windowStart, windowEnd time.Time) (digest.Digest, error) {
	if _, err := digest.ParsePeriod(string(period)); err != nil {
		return digest.Digest{}, err
	}
	execs, err := s.repo.ListExecutionsByProject(ctx, projectID)
	if err != nil {
		return digest.Digest{}, err
	}
	p := Payload{
		Event: EventDigest, ProjectID: projectID, Period: period,
		WindowStart: windowStart, WindowEnd: windowEnd,
		Executions: []ExecutionSummary{},
	}
	for _, exe := range execs {
		reps, err := s.repo.ListReports(ctx, exe.ID, 0)
		if err != nil {
			return digest.Digest{}, err
		}
		summary := ExecutionSummary{ExecutionID: exe.ID, Name: exe.Name}
		for _, rep := range reps {
			if rep.StartedAt.Before(windowStart) || !rep.StartedAt.Before(windowEnd) {
				continue
			}
			summary.Runs++
			p.RunsTotal++
			switch rep.Outcome {
			case taurus.OutcomePassed:
				p.ByOutcome.Passed++
			case taurus.OutcomeFailed:
				p.ByOutcome.Failed++
			case taurus.OutcomeAborted:
				p.ByOutcome.Aborted++
			}
			if outcomeSeverity[rep.Outcome] > outcomeSeverity[taurus.Outcome(summary.WorstOutcome)] {
				summary.WorstOutcome = string(rep.Outcome)
			}
		}
		if summary.Runs > 0 {
			p.Executions = append(p.Executions, summary)
		}
	}
	p.ThresholdFailures = p.ByOutcome.Failed
	body, err := json.Marshal(&p)
	if err != nil {
		return digest.Digest{}, fmt.Errorf("digest: marshal payload: %w", err)
	}
	return digest.Digest{
		ProjectID: projectID, Period: period,
		WindowStart: windowStart, WindowEnd: windowEnd,
		Payload: body,
	}, nil
}

// Fire builds and stores the project's digest for period, then delivers the
// stored bytes to the project's webhooks. The window continues from the
// last same-period digest's window_end, or reaches back one full period
// when none has fired yet -- so consecutive digests tile the timeline
// without gaps or overlaps. Delivery failure is logged, never returned: the
// stored row is the source of truth and the in-app feed still has it, the
// same best-effort law run.completed delivery follows.
func (s *Service) Fire(ctx context.Context, projectID int64, period digest.Period, now time.Time) (digest.Digest, error) {
	windowStart, found, err := s.repo.LastDigestWindowEnd(ctx, projectID, period)
	if err != nil {
		return digest.Digest{}, err
	}
	if !found {
		windowStart = now.Add(-period.Duration())
	}
	d, err := s.BuildDigest(ctx, projectID, period, windowStart, now)
	if err != nil {
		return digest.Digest{}, err
	}
	id, err := s.repo.SaveDigest(ctx, d)
	if err != nil {
		return digest.Digest{}, err
	}
	d.ID = id
	if s.deliverer != nil {
		if err := s.deliverer.DeliverEvent(ctx, projectID, EventDigest, d.Payload); err != nil {
			s.log.Error("digest: deliver", "project_id", projectID, "period", period, "digest_id", d.ID, "error", err)
		}
	}
	return d, nil
}

// ListForProject returns the project's stored digests, newest first, for
// the in-app feed. limit behaves as ListDigestsByProject does.
func (s *Service) ListForProject(ctx context.Context, projectID int64, limit int) ([]digest.Digest, error) {
	return s.repo.ListDigestsByProject(ctx, projectID, limit)
}

// DecodePayload parses a stored digest's payload bytes. The handler layer's
// helper for serving the feed without re-aggregating: the stored bytes are
// the contract, this only reads them back.
func DecodePayload(raw []byte) (Payload, error) {
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Payload{}, fmt.Errorf("digest: decode payload: %w", err)
	}
	return p, nil
}
