package clusterapp_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/heridotlife/honryu/internal/adapters/secretbox"
	"github.com/heridotlife/honryu/internal/app/clusterapp"
	"github.com/heridotlife/honryu/internal/domain/clusterregistry"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// errSecretMissing stands in for the credential store's not-found.
var errSecretMissing = errors.New("secret not found")

type fakeCredStore struct {
	materialized   map[string]ports.ClusterCredential
	secrets        map[string]ports.ClusterCredential // pre-seeded operator secrets
	materializeErr error
	deleteErr      error
	// deleteCalls logs every secret name Delete was invoked with, regardless
	// of which map it touches -- the origin-gating tests assert against this
	// log rather than materialized state, so they fail if Delete is ever
	// called for an operator cluster even if the fake's bookkeeping changes.
	deleteCalls []string
}

func newCredStore() *fakeCredStore {
	return &fakeCredStore{
		materialized: map[string]ports.ClusterCredential{},
		secrets:      map[string]ports.ClusterCredential{},
	}
}

func (f *fakeCredStore) Materialize(_ context.Context, secretName string, cred ports.ClusterCredential) error {
	if f.materializeErr != nil {
		return f.materializeErr
	}
	f.materialized[secretName] = cred
	return nil
}

func (f *fakeCredStore) Read(_ context.Context, secretName string) (ports.ClusterCredential, error) {
	c, ok := f.secrets[secretName]
	if !ok {
		return ports.ClusterCredential{}, errSecretMissing
	}
	return c, nil
}

func (f *fakeCredStore) Delete(_ context.Context, secretName string) error {
	f.deleteCalls = append(f.deleteCalls, secretName)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.materialized, secretName)
	return nil
}

// failSetCredRegistry is a ports.ClusterRegistry whose SetClusterCredential
// always fails, to drive the BYOC late-rollback path.
type failSetCredRegistry struct {
	*fake.Store
	err error
}

func (r *failSetCredRegistry) SetClusterCredential(context.Context, string, []byte) error {
	return r.err
}

