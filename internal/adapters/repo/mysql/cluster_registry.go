package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heridotlife/honryu/internal/domain/clusterregistry"
	"github.com/heridotlife/honryu/internal/ports"
)

var _ ports.ClusterRegistry = (*Repository)(nil)

// clusterColumns are the domain-mapped columns. byoc_credential is deliberately
// excluded: it is opaque ciphertext accessed only through the credential
// methods, never round-tripped as part of the domain Cluster. ingest_token_hash
// is excluded for the same reason -- derived credential data, accessed only
// through the token-hash methods.
const clusterColumns = "name, api_url, ca_cert, ingest_url, sidecar_image, namespace, secret_ref, origin, geo, zone, created_by, created_time"

// CreateCluster stores c, or ports.ErrClusterExists if the name is taken.
func (r *Repository) CreateCluster(ctx context.Context, c clusterregistry.Cluster) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO cluster_registry ("+clusterColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		c.Name, c.APIURL, c.CACert, c.IngestURL, c.SidecarImage, c.Namespace, c.SecretRef, string(c.Origin), c.Geo, c.Zone, c.CreatedBy, c.CreatedTime)
	if isDuplicateKey(err) {
		return ports.ErrClusterExists
	}
	if err != nil {
		return fmt.Errorf("mysql: create cluster: %w", err)
	}
	return nil
}

// GetCluster returns the cluster named name, or ports.ErrNotFound.
func (r *Repository) GetCluster(ctx context.Context, name string) (clusterregistry.Cluster, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+clusterColumns+" FROM cluster_registry WHERE name = ?", name)
	c, err := scanCluster(row)
	if errors.Is(err, sql.ErrNoRows) {
		return clusterregistry.Cluster{}, ports.ErrNotFound
	}
	if err != nil {
		return clusterregistry.Cluster{}, fmt.Errorf("mysql: get cluster: %w", err)
	}
	return c, nil
}

// ListClusters returns every registered cluster, ordered by name.
func (r *Repository) ListClusters(ctx context.Context) ([]clusterregistry.Cluster, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+clusterColumns+" FROM cluster_registry ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("mysql: list clusters: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []clusterregistry.Cluster{}
	for rows.Next() {
		c, scanErr := scanCluster(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("mysql: scan cluster: %w", scanErr)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate clusters: %w", err)
	}
	return out, nil
}

// UpdateCluster replaces the entry named c.Name's mutable fields, or returns
// ports.ErrNotFound. created_by, created_time and byoc_credential are omitted
// -- created metadata is immutable after registration, and the credential is
// managed through its own methods. geo/zone are full-replace: whatever c
// carries (including empty, i.e. unlabeled) is what the row will say.
func (r *Repository) UpdateCluster(ctx context.Context, c clusterregistry.Cluster) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE cluster_registry SET api_url = ?, ca_cert = ?, ingest_url = ?, sidecar_image = ?, namespace = ?, secret_ref = ?, origin = ?, geo = ?, zone = ? WHERE name = ?",
		c.APIURL, c.CACert, c.IngestURL, c.SidecarImage, c.Namespace, c.SecretRef, string(c.Origin), c.Geo, c.Zone, c.Name)
	if err != nil {
		return fmt.Errorf("mysql: update cluster: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mysql: update cluster rows affected: %w", err)
	}
	if n == 0 {
		// RowsAffected is 0 for both "no such row" and "same values"; a
		// pre-check keeps the two distinguishable so an update of an existing
		// row to identical values is not misreported as not-found.
		if _, getErr := r.GetCluster(ctx, c.Name); getErr != nil {
			return getErr
		}
	}
	return nil
}

// DeleteCluster removes the cluster named name, or ports.ErrNotFound.
func (r *Repository) DeleteCluster(ctx context.Context, name string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM cluster_registry WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("mysql: delete cluster: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mysql: delete cluster rows affected: %w", err)
	}
	if n == 0 {
		return ports.ErrNotFound
	}
	return nil
}

// ResolveCluster returns the entry ref names, or ports.ErrNotFound.
func (r *Repository) ResolveCluster(ctx context.Context, ref ports.ClusterRef) (clusterregistry.Cluster, error) {
	return r.GetCluster(ctx, string(ref))
}

// SetClusterCredential stores a BYOC cluster's credential ciphertext verbatim
// against an existing entry, or returns ports.ErrNotFound. The bytes are
// opaque here -- encryption is applied by the caller (secretbox), so this
// column only ever holds ciphertext.
func (r *Repository) SetClusterCredential(ctx context.Context, name string, ciphertext []byte) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE cluster_registry SET byoc_credential = ? WHERE name = ?", ciphertext, name)
	if err != nil {
		return fmt.Errorf("mysql: set cluster credential: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mysql: set cluster credential rows affected: %w", err)
	}
	if n == 0 {
		if _, getErr := r.GetCluster(ctx, name); getErr != nil {
			return getErr
		}
	}
	return nil
}

// GetClusterCredential returns a cluster's stored credential ciphertext, or
// ports.ErrNotFound if the entry is absent. A row with no credential (operator
// entries) returns nil, nil.
func (r *Repository) GetClusterCredential(ctx context.Context, name string) ([]byte, error) {
	var ciphertext []byte
	err := r.db.QueryRowContext(ctx,
		"SELECT byoc_credential FROM cluster_registry WHERE name = ?", name).Scan(&ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mysql: get cluster credential: %w", err)
	}
	return ciphertext, nil
}

// SetClusterIngestTokenHash stores SHA-256 of a cluster's ingest token
// against an existing entry (overwrite = rotation; empty clears), or returns
// ports.ErrNotFound.
func (r *Repository) SetClusterIngestTokenHash(ctx context.Context, name string, hash string) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE cluster_registry SET ingest_token_hash = ? WHERE name = ?", hash, name)
	if err != nil {
		return fmt.Errorf("mysql: set cluster ingest token hash: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mysql: set cluster ingest token hash rows affected: %w", err)
	}
	if n == 0 {
		if _, getErr := r.GetCluster(ctx, name); getErr != nil {
			return getErr
		}
	}
	return nil
}

// ClusterByIngestTokenHash resolves an ingest token's hash to its cluster, or
// ports.ErrNotFound when no entry carries it.
func (r *Repository) ClusterByIngestTokenHash(ctx context.Context, hash string) (clusterregistry.Cluster, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+clusterColumns+" FROM cluster_registry WHERE ingest_token_hash = ?", hash)
	c, err := scanCluster(row)
	if errors.Is(err, sql.ErrNoRows) {
		return clusterregistry.Cluster{}, ports.ErrNotFound
	}
	return c, err
}

func scanCluster(s rowScanner) (clusterregistry.Cluster, error) {
	var (
		c      clusterregistry.Cluster
		origin string
	)
	if err := s.Scan(&c.Name, &c.APIURL, &c.CACert, &c.IngestURL, &c.SidecarImage,
		&c.Namespace, &c.SecretRef, &origin, &c.Geo, &c.Zone, &c.CreatedBy, &c.CreatedTime); err != nil {
		return clusterregistry.Cluster{}, err
	}
	c.Origin = clusterregistry.Origin(origin)
	return c, nil
}
