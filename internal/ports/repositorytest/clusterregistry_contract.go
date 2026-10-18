package repositorytest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/heridotlife/honryu/internal/domain/clusterregistry"
	"github.com/heridotlife/honryu/internal/ports"
)

// NewClusterRegistry builds a fresh, empty ClusterRegistry for one test.
type NewClusterRegistry func(t *testing.T) ports.ClusterRegistry

func testCluster(name string, origin clusterregistry.Origin) clusterregistry.Cluster {
	return clusterregistry.Cluster{
		Name:         name,
		APIURL:       "https://" + name + ".example:6443",
		CACert:       "-----BEGIN CERTIFICATE-----\n" + name + "\n-----END CERTIFICATE-----",
		IngestURL:    "http://honryu-ingest.honryu.svc:8080",
		SidecarImage: "registry.example/honryu-sidecar:1",
		Namespace:    "honryu",
		SecretRef:    "cluster-" + name + "-creds",
		Origin:       origin,
		// Phase 109 display labels; every cluster round-trips them.
		Geo:         "eu",
		Zone:        "eu-central-1",
		CreatedBy:   "admin@example.com",
		CreatedTime: at(100),
	}
}

// RunClusterRegistryContract pins the behaviour every ClusterRegistry must
// share: CRUD round-trips, list ordering, resolve, and not-found.
func RunClusterRegistryContract(t *testing.T, newRepo NewClusterRegistry) {
	t.Helper()

	t.Run("CreateAndGetRoundTrips", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		want := testCluster("prod-eu", clusterregistry.OriginBYOC)

		if err := repo.CreateCluster(ctx, want); err != nil {
			t.Fatalf("CreateCluster: %v", err)
		}
		got, err := repo.GetCluster(ctx, "prod-eu")
		if err != nil {
			t.Fatalf("GetCluster: %v", err)
		}
		if got.Name != want.Name || got.APIURL != want.APIURL || got.CACert != want.CACert ||
			got.IngestURL != want.IngestURL || got.SidecarImage != want.SidecarImage ||
			got.Namespace != want.Namespace || got.SecretRef != want.SecretRef ||
			got.Origin != want.Origin || got.CreatedBy != want.CreatedBy || !got.CreatedTime.Equal(want.CreatedTime) {
			t.Fatalf("GetCluster = %+v, want %+v", got, want)
		}
	})

	t.Run("CreateDuplicateReturnsExists", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		if err := repo.CreateCluster(ctx, testCluster("dup", clusterregistry.OriginOperator)); err != nil {
			t.Fatalf("CreateCluster (first): %v", err)
		}
		if err := repo.CreateCluster(ctx, testCluster("dup", clusterregistry.OriginOperator)); !errors.Is(err, ports.ErrClusterExists) {
			t.Fatalf("CreateCluster (dup) = %v, want ErrClusterExists", err)
		}
	})

	t.Run("GetMissingReturnsNotFound", func(t *testing.T) {
		repo := newRepo(t)
		if _, err := repo.GetCluster(context.Background(), "nope"); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("GetCluster(missing) = %v, want ErrNotFound", err)
		}
	})

	t.Run("ListReturnsEveryClusterOrderedByName", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		for _, name := range []string{"prod-us", "prod-eu", "staging"} {
			if err := repo.CreateCluster(ctx, testCluster(name, clusterregistry.OriginOperator)); err != nil {
				t.Fatalf("CreateCluster(%s): %v", name, err)
			}
		}
		got, err := repo.ListClusters(ctx)
		if err != nil {
			t.Fatalf("ListClusters: %v", err)
		}
		want := []string{"prod-eu", "prod-us", "staging"}
		if len(got) != len(want) {
			t.Fatalf("ListClusters len = %d, want %d (%+v)", len(got), len(want), got)
		}
		for i, c := range got {
			if c.Name != want[i] {
				t.Fatalf("ListClusters[%d].Name = %q, want %q (order: %+v)", i, c.Name, want[i], got)
			}
		}
	})

	t.Run("UpdateReplacesMutableFieldsPreservesCreatedMetadata", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		orig := testCluster("prod-eu", clusterregistry.OriginOperator)
		if err := repo.CreateCluster(ctx, orig); err != nil {
			t.Fatalf("CreateCluster: %v", err)
		}

		updated := orig
		updated.IngestURL = "http://new-ingest.honryu.svc:9090"
		updated.SidecarImage = "registry.example/honryu-sidecar:2"
		updated.SecretRef = "cluster-prod-eu-rotated"
		// A caller that forgets to carry created metadata must not erase it.
		updated.CreatedBy = "someone-else@example.com"
		updated.CreatedTime = at(999)

		if err := repo.UpdateCluster(ctx, updated); err != nil {
			t.Fatalf("UpdateCluster: %v", err)
		}
		got, err := repo.GetCluster(ctx, "prod-eu")
		if err != nil {
			t.Fatalf("GetCluster: %v", err)
		}
		if got.IngestURL != updated.IngestURL || got.SidecarImage != updated.SidecarImage || got.SecretRef != updated.SecretRef {
			t.Fatalf("UpdateCluster did not replace mutable fields: %+v", got)
		}
		if got.CreatedBy != orig.CreatedBy || !got.CreatedTime.Equal(orig.CreatedTime) {
			t.Fatalf("UpdateCluster rewrote created metadata: got by=%q time=%v, want by=%q time=%v",
				got.CreatedBy, got.CreatedTime, orig.CreatedBy, orig.CreatedTime)
		}
	})

	t.Run("UpdateMissingReturnsNotFound", func(t *testing.T) {
		repo := newRepo(t)
		if err := repo.UpdateCluster(context.Background(), testCluster("ghost", clusterregistry.OriginOperator)); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("UpdateCluster(missing) = %v, want ErrNotFound", err)
		}
	})

	t.Run("DeleteRemoves", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		if err := repo.CreateCluster(ctx, testCluster("gone", clusterregistry.OriginOperator)); err != nil {
			t.Fatalf("CreateCluster: %v", err)
		}
		if err := repo.DeleteCluster(ctx, "gone"); err != nil {
			t.Fatalf("DeleteCluster: %v", err)
		}
		if _, err := repo.GetCluster(ctx, "gone"); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("GetCluster after delete = %v, want ErrNotFound", err)
		}
	})

	t.Run("DeleteMissingReturnsNotFound", func(t *testing.T) {
		repo := newRepo(t)
		if err := repo.DeleteCluster(context.Background(), "ghost"); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("DeleteCluster(missing) = %v, want ErrNotFound", err)
		}
	})

	t.Run("ResolveByRef", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		if err := repo.CreateCluster(ctx, testCluster("prod-eu", clusterregistry.OriginOperator)); err != nil {
			t.Fatalf("CreateCluster: %v", err)
		}
		got, err := repo.ResolveCluster(ctx, ports.ClusterRef("prod-eu"))
		if err != nil {
			t.Fatalf("ResolveCluster: %v", err)
		}
		if got.Name != "prod-eu" {
			t.Fatalf("ResolveCluster.Name = %q, want prod-eu", got.Name)
		}
	})

	t.Run("ResolveUnknownReturnsNotFound", func(t *testing.T) {
		repo := newRepo(t)
		if _, err := repo.ResolveCluster(context.Background(), ports.ClusterRef("nope")); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("ResolveCluster(unknown) = %v, want ErrNotFound", err)
		}
	})

	// The credential is opaque ciphertext, stored and returned verbatim; a
	// fresh entry has none.
	t.Run("CredentialRoundTripsVerbatim", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		if err := repo.CreateCluster(ctx, testCluster("prod-eu", clusterregistry.OriginBYOC)); err != nil {
			t.Fatalf("CreateCluster: %v", err)
		}

		got, err := repo.GetClusterCredential(ctx, "prod-eu")
		if err != nil {
			t.Fatalf("GetClusterCredential (fresh): %v", err)
		}
		if got != nil {
			t.Fatalf("GetClusterCredential (fresh) = %v, want nil", got)
		}

		cipher := []byte{0x00, 0x01, 0xff, 0x7f, 0x80, 'x'}
		if err := repo.SetClusterCredential(ctx, "prod-eu", cipher); err != nil {
			t.Fatalf("SetClusterCredential: %v", err)
		}
		got, err = repo.GetClusterCredential(ctx, "prod-eu")
		if err != nil {
			t.Fatalf("GetClusterCredential: %v", err)
		}
		if !bytes.Equal(got, cipher) {
			t.Fatalf("GetClusterCredential = %v, want %v", got, cipher)
		}
	})

	t.Run("CredentialNotFoundForUnknownEntry", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		if err := repo.SetClusterCredential(ctx, "ghost", []byte{1}); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("SetClusterCredential(ghost) = %v, want ErrNotFound", err)
		}
		if _, err := repo.GetClusterCredential(ctx, "ghost"); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("GetClusterCredential(ghost) = %v, want ErrNotFound", err)
		}
	})

	// Ingest tokens: mint stores only the hash, lookup resolves it back, and
	// rotation overwrites -- the previous token dies with the write.
	t.Run("IngestTokenHashSetResolveRotate", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		a := testCluster("byoc-a", clusterregistry.OriginBYOC)
		b := testCluster("byoc-b", clusterregistry.OriginBYOC)
		if err := repo.CreateCluster(ctx, a); err != nil {
			t.Fatalf("CreateCluster(a): %v", err)
		}
		if err := repo.CreateCluster(ctx, b); err != nil {
			t.Fatalf("CreateCluster(b): %v", err)
		}

		hashA := clusterregistry.HashToken("token-a")
		if err := repo.SetClusterIngestTokenHash(ctx, "byoc-a", hashA); err != nil {
			t.Fatalf("SetClusterIngestTokenHash: %v", err)
		}
		got, err := repo.ClusterByIngestTokenHash(ctx, hashA)
		if err != nil {
			t.Fatalf("ClusterByIngestTokenHash: %v", err)
		}
		if got.Name != "byoc-a" {
			t.Fatalf("hash resolved to %q, want byoc-a", got.Name)
		}

		// Rotation: the old hash no longer resolves, the new one does.
		hashA2 := clusterregistry.HashToken("token-a-rotated")
		if err := repo.SetClusterIngestTokenHash(ctx, "byoc-a", hashA2); err != nil {
			t.Fatalf("rotate: %v", err)
		}
		if _, err := repo.ClusterByIngestTokenHash(ctx, hashA); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("old hash after rotation = %v, want ErrNotFound", err)
		}
		if got, err := repo.ClusterByIngestTokenHash(ctx, hashA2); err != nil || got.Name != "byoc-a" {
			t.Fatalf("new hash after rotation = %v, %v; want byoc-a", got, err)
		}

		// An entry with no token never resolves; an unknown hash is NotFound;
		// a missing entry cannot gain one.
		if _, err := repo.ClusterByIngestTokenHash(ctx, clusterregistry.HashToken("token-b")); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("byoc-b has no token but resolved: %v", err)
		}
		if _, err := repo.ClusterByIngestTokenHash(ctx, clusterregistry.HashToken("garbage")); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("unknown hash = %v, want ErrNotFound", err)
		}
		if err := repo.SetClusterIngestTokenHash(ctx, "ghost", hashA2); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("SetClusterIngestTokenHash(ghost) = %v, want ErrNotFound", err)
		}

		// Clearing: an empty hash makes the token dead and the entry mute.
		if err := repo.SetClusterIngestTokenHash(ctx, "byoc-a", ""); err != nil {
			t.Fatalf("clear: %v", err)
		}
		if _, err := repo.ClusterByIngestTokenHash(ctx, hashA2); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("cleared hash still resolves: %v", err)
		}
	})
}
