package calibrationapp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/run"
	"github.com/heridotlife/honryu/internal/ports"
)

// ErrScenarioNotConfigured means executionID's load profile does not name
// exactly one scenario -- a calibration execution is bound to its single
// target the ordinary way (an executionapp.StoreConfig upload) before its
// first job is ever triggered, and RunStep only ever rewrites that one
// entry's QPS-varying fields, never which scenario it targets.
var ErrScenarioNotConfigured = errors.New("calibrationapp: execution has no single scenario configured")

// ErrRequestedQPSInvalid means requestedQPS was not positive. Zero has a
// distinct, dangerous meaning to loadprofile.Entry.Throughput ("unlimited"),
// so a non-positive request is rejected rather than silently reinterpreted.
var ErrRequestedQPSInvalid = errors.New("calibrationapp: requested QPS must be positive")

// ErrStepRunNotStarted means Trigger returned without error but the
// execution has no current run to hold and measure -- should not happen in
// practice, but a step must not silently read an unrelated run's report.
var ErrStepRunNotStarted = errors.New("calibrationapp: triggered run is not reported as running")

const (
	// stepConcurrencyFloor is the least concurrency ever requested, so even
	// the search's seed QPS keeps enough virtual users to actually reach it.
	// It is also what the first attempt of a step runs at (see
	// stepConcurrency): a light probe, deliberately not sized to the rate.
	stepConcurrencyFloor = 20
	// concurrencyHeadroom multiplies the Little's-Law minimum
	// (requestedQPS * observedLatency) when a step is re-sized from a
	// measured response time -- enough slack to absorb latency jitter
	// without the gross over-provisioning that makes bzt's Constant
	// Throughput Timer undershoot with mostly-idle threads (measured live:
	// 400 VUs undershot 200 QPS by ~13%, 20 VUs held it within 1%).
	concurrencyHeadroom = 3.0
	// stepRampupSeconds is the warmup a step's run spends ramping to its
	// full concurrency before the steady-state hold begins.
	stepRampupSeconds = 5
	// triggerReadyPollInterval is how often RunStep retries Trigger while
	// the pod Deploy just created is still starting up -- a real cluster's
	// pod takes real time to schedule, pull its image, and start (unlike
	// the fake scheduler, which reports every deploy ready instantly, so
	// this race never surfaces against it).
	triggerReadyPollInterval = 2 * time.Second
	// triggerReadyTimeout bounds how long RunStep waits for a just-deployed
	// pod to become triggerable before giving up.
	triggerReadyTimeout = 2 * time.Minute
	// stepReportPollInterval is how often RunStep checks whether the run's
	// report has settled while waiting out the hold.
	stepReportPollInterval = 2 * time.Second
	// stepReportTimeout bounds how long RunStep waits for a run's report to
	// settle before giving up and stopping it anyway. A fixed sleep sized to
	// the compiled ramp-up+hold-for was tried first and repeatedly cut runs
	// off before their engine ever produced a Final batch -- task 85's live
	// verification saw the very same step's real pod-schedule-to-first-
	// sample latency vary from ~9s to ~40s across back-to-back attempts on
	// one real (sometimes-busy) node, nothing the fake scheduler's
	// instantly-sampling pods ever surface. Polling for the report itself,
	// which metricsapp only produces once every shard reports finished,
	// removes the guesswork; this timeout only bounds a genuinely stuck run
	// (crashed engine, no Final batch ever sent).
	stepReportTimeout = 5 * time.Minute
)

// RunnerRepo is the persistence a step needs beyond driving the run itself:
// reading and rewriting the calibration execution's own load profile, and
// finding the run a triggered step produced.
type RunnerRepo interface {
	LoadProfileFor(ctx context.Context, executionID int64) ([]loadprofile.Entry, error)
	StoreLoadProfile(ctx context.Context, executionID int64, csvSplit bool, entries []loadprofile.Entry) error
	CurrentRun(ctx context.Context, executionID int64) (runID int64, ok bool, err error)
}

// Lifecycle is the ordinary test-lifecycle mechanism a step drives -- reused
// rather than reinvented, so a calibration step inherits campaign-freeze
// gating and engine-equivalents quota exactly as any other trigger does.
// *lifecycleapp.Service satisfies this directly.
type Lifecycle interface {
	Deploy(ctx context.Context, executionID int64) error
	Trigger(ctx context.Context, executionID int64) error
	Stop(ctx context.Context, executionID int64) error
}