type harness struct {
	svc    *clusterapp.Service
	store  *fake.Store
	prober *fake.ClusterProber
	creds  *fakeCredStore
	cipher *secretbox.Cipher
	parsed ports.ClusterCredential
	parerr error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	key := make([]byte, secretbox.KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	cipher, err := secretbox.New(key)
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	h := &harness{
		store:  fake.NewStore(),
		prober: &fake.ClusterProber{},
		creds:  newCredStore(),
		cipher: cipher,
		parsed: ports.ClusterCredential{APIURL: "https://byoc:6443", CACert: []byte("byoc-ca"), Token: "byoc-token"},
	}
	h.svc = clusterapp.NewService(clusterapp.Deps{
		Registry:    h.store,
		Prober:      h.prober,
		Credentials: h.creds,
		Runs:        h.store,
		Cipher:      cipher,
		Parse:       func([]byte) (ports.ClusterCredential, error) { return h.parsed, h.parerr },
		SecretName:  func(name string) string { return "honryu-cluster-" + name },
	})
	return h
}

func byocEntry() clusterregistry.Cluster {
	return clusterregistry.Cluster{
		Name: "prod-eu", IngestURL: "http://ingest", SidecarImage: "img", Namespace: "honryu", CreatedBy: "admin",
	}
}

func TestRegisterBYOC_Success(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	kubeconfig := []byte("apiVersion: v1\nkind: Config\n...")

	got, err := h.svc.RegisterBYOC(ctx, byocEntry(), kubeconfig)
	if err != nil {
		t.Fatalf("RegisterBYOC: %v", err)
	}
	if got.Cluster.Origin != clusterregistry.OriginBYOC || got.Cluster.SecretRef != "honryu-cluster-prod-eu" {
		t.Fatalf("entry = %+v, want byoc origin + derived secret ref", got.Cluster)
	}
	if got.Cluster.APIURL != "https://byoc:6443" || got.Cluster.CACert != "byoc-ca" {
		t.Fatalf("entry api/ca not taken from the kubeconfig: %+v", got.Cluster)
	}
	// The minted ingest token: shown once here, only its hash stored.
	if got.IngestToken == "" {
		t.Fatal("BYOC registration returned no ingest token")
	}
	stored, err := h.store.GetCluster(ctx, "prod-eu")
	if err != nil {
		t.Fatalf("GetCluster: %v", err)
	}
	if stored.IngestTokenHash != clusterregistry.HashToken(got.IngestToken) {
		t.Fatalf("stored hash = %q, want hash of the returned token", stored.IngestTokenHash)
	}
	resolved, err := h.store.ClusterByIngestTokenHash(ctx, stored.IngestTokenHash)
	if err != nil || resolved.Name != "prod-eu" {
		t.Fatalf("token hash resolved to %+v, %v; want prod-eu", resolved, err)
	}

	// Persisted.
	persisted, err := h.store.GetCluster(ctx, "prod-eu")
	if err != nil {
		t.Fatalf("GetCluster: %v", err)
	}
	if persisted.SecretRef != "honryu-cluster-prod-eu" {
		t.Fatalf("stored SecretRef = %q", persisted.SecretRef)
	}
	// Secret materialized.
	if _, ok := h.creds.materialized["honryu-cluster-prod-eu"]; !ok {
		t.Fatalf("credential Secret not materialized")
	}
	// Encrypted kubeconfig stored and recoverable.
	ct, err := h.store.GetClusterCredential(ctx, "prod-eu")
	if err != nil {
		t.Fatalf("GetClusterCredential: %v", err)
	}
	pt, err := h.cipher.Open(ct)
	if err != nil {
		t.Fatalf("decrypt stored credential: %v", err)
	}
	if !bytes.Equal(pt, kubeconfig) {
		t.Fatalf("decrypted credential = %q, want the original kubeconfig", pt)
	}
	// Probed with the parsed credential.
	if len(h.prober.Calls) != 1 || h.prober.Calls[0].Namespace != "honryu" {
		t.Fatalf("probe calls = %+v", h.prober.Calls)
	}
}

func TestRegisterBYOC_InvalidKubeconfig(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.parerr = errors.New("exec auth unsupported")
	_, err := h.svc.RegisterBYOC(context.Background(), byocEntry(), []byte("bad"))
	if !errors.Is(err, clusterapp.ErrKubeconfigInvalid) {
		t.Fatalf("RegisterBYOC(bad kubeconfig) = %v, want ErrKubeconfigInvalid", err)
	}
	// Nothing persisted, nothing materialized.
	if _, err := h.store.GetCluster(context.Background(), "prod-eu"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("cluster persisted despite invalid kubeconfig")
	}
	if len(h.creds.materialized) != 0 {
		t.Fatalf("secret materialized despite invalid kubeconfig")
	}
}

func TestRegisterBYOC_ProbeFailsIsNotStored(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.prober.Err = &ports.ProbeError{Kind: ports.ProbeUnderPrivileged, Message: "missing configmaps"}
	_, err := h.svc.RegisterBYOC(context.Background(), byocEntry(), []byte("kc"))
	var pe *ports.ProbeError
	if !errors.As(err, &pe) || pe.Kind != ports.ProbeUnderPrivileged {
		t.Fatalf("RegisterBYOC(probe fail) = %v, want ProbeError under_privileged", err)
	}
	if _, err := h.store.GetCluster(context.Background(), "prod-eu"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("cluster persisted despite probe failure")
	}
	if len(h.creds.materialized) != 0 {
		t.Fatalf("secret materialized despite probe failure")
	}
}

func TestRegisterBYOC_BlankNameRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	entry := byocEntry()
	entry.Name = "  "
	if _, err := h.svc.RegisterBYOC(context.Background(), entry, []byte("kc")); !errors.Is(err, clusterregistry.ErrNameRequired) {
		t.Fatalf("RegisterBYOC(blank name) = %v, want ErrNameRequired", err)
	}
}

