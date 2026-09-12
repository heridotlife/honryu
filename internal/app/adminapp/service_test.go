package adminapp_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/adminapp"
	"github.com/heridotlife/honryu/internal/app/campaignapp"
	"github.com/heridotlife/honryu/internal/domain/campaign"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

type recordingPurger struct{ purged []int64 }

func (p *recordingPurger) Purge(_ context.Context, id int64) error {
	p.purged = append(p.purged, id)
	return nil
}

func seed(t *testing.T) (*fake.Store, *fake.Scheduler, *recordingPurger, *adminapp.Service, int64) {
	t.Helper()
	ctx := context.Background()
	store := fake.NewStore()
	c, _ := execution.New("peak", 3)
	executionID, _ := store.CreateExecution(ctx, c)
	sched := fake.NewScheduler()
	purger := &recordingPurger{}
	svc := adminapp.NewService(store, sched, purger)
	return store, sched, purger, svc, executionID
}

func TestRunningExecutions_Enriched(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, sched, _, svc, executionID := seed(t)
	_ = store
	if err := sched.DeployScenario(ctx, ports.DeploySpec{ProjectID: 3, ExecutionID: executionID, ScenarioID: 1, Shards: deployShards(2)}); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	list, err := svc.RunningExecutions(ctx)
	if err != nil {
		t.Fatalf("RunningExecutions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("running = %d, want 1", len(list))
	}
	if list[0].Name != "peak" || list[0].ProjectID != 3 || list[0].Running {
		t.Fatalf("running execution = %+v", list[0])
	}
}

func TestNodePools(t *testing.T) {
	t.Parallel()
	_, _, _, svc, _ := seed(t)
	pools, err := svc.NodePools(context.Background())
	if err != nil {
		t.Fatalf("NodePools: %v", err)
	}
	if len(pools) == 0 {
		t.Fatal("expected at least one node pool")
	}
}

func TestAbort_ExecutionList_PurgesExactlyTheGivenExecutions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, sched, purger, svc, executionID := seed(t)
	if err := sched.DeployScenario(ctx, ports.DeploySpec{ProjectID: 3, ExecutionID: executionID, ScenarioID: 1, Shards: deployShards(1)}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	c2, _ := execution.New("other", 3)
	otherID, _ := store.CreateExecution(ctx, c2)
	if err := sched.DeployScenario(ctx, ports.DeploySpec{ProjectID: 3, ExecutionID: otherID, ScenarioID: 2, Shards: deployShards(1)}); err != nil {
		t.Fatalf("deploy other: %v", err)
	}

	aborted, err := svc.Abort(ctx, adminapp.ScopeExecutionList, fmt.Sprintf("%d", executionID))
	if err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if len(aborted) != 1 || aborted[0] != executionID {
		t.Fatalf("aborted = %v, want [%d]", aborted, executionID)
	}
	if len(purger.purged) != 1 || purger.purged[0] != executionID {
		t.Fatalf("purger called with %v, want [%d]", purger.purged, executionID)
	}
}

func TestAbort_ExecutionList_ParsesMultipleCommaSeparatedIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, sched, _, svc, executionID := seed(t)
	if err := sched.DeployScenario(ctx, ports.DeploySpec{ProjectID: 3, ExecutionID: executionID, ScenarioID: 1, Shards: deployShards(1)}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	c2, _ := execution.New("other", 3)
	otherID, _ := store.CreateExecution(ctx, c2)
	if err := sched.DeployScenario(ctx, ports.DeploySpec{ProjectID: 3, ExecutionID: otherID, ScenarioID: 2, Shards: deployShards(1)}); err != nil {
		t.Fatalf("deploy other: %v", err)
	}

	aborted, err := svc.Abort(ctx, adminapp.ScopeExecutionList, fmt.Sprintf(" %d, %d ", executionID, otherID))
	if err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if len(aborted) != 2 {
		t.Fatalf("aborted = %v, want both executions", aborted)
	}
}

func TestAbort_ExecutionList_InvalidID(t *testing.T) {
	t.Parallel()
	_, _, _, svc, _ := seed(t)
	if _, err := svc.Abort(context.Background(), adminapp.ScopeExecutionList, "not-a-number"); !errors.Is(err, adminapp.ErrScopeInvalid) {
		t.Fatalf("Abort(bad id) = %v, want ErrScopeInvalid", err)
	}
}

func TestAbort_Tenant_PurgesOnlyThatTenantsExecutions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, sched, purger, svc, _ := seed(t)

	tenantA := int64(1)
	tenantB := int64(2)
	ca, _ := execution.New("a", 3)
	ca.TenantID = &tenantA
	aID, _ := store.CreateExecution(ctx, ca)
	cb, _ := execution.New("b", 3)
	cb.TenantID = &tenantB
	bID, _ := store.CreateExecution(ctx, cb)
	if err := sched.DeployScenario(ctx, ports.DeploySpec{ProjectID: 3, ExecutionID: aID, ScenarioID: 1, Shards: deployShards(1)}); err != nil {
		t.Fatalf("deploy a: %v", err)
	}
	if err := sched.DeployScenario(ctx, ports.DeploySpec{ProjectID: 3, ExecutionID: bID, ScenarioID: 2, Shards: deployShards(1)}); err != nil {
		t.Fatalf("deploy b: %v", err)
	}

	aborted, err := svc.Abort(ctx, adminapp.ScopeTenant, fmt.Sprintf("%d", tenantA))
	if err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if len(aborted) != 1 || aborted[0] != aID {
		t.Fatalf("aborted = %v, want [%d] (tenant A only)", aborted, aID)
	}
	if len(purger.purged) != 1 || purger.purged[0] != aID {
		t.Fatalf("purger called with %v, want [%d]", purger.purged, aID)
	}
}