// ReportReader reads back a run's settled report once it has ended.
// ports.ReportStore satisfies this directly.
type ReportReader interface {
	GetReport(ctx context.Context, runID int64) (report.Report, error)
}

// StepRunner drives a single calibration step: a real, settled, one-pod run
// at a chosen QPS. Each step is its own genuine run through the ordinary
// lifecycle (not a synthetic estimate) so its settled report's
// requested-vs-achieved gap and target-health signals are trustworthy.
type StepRunner struct {
	repo      RunnerRepo
	lifecycle Lifecycle
	reports   ReportReader
	sleep     func(time.Duration)
	now       func() time.Time
}

// NewStepRunner wires a step runner. sleep defaults to time.Sleep and now to
// time.Now; override both with WithSleep and WithClock for deterministic
// tests.
func NewStepRunner(repo RunnerRepo, lifecycle Lifecycle, reports ReportReader) *StepRunner {
	return &StepRunner{repo: repo, lifecycle: lifecycle, reports: reports, sleep: time.Sleep, now: time.Now}
}

// WithSleep overrides the hold mechanism. Returns the receiver for chaining.
func (r *StepRunner) WithSleep(sleep func(time.Duration)) *StepRunner {
	if sleep != nil {
		r.sleep = sleep
	}
	return r
}

// WithClock overrides the clock RunStep's bounded polling loops
// (triggerWhenReady, awaitReport) measure their deadlines against. A fake
// sleep that does not actually block must be paired with a fake clock that
// advances when it fires -- otherwise a deadline measured against the real
// wall clock still takes the real timeout duration to reach, spinning the
// loop as fast as the CPU allows for however many iterations that takes.
// Returns the receiver for chaining.
func (r *StepRunner) WithClock(now func() time.Time) *StepRunner {
	if now != nil {
		r.now = now
	}
	return r
}

// RunStep rewrites executionID's load profile to a single pinned-size pod
// running at requestedQPS, deploys and triggers it, holds through the
// steady-state window, stops it, and returns the settled report.
//
// latencyHintSec, when positive, is a response time measured for this
// scenario on an earlier attempt, from which the virtual-user count is sized
// by Little's Law; zero means none is known yet and a generous default is
// used instead (see stepConcurrency).
//
// executionID's pod size is not RunStep's concern -- it is pinned on the
// execution itself (execution.CPU/Memory, set once at calibrationapp.Create)
// and Deploy already reads it from there for every execution, calibration or
// not.
func (r *StepRunner) RunStep(ctx context.Context, executionID int64, requestedQPS float64, holdSeconds int, latencyHintSec float64) (report.Report, error) {
	if requestedQPS <= 0 {
		return report.Report{}, fmt.Errorf("%w: %g", ErrRequestedQPSInvalid, requestedQPS)
	}

	entries, err := r.repo.LoadProfileFor(ctx, executionID)
	if err != nil {
		return report.Report{}, err
	}
	if len(entries) != 1 {
		return report.Report{}, fmt.Errorf("%w: execution %d has %d scenarios, want exactly 1", ErrScenarioNotConfigured, executionID, len(entries))
	}
	base := entries[0]

	step := loadprofile.Entry{
		Name:        base.Name,
		ScenarioID:  base.ScenarioID,
		Engines:     1,
		Concurrency: stepConcurrency(requestedQPS, latencyHintSec),
		Rampup:      stepRampupSeconds,
		Duration:    holdSeconds,
		Throughput:  int(math.Ceil(requestedQPS)),
	}
	if err := step.Validate(); err != nil {
		return report.Report{}, err
	}
	// CSVSplit never matters here: a single-engine step never splits data
	// across engines.
	if err := r.repo.StoreLoadProfile(ctx, executionID, false, []loadprofile.Entry{step}); err != nil {
		return report.Report{}, err
	}

	if err := r.lifecycle.Deploy(ctx, executionID); err != nil {
		return report.Report{}, err
	}
	if err := r.triggerWhenReady(ctx, executionID); err != nil {
		return report.Report{}, err
	}
	runID, running, err := r.repo.CurrentRun(ctx, executionID)
	if err != nil {
		return report.Report{}, err
	}
	if !running {
		return report.Report{}, fmt.Errorf("%w: execution %d", ErrStepRunNotStarted, executionID)
	}

	rpt, reportErr := r.awaitReport(ctx, runID)

	// Since phase43, the run marker closes the moment the report settles
	// (metricsapp.finalize), so awaitReport returning may mean the run is
	// already closed. Stop is still attempted -- engines need teardown and
	// the not-yet-settled path still depends on it -- but ErrNotRunning is
	// exactly natural completion having beaten us to it.
	if err := r.lifecycle.Stop(ctx, executionID); err != nil && !errors.Is(err, run.ErrNotRunning) {
		return report.Report{}, err
	}
	if reportErr != nil {
		return report.Report{}, reportErr
	}
	return rpt, nil
}

