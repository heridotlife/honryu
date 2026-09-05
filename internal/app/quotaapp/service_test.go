package quotaapp_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/quotaapp"
	"github.com/heridotlife/honryu/internal/domain/reservation"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

func at(seconds int) time.Time { return time.Unix(int64(seconds), 0).UTC() }

func newQuotaService(t *testing.T) (*quotaapp.Service, *fake.Store) {
	t.Helper()
	store := fake.NewStore()
	return quotaapp.NewService(store), store
}

func TestReserve_AdmitsWhenUnderCeiling(t *testing.T) {
	t.Parallel()
	svc, store := newQuotaService(t)
	ctx := context.Background()
	if err := store.SetCeiling(ctx, 1, "default", 10); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}

	r, err := svc.Reserve(ctx, 1, "default", 5, at(0), at(60), 100)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if r.ID == 0 || r.EngineCount != 5 {
		t.Fatalf("Reserve = %+v, want a persisted reservation for 5 engines", r)
	}
}

// The boundary itself: using exactly the remaining headroom must be
// admitted, not rejected -- "exceed" means strictly over, not at, the
// ceiling.
func TestReserve_AdmitsExactlyAtCeiling(t *testing.T) {
	t.Parallel()
	svc, store := newQuotaService(t)
	ctx := context.Background()
	if err := store.SetCeiling(ctx, 1, "default", 10); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}
	if _, err := svc.Reserve(ctx, 1, "default", 5, at(0), at(60), 100); err != nil {
		t.Fatalf("Reserve (first): %v", err)
	}

	if _, err := svc.Reserve(ctx, 1, "default", 5, at(0), at(60), 101); err != nil {
		t.Fatalf("Reserve (second, exactly at ceiling) = %v, want nil", err)
	}
}

func TestReserve_RejectsWhenOverCeiling(t *testing.T) {
	t.Parallel()
	svc, store := newQuotaService(t)
	ctx := context.Background()
	if err := store.SetCeiling(ctx, 1, "default", 10); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}
	if _, err := svc.Reserve(ctx, 1, "default", 8, at(0), at(60), 100); err != nil {
		t.Fatalf("Reserve (first): %v", err)
	}

	if _, err := svc.Reserve(ctx, 1, "default", 5, at(0), at(60), 101); !errors.Is(err, quotaapp.ErrOverQuota) {
		t.Fatalf("Reserve (second, 8+5=13 > 10) = %v, want ErrOverQuota", err)
	} else if strings.Contains(err.Error(), "no quota configured") {
		t.Fatalf("Reserve (exhausted ceiling) error = %q, must not claim quota is unconfigured", err)
	}
}

// An unconfigured ceiling reads as 0, so any positive reservation is
// rejected -- unconfigured means nothing runs, not unlimited. The error
// must carry the remediation (set a ceiling via the quota endpoint), since
// "ceiling 0" alone reads like a configured limit of zero.
func TestReserve_ZeroCeilingRejectsEverything(t *testing.T) {
	t.Parallel()
	svc, _ := newQuotaService(t)
	if _, err := svc.Reserve(context.Background(), 1, "default", 1, at(0), at(60), 100); !errors.Is(err, quotaapp.ErrOverQuota) {
		t.Fatalf("Reserve(unconfigured ceiling) = %v, want ErrOverQuota", err)
	} else if !strings.Contains(err.Error(), "no quota configured for this tenant+cluster; set one via PUT /api/tenants/{tenant_id}/quota") {
		t.Fatalf("Reserve(unconfigured ceiling) error = %q, want remediation naming the quota endpoint", err)
	}
}

