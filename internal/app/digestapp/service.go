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
	"sort"
	"time"

	"github.com/heridotlife/honryu/internal/app/sloapp"
	"github.com/heridotlife/honryu/internal/domain/digest"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/domain/threshold"
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

// SLOBudgetLine is one SLO's share of the window (phase 68): whether the
// window stayed within the objective, and which metric came closest to --
// or past -- its target. The worst metric is the one with the least budget
// remaining, the line a reader triages by; both worst fields are
// absent/null when the window held no eligible runs for the objective.
type SLOBudgetLine struct {
	SLOID     int64  `json:"slo_id"`
	Name      string `json:"name"`
	Compliant bool   `json:"compliant"`
	// WorstMetric names the metric carrying the least budget remaining
	// ("" when the window held no eligible runs).
	WorstMetric string `json:"worst_metric,omitempty"`
	// WorstBudgetRemainingPct is that metric's budget_remaining_pct --
	// the SLO formula's own number, positive = margin, negative =
	// burned. Nil when the window held no eligible runs.
	WorstBudgetRemainingPct *float64 `json:"worst_budget_remaining_pct"`
}

// Outcome names for ThresholdLine.Outcome -- the three verdicts a digest
// reader acts on (ThresholdLine's doc carries the law).
const (
	// ThresholdOutcomeAllMet: every graded bound was satisfied.
	ThresholdOutcomeAllMet = "all_met"
	// ThresholdOutcomeMissed: at least one bound was definitively crossed.
	ThresholdOutcomeMissed = "missed"
	// ThresholdOutcomeUnknown: nothing definitively crossed, but some
	// bound could not be judged (its metric was absent from the report).
	ThresholdOutcomeUnknown = "unknown"
)

// CalibrationLine is one calibration search's share of the window (phase
// 71): where the search stands, which scenario it measured, and -- when it
// finished -- what a single pod sustained. A failed search is as
// digest-worthy as a finished one (an operational failure IS the capacity
// story that week), so phase and failure_reason carry it; per_pod_qps and
// saturated_by are absent until a search actually concludes.
type CalibrationLine struct {
	JobID        int64  `json:"job_id"`
	ScenarioID   int64  `json:"scenario_id"`
	ScenarioName string `json:"scenario_name"`
	Phase        string `json:"phase"`
	// PerPodQPS is the search's terminal per-pod capacity (confirmed or
	// floor, saturated_by's own doc says which) -- nil until phase done.
	PerPodQPS *float64 `json:"per_pod_qps"`
	// SaturatedBy names the side that gave out (engine/target/neither);
	// absent until phase done.
	SaturatedBy string `json:"saturated_by,omitempty"`
	// FailureReason is set once phase is failed -- an operational failure
	// (a step's run itself errored) or an environment-impaired verdict.
	FailureReason string    `json:"failure_reason,omitempty"`
	CreatedTime   time.Time `json:"created_time"`
}

// ThresholdMiss is one bound a run did not meet (or could not be judged
// on): the definition as it stood at evaluation time -- a result is
// historical evidence, legible even after the operator re-edits the
// scenario's thresholds -- plus what the run actually produced.
type ThresholdMiss struct {
	Metric     threshold.Metric     `json:"metric"`
	Comparison threshold.Comparison `json:"comparison"`
	// Value is the bound in the metric's own unit.
	Value float64 `json:"value"`
	// ObservedValue is the run's own figure for the metric; nil when the
	// report carried none (the reason then says which figure was missing).
	ObservedValue *float64 `json:"observed_value"`
	// Reason is set only for an unjudgeable bound -- why there was nothing
	// to compare. Empty for a plain miss.
	Reason string `json:"reason,omitempty"`
}