func TestRegisterBYOC_DuplicateRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.RegisterBYOC(ctx, byocEntry(), []byte("kc")); err != nil {
		t.Fatalf("RegisterBYOC (first): %v", err)
	}
	if _, err := h.svc.RegisterBYOC(ctx, byocEntry(), []byte("kc2")); !errors.Is(err, ports.ErrClusterExists) {
		t.Fatalf("RegisterBYOC (dup) = %v, want ErrClusterExists", err)
	}
}

func TestRegisterBYOC_MaterializeFailureRollsBack(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.creds.materializeErr = errors.New("secret write denied")
	ctx := context.Background()
	if _, err := h.svc.RegisterBYOC(ctx, byocEntry(), []byte("kc")); err == nil {
		t.Fatalf("RegisterBYOC(materialize fail) = nil, want error")
	}
	// Rolled back -- no half-registered entry.
	if _, err := h.store.GetCluster(ctx, "prod-eu"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("entry not rolled back after materialize failure")
	}
}

// If storing the encrypted credential fails after the Secret was materialized,
// both the entry and the orphaned Secret are rolled back.
func TestRegisterBYOC_CredentialStoreFailureCleansUpSecret(t *testing.T) {
	t.Parallel()
	key := make([]byte, secretbox.KeySize)
	cipher, _ := secretbox.New(key)
	registry := &failSetCredRegistry{Store: fake.NewStore(), err: errors.New("db write failed")}
	creds := newCredStore()
	svc := clusterapp.NewService(clusterapp.Deps{
		Registry:    registry,
		Prober:      &fake.ClusterProber{},
		Credentials: creds,
		Runs:        registry.Store,
		Cipher:      cipher,
		Parse: func([]byte) (ports.ClusterCredential, error) {
			return ports.ClusterCredential{APIURL: "https://a", CACert: []byte("ca"), Token: "t"}, nil
		},
		SecretName: func(name string) string { return "honryu-cluster-" + name },
	})

	ctx := context.Background()
	if _, err := svc.RegisterBYOC(ctx, byocEntry(), []byte("kc")); err == nil {
		t.Fatalf("RegisterBYOC(store fail) = nil, want error")
	}
	// Entry rolled back.
	if _, err := registry.GetCluster(ctx, "prod-eu"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("entry not rolled back after credential-store failure")
	}
	// Materialized Secret cleaned up -- no orphan.
	if len(creds.materialized) != 0 {
		t.Fatalf("orphaned Secret left behind: %v", creds.materialized)
	}
}

func TestRegisterOperator_Success(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	h.creds.secrets["prod-eu-creds"] = ports.ClusterCredential{APIURL: "https://op:6443", CACert: []byte("op-ca"), Token: "op-token"}

	entry := clusterregistry.Cluster{
		Name: "prod-eu", IngestURL: "http://ingest", SidecarImage: "img",
		Namespace: "honryu", SecretRef: "prod-eu-creds", CreatedBy: "admin",
	}
	got, err := h.svc.RegisterOperator(ctx, entry)
	if err != nil {
		t.Fatalf("RegisterOperator: %v", err)
	}
	if got.Origin != clusterregistry.OriginOperator {
		t.Fatalf("origin = %q, want operator", got.Origin)
	}
	if got.APIURL != "https://op:6443" || got.CACert != "op-ca" {
		t.Fatalf("entry api/ca not taken from the operator secret: %+v", got)
	}
	if _, err := h.store.GetCluster(ctx, "prod-eu"); err != nil {
		t.Fatalf("operator cluster not persisted: %v", err)
	}
	// Operator flow never materializes -- the Secret is the source of truth.
	if len(h.creds.materialized) != 0 {
		t.Fatalf("operator registration should not materialize a Secret")
	}
}

func TestRegisterOperator_SecretMissing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	entry := clusterregistry.Cluster{
		Name: "prod-eu", IngestURL: "http://ingest", SidecarImage: "img",
		Namespace: "honryu", SecretRef: "absent", CreatedBy: "admin",
	}
	if _, err := h.svc.RegisterOperator(context.Background(), entry); !errors.Is(err, errSecretMissing) {
		t.Fatalf("RegisterOperator(missing secret) = %v, want errSecretMissing", err)
	}
	if _, err := h.store.GetCluster(context.Background(), "prod-eu"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("cluster persisted despite missing secret")
	}
}

