package scenarioapp_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

func newScenarioService(t *testing.T) (*scenarioapp.Service, *fake.Store, *fake.ObjectStore) {
	t.Helper()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	return scenarioapp.NewService(store, obj), store, obj
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
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()

	p, err := svc.Create(ctx, "smoke", 10)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("Create returned zero ID")
	}
	got, err := svc.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "smoke" {
		t.Fatalf("Get name = %q, want smoke", got.Name)
	}
	list, err := svc.ListByProject(ctx, 10)
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListByProject len = %d, want 1", len(list))
	}
}

// TestCreate_StampsTenantFromProject is Phase 20 tenant propagation (spec
// Approach A): a scenario's tenant always equals its project's tenant.
func TestCreate_StampsTenantFromProject(t *testing.T) {
	t.Parallel()
	svc, store, _ := newScenarioService(t)
	ctx := context.Background()
	tenantID := int64(7)

	projectID := seedProject(t, store, &tenantID)
	p, err := svc.Create(ctx, "smoke", projectID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.TenantID == nil || *p.TenantID != tenantID {
		t.Fatalf("Create TenantID = %v, want %d", p.TenantID, tenantID)
	}
	got, err := svc.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TenantID == nil || *got.TenantID != tenantID {
		t.Fatalf("stored TenantID = %v, want %d", got.TenantID, tenantID)
	}
}

// TestCreate_NilTenantProjectStampsNilTenant covers a project with no
// tenant -- the scenario must inherit nil, not silently pick up a stray
// value.
func TestCreate_NilTenantProjectStampsNilTenant(t *testing.T) {
	t.Parallel()
	svc, store, _ := newScenarioService(t)
	ctx := context.Background()

	projectID := seedProject(t, store, nil)
	p, err := svc.Create(ctx, "smoke", projectID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.TenantID != nil {
		t.Fatalf("Create TenantID = %v, want nil", p.TenantID)
	}
}

func TestCreate_InvalidName(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	if _, err := svc.Create(context.Background(), "", 10); err == nil {
		t.Fatal("Create with empty name: expected error")
	}
}

func TestFileLifecycle(t *testing.T) {
	t.Parallel()
	svc, _, obj := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "smoke", 10)

	// Upload a JMX (test file) and a CSV (data file).
	if err := svc.UploadFile(ctx, p.ID, "scenario.jmx", bytes.NewReader([]byte("<jmx/>"))); err != nil {
		t.Fatalf("UploadFile jmx: %v", err)
	}
	if err := svc.UploadFile(ctx, p.ID, "users.csv", bytes.NewReader([]byte("a,b"))); err != nil {
		t.Fatalf("UploadFile csv: %v", err)
	}

	// The object store holds them under the scenario key convention.
	if _, err := obj.Download(ctx, "scenario/1/scenario.jmx"); err != nil {
		t.Fatalf("object not stored at scenario/1/scenario.jmx: %v", err)
	}

	files, err := svc.Files(ctx, p.ID)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if files.TestFile == nil || files.TestFile.Filename != "scenario.jmx" {
		t.Fatalf("TestFile = %+v, want scenario.jmx", files.TestFile)
	}
	if len(files.Data) != 1 || files.Data[0].Filename != "users.csv" || files.Data[0].URL == "" {
		t.Fatalf("Data = %+v, want [users.csv] with URL", files.Data)
	}

	// Download round trip.
	got, err := svc.DownloadFile(ctx, p.ID, "users.csv")
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if string(got) != "a,b" {
		t.Fatalf("DownloadFile = %q, want a,b", got)
	}

	// Duplicate upload is rejected.
	if err := svc.UploadFile(ctx, p.ID, "users.csv", bytes.NewReader([]byte("x"))); !errors.Is(err, ports.ErrFileExists) {
		t.Fatalf("duplicate UploadFile = %v, want ErrFileExists", err)
	}

	// Delete the data file; it disappears from storage and listing.
	if err := svc.DeleteFile(ctx, p.ID, "users.csv"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if _, err := obj.Download(ctx, "scenario/1/users.csv"); !errors.Is(err, ports.ErrObjectNotFound) {
		t.Fatalf("object still present after delete: %v", err)
	}
}

// A k6 script is a test file exactly like a JMX plan: uploads classify it
// with isTestFile (scenario_test_file), so DeleteFile must classify it the
// same way. It used to classify with isJMX, routing a .js delete at the
// data-file table -- the record survived as a phantom test file and the
// delete 404'd (phase 36, hit live deleting k6_mixed.js).
func TestDeleteFile_K6ScriptIsATestFile(t *testing.T) {
	t.Parallel()
	svc, store, obj := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "k6", 10)

	if err := svc.UploadFile(ctx, p.ID, "k6_mixed.js", bytes.NewReader([]byte("export default {}"))); err != nil {
		t.Fatalf("UploadFile js: %v", err)
	}
	if err := svc.DeleteFile(ctx, p.ID, "k6_mixed.js"); err != nil {
		t.Fatalf("DeleteFile js: %v (delete must classify .js with isTestFile, like the upload did)", err)
	}

	// The test-file slot is free again: the record is gone from
	// scenario_test_file, and the object with it.
	files, err := svc.Files(ctx, p.ID)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if files.TestFile != nil {
		t.Fatalf("TestFile after delete = %+v, want none", files.TestFile)
	}
	if _, err := obj.Download(ctx, "scenario/1/k6_mixed.js"); !errors.Is(err, ports.ErrObjectNotFound) {
		t.Fatalf("object still present after delete: %v", err)
	}
	if _, err := store.ScenarioFilesFor(ctx, p.ID); err != nil {
		t.Fatalf("ScenarioFilesFor: %v", err)
	}
}

func TestSetRequests_ValidFragmentPersists(t *testing.T) {
	t.Parallel()
	svc, store, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "portable", 10)

	raw := []byte("default-address: http://example.com\nrequests:\n  - url: /checkout\n")
	if err := svc.SetRequests(ctx, p.ID, raw); err != nil {
		t.Fatalf("SetRequests: %v", err)
	}

	got, err := store.GetScenarioRequests(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetScenarioRequests: %v", err)
	}
	if string(got) != string(raw) {
		t.Errorf("stored requests = %q, want %q (stored exactly as uploaded, not re-marshaled)", got, raw)
	}
}