// ThresholdLine is one run's threshold verdict (phase 74): whether the
// scenario's bounds held over that run, and which did not. Only runs that
// were actually graded carry a line -- a run whose scenario defined no
// thresholds (or predates the feature) is excluded rather than rendered as
// null-empty noise, the same honesty calibrations' job lines follow. A run
// graded but unjudgeable (every metric missing) still carries one: the
// unknown verdict IS the evidence.
type ThresholdLine struct {
	RunID       int64 `json:"run_id"`
	ExecutionID int64 `json:"execution_id"`
	// ScenarioName is the graded scenario's name at build time (joined
	// from the project's scenario list; empty if the scenario was since
	// deleted).
	ScenarioName string `json:"scenario_name"`
	// Outcome summarises the run's results: all_met when every graded
	// bound was satisfied, missed when any bound was definitively crossed,
	// unknown when nothing was crossed but some bound could not be judged
	// (a percentile the engine never measured). A definitive miss outranks
	// an unknown -- triage reads missed first.
	Outcome string `json:"outcome"`
	// Missed lists every bound the run did not meet: definitive crosses
	// with their observed values, and unjudgeable bounds with the reason
	// (observed_value null). Empty for an all_met run.
	Missed []ThresholdMiss `json:"missed"`
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
	// SLOBudgets grades the project's objectives over this same window
	// (phase 68). Always an array -- empty when the project defines no
	// SLOs or no grader is wired -- so a receiver renders "none", never
	// noughts.
	SLOBudgets []SLOBudgetLine `json:"slo_budgets"`
	// Calibrations lists the window's calibration searches for this
	// project (phase 71), newest first. Always an array -- empty when the
	// project ran none -- so a receiver renders "none", never noughts. A
	// job surfaces regardless of whether its scenario produced any runs:
	// a failed calibration is the window's capacity story too.
	Calibrations []CalibrationLine `json:"calibrations"`
	// Thresholds carries one line per window run that was graded against
	// its scenario's thresholds (phase 74), in run order. Always an array
	// -- empty when no window run has threshold results -- so a receiver
	// renders "none", never noughts.
	Thresholds []ThresholdLine `json:"thresholds"`
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
// their reports (the same surface the Reports page reads), the digest rows
// themselves, and the firing schedules.
type Repo interface {
	ports.ExecutionRepository
	ports.ReportStore
	ports.ReportDigestStore
	ports.DigestScheduleStore
	// ListCalibrationJobsByProject is the calibration ledger's by-project
	// window read (ports.CalibrationJobRepository's method, declared here
	// inline the way calibrationapp.Repo declares its own cross-aggregate
	// reads): the window's calibration lines, scenario names joined.
	ListCalibrationJobsByProject(ctx context.Context, projectID int64, start, end time.Time) ([]ports.CalibrationJobSummary, error)
	// ListScenariosByProject is the project's scenario list
	// (ports.ScenarioRepository's method, declared here inline like the
	// read above): the id→name join the threshold lines' scenario_name
	// needs.
	ListScenariosByProject(ctx context.Context, projectID int64) ([]scenario.Scenario, error)
	// ThresholdResultsForRun is the run's stored threshold results
	// (ports.ThresholdStore's method, declared here inline): the per-run
	// grading evidence the threshold lines aggregate. Empty (never nil)
	// when the run was never graded.
	ThresholdResultsForRun(ctx context.Context, runID int64) ([]threshold.Result, error)
}

// Deliverer is the delivery sink a digest rides on -- webhookapp satisfies
// it, delivering with the same signing and bounds as a run.completed event,
// plus the deploy-wide sink when one is configured. An interface, not an
// import, so the use-case stays testable against a recorder and the
// delivery mechanism stays webhookapp's to own. The answer is the tri-state
// DeliverDigest documents: delivered / failed / nothing-to-do.
type Deliverer interface {
	DeliverDigest(ctx context.Context, projectID int64, body []byte) (delivered bool, err error)
}

// SLOGrader is the optional SLO-budget source the digest builder consults
// (phase 68): sloapp satisfies it, grading the project's objectives over
// exactly the digest's own tiling window -- the same aggregation the
// budget endpoint serves, so a digest line can never disagree with the
// dashboard's badge. An interface, not an import, the Deliverer precedent:
// a deployment without SLOs wires nothing and the payload carries an empty
// array.
type SLOGrader interface {
	ProjectWindowBudgets(ctx context.Context, projectID int64, start, end time.Time) ([]sloapp.WindowOutcome, error)
}

// Service implements the digest use-cases: building, firing, and listing.
type Service struct {
	repo      Repo
	deliverer Deliverer
	// grader, when wired, adds the slo_budgets lines to every payload.
	grader SLOGrader
	log    *slog.Logger
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

// WithSLOGrader sets the SLO-budget source (phase 68). Returns the receiver
// for chaining. Without one, payloads carry an empty slo_budgets array --
// the honest reading of "no objectives are graded here".
func (s *Service) WithSLOGrader(g SLOGrader) *Service {
	if g != nil {
		s.grader = g
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
		Executions:   []ExecutionSummary{},
		SLOBudgets:   []SLOBudgetLine{},
		Calibrations: []CalibrationLine{},
		Thresholds:   []ThresholdLine{},
	}
	// The window's graded runs, kept in iteration order for the threshold
	// section below -- the same run set the executions section covers.
	type windowRun struct {
		executionID int64
		scenarioID  int64
		runID       int64
	}
	var windowRuns []windowRun
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
			windowRuns = append(windowRuns, windowRun{executionID: exe.ID, scenarioID: rep.ScenarioID, runID: rep.RunID})
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
	// The threshold section joins the same run set the executions section
	// covers with the per-run grading evidence (phase 74): scenario names
	// resolved in one project read, each window run's results read the way
	// the run-detail surface reads them. A storage-level failure fails the
	// build -- the same law the calibrations read follows -- rather than
	// sending a digest whose threshold section silently understates the
	// window's misses.
	if len(windowRuns) > 0 {
		scenarios, err := s.repo.ListScenariosByProject(ctx, projectID)
		if err != nil {
			return digest.Digest{}, fmt.Errorf("digest: thresholds: %w", err)
		}
		names := make(map[int64]string, len(scenarios))
		for _, sc := range scenarios {
			names[sc.ID] = sc.Name
		}
		// Deterministic wire order: run id ascending, independent of
		// each execution's report-list ordering.
		sort.Slice(windowRuns, func(i, j int) bool { return windowRuns[i].runID < windowRuns[j].runID })
		for _, wr := range windowRuns {
			results, err := s.repo.ThresholdResultsForRun(ctx, wr.runID)
			if err != nil {
				return digest.Digest{}, fmt.Errorf("digest: thresholds: %w", err)
			}
			if len(results) == 0 {
				continue // never graded: excluded, not null-empty noise
			}
			line := ThresholdLine{
				RunID: wr.runID, ExecutionID: wr.executionID,
				ScenarioName: names[wr.scenarioID],
				Missed:       []ThresholdMiss{},
			}
			missed, unknown := false, false
			for _, r := range results {
				if r.Satisfied != nil && *r.Satisfied {
					continue // met: no line
				}
				if r.Satisfied == nil {
					unknown = true
				} else {
					missed = true
				}
				line.Missed = append(line.Missed, ThresholdMiss{
					Metric: r.Metric, Comparison: r.Comparison, Value: r.Value,
					ObservedValue: r.Observed, Reason: r.Reason,
				})
			}
			switch {
			case missed:
				line.Outcome = ThresholdOutcomeMissed
			case unknown:
				line.Outcome = ThresholdOutcomeUnknown
			default:
				line.Outcome = ThresholdOutcomeAllMet
			}
			p.Thresholds = append(p.Thresholds, line)
		}
	}
	// The window's calibration searches ride the same law as the runs
	// above: a storage-level failure fails the build -- the fire is skipped
	// and the scheduler's record shows why -- rather than sending a digest
	// that silently omits searches the operator ran.
	cals, err := s.repo.ListCalibrationJobsByProject(ctx, projectID, windowStart, windowEnd)
	if err != nil {
		return digest.Digest{}, fmt.Errorf("digest: calibrations: %w", err)
	}
	for _, c := range cals {
		line := CalibrationLine{
			JobID: c.ID, ScenarioID: c.ScenarioID, ScenarioName: c.ScenarioName,
			Phase: string(c.Phase), FailureReason: c.FailureReason,
			CreatedTime: c.CreatedTime,
		}
		if c.Result != nil {
			qps := c.Result.PerPodQPS
			line.PerPodQPS = &qps
			line.SaturatedBy = string(c.Result.SaturatedBy)
		}
		p.Calibrations = append(p.Calibrations, line)
	}
	// The SLO lines grade over exactly the digest's own tiling window --
	// never a separate named window -- so a digest's verdict is the same
	// one the dashboard would compute for the same span. A storage-level
	// failure fails the build: the fire is skipped and the scheduler's
	// record shows why, rather than a digest that silently omits data the
	// operator configured (the same law that fails the run-report read
	// above instead of sending a hollow digest).
	if s.grader != nil {
		grades, err := s.grader.ProjectWindowBudgets(ctx, projectID, windowStart, windowEnd)
		if err != nil {
			return digest.Digest{}, fmt.Errorf("digest: slo budgets: %w", err)
		}
		for _, g := range grades {
			line := SLOBudgetLine{
				SLOID: g.SLOID, Name: g.SLO.Name,
				Compliant: g.Budget.Compliant,
			}
			if worst, ok := g.Budget.Worst(); ok && worst.BudgetRemainingPct != nil {
				line.WorstMetric = worst.Metric
				line.WorstBudgetRemainingPct = worst.BudgetRemainingPct
			}
			p.SLOBudgets = append(p.SLOBudgets, line)
		}
	}
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
// stored bytes through the deliverer (the project's webhooks plus the
// deploy-wide sink when one is configured), recording the outcome on the
// row: delivered stamps DeliveredAt, attempted-but-lost marks failed,
// nothing-attempted stays pending. The window continues from the last
// same-period digest's window_end, or reaches back one full period when
// none has fired yet -- so consecutive digests tile the timeline without
// gaps or overlaps. Delivery failure is logged and surfaced as a failed
// status, never returned: the stored row is the source of truth and the
// in-app feed still has it, the same best-effort law run.completed delivery
// follows.
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
	d.DeliveryStatus = digest.DeliveryPending
	if s.deliverer != nil {
		delivered, err := s.deliverer.DeliverDigest(ctx, projectID, d.Payload)
		var deliveredAt time.Time
		switch {
		case delivered:
			d.DeliveryStatus = digest.DeliveryDelivered
			deliveredAt = now
			at := now
			d.DeliveredAt = &at
		case err != nil:
			// Attempted and entirely lost: the window is never re-fired, so
			// the failure must be visible on the record, not just in a log.
			d.DeliveryStatus = digest.DeliveryFailed
		default:
			// (false, nil): nothing was configured to notify. "Nothing to
			// do" is not a failure -- the row stays pending.
		}
		if err != nil {
			s.log.Error("digest: deliver", "project_id", projectID, "period", period, "digest_id", d.ID, "error", err)
		}
		if d.DeliveryStatus != digest.DeliveryPending {
			if err := s.repo.MarkDigestDelivery(ctx, d.ID, d.DeliveryStatus, deliveredAt); err != nil {
				s.log.Error("digest: mark delivery", "digest_id", d.ID, "status", d.DeliveryStatus, "error", err)
			}
		}
	}
	return d, nil
}

// ListForProject returns the project's stored digests, newest first, for
// the in-app feed. limit behaves as ListDigestsByProject does.
func (s *Service) ListForProject(ctx context.Context, projectID int64, limit int) ([]digest.Digest, error) {
	return s.repo.ListDigestsByProject(ctx, projectID, limit)
}

// SetSchedule configures the project's digest firing: upserts the period
// and enables it. The API's whole "on" path -- turning it off is a delete,
// not an enabled=false row, so a paused-for-good project leaves no row the
// scheduler keeps re-examining.
func (s *Service) SetSchedule(ctx context.Context, projectID int64, period digest.Period) error {
	if _, err := digest.ParsePeriod(string(period)); err != nil {
		return err
	}
	return s.repo.UpsertDigestSchedule(ctx, projectID, period, true)
}

// GetSchedule returns the project's digest configuration, or
// ports.ErrNotFound when none is set.
func (s *Service) GetSchedule(ctx context.Context, projectID int64) (digest.Schedule, error) {
	return s.repo.GetDigestSchedule(ctx, projectID)
}

// DeleteSchedule removes the project's digest configuration, or
// ports.ErrNotFound when none is set.
func (s *Service) DeleteSchedule(ctx context.Context, projectID int64) error {
	return s.repo.DeleteDigestSchedule(ctx, projectID)
}

// ClaimDue claims the most overdue due, enabled schedule -- the scheduler
// loop's half of the firing handshake. The claim happens BEFORE Fire's
// window read on purpose: exclusivity comes from the stamp (a second
// replica's claim affects zero rows), so at most one fire per due window
// even with many replicas polling. A claimed fire that then fails to build
// is skipped, never re-fired for the same window -- the same trade
// ClaimDueOccurrence makes for occurrences.
func (s *Service) ClaimDue(ctx context.Context, now time.Time) (digest.Schedule, bool, error) {
	return s.repo.ClaimDueDigestSchedule(ctx, now)
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
