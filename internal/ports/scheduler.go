package ports

import (
	"context"
	"errors"
	"time"
)

// ErrEnginesUnreachable is returned when a scenario's engines are not yet
// routable. Callers may retry.
var ErrEnginesUnreachable = errors.New("ports: engines unreachable")

// ClusterRef names the cluster an operation targets.
//
// It appears on every method so a caller cannot forget to say where work should
// happen. An empty ref means the deployment's own cluster, which is the only one
// there is until a registry maps refs to credentials -- at which point nothing
// here changes shape.
type ClusterRef string

// DeploySpec describes the pods to run for a single scenario of an execution.
//
// Load is divided by Honryu, not by the engine: each shard is an ordinary bzt
// running its own fraction of the profile, so a pod is described by the config
// it runs rather than by a count of interchangeable engines.
type DeploySpec struct {
	Cluster     ClusterRef
	ProjectID   int64
	ExecutionID int64
	ScenarioID  int64
	Image       string // engine container image
	CPU         string // optional resource request/limit, e.g. "1"
	Memory      string // optional resource request/limit, e.g. "512Mi"
	// Shards are the pods to create, one per shard of the load profile.
	Shards []ShardSpec
	// ScenarioFiles are the scenario's own artefacts -- a .jmx, a k6 script,
	// CSV data -- keyed by filename, mounted into every pod. A native scenario
	// cannot run without the file its config points at.
	ScenarioFiles map[string][]byte
}

// ShardSpec is one pod: its position in the plan and the compiled Taurus config
// it runs.
type ShardSpec struct {
	// Index is the shard's position, matching the shard plan, and identifies the
	// pod's measurements when they are pushed back.
	Index int
	// Config is the compiled Taurus config this pod runs.
	Config []byte
	// Concurrency is this pod's share of the virtual users, for reporting what
	// was asked of it.
	Concurrency int
}

// ScenarioRef names a scenario and how many pods it expects; used to query
// status.
type ScenarioRef struct {
	ScenarioID int64
	Shards     int
}

// ScenarioReadiness reports how many of a scenario's pods are up.
type ScenarioReadiness struct {
	ScenarioID      int64 `json:"scenario_id"`
	EnginesWanted   int   `json:"engines"`
	EnginesDeployed int   `json:"engines_deployed"`
	// Reachable reported whether an engine could be called. Nothing calls an
	// engine now -- a pod runs its config and pushes results back -- so it
	// reports only that the pods are running.
	Reachable bool `json:"engines_reachable"`
}

// ExecutionStatus aggregates scenario readiness for an execution.
type ExecutionStatus struct {
	Scenarios []ScenarioReadiness `json:"status"`
	PoolSize  int                 `json:"pool_size"`
}

// EngineDetail describes one engine pod.
type EngineDetail struct {
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	CreatedTime time.Time `json:"created_time"`
}

// ExecutionDetail describes an execution's ingress and engine pods.
type ExecutionDetail struct {
	IngressIP string         `json:"ingress_ip"`
	Engines   []EngineDetail `json:"engines"`
}

// NodePool summarises a cluster node pool backing engine capacity.
type NodePool struct {
	Name       string    `json:"name"`
	Size       int       `json:"size"`
	LaunchTime time.Time `json:"launch_time"`
}

// Scheduler manages the compute (pods, services, ingress) backing a
// execution's engines in a cluster. It is orchestration only: what a test
// tool does on an engine is the Executor's concern.
type Scheduler interface {
	// DeployScenario creates a pod per shard in spec, each running its own
	// compiled config. It is idempotent: deploying an already-deployed scenario
	// is a no-op.
	DeployScenario(ctx context.Context, spec DeploySpec) error
	// ExecutionStatus reports per-scenario pod readiness.
	ExecutionStatus(ctx context.Context, cluster ClusterRef, executionID int64, scenarios []ScenarioRef) (ExecutionStatus, error)
	// EngineDetail reports the pods of an execution.
	EngineDetail(ctx context.Context, cluster ClusterRef, projectID, executionID int64) (ExecutionDetail, error)
	// PurgeExecution removes everything an execution deployed. Purging an
	// execution with nothing deployed is not an error.
	PurgeExecution(ctx context.Context, cluster ClusterRef, executionID int64) error
	// PodLog returns the current logs of one shard's pod. Shard logs are the
	// engine-side half of fault attribution, so they are addressed per pod
	// rather than only for the first.
	PodLog(ctx context.Context, cluster ClusterRef, executionID, scenarioID int64, shard int) (string, error)
	// DeployedExecutions maps deployed execution id to its earliest deploy time;
	// used by the idle reaper, campaign kill-switch scoping, and admin
	// listings.
	DeployedExecutions(ctx context.Context, cluster ClusterRef) (map[int64]time.Time, error)
	// NodePools summarises the node pools backing engine capacity.
	NodePools(ctx context.Context, cluster ClusterRef) ([]NodePool, error)
}
