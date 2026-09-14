package ports

import (
	"context"
	"time"

	"github.com/heridotlife/honryu/internal/domain/calibration"
)

// CalibrationJob is the persisted runtime state of one calibration search --
// calibration.Job's own decision-state (Phase, StepCount, bracket) plus the
// identity and bookkeeping a repository owns. Spec is deliberately not
// carried here: a job's Spec lives with the owning execution (its own
// repository concern), not duplicated per job.
type CalibrationJob struct {
	ID          int64
	ExecutionID int64
	// ScenarioID is the scenario the search calibrates -- the execution's
	// single bound load-profile entry, resolved at trigger time and carried
	// on the row so the scenario-first API never re-derives it. 0 means
	// unknown: the column is NULL for legacy rows the 0065 backfill could
	// not resolve (an execution that lost its scenario binding).
	ScenarioID int64
	Phase      calibration.Phase
	StepCount  int
	// BracketLoRequested/BracketLoAchieved/BracketHiRequested mirror
	// calibration.Job's own fields exactly.
	BracketLoRequested float64
	BracketLoAchieved  float64
	BracketHiRequested float64
	// NextRequestedQPS is the QPS the job's next step must run at --
	// calibration.Action.NextRequestedQPS, persisted so a controller
	// resuming a claimed job does not need to replay the whole step
	// history to rediscover it.
	NextRequestedQPS float64
	// Result is set once Phase is PhaseDone.
	Result *calibration.Result
	// FailureReason is set once Phase is PhaseFailed -- an operational
	// failure (a step's run itself errored), or a finished search refused
	// as environment-impaired because it recorded no clean step at all.
	FailureReason string
	CreatedTime   time.Time
}

// CalibrationJobRepository persists calibration searches and their
// step-by-step history, and arbitrates which controller replica advances a
// given job next.
type CalibrationJobRepository interface {
	// CreateCalibrationJob persists a fresh job (PhasePending) for
	// executionID, recording scenarioID as the scenario the search
	// calibrates (0 stores NULL -- unknown), and returns its assigned ID.
	CreateCalibrationJob(ctx context.Context, executionID, scenarioID int64) (int64, error)
	// GetCalibrationJob returns the job with id, or ErrNotFound.
	GetCalibrationJob(ctx context.Context, id int64) (CalibrationJob, error)
	// ListCalibrationJobsByExecution returns every job ever run for
	// executionID, most recent first.
	ListCalibrationJobsByExecution(ctx context.Context, executionID int64) ([]CalibrationJob, error)

	// ClaimNextStep locks and returns one non-terminal (not Done or Failed)
	// job whose claim has expired -- claimed_at is unset, or older than
	// now.Add(-leaseFor) -- or found=false if none is due right now.
	//
	// The claim alone does not advance the job's search state (see
	// RecordStep for that); it only marks the job as being worked so a
	// second concurrent caller cannot claim the same job while the first is
	// still driving its step's run. More than one controller replica (a
	// dedicated cmd/calibrator alongside cmd/scheduler optionally hosting
	// the same loop) may call this concurrently; exactly one ever claims a
	// given due job.
	ClaimNextStep(ctx context.Context, now time.Time, leaseFor time.Duration) (job CalibrationJob, found bool, err error)

	// RecordStep appends step to jobID's history and replaces its
	// persisted state with updated (the result of feeding step through
	// calibration.Next), clearing the claim so a still-in-progress job
	// becomes claimable again for its next step.
	RecordStep(ctx context.Context, jobID int64, step calibration.Step, updated CalibrationJob) error
	// StepsFor returns every step recorded for jobID, in the order taken.
	StepsFor(ctx context.Context, jobID int64) ([]calibration.Step, error)

	// MarkFailed ends jobID with PhaseFailed and reason -- an operational
	// failure (the step's own run errored, never even producing a
	// classification, or a finished search judged environment-impaired),
	// distinct from any search outcome Next can reach.
	// Clears the claim; a failed job is never claimed again.
	MarkFailed(ctx context.Context, jobID int64, reason string) error
}

// CalibrationBounds are the numeric search parameters
// (calibration.Spec's own SeedQPS/MaxQPS/MaxSteps/HoldSeconds) configured
// for a CalibrateEngine execution -- the only parts of Spec that don't
// already have a home on the execution itself (pod size) or in
// execution_criteria (the target-health criterion).
type CalibrationBounds struct {
	SeedQPS     float64
	MaxQPS      float64
	MaxSteps    int
	HoldSeconds int
}
