package repositorytest

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

// Repository is the full repository surface implemented by both the fake and
// the MySQL adapter. Cross-aggregate behaviour (e.g. "is this scenario in use") is
// tested against this combined interface.
type Repository interface {
	ports.ProjectRepository
	ports.ScenarioRepository
	ports.ExecutionRepository
	// ReportStore rides along because the last-run summary is a join across
	// aggregates: seeding reports is the only way to pin which execution's
	// report LatestRunsForScenarios must read.
	ports.ReportStore
}

// NewRepo returns a fresh, empty Repository for a single subtest.
type NewRepo func(t *testing.T) Repository

// RunScenarioRepositoryContract exercises ScenarioRepository behaviour.
func RunScenarioRepositoryContract(t *testing.T, newRepo NewRepo) {
	t.Helper()

	// Portability decides which engines a scenario may run on. If it does not
	// survive a round trip the domain refuses every engine, so the contract
	// pins it for the fake and every real adapter alike.
	t.Run("PortabilityRoundTrips", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		portable, err := scenario.New("portable", 10)
		if err != nil {
			t.Fatalf("scenario.New: %v", err)
		}
		portableID, err := repo.CreateScenario(ctx, portable)
		if err != nil {
			t.Fatalf("CreateScenario(portable): %v", err)
		}

		native, err := scenario.NewNative("imported", 10, taurus.ExecutorJMeter)
		if err != nil {
			t.Fatalf("scenario.NewNative: %v", err)
		}
		nativeID, err := repo.CreateScenario(ctx, native)
		if err != nil {
			t.Fatalf("CreateScenario(native): %v", err)
		}

		gotPortable, err := repo.GetScenario(ctx, portableID)
		if err != nil {
			t.Fatalf("GetScenario(portable): %v", err)
		}
		if gotPortable.Kind != scenario.KindPortable || gotPortable.Engine != "" {
			t.Errorf("portable round trip = kind %q engine %q, want portable with no engine",
				gotPortable.Kind, gotPortable.Engine)
		}
		if err := gotPortable.Validate(); err != nil {
			t.Errorf("portable round trip does not validate: %v", err)
		}

		gotNative, err := repo.GetScenario(ctx, nativeID)
		if err != nil {
			t.Fatalf("GetScenario(native): %v", err)
		}
		if gotNative.Kind != scenario.KindNative || gotNative.Engine != taurus.ExecutorJMeter {
			t.Errorf("native round trip = kind %q engine %q, want native/jmeter",
				gotNative.Kind, gotNative.Engine)
		}
		if err := gotNative.CanRunOn(taurus.ExecutorK6); err == nil {
			t.Error("a JMeter-pinned scenario read back accepted k6")
		}

		// Uploading a script pins a scenario to the engine that runs it, so the
		// change must be persisted -- otherwise a scenario reverts to portable
		// and stops compiling.
		if err := repo.SetScenarioKind(ctx, portableID, scenario.KindNative, taurus.ExecutorK6); err != nil {
			t.Fatalf("SetScenarioKind: %v", err)
		}
		pinned, err := repo.GetScenario(ctx, portableID)
		if err != nil {
			t.Fatalf("GetScenario after pinning: %v", err)
		}
		if pinned.Kind != scenario.KindNative || pinned.Engine != taurus.ExecutorK6 {
			t.Errorf("after pinning = kind %q engine %q, want native/k6", pinned.Kind, pinned.Engine)
		}
		if err := repo.SetScenarioKind(ctx, 999999, scenario.KindNative, taurus.ExecutorK6); err == nil {
			t.Error("SetScenarioKind on a missing scenario succeeded")
		}

		// Setting the kind a scenario already has is a no-op UPDATE, and
		// MySQL reports RowsAffected 0 for those -- exactly as for a missing
		// row. It must still succeed: re-uploading a same-engine script pins
		// the scenario to values it already has (phase 36: this 404'd live).
		if err := repo.SetScenarioKind(ctx, portableID, scenario.KindNative, taurus.ExecutorK6); err != nil {
			t.Fatalf("SetScenarioKind(unchanged values) = %v, want nil: the scenario exists; 0 rows affected only means the UPDATE changed nothing", err)
		}

		// An execution's engine selection must survive too, or a run would
		// silently fall back to the deployment default and measure a workload
		// nobody asked for. Its target cluster must round-trip for the same
		// reason -- a lost cluster would silently run on the default.
		exe := execution.Execution{Name: "on-k6", ProjectID: 10, Engine: taurus.ExecutorK6, Cluster: "prod-eu"}
		exeID, err := repo.CreateExecution(ctx, exe)
		if err != nil {
			t.Fatalf("CreateExecution(with engine): %v", err)
		}
		gotExe, err := repo.GetExecution(ctx, exeID)
		if err != nil {
			t.Fatalf("GetExecution: %v", err)
		}
		if gotExe.Engine != taurus.ExecutorK6 {
			t.Errorf("execution engine round trip = %q, want k6", gotExe.Engine)
		}
		if gotExe.Cluster != "prod-eu" {
			t.Errorf("execution cluster round trip = %q, want prod-eu", gotExe.Cluster)
		}

		// An execution with no cluster (the pre-Phase-8 shape) must load back
		// empty -- the default -- not some placeholder.
		defExeID, err := repo.CreateExecution(ctx, execution.Execution{Name: "on-default", ProjectID: 10})
		if err != nil {
			t.Fatalf("CreateExecution(no cluster): %v", err)
		}
		gotDef, err := repo.GetExecution(ctx, defExeID)
		if err != nil {
			t.Fatalf("GetExecution(no cluster): %v", err)
		}
		if gotDef.Cluster != "" {
			t.Errorf("execution with no cluster round trip = %q, want empty", gotDef.Cluster)
		}

		// Listing must carry portability too: engine selection is offered from
		// list views, not only from a single fetch.
		listed, err := repo.ListScenariosByProject(ctx, 10)
		if err != nil {
			t.Fatalf("ListScenariosByProject: %v", err)
		}
		for _, s := range listed {
			if err := s.Validate(); err != nil {
				t.Errorf("listed scenario %q does not validate: %v", s.Name, err)
			}
		}
	})

	// A portable scenario's declarative requests fragment: nothing uploaded
	// yet must read as ErrNotFound (not an empty byte slice, which would be
	// indistinguishable from "uploaded an empty fragment"), and a later
	// upload must overwrite rather than merge with the one before it.
	t.Run("RequestsRoundTrip", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		id := mustCreateScenario(t, repo, "portable", 10)

		if _, err := repo.GetScenarioRequests(ctx, id); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("GetScenarioRequests before any upload = %v, want ErrNotFound", err)
		}

		first := []byte("requests:\n  - url: http://example.com/one\n")
		if err := repo.SetScenarioRequests(ctx, id, first); err != nil {
			t.Fatalf("SetScenarioRequests: %v", err)
		}
		got, err := repo.GetScenarioRequests(ctx, id)
		if err != nil {
			t.Fatalf("GetScenarioRequests: %v", err)
		}
		if string(got) != string(first) {
			t.Errorf("GetScenarioRequests = %q, want %q", got, first)
		}

		second := []byte("requests:\n  - url: http://example.com/two\n")
		if err := repo.SetScenarioRequests(ctx, id, second); err != nil {
			t.Fatalf("SetScenarioRequests (overwrite): %v", err)
		}
		got, err = repo.GetScenarioRequests(ctx, id)
		if err != nil {
			t.Fatalf("GetScenarioRequests after overwrite: %v", err)
		}
		if string(got) != string(second) {
			t.Errorf("GetScenarioRequests after overwrite = %q, want %q (not merged with the first upload)", got, second)
		}
	})

	t.Run("UpdateScenarioRewritesMutableFieldsOnly", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		id := mustCreateScenario(t, repo, "portable", 10)
		before, err := repo.GetScenario(ctx, id)
		if err != nil {
			t.Fatalf("GetScenario: %v", err)
		}

		// The restore path's apply step: mutable fields rewritten, identity,
		// ownership, and provenance untouched.
		rewound := scenario.Scenario{
			ID: id, Name: "rewound", ProjectID: before.ProjectID,
			Kind: scenario.KindNative, Engine: taurus.ExecutorK6,
		}
		if err := repo.UpdateScenario(ctx, rewound); err != nil {
			t.Fatalf("UpdateScenario: %v", err)
		}
		after, err := repo.GetScenario(ctx, id)
		if err != nil {
			t.Fatalf("GetScenario after update: %v", err)
		}
		if after.Name != "rewound" || after.Kind != scenario.KindNative || after.Engine != taurus.ExecutorK6 {
			t.Errorf("after update = %q %q %q, want rewound/native/k6", after.Name, after.Kind, after.Engine)
		}
		if !after.CreatedTime.Equal(before.CreatedTime) {
			t.Errorf("created_time moved from %v to %v; provenance is not restorable", before.CreatedTime, after.CreatedTime)
		}

		// A no-op update (same values) is a success -- RowsAffected 0 for a
		// no-op UPDATE must not read as missing (SetScenarioKind's live bug,
		// phase 36).
		if err := repo.UpdateScenario(ctx, rewound); err != nil {
			t.Errorf("UpdateScenario(unchanged values) = %v, want nil", err)
		}
		if err := repo.UpdateScenario(ctx, scenario.Scenario{ID: 999999, Name: "ghost", Kind: scenario.KindPortable}); err == nil {
			t.Error("UpdateScenario on a missing scenario succeeded")
		}
	})

	t.Run("DeleteScenarioRequestsRemovesTheFragment", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		id := mustCreateScenario(t, repo, "portable", 10)
		// Deleting what was never stored is a no-op, not an error.
		if err := repo.DeleteScenarioRequests(ctx, id); err != nil {
			t.Fatalf("DeleteScenarioRequests(none stored) = %v, want nil", err)
		}
		if err := repo.SetScenarioRequests(ctx, id, []byte("requests: []\n")); err != nil {
			t.Fatalf("SetScenarioRequests: %v", err)
		}
		if err := repo.DeleteScenarioRequests(ctx, id); err != nil {
			t.Fatalf("DeleteScenarioRequests: %v", err)
		}
		if _, err := repo.GetScenarioRequests(ctx, id); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("GetScenarioRequests after delete = %v, want ErrNotFound", err)
		}
	})

	t.Run("CreateGetListDelete", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		id := mustCreateScenario(t, repo, "smoke", 10)
		got, err := repo.GetScenario(ctx, id)
		if err != nil {
			t.Fatalf("GetScenario: %v", err)
		}
		if got.ID != id || got.Name != "smoke" || got.ProjectID != 10 || got.CreatedTime.IsZero() {
			t.Fatalf("GetScenario = %+v, want id=%d name=smoke project=10 with timestamp", got, id)
		}

		mustCreateScenario(t, repo, "second", 10)
		mustCreateScenario(t, repo, "other-project", 99)
		inProject, err := repo.ListScenariosByProject(ctx, 10)
		if err != nil {
			t.Fatalf("ListScenariosByProject: %v", err)
		}
		if names := planNames(inProject); !equalStringSet(names, []string{"smoke", "second"}) {
			t.Fatalf("ListScenariosByProject(10) = %v, want [smoke second]", names)
		}

		if err := repo.DeleteScenario(ctx, id); err != nil {
			t.Fatalf("DeleteScenario: %v", err)
		}
		if _, err := repo.GetScenario(ctx, id); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("GetScenario after delete = %v, want ErrNotFound", err)
		}
		if err := repo.DeleteScenario(ctx, id); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("DeleteScenario(missing) = %v, want ErrNotFound", err)
		}
	})

	t.Run("GetMissingReturnsNotFound", func(t *testing.T) {
		repo := newRepo(t)
		if _, err := repo.GetScenario(context.Background(), 987654); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("GetScenario(missing) = %v, want ErrNotFound", err)
		}
	})

	// Templates are scenarios with a flag: the flag and its slug must
	// round-trip, the template catalog must list them, and a template must
	// validate (global: no project, no tenant).
	t.Run("TemplatesRoundTrip", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		tpl, err := scenario.NewTemplate("HTTPbin baseline", "httpbin-baseline")
		if err != nil {
			t.Fatalf("NewTemplate: %v", err)
		}
		tplID, err := repo.CreateScenario(ctx, tpl)
		if err != nil {
			t.Fatalf("CreateScenario(template): %v", err)
		}
		// An ordinary scenario beside it, so the catalog cannot be "everything".
		mustCreateScenario(t, repo, "smoke", 10)

		got, err := repo.GetScenario(ctx, tplID)
		if err != nil {
			t.Fatalf("GetScenario(template): %v", err)
		}
		if !got.IsTemplate || got.TemplateName != "httpbin-baseline" {
			t.Errorf("GetScenario = is_template=%v template_name=%q, want true/httpbin-baseline", got.IsTemplate, got.TemplateName)
		}
		if err := got.Validate(); err != nil {
			t.Errorf("template round trip does not validate: %v", err)
		}

		templates, err := repo.ListTemplates(ctx)
		if err != nil {
			t.Fatalf("ListTemplates: %v", err)
		}
		if len(templates) != 1 || templates[0].ID != tplID || templates[0].TemplateName != "httpbin-baseline" {
			t.Fatalf("ListTemplates = %+v, want only the httpbin-baseline template", templates)
		}

		// A template is projectless by design (project_id 0): the per-project
		// list is the ordinary scenarios' surface and must not show it.
		inProject, err := repo.ListScenariosByProject(ctx, 10)
		if err != nil {
			t.Fatalf("ListScenariosByProject: %v", err)
		}
		if len(inProject) != 1 || inProject[0].IsTemplate {
			t.Fatalf("ListScenariosByProject(10) = %+v, want only the non-template smoke", inProject)
		}
	})

	t.Run("Files", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id := mustCreateScenario(t, repo, "smoke", 10)

		if err := repo.AddScenarioFile(ctx, id, "test.jmx", true); err != nil {
			t.Fatalf("AddScenarioFile(test): %v", err)
		}
		if err := repo.AddScenarioFile(ctx, id, "users.csv", false); err != nil {
			t.Fatalf("AddScenarioFile(data): %v", err)
		}

		// One JMX slot per scenario; a second test file conflicts.
		if err := repo.AddScenarioFile(ctx, id, "other.jmx", true); !errors.Is(err, ports.ErrFileExists) {
			t.Fatalf("AddScenarioFile(second test) = %v, want ErrFileExists", err)
		}
		if err := repo.AddScenarioFile(ctx, id, "users.csv", false); !errors.Is(err, ports.ErrFileExists) {
			t.Fatalf("AddScenarioFile(dup data) = %v, want ErrFileExists", err)
		}

		files, err := repo.ScenarioFilesFor(ctx, id)
		if err != nil {
			t.Fatalf("ScenarioFilesFor: %v", err)
		}
		if files.TestFile != "test.jmx" || !equalStringSet(files.Data, []string{"users.csv"}) {
			t.Fatalf("ScenarioFilesFor = %+v, want test.jmx + [users.csv]", files)
		}

		if err := repo.DeleteScenarioFile(ctx, id, "users.csv", false); err != nil {
			t.Fatalf("DeleteScenarioFile(data): %v", err)
		}
		if err := repo.DeleteScenarioFile(ctx, id, "users.csv", false); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("DeleteScenarioFile(missing) = %v, want ErrNotFound", err)
		}
		if err := repo.DeleteScenarioFile(ctx, id, "test.jmx", true); err != nil {
			t.Fatalf("DeleteScenarioFile(test): %v", err)
		}
	})

	t.Run("ScenarioInUse", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		scenarioID := mustCreateScenario(t, repo, "smoke", 10)

		inUse, err := repo.ScenarioInUse(ctx, scenarioID)
		if err != nil {
			t.Fatalf("ScenarioInUse: %v", err)
		}
		if inUse {
			t.Fatal("ScenarioInUse = true for an unused scenario")
		}

		collID := mustCreateExecution(t, repo, "peak", 10)
		if err := repo.StoreLoadProfile(ctx, collID, false, []loadprofile.Entry{
			{Name: "smoke", ScenarioID: scenarioID, Engines: 1, Concurrency: 1, Duration: 60},
		}); err != nil {
			t.Fatalf("StoreLoadProfile: %v", err)
		}

		inUse, err = repo.ScenarioInUse(ctx, scenarioID)
		if err != nil {
			t.Fatalf("ScenarioInUse: %v", err)
		}
		if !inUse {
			t.Fatal("ScenarioInUse = false after the scenario was added to an execution")
		}
	})

	// ListExecutionsByScenario is the scenario-first lens: every execution
	// whose load profile binds the scenario, newest first -- whatever kind
	// it is and however many scenarios each execution also runs.
	t.Run("ListExecutionsByScenarioNewestFirst", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		scenarioA := mustCreateScenario(t, repo, "alpha", 10)
		scenarioB := mustCreateScenario(t, repo, "beta", 10)

		first := mustCreateExecution(t, repo, "first", 10)
		if err := repo.StoreLoadProfile(ctx, first, false, []loadprofile.Entry{
			{Name: "alpha", ScenarioID: scenarioA, Engines: 1, Concurrency: 1, Duration: 60},
		}); err != nil {
			t.Fatalf("StoreLoadProfile(first): %v", err)
		}
		second := mustCreateExecution(t, repo, "second", 10)
		if err := repo.StoreLoadProfile(ctx, second, false, []loadprofile.Entry{
			{Name: "alpha", ScenarioID: scenarioA, Engines: 1, Concurrency: 1, Duration: 60},
			{Name: "beta", ScenarioID: scenarioB, Engines: 1, Concurrency: 1, Duration: 60},
		}); err != nil {
			t.Fatalf("StoreLoadProfile(second): %v", err)
		}
		third := mustCreateExecution(t, repo, "third", 10)
		if err := repo.StoreLoadProfile(ctx, third, false, []loadprofile.Entry{
			{Name: "beta", ScenarioID: scenarioB, Engines: 1, Concurrency: 1, Duration: 60},
		}); err != nil {
			t.Fatalf("StoreLoadProfile(third): %v", err)
		}
		// An execution bound to no scenario at all must appear in neither list.
		_ = mustCreateExecution(t, repo, "unbound", 10)

		forA, err := repo.ListExecutionsByScenario(ctx, scenarioA)
		if err != nil {
			t.Fatalf("ListExecutionsByScenario(alpha): %v", err)
		}
		if len(forA) != 2 || forA[0].ID != second || forA[1].ID != first {
			t.Fatalf("ListExecutionsByScenario(alpha) = %v, want [second first] newest-first", idsOf(forA))
		}

		// An execution bound to two scenarios appears in both lists.
		forB, err := repo.ListExecutionsByScenario(ctx, scenarioB)
		if err != nil {
			t.Fatalf("ListExecutionsByScenario(beta): %v", err)
		}
		if len(forB) != 2 || forB[0].ID != third || forB[1].ID != second {
			t.Fatalf("ListExecutionsByScenario(beta) = %v, want [third second] newest-first", idsOf(forB))
		}

		// A scenario nothing runs, and a scenario id that does not exist at
		// all: empty results, not errors.
		none, err := repo.ListExecutionsByScenario(ctx, 424242)
		if err != nil {
			t.Fatalf("ListExecutionsByScenario(unknown): %v", err)
		}
		if len(none) != 0 {
			t.Fatalf("ListExecutionsByScenario(unknown) = %v, want empty", idsOf(none))
		}
	})
}