func TestDelete_GuardBlocksActiveRun(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.RegisterBYOC(ctx, byocEntry(), []byte("kc")); err != nil {
		t.Fatalf("RegisterBYOC: %v", err)
	}

	// An execution on prod-eu with an active run.
	exeID, err := h.store.CreateExecution(ctx, execution.Execution{Name: "load", ProjectID: 1, Cluster: "prod-eu"})
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
	if _, err := h.store.StartRun(ctx, exeID, ""); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if err := h.svc.Delete(ctx, "prod-eu"); !errors.Is(err, clusterapp.ErrClusterInUse) {
		t.Fatalf("Delete(in use) = %v, want ErrClusterInUse", err)
	}
	// Still present.
	if _, err := h.store.GetCluster(ctx, "prod-eu"); err != nil {
		t.Fatalf("cluster removed despite active run: %v", err)
	}

	// Once the run stops, delete proceeds.
	if err := h.store.StopRun(ctx, exeID); err != nil {
		t.Fatalf("StopRun: %v", err)
	}
	if err := h.svc.Delete(ctx, "prod-eu"); err != nil {
		t.Fatalf("Delete(idle) = %v, want nil", err)
	}
	if _, err := h.store.GetCluster(ctx, "prod-eu"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("cluster not removed after delete")
	}
}

func TestDelete_NotFound(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if err := h.svc.Delete(context.Background(), "ghost"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Delete(ghost) = %v, want ErrNotFound", err)
	}
}

// Deleting a BYOC cluster must remove the credential Secret it materialized at
// registration -- the phase-12 leak this test pins shut: DELETE removed the
// registry row but left the Secret behind, found live in phase 12's dogfood and
// again in phase 13's cleanup.
func TestDelete_BYOCRemovesTheMaterializedSecret(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.RegisterBYOC(ctx, byocEntry(), []byte("kc")); err != nil {
		t.Fatalf("RegisterBYOC: %v", err)
	}
	if _, ok := h.creds.materialized["honryu-cluster-prod-eu"]; !ok {
		t.Fatalf("setup: Secret not materialized")
	}

	if err := h.svc.Delete(ctx, "prod-eu"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := h.creds.materialized["honryu-cluster-prod-eu"]; ok {
		t.Fatalf("credential Secret survived Delete -- the phase-12 leak")
	}
	if _, err := h.store.GetCluster(ctx, "prod-eu"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("registry row survived Delete")
	}
}

// An operator entry's Secret is infrastructure the operator manages out of
// band -- RegisterOperator only ever reads it (service.go, "Read backs
// operator registration (whose Secret is the source of truth)"). Deleting an
// operator cluster must never touch it: doing so would destroy operator
// infrastructure, a worse defect than the leak this phase fixes. Asserted
// against the Delete call log, not materialized-map state, so this fails even
// if the fake's internal bookkeeping changes.
func TestDelete_OperatorLeavesItsSecretUntouched(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	h.creds.secrets["prod-eu-creds"] = ports.ClusterCredential{APIURL: "https://op:6443", CACert: []byte("op-ca"), Token: "op-token"}
	entry := clusterregistry.Cluster{
		Name: "prod-eu", IngestURL: "http://ingest", SidecarImage: "img",
		Namespace: "honryu", SecretRef: "prod-eu-creds", CreatedBy: "admin",
	}
	if _, err := h.svc.RegisterOperator(ctx, entry); err != nil {
		t.Fatalf("RegisterOperator: %v", err)
	}

	if err := h.svc.Delete(ctx, "prod-eu"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(h.creds.deleteCalls) != 0 {
		t.Fatalf("Credentials.Delete called for an operator cluster: %v", h.creds.deleteCalls)
	}
	if _, ok := h.creds.secrets["prod-eu-creds"]; !ok {
		t.Fatalf("operator's out-of-band Secret was removed")
	}
}

// A teardown failure must abort the delete with the row and the Secret both
// intact -- Secret-before-row is what keeps a failed delete retryable instead
// of reproducing the phase-12 orphan the other way around.
func TestDelete_TeardownFailureAbortsAndRetrySucceeds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.RegisterBYOC(ctx, byocEntry(), []byte("kc")); err != nil {
		t.Fatalf("RegisterBYOC: %v", err)
	}
	h.creds.deleteErr = errors.New("k8s: secret delete denied")

	if err := h.svc.Delete(ctx, "prod-eu"); err == nil {
		t.Fatal("Delete succeeded despite the credential store's Delete failing")
	}
	if _, err := h.store.GetCluster(ctx, "prod-eu"); err != nil {
		t.Fatalf("registry row removed despite the aborted delete: %v", err)
	}
	if _, ok := h.creds.materialized["honryu-cluster-prod-eu"]; !ok {
		t.Fatalf("Secret removed despite the aborted delete")
	}

	h.creds.deleteErr = nil
	if err := h.svc.Delete(ctx, "prod-eu"); err != nil {
		t.Fatalf("retried Delete: %v", err)
	}
	if _, err := h.store.GetCluster(ctx, "prod-eu"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("registry row survived the retried Delete")
	}
}

// A Secret already absent (e.g. from a previous partial attempt) must not
// block Delete -- the real adapter tolerates a not-found Secret delete, and
// the fake mirrors that by construction (deleting an absent map key is a
// no-op), so this pins the contract rather than the fake's mechanics.
func TestDelete_SecretAlreadyAbsentStillSucceeds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.RegisterBYOC(ctx, byocEntry(), []byte("kc")); err != nil {
		t.Fatalf("RegisterBYOC: %v", err)
	}
	delete(h.creds.materialized, "honryu-cluster-prod-eu") // simulate a prior partial delete

	if err := h.svc.Delete(ctx, "prod-eu"); err != nil {
		t.Fatalf("Delete(Secret already absent): %v", err)
	}
	if _, err := h.store.GetCluster(ctx, "prod-eu"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("registry row survived Delete")
	}
}

