package ports

import (
	"context"
	"time"

	"github.com/heridotlife/honryu/internal/domain/reservation"
)

// TenantQuota is one tenant's configured engine ceiling on a cluster -- one
// row of the quota ledger, as ListClusterQuotas returns them.
type TenantQuota struct {
	TenantID int64
	Ceiling  int
}

// ReservationRepository persists time-bounded engine-capacity reservations --
// the ledger that makes quota a guarantee rather than a best-effort check.
type ReservationRepository interface {
	// CreateReservation persists r and returns its assigned ID.
	CreateReservation(ctx context.Context, r reservation.Reservation) (int64, error)
	// DeleteReservation removes a reservation, freeing its capacity
	// immediately rather than waiting for its declared end time.
	DeleteReservation(ctx context.Context, id int64) error
	// ReservationsInWindow returns every reservation for tenant+cluster whose
	// window overlaps [start, end) -- the query a quota check runs to decide
	// whether a new reservation fits.
	ReservationsInWindow(ctx context.Context, tenantID int64, cluster string, start, end time.Time) ([]reservation.Reservation, error)
	// ReservationsForTenant returns every reservation for tenant+cluster,
	// regardless of window -- what an overrun-reclaim pass scans to find
	// reservations whose declared end has already passed.
	ReservationsForTenant(ctx context.Context, tenantID int64, cluster string) ([]reservation.Reservation, error)

	// GetCeiling returns a tenant's engine quota ceiling for cluster, or 0 if
	// never configured -- nothing runs until a ceiling is explicitly set,
	// rather than defaulting to unlimited.
	GetCeiling(ctx context.Context, tenantID int64, cluster string) (int, error)
	// SetCeiling sets a tenant's per-cluster engine quota ceiling.
	SetCeiling(ctx context.Context, tenantID int64, cluster string, ceiling int) error
	// ListClusterQuotas returns every tenant quota configured for cluster,
	// ordered by tenant ID -- the cluster-scoped read an aggregate capacity
	// view sums across tenants (GetCeiling answers one tenant at a time).
	// No rows is an empty slice, not an error: a cluster nobody configured a
	// ceiling on is the normal unconfigured state.
	ListClusterQuotas(ctx context.Context, cluster string) ([]TenantQuota, error)

	// ReleaseReservationsForExecution deletes every reservation belonging to
	// executionID, freeing their capacity immediately. Not an error when
	// there are none -- an execution with no reservation (quota not
	// applicable, or already released) has nothing to release, which is a
	// normal outcome, not a fault.
	ReleaseReservationsForExecution(ctx context.Context, executionID int64) error

	// WithTenantLock runs fn while holding an exclusive lock scoped to
	// tenantID+cluster, serializing concurrent admission decisions for the
	// same tenant+cluster -- e.g. a manual Trigger racing a scheduled fire,
	// or two cmd/api or cmd/scheduler replicas admitting at once. Without
	// this, two concurrent callers could each read the same "used capacity"
	// before either commits its own reservation, jointly admitting more than
	// the ceiling allows. A call for a different tenant+cluster must not
	// block on this one. fn's error (if any) is returned unchanged; the lock
	// is always released, even when fn errors.
	WithTenantLock(ctx context.Context, tenantID int64, cluster string, fn func(ctx context.Context) error) error
}
