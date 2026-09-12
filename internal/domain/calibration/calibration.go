// Package calibration models an engine-capacity search: a sequence of
// single-pod runs at increasing QPS, bracketing then bisecting toward
// whichever ceiling gives out first -- the engine's own, or the target's.
//
// Pure domain: arithmetic and state transitions only, deterministic, no I/O.
// A caller (internal/app/calibrationapp) drives the actual runs and feeds
// each one's classified outcome back through Next.
package calibration

import "errors"

// Spec validation errors. Callers compare with errors.Is.
var (
	ErrCriterionRequired = errors.New("calibration: a target-health criterion is required")
	ErrPodSizeRequired   = errors.New("calibration: pod CPU and memory are required")
	ErrSeedQPSInvalid    = errors.New("calibration: seed QPS must be positive")
	ErrMaxQPSInvalid     = errors.New("calibration: max QPS must exceed the seed QPS")
	ErrMaxStepsInvalid   = errors.New("calibration: max steps must be positive")
	ErrHoldInvalid       = errors.New("calibration: hold duration must be positive")
)

// Default search parameters, applied by Spec.WithDefaults for whichever
// fields the caller left zero.
const (
	DefaultSeedQPS     = 10.0
	DefaultMaxQPS      = 10000.0
	DefaultMaxSteps    = 20
	DefaultHoldSeconds = 30
	// BisectionToleranceQPS ends bisection once the bracket has narrowed to
	// within this many QPS -- tight enough to be a useful capacity number,
	// loose enough not to burn the step budget chasing false precision a
	// fan-out engine count would round away anyway.
	BisectionToleranceQPS = 1.0
)

// Spec configures one calibration search.
type Spec struct {
	// Criterion is the target-health expression (Phase 6's grammar, e.g.
	// "failures>5%") that trips target-saturation. Required: no calibrating
	// against a real target with no defined "too far".
	Criterion string
	// CPU and Memory pin the single pod's resource request for every step
	// (ports.DeploySpec's own string format, e.g. "1", "512Mi"). Required:
	// a capacity profile answers "QPS per pod of THIS size", so there is no
	// meaningful default size to fall back to.
	CPU    string
	Memory string

	// SeedQPS is the first rate tried.
	SeedQPS float64
	// MaxQPS is the absolute ceiling the search will never issue a step
	// above, however clean every step below it looked -- the safety bound
	// that keeps an unresponsive target from being doubled into
	// indefinitely.
	MaxQPS float64
	// MaxSteps bounds the whole search (bracketing and bisecting combined).
	MaxSteps int
	// HoldSeconds is how long each step holds steady state before its
	// report is read -- discards ramp/warmup so a step's classification
	// reflects settled behaviour, not a cold start.
	HoldSeconds int
}

// WithDefaults returns a copy of s with every zero-valued search parameter
// (not the two required fields, which have no sensible default) filled in.
func (s Spec) WithDefaults() Spec {
	if s.SeedQPS <= 0 {
		s.SeedQPS = DefaultSeedQPS
	}
	if s.MaxQPS <= 0 {
		s.MaxQPS = DefaultMaxQPS
	}
	if s.MaxSteps <= 0 {
		s.MaxSteps = DefaultMaxSteps
	}
	if s.HoldSeconds <= 0 {
		s.HoldSeconds = DefaultHoldSeconds
	}
	return s
}

// Validate checks s's invariants. Intended to run after WithDefaults --
// Validate does not itself default anything, so a caller that skips
// WithDefaults sees the same *Invalid errors a caller who explicitly zeroed
// a field would.
func (s Spec) Validate() error {
	switch {
	case s.Criterion == "":
		return ErrCriterionRequired
	case s.CPU == "" || s.Memory == "":
		return ErrPodSizeRequired
	case s.SeedQPS <= 0:
		return ErrSeedQPSInvalid
	case s.MaxQPS <= s.SeedQPS:
		return ErrMaxQPSInvalid
	case s.MaxSteps <= 0:
		return ErrMaxStepsInvalid
	case s.HoldSeconds <= 0:
		return ErrHoldInvalid
	}
	return nil
}

// Classification is what one step's settled report told the search.
type Classification string

const (
	// ClassificationEngineSaturated means the pod itself could not sustain
	// the requested rate, judged by the volume the hold implies
	// (report.Report.ShortOfVolume or EngineImpaired).
	ClassificationEngineSaturated Classification = "engine_saturated"
	// ClassificationTargetSaturated means the engine kept up, but the
	// target-health criterion tripped -- one pod already overloads the
	// target. A caller checks this only once engine-saturation has been
	// ruled out, so an engine's own failures (which also inflate a report's
	// total error rate) are never misread as target distress.
	ClassificationTargetSaturated Classification = "target_saturated"
	// ClassificationClean means neither side gave out: the engine achieved
	// the requested rate and the target stayed healthy.
	ClassificationClean Classification = "clean"
)

// SaturatedBy is a finished calibration's verdict on which side gave out
// first, and licenses what a reader may conclude from PerPodQPS.
type SaturatedBy string

const (
	// SaturatedByEngine means the search bracketed and bisected down to the
	// engine's own ceiling: PerPodQPS is confirmed capacity.
	SaturatedByEngine SaturatedBy = "engine"
	// SaturatedByTarget means a single pod already overloaded the target
	// before the engine ran out of headroom: PerPodQPS is a lower bound,
	// and fan-out does not apply -- more engines would only overload the
	// target harder, not confirm more capacity.
	SaturatedByTarget SaturatedBy = "target"
	// SaturatedByNeither means the search reached its safety ceiling
	// (MaxQPS or MaxSteps) with both sides still healthy: PerPodQPS is a
	// lower bound, and the engine's real ceiling was never found.
	SaturatedByNeither SaturatedBy = "neither"
)

