package calibrationapp_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/calibrationapp"
	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/capacityprofile"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

func seedProject(t *testing.T, store *fake.Store) int64 {
	t.Helper()
	p, err := project.New("web", "honryu", "")
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	id, err := store.CreateProject(context.Background(), p)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return id
}

func validSpec() calibration.Spec {
	return calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi"}
}

// seedBoundSource creates the ordinary execution Create copies its
// scenario binding from: a native JMeter scenario and a source execution
// whose load profile runs it. The entry's values are deliberately distinct
// from anything Create should write, so a test can tell the bound copy's
// overrides apart from the source's own fields.
func seedBoundSource(t *testing.T, store *fake.Store, projectID int64) (sourceID, scenarioID int64) {
	t.Helper()
	ctx := context.Background()
	pl, err := scenario.NewNative("target", projectID, taurus.ExecutorJMeter)
	if err != nil {
		t.Fatalf("scenario.NewNative: %v", err)
	}
	scenarioID, err = store.CreateScenario(ctx, pl)
	if err != nil {
		t.Fatalf("CreateScenario: %v", err)
	}
	exe, err := execution.New("source", projectID)
	if err != nil {
		t.Fatalf("execution.New: %v", err)
	}
	sourceID, err = store.CreateExecution(ctx, exe)
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
	entries := []loadprofile.Entry{{ScenarioID: scenarioID, Engines: 3, Concurrency: 40, Rampup: 15, Duration: 45, Throughput: 777}}
	if err := store.StoreLoadProfile(ctx, sourceID, false, entries); err != nil {
		t.Fatalf("StoreLoadProfile: %v", err)
	}
	return sourceID, scenarioID
}

func TestCreate_PersistsExecutionCriteriaAndBounds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	projectID := seedProject(t, store)
	src, scenarioID := seedBoundSource(t, store, projectID)

	executionID, err := svc.Create(ctx, "checkout-calibration", projectID, taurus.ExecutorJMeter, validSpec(), src, scenarioID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if executionID <= 0 {
		t.Fatalf("Create returned id = %d, want > 0", executionID)
	}

	exe, err := store.GetExecution(ctx, executionID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exe.Kind != execution.KindCalibrateEngine {
		t.Errorf("Kind = %q, want calibrate_engine", exe.Kind)
	}
	if exe.Engine != taurus.ExecutorJMeter {
		t.Errorf("Engine = %q, want jmeter", exe.Engine)
	}
	if exe.CPU != "1" || exe.Memory != "512Mi" {
		t.Errorf("CPU/Memory = %q/%q, want 1/512Mi", exe.CPU, exe.Memory)
	}

	criteria, err := store.CriteriaFor(ctx, executionID)
	if err != nil {
		t.Fatalf("CriteriaFor: %v", err)
	}
	if len(criteria) != 1 || criteria[0] != "failures>5%" {
		t.Errorf("CriteriaFor = %v, want [failures>5%%]", criteria)
	}

	bounds, err := store.CalibrationBoundsFor(ctx, executionID)
	if err != nil {
		t.Fatalf("CalibrationBoundsFor: %v", err)
	}
	if bounds.SeedQPS != calibration.DefaultSeedQPS || bounds.MaxQPS != calibration.DefaultMaxQPS {
		t.Errorf("bounds = %+v, want the defaulted values", bounds)
	}
}

// Create binds the source's scenario entry as the calibration execution's
// single load-profile entry -- the phase 41 fix: before it, a UI-created
// calibration had criteria and bounds but no execution_scenario row, so its
// first Trigger died with run.ErrNoScenarios and Deploy with
// ports.ErrNotFound.
func TestCreate_BindsTheSourceScenarioAsItsSingleEntry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	projectID := seedProject(t, store)
	src, scenarioID := seedBoundSource(t, store, projectID)

	spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 7.5, HoldSeconds: 20}
	executionID, err := svc.Create(ctx, "calibrate", projectID, taurus.ExecutorJMeter, spec, src, scenarioID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	entries, err := store.LoadProfileFor(ctx, executionID)
	if err != nil {
		t.Fatalf("LoadProfileFor: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("LoadProfileFor = %+v, want exactly 1 entry (the bound scenario)", entries)
	}
	got := entries[0]
	if got.ScenarioID != scenarioID {
		t.Errorf("ScenarioID = %d, want %d", got.ScenarioID, scenarioID)
	}
	if got.Engines != 1 {
		t.Errorf("Engines = %d, want 1 (a calibration searches one pod)", got.Engines)
	}
	if want := int(math.Ceil(spec.SeedQPS)); got.Throughput != want {
		t.Errorf("Throughput = %d, want ceil(SeedQPS %g) = %d", got.Throughput, spec.SeedQPS, want)
	}
	if got.Duration != 45 {
		t.Errorf("Duration = %d, want max(HoldSeconds 20, source 45) = 45", got.Duration)
	}
	if got.Concurrency != 40 || got.Rampup != 15 {
		t.Errorf("Concurrency/Rampup = %d/%d, want the source's 40/15 carried over", got.Concurrency, got.Rampup)
	}

	// The criterion lands with the binding -- one atomic config write.
	criteria, err := store.CriteriaFor(ctx, executionID)
	if err != nil {
		t.Fatalf("CriteriaFor: %v", err)
	}
	if len(criteria) != 1 || criteria[0] != spec.Criterion {
		t.Fatalf("CriteriaFor = %v, want [%s] alongside the binding", criteria, spec.Criterion)
	}

	// The source execution's own profile is untouched -- the binding is a
	// copy, never a move.
	sourceEntries, err := store.LoadProfileFor(ctx, src)
	if err != nil {
		t.Fatalf("LoadProfileFor(source): %v", err)
	}
	if len(sourceEntries) != 1 || sourceEntries[0].Engines != 3 || sourceEntries[0].Throughput != 777 {
		t.Fatalf("source profile = %+v, want the original 3-engine/777-QPS entry", sourceEntries)
	}
}

// A source entry shorter than the search's hold window gives way to it:
// every step must hold its full steady-state window even when the source
// ran a brief smoke entry.
func TestCreate_BoundDurationExtendsToTheHold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	projectID := seedProject(t, store)
	src, scenarioID := seedBoundSource(t, store, projectID)
	if err := store.StoreLoadProfile(ctx, src, false, []loadprofile.Entry{{ScenarioID: scenarioID, Engines: 2, Concurrency: 5, Rampup: 1, Duration: 5}}); err != nil {
		t.Fatalf("StoreLoadProfile (short source): %v", err)
	}

	spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", HoldSeconds: 20}
	executionID, err := svc.Create(ctx, "calibrate", projectID, taurus.ExecutorJMeter, spec, src, scenarioID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	entries, err := store.LoadProfileFor(ctx, executionID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("LoadProfileFor = %+v (%v), want 1 entry", entries, err)
	}
	if entries[0].Duration != 20 {
		t.Errorf("Duration = %d, want the spec's hold 20 over the source's 5", entries[0].Duration)
	}
}

// A source that runs no entry for the scenario fails the whole Create
// before anything is written -- no half-configured execution is left
// behind.
func TestCreate_RejectsASourceThatDoesNotRunTheScenario(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	projectID := seedProject(t, store)
	src, scenarioID := seedBoundSource(t, store, projectID)

	if _, err := svc.Create(ctx, "calibrate", projectID, taurus.ExecutorJMeter, validSpec(), src, scenarioID+1000); !errors.Is(err, calibrationapp.ErrSourceScenarioNotBound) {
		t.Fatalf("Create (scenario not in source) = %v, want ErrSourceScenarioNotBound", err)
	}
	// A source execution that does not exist at all fails the same way:
	// it runs the scenario no more than a mismatched one does.
	if _, err := svc.Create(ctx, "calibrate", projectID, taurus.ExecutorJMeter, validSpec(), src+1000, scenarioID); !errors.Is(err, calibrationapp.ErrSourceScenarioNotBound) {
		t.Fatalf("Create (missing source) = %v, want ErrSourceScenarioNotBound", err)
	}

	execs, err := store.ListExecutionsByProject(ctx, projectID)
	if err != nil {
		t.Fatalf("ListExecutionsByProject: %v", err)
	}
	if len(execs) != 1 {
		t.Fatalf("project executions = %d, want only the source (nothing half-created)", len(execs))
	}
}

func TestCreate_RejectsAnInvalidSpec(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	projectID := seedProject(t, store)

	spec := validSpec()
	spec.Criterion = ""
	src, scenarioID := seedBoundSource(t, store, projectID)
	if _, err := svc.Create(ctx, "x", projectID, taurus.ExecutorJMeter, spec, src, scenarioID); !errors.Is(err, calibration.ErrCriterionRequired) {
		t.Fatalf("Create (no criterion) = %v, want ErrCriterionRequired", err)
	}
}

func TestCreate_RejectsAnInvalidExecutionName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	projectID := seedProject(t, store)

	src, scenarioID := seedBoundSource(t, store, projectID)
	if _, err := svc.Create(ctx, "", projectID, taurus.ExecutorJMeter, validSpec(), src, scenarioID); !errors.Is(err, execution.ErrNameRequired) {
		t.Fatalf("Create (no name) = %v, want ErrNameRequired", err)
	}
}

