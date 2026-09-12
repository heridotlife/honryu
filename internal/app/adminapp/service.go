// Package adminapp is the operations use-case: it lists the executions
// currently holding engines and reports cluster node pools.
package adminapp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/heridotlife/honryu/internal/domain/campaign"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/ports"
)

// Repo is the persistence admin needs to enrich and evaluate executions.
type Repo interface {
	GetExecution(ctx context.Context, id int64) (execution.Execution, error)
	CurrentRun(ctx context.Context, executionID int64) (int64, bool, error)
}

// Purger tears down a execution's engines. The lifecycle service satisfies it.
type Purger interface {
	Purge(ctx context.Context, executionID int64) error
}

// CampaignScoper resolves a campaign's kill-switch target set and can close
// the campaign once aborted. The campaignapp service satisfies it.
type CampaignScoper interface {
	Get(ctx context.Context, campaignID int64) (campaign.Campaign, error)
	// InScopeExecutions returns the campaign's non-compliant deployed
	// executions -- see campaignapp.Service.InScopeExecutions.
	InScopeExecutions(ctx context.Context, campaignID int64) ([]int64, error)
	// Abort marks the campaign closed, lifting freeze immediately.
	Abort(ctx context.Context, campaignID int64) error
}

// noopCampaignScoper is the default when no CampaignScoper is wired: every
// call reports the campaign as not found, matching "unreachable, matches
// nothing" for a caller not built against a deployment with campaigns.
type noopCampaignScoper struct{}

func (noopCampaignScoper) Get(context.Context, int64) (campaign.Campaign, error) {
	return campaign.Campaign{}, ports.ErrNotFound
}
func (noopCampaignScoper) InScopeExecutions(context.Context, int64) ([]int64, error) {
	return nil, ports.ErrNotFound
}
func (noopCampaignScoper) Abort(context.Context, int64) error { return ports.ErrNotFound }

// Service implements the admin use-cases.
type Service struct {
	repo      Repo
	sched     ports.Scheduler
	purger    Purger
	campaigns CampaignScoper
}

// NewService wires the admin service.
func NewService(repo Repo, sched ports.Scheduler, purger Purger) *Service {
	return &Service{repo: repo, sched: sched, purger: purger, campaigns: noopCampaignScoper{}}
}

// WithCampaigns attaches the kill-switch's campaign-scope resolver. Returns
// the receiver for chaining.
func (s *Service) WithCampaigns(c CampaignScoper) *Service {
	if c != nil {
		s.campaigns = c
	}
	return s
}

// RunningExecution describes an execution currently holding engines.
type RunningExecution struct {
	ExecutionID int64     `json:"execution_id"`
	Name        string    `json:"name"`
	ProjectID   int64     `json:"project_id"`
	DeployedAt  time.Time `json:"deployed_at"`
	Running     bool      `json:"running"`
}

// RunningExecutions lists every deployed execution, enriched with its name,
// project, and whether a run is in progress.
func (s *Service) RunningExecutions(ctx context.Context) ([]RunningExecution, error) {
	deployed, err := s.sched.DeployedExecutions(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]RunningExecution, 0, len(deployed))
	for executionID, deployedAt := range deployed {
		rc := RunningExecution{ExecutionID: executionID, DeployedAt: deployedAt}
		if c, err := s.repo.GetExecution(ctx, executionID); err == nil {
			rc.Name = c.Name
			rc.ProjectID = c.ProjectID
		}
		if _, running, err := s.repo.CurrentRun(ctx, executionID); err == nil {
			rc.Running = running
		}
		out = append(out, rc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExecutionID < out[j].ExecutionID })
	return out, nil
}

// NodePools reports the cluster node pools.
func (s *Service) NodePools(ctx context.Context) ([]ports.NodePool, error) {
	return s.sched.NodePools(ctx, "")
}

// abortGracePeriod bounds how long one Abort call may take tearing down
// every matching execution, mirroring the k8s scheduler adapter's own
// default pod termination grace: admin can't reasonably wait longer than a
// single pod's own shutdown budget, however many it's tearing down at once.
const abortGracePeriod = 30 * time.Second

// Scope selects which in-flight (deployed) executions a kill-switch
// invocation targets.
type Scope string

const (
	// ScopeTenant matches every deployed execution belonging to a tenant.
	// value is the tenant id.
	ScopeTenant Scope = "tenant"
	// ScopeCluster matches every deployed execution on a cluster. value is
	// the cluster ref (empty string is this deployment's own default
	// cluster, the only one there is until Phase 8).
	ScopeCluster Scope = "cluster"
	// ScopeCampaign matches every currently-deployed execution belonging to
	// the campaign -- both its non-compliant ones (what the drain sweep
	// also stops) and, unlike the drain sweep, each participating service's
	// own designated execution too: an abort is total. value is the
	// campaign id.
	ScopeCampaign Scope = "campaign"
	// ScopeExecutionList matches exactly the given executions. value is a
	// comma-separated list of execution ids.
	ScopeExecutionList Scope = "execution_list"
)