// RunExecutionRepositoryContract exercises ExecutionRepository behaviour.
func RunExecutionRepositoryContract(t *testing.T, newRepo NewRepo) {
	t.Helper()

	t.Run("CreateGetListDelete", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		id := mustCreateExecution(t, repo, "peak", 10)
		got, err := repo.GetExecution(ctx, id)
		if err != nil {
			t.Fatalf("GetExecution: %v", err)
		}
		if got.ID != id || got.Name != "peak" || got.ProjectID != 10 || got.CSVSplit || got.CreatedTime.IsZero() {
			t.Fatalf("GetExecution = %+v, want id=%d name=peak project=10 csv_split=false with timestamp", got, id)
		}
		if got.Kind != execution.KindNormal || got.CPU != "" || got.Memory != "" {
			t.Fatalf("GetExecution = %+v, want kind=normal, cpu/memory empty (execution.New's own defaults)", got)
		}

		mustCreateExecution(t, repo, "other", 99)
		inProject, err := repo.ListExecutionsByProject(ctx, 10)
		if err != nil {
			t.Fatalf("ListExecutionsByProject: %v", err)
		}
		if len(inProject) != 1 || inProject[0].Name != "peak" {
			t.Fatalf("ListExecutionsByProject(10) = %+v, want only peak", inProject)
		}

		if err := repo.DeleteExecution(ctx, id); err != nil {
			t.Fatalf("DeleteExecution: %v", err)
		}
		if err := repo.DeleteExecution(ctx, id); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("DeleteExecution(missing) = %v, want ErrNotFound", err)
		}
	})

	// DeleteExecution must take its scenario links with it: the schema has
	// no FK cascades, so an execution_scenario row left behind keeps
	// ScenarioInUse true forever and 409s every delete of the linked
	// scenario (and through it, its project) -- phase 36, hit live.
	t.Run("DeleteExecutionDropsItsScenarioLinks", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		scenarioID := mustCreateScenario(t, repo, "smoke", 10)
		collID := mustCreateExecution(t, repo, "peak", 10)
		if err := repo.StoreLoadProfile(ctx, collID, false, []loadprofile.Entry{
			{Name: "smoke", ScenarioID: scenarioID, Engines: 1, Concurrency: 1, Duration: 60},
		}); err != nil {
			t.Fatalf("StoreLoadProfile: %v", err)
		}

		if err := repo.DeleteExecution(ctx, collID); err != nil {
			t.Fatalf("DeleteExecution: %v", err)
		}
		inUse, err := repo.ScenarioInUse(ctx, scenarioID)
		if err != nil {
			t.Fatalf("ScenarioInUse: %v", err)
		}
		if inUse {
			t.Fatal("ScenarioInUse = true after the execution was deleted (orphaned execution_scenario row)")
		}
		// The operator-visible symptom: the scenario delete that 409'd.
		if err := repo.DeleteScenario(ctx, scenarioID); err != nil {
			t.Fatalf("DeleteScenario after the execution was deleted: %v", err)
		}
	})

	// ListExecutionsByProjects is the operator listing: every execution of
	// every visible project, newest first, regardless of which project any
	// single execution belongs to. An empty project list must widen to
	// nothing, not to everything -- that is the scoping guard.
	t.Run("ListExecutionsByProjectsNewestFirst", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		first := mustCreateExecution(t, repo, "first", 10)
		mustCreateExecution(t, repo, "other-project", 99)
		last := mustCreateExecution(t, repo, "last", 10)

		// One project: only that project's executions.
		proj10, err := repo.ListExecutionsByProjects(ctx, []int64{10})
		if err != nil {
			t.Fatalf("ListExecutionsByProjects([10]): %v", err)
		}
		if len(proj10) != 2 || proj10[0].ID != last || proj10[1].ID != first {
			t.Fatalf("ListExecutionsByProjects([10]) = %v, want [last first] newest-first", idsOf(proj10))
		}

		// Both projects, explicit: everything, still newest first.
		both, err := repo.ListExecutionsByProjects(ctx, []int64{10, 99})
		if err != nil {
			t.Fatalf("ListExecutionsByProjects([10,99]): %v", err)
		}
		if len(both) != 3 || both[0].ID != last || both[2].ID != first {
			t.Fatalf("ListExecutionsByProjects([10,99]) = %v, want [last other first]", idsOf(both))
		}

		// Unused project: empty result, not an error.
		empty, err := repo.ListExecutionsByProjects(ctx, []int64{77})
		if err != nil {
			t.Fatalf("ListExecutionsByProjects([77]): %v", err)
		}
		if len(empty) != 0 {
			t.Fatalf("ListExecutionsByProjects([77]) = %v, want empty", idsOf(empty))
		}

		// Empty project list widens to nothing, never to every execution.
		none, err := repo.ListExecutionsByProjects(ctx, nil)
		if err != nil {
			t.Fatalf("ListExecutionsByProjects(nil): %v", err)
		}
		if len(none) != 0 {
			t.Fatalf("ListExecutionsByProjects(nil) = %v, want empty", idsOf(none))
		}
	})

	// A CalibrateEngine execution pins its pod size -- both round-trip
	// exactly, distinct from a Normal execution's own empty defaults.
	t.Run("CalibrateEngineKindAndPinnedResourcesRoundTrip", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		c, err := execution.New("engine-calibration", 10)
		if err != nil {
			t.Fatalf("execution.New: %v", err)
		}
		c.Kind = execution.KindCalibrateEngine
		c.CPU, c.Memory = "2", "1Gi"
		id, err := repo.CreateExecution(ctx, c)
		if err != nil {
			t.Fatalf("CreateExecution: %v", err)
		}

		got, err := repo.GetExecution(ctx, id)
		if err != nil {
			t.Fatalf("GetExecution: %v", err)
		}
		if got.Kind != execution.KindCalibrateEngine || got.CPU != "2" || got.Memory != "1Gi" {
			t.Fatalf("GetExecution = %+v, want kind=calibrate_engine cpu=2 memory=1Gi", got)
		}
	})

	t.Run("Files", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id := mustCreateExecution(t, repo, "peak", 10)

		if err := repo.AddExecutionFile(ctx, id, "shared.csv"); err != nil {
			t.Fatalf("AddExecutionFile: %v", err)
		}
		if err := repo.AddExecutionFile(ctx, id, "shared.csv"); !errors.Is(err, ports.ErrFileExists) {
			t.Fatalf("AddExecutionFile(dup) = %v, want ErrFileExists", err)
		}
		files, err := repo.ExecutionFilesFor(ctx, id)
		if err != nil {
			t.Fatalf("ExecutionFilesFor: %v", err)
		}
		if !equalStringSet(files, []string{"shared.csv"}) {
			t.Fatalf("ExecutionFilesFor = %v, want [shared.csv]", files)
		}
		if err := repo.DeleteExecutionFile(ctx, id, "shared.csv"); err != nil {
			t.Fatalf("DeleteExecutionFile: %v", err)
		}
		if err := repo.DeleteExecutionFile(ctx, id, "shared.csv"); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("DeleteExecutionFile(missing) = %v, want ErrNotFound", err)
		}
	})

	t.Run("ExecutionScenariosStoreReplacesAndSetsCSVSplit", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id := mustCreateExecution(t, repo, "peak", 10)

		first := []loadprofile.Entry{
			{Name: "a", ScenarioID: 1, Engines: 2, Concurrency: 10, Duration: 60, Throughput: 750},
			{Name: "b", ScenarioID: 2, Engines: 3, Concurrency: 10, Duration: 60},
		}
		if err := repo.StoreLoadProfile(ctx, id, true, first); err != nil {
			t.Fatalf("StoreLoadProfile(first): %v", err)
		}
		got, err := repo.LoadProfileFor(ctx, id)
		if err != nil {
			t.Fatalf("LoadProfileFor: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("LoadProfileFor len = %d, want 2", len(got))
		}
		// A rate limit that does not survive persistence would let the run go as
		// fast as it could and measure something nobody asked for.
		byScenario := map[int64]loadprofile.Entry{}
		for _, e := range got {
			byScenario[e.ScenarioID] = e
		}
		if byScenario[1].Throughput != 750 {
			t.Errorf("throughput round trip = %d, want 750", byScenario[1].Throughput)
		}
		if byScenario[2].Throughput != 0 {
			t.Errorf("unlimited throughput round trip = %d, want 0", byScenario[2].Throughput)
		}
		if c, _ := repo.GetExecution(ctx, id); !c.CSVSplit {
			t.Fatalf("execution CSVSplit = false after store, want true")
		}

		// Storing a smaller set replaces (not merges) the previous scenarios.
		second := []loadprofile.Entry{{Name: "a", ScenarioID: 1, Engines: 5, Concurrency: 20, Duration: 60}}
		if err := repo.StoreLoadProfile(ctx, id, false, second); err != nil {
			t.Fatalf("StoreLoadProfile(second): %v", err)
		}
		got, _ = repo.LoadProfileFor(ctx, id)
		if len(got) != 1 || got[0].ScenarioID != 1 || got[0].Engines != 5 {
			t.Fatalf("LoadProfileFor after replace = %+v, want single scenario 1 with 5 engines", got)
		}
		if c, _ := repo.GetExecution(ctx, id); c.CSVSplit {
			t.Fatalf("execution CSVSplit = true after second store, want false")
		}
	})

	t.Run("StoreOnMissingExecution", func(t *testing.T) {
		repo := newRepo(t)
		err := repo.StoreLoadProfile(context.Background(), 987654, false,
			[]loadprofile.Entry{{ScenarioID: 1, Engines: 1, Concurrency: 1, Duration: 1}})
		if !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("StoreLoadProfile(missing execution) = %v, want ErrNotFound", err)
		}
	})

	t.Run("CriteriaForNeverStoredReturnsEmptyNotNil", func(t *testing.T) {
		repo := newRepo(t)
		id := mustCreateExecution(t, repo, "peak", 10)

		got, err := repo.CriteriaFor(context.Background(), id)
		if err != nil {
			t.Fatalf("CriteriaFor: %v", err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("CriteriaFor(never set) = %v, want empty (never nil)", got)
		}
	})

	t.Run("SetExecutionCriteriaRoundTripsInOrder", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id := mustCreateExecution(t, repo, "peak", 10)

		want := []string{"failures>10%", "p95>500ms", "p99>2s"}
		if err := repo.SetExecutionCriteria(ctx, id, want); err != nil {
			t.Fatalf("SetExecutionCriteria: %v", err)
		}
		got, err := repo.CriteriaFor(ctx, id)
		if err != nil {
			t.Fatalf("CriteriaFor: %v", err)
		}
		if len(got) != len(want) {
			t.Fatalf("CriteriaFor = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("CriteriaFor = %v, want %v in this exact order", got, want)
			}
		}
	})

	// A later SetExecutionCriteria call replaces the whole set, not appends
	// to it -- the same "replace, not accumulate" contract StoreLoadProfile
	// already guarantees for load profile entries.
	t.Run("SetExecutionCriteriaReplacesRatherThanAppends", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id := mustCreateExecution(t, repo, "peak", 10)

		if err := repo.SetExecutionCriteria(ctx, id, []string{"failures>10%", "p95>500ms"}); err != nil {
			t.Fatalf("SetExecutionCriteria (first): %v", err)
		}
		if err := repo.SetExecutionCriteria(ctx, id, []string{"failures>50%"}); err != nil {
			t.Fatalf("SetExecutionCriteria (second): %v", err)
		}
		got, err := repo.CriteriaFor(ctx, id)
		if err != nil {
			t.Fatalf("CriteriaFor: %v", err)
		}
		if len(got) != 1 || got[0] != "failures>50%" {
			t.Fatalf("CriteriaFor after replace = %v, want exactly [failures>50%%]", got)
		}
	})

	t.Run("SetExecutionCriteriaToEmptyClearsThem", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id := mustCreateExecution(t, repo, "peak", 10)

		if err := repo.SetExecutionCriteria(ctx, id, []string{"failures>10%"}); err != nil {
			t.Fatalf("SetExecutionCriteria (set): %v", err)
		}
		if err := repo.SetExecutionCriteria(ctx, id, nil); err != nil {
			t.Fatalf("SetExecutionCriteria (clear): %v", err)
		}
		got, err := repo.CriteriaFor(ctx, id)
		if err != nil {
			t.Fatalf("CriteriaFor: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("CriteriaFor after clear = %v, want none", got)
		}
	})

	// StoreExecutionConfig is the combined write executionapp.StoreConfig
	// actually uses -- one call replaces the load profile and criteria
	// together, so a caller never observes one half updated without the
	// other.
	t.Run("StoreExecutionConfigRoundTripsBothTogether", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id := mustCreateExecution(t, repo, "peak", 10)
		scenarioID := mustCreateScenario(t, repo, "smoke", 10)

		entries := []loadprofile.Entry{{ScenarioID: scenarioID, Engines: 2, Concurrency: 10, Duration: 60}}
		criteria := []string{"failures>10%", "p95>500ms"}
		if err := repo.StoreExecutionConfig(ctx, id, true, entries, criteria); err != nil {
			t.Fatalf("StoreExecutionConfig: %v", err)
		}

		gotEntries, err := repo.LoadProfileFor(ctx, id)
		if err != nil {
			t.Fatalf("LoadProfileFor: %v", err)
		}
		if len(gotEntries) != 1 || gotEntries[0].ScenarioID != scenarioID || gotEntries[0].Engines != 2 {
			t.Fatalf("LoadProfileFor = %+v, want the stored entry", gotEntries)
		}
		gotCriteria, err := repo.CriteriaFor(ctx, id)
		if err != nil {
			t.Fatalf("CriteriaFor: %v", err)
		}
		if len(gotCriteria) != 2 || gotCriteria[0] != "failures>10%" || gotCriteria[1] != "p95>500ms" {
			t.Fatalf("CriteriaFor = %v, want %v", gotCriteria, criteria)
		}
	})

	// Phase 90: mode provenance round-trips through the load profile --
	// an advanced entry stores and reads back as no mode at all (NULL on
	// the column), and a resolved mode entry keeps its mode next to the
	// numbers the server filled. The fake and the real adapter must agree
	// here so the shared conformance meaning of "stored config" stays one
	// thing.
	t.Run("LoadProfileRoundTripsModeProvenance", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id := mustCreateExecution(t, repo, "peak", 10)
		scenarioID := mustCreateScenario(t, repo, "smoke", 10)

		entries := []loadprofile.Entry{
			{ScenarioID: scenarioID, Engines: 2, Concurrency: 10, Duration: 60},
			{ScenarioID: scenarioID + 1, Engines: 4, Concurrency: 375, Rampup: 120, Duration: 600, Throughput: 500, Mode: "burst"},
			{ScenarioID: scenarioID + 2, Engines: 1, Concurrency: 60, Rampup: 60, Duration: 3600, Throughput: 50, Mode: "soak"},
		}
		if err := repo.StoreExecutionConfig(ctx, id, false, entries, nil); err != nil {
			t.Fatalf("StoreExecutionConfig: %v", err)
		}
		got, err := repo.LoadProfileFor(ctx, id)
		if err != nil {
			t.Fatalf("LoadProfileFor: %v", err)
		}
		byScenario := map[int64]loadprofile.Entry{}
		for _, e := range got {
			byScenario[e.ScenarioID] = e
		}
		if m := byScenario[scenarioID].Mode; m != "" {
			t.Errorf("advanced entry mode = %q, want \"\" (NULL reads back as advanced)", m)
		}
		if m := byScenario[scenarioID+1].Mode; m != "burst" {
			t.Errorf("burst entry mode = %q, want burst", m)
		}
		if m := byScenario[scenarioID+2].Mode; m != "soak" {
			t.Errorf("soak entry mode = %q, want soak", m)
		}

		// Replacing with mode-less entries clears the provenance: mode is
		// per-entry state, never a sticky column default.
		advanced := []loadprofile.Entry{{ScenarioID: scenarioID, Engines: 1, Concurrency: 1, Duration: 30}}
		if err := repo.StoreLoadProfile(ctx, id, false, advanced); err != nil {
			t.Fatalf("StoreLoadProfile (advanced): %v", err)
		}
		got, err = repo.LoadProfileFor(ctx, id)
		if err != nil {
			t.Fatalf("LoadProfileFor after replace: %v", err)
		}
		if len(got) != 1 || got[0].Mode != "" {
			t.Fatalf("LoadProfileFor after replace = %+v, want a single mode-less entry", got)
		}
	})

	// A later call replaces both halves, not just one -- the same
	// "replace, not accumulate" contract each half already guarantees on
	// its own.
	t.Run("StoreExecutionConfigReplacesBothOnASecondCall", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id := mustCreateExecution(t, repo, "peak", 10)
		scenarioID := mustCreateScenario(t, repo, "smoke", 10)

		first := []loadprofile.Entry{{ScenarioID: scenarioID, Engines: 2, Concurrency: 10, Duration: 60}}
		if err := repo.StoreExecutionConfig(ctx, id, false, first, []string{"failures>10%"}); err != nil {
			t.Fatalf("StoreExecutionConfig (first): %v", err)
		}
		if err := repo.StoreExecutionConfig(ctx, id, false, nil, nil); err != nil {
			t.Fatalf("StoreExecutionConfig (second, empty): %v", err)
		}

		gotEntries, err := repo.LoadProfileFor(ctx, id)
		if err != nil {
			t.Fatalf("LoadProfileFor: %v", err)
		}
		if len(gotEntries) != 0 {
			t.Fatalf("LoadProfileFor after replace = %+v, want none", gotEntries)
		}
		gotCriteria, err := repo.CriteriaFor(ctx, id)
		if err != nil {
			t.Fatalf("CriteriaFor: %v", err)
		}
		if len(gotCriteria) != 0 {
			t.Fatalf("CriteriaFor after replace = %v, want none", gotCriteria)
		}
	})

	t.Run("StoreExecutionConfigMissingExecutionReturnsNotFound", func(t *testing.T) {
		repo := newRepo(t)
		err := repo.StoreExecutionConfig(context.Background(), 987654, false,
			[]loadprofile.Entry{{ScenarioID: 1, Engines: 1, Concurrency: 1, Duration: 1}}, nil)
		if !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("StoreExecutionConfig(missing execution) = %v, want ErrNotFound", err)
		}
	})

	// The pending correlation id is the trace id a Deploy minted, held until
	// the next Trigger stamps it onto the run it precedes. An execution
	// deployed before Phase 10 has none: empty, never an error.
	t.Run("PendingCorrelationIDNeverStoredIsEmpty", func(t *testing.T) {
		repo := newRepo(t)
		id := mustCreateExecution(t, repo, "peak", 10)

		got, err := repo.PendingCorrelationID(context.Background(), id)
		if err != nil {
			t.Fatalf("PendingCorrelationID(never set): %v", err)
		}
		if got != "" {
			t.Fatalf("PendingCorrelationID(never set) = %q, want empty", got)
		}
	})

	t.Run("SetPendingCorrelationIDRoundTrips", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id := mustCreateExecution(t, repo, "peak", 10)

		want := "4bf92f3577b34da6a3ce929d0e0e4736"
		if err := repo.SetPendingCorrelationID(ctx, id, want); err != nil {
			t.Fatalf("SetPendingCorrelationID: %v", err)
		}
		got, err := repo.PendingCorrelationID(ctx, id)
		if err != nil {
			t.Fatalf("PendingCorrelationID: %v", err)
		}
		if got != want {
			t.Fatalf("PendingCorrelationID = %q, want %q", got, want)
		}
	})

	// Last deploy wins, by design: the next Trigger runs against whichever
	// pods the latest Deploy created, so the id it should stamp is the latest
	// one -- an earlier deploy's pods are already gone or about to be replaced.
	t.Run("SetPendingCorrelationIDSecondDeployOverwrites", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id := mustCreateExecution(t, repo, "peak", 10)

		if err := repo.SetPendingCorrelationID(ctx, id, "11111111111111111111111111111111"); err != nil {
			t.Fatalf("SetPendingCorrelationID (first): %v", err)
		}
		if err := repo.SetPendingCorrelationID(ctx, id, "22222222222222222222222222222222"); err != nil {
			t.Fatalf("SetPendingCorrelationID (second): %v", err)
		}
		got, err := repo.PendingCorrelationID(ctx, id)
		if err != nil {
			t.Fatalf("PendingCorrelationID: %v", err)
		}
		if got != "22222222222222222222222222222222" {
			t.Fatalf("PendingCorrelationID after second set = %q, want the second id", got)
		}
	})

	// The idle clock the engine TTL reaper measures: unstamped reads as
	// ok=false (fall back to another clock), a touch stamps it, and a second
	// touch in the same second must not read as an error or a lost stamp.
	t.Run("ActivityTouchStampsTheIdleClock", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		id := mustCreateExecution(t, repo, "peak", 10)

		if _, ok, err := repo.LastActivity(ctx, id); err != nil || ok {
			t.Fatalf("LastActivity before any touch = %v,%v; want false,nil", ok, err)
		}

		before := time.Now()
		if err := repo.TouchActivity(ctx, id); err != nil {
			t.Fatalf("TouchActivity: %v", err)
		}
		// A same-second second touch: NOW()'s second precision means the row
		// may not change, which must not be mistaken for a missing row.
		if err := repo.TouchActivity(ctx, id); err != nil {
			t.Fatalf("TouchActivity (same second): %v", err)
		}
		last, ok, err := repo.LastActivity(ctx, id)
		if err != nil || !ok {
			t.Fatalf("LastActivity after touch = %v,%v; want true,nil", ok, err)
		}
		// Second-precision columns can lag the test's clock by a second on
		// either side; anything within that tolerance is the touch just made.
		if last.Before(before.Add(-2*time.Second)) || last.After(time.Now().Add(2*time.Second)) {
			t.Fatalf("LastActivity = %v, want the just-made stamp near %v", last, before)
		}

		// Touching a deleted execution drops the stamp rather than erroring:
		// a stamp is advisory, and the reaper's guards -- not a touch error --
		// are what protect a live run from a racing delete.
		if err := repo.DeleteExecution(ctx, id); err != nil {
			t.Fatalf("DeleteExecution: %v", err)
		}
		if err := repo.TouchActivity(ctx, id); err != nil {
			t.Fatalf("TouchActivity after delete: %v", err)
		}
		if _, ok, err := repo.LastActivity(ctx, id); err != nil || ok {
			t.Fatalf("LastActivity after delete = %v,%v; want false,nil", ok, err)
		}
	})

	// LatestRunsForScenarios must reproduce the scenario detail page's
	// per-row probe for a whole list at once: the newest execution bound to
	// the scenario, and THAT execution's newest report -- never a report
	// from an older execution (however new it is), never the newest
	// execution's older report, and an honest absence when there is no
	// verdict yet.
	t.Run("LatestRunsForScenariosNewestExecutionThenItsNewestReport", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		withRuns := mustCreateScenario(t, repo, "with-runs", 10)
		// Two executions bound to it, created in order: the second is the
		// newest either way -- created_time order when the clock separates
		// them, the id tiebreak when the column's precision does not.
		older := mustCreateExecution(t, repo, "older", 10)
		newest := mustCreateExecution(t, repo, "newest", 10)
		for _, execID := range []int64{older, newest} {
			if err := repo.StoreLoadProfile(ctx, execID, false, []loadprofile.Entry{
				{Name: "with-runs", ScenarioID: withRuns, Engines: 1, Concurrency: 1, Duration: 60},
			}); err != nil {
				t.Fatalf("StoreLoadProfile(%d): %v", execID, err)
			}
		}

		// The trap: an older execution whose report started after anything
		// on the newest one. A summary that surfaced this picked a report
		// across executions instead of the newest execution's own.
		mustSaveReport(t, repo, report.Report{
			ExecutionID: older, ScenarioID: withRuns, RunID: 901,
			Outcome:   taurus.OutcomeError,
			StartedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		})
		// The newest execution's two reports, in order: the later one wins.
		mustSaveReport(t, repo, report.Report{
			ExecutionID: newest, ScenarioID: withRuns, RunID: 902,
			Outcome:   taurus.OutcomePassed,
			StartedAt: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		})
		mustSaveReport(t, repo, report.Report{
			ExecutionID: newest, ScenarioID: withRuns, RunID: 903,
			Outcome:   taurus.OutcomeFailed,
			StartedAt: time.Date(2026, 3, 1, 11, 0, 0, 0, time.UTC),
		})

		// Bound, but its newest execution never finalised a report: no
		// verdict, so no entry -- "pending", not "failed".
		pending := mustCreateScenario(t, repo, "pending", 10)
		undeclared := mustCreateExecution(t, repo, "undeclared", 10)
		if err := repo.StoreLoadProfile(ctx, undeclared, false, []loadprofile.Entry{
			{Name: "pending", ScenarioID: pending, Engines: 1, Concurrency: 1, Duration: 60},
		}); err != nil {
			t.Fatalf("StoreLoadProfile(%d): %v", undeclared, err)
		}

		// Never bound to anything at all.
		idle := mustCreateScenario(t, repo, "idle", 10)

		got, err := repo.LatestRunsForScenarios(ctx, []int64{withRuns, pending, idle})
		if err != nil {
			t.Fatalf("LatestRunsForScenarios: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("LatestRunsForScenarios returned %d entries (%+v), want only the scenario with a verdict", len(got), got)
		}
		lr, ok := got[withRuns]
		if !ok {
			t.Fatalf("LatestRunsForScenarios missing scenario %d entirely: %+v", withRuns, got)
		}
		if lr.ExecutionID != newest || lr.Outcome != taurus.OutcomeFailed {
			t.Fatalf("LatestRunsForScenarios[%d] = {exec %d, %s}, want {exec %d, failed}", withRuns, lr.ExecutionID, lr.Outcome, newest)
		}
		if !lr.StartedAt.Equal(time.Date(2026, 3, 1, 11, 0, 0, 0, time.UTC)) {
			t.Fatalf("LatestRunsForScenarios[%d].StartedAt = %v, want the newest report's start", withRuns, lr.StartedAt)
		}
	})

	// An empty id list asks for nothing and must read nothing -- the same
	// convention ListExecutionsByProjects keeps, so "no rows" can never
	// become "every row".
	t.Run("LatestRunsForScenariosEmptyListAsksNothing", func(t *testing.T) {
		repo := newRepo(t)
		got, err := repo.LatestRunsForScenarios(context.Background(), nil)
		if err != nil {
			t.Fatalf("LatestRunsForScenarios(nil): %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("LatestRunsForScenarios(nil) = %+v, want empty", got)
		}
	})
}

