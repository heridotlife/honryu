package ports

import (
	"context"
	"errors"
	"time"
)

// ErrScenarioVersionNotFound is the one way a version lookup can fail to
// find anything: the scenario has no such version. The store speaks
// ports.ErrNotFound for unknown scenarios like every other store; this
// sentinel exists so a handler can tell "unknown scenario" (its usual 404)
// from "unknown version" without re-deriving it. It wraps ErrNotFound, so
// errors.Is(err, ports.ErrNotFound) still matches.
var ErrScenarioVersionNotFound = errors.New("ports: scenario version not found")

// ScenarioSnapshot is the full persisted shape of a scenario at one point
// in time: the scenario row's own fields plus its file records (test file
// and data — names only; blob contents are not versioned) and its stored
// requests fragment ("" when none was ever uploaded).
//
// The zero values are honest: an empty name means the scenario had no name,
// an empty Data means it had no data files. Data is always non-nil when the
// snapshot was built by the application, so it marshals as [] rather than
// null; the field names are the stable JSON contract the 0070 backfill's
// JSON_OBJECT spells identically.
type ScenarioSnapshot struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	ProjectID    int64     `json:"project_id"`
	Kind         string    `json:"kind"`
	Engine       string    `json:"engine"`
	TenantID     *int64    `json:"tenant_id"`
	CreatedBy    string    `json:"created_by"`
	UpdatedBy    string    `json:"updated_by"`
	CreatedTime  time.Time `json:"created_time"`
	IsTemplate   bool      `json:"is_template"`
	TemplateName string    `json:"template_name"`
	TestFile     string    `json:"test_file"`
	Data         []string  `json:"data"`
	Requests     string    `json:"requests"`
}

// ScenarioVersionMeta describes one stored version: what it is and when it
// was captured, snapshot excluded. CreatedBy is the principal that produced
// the change — nil when none was reachable (the legacy no-auth path); the
// API renders it as null, never a fabricated name.
type ScenarioVersionMeta struct {
	ID          int64
	ScenarioID  int64
	Version     int
	CreatedTime time.Time
	CreatedBy   *string
}

// ScenarioVersion is one stored version: its metadata plus the full
// snapshot it captured. ListScenarioVersions returns the metas only (the
// snapshots stay in the store until asked for); ScenarioVersion fills
// Snapshot in.
type ScenarioVersion struct {
	ScenarioVersionMeta
	Snapshot ScenarioSnapshot
}

// ScenarioVersionStore persists a scenario's edit history: an append-only
// sequence of full-scenario snapshots, one per captured write. Version
// numbers are per scenario, assigned by the store as max(existing)+1 under
// a lock, with the (scenario_id, version) unique key as the storage-level
// backstop. No method rewrites or removes a stored row: history is
// append-only by contract, and restoring captures the pre-restore state as
// a new version rather than mutating anything.
type ScenarioVersionStore interface {
	// AppendScenarioVersion records snapshot as scenarioID's next version
	// and returns the number it was assigned. An empty createdBy is stored
	// as NULL (no actor was reachable). The scenario row must exist — the
	// caller (the scenario use-cases) has it in hand.
	AppendScenarioVersion(ctx context.Context, scenarioID int64, snapshot ScenarioSnapshot, createdBy string) (int, error)
	// ListScenarioVersions returns the scenario's versions, newest first.
	// Always non-nil; empty when the scenario has none (it predates the
	// feature and 0070 has not run, or it was created without a version
	// store wired).
	ListScenarioVersions(ctx context.Context, scenarioID int64) ([]ScenarioVersionMeta, error)
	// ScenarioVersion returns one version, snapshot included:
	// ErrScenarioVersionNotFound when the scenario has no such version.
	ScenarioVersion(ctx context.Context, scenarioID int64, version int) (ScenarioVersion, error)
}