func TestAbort_Tenant_InvalidValue(t *testing.T) {
	t.Parallel()
	_, _, _, svc, _ := seed(t)
	if _, err := svc.Abort(context.Background(), adminapp.ScopeTenant, "not-a-number"); !errors.Is(err, adminapp.ErrScopeInvalid) {
		t.Fatalf("Abort(bad tenant) = %v, want ErrScopeInvalid", err)
	}
}

func TestAbort_Cluster_PurgesDeployedExecutions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, sched, purger, svc, executionID := seed(t)
	if err := sched.DeployScenario(ctx, ports.DeploySpec{ProjectID: 3, ExecutionID: executionID, ScenarioID: 1, Shards: deployShards(1)}); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	aborted, err := svc.Abort(ctx, adminapp.ScopeCluster, "")
	if err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if len(aborted) != 1 || aborted[0] != executionID {
		t.Fatalf("aborted = %v, want [%d]", aborted, executionID)
	}
	if len(purger.purged) != 1 {
		t.Fatalf("purger called %d times, want 1", len(purger.purged))
	}
}

// A campaign abort is total: it tears down both the campaign's in-scope
// (non-compliant) executions and, unlike the drain sweep, the designated
// readiness execution too -- and closes the campaign itself, so freeze
// lifts immediately.
func TestAbort_Campaign_TearsDownInScopeAndDesignatedExecutionsAndClosesTheCampaign(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, sched, purger, svc, _ := seed(t)
	campaigns := campaignapp.NewService(store, sched)
	svc = svc.WithCampaigns(campaigns)

	// seed's own execution references project id 3 as a bare sentinel with
	// no real project record and isn't reused here -- campaignapp.Create
	// now requires a real, tenant-owned project (to check the campaign's
	// own tenant against it), and InScopeExecutions matches purely on each
	// execution's own stored ProjectID, so "stray" and "designated" both
	// need to actually belong to the same freshly created project.
	tenantID := int64(7)
	projectID, err := store.CreateProject(ctx, project.Project{Name: "acme", Owner: "honryu", TenantID: &tenantID})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	se, err := execution.New("stray", projectID)
	if err != nil {
		t.Fatalf("execution.New (stray): %v", err)
	}
	strayID, err := store.CreateExecution(ctx, se)
	if err != nil {
		t.Fatalf("CreateExecution (stray): %v", err)
	}
	de, err := execution.New("readiness", projectID)
	if err != nil {
		t.Fatalf("execution.New: %v", err)
	}
	designatedID, err := store.CreateExecution(ctx, de)
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
	if err := sched.DeployScenario(ctx, ports.DeploySpec{ProjectID: projectID, ExecutionID: strayID, ScenarioID: 1, Shards: deployShards(1)}); err != nil {
		t.Fatalf("deploy stray: %v", err)
	}
	if err := sched.DeployScenario(ctx, ports.DeploySpec{ProjectID: projectID, ExecutionID: designatedID, ScenarioID: 1, Shards: deployShards(1)}); err != nil {
		t.Fatalf("deploy designated: %v", err)
	}

	created, err := campaigns.Create(ctx, campaign.Campaign{
		Name: "c", TenantID: 7, Window: campaign.Window{Start: time.Now().Add(-time.Hour), End: time.Now().Add(time.Hour)},
		Services: []campaign.Service{{ProjectID: projectID, ExecutionID: designatedID}},
	})
	if err != nil {
		t.Fatalf("Create campaign: %v", err)
	}

	aborted, err := svc.Abort(ctx, adminapp.ScopeCampaign, strconv.FormatInt(created.ID, 10))
	if err != nil {
		t.Fatalf("Abort(campaign) = %v, want nil", err)
	}
	want := map[int64]bool{strayID: true, designatedID: true}
	if len(aborted) != len(want) {
		t.Fatalf("aborted = %v, want exactly %v", aborted, want)
	}
	for _, id := range aborted {
		if !want[id] {
			t.Errorf("aborted %d, not expected", id)
		}
	}
	if len(purger.purged) != 2 {
		t.Fatalf("purged = %v, want both executions purged", purger.purged)
	}

	got, err := campaigns.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get campaign: %v", err)
	}
	if got.AbortedAt == nil {
		t.Fatal("campaign should be marked aborted after a campaign-scoped Abort")
	}
}

