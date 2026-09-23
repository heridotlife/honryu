// Package campaignapp is the campaign use-case: create a PM-owned readiness
// event, list and retrieve campaigns, and abort one. It performs no I/O of
// its own beyond its Repo port.
package campaignapp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/heridotlife/honryu/internal/domain/campaign"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/reservation"
	"github.com/heridotlife/honryu/internal/ports"
)

// ErrServiceExecutionMismatch means a campaign named an execution that does
// not actually belong to the project it was registered under.
var ErrServiceExecutionMismatch = errors.New("campaignapp: designated execution does not belong to its stated project")

// ErrServiceProjectTenantMismatch means a campaign named a project that does
// not actually belong to the campaign's own declared tenant. Without this
// check, a caller authorized to manage campaigns in one tenant could name
// another tenant's project id and execution id (plain sequential integers,
// not secrets) and have freeze, the drain sweep, and the kill-switch's
// ScopeCampaign all act on that project -- none of which re-derive tenant
// ownership from anywhere else, since they key purely off project id.
var ErrServiceProjectTenantMismatch = errors.New("campaignapp: project does not belong to the campaign's tenant")

// ErrCampaignStarted means the campaign's window has already opened, so
// its definition is frozen: freeze, the drain sweep, and the verdict all
// key off the window and the service set, and editing a live campaign
// would change what they apply to mid-flight. Editing is
// preparation-only (spec Phase 20, Approach D).
var ErrCampaignStarted = errors.New("campaignapp: campaign window has started; definition is frozen")

// Repo is the persistence campaignapp needs: the campaign ledger, enough of
// a project and execution to verify a service's designated execution
// actually belongs to the project it's registered under and that project
// actually belongs to the campaign's own tenant, and enough of the report
// store and execution criteria to compute a campaign's verdict.
type Repo interface {
	ports.CampaignRepository
	GetExecution(ctx context.Context, id int64) (execution.Execution, error)
	// GetProject backs the tenant-ownership check in Create and OtherLoad's
	// tenant scoping -- execution.TenantID is never populated by any
	// current execution-creation path, so a project's own TenantID is the
	// only reliable source for "which tenant does this actually belong to".
	GetProject(ctx context.Context, id int64) (project.Project, error)
	// ListReports returns an execution's reports, most recent first --
	// Verdict reads index 0 as "the designated execution's latest report."
	ListReports(ctx context.Context, executionID int64, limit int) ([]report.Report, error)
	// CriteriaFor returns an execution's configured Taurus pass/fail
	// criteria, evaluated against its latest report to name what failed.
	CriteriaFor(ctx context.Context, executionID int64) ([]string, error)
	// ReservationsInWindow and LaunchHistory feed Verdict's OtherLoad
	// annotation -- the residual-risk mitigation of recording what else was
	// active in the campaign's tenant during its window, reusing Phase 5's
	// existing reservation ledger and usage history rather than adding new
	// instrumentation.
	ReservationsInWindow(ctx context.Context, tenantID int64, cluster string, start, end time.Time) ([]reservation.Reservation, error)
	LaunchHistory(ctx context.Context, from, to time.Time) ([]ports.LaunchRecord, error)
}

// Scheduler is the subset of ports.Scheduler campaignapp needs: which
// executions are currently deployed, to resolve a campaign's in-scope
// non-compliant executions (InScopeExecutions).
type Scheduler interface {
	DeployedExecutions(ctx context.Context, cluster ports.ClusterRef) (map[int64]time.Time, error)
}

// Service implements the campaign use-cases.
type Service struct {
	repo  Repo
	sched Scheduler
	now   func() time.Time
}

// NewService wires the campaign service.
func NewService(repo Repo, sched Scheduler) *Service {
	return &Service{repo: repo, sched: sched, now: time.Now}
}

// WithNow overrides the clock Abort timestamps with. Returns the receiver
// for chaining.
func (s *Service) WithNow(now func() time.Time) *Service {
	if now != nil {
		s.now = now
	}
	return s
}