// A reservation elsewhere in time must not count against a window it does
// not overlap -- the whole point of InWindow scoping the sum, not a running
// total across all time.
func TestReserve_IgnoresReservationsOutsideTheWindow(t *testing.T) {
	t.Parallel()
	svc, store := newQuotaService(t)
	ctx := context.Background()
	if err := store.SetCeiling(ctx, 1, "default", 10); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}
	if _, err := svc.Reserve(ctx, 1, "default", 9, at(1000), at(1060), 100); err != nil {
		t.Fatalf("Reserve (elsewhere in time): %v", err)
	}

	if _, err := svc.Reserve(ctx, 1, "default", 9, at(0), at(60), 101); err != nil {
		t.Fatalf("Reserve (non-overlapping window) = %v, want nil", err)
	}
}

func TestReserve_RejectsInvalidWindow(t *testing.T) {
	t.Parallel()
	svc, store := newQuotaService(t)
	ctx := context.Background()
	if err := store.SetCeiling(ctx, 1, "default", 10); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}

	if _, err := svc.Reserve(ctx, 1, "default", 1, at(60), at(0), 100); !errors.Is(err, reservation.ErrWindowInvalid) {
		t.Fatalf("Reserve(end before start) = %v, want ErrWindowInvalid", err)
	}
}

// fakeStopper mimics what lifecycleapp.Service.Stop does to a quota
// reservation via its teardown -- clears the run and releases the
// reservation -- without pulling in the full lifecycleapp package, and
// records which executions it was asked to stop.
type fakeStopper struct {
	store   *fake.Store
	stopped []int64
}

func (f *fakeStopper) Stop(ctx context.Context, executionID int64) error {
	f.stopped = append(f.stopped, executionID)
	_ = f.store.StopRun(ctx, executionID)
	return f.store.ReleaseReservationsForExecution(ctx, executionID)
}

// A reservation whose declared end has passed but whose execution is still
// running is exactly the "overrun" case: it still occupies its capacity, so
// a new request that cannot fit alongside it triggers reclaim.
func TestReserve_ReclaimsOverrunReservationWhenCapacityIsNeeded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	now := at(1_000_000)
	stopper := &fakeStopper{store: store}
	svc := quotaapp.NewService(store).WithNow(func() time.Time { return now }).WithStopper(stopper)

	if err := store.SetCeiling(ctx, 1, "default", 5); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}
	if _, err := store.CreateReservation(ctx, reservation.Reservation{
		TenantID: 1, Cluster: "default", EngineCount: 5, Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour), ExecutionID: 100,
	}); err != nil {
		t.Fatalf("CreateReservation (overrun): %v", err)
	}
	if _, err := store.StartRun(ctx, 100, ""); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	r, err := svc.Reserve(ctx, 1, "default", 5, now, now.Add(time.Hour), 200)
	if err != nil {
		t.Fatalf("Reserve = %v, want the overrunning reservation reclaimed and this one admitted", err)
	}
	if r.ExecutionID != 200 {
		t.Fatalf("Reserve = %+v, want a reservation for execution 200", r)
	}
	if len(stopper.stopped) != 1 || stopper.stopped[0] != 100 {
		t.Fatalf("stopped = %v, want [100]", stopper.stopped)
	}

	got, err := store.ReservationsForTenant(ctx, 1, "default")
	if err != nil {
		t.Fatalf("ReservationsForTenant: %v", err)
	}
	if len(got) != 1 || got[0].ExecutionID != 200 {
		t.Fatalf("ReservationsForTenant = %+v, want only the new reservation -- the overrun one was freed", got)
	}
}

// The exact non-goal the spec calls out: an overrunning run whose capacity
// nobody needs keeps running untouched.
func TestReserve_LeavesOverrunReservationAloneWhenCapacityAllows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	now := at(1_000_000)
	stopper := &fakeStopper{store: store}
	svc := quotaapp.NewService(store).WithNow(func() time.Time { return now }).WithStopper(stopper)

	if err := store.SetCeiling(ctx, 1, "default", 10); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}
	if _, err := store.CreateReservation(ctx, reservation.Reservation{
		TenantID: 1, Cluster: "default", EngineCount: 5, Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour), ExecutionID: 100,
	}); err != nil {
		t.Fatalf("CreateReservation (overrun): %v", err)
	}
	if _, err := store.StartRun(ctx, 100, ""); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if _, err := svc.Reserve(ctx, 1, "default", 3, now, now.Add(time.Hour), 200); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if len(stopper.stopped) != 0 {
		t.Fatalf("stopped = %v, want none -- there was already enough headroom without reclaiming", stopper.stopped)
	}
	if _, running, err := store.CurrentRun(ctx, 100); err != nil || !running {
		t.Fatalf("execution 100 running = %v, %v, want still running (untouched)", running, err)
	}
}