// ValidateRequests is the check-only twin of SetRequests: a fragment it
// accepts must be one SetRequests stores, and validating must leave nothing
// behind. Both go through the single requestDiagnostics path, so they cannot
// diverge; this pins it.
func TestValidateRequests_MatchesSetRequestsAndStoresNothing(t *testing.T) {
	t.Parallel()
	svc, store, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "portable", 10)

	raw := []byte("requests:\n  - url: /checkout\n")
	diags, err := svc.ValidateRequests(ctx, p.ID, raw)
	if err != nil {
		t.Fatalf("ValidateRequests(valid): %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("ValidateRequests(valid) diagnostics = %+v, want none", diags)
	}
	if _, err := store.GetScenarioRequests(ctx, p.ID); err == nil {
		t.Error("ValidateRequests stored a fragment; it must validate without side effects")
	}

	// Whatever validate rejects, store rejects, with the same error: a
	// *InvalidRequestsError wrapping ErrRequestsInvalid. Rejection is the
	// ERROR, not diagnostics-with-nil-error -- the two paths share one
	// verdict, or validate and store could disagree.
	bad := []byte("requests:\n  - method: GET\n")
	if _, verr := svc.ValidateRequests(ctx, p.ID, bad); !errors.Is(verr, scenarioapp.ErrRequestsInvalid) {
		t.Fatalf("ValidateRequests(no url) = %v, want ErrRequestsInvalid", verr)
	}
	if serr := svc.SetRequests(ctx, p.ID, bad); !errors.Is(serr, scenarioapp.ErrRequestsInvalid) {
		t.Fatalf("SetRequests(no url) = %v, want ErrRequestsInvalid (parity with validate)", serr)
	}

	// A later real store still works: validation touched no state that
	// would block it.
	if err := svc.SetRequests(ctx, p.ID, raw); err != nil {
		t.Fatalf("SetRequests after validate: %v", err)
	}
}

