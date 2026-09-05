// Package quotaapp is the shared quota-check/reserve use-case: the one place
// that decides whether a new reservation fits a tenant's per-cluster engine
// ceiling. Both a manual Trigger (internal/app/lifecycleapp) and a scheduled
// occurrence's firing (internal/app/scheduleapp) call Reserve rather than
// each deciding admission its own way -- the guarantee a reservation is
// supposed to be would mean nothing if the two paths could disagree.
package quotaapp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/heridotlife/honryu/internal/domain/reservation"
	"github.com/heridotlife/honryu/internal/ports"
)

// ErrOverQuota is returned when a new reservation would exceed the tenant's
// ceiling for its cluster.
var ErrOverQuota = errors.New("quotaapp: reservation would exceed quota")

// Repo is the persistence quotaapp needs: the reservation ledger, plus
// enough run state to tell whether a reservation's execution is still
// actually running -- a still-running execution keeps occupying its
// reservation's capacity for as long as it runs, not just until the
// declared end (spec: "overrun tolerated if capacity allows").
type Repo interface {
	ports.ReservationRepository
	// CurrentRun reports whether executionID has an active run.
	CurrentRun(ctx context.Context, executionID int64) (runID int64, running bool, err error)
}

// Stopper tears an execution's run down, freeing its reservation as a side
// effect (lifecycleapp.Service.Stop, via its teardown, calls back into this
// package's Release). A no-op default is used when none is wired, in which
// case overrun capacity is never reclaimed -- Reserve simply rejects as it
// always has.
type Stopper interface {
	Stop(ctx context.Context, executionID int64) error
}

// errNoStopper distinguishes "nothing wired to reclaim with" (fall through to
// an ordinary rejection) from a real failure while reclaiming (surface it).
var errNoStopper = errors.New("quotaapp: no stopper wired")

type noopStopper struct{}

func (noopStopper) Stop(context.Context, int64) error { return errNoStopper }

// Service is the shared quota-check/reserve use-case.
type Service struct {
	repo    Repo
	stopper Stopper
	// now is overrun-reclaim's clock: reclaim only ever runs for an admission
	// starting now-or-earlier (see Reserve) -- overridable for deterministic
	// tests.
	now func() time.Time
}

// NewService wires the quota service against its reservation ledger.
func NewService(repo Repo) *Service {
	return &Service{repo: repo, stopper: noopStopper{}, now: time.Now}
}

// WithStopper attaches the hook overrun-reclaim uses to force-stop a
// blocking, still-running execution. Returns the receiver for chaining.
func (s *Service) WithStopper(stopper Stopper) *Service {
	if stopper != nil {
		s.stopper = stopper
	}
	return s
}

// WithNow overrides the clock overrun-reclaim measures "is this admission
// happening now" against. Returns the receiver for chaining.
func (s *Service) WithNow(now func() time.Time) *Service {
	if now != nil {
		s.now = now
	}
	return s
}

// Reserve admits a new reservation for tenantID+cluster if it fits the
// ceiling, creating and returning it. Admission sums every existing
// reservation whose window overlaps [start, end) -- not a running total --
// so the check is exact for the specific window being claimed, not an
// approximation across all time. It also counts any of the tenant's own
// reservations whose declared end has already passed but whose execution is
// still actually running: Stop was never called, so it is still occupying
// real capacity regardless of what its reservation's End says. If that
// capacity is what stands between this request and admission, and the
// request itself starts now (not a future booking), the overrunning
// execution is force-stopped to free it before rejecting outright -- an
// overrunning run whose capacity nobody needs is left running untouched.
//
// The whole read-then-write admission decision runs under
// Repo.WithTenantLock, scoped to tenantID+cluster: without it, two
// concurrent callers (a manual Trigger racing a scheduled fire, or two
// cmd/api or cmd/scheduler replicas) could each read the same used capacity
// before either commits its own reservation, jointly admitting more than the
// ceiling allows -- exactly the guarantee this ledger exists to provide.
func (s *Service) Reserve(ctx context.Context, tenantID int64, cluster string, engineCount int, start, end time.Time, executionID int64) (reservation.Reservation, error) {
	r := reservation.Reservation{
		TenantID: tenantID, Cluster: cluster, EngineCount: engineCount,
		Start: start, End: end, ExecutionID: executionID,
	}
	if err := r.Validate(); err != nil {
		return reservation.Reservation{}, err
	}

	err := s.repo.WithTenantLock(ctx, tenantID, cluster, func(ctx context.Context) error {
		ceiling, err := s.repo.GetCeiling(ctx, tenantID, cluster)
		if err != nil {
			return err
		}
		used, overrun, err := s.usedCapacity(ctx, tenantID, cluster, start, end)
		if err != nil {
			return err
		}

		if used+engineCount > ceiling && !start.After(s.now()) {
			freed, reclaimErr := s.reclaimOverrun(ctx, overrun, used+engineCount-ceiling)
			if reclaimErr != nil {
				return reclaimErr
			}
			used -= freed
		}
		if used+engineCount > ceiling {
			// A ceiling of 0 means no quota was ever configured for this
			// tenant+cluster (absent reads as 0 -- migrations/0028), not that
			// one was configured and exhausted: say so, so an operator knows
			// the remediation is to set a ceiling, not to free capacity.
			if ceiling == 0 {
				return fmt.Errorf(
					"%w: tenant %d cluster %q wants %d engines, %d already reserved in this window, ceiling %d — no quota configured for this tenant+cluster; set one via PUT /api/tenants/{tenant_id}/quota",
					ErrOverQuota, tenantID, cluster, engineCount, used, ceiling)
			}
			return fmt.Errorf(
				"%w: tenant %d cluster %q wants %d engines, %d already reserved in this window, ceiling %d",
				ErrOverQuota, tenantID, cluster, engineCount, used, ceiling)
		}

		id, err := s.repo.CreateReservation(ctx, r)
		if err != nil {
			return err
		}
		r.ID = id
		return nil
	})
	if err != nil {
		return reservation.Reservation{}, err
	}
	return r, nil
}