// Reclaim only ever applies to an admission happening now: a request for a
// future window (a schedule's advance reservation) must not force-stop
// today's overrunning execution to make room for a booking next week --
// that capacity will naturally be re-evaluated when its own time comes.
func TestReserve_DoesNotReclaimForAFutureBooking(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	now := at(1_000_000)
	stopper := &fakeStopper{store: store}
	svc := quotaapp.NewService(store).WithNow(func() time.Time { return now }).WithStopper(stopper)

	if err := store.SetCeiling(ctx, 1, "default", 5); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}
	if _, err := store.CreateReservation(ctx, reservation.Reservation{
		TenantID: 1, Cluster: "default", EngineCount: 5, Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour), ExecutionID: 100,
	}); err != nil {
		t.Fatalf("CreateReservation (overrun): %v", err)
	}
	if _, err := store.StartRun(ctx, 100, ""); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	future := now.AddDate(0, 0, 7)
	if _, err := svc.Reserve(ctx, 1, "default", 5, future, future.Add(time.Hour), 200); !errors.Is(err, quotaapp.ErrOverQuota) {
		t.Fatalf("Reserve (future booking) = %v, want ErrOverQuota -- reclaim must not apply", err)
	}
	if len(stopper.stopped) != 0 {
		t.Fatalf("stopped = %v, want none -- a future booking must never trigger reclaim", stopper.stopped)
	}
}

// A reservation whose declared end has passed but whose execution is not
// (or no longer) running has nothing left to reclaim and must not count
// against the ceiling at all -- it is simply stale, not overrunning.
func TestReserve_IgnoresOverrunReservationsWhoseExecutionIsNotRunning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	now := at(1_000_000)
	svc := quotaapp.NewService(store).WithNow(func() time.Time { return now })

	if err := store.SetCeiling(ctx, 1, "default", 5); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}
	if _, err := store.CreateReservation(ctx, reservation.Reservation{
		TenantID: 1, Cluster: "default", EngineCount: 5, Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour), ExecutionID: 100,
	}); err != nil {
		t.Fatalf("CreateReservation (stale, never started): %v", err)
	}

	if _, err := svc.Reserve(ctx, 1, "default", 5, now, now.Add(time.Hour), 200); err != nil {
		t.Fatalf("Reserve = %v, want nil -- a non-running stale reservation must not count as used capacity", err)
	}
}

// Without a stopper wired, reclaim has nothing to reclaim with -- Reserve
// falls through to its ordinary rejection rather than surfacing a confusing
// internal error.
func TestReserve_NoStopperWiredFallsThroughToOrdinaryRejection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	now := at(1_000_000)
	svc := quotaapp.NewService(store).WithNow(func() time.Time { return now }) // no WithStopper

	if err := store.SetCeiling(ctx, 1, "default", 5); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}
	if _, err := store.CreateReservation(ctx, reservation.Reservation{
		TenantID: 1, Cluster: "default", EngineCount: 5, Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour), ExecutionID: 100,
	}); err != nil {
		t.Fatalf("CreateReservation (overrun): %v", err)
	}
	if _, err := store.StartRun(ctx, 100, ""); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if _, err := svc.Reserve(ctx, 1, "default", 5, now, now.Add(time.Hour), 200); !errors.Is(err, quotaapp.ErrOverQuota) {
		t.Fatalf("Reserve (no stopper wired) = %v, want ErrOverQuota", err)
	}
}