// A non-portable scenario is rejected by validate exactly as by store --
// same sentinel, no diagnostics.
func TestValidateRequests_RejectsNativeScenario(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "validate-native", 10)
	if err := svc.UploadFile(ctx, p.ID, "scenario.jmx", strings.NewReader("<jmx/>")); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if _, err := svc.ValidateRequests(ctx, p.ID, []byte("requests:\n  - url: /a\n")); !errors.Is(err, scenarioapp.ErrScenarioNotPortable) {
		t.Fatalf("ValidateRequests(native scenario) = %v, want ErrScenarioNotPortable", err)
	}
}

// An unknown scenario is ports.ErrNotFound, before any body parsing --
// identical to SetRequests' stance.
func TestValidateRequests_UnknownScenario(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	if _, err := svc.ValidateRequests(context.Background(), 999, []byte("requests:\n  - url: /a\n")); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("ValidateRequests(unknown scenario) = %v, want ErrNotFound", err)
	}
}

// A scenario pinned native by an uploaded script has nothing that ever reads
// stored requests (compileShards only checks for a portable scenario), so
// accepting an upload here would silently store data nothing will ever use.
func TestSetRequests_RejectsNativeScenario(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "will-be-native", 10)
	if err := svc.UploadFile(ctx, p.ID, "scenario.jmx", strings.NewReader("<jmx/>")); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}

	raw := []byte("requests:\n  - url: /checkout\n")
	if err := svc.SetRequests(ctx, p.ID, raw); !errors.Is(err, scenarioapp.ErrScenarioNotPortable) {
		t.Fatalf("SetRequests(native scenario) = %v, want ErrScenarioNotPortable", err)
	}
}

func TestSetRequests_UnknownScenario(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	raw := []byte("requests:\n  - url: /checkout\n")
	if err := svc.SetRequests(context.Background(), 999, raw); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("SetRequests(unknown scenario) = %v, want ErrNotFound", err)
	}
}

func TestSetRequests_RejectsMalformedYAML(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "portable", 10)

	if err := svc.SetRequests(ctx, p.ID, []byte("not: [valid: yaml")); !errors.Is(err, scenarioapp.ErrRequestsInvalid) {
		t.Fatalf("SetRequests(malformed YAML) = %v, want ErrRequestsInvalid", err)
	}
}

func TestSetRequests_RejectsEmptyRequests(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "portable", 10)

	// Valid YAML, but no requests -- exactly what would otherwise reach
	// compile.Taurus and only fail there as ErrRequestsRequired, with a worse
	// error at the wrong layer.
	if err := svc.SetRequests(ctx, p.ID, []byte("default-address: http://example.com\n")); !errors.Is(err, scenarioapp.ErrRequestsInvalid) {
		t.Fatalf("SetRequests(no requests) = %v, want ErrRequestsInvalid", err)
	}
}

func TestSetRequests_RejectsRequestWithNoURL(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "portable", 10)

	if err := svc.SetRequests(ctx, p.ID, []byte("requests:\n  - method: GET\n")); !errors.Is(err, scenarioapp.ErrRequestsInvalid) {
		t.Fatalf("SetRequests(request with no url) = %v, want ErrRequestsInvalid", err)
	}
}

// A later upload overwrites, never merges with, an earlier one -- PUT
// semantics, matching the persistence layer's own contract.
func TestSetRequests_LaterUploadOverwrites(t *testing.T) {
	t.Parallel()
	svc, store, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "portable", 10)

	if err := svc.SetRequests(ctx, p.ID, []byte("requests:\n  - url: /one\n")); err != nil {
		t.Fatalf("SetRequests (first): %v", err)
	}
	second := []byte("requests:\n  - url: /two\n")
	if err := svc.SetRequests(ctx, p.ID, second); err != nil {
		t.Fatalf("SetRequests (second): %v", err)
	}

	got, err := store.GetScenarioRequests(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetScenarioRequests: %v", err)
	}
	if string(got) != string(second) {
		t.Errorf("stored requests = %q, want %q (the second upload alone)", got, second)
	}
}

func TestUploadFile_InvalidFilename(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "smoke", 10)

	for _, name := range []string{"", "sub/dir.csv", "..", "."} {
		if err := svc.UploadFile(ctx, p.ID, name, bytes.NewReader(nil)); !errors.Is(err, scenarioapp.ErrInvalidFilename) {
			t.Fatalf("UploadFile(%q) = %v, want ErrInvalidFilename", name, err)
		}
	}
}