// usedCapacity is the engine count already committed against tenant+cluster
// for [start, end): every reservation whose declared window overlaps it,
// plus every overrunning reservation (declared end passed, execution still
// running) regardless of whether its now-stale declared window happens to
// overlap -- it is still occupying real capacity for as long as it runs.
// The two sets are disjoint by construction: a window-overlapping
// reservation's end is always after start, so it can never also qualify as
// overrun relative to a start at or before now.
func (s *Service) usedCapacity(ctx context.Context, tenantID int64, cluster string, start, end time.Time) (used int, overrun []reservation.Reservation, err error) {
	existing, err := s.repo.ReservationsInWindow(ctx, tenantID, cluster, start, end)
	if err != nil {
		return 0, nil, err
	}
	for _, e := range existing {
		used += e.EngineCount
	}

	overrun, err = s.overrunReservations(ctx, tenantID, cluster)
	if err != nil {
		return 0, nil, err
	}
	for _, o := range overrun {
		used += o.EngineCount
	}
	return used, overrun, nil
}

// overrunReservations returns the tenant's reservations, for cluster, whose
// declared end has already passed (relative to this service's own clock) but
// whose execution is still marked running.
func (s *Service) overrunReservations(ctx context.Context, tenantID int64, cluster string) ([]reservation.Reservation, error) {
	all, err := s.repo.ReservationsForTenant(ctx, tenantID, cluster)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make([]reservation.Reservation, 0, len(all))
	for _, r := range all {
		if !r.End.Before(now) {
			continue
		}
		_, running, runErr := s.repo.CurrentRun(ctx, r.ExecutionID)
		if runErr != nil {
			return nil, runErr
		}
		if running {
			out = append(out, r)
		}
	}
	return out, nil
}

// reclaimOverrun force-stops overrun reservations' executions, earliest
// declared end first, until at least needed engines' worth of capacity has
// been freed or there are none left to try. Returns how much was actually
// freed (which may be less than needed). A nil Stopper (the default) means
// nothing can be reclaimed: that is not itself an error, it just leaves
// freed at 0 so Reserve falls through to its ordinary rejection.
func (s *Service) reclaimOverrun(ctx context.Context, overrun []reservation.Reservation, needed int) (int, error) {
	sorted := make([]reservation.Reservation, len(overrun))
	copy(sorted, overrun)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].End.Before(sorted[j].End) })

	freed := 0
	for _, r := range sorted {
		if freed >= needed {
			break
		}
		if err := s.stopper.Stop(ctx, r.ExecutionID); err != nil {
			if errors.Is(err, errNoStopper) {
				break
			}
			return freed, err
		}
		freed += r.EngineCount
	}
	return freed, nil
}

// Release frees an execution's reservation immediately, rather than waiting
// for its declared end -- called on Stop/teardown. Not an error when the
// execution never had one (quota did not apply, or it was already released).
func (s *Service) Release(ctx context.Context, executionID int64) error {
	return s.repo.ReleaseReservationsForExecution(ctx, executionID)
}