// A capacity profile is keyed by engine and the profile/fan-out API requires
// one to look it up, so a calibration that names no engine would write a
// profile nothing could ever query. Create rejects it up front.
func TestCreate_RejectsAnEmptyEngine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	projectID := seedProject(t, store)

	src, scenarioID := seedBoundSource(t, store, projectID)
	if _, err := svc.Create(ctx, "x", projectID, taurus.Executor(""), validSpec(), src, scenarioID); !errors.Is(err, calibrationapp.ErrEngineRequired) {
		t.Fatalf("Create (no engine) = %v, want ErrEngineRequired", err)
	}
}

func TestSpecFor_ReassemblesFromExecutionCriteriaAndBounds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	projectID := seedProject(t, store)

	spec := calibration.Spec{Criterion: "p95>500ms", CPU: "2", Memory: "1Gi", SeedQPS: 5, MaxQPS: 500, MaxSteps: 10, HoldSeconds: 20}
	src, scenarioID := seedBoundSource(t, store, projectID)
	executionID, err := svc.Create(ctx, "x", projectID, taurus.ExecutorK6, spec, src, scenarioID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.SpecFor(ctx, executionID)
	if err != nil {
		t.Fatalf("SpecFor: %v", err)
	}
	if got != spec {
		t.Fatalf("SpecFor = %+v, want %+v", got, spec)
	}
}

func TestSpecFor_UnconfiguredExecutionPropagatesNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	projectID := seedProject(t, store)

	// A bare execution never routed through Create -- no bounds recorded.
	exe, err := execution.New("bare", projectID)
	if err != nil {
		t.Fatalf("execution.New: %v", err)
	}
	exe.Kind = execution.KindCalibrateEngine
	executionID, err := store.CreateExecution(ctx, exe)
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}

	if _, err := svc.SpecFor(ctx, executionID); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("SpecFor(unconfigured) = %v, want ErrNotFound", err)
	}
}

func TestTrigger_CreatesAPendingJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	projectID := seedProject(t, store)
	src, scenarioID := seedBoundSource(t, store, projectID)
	executionID, err := svc.Create(ctx, "x", projectID, taurus.ExecutorJMeter, validSpec(), src, scenarioID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	jobID, err := svc.Trigger(ctx, executionID)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if jobID <= 0 {
		t.Fatalf("Trigger returned job id = %d, want > 0", jobID)
	}

	job, err := store.GetCalibrationJob(ctx, jobID)
	if err != nil {
		t.Fatalf("GetCalibrationJob: %v", err)
	}
	if job.ExecutionID != executionID || job.Phase != calibration.PhasePending {
		t.Fatalf("GetCalibrationJob = %+v, want execution=%d phase=pending", job, executionID)
	}
}

// A calibration execution can be triggered more than once over its life,
// the same way an execution can have more than one run.
func TestTrigger_MoreThanOnceCreatesSeparateJobs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	projectID := seedProject(t, store)
	src, scenarioID := seedBoundSource(t, store, projectID)
	executionID, err := svc.Create(ctx, "x", projectID, taurus.ExecutorJMeter, validSpec(), src, scenarioID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	first, err := svc.Trigger(ctx, executionID)
	if err != nil {
		t.Fatalf("Trigger (first): %v", err)
	}
	second, err := svc.Trigger(ctx, executionID)
	if err != nil {
		t.Fatalf("Trigger (second): %v", err)
	}
	if first == second {
		t.Fatalf("two triggers produced the same job id %d", first)
	}

	jobs, err := svc.ListByExecution(ctx, executionID)
	if err != nil {
		t.Fatalf("ListByExecution: %v", err)
	}
	if len(jobs) != 2 || jobs[0].ID != second || jobs[1].ID != first {
		t.Fatalf("ListByExecution = %+v, want [second, first] most-recent-first", jobs)
	}
}

func TestTrigger_RejectsANonCalibrationExecution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	projectID := seedProject(t, store)

	normal, err := execution.New("ordinary", projectID)
	if err != nil {
		t.Fatalf("execution.New: %v", err)
	}
	executionID, err := store.CreateExecution(ctx, normal)
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}

	if _, err := svc.Trigger(ctx, executionID); !errors.Is(err, calibrationapp.ErrExecutionNotCalibration) {
		t.Fatalf("Trigger(normal execution) = %v, want ErrExecutionNotCalibration", err)
	}
}

// Trigger fails loudly rather than starting a search with an undefined
// target-health criterion or pod size -- the safety net for an execution
// that reached CalibrateEngine kind without ever going through Create (or
// whose Create partially failed).
func TestTrigger_RejectsAnUnconfiguredCalibrationExecution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	projectID := seedProject(t, store)

	exe, err := execution.New("unconfigured", projectID)
	if err != nil {
		t.Fatalf("execution.New: %v", err)
	}
	exe.Kind = execution.KindCalibrateEngine
	executionID, err := store.CreateExecution(ctx, exe)
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}

	if _, err := svc.Trigger(ctx, executionID); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Trigger(unconfigured) = %v, want the SpecFor NotFound to propagate", err)
	}
}

func TestTrigger_UnknownExecutionPropagatesNotFound(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	if _, err := svc.Trigger(context.Background(), 999); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Trigger(unknown execution) = %v, want ErrNotFound", err)
	}
}

func TestGet_MissingJobReturnsNotFound(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	if _, err := svc.Get(context.Background(), 999); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Get(missing job) = %v, want ErrNotFound", err)
	}
}

// Get exposes a job's full step history; ListByExecution stays lightweight
// (no steps), matching how a caller wanting one job's detail asks for it
// specifically rather than every listed job carrying its whole history.
func TestGet_ExposesStepHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	projectID := seedProject(t, store)
	src, scenarioID := seedBoundSource(t, store, projectID)
	executionID, err := svc.Create(ctx, "x", projectID, taurus.ExecutorJMeter, validSpec(), src, scenarioID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	jobID, err := svc.Trigger(ctx, executionID)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	step := calibration.Step{RequestedQPS: 10, AchievedQPS: 10, Classification: calibration.ClassificationClean}
	if err := store.RecordStep(ctx, jobID, step,
		ports.CalibrationJob{Phase: calibration.PhaseBracketing, StepCount: 1, BracketLoRequested: 10, BracketLoAchieved: 10, NextRequestedQPS: 20},
	); err != nil {
		t.Fatalf("RecordStep: %v", err)
	}

	got, err := svc.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Phase != calibration.PhaseBracketing || got.NextRequestedQPS != 20 {
		t.Fatalf("Get = %+v, want the recorded state", got)
	}
	if len(got.Steps) != 1 || got.Steps[0] != step {
		t.Fatalf("Get Steps = %+v, want [%+v]", got.Steps, step)
	}
}

