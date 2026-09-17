// Phase 80's write-path capture tests: creation records version 1, every
// update records the state it changes BEFORE applying it, restore is
// itself a version (append-only), and the actor rides the request context.
package scenarioapp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/domain/account"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

func newVersionedService(t *testing.T) (*scenarioapp.Service, *fake.Store, *fake.ObjectStore) {
	t.Helper()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	return scenarioapp.NewService(store, obj).WithVersions(store), store, obj
}

const validFragment = "default-address: http://example.com\nrequests:\n  - url: /checkout\n"

// mustCreate seeds a scenario through the service (so version 1 exists)
// and returns its id.
func mustCreate(t *testing.T, svc *scenarioapp.Service, ctx context.Context, name string) int64 {
	t.Helper()
	p, err := svc.Create(ctx, name, 10)
	if err != nil {
		t.Fatalf("Create(%s): %v", name, err)
	}
	return p.ID
}

func versionSnapshot(t *testing.T, svc *scenarioapp.Service, ctx context.Context, scenarioID int64, version int) ports.ScenarioSnapshot {
	t.Helper()
	v, err := svc.ScenarioVersion(ctx, scenarioID, version)
	if err != nil {
		t.Fatalf("ScenarioVersion(%d): %v", version, err)
	}
	return v.Snapshot
}

func TestVersioning_CreateCapturesVersionOne(t *testing.T) {
	t.Parallel()
	svc, _, _ := newVersionedService(t)
	ctx := context.Background()

	id := mustCreate(t, svc, ctx, "audited")

	list, err := svc.ListVersions(ctx, id)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(list) != 1 || list[0].Version != 1 {
		t.Fatalf("list = %+v, want exactly version 1", list)
	}
	snap := versionSnapshot(t, svc, ctx, id, 1)
	if snap.Name != "audited" || snap.ProjectID != 10 {
		t.Errorf("v1 snapshot = %+v, want the created scenario", snap)
	}
	if snap.Data == nil {
		t.Error("v1 data is nil; the wire shape promises an array")
	}
	if snap.Requests != "" || snap.TestFile != "" {
		t.Errorf("v1 carries files/requests it never had: %q %q", snap.TestFile, snap.Requests)
	}
}

func TestVersioning_UnwiredStoreIsUnavailableAndInert(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	svc := scenarioapp.NewService(store, obj)
	ctx := context.Background()

	if svc.VersionsEnabled() {
		t.Fatal("VersionsEnabled = true without WithVersions")
	}
	p, err := svc.Create(ctx, "plain", 10)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The un-versioned service still serves every pre-versioning use-case;
	// its version surfaces report unavailable.
	if _, err := svc.ListVersions(ctx, p.ID); !errors.Is(err, scenarioapp.ErrVersioningUnavailable) {
		t.Errorf("ListVersions = %v, want ErrVersioningUnavailable", err)
	}
	if _, err := svc.ScenarioVersion(ctx, p.ID, 1); !errors.Is(err, scenarioapp.ErrVersioningUnavailable) {
		t.Errorf("ScenarioVersion = %v, want ErrVersioningUnavailable", err)
	}
	if _, err := svc.Restore(ctx, p.ID, 1); !errors.Is(err, scenarioapp.ErrVersioningUnavailable) {
		t.Errorf("Restore = %v, want ErrVersioningUnavailable", err)
	}
	if err := svc.SetRequests(ctx, p.ID, []byte(validFragment)); err != nil {
		t.Fatalf("SetRequests without versioning: %v", err)
	}
}