// verifyServices checks every participating service's invariants against
// the rest of the store: the designated execution must actually belong to
// the project it's registered under (a typo'd execution id would let one
// project's execution silently decide a completely different service's
// verdict), and that project must actually belong to the campaign's own
// declared tenant (a campaign manager in one tenant must not be able to
// freeze, drain, and kill-switch another tenant's project). Shared by
// Create and Update -- an edit re-runs every check a creation ran.
func (s *Service) verifyServices(ctx context.Context, c campaign.Campaign) error {
	for _, svc := range c.Services {
		exe, err := s.repo.GetExecution(ctx, svc.ExecutionID)
		if err != nil {
			return err
		}
		if exe.ProjectID != svc.ProjectID {
			return fmt.Errorf("%w: execution %d belongs to project %d, not %d",
				ErrServiceExecutionMismatch, svc.ExecutionID, exe.ProjectID, svc.ProjectID)
		}
		proj, err := s.repo.GetProject(ctx, svc.ProjectID)
		if err != nil {
			return err
		}
		if proj.TenantID == nil || *proj.TenantID != c.TenantID {
			return fmt.Errorf("%w: project %d does not belong to tenant %d",
				ErrServiceProjectTenantMismatch, svc.ProjectID, c.TenantID)
		}
	}
	return nil
}

// Create validates c and persists it. Each service's designated execution
// must actually belong to the project it's registered under -- without this
// check, a typo'd execution id would let one project's execution silently
// decide a completely different service's verdict -- and that project must
// actually belong to c's own declared tenant, or a campaign manager in one
// tenant could freeze, drain, and kill-switch another tenant's project.
func (s *Service) Create(ctx context.Context, c campaign.Campaign) (campaign.Campaign, error) {
	if err := c.Validate(); err != nil {
		return campaign.Campaign{}, err
	}
	if err := s.verifyServices(ctx, c); err != nil {
		return campaign.Campaign{}, err
	}
	id, err := s.repo.CreateCampaign(ctx, c)
	if err != nil {
		return campaign.Campaign{}, err
	}
	c.ID = id
	return c, nil
}

// Get returns the campaign with id, or ports.ErrNotFound.
func (s *Service) Get(ctx context.Context, id int64) (campaign.Campaign, error) {
	return s.repo.GetCampaign(ctx, id)
}

// List returns every campaign belonging to tenantID.
func (s *Service) List(ctx context.Context, tenantID int64) ([]campaign.Campaign, error) {
	return s.repo.ListCampaignsByTenant(ctx, tenantID)
}

// ListAll returns every campaign across every tenant -- the
// service-provider admin branch of GET /api/campaigns (the same
// short-circuit visibleProjects applies to projects).
func (s *Service) ListAll(ctx context.Context) ([]campaign.Campaign, error) {
	return s.repo.ListAllCampaigns(ctx)
}

// ListByTenants returns every campaign belonging to any of tenantIDs --
// the cross-tenant view behind GET /api/campaigns, where a campaign
// manager coordinating several tenants needs one list, not one request
// per tenant.
func (s *Service) ListByTenants(ctx context.Context, tenantIDs []int64) ([]campaign.Campaign, error) {
	return s.repo.ListCampaignsByTenants(ctx, tenantIDs)
}

// ActiveCampaigns returns every campaign currently active (per
// Campaign.IsActive) across every tenant -- what cmd/scheduler's drain
// sweep iterates, one InScopeExecutions call per campaign.
func (s *Service) ActiveCampaigns(ctx context.Context) ([]campaign.Campaign, error) {
	return s.repo.ListActiveCampaigns(ctx, s.now())
}

// Abort marks the campaign with id aborted now. Tearing down its in-scope
// executions' engines is the caller's responsibility (adminapp) -- this
// only closes the campaign itself, which is what lifts freeze immediately
// (Campaign.IsActive is derived from AbortedAt, not a separate flag).
func (s *Service) Abort(ctx context.Context, id int64) error {
	return s.repo.AbortCampaign(ctx, id, s.now())
}