// Reclaim frees overrunning reservations earliest-declared-end first, and
// stops as soon as enough capacity has been freed -- an overrunning run
// whose capacity nobody needs (here, the second one) is left running
// untouched even mid-reclaim.
func TestReserve_ReclaimStopsAsSoonAsEnoughCapacityIsFreed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	now := at(1_000_000)
	stopper := &fakeStopper{store: store}
	svc := quotaapp.NewService(store).WithNow(func() time.Time { return now }).WithStopper(stopper)

	if err := store.SetCeiling(ctx, 1, "default", 10); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}
	// Earliest declared end, execution 100.
	if _, err := store.CreateReservation(ctx, reservation.Reservation{
		TenantID: 1, Cluster: "default", EngineCount: 5, Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour), ExecutionID: 100,
	}); err != nil {
		t.Fatalf("CreateReservation (100): %v", err)
	}
	// Later declared end, execution 101 -- reclaiming 100 alone is enough.
	if _, err := store.CreateReservation(ctx, reservation.Reservation{
		TenantID: 1, Cluster: "default", EngineCount: 5, Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour), ExecutionID: 101,
	}); err != nil {
		t.Fatalf("CreateReservation (101): %v", err)
	}
	for _, id := range []int64{100, 101} {
		if _, err := store.StartRun(ctx, id, ""); err != nil {
			t.Fatalf("StartRun(%d): %v", id, err)
		}
	}

	if _, err := svc.Reserve(ctx, 1, "default", 3, now, now.Add(time.Hour), 200); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if len(stopper.stopped) != 1 || stopper.stopped[0] != 100 {
		t.Fatalf("stopped = %v, want [100] only -- reclaiming it alone already freed enough", stopper.stopped)
	}
	if _, running, err := store.CurrentRun(ctx, 101); err != nil || !running {
		t.Fatalf("execution 101 running = %v, %v, want still running (untouched)", running, err)
	}
}

// The concrete guarantee Reserve exists to provide: many concurrent
// admission decisions for the same tenant+cluster (a manual Trigger racing a
// scheduled fire, or several cmd/api or cmd/scheduler replicas at once) must
// never jointly admit more than the ceiling, even though each call on its
// own only asks for a fraction of it. Without WithTenantLock serializing the
// read-then-write admission decision, every one of these could read the
// ceiling as unclaimed before any of them commits a reservation.
func TestReserve_ConcurrentCallsNeverJointlyExceedTheCeiling(t *testing.T) {
	t.Parallel()
	svc, store := newQuotaService(t)
	ctx := context.Background()
	const ceiling = 5
	const callers = 20
	if err := store.SetCeiling(ctx, 1, "default", ceiling); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	rejected := 0
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(executionID int64) {
			defer wg.Done()
			_, err := svc.Reserve(ctx, 1, "default", 1, at(0), at(3600), executionID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				admitted++
			case errors.Is(err, quotaapp.ErrOverQuota):
				rejected++
			default:
				t.Errorf("Reserve(executionID=%d) = %v, want nil or ErrOverQuota", executionID, err)
			}
		}(int64(100 + i))
	}
	wg.Wait()

	if admitted != ceiling {
		t.Fatalf("admitted = %d, want exactly %d (the ceiling) -- concurrent callers jointly over-admitted", admitted, ceiling)
	}
	if admitted+rejected != callers {
		t.Fatalf("admitted+rejected = %d, want %d (every caller accounted for)", admitted+rejected, callers)
	}

	got, err := store.ReservationsInWindow(ctx, 1, "default", at(0), at(3600))
	if err != nil {
		t.Fatalf("ReservationsInWindow: %v", err)
	}
	sum := 0
	for _, r := range got {
		sum += r.EngineCount
	}
	if sum != ceiling {
		t.Fatalf("total reserved engines = %d, want exactly %d (the ceiling), got reservations = %+v", sum, ceiling, got)
	}
}
