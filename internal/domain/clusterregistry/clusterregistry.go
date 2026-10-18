// Package clusterregistry holds the Cluster aggregate: a Kubernetes cluster an
// operator has registered as a place Honryu can generate load from. Pure
// domain: no I/O, no persistence. Credential material -- reading/writing the
// home-cluster k8s Secret, encrypting a BYOC kubeconfig at rest -- is an
// adapter concern and lives well outside this package.
package clusterregistry

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Origin records where a registered cluster's credentials come from, which
// determines who owns the source of truth.
type Origin string

const (
	// OriginOperator marks a cluster whose home-cluster k8s Secret a platform
	// operator created and manages out of band; the registry entry only
	// references it.
	OriginOperator Origin = "operator"
	// OriginBYOC marks a customer-supplied cluster: a self-contained kubeconfig
	// stored encrypted-at-rest in MySQL and materialized into a home-cluster
	// k8s Secret. The encrypted kubeconfig is the source of truth; the Secret
	// is a reconciled copy the scheduler reads.
	OriginBYOC Origin = "byoc"
)

// DefaultName is the ClusterRef of the implicit default cluster: the control
// plane's own in-cluster credentials. An execution that names no cluster
// resolves to it. It is never stored as a registry row -- the scheduler
// resolves it straight to rest.InClusterConfig() -- so a *registered* cluster
// must carry a non-empty name, which Validate enforces.
const DefaultName = ""

// IsDefaultName reports whether ref denotes the implicit default cluster,
// distinguishing it from every registered entry. Callers resolving a
// ClusterRef check this before consulting the registry.
func IsDefaultName(ref string) bool {
	return strings.TrimSpace(ref) == DefaultName
}

// Validation errors. Callers compare with errors.Is.
var (
	ErrNameRequired         = errors.New("clusterregistry: name is required")
	ErrOriginUnknown        = errors.New("clusterregistry: origin must be operator or byoc")
	ErrSecretRefRequired    = errors.New("clusterregistry: a credential reference (k8s secret) is required")
	ErrNamespaceRequired    = errors.New("clusterregistry: namespace is required")
	ErrIngestURLRequired    = errors.New("clusterregistry: ingest url is required")
	ErrSidecarImageRequired = errors.New("clusterregistry: sidecar image is required")
	// ErrLabelTooLong / ErrLabelControl bound the optional Geo/Zone display
	// labels: longer than the column, or carrying control characters.
	ErrLabelTooLong = errors.New("clusterregistry: geo/zone label exceeds 64 characters")
	ErrLabelControl = errors.New("clusterregistry: geo/zone label must not contain control characters")
)

// Cluster is a registered Kubernetes cluster Honryu can deploy engines into.
//
// Name is the ClusterRef, a plain string rather than ports.ClusterRef, for the
// same reason as reservation.Reservation.Cluster: domain packages do not import
// ports -- the dependency runs the other way -- and the two are interchangeable
// representations of the same identifier.
type Cluster struct {
	Name string
	// APIURL and CACert address and trust the target cluster's API server.
	APIURL string
	CACert string
	// IngestURL is where engines deployed into this cluster push their metrics;
	// it must be reachable from inside the cluster and is per-cluster because a
	// GKE cluster and an on-prem cluster need not share one reachable address.
	IngestURL string
	// SidecarImage is the metrics sidecar image reachable from this cluster,
	// per-cluster for the same reason as IngestURL.
	SidecarImage string
	// Namespace is the single namespace Honryu deploys into on this cluster.
	Namespace string
	// SecretRef names the home-cluster k8s Secret the scheduler reads to build
	// this cluster's client. Every entry references one -- consumption is
	// uniform regardless of Origin.
	SecretRef string
	// IngestTokenHash is SHA-256 of this cluster's ingest token (see token.go),
	// empty for entries that have none minted. The token itself is shown to
	// the registrant exactly once at mint/rotate and never stored -- this
	// field is credential-derived data, never a credential, and never appears
	// in any API response.
	IngestTokenHash string
	// Origin records who owns the credential's source of truth.
	Origin Origin
	// Geo and Zone are optional display-only labels (e.g. "eu",
	// "eu-central-1") for surfaces that fan out across clusters: the
	// NewTest picker and the registry page show them so a user can reason
	// about latency and geographic spread. They are never scheduling or
	// routing inputs -- placement stays name-driven -- and the empty string
	// means unlabeled (the pre-phase-109 state of every row).
	Geo string
	// Zone is Geo's finer half: same display-only contract, same
	// name-driven scheduling.
	Zone string
	// CreatedBy and CreatedTime are audit metadata assigned at registration.
	CreatedBy   string
	CreatedTime time.Time
}

// Validate checks a registered cluster's own invariants, independent of
// connectivity or persistence. The implicit default (see DefaultName) is never
// a Cluster value passed here -- it is resolved directly -- so a blank name is
// always an error, not "the default".
func (c Cluster) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return ErrNameRequired
	}
	switch c.Origin {
	case OriginOperator, OriginBYOC:
		// known
	default:
		return fmt.Errorf("%w: %q", ErrOriginUnknown, c.Origin)
	}
	// Uniform consumption: the scheduler always builds a client by reading the
	// referenced Secret, so an entry without one is unusable. It is especially
	// load-bearing for BYOC, where the Secret is the only materialized handle
	// to a credential we encrypted and stored ourselves.
	if strings.TrimSpace(c.SecretRef) == "" {
		return ErrSecretRefRequired
	}
	// A registered cluster carries its own deploy settings -- the namespace it
	// deploys into, the metrics sidecar image reachable from it, and the ingest
	// URL its engines push to. These moved off global config (Phase 7) to
	// per-cluster in Phase 8, since a GKE cluster and an on-prem cluster need
	// not share one reachable ingest address or image source, so every
	// registered cluster must set them.
	if strings.TrimSpace(c.Namespace) == "" {
		return ErrNamespaceRequired
	}
	if strings.TrimSpace(c.IngestURL) == "" {
		return ErrIngestURLRequired
	}
	if strings.TrimSpace(c.SidecarImage) == "" {
		return ErrSidecarImageRequired
	}
	// Geo/Zone are optional (empty = unlabeled), but whatever is set must
	// fit its column and carry no control characters -- these strings reach
	// the UI verbatim.
	if err := validateLabel("geo", c.Geo); err != nil {
		return err
	}
	if err := validateLabel("zone", c.Zone); err != nil {
		return err
	}
	return nil
}

// MaxLabelLen bounds Geo/Zone, matching the VARCHAR(64) columns: they are
// labels for humans, not identifiers.
const MaxLabelLen = 64

// validateLabel checks one optional display label: empty is fine
// (unlabeled), but a label longer than the column or carrying control
// characters is not.
func validateLabel(field, label string) error {
	if len(label) > MaxLabelLen {
		return fmt.Errorf("%w: %s is %d characters", ErrLabelTooLong, field, len(label))
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %s", ErrLabelControl, field)
		}
	}
	return nil
}