func TestVersioning_UpdateCapturesBeforeApplying(t *testing.T) {
	t.Parallel()
	svc, _, obj := newVersionedService(t)
	ctx := context.Background()

	id := mustCreate(t, svc, ctx, "edited")

	// An edit: the new version records the state BEING CHANGED (no
	// requests yet), while the live scenario gains the fragment.
	if err := svc.SetRequests(ctx, id, []byte(validFragment)); err != nil {
		t.Fatalf("SetRequests: %v", err)
	}
	list, err := svc.ListVersions(ctx, id)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(list) != 2 || list[0].Version != 2 {
		t.Fatalf("list = %+v, want v2 newest first", list)
	}
	if snap := versionSnapshot(t, svc, ctx, id, 2); snap.Requests != "" {
		t.Errorf("v2 snapshot carries requests %q; the capture must precede the change", snap.Requests)
	}
	live, err := svc.Requests(ctx, id)
	if err != nil || string(live) != validFragment {
		t.Errorf("live fragment = %q (%v), want the stored edit", live, err)
	}

	// A file upload: same law.
	if err := svc.UploadFile(ctx, id, "users.csv", strings.NewReader("a,b\n")); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if snap := versionSnapshot(t, svc, ctx, id, 3); len(snap.Data) != 0 {
		t.Errorf("v3 snapshot data = %v; the capture must precede the upload", snap.Data)
	}
	files, err := svc.Files(ctx, id)
	if err != nil || len(files.Data) != 1 {
		t.Errorf("live files = %+v (%v), want the uploaded data file", files, err)
	}

	// A rejected edit records nothing: validate-then-capture, so a
	// malformed fragment never pollutes history.
	if err := svc.SetRequests(ctx, id, []byte("scenarios: {}\n")); err == nil {
		t.Fatal("SetRequests accepted a fragment with no requests")
	}
	list, err = svc.ListVersions(ctx, id)
	if err != nil || len(list) != 3 {
		t.Errorf("list after rejected edit = %+v (%v), want the same three versions", list, err)
	}
	_ = obj
}

func TestVersioning_RestoreAppendsAndNeverMutates(t *testing.T) {
	t.Parallel()
	svc, _, _ := newVersionedService(t)
	ctx := context.Background()

	id := mustCreate(t, svc, ctx, "rewound")
	v1 := versionSnapshot(t, svc, ctx, id, 1)
	if err := svc.SetRequests(ctx, id, []byte(validFragment)); err != nil {
		t.Fatalf("SetRequests: %v", err)
	}

	res, err := svc.Restore(ctx, id, 1)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.RestoredFrom != 1 {
		t.Errorf("RestoredFrom = %d, want 1", res.RestoredFrom)
	}
	if res.Version != 3 {
		t.Errorf("Version = %d, want 3 (the pre-restore capture)", res.Version)
	}

	// The pre-restore state (with the fragment) is version 3 -- the
	// restore is itself a version -- and version 1 is byte-identical to
	// what it was before the restore.
	v3 := versionSnapshot(t, svc, ctx, id, 3)
	if v3.Requests != validFragment {
		t.Errorf("v3 requests = %q, want the pre-restore fragment", v3.Requests)
	}
	v1After := versionSnapshot(t, svc, ctx, id, 1)
	if v1After.Requests != v1.Requests || v1After.Name != v1.Name {
		t.Errorf("v1 changed across the restore: %+v want %+v", v1After, v1)
	}

	// The live scenario is back to v1's shape: no fragment.
	if _, err := svc.Requests(ctx, id); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("Requests after restore-to-v1 = %v, want ErrNotFound (fragment rewound away)", err)
	}
	list, _ := svc.ListVersions(ctx, id)
	if len(list) != 3 {
		t.Errorf("list = %d rows, want 3 (append-only: restore added, nothing removed)", len(list))
	}
}

func TestVersioning_RestoreUnknownVersion(t *testing.T) {
	t.Parallel()
	svc, _, _ := newVersionedService(t)
	ctx := context.Background()

	id := mustCreate(t, svc, ctx, "x")
	if _, err := svc.Restore(ctx, id, 9); !errors.Is(err, ports.ErrScenarioVersionNotFound) {
		t.Errorf("Restore(9) = %v, want ErrScenarioVersionNotFound", err)
	}
	// Nothing was captured by the failed restore.
	if list, _ := svc.ListVersions(ctx, id); len(list) != 1 {
		t.Errorf("list = %d rows, want 1 (a failed restore records nothing)", len(list))
	}
}

