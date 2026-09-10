// Package metricsapp is the metric use-case: it absorbs the measurements engine
// pods push, stamps each with the pod that produced it, fans it to the
// MetricsSink (Prometheus) and the EventBus (SSE subscribers) for the live view,
// and accumulates it into the run's report.
//
// Measurements used to be pulled: the controller opened a stream to every
// engine's agent, which meant tracking which executions were being collected,
// re-establishing those streams after a restart, and losing whatever a pod
// measured once it became unreachable. Under Taurus a sidecar in each pod pushes
// instead, so none of that machinery has anything to do -- an unreachable pod is
// simply one that has stopped sending, and what it sent already arrived.
//
// What remains is dropping an execution's series when it is purged, and
// finalising a run's report once it is over -- naturally, when every shard has
// said it is done, or because Honryu itself is ending it.
package metricsapp

import (
	"context"
	"time"

	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/run"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

// Repo is the persistence the service reads to attribute a pushed batch,
// finalise a run's report, and close the run's marker once it does.
type Repo interface {
	// GetExecution supplies a report's Engine, from the execution's own
	// configured preference.
	GetExecution(ctx context.Context, executionID int64) (execution.Execution, error)
	LoadProfileFor(ctx context.Context, executionID int64) ([]loadprofile.Entry, error)
	CurrentRun(ctx context.Context, executionID int64) (int64, bool, error)
	// RunHistory supplies a report's StartedAt: nothing else keeps when a run
	// began once it is no longer the active one.
	RunHistory(ctx context.Context, runID int64) (ports.RunRecord, error)
	// StopRun clears the active run and stamps its history end time: the
	// same closer teardown uses, held here so natural completion can close
	// its own marker without lifecycleapp's involvement. Stopping an
	// execution with no active run is not an error.
	StopRun(ctx context.Context, executionID int64) error
	// OrphanCompletions' recording side: a shard Final that arrives with no
	// open run is evidence the engines already finished, and Trigger refuses
	// to open a corpse-run against it until the next Deploy clears it.
	RecordOrphanCompletion(ctx context.Context, oc ports.OrphanCompletion) error
}

// Service absorbs pushed measurements and finalises runs.
type Service struct {
	repo     Repo
	sink     ports.MetricsSink
	bus      ports.EventBus
	progress ports.ReportProgress
	reports  ports.ReportStore
	// notifier is the run-completion hook: after a run's report is saved
	// it receives the report and the execution's project, and fans a
	// run.completed event out to the project's registered webhooks. The
	// webhookapp service implements it; a no-op default is used when none
	// is wired (e.g. deployments and tests that do not want notifications).
	notifier Notifier
	// seen deduplicates intervals a pod pushed more than once, for the live
	// view only. The permanent record's exactness comes from ReportProgress's
	// own per-shard sequence, which survives a restart this map does not.
	seen *seen
	now  func() time.Time
}

// Notifier is the outbound notification a completed run produces. It has
// no error return on purpose: a notification is best-effort and must
// never be able to fail the run it reports -- the webhook use-case drops,
// retries, and logs entirely on its own side of this interface.
type Notifier interface {
	// RunCompleted is called once per run, after the run's report has been
	// saved, with the execution's project (webhooks are registered per
	// project) and the report as stored.
	RunCompleted(ctx context.Context, projectID int64, rep report.Report)
}

// noopNotifier is the default Notifier: nothing is notified.
type noopNotifier struct{}

func (noopNotifier) RunCompleted(context.Context, int64, report.Report) {}

// NewService wires the metric service.
func NewService(repo Repo, sink ports.MetricsSink, bus ports.EventBus, progress ports.ReportProgress, reports ports.ReportStore) *Service {
	return &Service{repo: repo, sink: sink, bus: bus, progress: progress, reports: reports, notifier: noopNotifier{}, seen: newSeen(), now: time.Now}
}

// WithNotifier overrides the run-completion hook. A nil notifier is
// ignored, so WithNotifier(nil) disables nothing. Returns the receiver for
// chaining.
func (s *Service) WithNotifier(n Notifier) *Service {
	if n != nil {
		s.notifier = n
	}
	return s
}

// WithNow overrides the clock a finalised report is stamped with. Returns the
// receiver for chaining.
func (s *Service) WithNow(now func() time.Time) *Service {
	if now != nil {
		s.now = now
	}
	return s
}

// Purge drops an execution's metric series and forgets what it absorbed.
//
// Called when an execution's engines are removed. Without it a long-lived
// controller would hold a series for every execution it had ever run.
func (s *Service) Purge(executionID int64) {
	s.sink.DeleteExecution(executionID)
	s.seen.forget(executionID)
}

// Finalize writes the report for a run Honryu is deliberately ending -- a
// user-initiated Stop or Purge -- rather than one that finished on its own.
//
// Idempotent: a run already finalised by its own natural completion is left
// untouched. That is what stops a Purge called after a run has already
// finished from overwriting its real verdict with "aborted" -- teardown and
// natural completion are racing to finalise the same run, and whichever gets
// there first decides it. The guarantee comes from SaveReport itself (the
// first report saved for a run is the one that survives), not from a check
// here first: a plain existence check followed later by a save would leave a
// window where both racers could pass the check before either had written.
func (s *Service) Finalize(ctx context.Context, executionID, runID int64) error {
	outcome, err := s.stopOutcome(ctx, runID)
	if err != nil {
		return err
	}
	return s.finalize(ctx, executionID, runID, outcome)
}

// FinalizeOrphaned writes the report for a run whose engines finished while it
// was open -- the stranded-run case a reconciliation pass closes. The outcome
// mirrors stopOutcome's severity logic but sources its evidence from the
// orphaned Finals themselves (the run's own progress never absorbed them,
// which is what stranded it): an abort is the baseline, and any shard's real
// exit-code evidence that is more severe wins.
func (s *Service) FinalizeOrphaned(ctx context.Context, executionID, runID int64, orphans []ports.OrphanCompletion) error {
	outcomes := []taurus.Outcome{taurus.OutcomeAborted}
	for _, oc := range orphans {
		if oc.ExitCode == nil {
			outcomes = append(outcomes, taurus.OutcomeFromExitCode(-1))
			continue
		}
		outcomes = append(outcomes, taurus.OutcomeFromExitCode(*oc.ExitCode))
	}
	return s.finalize(ctx, executionID, runID, taurus.WorstOutcome(outcomes))
}

// stopOutcome is the outcome for a run Honryu is deliberately ending.
//
// Not derived from shard exit codes the way finalizeCompleted's is:
// taurus.OutcomeFromExitCode's own doc comment establishes that bzt's real
// exit codes cannot tell a deliberate stop from a crash, so Honryu's own
// certainty that it issued the stop -- OutcomeAborted -- is the baseline, not
// something inferred here. But a shard that had already finished naturally,
// with a real exit code, in the race between Stop and that shard's own last
// Final batch is real evidence and must not be silently discarded just
// because Stop reached the run first: if that evidence is more severe than an
// ordinary abort -- a criteria failure or an engine error -- it must win.
func (s *Service) stopOutcome(ctx context.Context, runID int64) (taurus.Outcome, error) {
	states, err := s.progress.ShardStates(ctx, runID)
	if err != nil {
		return "", err
	}
	outcomes := []taurus.Outcome{taurus.OutcomeAborted}
	for _, st := range states {
		if st.Finished && st.ExitCode != nil {
			outcomes = append(outcomes, taurus.OutcomeFromExitCode(*st.ExitCode))
		}
	}
	return taurus.WorstOutcome(outcomes), nil
}

// finalize builds a run's report from its accumulated measurements, stores it,
// discards the working state that produced it, and closes the run's marker.
//
// Discard runs whether this call's SaveReport actually wrote the report or
// found one already there: either way the working state this run produced is
// no longer needed, and running it unconditionally means a retry after a
// prior Discard failure still cleans up rather than short-circuiting on an
// early "already finalised" check the way a return-before-Discard would.
func (s *Service) finalize(ctx context.Context, executionID, runID int64, outcome taurus.Outcome) error {
	// A run already finalised has already notified (and its report is the
	// one that survived): only the first finalisation announces the
	// completion. Checked before SaveReport rather than after, because
	// SaveReport's first-write-wins is exactly the same race viewed from
	// the store -- the loser here is the loser there too. The window
	// between check and save can still let two truly concurrent
	// finalisations both announce; that duplicate is benign (a receiver
	// sees one run twice with the same run_id) and the alternative -- a
	// store round-trip that reports which write won -- is not worth a port
	// change for a best-effort notification.
	alreadyFinalised := false
	if _, err := s.reports.GetReport(ctx, runID); err == nil {
		alreadyFinalised = true
	}
	snapshot, err := s.progress.Snapshot(ctx, runID)
	if err != nil {
		return err
	}
	profile, err := s.repo.LoadProfileFor(ctx, executionID)
	if err != nil {
		return err
	}
	history, err := s.repo.RunHistory(ctx, runID)
	if err != nil {
		return err
	}
	exe, err := s.repo.GetExecution(ctx, executionID)
	if err != nil {
		return err
	}

	meta := report.Meta{
		ExecutionID: executionID,
		RunID:       runID,
		// The execution's own configured preference. Empty when it deferred to
		// the deployment's default engine instead of naming one -- that
		// resolution happens in lifecycleapp at deploy time and is not
		// currently threaded through to here, so a defaulted execution's
		// report still under-reports which engine actually ran.
		Engine: exe.Engine,
		// The load origin: the cluster this run generated load from (empty =
		// the deployment default), recorded on the report so a reader knows
		// where the numbers came from.
		Cluster: exe.Cluster,
		// The run's own correlation id, from its history row -- NOT the
		// execution's pending value, which by finalize time can already point at
		// a later deploy. Engine and Cluster above accept that imprecision; a
		// wrong correlation id would be load-bearing, deep-linking a reader into
		// the wrong run's traffic.
		CorrelationID: history.CorrelationID,
		StartedAt:     history.StartedTime,
		EndedAt:       s.now(),
		Requested:     requestedLoad(profile),
		Outcome:       outcome,
	}
	// An execution can bundle several scenarios under one run; ScenarioID is
	// informational and only unambiguous when there is exactly one. The label
	// breakdown the report already carries covers the multi-scenario case.
	if len(profile) == 1 {
		meta.ScenarioID = profile[0].ScenarioID
	}

	rep := report.Restore(snapshot).Report(meta)
	if err := s.reports.SaveReport(ctx, rep); err != nil {
		return err
	}
	// Announce the completion after the report is durable and before the
	// working state goes -- the notification's payload IS the stored
	// report. This is the one shared entry point every finalisation path
	// reaches (natural completion, Stop/Purge, the orphan sweep: they all
	// land in finalize), so a webhook-registered project is notified no
	// matter how its run ended. Best-effort by interface: RunCompleted has
	// no error to propagate onto the run.
	if !alreadyFinalised {
		s.notifier.RunCompleted(ctx, exe.ProjectID, rep)
	}
	if err := s.progress.Discard(ctx, runID); err != nil {
		return err
	}
	// Natural completion must close the run marker the same way teardown's
	// StopRun does: this is the one shared exit every finalisation path
	// reaches, and before it closed the marker, a run that finished on its
	// own left an open execution_run row nothing else ever clears -- teardown
	// never comes for a dead run, and the abandoned-run sweep skips runs that
	// already have reports -- so the execution reads running forever and
	// every later Trigger 409s on the corpse marker (phase 43's wedge).
	return s.closeRun(ctx, executionID, runID)
}

// closeRun clears the execution's run marker when -- and only when -- it
// still names the run being finalised. StopRun deletes by execution id, so
// closing unconditionally would tear down a NEW run's marker when the
// marker has already rotated (a redeploy raced the finalize and retriggered):
// the comparison against CurrentRun is what keeps a stale finalize from
// wedging the run that came after it. A marker already gone, as when
// teardown's own StopRun follows its Finalize, is simply left alone.
func (s *Service) closeRun(ctx context.Context, executionID, runID int64) error {
	current, ok, err := s.repo.CurrentRun(ctx, executionID)
	if err != nil {
		return err
	}
	if !ok || current != runID {
		return nil
	}
	return s.repo.StopRun(ctx, executionID)
}

// requestedLoad collapses an execution's load profile into the one figure a
// report compares achieved load against. An execution can bundle several
// scenarios, each with its own rate: concurrency sums exactly as usage
// accounting already collapses it (run.VirtualUsers), throughput sums since
// each scenario's target rate is additive, and duration takes the longest,
// since the run lasts as long as its longest scenario.
func requestedLoad(profile []loadprofile.Entry) report.Load {
	load := report.Load{Concurrency: run.VirtualUsers(loadprofile.Profile{Tests: profile})}
	for _, e := range profile {
		load.Throughput += float64(e.Throughput)
		if e.Duration > load.DurationSeconds {
			load.DurationSeconds = e.Duration
		}
	}
	return load
}
