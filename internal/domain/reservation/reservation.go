// Package reservation models time-bounded engine-capacity commitments against
// a tenant's quota. Pure domain: no I/O, no persistence.
//
// A reservation is what makes quota a guarantee rather than a best-effort
// check: creating one for a future scheduled run holds that capacity for it,
// rather than only checking what happens to be free at the moment it fires.
package reservation

import (
	"errors"
	"time"
)

// Validation errors. Callers compare with errors.Is.
var (
	ErrEngineCountInvalid = errors.New("reservation: engine count must be positive")
	ErrWindowInvalid      = errors.New("reservation: end must be after start")
)

// DefaultCeiling is the engine-unit quota ceiling a tenant+cluster reads
// when no quota row exists -- the platform's out-of-the-box default, so a
// fresh install or a new tenant can trigger without an admin first PUT-ing
// a quota. It lives here, in the domain rather than config, because the
// platform ships with a defined default; deployments override it per
// tenant by writing a quota row (any value, including an explicit 0 to
// block), which then wins over this constant.
const DefaultCeiling = 10

// Reservation is a tenant's claim on engine capacity for a bounded window,
// owned by the execution (manual trigger or a schedule's occurrence) that
// made it.
//
// Cluster is a plain string, not ports.ClusterRef: domain packages do not
// import ports -- the dependency runs the other way -- and the two are
// interchangeable representations of the same identifier.
type Reservation struct {
	ID          int64
	TenantID    int64
	Cluster     string
	EngineCount int
	Start       time.Time
	End         time.Time
	ExecutionID int64
}

// Validate checks a reservation's own invariants, independent of anything
// else already reserved.
func (r Reservation) Validate() error {
	switch {
	case r.EngineCount <= 0:
		return ErrEngineCountInvalid
	case !r.End.After(r.Start):
		return ErrWindowInvalid
	default:
		return nil
	}
}

// Overlaps reports whether r and other's windows share any instant.
//
// Half-open intervals: a reservation ending exactly when another starts does
// not overlap it. The outgoing run's capacity is free from that instant, and
// the incoming one may claim it starting there -- treating the shared instant
// as a collision would make two reservations that exactly abut impossible to
// create back to back, for no capacity reason.
func (r Reservation) Overlaps(other Reservation) bool {
	return r.Start.Before(other.End) && other.Start.Before(r.End)
}