func TestAbort_Campaign_InvalidValue(t *testing.T) {
	t.Parallel()
	_, _, _, svc, _ := seed(t)
	if _, err := svc.Abort(context.Background(), adminapp.ScopeCampaign, "not-a-number"); !errors.Is(err, adminapp.ErrScopeInvalid) {
		t.Fatalf("Abort(campaign, invalid value) = %v, want ErrScopeInvalid", err)
	}
}

func TestAbort_Campaign_MissingCampaignPropagatesNotFound(t *testing.T) {
	t.Parallel()
	store, sched, _, svc, _ := seed(t)
	svc = svc.WithCampaigns(campaignapp.NewService(store, sched))
	if _, err := svc.Abort(context.Background(), adminapp.ScopeCampaign, "999"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Abort(campaign, missing) = %v, want ErrNotFound", err)
	}
}

// Without WithCampaigns, the noop default reports every campaign as not
// found -- a caller built against a deployment that never wired campaigns
// gets a clear error, not a silent no-op.
func TestAbort_Campaign_NotWiredPropagatesNotFound(t *testing.T) {
	t.Parallel()
	_, _, _, svc, _ := seed(t)
	if _, err := svc.Abort(context.Background(), adminapp.ScopeCampaign, "1"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Abort(campaign, not wired) = %v, want ErrNotFound", err)
	}
}

func TestAbort_UnknownScope(t *testing.T) {
	t.Parallel()
	_, _, _, svc, _ := seed(t)
	if _, err := svc.Abort(context.Background(), adminapp.Scope("bogus"), ""); !errors.Is(err, adminapp.ErrScopeInvalid) {
		t.Fatalf("Abort(unknown scope) = %v, want ErrScopeInvalid", err)
	}
}