// ErrScopeInvalid is returned for an unknown scope, or a value that cannot
// be parsed for the scope given.
var ErrScopeInvalid = errors.New("adminapp: invalid kill-switch scope or value")

// Abort tears down every in-flight execution matching scope+value, via the
// existing Purge (which itself stops any run in progress first), within a
// bounded time. One execution failing to purge does not stop the rest --
// the returned ids are exactly the ones actually torn down, so a partial
// abort is visible rather than silently reported as complete.
//
// For ScopeCampaign, the campaign itself is also closed once the purge pass
// completes, regardless of whether every execution purged cleanly: freeze
// must lift even from a partial abort, since a still-stuck engine is a
// separate, already-visible problem from the campaign's window continuing
// to block other teams' work.
func (s *Service) Abort(ctx context.Context, scope Scope, value string) ([]int64, error) {
	targets, err := s.matchingExecutions(ctx, scope, value)
	if err != nil {
		return nil, err
	}
	boundedCtx, cancel := context.WithTimeout(ctx, abortGracePeriod)
	defer cancel()

	var aborted []int64
	for _, executionID := range targets {
		if err := s.purger.Purge(boundedCtx, executionID); err != nil {
			continue
		}
		aborted = append(aborted, executionID)
	}
	sort.Slice(aborted, func(i, j int) bool { return aborted[i] < aborted[j] })

	if scope == ScopeCampaign {
		campaignID, parseErr := parseCampaignID(value)
		if parseErr != nil {
			return aborted, parseErr
		}
		if err := s.campaigns.Abort(ctx, campaignID); err != nil {
			return aborted, err
		}
	}
	return aborted, nil
}

// matchingExecutions resolves scope+value into the deployed execution ids it
// selects.
func (s *Service) matchingExecutions(ctx context.Context, scope Scope, value string) ([]int64, error) {
	switch scope {
	case ScopeExecutionList:
		return parseExecutionList(value)
	case ScopeCampaign:
		campaignID, err := parseCampaignID(value)
		if err != nil {
			return nil, err
		}
		return s.campaignExecutionIDs(ctx, campaignID)
	case ScopeTenant:
		tenantID, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid tenant id %q", ErrScopeInvalid, value)
		}
		return s.deployedExecutionsForTenant(ctx, tenantID)
	case ScopeCluster:
		return s.deployedExecutionIDs(ctx, ports.ClusterRef(value))
	default:
		return nil, fmt.Errorf("%w: %q", ErrScopeInvalid, scope)
	}
}

func parseCampaignID(value string) (int64, error) {
	campaignID, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid campaign id %q", ErrScopeInvalid, value)
	}
	return campaignID, nil
}

// campaignExecutionIDs resolves every currently-deployed execution a total
// campaign abort tears down: InScopeExecutions' non-compliant set, plus
// (unlike the drain sweep, which leaves them running) each participating
// service's own designated execution if it is currently deployed.
func (s *Service) campaignExecutionIDs(ctx context.Context, campaignID int64) ([]int64, error) {
	c, err := s.campaigns.Get(ctx, campaignID)
	if err != nil {
		return nil, err
	}
	inScope, err := s.campaigns.InScopeExecutions(ctx, campaignID)
	if err != nil {
		return nil, err
	}
	deployed, err := s.sched.DeployedExecutions(ctx, "")
	if err != nil {
		return nil, err
	}
	out := append([]int64(nil), inScope...)
	for _, svc := range c.Services {
		if _, ok := deployed[svc.ExecutionID]; ok {
			out = append(out, svc.ExecutionID)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (s *Service) deployedExecutionIDs(ctx context.Context, cluster ports.ClusterRef) ([]int64, error) {
	deployed, err := s.sched.DeployedExecutions(ctx, cluster)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(deployed))
	for executionID := range deployed {
		out = append(out, executionID)
	}
	return out, nil
}

func (s *Service) deployedExecutionsForTenant(ctx context.Context, tenantID int64) ([]int64, error) {
	ids, err := s.deployedExecutionIDs(ctx, "")
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, executionID := range ids {
		exe, err := s.repo.GetExecution(ctx, executionID)
		if err != nil {
			continue // a deployed execution whose record vanished has nothing left to match against
		}
		if exe.TenantID != nil && *exe.TenantID == tenantID {
			out = append(out, executionID)
		}
	}
	return out, nil
}

func parseExecutionList(value string) ([]int64, error) {
	parts := strings.Split(value, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid execution id %q", ErrScopeInvalid, p)
		}
		out = append(out, id)
	}
	return out, nil
}