// erroringRepo wraps a *fake.Store and lets a test force one method to
// fail, proving each use-case propagates a downstream failure from any of
// its data sources rather than swallowing it.
type erroringRepo struct {
	*fake.Store
	createExecutionErr       error
	storeExecutionConfigErr  error
	setCalibrationBoundsErr  error
	criteriaForErr           error
	calibrationBoundsForErr  error
	createCalibrationJobErr  error
	stepsForErr              error
	recordStepErr            error
	upsertCapacityProfileErr error

	// getExecutionErr/loadProfileForErr apply starting from the Nth call
	// (1-indexed) so a test can let an earlier caller in the same
	// AdvanceOne tick (SpecFor) succeed while a later one (writeProfile)
	// fails -- both route through the same repo method.
	getExecutionErr           error
	getExecutionErrFromCall   int
	getExecutionCalls         int
	loadProfileForErr         error
	loadProfileForErrFromCall int
	loadProfileForCalls       int
}

func (r *erroringRepo) CreateExecution(ctx context.Context, c execution.Execution) (int64, error) {
	if r.createExecutionErr != nil {
		return 0, r.createExecutionErr
	}
	return r.Store.CreateExecution(ctx, c)
}

func (r *erroringRepo) StoreExecutionConfig(ctx context.Context, executionID int64, csvSplit bool, entries []loadprofile.Entry, criteria []string) error {
	if r.storeExecutionConfigErr != nil {
		return r.storeExecutionConfigErr
	}
	return r.Store.StoreExecutionConfig(ctx, executionID, csvSplit, entries, criteria)
}

func (r *erroringRepo) SetCalibrationBounds(ctx context.Context, executionID int64, bounds ports.CalibrationBounds) error {
	if r.setCalibrationBoundsErr != nil {
		return r.setCalibrationBoundsErr
	}
	return r.Store.SetCalibrationBounds(ctx, executionID, bounds)
}

func (r *erroringRepo) CriteriaFor(ctx context.Context, executionID int64) ([]string, error) {
	if r.criteriaForErr != nil {
		return nil, r.criteriaForErr
	}
	return r.Store.CriteriaFor(ctx, executionID)
}

func (r *erroringRepo) CalibrationBoundsFor(ctx context.Context, executionID int64) (ports.CalibrationBounds, error) {
	if r.calibrationBoundsForErr != nil {
		return ports.CalibrationBounds{}, r.calibrationBoundsForErr
	}
	return r.Store.CalibrationBoundsFor(ctx, executionID)
}

func (r *erroringRepo) CreateCalibrationJob(ctx context.Context, executionID int64) (int64, error) {
	if r.createCalibrationJobErr != nil {
		return 0, r.createCalibrationJobErr
	}
	return r.Store.CreateCalibrationJob(ctx, executionID)
}

func (r *erroringRepo) StepsFor(ctx context.Context, jobID int64) ([]calibration.Step, error) {
	if r.stepsForErr != nil {
		return nil, r.stepsForErr
	}
	return r.Store.StepsFor(ctx, jobID)
}

func (r *erroringRepo) RecordStep(ctx context.Context, jobID int64, step calibration.Step, updated ports.CalibrationJob) error {
	if r.recordStepErr != nil {
		return r.recordStepErr
	}
	return r.Store.RecordStep(ctx, jobID, step, updated)
}

func (r *erroringRepo) UpsertCapacityProfile(ctx context.Context, profile capacityprofile.CapacityProfile) error {
	if r.upsertCapacityProfileErr != nil {
		return r.upsertCapacityProfileErr
	}
	return r.Store.UpsertCapacityProfile(ctx, profile)
}

func (r *erroringRepo) GetExecution(ctx context.Context, id int64) (execution.Execution, error) {
	r.getExecutionCalls++
	if r.getExecutionErr != nil && r.getExecutionCalls >= r.getExecutionErrFromCall {
		return execution.Execution{}, r.getExecutionErr
	}
	return r.Store.GetExecution(ctx, id)
}

func (r *erroringRepo) LoadProfileFor(ctx context.Context, executionID int64) ([]loadprofile.Entry, error) {
	r.loadProfileForCalls++
	if r.loadProfileForErr != nil && r.loadProfileForCalls >= r.loadProfileForErrFromCall {
		return nil, r.loadProfileForErr
	}
	return r.Store.LoadProfileFor(ctx, executionID)
}

func TestCreate_DownstreamErrorsPropagate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		wire func(r *erroringRepo)
	}{
		{"CreateExecution fails", func(r *erroringRepo) { r.createExecutionErr = errors.New("boom") }},
		{"StoreExecutionConfig fails", func(r *erroringRepo) { r.storeExecutionConfigErr = errors.New("boom") }},
		{"SetCalibrationBounds fails", func(r *erroringRepo) { r.setCalibrationBoundsErr = errors.New("boom") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := fake.NewStore()
			repo := &erroringRepo{Store: store}
			tt.wire(repo)
			svc := calibrationapp.NewService(repo)
			projectID := seedProject(t, store)
			src, scenarioID := seedBoundSource(t, store, projectID)

			if _, err := svc.Create(context.Background(), "x", projectID, taurus.ExecutorJMeter, validSpec(), src, scenarioID); err == nil {
				t.Fatal("Create = nil error, want the downstream failure to propagate")
			}
		})
	}
}

func TestSpecFor_DownstreamErrorsPropagate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		wire func(r *erroringRepo)
	}{
		{"CriteriaFor fails", func(r *erroringRepo) { r.criteriaForErr = errors.New("boom") }},
		{"CalibrationBoundsFor fails", func(r *erroringRepo) { r.calibrationBoundsForErr = errors.New("boom") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := fake.NewStore()
			projectID := seedProject(t, store)
			src, scenarioID := seedBoundSource(t, store, projectID)
			executionID, err := calibrationapp.NewService(store).Create(context.Background(), "x", projectID, taurus.ExecutorJMeter, validSpec(), src, scenarioID)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			repo := &erroringRepo{Store: store}
			tt.wire(repo)
			svc := calibrationapp.NewService(repo)
			if _, err := svc.SpecFor(context.Background(), executionID); err == nil {
				t.Fatal("SpecFor = nil error, want the downstream failure to propagate")
			}
		})
	}
}