func TestVersioning_RestoreReconcilesFileRecords(t *testing.T) {
	t.Parallel()
	svc, _, obj := newVersionedService(t)
	ctx := context.Background()

	id := mustCreate(t, svc, ctx, "files")
	// v1: no files. Uploading the script captures v2 (no files yet); the
	// data file captures v3 (script, no data); deleting the script
	// captures v4 (both records present, pre-delete).
	if err := svc.UploadFile(ctx, id, "load.js", strings.NewReader("export default function() {}\n")); err != nil {
		t.Fatalf("upload script: %v", err)
	}
	if err := svc.UploadFile(ctx, id, "users.csv", strings.NewReader("a\n")); err != nil {
		t.Fatalf("upload data: %v", err)
	}
	if err := svc.DeleteFile(ctx, id, "load.js"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}

	// Restore to v4 (both records present): the script's record comes back
	// and the data record is kept.
	if _, err := svc.Restore(ctx, id, 4); err != nil {
		t.Fatalf("Restore(v4): %v", err)
	}
	files, err := svc.Files(ctx, id)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if files.TestFile == nil || files.TestFile.Filename != "load.js" {
		t.Errorf("test file after restore = %+v, want load.js back", files.TestFile)
	}
	if len(files.Data) != 1 || files.Data[0].Filename != "users.csv" {
		t.Errorf("data files after restore = %+v, want users.csv kept", files.Data)
	}
	// The engine pin rode the snapshot: the script-era state is native/k6.
	sc, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if sc.Kind != "native" || sc.Engine != "k6" {
		t.Errorf("kind/engine after restore = %q/%q, want native/k6 (pinned by the restored script)", sc.Kind, sc.Engine)
	}
	// Restoring to the pre-upload capture (v2: no files at all) removes
	// the data record the later upload added.
	if _, err := svc.Restore(ctx, id, 2); err != nil {
		t.Fatalf("Restore(v2): %v", err)
	}
	files2, err := svc.Files(ctx, id)
	if err != nil {
		t.Fatalf("Files after v2 restore: %v", err)
	}
	if files2.TestFile != nil || len(files2.Data) != 0 {
		t.Errorf("files after restore to v2 = %+v, want none", files2)
	}
	_ = obj
}

func TestVersioning_ActorRidesTheRequestContext(t *testing.T) {
	t.Parallel()
	svc, _, _ := newVersionedService(t)
	ctx := account.WithContext(context.Background(), account.Account{Subject: "alice"})

	id := mustCreate(t, svc, ctx, "attributed")
	if err := svc.SetRequests(ctx, id, []byte(validFragment)); err != nil {
		t.Fatalf("SetRequests: %v", err)
	}
	list, err := svc.ListVersions(ctx, id)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v (%v), want two versions", list, err)
	}
	for _, row := range list {
		if row.CreatedBy == nil || *row.CreatedBy != "alice" {
			t.Errorf("v%d created_by = %v, want alice", row.Version, row.CreatedBy)
		}
	}

	// Without a principal (legacy no-auth mode) the actor is stored NULL,
	// the honest unknown -- never a fabricated name.
	anon := mustCreate(t, svc, context.Background(), "anonymous")
	v1, err := svc.ScenarioVersion(context.Background(), anon, 1)
	if err != nil {
		t.Fatalf("ScenarioVersion: %v", err)
	}
	if v1.CreatedBy != nil {
		t.Errorf("anonymous v1 created_by = %q, want nil", *v1.CreatedBy)
	}
}

func TestVersioning_UnknownScenario(t *testing.T) {
	t.Parallel()
	svc, _, _ := newVersionedService(t)
	ctx := context.Background()
	if _, err := svc.ListVersions(ctx, 999); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("ListVersions(999) = %v, want ErrNotFound", err)
	}
	if _, err := svc.ScenarioVersion(ctx, 999, 1); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("ScenarioVersion(999,1) = %v, want ErrNotFound", err)
	}
	if _, err := svc.Restore(ctx, 999, 1); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("Restore(999,1) = %v, want ErrNotFound", err)
	}
}