func TestUploadFile_UnknownScenario(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	if err := svc.UploadFile(context.Background(), 999, "a.csv", bytes.NewReader(nil)); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("UploadFile(unknown scenario) = %v, want ErrNotFound", err)
	}
}

func TestDelete_RefusesWhenInUse(t *testing.T) {
	t.Parallel()
	svc, store, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "smoke", 10)

	coll, _ := execution.New("peak", 10)
	collID, _ := store.CreateExecution(ctx, coll)
	if err := store.StoreLoadProfile(ctx, collID, false, []loadprofile.Entry{
		{ScenarioID: p.ID, Engines: 1, Concurrency: 1, Duration: 60},
	}); err != nil {
		t.Fatalf("seed execution: %v", err)
	}

	if err := svc.Delete(ctx, p.ID); !errors.Is(err, scenarioapp.ErrScenarioInUse) {
		t.Fatalf("Delete(in use) = %v, want ErrScenarioInUse", err)
	}
}

func TestDelete_RemovesFiles(t *testing.T) {
	t.Parallel()
	svc, _, obj := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "smoke", 10)
	_ = svc.UploadFile(ctx, p.ID, "scenario.jmx", bytes.NewReader([]byte("x")))

	if err := svc.Delete(ctx, p.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := obj.Download(ctx, "scenario/1/scenario.jmx"); !errors.Is(err, ports.ErrObjectNotFound) {
		t.Fatalf("file survived scenario delete: %v", err)
	}
	if _, err := svc.Get(ctx, p.ID); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("scenario survived delete: %v", err)
	}
}

// Uploading an engine-native script decides what the scenario is. Without this a
// scenario stays portable with no requests, which nothing can compile -- so the
// upload would appear to succeed and the deploy would fail later.
func TestUploadFile_PinsTheScenarioToItsEngine(t *testing.T) {
	t.Parallel()

	cases := []struct {
		filename string
		wantKind scenario.Kind
		engine   taurus.Executor
	}{
		{"plan.jmx", scenario.KindNative, taurus.ExecutorJMeter},
		{"load.js", scenario.KindNative, taurus.ExecutorK6},
		// Data files say nothing about which engine runs the scenario.
		{"users.csv", scenario.KindPortable, ""},
	}
	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := fake.NewStore()
			svc := scenarioapp.NewService(store, fake.NewObjectStore())

			sc, err := svc.Create(ctx, "probe", 1)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if err := svc.UploadFile(ctx, sc.ID, tc.filename, strings.NewReader("x")); err != nil {
				t.Fatalf("UploadFile: %v", err)
			}

			got, err := svc.Get(ctx, sc.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Kind != tc.wantKind || got.Engine != tc.engine {
				t.Errorf("after uploading %s: kind %q engine %q, want %q/%q",
					tc.filename, got.Kind, got.Engine, tc.wantKind, tc.engine)
			}
		})
	}
}

// A scenario pinned by its script must be able to say where it can run.
func TestUploadFile_PinnedScenarioRefusesAnotherEngine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	svc := scenarioapp.NewService(store, fake.NewObjectStore())

	sc, _ := svc.Create(ctx, "probe", 1)
	if err := svc.UploadFile(ctx, sc.ID, "plan.jmx", strings.NewReader("<jmx/>")); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	got, _ := svc.Get(ctx, sc.ID)

	if err := got.CanRunOn(taurus.ExecutorJMeter); err != nil {
		t.Errorf("CanRunOn(jmeter) = %v, want nil", err)
	}
	if err := got.CanRunOn(taurus.ExecutorK6); err == nil {
		t.Error("a JMeter plan accepted k6")
	}
}

