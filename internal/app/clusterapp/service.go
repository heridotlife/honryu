// Package clusterapp is the application service for the cluster registry: it
// registers, resolves, and removes the Kubernetes clusters Honryu generates
// load from. Registration is the load-bearing use case -- it validates a
// credential, probes the target for reachability and least-privilege RBAC,
// (for BYOC) encrypts the kubeconfig at rest and materializes a home-cluster
// Secret, then stores the entry. Everything infrastructural (parsing a
// kubeconfig, reading/writing a Secret, probing, encrypting) is injected as a
// port or function, so the service holds no k8s or crypto type directly.
package clusterapp

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/heridotlife/honryu/internal/domain/clusterregistry"
	"github.com/heridotlife/honryu/internal/ports"
)

// Errors callers compare with errors.Is. Registration also surfaces a
// *ports.ProbeError (probe failure) and the domain clusterregistry.Err* and
// ports.ErrClusterExists / ErrNotFound unchanged.
var (
	// ErrKubeconfigInvalid wraps a BYOC kubeconfig that is not self-contained
	// (exec/auth-provider, external file, missing credential). The wrapped
	// detail states which.
	ErrKubeconfigInvalid = errors.New("clusterapp: kubeconfig is not a self-contained, provider-neutral config")
	// ErrClusterInUse is the delete guard: a cluster with an execution mid-run
	// cannot be removed.
	ErrClusterInUse = errors.New("clusterapp: cluster has an execution with an active run")
)

