// Package execution holds the Execution aggregate: the runnable unit that
// groups scenarios to run together against a Project. It is the Taurus
// "execution" concept. Pure domain, no I/O.
package execution

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/heridotlife/honryu/internal/domain/taurus"
)

// MaxNameLen mirrors the persisted schema (execution.name VARCHAR(100)).
const MaxNameLen = 100

// Validation errors. Callers compare with errors.Is.
var (
	ErrNameRequired    = errors.New("execution: name is required")
	ErrNameTooLong     = errors.New("execution: name exceeds maximum length")
	ErrProjectRequired = errors.New("execution: a valid project id is required")
	ErrEngineUnknown   = errors.New("execution: unknown engine")
	ErrKindUnknown     = errors.New("execution: unknown kind")
	// ErrFanOutTargetDuplicate means the fan-out target list names the same
	// cluster twice. Duplicating a target would double-deploy its full shard
	// set under one name -- never what the caller meant -- so it is refused
	// rather than silently deduplicated.
	ErrFanOutTargetDuplicate = errors.New("execution: duplicate fan-out target")
)

// Kind distinguishes what an execution is for.
//
// Empty is treated as KindNormal (the same "empty is the sentinel default"
// convention Engine already uses): an execution row persisted before Kind
// existed decodes to the Go zero value and must keep behaving exactly as it
// always did, with no backfill migration required. New always assigns the
// canonical KindNormal explicitly, so only pre-existing rows ever carry "".
type Kind string

const (
	// KindNormal is an ordinary execution: what every execution was before
	// Kind existed, and what New assigns by default.
	KindNormal Kind = "normal"
	// KindCalibrateEngine is an engine-capacity search (Phase 7): it drives
	// a sequence of single-pod runs at increasing QPS to find where the
	// engine or the target saturates, rather than producing a verdict of
	// its own.
	KindCalibrateEngine Kind = "calibrate_engine"
)

// knownKinds records the kinds Honryu recognises, empty excluded -- mirrors
// taurus.Executor's declarativeSupport map shape.
var knownKinds = map[Kind]bool{
	KindNormal:          true,
	KindCalibrateEngine: true,
}

// Known reports whether k is a Kind Honryu recognises.
func (k Kind) Known() bool {
	return knownKinds[k]
}

// Execution is a group of scenarios executed together against a Project.
type Execution struct {
	ID        int64
	Name      string
	ProjectID int64
	// Engine is the load-test engine this execution runs on. Empty means the
	// deployment's configured default, so an execution created before an
	// operator offered a choice keeps working.
	Engine taurus.Executor
	// Kind distinguishes an ordinary execution from a CalibrateEngine one.
	// See Kind's own doc for the empty-means-Normal convention.
	Kind Kind
	// CPU and Memory pin every pod this execution deploys to a specific
	// resource request/limit (ports.DeploySpec's own string format, e.g.
	// "1", "512Mi"). Empty means the cluster's default size, exactly as
	// before these fields existed -- only a CalibrateEngine execution pins
	// them, since a capacity profile answers "QPS per pod of THIS size".
	CPU, Memory string
	// Cluster names the registered cluster this execution generates load from
	// (a clusterregistry.Cluster name / ports.ClusterRef). Empty means the
	// deployment's default cluster -- the same "empty is the default"
	// convention Engine uses -- so every execution created before multi-cluster
	// existed resolves to the control plane's own cluster and behaves as before.
	// A fan-out execution ignores it: its load origin is FanOutTargets.
	Cluster string
	// FanOutTargets names the registered clusters a fan-out execution runs
	// its FULL load profile on, simultaneously -- the "run everywhere"
	// primitive (phase 88): N clusters × S shards = N×S pods, each cluster
	// running the complete shard set rather than a slice of it. nil/empty
	// means an ordinary single-cluster execution; a non-empty list makes the
	// execution fan-out, and Cluster is then meaningless. Names are
	// clusterregistry.Cluster names; the empty string never appears
	// (NormalizeFanOutTargets drops it), because the implicit default cluster
	// is chosen by leaving fan-out unset, not by naming it.
	FanOutTargets []string
	CSVSplit      bool
	TenantID      *int64
	CreatedBy     string
	UpdatedBy     string
	CreatedTime   time.Time
}

// New constructs and validates a Execution. Name is trimmed; ID and
// CreatedTime are assigned by the repository. Kind defaults to KindNormal.
func New(name string, projectID int64) (Execution, error) {
	c := Execution{Name: strings.TrimSpace(name), ProjectID: projectID, Kind: KindNormal}
	if err := c.Validate(); err != nil {
		return Execution{}, err
	}
	return c, nil
}

// Validate checks the Execution's invariants.
func (c Execution) Validate() error {
	switch {
	case c.Name == "":
		return ErrNameRequired
	case len(c.Name) > MaxNameLen:
		return ErrNameTooLong
	case c.ProjectID <= 0:
		return ErrProjectRequired
	}
	if c.Engine != "" && !c.Engine.Known() {
		return fmt.Errorf("%w: %q", ErrEngineUnknown, c.Engine)
	}
	if c.Kind != "" && !c.Kind.Known() {
		return fmt.Errorf("%w: %q", ErrKindUnknown, c.Kind)
	}
	// Fan-out targets are checked (trimmed, no duplicates) but not rewritten
	// here: Validate is a pure check on a value receiver, so normalization
	// happens once at construction -- executionapp.Create -- not per check.
	if _, err := NormalizeFanOutTargets(c.FanOutTargets); err != nil {
		return err
	}
	return nil
}

// NormalizeFanOutTargets trims each target, drops empty entries, and refuses
// duplicates. The empty-in/empty-out cases are both "no fan-out": a nil or
// blank list is an ordinary single-cluster execution, not an error. A
// non-empty input that normalizes to nothing (e.g. [""]) is likewise no
// fan-out -- the caller said nothing meaningful.
func NormalizeFanOutTargets(targets []string) ([]string, error) {
	seen := make(map[string]struct{}, len(targets))
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			return nil, fmt.Errorf("%w: %q", ErrFanOutTargetDuplicate, t)
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// IsFanOut reports whether this execution runs its load on multiple clusters
// at once (FanOutTargets set). Everything that branches on fan-out behaviour
// -- deploy fan-out, per-cluster readiness, per-cluster quota, ingest-token
// scoping -- asks this, never len(FanOutOutTargets) directly, so the nil and
// empty-list forms stay equivalent.
func (c Execution) IsFanOut() bool {
	return len(c.FanOutTargets) > 0
}

// Clusters returns every cluster this execution's engines live on: the
// fan-out targets of a fan-out execution, or the execution's own single
// cluster (empty = the deployment default) of an ordinary one. It is the one
// iteration source for every per-cluster operation -- deploy, status, purge,
// quota, log capture -- so a fan-out-aware caller can never forget a target
// by iterating Cluster alone.
func (c Execution) Clusters() []string {
	if c.IsFanOut() {
		return c.FanOutTargets
	}
	return []string{c.Cluster}
}