// mustSaveReport stores rep, filling in the ended_at clock a report needs
// when the test only cares about the start.
func mustSaveReport(t *testing.T, repo Repository, rep report.Report) {
	t.Helper()
	if rep.EndedAt.IsZero() {
		rep.EndedAt = rep.StartedAt.Add(time.Minute)
	}
	if err := repo.SaveReport(context.Background(), rep); err != nil {
		t.Fatalf("SaveReport(run %d): %v", rep.RunID, err)
	}
}

func mustCreateScenario(t *testing.T, repo Repository, name string, projectID int64) int64 {
	t.Helper()
	p, err := scenario.New(name, projectID)
	if err != nil {
		t.Fatalf("build scenario %q: %v", name, err)
	}
	id, err := repo.CreateScenario(context.Background(), p)
	if err != nil {
		t.Fatalf("CreateScenario %q: %v", name, err)
	}
	return id
}

func idsOf(es []execution.Execution) []int64 {
	out := make([]int64, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

func mustCreateExecution(t *testing.T, repo Repository, name string, projectID int64) int64 {
	t.Helper()
	c, err := execution.New(name, projectID)
	if err != nil {
		t.Fatalf("build execution %q: %v", name, err)
	}
	id, err := repo.CreateExecution(context.Background(), c)
	if err != nil {
		t.Fatalf("CreateExecution %q: %v", name, err)
	}
	return id
}

func planNames(ps []scenario.Scenario) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}
