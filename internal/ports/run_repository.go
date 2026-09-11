package ports

import (
	"context"
	"errors"
	"time"
)

// ErrRunActive is returned by StartRun when an execution already has an active
// run. ErrNoActiveRun is returned when an operation needs one and none exists.
var (
	ErrRunActive   = errors.New("ports: a run is already active for this execution")
	ErrNoActiveRun = errors.New("ports: no active run for this execution")
)

// RunRecord is a row of execution run history.
type RunRecord struct {
	RunID       int64
	ExecutionID int64
	StartedTime time.Time
	EndTime     *time.Time
	// CorrelationID is the trace id the run's load carried in its
	// traceparent/baggage headers: the id Deploy minted and StartRun stamped
	// here. Empty for runs started before that existed.
	CorrelationID string
}

// RunningScenario marks a scenario currently executing within an execution.
type RunningScenario struct {
	ExecutionID int64
	ScenarioID  int64
	StartedTime time.Time
}

// OpenRun is one execution's currently-active run, as a reconciliation pass
// sees it: the run row exists (StartRun happened, no StopRun/teardown has).
type OpenRun struct {
	ExecutionID int64
	RunID       int64
	StartedTime time.Time
}

// RunRepository persists the lifecycle state of runs: the single active run per
// execution, its history, and which scenarios are currently executing. It reuses
// the execution_run, execution_run_history, and running_scenario tables.
type RunRepository interface {
	// StartRun creates the active run for an execution and opens a history row,
	// returning the new run id. correlationID is the trace id the deploy that
	// preceded this trigger minted (empty when it had none), stamped onto the
	// history row so the run keeps its own id even after the execution is
	// re-deployed. Returns ErrRunActive if a run already exists.
	StartRun(ctx context.Context, executionID int64, correlationID string) (int64, error)
	// CurrentRun returns the active run id for an execution; ok is false when
	// there is none.
	CurrentRun(ctx context.Context, executionID int64) (runID int64, ok bool, err error)
	// OpenRuns lists every execution's active run across this deployment
	// context, with the run's start time. Reconciliation scans it for runs
	// whose engines are already gone; normal operation never needs it.
	OpenRuns(ctx context.Context) ([]OpenRun, error)
	// StopRun clears the active run and stamps end_time on its history row.
	// Stopping an execution with no active run is not an error.
	StopRun(ctx context.Context, executionID int64) error
	// RunHistory returns a run's history record, or ErrNotFound. It is how a
	// report learns when its run started, since nothing else keeps that once the
	// run itself has been superseded or stopped.
	RunHistory(ctx context.Context, runID int64) (RunRecord, error)
	// MarkScenarioRunning records that a scenario is executing (idempotent).
	MarkScenarioRunning(ctx context.Context, executionID, scenarioID int64) error
	// ClearScenarioRunning removes a scenario's running marker (idempotent).
	ClearScenarioRunning(ctx context.Context, executionID, scenarioID int64) error
	// RunningScenarios lists every running scenario in this deployment context, used to
	// resume tracking after a controller restart.
	RunningScenarios(ctx context.Context) ([]RunningScenario, error)
	// RunningScenariosByExecution lists running scenarios for one execution.
	RunningScenariosByExecution(ctx context.Context, executionID int64) ([]RunningScenario, error)
	// LastRun returns the most recently started run for an execution from its
	// history, or ErrNotFound when the execution has never run. Natural
	// completion (phase 43) closes the active marker, so this is how a late
	// purge learns which run's logs it still owes a capture for.
	LastRun(ctx context.Context, executionID int64) (RunRecord, error)
}