// awaitReport polls for runID's settled report -- produced by metricsapp
// automatically once every shard the run's profile called for has reported
// itself finished, not on any timer RunStep itself controls. Bounded by
// stepReportTimeout so a run whose engine never finishes still returns an
// error instead of hanging forever.
func (r *StepRunner) awaitReport(ctx context.Context, runID int64) (report.Report, error) {
	deadline := r.now().Add(stepReportTimeout)
	for {
		rpt, err := r.reports.GetReport(ctx, runID)
		if err == nil {
			return rpt, nil
		}
		if !errors.Is(err, ports.ErrNotFound) {
			return report.Report{}, err
		}
		if r.now().After(deadline) {
			return report.Report{}, fmt.Errorf("calibrationapp: run %d report never settled: %w", runID, err)
		}
		r.sleep(stepReportPollInterval)
	}
}

// triggerWhenReady retries Trigger while the pod Deploy just created is
// still starting up (run.ErrNotDeployed while its StatefulSet has not yet
// reported any pods, run.ErrEnginesNotReady while they exist but are not
// yet ready) -- Deploy returning success only means the StatefulSet was
// created, not that its pod is scheduled, image-pulled, and running. Any
// other error, or the timeout itself, is returned immediately.
func (r *StepRunner) triggerWhenReady(ctx context.Context, executionID int64) error {
	deadline := r.now().Add(triggerReadyTimeout)
	for {
		err := r.lifecycle.Trigger(ctx, executionID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, run.ErrNotDeployed) && !errors.Is(err, run.ErrEnginesNotReady) {
			return err
		}
		if r.now().After(deadline) {
			return fmt.Errorf("calibrationapp: execution %d never became triggerable: %w", executionID, err)
		}
		r.sleep(triggerReadyPollInterval)
	}
}

// stepConcurrency returns the virtual-user count a step at requestedQPS
// should run with.
//
// With a measured response time (latencyHintSec > 0) it sizes by Little's
// Law -- requestedQPS * latency is the minimum VUs to sustain the rate, times
// concurrencyHeadroom for jitter -- so the thread count matches what the load
// actually needs. This matters because bzt's Constant Throughput Timer
// undershoots the target rate when given far more threads than the load needs
// (they sit mostly idle and pace poorly): against a 2ms target, sizing 2
// VUs/QPS put ~1000x too many threads in play and undershot the requested
// rate by ~13%, enough to misread an un-saturated engine as saturated.
//
// Without a measurement yet (latencyHintSec == 0, the first attempt of a
// step) it falls back to the generous stepConcurrencyPerQPS default -- see
// that constant for why over-provisioning here is the safe direction, since
// the RT-informed retry corrects it.
//
// Either way the result is floored at stepConcurrencyFloor so a low-QPS step
// still has enough users to reach its rate.
func stepConcurrency(requestedQPS, latencyHintSec float64) int {
	if latencyHintSec <= 0 {
		// First attempt: a light probe at the floor, just enough to measure a
		// representative response time cheaply. Deliberately NOT sized to the
		// requested rate. Sizing an uninformed first guess to the rate is what
		// caused two live failures: a high rate opened a storm of connections
		// that tripped the target's connection limit (2000 VUs at 4000 QPS
		// made httpbin's proxy close every connection), and at higher rates it
		// would exhaust the pod's memory -- both before the response time that
		// would size it correctly was even known. If the probe undershoots the
		// rate the step is retried, and the retry sizes threads accurately from
		// the probe's measured latency.
		return stepConcurrencyFloor
	}
	// Retry: Little's Law from the measured response time, sized to what the
	// load actually needs. No ceiling -- a genuinely slow target legitimately
	// needs many threads, and this is sized from a real measurement, not a
	// guess.
	c := int(math.Ceil(requestedQPS * latencyHintSec * concurrencyHeadroom))
	if c < stepConcurrencyFloor {
		return stepConcurrencyFloor
	}
	return c
}