func TestTrigger_CreateCalibrationJobErrorPropagates(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	projectID := seedProject(t, store)
	src, scenarioID := seedBoundSource(t, store, projectID)
	executionID, err := calibrationapp.NewService(store).Create(context.Background(), "x", projectID, taurus.ExecutorJMeter, validSpec(), src, scenarioID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	repo := &erroringRepo{Store: store, createCalibrationJobErr: errors.New("boom")}
	svc := calibrationapp.NewService(repo)
	if _, err := svc.Trigger(context.Background(), executionID); err == nil {
		t.Fatal("Trigger = nil error, want the CreateCalibrationJob failure to propagate")
	}
}

func TestGet_StepsForErrorPropagates(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	projectID := seedProject(t, store)
	svc := calibrationapp.NewService(store)
	src, scenarioID := seedBoundSource(t, store, projectID)
	executionID, err := svc.Create(context.Background(), "x", projectID, taurus.ExecutorJMeter, validSpec(), src, scenarioID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	jobID, err := svc.Trigger(context.Background(), executionID)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	repo := &erroringRepo{Store: store, stepsForErr: errors.New("boom")}
	errSvc := calibrationapp.NewService(repo)
	if _, err := errSvc.Get(context.Background(), jobID); err == nil {
		t.Fatal("Get = nil error, want the StepsFor failure to propagate")
	}
}

// stubRunnerCall records one RunStep invocation's arguments.
type stubRunnerCall struct {
	executionID    int64
	requestedQPS   float64
	holdSeconds    int
	latencyHintSec float64
}

// stubRunnerResponse is one scripted RunStep outcome.
type stubRunnerResponse struct {
	report report.Report
	err    error
}

// stubRunner is a scripted calibrationapp.Runner: each RunStep call consumes
// the next queued response, in order, and records its own arguments -- lets
// a test drive AdvanceOne's classify/decide/persist loop deterministically,
// without a real Deploy/Trigger/Stop pipeline (step_test.go already proves
// that machinery works).
type stubRunner struct {
	responses []stubRunnerResponse
	calls     []stubRunnerCall
}

func (r *stubRunner) RunStep(_ context.Context, executionID int64, requestedQPS float64, holdSeconds int, latencyHintSec float64) (report.Report, error) {
	r.calls = append(r.calls, stubRunnerCall{executionID, requestedQPS, holdSeconds, latencyHintSec})
	if len(r.responses) == 0 {
		panic("stubRunner: no more scripted responses")
	}
	resp := r.responses[0]
	r.responses = r.responses[1:]
	return resp.report, resp.err
}

// stepHoldSeconds is the hold every seeded spec runs its steps with and every
// stub report carries as its requested duration -- a real step's report gets
// the same figure from the load-profile entry RunStep rewrites.
const stepHoldSeconds = 1

// stepReport is a settled one-second step's report: samples are the volume
// achievedQPS actually produced over the hold, so a stub is faithful in volume
// as well as rate -- calibration classifies steps by volume (ShortOfVolume),
// not by the achieved rate the report also carries. A stub whose sample count
// disagreed with its rate would survive rate-based classification and break
// silently under volume-based.
func stepReport(requestedQPS, achievedQPS float64) report.Report {
	return report.Report{
		Requested: report.Load{Throughput: requestedQPS, DurationSeconds: stepHoldSeconds},
		Achieved: report.Load{
			Throughput: achievedQPS, DurationSeconds: stepHoldSeconds,
			Samples: int64(achievedQPS * stepHoldSeconds),
		},
	}
}

// cleanReport reports achievedQPS against requestedQPS with no failures --
// engine kept pace, target healthy.
func cleanReport(requestedQPS, achievedQPS float64) report.Report {
	return stepReport(requestedQPS, achievedQPS)
}

// engineSaturatedReport reports well under the requested volume (ShortOfVolume),
// with no target-attributed failures -- the pod itself could not sustain
// the rate.
func engineSaturatedReport(requestedQPS, achievedQPS float64) report.Report {
	return stepReport(requestedQPS, achievedQPS)
}

// targetSaturatedReport reports the engine keeping up (achieved at or above
// requestedQPS's tolerance) while the overall error rate trips
// validSpec()'s "failures>5%" criterion.
func targetSaturatedReport(requestedQPS, achievedQPS float64) report.Report {
	rpt := stepReport(requestedQPS, achievedQPS)
	rpt.ErrorRate = 0.10
	rpt.Attribution = report.Attribution{Target: 100}
	return rpt
}

// stubFingerprinter returns a fixed fingerprint, or an error when set.
type stubFingerprinter struct {
	value string
	err   error
}

func (f *stubFingerprinter) ScenarioFingerprint(context.Context, int64) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.value, nil
}

// seedTriggeredCalibration creates a project, a CalibrateEngine execution
// configured with spec and bound to a scenario (Create's own binding), and
// triggers a fresh Pending job. Mirrors what a real caller does before a
// controller ever calls AdvanceOne.
func seedTriggeredCalibration(t *testing.T, store *fake.Store, spec calibration.Spec) (executionID, jobID, scenarioID int64) {
	t.Helper()
	ctx := context.Background()
	projectID := seedProject(t, store)
	src, scenarioID := seedBoundSource(t, store, projectID)
	svc := calibrationapp.NewService(store)
	executionID, err := svc.Create(ctx, "calibrate", projectID, taurus.ExecutorJMeter, spec, src, scenarioID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	jobID, err = svc.Trigger(ctx, executionID)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	return executionID, jobID, scenarioID
}

func TestAdvanceOne_NoJobDue(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	runner := &stubRunner{}
	svc := calibrationapp.NewService(store).WithRunner(runner).WithFingerprint(&stubFingerprinter{value: "fp"})

	found, err := svc.AdvanceOne(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("AdvanceOne: %v", err)
	}
	if found {
		t.Fatal("found = true, want false with no job ever triggered")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %v, want none", runner.calls)
	}
}

func TestAdvanceOne_RequiresRunnerAndFingerprint(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 10, MaxQPS: 1000, MaxSteps: 5, HoldSeconds: 1}
	seedTriggeredCalibration(t, store, spec)

	// Neither WithRunner nor WithFingerprint is wired.
	svc := calibrationapp.NewService(store)
	if _, err := svc.AdvanceOne(context.Background(), time.Now()); !errors.Is(err, calibrationapp.ErrNotConfiguredForAdvance) {
		t.Fatalf("error = %v, want ErrNotConfiguredForAdvance", err)
	}
	// The job must still be claimable afterward -- a configuration error
	// must not consume its claim.
	if _, running, _ := store.CurrentRun(context.Background(), 0); running {
		t.Fatal("unexpected run state touched")
	}
}

func TestAdvanceOne_EngineLimitedHappyPath_WithConfirmedRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 10, MaxQPS: 1000, MaxSteps: 2, HoldSeconds: 1}
	executionID, jobID, scenarioID := seedTriggeredCalibration(t, store, spec)

	runner := &stubRunner{responses: []stubRunnerResponse{
		{report: cleanReport(10, 10)},             // tick 1: clean, doubles to 20
		{report: engineSaturatedReport(20, 10)},   // tick 2 attempt 1: engine-short (anomalous?)
		{report: engineSaturatedReport(20, 10.5)}, // tick 2 retry: confirmed engine-short
	}}
	fp := &stubFingerprinter{value: "fingerprint-1"}
	svc := calibrationapp.NewService(store).WithRunner(runner).WithFingerprint(fp)

	// Tick 1: clean at the seed QPS, not yet terminal.
	found, err := svc.AdvanceOne(ctx, time.Now())
	if err != nil || !found {
		t.Fatalf("tick 1: found=%v, err=%v", found, err)
	}
	job, err := svc.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("Get after tick 1: %v", err)
	}
	if job.Phase != calibration.PhaseBracketing || job.NextRequestedQPS != 20 {
		t.Fatalf("after tick 1: %+v, want Bracketing at next 20", job.CalibrationJob)
	}

	// Tick 2: engine-short, retried once, confirmed -- terminal.
	found, err = svc.AdvanceOne(ctx, time.Now())
	if err != nil || !found {
		t.Fatalf("tick 2: found=%v, err=%v", found, err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("runner calls = %d, want 3 (1 clean + 1 engine-short + 1 retry)", len(runner.calls))
	}
	if runner.calls[1].requestedQPS != 20 || runner.calls[2].requestedQPS != 20 {
		t.Fatalf("retry must reuse the same requested QPS: calls = %+v", runner.calls)
	}

	job, err = svc.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("Get after tick 2: %v", err)
	}
	if job.Phase != calibration.PhaseDone || job.Result == nil {
		t.Fatalf("job not terminal: %+v", job.CalibrationJob)
	}
	if job.Result.SaturatedBy != calibration.SaturatedByEngine || job.Result.PerPodQPS != 10 {
		t.Fatalf("result = %+v, want engine-limited at 10 (the last clean step's achieved QPS)", job.Result)
	}
	// Only the retry's own outcome is recorded -- the discarded first
	// attempt leaves no trace.
	if len(job.Steps) != 2 || job.Steps[1].AchievedQPS != 10.5 {
		t.Fatalf("steps = %+v, want the retry's achieved QPS (10.5), not the discarded first attempt's", job.Steps)
	}

	profile, err := store.GetCapacityProfile(ctx, capacityprofile.Key{ScenarioID: scenarioID, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi"})
	if err != nil {
		t.Fatalf("GetCapacityProfile: %v", err)
	}
	if profile.PerPodQPS != 10 || profile.SaturatedBy != calibration.SaturatedByEngine {
		t.Fatalf("profile = %+v, want PerPodQPS 10, SaturatedBy engine", profile)
	}
	if profile.ScenarioFingerprint != "fingerprint-1" {
		t.Fatalf("profile fingerprint = %q, want fingerprint-1", profile.ScenarioFingerprint)
	}
	if profile.JobID != jobID {
		t.Fatalf("profile.JobID = %d, want %d", profile.JobID, jobID)
	}
	_ = executionID
}

func TestAdvanceOne_RetryDiscardsAnAnomalousEngineShort(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 10, MaxQPS: 1000, MaxSteps: 5, HoldSeconds: 1}
	_, jobID, _ := seedTriggeredCalibration(t, store, spec)

	runner := &stubRunner{responses: []stubRunnerResponse{
		{report: engineSaturatedReport(10, 4)}, // attempt 1: anomalous engine-short
		{report: cleanReport(10, 10)},          // retry: actually clean
	}}
	svc := calibrationapp.NewService(store).WithRunner(runner).WithFingerprint(&stubFingerprinter{value: "fp"})

	if _, err := svc.AdvanceOne(ctx, time.Now()); err != nil {
		t.Fatalf("AdvanceOne: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("runner calls = %d, want 2 (1 anomalous + 1 retry)", len(runner.calls))
	}

	job, err := svc.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// The retry's clean outcome governs: bracketing continues (doubled),
	// not bisecting.
	if job.Phase != calibration.PhaseBracketing || job.NextRequestedQPS != 20 {
		t.Fatalf("job = %+v, want Bracketing at next 20 (the retry's clean result)", job.CalibrationJob)
	}
	if len(job.Steps) != 1 || job.Steps[0].Classification != calibration.ClassificationClean {
		t.Fatalf("steps = %+v, want a single clean step (the retry's, not the discarded anomaly)", job.Steps)
	}
}

// The retry after an engine-short sizes its threads from the first attempt's
// measured response time (Little's Law), so an engine-short caused merely by
// over-provisioned, poorly-paced threads resolves cleanly instead of
// derailing the search. The first attempt itself has no measurement yet, so
// it runs with the generous default (hint 0).
func TestAdvanceOne_RetrySizesThreadsFromMeasuredLatency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 10, MaxQPS: 1000, MaxSteps: 5, HoldSeconds: 1}
	seedTriggeredCalibration(t, store, spec)

	firstAttempt := engineSaturatedReport(10, 4)
	firstAttempt.Latency = report.Percentiles{50: 0.05, 95: 0.2} // 200ms p95
	runner := &stubRunner{responses: []stubRunnerResponse{
		{report: firstAttempt},        // attempt 1: engine-short, p95 = 0.2s
		{report: cleanReport(10, 10)}, // retry (now correctly sized): clean
	}}
	svc := calibrationapp.NewService(store).WithRunner(runner).WithFingerprint(&stubFingerprinter{value: "fp"})

	if _, err := svc.AdvanceOne(ctx, time.Now()); err != nil {
		t.Fatalf("AdvanceOne: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("runner calls = %d, want 2", len(runner.calls))
	}
	// First attempt: no measurement yet, generous default.
	if runner.calls[0].latencyHintSec != 0 {
		t.Fatalf("first attempt latency hint = %v, want 0 (bootstrap)", runner.calls[0].latencyHintSec)
	}
	// Retry: sized from the first attempt's measured p95.
	if runner.calls[1].latencyHintSec != 0.2 {
		t.Fatalf("retry latency hint = %v, want the first attempt's p95 (0.2s)", runner.calls[1].latencyHintSec)
	}
}

