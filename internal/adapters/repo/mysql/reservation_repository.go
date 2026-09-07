package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/heridotlife/honryu/internal/domain/reservation"
	"github.com/heridotlife/honryu/internal/ports"
)

var _ ports.ReservationRepository = (*Repository)(nil)

// tenantLockTimeout bounds how long WithTenantLock waits to acquire the
// named lock before giving up -- long enough to ride out a reclaim's Stop
// call on another connection, short enough that a stuck holder cannot wedge
// every future admission decision for the tenant+cluster indefinitely.
const tenantLockTimeout = 10 * time.Second

// WithTenantLock serializes concurrent admission decisions for tenantID+
// cluster using a MySQL named lock (GET_LOCK/RELEASE_LOCK): unlike a row
// lock, this works even when no tenant_quota row exists yet, and it is
// visible to every connection against this database -- other cmd/api or
// cmd/scheduler replicas racing the same tenant+cluster block here too, not
// just goroutines within this process.
func (r *Repository) WithTenantLock(ctx context.Context, tenantID int64, cluster string, fn func(context.Context) error) error {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("mysql: acquire connection for tenant lock: %w", err)
	}
	defer func() { _ = conn.Close() }()

	name := fmt.Sprintf("honryu:quota:%d:%s", tenantID, cluster)
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", name, tenantLockTimeout.Seconds()).Scan(&got); err != nil {
		return fmt.Errorf("mysql: get tenant lock %q: %w", name, err)
	}
	if !got.Valid || got.Int64 != 1 {
		return fmt.Errorf("mysql: could not acquire tenant lock %q within %s", name, tenantLockTimeout)
	}
	defer func() {
		// Released on the same connection that acquired it (MySQL named
		// locks are session-scoped); best effort with a fresh context since
		// ctx may already be done -- closing the connection (above, deferred
		// first so it runs after this) also drops the lock as a backstop.
		_, _ = conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", name)
	}()

	return fn(ctx)
}

// CreateReservation inserts r and returns its auto-assigned ID.
func (r *Repository) CreateReservation(ctx context.Context, res reservation.Reservation) (int64, error) {
	result, err := r.db.ExecContext(ctx,
		"INSERT INTO reservation (tenant_id, cluster, engine_count, start_time, end_time, execution_id)"+
			" VALUES (?, ?, ?, ?, ?, ?)",
		res.TenantID, res.Cluster, res.EngineCount, res.Start, res.End, res.ExecutionID)
	if err != nil {
		return 0, fmt.Errorf("mysql: create reservation: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("mysql: create reservation last id: %w", err)
	}
	return id, nil
}

// DeleteReservation removes the reservation with id, or ports.ErrNotFound.
func (r *Repository) DeleteReservation(ctx context.Context, id int64) error {
	return execDelete(ctx, r.db, "DELETE FROM reservation WHERE id = ?", id)
}

// ReleaseReservationsForExecution deletes every reservation belonging to
// executionID. Unlike DeleteReservation, deleting none is not an error --
// an execution with no reservation has nothing to release.
func (r *Repository) ReleaseReservationsForExecution(ctx context.Context, executionID int64) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM reservation WHERE execution_id = ?", executionID); err != nil {
		return fmt.Errorf("mysql: release reservations for execution: %w", err)
	}
	return nil
}

// ReservationsInWindow returns every reservation for tenant+cluster whose
// window overlaps [start, end): start_time < end AND end_time > start is the
// half-open-interval overlap test, matching reservation.Reservation.Overlaps.
func (r *Repository) ReservationsInWindow(ctx context.Context, tenantID int64, cluster string, start, end time.Time) ([]reservation.Reservation, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, tenant_id, cluster, engine_count, start_time, end_time, execution_id"+
			" FROM reservation WHERE tenant_id = ? AND cluster = ? AND start_time < ? AND end_time > ?",
		tenantID, cluster, end, start)
	if err != nil {
		return nil, fmt.Errorf("mysql: reservations in window: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []reservation.Reservation{}
	for rows.Next() {
		res, scanErr := scanReservation(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("mysql: scan reservation: %w", scanErr)
		}
		out = append(out, res)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate reservations: %w", err)
	}
	return out, nil
}

// ReservationsForTenant returns every reservation for tenant+cluster,
// regardless of window.
func (r *Repository) ReservationsForTenant(ctx context.Context, tenantID int64, cluster string) ([]reservation.Reservation, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, tenant_id, cluster, engine_count, start_time, end_time, execution_id"+
			" FROM reservation WHERE tenant_id = ? AND cluster = ?",
		tenantID, cluster)
	if err != nil {
		return nil, fmt.Errorf("mysql: reservations for tenant: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []reservation.Reservation{}
	for rows.Next() {
		res, scanErr := scanReservation(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("mysql: scan reservation: %w", scanErr)
		}
		out = append(out, res)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate reservations: %w", err)
	}
	return out, nil
}

func scanReservation(s rowScanner) (reservation.Reservation, error) {
	var res reservation.Reservation
	if err := s.Scan(&res.ID, &res.TenantID, &res.Cluster, &res.EngineCount, &res.Start, &res.End, &res.ExecutionID); err != nil {
		return reservation.Reservation{}, err
	}
	return res, nil
}

// GetCeiling returns a tenant's quota ceiling for cluster, or 0 if never
// configured -- absence is not an error, it is the normal unconfigured state.
func (r *Repository) GetCeiling(ctx context.Context, tenantID int64, cluster string) (int, error) {
	var ceiling int
	err := r.db.QueryRowContext(ctx,
		"SELECT ceiling FROM tenant_quota WHERE tenant_id = ? AND cluster = ?",
		tenantID, cluster).Scan(&ceiling)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("mysql: get ceiling: %w", err)
	}
	return ceiling, nil
}

// SetCeiling sets a tenant's per-cluster quota ceiling, overwriting whatever
// was configured before.
func (r *Repository) SetCeiling(ctx context.Context, tenantID int64, cluster string, ceiling int) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO tenant_quota (tenant_id, cluster, ceiling) VALUES (?, ?, ?)"+
			" ON DUPLICATE KEY UPDATE ceiling = VALUES(ceiling)",
		tenantID, cluster, ceiling)
	if err != nil {
		return fmt.Errorf("mysql: set ceiling: %w", err)
	}
	return nil
}

// ListClusterQuotas returns every tenant quota configured for cluster,
// ordered by tenant ID. No rows is an empty slice, not an error.
func (r *Repository) ListClusterQuotas(ctx context.Context, cluster string) ([]ports.TenantQuota, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT tenant_id, ceiling FROM tenant_quota WHERE cluster = ? ORDER BY tenant_id",
		cluster)
	if err != nil {
		return nil, fmt.Errorf("mysql: list cluster quotas: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []ports.TenantQuota{}
	for rows.Next() {
		var q ports.TenantQuota
		if err := rows.Scan(&q.TenantID, &q.Ceiling); err != nil {
			return nil, fmt.Errorf("mysql: scan cluster quota: %w", err)
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate cluster quotas: %w", err)
	}
	return out, nil
}
