package ports

import (
	"context"
	"time"

	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
)

// ExecutionRepository persists Execution aggregates, their data files, and
// their execution configuration.
type ExecutionRepository interface {
	CreateExecution(ctx context.Context, c execution.Execution) (int64, error)
	GetExecution(ctx context.Context, id int64) (execution.Execution, error)
	ListExecutionsByProject(ctx context.Context, projectID int64) ([]execution.Execution, error)
	DeleteExecution(ctx context.Context, id int64) error

	// ExecutionsWithActiveRunOnCluster returns the ids of executions bound to
	// cluster (execution.Cluster) that currently have an active run, ordered by
	// id. It backs the cluster-registry delete guard: a cluster is not
	// removable while it is generating load. An empty result means none.
	ExecutionsWithActiveRunOnCluster(ctx context.Context, cluster string) ([]int64, error)

	// ListExecutionsByProjects returns every execution belonging to any of
	// projectIDs, newest first by creation time, so an operator's execution
	// list reads like a history. An empty projectIDs slice returns an empty
	// result and no error -- the same convention ListProjectsByOwners keeps.
	ListExecutionsByProjects(ctx context.Context, projectIDs []int64) ([]execution.Execution, error)

	AddExecutionFile(ctx context.Context, executionID int64, filename string) error
	ExecutionFilesFor(ctx context.Context, executionID int64) ([]string, error)
	DeleteExecutionFile(ctx context.Context, executionID int64, filename string) error

	// StoreLoadProfile replaces the execution's load profile entries with
	// entries and updates its csv_split flag, atomically.
	StoreLoadProfile(ctx context.Context, executionID int64, csvSplit bool, entries []loadprofile.Entry) error
	// LoadProfileFor returns the execution's current load profile entries.
	LoadProfileFor(ctx context.Context, executionID int64) ([]loadprofile.Entry, error)

	// SetExecutionCriteria replaces the execution's configured Taurus
	// pass/fail criteria with criteria, atomically, in the given order.
	// Empty criteria clears them.
	SetExecutionCriteria(ctx context.Context, executionID int64, criteria []string) error
	// CriteriaFor returns the execution's currently configured criteria, in
	// the order they were set. Never nil.
	CriteriaFor(ctx context.Context, executionID int64) ([]string, error)

	// SetPendingCorrelationID records the trace id a Deploy minted for the run
	// it precedes, overwriting any earlier one (last deploy wins). It is
	// pending state, not part of the Execution aggregate: Trigger consumes it
	// when StartRun stamps it onto the run.
	SetPendingCorrelationID(ctx context.Context, executionID int64, correlationID string) error
	// PendingCorrelationID returns the id the latest Deploy minted. Empty when
	// no deploy has happened since the phase that introduced it.
	PendingCorrelationID(ctx context.Context, executionID int64) (string, error)

	// TouchActivity stamps the execution's last-activity clock to now: every
	// lifecycle event that proves its engines are in use (a deploy, a run
	// start, a run completion -- natural finalize or a stop -- a calibration
	// step) restamps it, resetting the idle TTL the engine reaper measures.
	// Touching an unknown execution is not an error -- a stamp is advisory
	// bookkeeping, and the reaper's own guards are what protect a live run,
	// so a racing delete simply drops the stamp.
	TouchActivity(ctx context.Context, executionID int64) error
	// LastActivity returns the execution's last-activity stamp; ok is false
	// when there is nothing to read -- no row, or a row never stamped (NULL:
	// deployed before the column existed). A caller that needs a clock
	// anyway falls back to another source, such as the engine's own deploy
	// time. err is reserved for real read failures.
	LastActivity(ctx context.Context, executionID int64) (last time.Time, ok bool, err error)

	// StoreExecutionConfig replaces the execution's load profile and
	// configured criteria together, in one transaction -- unlike calling
	// StoreLoadProfile and SetExecutionCriteria separately, a failure here
	// can never leave the two halves of one config upload out of sync with
	// each other.
	StoreExecutionConfig(ctx context.Context, executionID int64, csvSplit bool, entries []loadprofile.Entry, criteria []string) error
}