// The incident this phase exists for, reproduced at exec 16's exact numbers
// through the real accumulator. Exec 16 (vs httpbin, jobs 4/5/6) requested
// 10 qps over a 30s hold at every step; the engine kept pace -- 293 samples --
// yet the report read 293/37 ≈ 7.9/s, because the measured span covers the 5s
// ramp-up and 2s drain as well as the hold. 7.9 < 10*0.95 then misclassified
// every step engine_saturated, and since a paced run's rate can never clear
// that bar (hold/(hold+ramp-up+drain) ≈ 81% < 95%), the search collapsed
// 10→5→2.5→1.25→0.625 and concluded per_pod_qps = 0 -- while run 86, the same
// pod unlimited, achieved 4,064 rps. Judging volume instead must read the very
// same run clean and drive the search UP; a genuine shortfall at the same hold
// must still bisect down.
func TestAdvanceOne_Exec16KeptPaceAndMustNotBisect(t *testing.T) {
	t.Parallel()

	// Rebuilds a step's report as the accumulator really assembles it: 5s
	// ramp-up (nothing completed yet), a 30s hold whose first second tapers off
	// the ramp-up, then a 2s drain -- 37 measured seconds in all.
	build := func(firstHoldSecond, holdPerSecond int64) report.Report {
		var intervals []metrics.Interval
		second := func(ts, perSec int64) {
			intervals = append(intervals, metrics.Interval{
				Timestamp: ts, Label: "get", Concurrency: 20,
				Samples: perSec, Succeeded: perSec,
			})
		}
		const start = int64(1000)
		for ts := start; ts < start+5; ts++ {
			second(ts, 0)
		}
		second(start+5, firstHoldSecond)
		for ts := start + 6; ts < start+35; ts++ {
			second(ts, holdPerSecond)
		}
		for ts := start + 35; ts < start+37; ts++ {
			second(ts, 0)
		}
		rpt := report.Build(report.Input{
			ExecutionID: 1, RunID: 1, Outcome: taurus.OutcomePassed,
			Requested: report.Load{Throughput: 10, DurationSeconds: 30},
			Intervals: intervals,
		})
		rpt.Latency = report.Percentiles{95: 0.3} // what a retry's threads would be sized from
		return rpt
	}

	tests := []struct {
		name            string
		firstHoldSecond int64
		holdPerSecond   int64
		wantSamples     int64
		wantClass       calibration.Classification
		wantNextQPS     float64
		wantCalls       int
	}{
		{
			name:            "exec 16: engine kept pace (293 samples in the 30s hold) -- clean, search proceeds up, no retry burned",
			firstHoldSecond: 3, holdPerSecond: 10, // 3 + 29*10 = 293
			wantSamples: 293,
			wantClass:   calibration.ClassificationClean, wantNextQPS: 20, wantCalls: 1,
		},
		{
			name:            "control: hold filled exactly (300 samples) -- clean despite the span-deflated 8.1/s",
			firstHoldSecond: 10, holdPerSecond: 10, // 10 + 29*10 = 300 = requested*hold
			wantSamples: 300,
			wantClass:   calibration.ClassificationClean, wantNextQPS: 20, wantCalls: 1,
		},
		{
			name:            "genuine shortfall: half the hold's volume (150 samples) -- engine-saturated, bisects down",
			firstHoldSecond: 5, holdPerSecond: 5, // 5 + 29*5 = 150
			wantSamples: 150,
			wantClass:   calibration.ClassificationEngineSaturated, wantNextQPS: 5, wantCalls: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := fake.NewStore()
			spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 10, MaxQPS: 1000, MaxSteps: 5, HoldSeconds: 30}
			_, jobID, _ := seedTriggeredCalibration(t, store, spec)

			attempt := build(tt.firstHoldSecond, tt.holdPerSecond)
			if attempt.Achieved.Samples != tt.wantSamples {
				t.Fatalf("builder produced %d samples, want %d -- the reproduction must hold exec 16's numbers exactly", attempt.Achieved.Samples, tt.wantSamples)
			}
			if attempt.Achieved.DurationSeconds != 37 {
				t.Fatalf("builder produced a %ds span, want 37 (5s ramp-up + 30s hold + 2s drain)", attempt.Achieved.DurationSeconds)
			}
			responses := []stubRunnerResponse{{report: attempt}}
			if tt.wantClass == calibration.ClassificationEngineSaturated {
				// The retry confirms: a genuine ceiling, not an anomaly.
				responses = append(responses, stubRunnerResponse{report: build(tt.firstHoldSecond, tt.holdPerSecond)})
			}
			runner := &stubRunner{responses: responses}
			svc := calibrationapp.NewService(store).WithRunner(runner).WithFingerprint(&stubFingerprinter{value: "fp"})

			if _, err := svc.AdvanceOne(context.Background(), time.Now()); err != nil {
				t.Fatalf("AdvanceOne: %v", err)
			}
			if len(runner.calls) != tt.wantCalls {
				t.Fatalf("runner calls = %d, want %d", len(runner.calls), tt.wantCalls)
			}
			job, err := svc.Get(context.Background(), jobID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			// Only the retry's outcome is recorded; it carries the verdict.
			if len(job.Steps) != 1 {
				t.Fatalf("steps = %d, want 1 (a discarded attempt leaves no trace)", len(job.Steps))
			}
			if job.Steps[0].Classification != tt.wantClass {
				t.Errorf("classification = %q, want %q", job.Steps[0].Classification, tt.wantClass)
			}
			if job.NextRequestedQPS != tt.wantNextQPS {
				t.Errorf("next requested QPS = %v, want %v", job.NextRequestedQPS, tt.wantNextQPS)
			}
		})
	}
}

