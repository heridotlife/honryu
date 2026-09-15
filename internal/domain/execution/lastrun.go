package execution

import (
	"time"

	"github.com/heridotlife/honryu/internal/domain/taurus"
)

// LastRun is a scenario's most recent run, as the scenario list surfaces it:
// the newest execution bound to the scenario (created_time desc, id desc --
// the ListExecutionsByScenario order) paired with the verdict of that
// execution's newest report (started_at desc, run_id desc -- the
// ListReports order). It is a read model joined across aggregates for one
// purpose -- a scenario's "how did its latest run go" cell -- so it carries
// identity and verdict only; every measurement a report owns stays on
// report.Report.
type LastRun struct {
	// ExecutionID is the newest execution the scenario is bound to.
	ExecutionID int64
	// Outcome is that execution's newest report's verdict.
	Outcome taurus.Outcome
	// StartedAt is when that run started -- the report's clock, not the
	// execution's creation time.
	StartedAt time.Time
}