// Update replaces the campaign's editable definition -- name, window, and
// participating services -- while it is still in preparation, i.e. while
// Window.Start is still in the future. Once the window has opened the
// definition is frozen (ErrCampaignStarted): freeze, the drain sweep, and
// the verdict all key off the window and the service set, and editing a
// live campaign would change what they apply to mid-flight. The identity
// fields (id, tenant) and AbortedAt come from the stored campaign, not
// from next -- a campaign never changes tenants by being edited, and
// AbortCampaign owns AbortedAt.
func (s *Service) Update(ctx context.Context, id int64, next campaign.Campaign) (campaign.Campaign, error) {
	existing, err := s.repo.GetCampaign(ctx, id)
	if err != nil {
		return campaign.Campaign{}, err
	}
	if !s.now().Before(existing.Window.Start) {
		return campaign.Campaign{}, fmt.Errorf("%w: window opened at %s", ErrCampaignStarted,
			existing.Window.Start.UTC().Format(time.RFC3339))
	}
	next.ID = existing.ID
	next.TenantID = existing.TenantID
	next.AbortedAt = existing.AbortedAt
	if err := next.Validate(); err != nil {
		return campaign.Campaign{}, err
	}
	if err := s.verifyServices(ctx, next); err != nil {
		return campaign.Campaign{}, err
	}
	if err := s.repo.UpdateCampaign(ctx, next); err != nil {
		return campaign.Campaign{}, err
	}
	return s.repo.GetCampaign(ctx, id)
}

// InScopeExecutions returns every currently-deployed execution belonging to
// one of campaignID's participating projects, excluding each service's own
// designated execution -- the exact set cmd/scheduler's drain sweep stops.
// A pure query: it has no opinion on whether campaignID is currently
// active, since a caller (the drain sweep) already scoped that by only
// ever asking about campaigns ListActiveCampaigns returned.
//
// Shared with adminapp's kill-switch (ScopeCampaign), which additionally
// includes the designated executions themselves -- an abort tears down
// everything, including the readiness tests, unlike a drain which is meant
// to leave them running.
func (s *Service) InScopeExecutions(ctx context.Context, campaignID int64) ([]int64, error) {
	c, err := s.repo.GetCampaign(ctx, campaignID)
	if err != nil {
		return nil, err
	}
	deployed, err := s.sched.DeployedExecutions(ctx, "")
	if err != nil {
		return nil, err
	}
	designated := make(map[int64]bool, len(c.Services))
	projects := make(map[int64]bool, len(c.Services))
	for _, svc := range c.Services {
		designated[svc.ExecutionID] = true
		projects[svc.ProjectID] = true
	}
	var out []int64
	for executionID := range deployed {
		if designated[executionID] {
			continue
		}
		exe, err := s.repo.GetExecution(ctx, executionID)
		if err != nil {
			continue // a deployed execution whose record vanished has nothing left to match against
		}
		if projects[exe.ProjectID] {
			out = append(out, executionID)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// IsFrozen reports whether executionID (belonging to projectID) is blocked
// by campaign freeze right now: blocked unless executionID is the
// designated execution of every active campaign that includes projectID at
// all. campaignName names a blocking campaign so lifecycleapp.Trigger can
// state a reason.
//
// Scans every active campaign rather than stopping at the first one that
// includes projectID: two campaigns could both register the same project
// (not prevented by Campaign.Validate, which only checks one campaign's own
// invariants), and an execution that is one campaign's designated readiness
// test must stay exempt even if a second, unrelated campaign also touches
// the same project with a different designated execution.
func (s *Service) IsFrozen(ctx context.Context, projectID, executionID int64) (blocked bool, campaignName string, err error) {
	active, err := s.repo.ListActiveCampaigns(ctx, s.now())
	if err != nil {
		return false, "", err
	}
	blockingCampaign := ""
	for _, c := range active {
		designated, ok := c.DesignatedExecution(projectID)
		if !ok {
			continue // this campaign does not include projectID at all
		}
		if designated == executionID {
			return false, "", nil
		}
		if blockingCampaign == "" {
			blockingCampaign = c.Name
		}
	}
	if blockingCampaign != "" {
		return true, blockingCampaign, nil
	}
	return false, "", nil
}