func TestImportJMX(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	svc := scenarioapp.NewService(store, obj)

	plan := `<?xml version="1.0"?><jmeterTestPlan><hashTree>
	  <TestPlan testname="Checkout"/>
	  <CSVDataSet testname="a" enabled="true"><stringProp name="filename">/abs/users.csv</stringProp></CSVDataSet>
	</hashTree></jmeterTestPlan>`

	res, err := svc.ImportJMX(ctx, "checkout", 1, "checkout.jmx", strings.NewReader(plan))
	if err != nil {
		t.Fatalf("ImportJMX: %v", err)
	}
	if res.Scenario.Kind != scenario.KindNative || res.Scenario.Engine != taurus.ExecutorJMeter {
		t.Errorf("imported scenario = kind %q engine %q", res.Scenario.Kind, res.Scenario.Engine)
	}
	if res.Report.TestPlanName != "Checkout" || len(res.Report.Findings) == 0 {
		t.Errorf("report = %+v", res.Report)
	}
	// The plan itself must be stored, or the scenario cannot run.
	files, err := svc.Files(ctx, res.Scenario.ID)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if files.TestFile == nil || files.TestFile.Filename != "checkout.jmx" {
		t.Errorf("stored files = %+v", files)
	}
}

func TestImportJMX_Rejections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := scenarioapp.NewService(fake.NewStore(), fake.NewObjectStore())

	cases := []struct{ name, filename, body string }{
		{"not a jmx file", "plan.txt", "<jmeterTestPlan/>"},
		{"malformed xml", "plan.jmx", "<jmeterTestPlan>"},
		{"not a test plan", "plan.jmx", `<project/>`},
		{"no TestPlan element", "plan.jmx", `<jmeterTestPlan><hashTree><WorkBench/></hashTree></jmeterTestPlan>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := svc.ImportJMX(ctx, "x", 1, tc.filename, strings.NewReader(tc.body)); err == nil {
				t.Errorf("ImportJMX(%s) succeeded, want an error", tc.name)
			}
		})
	}
}

func TestScenarioFingerprint_DeterministicForTheSameContent(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "smoke", 10)
	if err := svc.UploadFile(ctx, p.ID, "scenario.jmx", strings.NewReader("<jmx/>")); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}

	first, err := svc.ScenarioFingerprint(ctx, p.ID)
	if err != nil {
		t.Fatalf("ScenarioFingerprint: %v", err)
	}
	if first == "" {
		t.Fatal("ScenarioFingerprint = empty, want a real hash")
	}
	second, err := svc.ScenarioFingerprint(ctx, p.ID)
	if err != nil {
		t.Fatalf("ScenarioFingerprint (again): %v", err)
	}
	if first != second {
		t.Fatalf("ScenarioFingerprint = %q then %q, want identical for unchanged content", first, second)
	}
}

// Two entirely different scenarios with byte-identical content must
// fingerprint identically -- staleness must never trigger on identity, only
// on an actual content difference.
func TestScenarioFingerprint_IdenticalContentAcrossScenariosMatches(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()

	a, _ := svc.Create(ctx, "scenario-a", 10)
	if err := svc.UploadFile(ctx, a.ID, "scenario.jmx", strings.NewReader("<jmx>same</jmx>")); err != nil {
		t.Fatalf("UploadFile (a): %v", err)
	}
	b, _ := svc.Create(ctx, "scenario-b", 10)
	if err := svc.UploadFile(ctx, b.ID, "scenario.jmx", strings.NewReader("<jmx>same</jmx>")); err != nil {
		t.Fatalf("UploadFile (b): %v", err)
	}

	fpA, err := svc.ScenarioFingerprint(ctx, a.ID)
	if err != nil {
		t.Fatalf("ScenarioFingerprint (a): %v", err)
	}
	fpB, err := svc.ScenarioFingerprint(ctx, b.ID)
	if err != nil {
		t.Fatalf("ScenarioFingerprint (b): %v", err)
	}
	if fpA != fpB {
		t.Fatalf("fingerprints = %q, %q, want identical for byte-identical content", fpA, fpB)
	}
}

func TestScenarioFingerprint_DifferentFileContentChangesIt(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()

	a, _ := svc.Create(ctx, "scenario-a", 10)
	if err := svc.UploadFile(ctx, a.ID, "scenario.jmx", strings.NewReader("<jmx>version-1</jmx>")); err != nil {
		t.Fatalf("UploadFile (a): %v", err)
	}
	b, _ := svc.Create(ctx, "scenario-b", 10)
	if err := svc.UploadFile(ctx, b.ID, "scenario.jmx", strings.NewReader("<jmx>version-2</jmx>")); err != nil {
		t.Fatalf("UploadFile (b): %v", err)
	}

	fpA, err := svc.ScenarioFingerprint(ctx, a.ID)
	if err != nil {
		t.Fatalf("ScenarioFingerprint (a): %v", err)
	}
	fpB, err := svc.ScenarioFingerprint(ctx, b.ID)
	if err != nil {
		t.Fatalf("ScenarioFingerprint (b): %v", err)
	}
	if fpA == fpB {
		t.Fatalf("fingerprints both = %q, want different content to fingerprint differently", fpA)
	}
}

// Adding a data file (with no change to the test file itself) is a real
// content change and must not be invisible to the fingerprint.
func TestScenarioFingerprint_AddingAFileChangesIt(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "smoke", 10)
	if err := svc.UploadFile(ctx, p.ID, "scenario.jmx", strings.NewReader("<jmx/>")); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	before, err := svc.ScenarioFingerprint(ctx, p.ID)
	if err != nil {
		t.Fatalf("ScenarioFingerprint (before): %v", err)
	}

	if err := svc.UploadFile(ctx, p.ID, "data.csv", strings.NewReader("id,value\n1,2\n")); err != nil {
		t.Fatalf("UploadFile (data.csv): %v", err)
	}
	after, err := svc.ScenarioFingerprint(ctx, p.ID)
	if err != nil {
		t.Fatalf("ScenarioFingerprint (after): %v", err)
	}
	if before == after {
		t.Fatalf("fingerprint unchanged after adding a file: %q", before)
	}
}

// A portable scenario's requests fragment is content too -- editing it must
// change the fingerprint exactly as editing an uploaded file does.
func TestScenarioFingerprint_ChangingRequestsChangesIt(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "portable", 10)

	before, err := svc.ScenarioFingerprint(ctx, p.ID)
	if err != nil {
		t.Fatalf("ScenarioFingerprint (before): %v", err)
	}
	raw := []byte("default-address: http://example.com\nrequests:\n  - url: /checkout\n")
	if err := svc.SetRequests(ctx, p.ID, raw); err != nil {
		t.Fatalf("SetRequests: %v", err)
	}
	after, err := svc.ScenarioFingerprint(ctx, p.ID)
	if err != nil {
		t.Fatalf("ScenarioFingerprint (after): %v", err)
	}
	if before == after {
		t.Fatalf("fingerprint unchanged after SetRequests: %q", before)
	}
}

func TestScenarioFingerprint_UnknownScenarioPropagatesNotFound(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	if _, err := svc.ScenarioFingerprint(context.Background(), 999); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("ScenarioFingerprint(unknown) = %v, want ErrNotFound", err)
	}
}

// errObjectStore wraps *fake.ObjectStore and lets a test force Download to
// fail, proving ScenarioFingerprint surfaces a downstream failure rather
// than silently hashing partial content.
type errObjectStore struct {
	*fake.ObjectStore
	downloadErr error
}

func (o *errObjectStore) Download(ctx context.Context, key string) ([]byte, error) {
	if o.downloadErr != nil {
		return nil, o.downloadErr
	}
	return o.ObjectStore.Download(ctx, key)
}

func TestScenarioFingerprint_FileDownloadErrorPropagates(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	obj := &errObjectStore{ObjectStore: fake.NewObjectStore()}
	svc := scenarioapp.NewService(store, obj)
	ctx := context.Background()

	p, _ := svc.Create(ctx, "smoke", 10)
	if err := svc.UploadFile(ctx, p.ID, "scenario.jmx", strings.NewReader("<jmx/>")); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}

	obj.downloadErr = errors.New("boom")
	if _, err := svc.ScenarioFingerprint(ctx, p.ID); err == nil {
		t.Fatal("ScenarioFingerprint = nil error, want the Download failure to propagate")
	}
}

// errRepo wraps *fake.Store and lets a test force GetScenarioRequests to
// fail with something other than ErrNotFound, proving that -- unlike the
// "no requests uploaded yet" case -- a real downstream failure propagates
// rather than being silently treated as "no requests".
type errRepo struct {
	*fake.Store
	requestsErr error
}

func (r *errRepo) GetScenarioRequests(ctx context.Context, scenarioID int64) ([]byte, error) {
	if r.requestsErr != nil {
		return nil, r.requestsErr
	}
	return r.Store.GetScenarioRequests(ctx, scenarioID)
}

func TestScenarioFingerprint_RequestsLookupErrorPropagates(t *testing.T) {
	t.Parallel()
	store := &errRepo{Store: fake.NewStore(), requestsErr: errors.New("boom")}
	svc := scenarioapp.NewService(store, fake.NewObjectStore())
	ctx := context.Background()

	p, _ := svc.Create(ctx, "portable", 10)
	if _, err := svc.ScenarioFingerprint(ctx, p.ID); err == nil {
		t.Fatal("ScenarioFingerprint = nil error, want the GetScenarioRequests failure to propagate")
	}
}

// G6: keys taurus.Scenario does not model (think-time is the canonical
// example) must be reported as severity info -- stored but not compiled and
// will not affect the run -- never as errors, and the document must still
// validate and still store. compileScenario builds a fresh taurus.Scenario
// from modelled fields only, so "passes through" would be the exact inverse
// of the truth; the wording below is asserted.
func TestUncompiledKeys_ReportAsInfoAndStillStore(t *testing.T) {
	t.Parallel()
	svc, store, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "portable", 10)

	raw := []byte("think-time: 1.5s\nrequests:\n  - url: /checkout\n")

	// Validate: accepted, with an info diagnostic carrying the wording.
	diags, err := svc.ValidateRequests(ctx, p.ID, raw)
	if err != nil {
		t.Fatalf("ValidateRequests rejected a document with an uncompiled key: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %d entries (%v), want exactly 1 info", len(diags), diags)
	}
	d := diags[0]
	if d.Severity != scenarioapp.SeverityInfo {
		t.Errorf("severity = %q, want info (never an error)", d.Severity)
	}
	if d.Line != 1 {
		t.Errorf("line = %d, want 1 (think-time is the first line)", d.Line)
	}
	if !strings.Contains(d.Message, "stored but not compiled and will not affect the run") {
		t.Errorf("message %q must state the key is stored but not compiled and will not affect the run", d.Message)
	}
	if strings.Contains(d.Message, "pass") {
		t.Errorf("message %q must not claim the key passes through", d.Message)
	}

	// Store: the document still stores, byte-for-byte, info diags or not.
	if err := svc.SetRequests(ctx, p.ID, raw); err != nil {
		t.Fatalf("SetRequests rejected a document with an uncompiled key: %v", err)
	}
	got, err := store.GetScenarioRequests(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetScenarioRequests: %v", err)
	}
	if string(got) != string(raw) {
		t.Errorf("stored = %q, want %q byte-for-byte", got, raw)
	}
}

// A fragment with only modelled keys validates with zero diagnostics -- the
// info path must not invent findings.
func TestUncompiledKeys_NoneWhenAllModelled(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "portable", 10)

	raw := []byte("default-address: http://example.com\nrequests:\n  - url: /\n")
	diags, err := svc.ValidateRequests(ctx, p.ID, raw)
	if err != nil {
		t.Fatalf("ValidateRequests: %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("diagnostics = %v, want none for a fully-modelled fragment", diags)
	}
}

// A fragment with BOTH an uncompiled key and a real error (no url) must be
// rejected -- unknown-key info must never mask a genuine rejection.
func TestUncompiledKeys_ErrorStillRejects(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "portable", 10)

	raw := []byte("think-time: 1.5s\nrequests:\n  - label: broken\n")
	_, err := svc.ValidateRequests(ctx, p.ID, raw)
	var inv *scenarioapp.InvalidRequestsError
	if !errors.As(err, &inv) {
		t.Fatalf("ValidateRequests = %v, want InvalidRequestsError (info must not mask the missing url)", err)
	}
}

// Phase 19b F2 (review finding 2): KnownFields(true) only catches keys
// taurus.Scenario does not model at all. data-sources and script ARE on the
// struct, so a strict decode sees them as known and reports nothing -- yet
// compileScenario never reads either from the fragment (DataSources comes
// from ScenarioInput.DataPaths, resolved from uploaded files; Script from
// ScenarioInput.ScriptPath, and only for a native scenario). Before this fix
// a fragment carrying data-sources got zero diagnostics, live-verified
// against the deployed API on 2026-09-01: an operator parameterising a test
// would see no warning and get an unparameterised run.
func TestUncompiledKeys_DataSourcesFlaggedThoughModelled(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "portable", 10)

	raw := []byte("requests:\n  - url: /a\ndata-sources:\n  - path: users.csv\n")
	diags, err := svc.ValidateRequests(ctx, p.ID, raw)
	if err != nil {
		t.Fatalf("ValidateRequests rejected a document with data-sources: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %d entries (%v), want exactly 1 info for data-sources", len(diags), diags)
	}
	d := diags[0]
	if d.Severity != scenarioapp.SeverityInfo {
		t.Errorf("severity = %q, want info", d.Severity)
	}
	if d.Line != 3 {
		t.Errorf("line = %d, want 3 (data-sources is the third line)", d.Line)
	}
	if !strings.Contains(d.Message, "data-sources") {
		t.Errorf("message %q must name data-sources", d.Message)
	}
	if !strings.Contains(d.Message, "stored but not compiled and will not affect the run") {
		t.Errorf("message %q must state the key is stored but not compiled", d.Message)
	}
}

// Same failure mode, for a portable fragment's own script: key. Modelled on
// taurus.Scenario, but compileScenario only ever reads Script from
// ScenarioInput.ScriptPath for a NATIVE scenario -- a portable fragment's
// script: is never read at all.
func TestUncompiledKeys_ScriptFlaggedThoughModelled(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "portable", 10)

	raw := []byte("script: custom.jmx\nrequests:\n  - url: /a\n")
	diags, err := svc.ValidateRequests(ctx, p.ID, raw)
	if err != nil {
		t.Fatalf("ValidateRequests rejected a document with script: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %d entries (%v), want exactly 1 info for script", len(diags), diags)
	}
	if diags[0].Severity != scenarioapp.SeverityInfo {
		t.Errorf("severity = %q, want info", diags[0].Severity)
	}
	if !strings.Contains(diags[0].Message, "script") {
		t.Errorf("message %q must name script", diags[0].Message)
	}
}

// A fragment mixing all three uncompiled-key shapes -- think-time (unknown
// to the type entirely), data-sources and script (modelled but never
// compiled from the fragment) -- yields three independent diagnostics, each
// anchored to its own line. Detection of the two shapes must not interfere.
func TestUncompiledKeys_MixedSourcesAllReported(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "portable", 10)

	raw := []byte("think-time: 1.5s\nrequests:\n  - url: /a\ndata-sources:\n  - path: users.csv\nscript: custom.jmx\n")
	diags, err := svc.ValidateRequests(ctx, p.ID, raw)
	if err != nil {
		t.Fatalf("ValidateRequests rejected a mixed uncompiled-key document: %v", err)
	}
	if len(diags) != 3 {
		t.Fatalf("diagnostics = %d entries (%v), want 3 (think-time, data-sources, script)", len(diags), diags)
	}
	seen := map[string]bool{}
	for _, d := range diags {
		if d.Severity != scenarioapp.SeverityInfo {
			t.Errorf("diagnostic %v: severity = %q, want info", d, d.Severity)
		}
		for _, key := range []string{"think-time", "data-sources", "script"} {
			if strings.Contains(d.Message, key) {
				seen[key] = true
			}
		}
	}
	for _, key := range []string{"think-time", "data-sources", "script"} {
		if !seen[key] {
			t.Errorf("no diagnostic mentioned %q", key)
		}
	}
}

// think-time keeps its existing single-diagnostic, line-1 behaviour
// unchanged after the modelled-but-uncompiled detection was added --
// regression guard for review finding 2's fix.
func TestUncompiledKeys_ThinkTimeUnaffectedByModelledDetection(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScenarioService(t)
	ctx := context.Background()
	p, _ := svc.Create(ctx, "portable", 10)

	raw := []byte("think-time: 1.5s\nrequests:\n  - url: /checkout\n")
	diags, err := svc.ValidateRequests(ctx, p.ID, raw)
	if err != nil {
		t.Fatalf("ValidateRequests: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %d entries (%v), want exactly 1 (unchanged from before F2)", len(diags), diags)
	}
	if diags[0].Line != 1 {
		t.Errorf("line = %d, want 1", diags[0].Line)
	}
}