func TestAdvanceOne_TargetLimitedTerminatesInOneTick(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 10, MaxQPS: 1000, MaxSteps: 5, HoldSeconds: 1}
	_, jobID, scenarioID := seedTriggeredCalibration(t, store, spec)

	// Engine keeps up (achieved == requested), but the overall error rate
	// trips the "failures>5%" criterion -- one pod already overloads the
	// target.
	runner := &stubRunner{responses: []stubRunnerResponse{{report: targetSaturatedReport(10, 10)}}}
	svc := calibrationapp.NewService(store).WithRunner(runner).WithFingerprint(&stubFingerprinter{value: "fp"})

	found, err := svc.AdvanceOne(ctx, time.Now())
	if err != nil || !found {
		t.Fatalf("found=%v, err=%v", found, err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1 -- target-saturation is not retried", len(runner.calls))
	}

	job, err := svc.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.Phase != calibration.PhaseDone || job.Result == nil || job.Result.SaturatedBy != calibration.SaturatedByTarget {
		t.Fatalf("job = %+v, want Done/target", job.CalibrationJob)
	}
	if job.Result.PerPodQPS != 10 {
		t.Fatalf("PerPodQPS = %v, want 10 (achieved at the tripping step, a lower bound)", job.Result.PerPodQPS)
	}

	profile, err := store.GetCapacityProfile(ctx, capacityprofile.Key{ScenarioID: scenarioID, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi"})
	if err != nil {
		t.Fatalf("GetCapacityProfile: %v", err)
	}
	if profile.SaturatedBy != calibration.SaturatedByTarget {
		t.Fatalf("profile.SaturatedBy = %q, want target -- a target-limited finding must still be recorded, not dropped", profile.SaturatedBy)
	}
}

func TestAdvanceOne_NeitherTerminatesAtTheSafetyCeiling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	// MaxQPS is set so the very first clean step's double (20) would
	// breach it -- the search ends honestly unresolved.
	spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 10, MaxQPS: 15, MaxSteps: 5, HoldSeconds: 1}
	_, jobID, scenarioID := seedTriggeredCalibration(t, store, spec)

	runner := &stubRunner{responses: []stubRunnerResponse{{report: cleanReport(10, 10)}}}
	svc := calibrationapp.NewService(store).WithRunner(runner).WithFingerprint(&stubFingerprinter{value: "fp"})

	found, err := svc.AdvanceOne(ctx, time.Now())
	if err != nil || !found {
		t.Fatalf("found=%v, err=%v", found, err)
	}

	job, err := svc.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.Phase != calibration.PhaseDone || job.Result == nil || job.Result.SaturatedBy != calibration.SaturatedByNeither {
		t.Fatalf("job = %+v, want Done/neither", job.CalibrationJob)
	}
	if job.Result.PerPodQPS != 10 {
		t.Fatalf("PerPodQPS = %v, want 10 (the last clean step, a lower bound)", job.Result.PerPodQPS)
	}

	profile, err := store.GetCapacityProfile(ctx, capacityprofile.Key{ScenarioID: scenarioID, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi"})
	if err != nil {
		t.Fatalf("GetCapacityProfile: %v", err)
	}
	if profile.SaturatedBy != calibration.SaturatedByNeither {
		t.Fatalf("profile.SaturatedBy = %q, want neither -- an inconclusive finding must still be recorded", profile.SaturatedBy)
	}
}

func TestAdvanceOne_SpecForFailureMarksJobFailed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 10, MaxQPS: 1000, MaxSteps: 5, HoldSeconds: 1}
	errRepo := &erroringRepo{Store: store}
	_, jobID, _ := seedTriggeredCalibrationWithRepo(t, errRepo, spec)

	sentinel := errors.New("boom")
	errRepo.criteriaForErr = sentinel
	svc := calibrationapp.NewService(errRepo).WithRunner(&stubRunner{}).WithFingerprint(&stubFingerprinter{value: "fp"})

	if _, err := svc.AdvanceOne(ctx, time.Now()); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want sentinel", err)
	}
	job, err := store.GetCalibrationJob(ctx, jobID)
	if err != nil {
		t.Fatalf("GetCalibrationJob: %v", err)
	}
	if job.Phase != calibration.PhaseFailed {
		t.Fatalf("job.Phase = %q, want failed", job.Phase)
	}
	if job.FailureReason == "" {
		t.Fatal("FailureReason is empty, want the propagated error's message")
	}
}

func TestAdvanceOne_RunStepFailureMarksJobFailed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 10, MaxQPS: 1000, MaxSteps: 5, HoldSeconds: 1}
	_, jobID, _ := seedTriggeredCalibration(t, store, spec)

	sentinel := errors.New("boom")
	runner := &stubRunner{responses: []stubRunnerResponse{{err: sentinel}}}
	svc := calibrationapp.NewService(store).WithRunner(runner).WithFingerprint(&stubFingerprinter{value: "fp"})

	if _, err := svc.AdvanceOne(ctx, time.Now()); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want sentinel", err)
	}
	job, err := store.GetCalibrationJob(ctx, jobID)
	if err != nil {
		t.Fatalf("GetCalibrationJob: %v", err)
	}
	if job.Phase != calibration.PhaseFailed {
		t.Fatalf("job.Phase = %q, want failed", job.Phase)
	}
}

