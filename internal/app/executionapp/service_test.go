package executionapp_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

const maxEngines = 5

func newCollService(t *testing.T) (*executionapp.Service, *fake.Store, *fake.ObjectStore) {
	t.Helper()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	return executionapp.NewService(store, obj, maxEngines), store, obj
}

// seedScenario creates a scenario directly in the store for the given project.
func seedScenario(t *testing.T, store *fake.Store, name string, projectID int64) int64 {
	t.Helper()
	p, err := scenario.New(name, projectID)
	if err != nil {
		t.Fatalf("scenario.New: %v", err)
	}
	id, err := store.CreateScenario(context.Background(), p)
	if err != nil {
		t.Fatalf("CreateScenario: %v", err)
	}
	return id
}

// seedProject creates a project directly in the store with the given tenant
// (nil for none) and returns its ID.
func seedProject(t *testing.T, store *fake.Store, tenantID *int64) int64 {
	t.Helper()
	p, err := project.New("proj", "owner", "123")
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	p.TenantID = tenantID
	id, err := store.CreateProject(context.Background(), p)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return id
}

func TestCreate_Get_List(t *testing.T) {
	t.Parallel()
	svc, _, _ := newCollService(t)
	ctx := context.Background()

	c, err := svc.Create(ctx, "peak", 10, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := svc.Get(ctx, c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "peak" || got.ProjectID != 10 {
		t.Fatalf("Get = %+v, want peak/10", got)
	}
	list, err := svc.ListByProject(ctx, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByProject = %v (err %v), want 1", list, err)
	}
}

// TestCreate_StampsTenantFromProject is Phase 20 tenant propagation
// (spec Approach A): an execution's tenant always equals its project's
// tenant, resolved at create time rather than left NULL forever.
func TestCreate_StampsTenantFromProject(t *testing.T) {
	t.Parallel()
	svc, store, _ := newCollService(t)
	ctx := context.Background()
	tenantID := int64(7)

	projectID := seedProject(t, store, &tenantID)
	c, err := svc.Create(ctx, "peak", projectID, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.TenantID == nil || *c.TenantID != tenantID {
		t.Fatalf("Create TenantID = %v, want %d", c.TenantID, tenantID)
	}
	got, err := svc.Get(ctx, c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TenantID == nil || *got.TenantID != tenantID {
		t.Fatalf("stored TenantID = %v, want %d", got.TenantID, tenantID)
	}
}

// TestCreate_NilTenantProjectStampsNilTenant covers a project with no tenant
// (TenantID nil) -- the execution must inherit nil, not silently pick up a
// stray value.
func TestCreate_NilTenantProjectStampsNilTenant(t *testing.T) {
	t.Parallel()
	svc, store, _ := newCollService(t)
	ctx := context.Background()

	projectID := seedProject(t, store, nil)
	c, err := svc.Create(ctx, "peak", projectID, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.TenantID != nil {
		t.Fatalf("Create TenantID = %v, want nil", c.TenantID)
	}
}

func TestCreate_StoresClusterTrimmed(t *testing.T) {
	t.Parallel()
	svc, _, _ := newCollService(t)
	ctx := context.Background()

	c, err := svc.Create(ctx, "peak", 10, "", "  prod-eu  ", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.Cluster != "prod-eu" {
		t.Fatalf("Create Cluster = %q, want trimmed prod-eu", c.Cluster)
	}
	got, err := svc.Get(ctx, c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Cluster != "prod-eu" {
		t.Fatalf("stored Cluster = %q, want prod-eu", got.Cluster)
	}
}

func TestCreate_EmptyClusterIsDefault(t *testing.T) {
	t.Parallel()
	svc, _, _ := newCollService(t)
	c, err := svc.Create(context.Background(), "peak", 10, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.Cluster != "" {
		t.Fatalf("Create Cluster = %q, want empty (default)", c.Cluster)
	}
}

func TestFileLifecycle(t *testing.T) {
	t.Parallel()
	svc, _, obj := newCollService(t)
	ctx := context.Background()
	c, _ := svc.Create(ctx, "peak", 10, "", "", nil)

	if err := svc.UploadFile(ctx, c.ID, "shared.csv", bytes.NewReader([]byte("x,y"))); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if _, err := obj.Download(ctx, "execution/1/shared.csv"); err != nil {
		t.Fatalf("object not stored: %v", err)
	}
	files, err := svc.Files(ctx, c.ID)
	if err != nil || len(files) != 1 || files[0].Filename != "shared.csv" || files[0].URL == "" {
		t.Fatalf("Files = %+v (err %v)", files, err)
	}
	got, err := svc.DownloadFile(ctx, c.ID, "shared.csv")
	if err != nil || string(got) != "x,y" {
		t.Fatalf("DownloadFile = %q (err %v)", got, err)
	}
	if err := svc.UploadFile(ctx, c.ID, "shared.csv", bytes.NewReader([]byte("z"))); !errors.Is(err, ports.ErrFileExists) {
		t.Fatalf("dup UploadFile = %v, want ErrFileExists", err)
	}
	if err := svc.DeleteFile(ctx, c.ID, "shared.csv"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if _, err := obj.Download(ctx, "execution/1/shared.csv"); !errors.Is(err, ports.ErrObjectNotFound) {
		t.Fatalf("object survived delete: %v", err)
	}
}

func TestDelete_RemovesFiles(t *testing.T) {
	t.Parallel()
	svc, _, obj := newCollService(t)
	ctx := context.Background()
	c, _ := svc.Create(ctx, "peak", 10, "", "", nil)
	_ = svc.UploadFile(ctx, c.ID, "shared.csv", bytes.NewReader([]byte("x")))

	if err := svc.Delete(ctx, c.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := obj.Download(ctx, "execution/1/shared.csv"); !errors.Is(err, ports.ErrObjectNotFound) {
		t.Fatalf("file survived execution delete: %v", err)
	}
}

func TestStoreConfig_And_GetConfig(t *testing.T) {
	t.Parallel()
	svc, store, _ := newCollService(t)
	ctx := context.Background()
	c, _ := svc.Create(ctx, "peak", 10, "", "", nil)
	scenarioID := seedScenario(t, store, "smoke", 10)

	ec := loadprofile.Profile{
		ExecutionID: c.ID,
		CSVSplit:    true,
		Tests: []loadprofile.Entry{
			{ScenarioID: scenarioID, Engines: 2, Concurrency: 10, Duration: 60},
		},
		Criteria: []string{"failures>10%", "p95>500ms"},
	}
	if err := svc.StoreConfig(ctx, c.ID, ec); err != nil {
		t.Fatalf("StoreConfig: %v", err)
	}

	wrapper, err := svc.GetConfig(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if wrapper.Content.ExecutionID != c.ID || !wrapper.Content.CSVSplit {
		t.Fatalf("GetConfig content = %+v", wrapper.Content)
	}
	if len(wrapper.Content.Tests) != 1 || wrapper.Content.Tests[0].ScenarioID != scenarioID || wrapper.Content.Tests[0].Engines != 2 {
		t.Fatalf("GetConfig tests = %+v", wrapper.Content.Tests)
	}
	if len(wrapper.Content.Criteria) != 2 || wrapper.Content.Criteria[0] != "failures>10%" || wrapper.Content.Criteria[1] != "p95>500ms" {
		t.Fatalf("GetConfig criteria = %+v, want [failures>10%%, p95>500ms] in this order", wrapper.Content.Criteria)
	}
}

// A config re-upload with no criteria clears whatever was configured
// before -- StoreConfig replaces the whole configuration, it does not
// merge with what was there.
func TestStoreConfig_ReuploadWithNoCriteriaClearsThem(t *testing.T) {
	t.Parallel()
	svc, store, _ := newCollService(t)
	ctx := context.Background()
	c, _ := svc.Create(ctx, "peak", 10, "", "", nil)
	scenarioID := seedScenario(t, store, "smoke", 10)
	tests := []loadprofile.Entry{{ScenarioID: scenarioID, Engines: 2, Concurrency: 10, Duration: 60}}

	if err := svc.StoreConfig(ctx, c.ID, loadprofile.Profile{
		ExecutionID: c.ID, Tests: tests, Criteria: []string{"failures>10%"},
	}); err != nil {
		t.Fatalf("StoreConfig (with criteria): %v", err)
	}
	if err := svc.StoreConfig(ctx, c.ID, loadprofile.Profile{ExecutionID: c.ID, Tests: tests}); err != nil {
		t.Fatalf("StoreConfig (no criteria): %v", err)
	}

	wrapper, err := svc.GetConfig(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if len(wrapper.Content.Criteria) != 0 {
		t.Fatalf("GetConfig criteria after re-upload with none = %v, want none", wrapper.Content.Criteria)
	}
}

func TestStoreConfig_Errors(t *testing.T) {
	t.Parallel()
	svc, store, _ := newCollService(t)
	ctx := context.Background()
	c, _ := svc.Create(ctx, "peak", 10, "", "", nil)
	scenarioID := seedScenario(t, store, "smoke", 10)
	foreignScenario := seedScenario(t, store, "other", 99)

	valid := func() loadprofile.Entry {
		return loadprofile.Entry{ScenarioID: scenarioID, Engines: 2, Concurrency: 10, Duration: 60}
	}

	t.Run("execution id mismatch", func(t *testing.T) {
		ec := loadprofile.Profile{ExecutionID: c.ID + 100, Tests: []loadprofile.Entry{valid()}}
		if err := svc.StoreConfig(ctx, c.ID, ec); !errors.Is(err, executionapp.ErrExecutionMismatch) {
			t.Fatalf("= %v, want ErrExecutionMismatch", err)
		}
	})

	t.Run("validation error (zero engines)", func(t *testing.T) {
		ec := loadprofile.Profile{ExecutionID: c.ID, Tests: []loadprofile.Entry{
			{ScenarioID: scenarioID, Engines: 0, Concurrency: 1, Duration: 1},
		}}
		if err := svc.StoreConfig(ctx, c.ID, ec); !errors.Is(err, loadprofile.ErrEnginesInvalid) {
			t.Fatalf("= %v, want ErrEnginesInvalid", err)
		}
	})

	t.Run("unknown scenario", func(t *testing.T) {
		ec := loadprofile.Profile{ExecutionID: c.ID, Tests: []loadprofile.Entry{
			{ScenarioID: 987654, Engines: 1, Concurrency: 1, Duration: 1},
		}}
		if err := svc.StoreConfig(ctx, c.ID, ec); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("= %v, want ErrNotFound", err)
		}
	})

	t.Run("scenario in another project", func(t *testing.T) {
		ec := loadprofile.Profile{ExecutionID: c.ID, Tests: []loadprofile.Entry{
			{ScenarioID: foreignScenario, Engines: 1, Concurrency: 1, Duration: 1},
		}}
		if err := svc.StoreConfig(ctx, c.ID, ec); !errors.Is(err, executionapp.ErrScenarioNotInProject) {
			t.Fatalf("= %v, want ErrScenarioNotInProject", err)
		}
	})

	t.Run("engine limit exceeded", func(t *testing.T) {
		ec := loadprofile.Profile{ExecutionID: c.ID, Tests: []loadprofile.Entry{
			{ScenarioID: scenarioID, Engines: maxEngines + 1, Concurrency: 1, Duration: 1},
		}}
		if err := svc.StoreConfig(ctx, c.ID, ec); !errors.Is(err, executionapp.ErrEngineLimit) {
			t.Fatalf("= %v, want ErrEngineLimit", err)
		}
	})

	t.Run("missing execution", func(t *testing.T) {
		ec := loadprofile.Profile{ExecutionID: 424242, Tests: []loadprofile.Entry{valid()}}
		if err := svc.StoreConfig(ctx, 424242, ec); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("= %v, want ErrNotFound", err)
		}
	})
}

// The last-run batch read is a pass-through with one rule of its own: an
// empty id list asks for nothing, the ListForProjects convention, so "no
// rows" can never turn into "every row". The read semantics themselves are
// the repository contract's (repositorytest, RunExecutionRepositoryContract),
// and the HTTP wiring's (scenario_list_handlers_test).
func TestLatestRunsForScenarios_EmptyListAsksNothing(t *testing.T) {
	svc, _, _ := newCollService(t)

	got, err := svc.LatestRunsForScenarios(context.Background(), nil)
	if err != nil {
		t.Fatalf("LatestRunsForScenarios(nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("LatestRunsForScenarios(nil) = %v, want empty", got)
	}
}

// --- Fan-out (phase 88) --------------------------------------------------------

func TestCreate_FanOutTargetsNormalized(t *testing.T) {
	t.Parallel()
	svc, store, obj := newCollService(t)
	c, err := svc.Create(context.Background(), "everywhere", 10, "", "", []string{" eu-1 ", "", "us-1"})
	if err != nil {
		t.Fatalf("Create (fan-out) = %v, want nil", err)
	}
	if len(c.FanOutTargets) != 2 || c.FanOutTargets[0] != "eu-1" || c.FanOutTargets[1] != "us-1" {
		t.Fatalf("FanOutTargets = %q, want [eu-1 us-1]", c.FanOutTargets)
	}
	if !c.IsFanOut() {
		t.Error("IsFanOut = false, want true")
	}
	got, err := store.GetExecution(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if len(got.FanOutTargets) != 2 || got.FanOutTargets[0] != "eu-1" {
		t.Fatalf("persisted FanOutTargets = %q, want [eu-1 us-1]", got.FanOutTargets)
	}
	_ = obj
}

func TestCreate_FanOutAllBlankIsNoFanOut(t *testing.T) {
	t.Parallel()
	svc, _, _ := newCollService(t)
	c, err := svc.Create(context.Background(), "plain", 10, "", "", []string{"", "  "})
	if err != nil {
		t.Fatalf("Create (all-blank targets) = %v, want nil", err)
	}
	if c.IsFanOut() {
		t.Error("IsFanOut = true for an all-blank target list, want false")
	}
}

func TestCreate_FanOutDuplicateTargetRejected(t *testing.T) {
	t.Parallel()
	svc, _, _ := newCollService(t)
	if _, err := svc.Create(context.Background(), "everywhere", 10, "", "", []string{"eu", "eu"}); !errors.Is(err, execution.ErrFanOutTargetDuplicate) {
		t.Fatalf("Create (duplicate target) err = %v, want ErrFanOutTargetDuplicate", err)
	}
}