// One execution failing to purge must not stop the rest, and the returned
// ids must be exactly what actually got torn down -- a partial abort must be
// visible, not silently reported as complete.
func TestAbort_PartialFailureSkipsButContinues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, sched, _, _, executionID := seed(t)
	if err := sched.DeployScenario(ctx, ports.DeploySpec{ProjectID: 3, ExecutionID: executionID, ScenarioID: 1, Shards: deployShards(1)}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	c2, _ := execution.New("other", 3)
	otherID, _ := store.CreateExecution(ctx, c2)
	if err := sched.DeployScenario(ctx, ports.DeploySpec{ProjectID: 3, ExecutionID: otherID, ScenarioID: 2, Shards: deployShards(1)}); err != nil {
		t.Fatalf("deploy other: %v", err)
	}

	failing := &failingPurger{failFor: executionID}
	svc := adminapp.NewService(store, sched, failing)
	aborted, err := svc.Abort(ctx, adminapp.ScopeExecutionList, fmt.Sprintf("%d,%d", executionID, otherID))
	if err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if len(aborted) != 1 || aborted[0] != otherID {
		t.Fatalf("aborted = %v, want [%d] -- the failing one must be skipped, not fatal", aborted, otherID)
	}
}

type failingPurger struct{ failFor int64 }

func (p *failingPurger) Purge(_ context.Context, id int64) error {
	if id == p.failFor {
		return errors.New("boom")
	}
	return nil
}

// deployShards builds n placeholder shard specs for a deploy.
func deployShards(n int) []ports.ShardSpec {
	out := make([]ports.ShardSpec, n)
	for i := range out {
		out[i] = ports.ShardSpec{Index: i, Concurrency: 1}
	}
	return out
}

// stubScoper is a CampaignScoper with injectable failures, for proving
// Abort's campaign-scope error propagation and partial-abort semantics
// without leaning on campaignapp internals.
type stubScoper struct {
	campaign campaign.Campaign
	getErr   error
	inScope  []int64
	scopeErr error
	abortErr error
	aborted  bool
}

func (s *stubScoper) Get(context.Context, int64) (campaign.Campaign, error) {
	return s.campaign, s.getErr
}
func (s *stubScoper) InScopeExecutions(context.Context, int64) ([]int64, error) {
	return s.inScope, s.scopeErr
}
func (s *stubScoper) Abort(context.Context, int64) error {
	s.aborted = true
	return s.abortErr
}

func TestAbort_Campaign_InScopeLookupErrorPropagates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, _, purger, svc, _ := seed(t)
	svc = svc.WithCampaigns(&stubScoper{scopeErr: errors.New("scope resolve down")})

	aborted, err := svc.Abort(ctx, adminapp.ScopeCampaign, "5")
	if err == nil {
		t.Fatal("Abort with a failing scope resolver: want error")
	}
	if aborted != nil {
		t.Errorf("aborted = %v, want nil when matching failed", aborted)
	}
	if len(purger.purged) != 0 {
		t.Errorf("purged = %v, want nothing purged when matching failed", purger.purged)
	}
}

func TestAbort_Campaign_ScoperAbortFailureStillReportsPurgedExecutions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, _, purger, svc, executionID := seed(t)
	scoper := &stubScoper{inScope: []int64{executionID}, abortErr: errors.New("campaign close failed")}
	svc = svc.WithCampaigns(scoper)

	aborted, err := svc.Abort(ctx, adminapp.ScopeCampaign, "5")
	if err == nil {
		t.Fatal("Abort with a failing campaign close: want error")
	}
	// Partial-abort semantics: the engines were torn down and the caller is
	// told so, even though closing the campaign failed.
	if len(aborted) != 1 || aborted[0] != executionID {
		t.Fatalf("aborted = %v, want [%d]", aborted, executionID)
	}
	if len(purger.purged) != 1 || purger.purged[0] != executionID {
		t.Fatalf("purged = %v, want [%d]", purger.purged, executionID)
	}
	if !scoper.aborted {
		t.Error("campaign close was not attempted")
	}
}