func TestAdvanceOne_RunStepFailureOnRetryMarksJobFailed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 10, MaxQPS: 1000, MaxSteps: 5, HoldSeconds: 1}
	_, jobID, _ := seedTriggeredCalibration(t, store, spec)

	sentinel := errors.New("boom")
	runner := &stubRunner{responses: []stubRunnerResponse{
		{report: engineSaturatedReport(10, 4)}, // attempt 1: engine-short, triggers a retry
		{err: sentinel},                        // retry itself fails
	}}
	svc := calibrationapp.NewService(store).WithRunner(runner).WithFingerprint(&stubFingerprinter{value: "fp"})

	if _, err := svc.AdvanceOne(ctx, time.Now()); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want sentinel", err)
	}
	job, err := store.GetCalibrationJob(ctx, jobID)
	if err != nil {
		t.Fatalf("GetCalibrationJob: %v", err)
	}
	if job.Phase != calibration.PhaseFailed {
		t.Fatalf("job.Phase = %q, want failed", job.Phase)
	}
}

func TestAdvanceOne_RecordStepFailurePropagates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 10, MaxQPS: 1000, MaxSteps: 5, HoldSeconds: 1}
	errRepo := &erroringRepo{Store: store}
	seedTriggeredCalibrationWithRepo(t, errRepo, spec)

	sentinel := errors.New("boom")
	errRepo.recordStepErr = sentinel
	runner := &stubRunner{responses: []stubRunnerResponse{{report: cleanReport(10, 10)}}}
	svc := calibrationapp.NewService(errRepo).WithRunner(runner).WithFingerprint(&stubFingerprinter{value: "fp"})

	if _, err := svc.AdvanceOne(ctx, time.Now()); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want sentinel", err)
	}
}

func TestAdvanceOne_WriteProfileFailuresPropagateWithoutCorruptingJobState(t *testing.T) {
	t.Parallel()

	t.Run("no scenario configured", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store := fake.NewStore()
		projectID := seedProject(t, store)
		spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 10, MaxQPS: 1000, MaxSteps: 5, HoldSeconds: 1}
		svc := calibrationapp.NewService(store)
		src, scenarioID := seedBoundSource(t, store, projectID)
		executionID, err := svc.Create(ctx, "calibrate", projectID, taurus.ExecutorJMeter, spec, src, scenarioID)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		// Deliberately strip the binding Create made: a calibration whose
		// execution_scenario row went missing must still fail the profile
		// write loudly, not silently skip it.
		if err := store.StoreLoadProfile(ctx, executionID, false, nil); err != nil {
			t.Fatalf("StoreLoadProfile (clear binding): %v", err)
		}
		jobID, err := svc.Trigger(ctx, executionID)
		if err != nil {
			t.Fatalf("Trigger: %v", err)
		}

		runner := &stubRunner{responses: []stubRunnerResponse{{report: targetSaturatedReport(10, 10)}}}
		advSvc := calibrationapp.NewService(store).WithRunner(runner).WithFingerprint(&stubFingerprinter{value: "fp"})
		if _, err := advSvc.AdvanceOne(ctx, time.Now()); !errors.Is(err, calibrationapp.ErrScenarioNotConfigured) {
			t.Fatalf("error = %v, want ErrScenarioNotConfigured", err)
		}
		// The search outcome itself is still durably recorded as Done --
		// only the downstream profile write failed.
		job, err := store.GetCalibrationJob(ctx, jobID)
		if err != nil {
			t.Fatalf("GetCalibrationJob: %v", err)
		}
		if job.Phase != calibration.PhaseDone {
			t.Fatalf("job.Phase = %q, want done (the search succeeded even though the profile write failed)", job.Phase)
		}
	})

	t.Run("fingerprint fails", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store := fake.NewStore()
		spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 10, MaxQPS: 1000, MaxSteps: 5, HoldSeconds: 1}
		seedTriggeredCalibration(t, store, spec)

		sentinel := errors.New("boom")
		runner := &stubRunner{responses: []stubRunnerResponse{{report: targetSaturatedReport(10, 10)}}}
		svc := calibrationapp.NewService(store).WithRunner(runner).WithFingerprint(&stubFingerprinter{err: sentinel})
		if _, err := svc.AdvanceOne(ctx, time.Now()); !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want sentinel", err)
		}
	})

	t.Run("UpsertCapacityProfile fails", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store := fake.NewStore()
		spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 10, MaxQPS: 1000, MaxSteps: 5, HoldSeconds: 1}
		errRepo := &erroringRepo{Store: store}
		seedTriggeredCalibrationWithRepo(t, errRepo, spec)

		sentinel := errors.New("boom")
		errRepo.upsertCapacityProfileErr = sentinel
		runner := &stubRunner{responses: []stubRunnerResponse{{report: targetSaturatedReport(10, 10)}}}
		svc := calibrationapp.NewService(errRepo).WithRunner(runner).WithFingerprint(&stubFingerprinter{value: "fp"})
		if _, err := svc.AdvanceOne(ctx, time.Now()); !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want sentinel", err)
		}
	})

	t.Run("writeProfile's own GetExecution fails", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store := fake.NewStore()
		spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 10, MaxQPS: 1000, MaxSteps: 5, HoldSeconds: 1}
		errRepo := &erroringRepo{Store: store}
		seedTriggeredCalibrationWithRepo(t, errRepo, spec)

		sentinel := errors.New("boom")
		// Call 1 is SpecFor's own GetExecution (must still succeed, or
		// AdvanceOne would mark the job Failed instead of reaching
		// writeProfile); call 2 is writeProfile's.
		errRepo.getExecutionErr = sentinel
		errRepo.getExecutionErrFromCall = 2
		runner := &stubRunner{responses: []stubRunnerResponse{{report: targetSaturatedReport(10, 10)}}}
		svc := calibrationapp.NewService(errRepo).WithRunner(runner).WithFingerprint(&stubFingerprinter{value: "fp"})
		if _, err := svc.AdvanceOne(ctx, time.Now()); !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want sentinel", err)
		}
	})

	t.Run("writeProfile's own LoadProfileFor fails", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store := fake.NewStore()
		spec := calibration.Spec{Criterion: "failures>5%", CPU: "1", Memory: "512Mi", SeedQPS: 10, MaxQPS: 1000, MaxSteps: 5, HoldSeconds: 1}
		errRepo := &erroringRepo{Store: store}
		seedTriggeredCalibrationWithRepo(t, errRepo, spec)

		sentinel := errors.New("boom")
		errRepo.loadProfileForErr = sentinel
		errRepo.loadProfileForErrFromCall = 1
		runner := &stubRunner{responses: []stubRunnerResponse{{report: targetSaturatedReport(10, 10)}}}
		svc := calibrationapp.NewService(errRepo).WithRunner(runner).WithFingerprint(&stubFingerprinter{value: "fp"})
		if _, err := svc.AdvanceOne(ctx, time.Now()); !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want sentinel", err)
		}
	})
}

