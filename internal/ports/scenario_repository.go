package ports

import (
	"context"

	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
)

// ScenarioFiles is the set of files attached to a scenario: one optional JMX test file
// and any number of data files (e.g. CSV). Names only — storage keys/URLs are
// derived by the application from a naming convention.
type ScenarioFiles struct {
	TestFile string   // "" when no JMX has been uploaded
	Data     []string // non-JMX data files
}

// ScenarioRepository persists and retrieves Scenario aggregates and their file records.
type ScenarioRepository interface {
	CreateScenario(ctx context.Context, p scenario.Scenario) (int64, error)
	GetScenario(ctx context.Context, id int64) (scenario.Scenario, error)
	ListScenariosByProject(ctx context.Context, projectID int64) ([]scenario.Scenario, error)
	DeleteScenario(ctx context.Context, id int64) error

	// AddScenarioFile records a file for the scenario. isTest selects the JMX test-file
	// slot (one per scenario) vs a data file. Returns ErrFileExists on duplicates.
	AddScenarioFile(ctx context.Context, scenarioID int64, filename string, isTest bool) error
	// ScenarioFilesFor returns the scenario's recorded files.
	ScenarioFilesFor(ctx context.Context, scenarioID int64) (ScenarioFiles, error)
	// DeleteScenarioFile removes a file record for the scenario.
	DeleteScenarioFile(ctx context.Context, scenarioID int64, filename string, isTest bool) error

	// SetScenarioKind records how a scenario's workload is expressed, and the
	// engine it is pinned to when native. Uploading an engine-native artefact is
	// what decides this, so it changes after creation.
	SetScenarioKind(ctx context.Context, scenarioID int64, kind scenario.Kind, engine taurus.Executor) error

	// SetScenarioRequests stores a portable scenario's declarative workload, as
	// the raw bytes of a Taurus `scenarios:` YAML fragment the caller has
	// already parsed and validated. Raw, not a parsed struct: the encoding is
	// the caller's concern, and storing exactly what was uploaded avoids any
	// lossy round-trip through re-marshaling.
	SetScenarioRequests(ctx context.Context, scenarioID int64, raw []byte) error
	// GetScenarioRequests returns a portable scenario's stored fragment.
	// ErrNotFound means nothing has been uploaded yet.
	GetScenarioRequests(ctx context.Context, scenarioID int64) ([]byte, error)

	// ScenarioInUse reports whether the scenario is referenced by any execution's
	// execution configuration.
	ScenarioInUse(ctx context.Context, scenarioID int64) (bool, error)

	// ListTemplates returns every scenario flagged as a template, in id order.
	// Templates are global (no project, no tenant), so the catalog takes no
	// scoping arguments.
	ListTemplates(ctx context.Context) ([]scenario.Scenario, error)
}