// Encryptor seals and opens the BYOC credential at rest (secretbox.Cipher
// satisfies it).
type Encryptor interface {
	Seal(plaintext []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
}

// KubeconfigParser validates a self-contained kubeconfig and extracts its
// credential. Wired to the k8s adapter's ParseSelfContainedKubeconfig, adapted
// to the neutral ports.ClusterCredential at construction.
type KubeconfigParser func(raw []byte) (ports.ClusterCredential, error)

// CredentialStore reads and writes a cluster's credential as a home-cluster
// Secret (the k8s materializer/reader). Read backs operator registration
// (whose Secret is the source of truth); Materialize backs BYOC; Delete cleans
// up a materialized Secret when a BYOC registration is rolled back.
type CredentialStore interface {
	Materialize(ctx context.Context, secretName string, cred ports.ClusterCredential) error
	Read(ctx context.Context, secretName string) (ports.ClusterCredential, error)
	Delete(ctx context.Context, secretName string) error
}

// ActiveRunQuery answers the delete guard.
type ActiveRunQuery interface {
	ExecutionsWithActiveRunOnCluster(ctx context.Context, cluster string) ([]int64, error)
}

// SecretNamer computes the deterministic home-cluster Secret name for a BYOC
// cluster (the k8s adapter's CredentialSecretName).
type SecretNamer func(clusterName string) string

// TokenSource mints the raw bytes of a cluster's ingest token. Injectable for
// tests; the default draws 32 bytes from crypto/rand. Following phase 10's
// telemetry seam: randomness lives here in the app layer, the pure
// encode/hash helpers live in the domain package.
type TokenSource func() ([]byte, error)

// RegisterResult is what a successful registration returns: the stored entry
// (never carrying credential material) and, for BYOC, the ingest token shown
// to the registrant exactly once -- only its hash is stored.
type RegisterResult struct {
	Cluster     clusterregistry.Cluster
	IngestToken string
}

// Deps are the injected collaborators.
type Deps struct {
	Registry    ports.ClusterRegistry
	Prober      ports.ClusterProber
	Credentials CredentialStore
	Runs        ActiveRunQuery
	Cipher      Encryptor
	Parse       KubeconfigParser
	SecretName  SecretNamer
	// MintToken overrides the ingest-token source (tests); nil uses
	// crypto/rand.
	MintToken TokenSource
}

// Service provides cluster-registry use cases.
type Service struct {
	deps Deps
}

// NewService wires a Service.
func NewService(deps Deps) *Service { return &Service{deps: deps} }

// RegisterOperator registers a cluster whose credential Secret an operator
// manages out of band: entry.SecretRef must name an existing home-cluster
// Secret. The Secret's credential is read and probed, and entry.APIURL/CACert
// are set from it so the stored entry reflects the source of truth.
func (s *Service) RegisterOperator(ctx context.Context, entry clusterregistry.Cluster) (clusterregistry.Cluster, error) {
	entry = normalize(entry)
	entry.Origin = clusterregistry.OriginOperator
	if err := entry.Validate(); err != nil {
		return clusterregistry.Cluster{}, err
	}
	cred, err := s.deps.Credentials.Read(ctx, entry.SecretRef)
	if err != nil {
		return clusterregistry.Cluster{}, fmt.Errorf("clusterapp: read operator credential secret %q: %w", entry.SecretRef, err)
	}
	entry.APIURL = cred.APIURL
	entry.CACert = string(cred.CACert)
	if err := s.deps.Prober.Probe(ctx, cred, entry.Namespace); err != nil {
		return clusterregistry.Cluster{}, err
	}
	if err := s.deps.Registry.CreateCluster(ctx, entry); err != nil {
		return clusterregistry.Cluster{}, err
	}
	return entry, nil
}

// RegisterBYOC registers a cluster from a customer-supplied, self-contained
// kubeconfig: validate its shape, probe the target, then -- only after both
// pass -- encrypt the kubeconfig at rest, materialize a home-cluster Secret,
// and store the entry. A failure after the entry is created rolls it back so a
// half-registered cluster is not left behind.
//
// The registration also mints the cluster's ingest token: shown once in the
// result, only its hash stored. The token is minted only after the probe
// passes -- a cluster that cannot be reached yields no credential.
func (s *Service) RegisterBYOC(ctx context.Context, entry clusterregistry.Cluster, kubeconfig []byte) (RegisterResult, error) {
	entry = normalize(entry)
	entry.Origin = clusterregistry.OriginBYOC

	cred, err := s.deps.Parse(kubeconfig)
	if err != nil {
		return RegisterResult{}, fmt.Errorf("%w: %v", ErrKubeconfigInvalid, err)
	}
	entry.APIURL = cred.APIURL
	entry.CACert = string(cred.CACert)
	entry.SecretRef = s.deps.SecretName(entry.Name)
	if err := entry.Validate(); err != nil {
		return RegisterResult{}, err
	}
	if err := s.deps.Prober.Probe(ctx, cred, entry.Namespace); err != nil {
		return RegisterResult{}, err
	}
	ciphertext, err := s.deps.Cipher.Seal(kubeconfig)
	if err != nil {
		return RegisterResult{}, fmt.Errorf("clusterapp: encrypt kubeconfig: %w", err)
	}

	token, err := s.mintToken()
	if err != nil {
		return RegisterResult{}, fmt.Errorf("clusterapp: mint ingest token: %w", err)
	}

	if err := s.deps.Registry.CreateCluster(ctx, entry); err != nil {
		return RegisterResult{}, err
	}
	// The hash rides the entry's own column via the setter; a failure here is
	// a failed registration, rolled back exactly like any other half-step.
	if err := s.deps.Registry.SetClusterIngestTokenHash(ctx, entry.Name, clusterregistry.HashToken(token)); err != nil {
		_ = s.deps.Registry.DeleteCluster(ctx, entry.Name)
		return RegisterResult{}, fmt.Errorf("clusterapp: store ingest token hash: %w", err)
	}
	if err := s.deps.Credentials.Materialize(ctx, entry.SecretRef, cred); err != nil {
		_ = s.deps.Registry.DeleteCluster(ctx, entry.Name)
		return RegisterResult{}, fmt.Errorf("clusterapp: materialize credential secret: %w", err)
	}
	if err := s.deps.Registry.SetClusterCredential(ctx, entry.Name, ciphertext); err != nil {
		// The Secret was already materialized; roll back both so no orphaned
		// Secret is left behind (both best-effort -- the store error is the one
		// the caller must see).
		_ = s.deps.Credentials.Delete(ctx, entry.SecretRef)
		_ = s.deps.Registry.DeleteCluster(ctx, entry.Name)
		return RegisterResult{}, fmt.Errorf("clusterapp: store encrypted credential: %w", err)
	}
	return RegisterResult{Cluster: entry, IngestToken: token}, nil
}

// RotateIngestToken replaces a cluster's ingest token: the new one is returned
// exactly once, and the previous token dies with the hash overwrite -- a fleet
// still presenting the old token starts receiving 401s until the customer
// updates their cluster's honryu-ingest Secret.
func (s *Service) RotateIngestToken(ctx context.Context, name string) (string, error) {
	if _, err := s.deps.Registry.GetCluster(ctx, name); err != nil {
		return "", err
	}
	token, err := s.mintToken()
	if err != nil {
		return "", fmt.Errorf("clusterapp: mint ingest token: %w", err)
	}
	if err := s.deps.Registry.SetClusterIngestTokenHash(ctx, name, clusterregistry.HashToken(token)); err != nil {
		return "", fmt.Errorf("clusterapp: store ingest token hash: %w", err)
	}
	return token, nil
}

// mintToken draws the raw bytes for a new ingest token (default crypto/rand).
func (s *Service) mintToken() (string, error) {
	mint := s.deps.MintToken
	if mint == nil {
		mint = func() ([]byte, error) {
			raw := make([]byte, 32)
			if _, err := rand.Read(raw); err != nil {
				return nil, err
			}
			return raw, nil
		}
	}
	raw, err := mint()
	if err != nil {
		return "", err
	}
	return clusterregistry.EncodeToken(raw), nil
}

// Get returns the cluster named name, or ports.ErrNotFound.
func (s *Service) Get(ctx context.Context, name string) (clusterregistry.Cluster, error) {
	return s.deps.Registry.GetCluster(ctx, name)
}

// List returns every registered cluster, ordered by name.
func (s *Service) List(ctx context.Context) ([]clusterregistry.Cluster, error) {
	return s.deps.Registry.ListClusters(ctx)
}

// Resolve returns the entry a ClusterRef names, or ports.ErrNotFound.
func (s *Service) Resolve(ctx context.Context, ref ports.ClusterRef) (clusterregistry.Cluster, error) {
	return s.deps.Registry.ResolveCluster(ctx, ref)
}

// Update replaces the mutable, non-credential fields (ingest url, sidecar
// image, namespace, and the display-only geo/zone labels) of an existing
// entry, or returns ports.ErrNotFound. The credential/origin are not
// re-negotiated here -- a credential change is a re-registration. geo/zone
// are full-replace: an empty string clears the label, PUT semantics.
func (s *Service) Update(ctx context.Context, name, ingestURL, sidecarImage, namespace, geo, zone string) (clusterregistry.Cluster, error) {
	existing, err := s.deps.Registry.GetCluster(ctx, name)
	if err != nil {
		return clusterregistry.Cluster{}, err
	}
	existing.IngestURL = strings.TrimSpace(ingestURL)
	existing.SidecarImage = strings.TrimSpace(sidecarImage)
	existing.Namespace = strings.TrimSpace(namespace)
	existing.Geo = strings.TrimSpace(geo)
	existing.Zone = strings.TrimSpace(zone)
	if err := existing.Validate(); err != nil {
		return clusterregistry.Cluster{}, err
	}
	if err := s.deps.Registry.UpdateCluster(ctx, existing); err != nil {
		return clusterregistry.Cluster{}, err
	}
	return existing, nil
}

// Delete removes a cluster, guarded: it is rejected with ErrClusterInUse while
// any execution bound to it has an active run.
//
// For a BYOC entry this also tears down the credential Secret RegisterBYOC
// materialized -- Secret first, registry row second, so a teardown failure
// aborts the delete with both intact and a retry can pick up where it left
// off, rather than leaving the row gone and the Secret orphaned. An operator
// entry's Secret is never touched: RegisterOperator only ever reads it (it is
// infrastructure the operator manages out of band, not something Honryu
// created), so deleting it on deregistration would destroy operator
// infrastructure -- a worse defect than the leak this guards against.
func (s *Service) Delete(ctx context.Context, name string) error {
	entry, err := s.deps.Registry.GetCluster(ctx, name)
	if err != nil {
		return err
	}
	active, err := s.deps.Runs.ExecutionsWithActiveRunOnCluster(ctx, name)
	if err != nil {
		return err
	}
	if len(active) > 0 {
		return fmt.Errorf("%w: executions %v", ErrClusterInUse, active)
	}
	if entry.Origin == clusterregistry.OriginBYOC {
		if err := s.deps.Credentials.Delete(ctx, entry.SecretRef); err != nil {
			return fmt.Errorf("clusterapp: delete credential secret: %w", err)
		}
	}
	return s.deps.Registry.DeleteCluster(ctx, name)
}

// normalize trims the operator-provided string fields so a stray space in a
// name or ref does not create a subtly-distinct entry.
func normalize(c clusterregistry.Cluster) clusterregistry.Cluster {
	c.Name = strings.TrimSpace(c.Name)
	c.IngestURL = strings.TrimSpace(c.IngestURL)
	c.SidecarImage = strings.TrimSpace(c.SidecarImage)
	c.Namespace = strings.TrimSpace(c.Namespace)
	c.SecretRef = strings.TrimSpace(c.SecretRef)
	return c
}