// seedTriggeredCalibrationWithRepo mirrors seedTriggeredCalibration but
// drives Create/Trigger through repo directly -- used when a test needs the
// *erroringRepo it will later flip an error on, so Create/Trigger
// themselves run error-free against the still-unset field.
func seedTriggeredCalibrationWithRepo(t *testing.T, repo calibrationapp.Repo, spec calibration.Spec) (executionID, jobID, scenarioID int64) {
	t.Helper()
	ctx := context.Background()
	er, ok := repo.(*erroringRepo)
	if !ok {
		t.Fatalf("seedTriggeredCalibrationWithRepo: repo is %T, want *erroringRepo", repo)
	}
	store := er.Store
	projectID := seedProject(t, store)
	src, scenarioID := seedBoundSource(t, store, projectID)
	svc := calibrationapp.NewService(repo)
	executionID, err := svc.Create(ctx, "calibrate", projectID, taurus.ExecutorJMeter, spec, src, scenarioID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	jobID, err = svc.Trigger(ctx, executionID)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	return executionID, jobID, scenarioID
}

func TestProfileFor_ReturnsStoredProfile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	key := capacityprofile.Key{ScenarioID: 1, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi"}
	want := capacityprofile.CapacityProfile{
		Key: key, PerPodQPS: 42, SaturatedBy: calibration.SaturatedByEngine,
		ScenarioFingerprint: "fp", JobID: 7,
	}
	if err := store.UpsertCapacityProfile(ctx, want); err != nil {
		t.Fatalf("UpsertCapacityProfile: %v", err)
	}

	svc := calibrationapp.NewService(store)
	got, err := svc.ProfileFor(ctx, key)
	if err != nil {
		t.Fatalf("ProfileFor: %v", err)
	}
	if got.PerPodQPS != 42 || got.SaturatedBy != calibration.SaturatedByEngine || got.JobID != 7 {
		t.Fatalf("ProfileFor = %+v, want %+v", got, want)
	}
}

func TestProfileFor_UnknownKeyPropagatesNotFound(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	key := capacityprofile.Key{ScenarioID: 999, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi"}
	if _, err := svc.ProfileFor(context.Background(), key); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("ProfileFor(unknown) = %v, want ErrNotFound", err)
	}
}

func TestFanOut_NoProfileReturnsStatusNoProfile(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store).WithFingerprint(&stubFingerprinter{value: "fp"})
	key := capacityprofile.Key{ScenarioID: 1, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi"}

	got, err := svc.FanOut(context.Background(), key, 100)
	if err != nil {
		t.Fatalf("FanOut: %v", err)
	}
	if got.Status != capacityprofile.StatusNoProfile {
		t.Fatalf("Status = %q, want no_profile", got.Status)
	}
}

func TestFanOut_FreshEngineLimitedProfileReturnsOK(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	key := capacityprofile.Key{ScenarioID: 1, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi"}
	if err := store.UpsertCapacityProfile(ctx, capacityprofile.CapacityProfile{
		Key: key, PerPodQPS: 50, SaturatedBy: calibration.SaturatedByEngine, ScenarioFingerprint: "fp",
	}); err != nil {
		t.Fatalf("UpsertCapacityProfile: %v", err)
	}
	svc := calibrationapp.NewService(store).WithFingerprint(&stubFingerprinter{value: "fp"})

	got, err := svc.FanOut(ctx, key, 120)
	if err != nil {
		t.Fatalf("FanOut: %v", err)
	}
	if got.Status != capacityprofile.StatusOK || got.Engines != 3 {
		t.Fatalf("FanOut = %+v, want {ok, 3} (ceil(120/50))", got)
	}
}

func TestFanOut_StaleProfileWhenScenarioFingerprintChanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	key := capacityprofile.Key{ScenarioID: 1, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi"}
	if err := store.UpsertCapacityProfile(ctx, capacityprofile.CapacityProfile{
		Key: key, PerPodQPS: 50, SaturatedBy: calibration.SaturatedByEngine, ScenarioFingerprint: "old-fp",
	}); err != nil {
		t.Fatalf("UpsertCapacityProfile: %v", err)
	}
	svc := calibrationapp.NewService(store).WithFingerprint(&stubFingerprinter{value: "new-fp"})

	got, err := svc.FanOut(ctx, key, 120)
	if err != nil {
		t.Fatalf("FanOut: %v", err)
	}
	if got.Status != capacityprofile.StatusStale {
		t.Fatalf("Status = %q, want stale", got.Status)
	}
}

func TestFanOut_RequiresFingerprintConfigured(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	key := capacityprofile.Key{ScenarioID: 1, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi"}
	if _, err := svc.FanOut(context.Background(), key, 100); !errors.Is(err, calibrationapp.ErrFingerprintNotConfigured) {
		t.Fatalf("error = %v, want ErrFingerprintNotConfigured", err)
	}
}

func TestFanOut_PropagatesFingerprintError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	key := capacityprofile.Key{ScenarioID: 1, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi"}
	if err := store.UpsertCapacityProfile(ctx, capacityprofile.CapacityProfile{
		Key: key, PerPodQPS: 50, SaturatedBy: calibration.SaturatedByEngine, ScenarioFingerprint: "fp",
	}); err != nil {
		t.Fatalf("UpsertCapacityProfile: %v", err)
	}
	sentinel := errors.New("boom")
	svc := calibrationapp.NewService(store).WithFingerprint(&stubFingerprinter{err: sentinel})

	if _, err := svc.FanOut(ctx, key, 100); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want sentinel", err)
	}
}

// TestCreate_RejectsProseCriterion (phase 42's hotfix): a criterion outside
// Taurus's expression grammar is rejected at Create with the offending
// expression named -- the cheapest place to say no, before anything is
// stored, instead of a run-time "Unsupported fail criteria subject" from
// bzt after the first trigger.
func TestCreate_RejectsProseCriterion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	projectID := seedProject(t, store)
	src, scenarioID := seedBoundSource(t, store, projectID)

	for _, raw := range []string{
		"error_rate < 0.01 AND p95 < 500ms", // the prose found live in phase 39
		"error_rate < 0.01",
		"latency>500ms", // a word, but not one Taurus supports
		" , ",           // separators only: nothing to evaluate
	} {
		spec := validSpec()
		spec.Criterion = raw
		if _, err := svc.Create(ctx, "bad", projectID, taurus.ExecutorJMeter, spec, src, scenarioID); !errors.Is(err, calibration.ErrCriterionInvalid) {
			t.Errorf("Create(criterion %q) = %v, want ErrCriterionInvalid", raw, err)
		}
	}
	// Nothing was written on any of those rejections.
	execs, err := store.ListExecutionsByProject(ctx, projectID)
	if err != nil {
		t.Fatalf("ListExecutionsByProject: %v", err)
	}
	if len(execs) != 1 || execs[0].ID != src {
		t.Errorf("executions after rejections = %v, want only the source", execs)
	}
}

// TestCreate_StoresEachExpressionAsItsOwnCriterion: a comma-separated
// criterion field splits into one stored criteria entry per expression --
// the unit compile maps onto one Taurus passfail entry -- and SpecFor joins
// them back into the same field the spec was created with.
func TestCreate_StoresEachExpressionAsItsOwnCriterion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	projectID := seedProject(t, store)
	src, scenarioID := seedBoundSource(t, store, projectID)

	spec := validSpec()
	spec.Criterion = "failures>10%, p95>500ms"
	executionID, err := svc.Create(ctx, "multi", projectID, taurus.ExecutorJMeter, spec, src, scenarioID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	criteria, err := store.CriteriaFor(ctx, executionID)
	if err != nil {
		t.Fatalf("CriteriaFor: %v", err)
	}
	if len(criteria) != 2 || criteria[0] != "failures>10%" || criteria[1] != "p95>500ms" {
		t.Fatalf("criteria = %v, want one entry per expression", criteria)
	}

	resolved, err := svc.SpecFor(ctx, executionID)
	if err != nil {
		t.Fatalf("SpecFor: %v", err)
	}
	if resolved.Criterion != "failures>10%, p95>500ms" {
		t.Errorf("SpecFor criterion = %q, want the created field joined back", resolved.Criterion)
	}
}