// Phase is where a Job's search currently stands.
type Phase string

const (
	// PhasePending is a job that has not yet run its first step.
	PhasePending Phase = "pending"
	// PhaseBracketing is doubling the requested rate looking for a first
	// saturated step.
	PhaseBracketing Phase = "bracketing"
	// PhaseBisecting is narrowing toward the boundary between the last
	// confirmed-clean rate and the first confirmed-saturated one.
	PhaseBisecting Phase = "bisecting"
	// PhaseDone is a finished search: Job.Result is set.
	PhaseDone Phase = "done"
	// PhaseFailed is an operational failure (a step's run itself errored --
	// Deploy/Trigger/report never settled) rather than a search outcome.
	// Next never produces it; only the caller driving the runs does, when a
	// step cannot even be classified.
	PhaseFailed Phase = "failed"
)

// Step is one run's classified outcome, fed into Next.
type Step struct {
	RequestedQPS   float64
	AchievedQPS    float64
	Classification Classification
}

// Result is a finished calibration's terminal outcome.
type Result struct {
	SaturatedBy SaturatedBy
	// PerPodQPS is the confirmed or floor capacity -- see SaturatedBy's own
	// doc for which one this is.
	PerPodQPS float64
}

// Job is a calibration search's decision state -- the minimum Next needs to
// compute what to try next. The full step-by-step audit history is a
// persistence concern (a CalibrationJobRepository's own step log), not
// carried here: StepCount is only what Next needs to enforce Spec.MaxSteps.
type Job struct {
	Spec      Spec
	Phase     Phase
	StepCount int
	// BracketLoRequested/BracketLoAchieved is the highest requested QPS
	// confirmed clean so far, and what the pod achieved there -- the
	// candidate PerPodQPS if the search stops now. Zero until the first
	// clean step.
	BracketLoRequested float64
	BracketLoAchieved  float64
	// BracketHiRequested is the lowest requested QPS confirmed
	// engine-saturated so far. Zero (unset) until bracketing finds one.
	BracketHiRequested float64
	// Result is set once Phase is PhaseDone.
	Result *Result
}

// Action is what the caller must do next.
type Action struct {
	// Done is true once the job needs no further steps -- Job.Result is set
	// and NextRequestedQPS is meaningless.
	Done bool
	// NextRequestedQPS is the QPS the next step must be run at. Never
	// exceeds Spec.MaxQPS.
	NextRequestedQPS float64
}

// Start begins a fresh search: PhaseBracketing at the seed QPS.
func Start(spec Spec) (Job, Action) {
	job := Job{Spec: spec, Phase: PhaseBracketing}
	return job, Action{NextRequestedQPS: spec.SeedQPS}
}

// Next advances job by the classified outcome of the step just run -- the
// run made at whatever QPS the previous Action (from Start or Next)
// prescribed -- and returns the updated job and what to do next.
//
// step.Classification is checked in a fixed order the whole search relies
// on: target-saturation is terminal in any phase (one pod already overloads
// the target, wherever that was discovered); engine-saturation brackets and
// moves to bisecting; a clean step raises the floor and either doubles
// (bracketing) or narrows toward the midpoint (bisecting).
func Next(job Job, step Step) (Job, Action) {
	job.StepCount++

	switch step.Classification {
	case ClassificationTargetSaturated:
		job.Phase = PhaseDone
		job.Result = &Result{SaturatedBy: SaturatedByTarget, PerPodQPS: step.AchievedQPS}
		return job, Action{Done: true}

	case ClassificationEngineSaturated:
		if job.Phase == PhaseBracketing {
			job.Phase = PhaseBisecting
		}
		job.BracketHiRequested = step.RequestedQPS
		return job.bisectOrFinish()

	default: // ClassificationClean
		job.BracketLoRequested = step.RequestedQPS
		job.BracketLoAchieved = step.AchievedQPS
		if job.Phase == PhaseBracketing {
			return job.doubleOrFinish()
		}
		return job.bisectOrFinish()
	}
}

// doubleOrFinish is bracketing's own step: double the requested rate, unless
// doing so would exceed Spec.MaxQPS or the step budget -- in which case the
// search ends honestly unresolved (SaturatedByNeither) rather than issuing a
// step outside its own safety bounds.
func (job Job) doubleOrFinish() (Job, Action) {
	next := job.BracketLoRequested * 2
	if next > job.Spec.MaxQPS || job.StepCount >= job.Spec.MaxSteps {
		job.Phase = PhaseDone
		job.Result = &Result{SaturatedBy: SaturatedByNeither, PerPodQPS: job.BracketLoAchieved}
		return job, Action{Done: true}
	}
	return job, Action{NextRequestedQPS: next}
}

// bisectOrFinish is bisecting's own step: narrow toward the midpoint of the
// current bracket, unless the bracket is already within tolerance or the
// step budget is spent -- in which case the search ends confirmed
// engine-limited, at whatever floor bisection reached.
func (job Job) bisectOrFinish() (Job, Action) {
	width := job.BracketHiRequested - job.BracketLoRequested
	if width <= BisectionToleranceQPS || job.StepCount >= job.Spec.MaxSteps {
		job.Phase = PhaseDone
		job.Result = &Result{SaturatedBy: SaturatedByEngine, PerPodQPS: job.BracketLoAchieved}
		return job, Action{Done: true}
	}
	mid := job.BracketLoRequested + width/2
	return job, Action{NextRequestedQPS: mid}
}