func TestUpdate_ReplacesMutableFields(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.RegisterBYOC(ctx, byocEntry(), []byte("kc")); err != nil {
		t.Fatalf("RegisterBYOC: %v", err)
	}
	got, err := h.svc.Update(ctx, "prod-eu", "http://new-ingest", "img:2", "honryu2", "eu", "eu-central-1")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.IngestURL != "http://new-ingest" || got.SidecarImage != "img:2" || got.Namespace != "honryu2" {
		t.Fatalf("Update result = %+v", got)
	}
	// Phase 109: the display labels ride the same full-replace update, and
	// an empty pair clears them.
	if got.Geo != "eu" || got.Zone != "eu-central-1" {
		t.Fatalf("Update labels = %q/%q, want eu/eu-central-1", got.Geo, got.Zone)
	}
	cleared, err := h.svc.Update(ctx, "prod-eu", "http://new-ingest", "img:2", "honryu2", "", "")
	if err != nil {
		t.Fatalf("Update (clear labels): %v", err)
	}
	if cleared.Geo != "" || cleared.Zone != "" {
		t.Fatalf("Update (clear labels) = %q/%q, want empty", cleared.Geo, cleared.Zone)
	}
	if _, err := h.svc.Update(ctx, "ghost", "a", "b", "c", "", ""); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Update(ghost) = %v, want ErrNotFound", err)
	}
}

func TestGetListResolve(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.RegisterBYOC(ctx, byocEntry(), []byte("kc")); err != nil {
		t.Fatalf("RegisterBYOC: %v", err)
	}
	if _, err := h.svc.Get(ctx, "prod-eu"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	list, err := h.svc.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v (err %v), want 1", list, err)
	}
	if _, err := h.svc.Resolve(ctx, ports.ClusterRef("prod-eu")); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := h.svc.Resolve(ctx, ports.ClusterRef("nope")); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Resolve(nope) = %v, want ErrNotFound", err)
	}
}
