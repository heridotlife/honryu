package quotaapp_test

import (
	"context"
	"errors"
	"fmt"
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

// The typed rejection carries the admission numbers (phase 24's details
// envelope), and its message text is pinned byte-for-byte so the envelope
// refactor can never drift what operators already match on.
func TestReserve_OverQuotaErrorCarriesNumbers(t *testing.T) {
	t.Parallel()
	svc, store := newQuotaService(t)
	ctx := context.Background()
	if err := store.SetCeiling(ctx, 1, "default", 10); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}
	if _, err := svc.Reserve(ctx, 1, "default", 8, at(0), at(60), 100); err != nil {
		t.Fatalf("Reserve (first): %v", err)
	}

	_, err := svc.Reserve(ctx, 1, "default", 5, at(0), at(60), 101)
	var oqe *quotaapp.OverQuotaError
	if !errors.As(err, &oqe) {
		t.Fatalf("Reserve (over quota) = %T (%v), want *OverQuotaError", err, err)
	}
	if oqe.TenantID != 1 || oqe.Cluster != "default" || oqe.Requested != 5 || oqe.Used != 8 || oqe.Ceiling != 10 {
		t.Fatalf("OverQuotaError fields = %+v, want the admission numbers", *oqe)
	}
	if oqe.NoQuotaConfigured {
		t.Fatalf("exhausted ceiling must not claim quota is unconfigured: %+v", *oqe)
	}
	const want = `quotaapp: reservation would exceed quota: tenant 1 cluster "default" wants 5 engines, 8 already reserved in this window, ceiling 10`
	if err.Error() != want {
		t.Fatalf("error text = %q, want %q", err.Error(), want)
	}
}

// The zero-ceiling branch is the one with a remediation sentence in the
// message; its typed form must keep that text and flag itself so the HTTP
// layer's hint can say "no quota row exists" rather than "lower your
// ask".
func TestReserve_ZeroCeilingErrorIsFlaggedUnconfigured(t *testing.T) {
	t.Parallel()
	svc, _ := newQuotaService(t)

	_, err := svc.Reserve(context.Background(), 7, "prod", 1, at(0), at(60), 100)
	var oqe *quotaapp.OverQuotaError
	if !errors.As(err, &oqe) {
		t.Fatalf("Reserve (unconfigured ceiling) = %T (%v), want *OverQuotaError", err, err)
	}
	if oqe.TenantID != 7 || oqe.Cluster != "prod" || oqe.Requested != 1 || oqe.Used != 0 || oqe.Ceiling != 0 || !oqe.NoQuotaConfigured {
		t.Fatalf("OverQuotaError fields = %+v, want zero-ceiling numbers + NoQuotaConfigured", *oqe)
	}
	const want = `quotaapp: reservation would exceed quota: tenant 7 cluster "prod" wants 1 engines, 0 already reserved in this window, ceiling 0 — no quota configured for this tenant+cluster; set one via PUT /api/tenants/{tenant_id}/quota`
	if err.Error() != want {
		t.Fatalf("error text = %q, want %q", err.Error(), want)
	}
}

// Callers re-wrap reservation failures (Trigger's own %w chains), so both
// the sentinel (errors.Is) and the typed form (errors.As) must survive an
// intervening wrap.
func TestOverQuotaError_SentinelAndTypeSurviveWrapping(t *testing.T) {
	t.Parallel()
	svc, _ := newQuotaService(t)
	_, err := svc.Reserve(context.Background(), 1, "default", 1, at(0), at(60), 100)
	if err == nil {
		t.Fatal("Reserve (unconfigured) = nil, want an error")
	}
	wrapped := fmt.Errorf("trigger: %w", err)
	if !errors.Is(wrapped, quotaapp.ErrOverQuota) {
		t.Fatal("errors.Is(wrapped, ErrOverQuota) = false, want true")
	}
	var oqe *quotaapp.OverQuotaError
	if !errors.As(wrapped, &oqe) {
		t.Fatal("errors.As(wrapped, &OverQuotaError) = false, want true")
	}
	if oqe.Ceiling != 0 || !oqe.NoQuotaConfigured {
		t.Fatalf("wrapped fields = %+v, want the zero-ceiling branch", *oqe)
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

// The aggregate a capacity meter renders: ceilings sum across every tenant
// configured on the cluster, and used sums each tenant's live-window
// commitments. Seeded to mirror the homelab deployment's shape (tenants 1
// and 4 on cluster "honryu").
func TestClusterCapacity_SumsCeilingsAndUsedAcrossTenants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	now := at(1_000_000)
	svc := quotaapp.NewService(store).WithNow(func() time.Time { return now })

	if err := store.SetCeiling(ctx, 1, "honryu", 4); err != nil {
		t.Fatalf("SetCeiling(1): %v", err)
	}
	if err := store.SetCeiling(ctx, 4, "honryu", 8); err != nil {
		t.Fatalf("SetCeiling(4): %v", err)
	}
	// Tenant 1: 3 engines spanning now. Tenant 4: 5 engines spanning now.
	for _, r := range []reservation.Reservation{
		{TenantID: 1, Cluster: "honryu", EngineCount: 3, Start: now.Add(-time.Hour), End: now.Add(time.Hour), ExecutionID: 100},
		{TenantID: 4, Cluster: "honryu", EngineCount: 5, Start: now.Add(-time.Minute), End: now.Add(2 * time.Hour), ExecutionID: 400},
	} {
		if _, err := store.CreateReservation(ctx, r); err != nil {
			t.Fatalf("CreateReservation(tenant %d): %v", r.TenantID, err)
		}
	}
	// A reservation on another cluster must not leak into this one's sum.
	if _, err := store.CreateReservation(ctx, reservation.Reservation{
		TenantID: 1, Cluster: "elsewhere", EngineCount: 7, Start: now.Add(-time.Hour), End: now.Add(time.Hour), ExecutionID: 101,
	}); err != nil {
		t.Fatalf("CreateReservation(other cluster): %v", err)
	}

	used, ceiling, err := svc.ClusterCapacity(ctx, "honryu")
	if err != nil {
		t.Fatalf("ClusterCapacity: %v", err)
	}
	if ceiling != 12 {
		t.Fatalf("ceiling = %d, want 12 (4 + 8 across tenants)", ceiling)
	}
	if used != 8 {
		t.Fatalf("used = %d, want 8 (3 + 5 across tenants, other cluster excluded)", used)
	}
}

// Overrun counts here exactly as it counts in Reserve: a reservation whose
// declared end has passed but whose execution still runs is still occupying
// real capacity, so the aggregate must carry it -- a meter that hid overrun
// would under-report precisely the cluster state an operator most needs to
// see.
func TestClusterCapacity_CountsOverrunningReservations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	now := at(1_000_000)
	svc := quotaapp.NewService(store).WithNow(func() time.Time { return now })

	if err := store.SetCeiling(ctx, 1, "honryu", 4); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}
	if _, err := store.CreateReservation(ctx, reservation.Reservation{
		TenantID: 1, Cluster: "honryu", EngineCount: 3,
		Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour), ExecutionID: 100,
	}); err != nil {
		t.Fatalf("CreateReservation (overrun): %v", err)
	}
	if _, err := store.StartRun(ctx, 100, ""); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	used, ceiling, err := svc.ClusterCapacity(ctx, "honryu")
	if err != nil {
		t.Fatalf("ClusterCapacity: %v", err)
	}
	if used != 3 || ceiling != 4 {
		t.Fatalf("ClusterCapacity = %d/%d, want 3/4 -- the overrunning reservation still occupies its engines", used, ceiling)
	}
}

// The "at now" scoping: a future booking is not capacity in use today, and a
// stale reservation whose execution is not running is not either -- the
// aggregate reports what is committed at this instant, not a running total
// across all time.
func TestClusterCapacity_IgnoresFutureBookingsAndStaleReservations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewStore()
	now := at(1_000_000)
	svc := quotaapp.NewService(store).WithNow(func() time.Time { return now })

	if err := store.SetCeiling(ctx, 1, "honryu", 6); err != nil {
		t.Fatalf("SetCeiling: %v", err)
	}
	for _, r := range []reservation.Reservation{
		{TenantID: 1, Cluster: "honryu", EngineCount: 5, Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), ExecutionID: 200},   // future
		{TenantID: 1, Cluster: "honryu", EngineCount: 4, Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour), ExecutionID: 201}, // stale, never ran
	} {
		if _, err := store.CreateReservation(ctx, r); err != nil {
			t.Fatalf("CreateReservation(execution %d): %v", r.ExecutionID, err)
		}
	}

	used, ceiling, err := svc.ClusterCapacity(ctx, "honryu")
	if err != nil {
		t.Fatalf("ClusterCapacity: %v", err)
	}
	if used != 0 || ceiling != 6 {
		t.Fatalf("ClusterCapacity = %d/%d, want 0/6 -- nothing is committed at now", used, ceiling)
	}
}

// A cluster nobody configured quota on is the normal unconfigured state:
// 0/0 with no error, so the caller (and the meter) treats it as "no
// capacity reported" rather than as a failure.
func TestClusterCapacity_UnconfiguredClusterIsZeroZeroNil(t *testing.T) {
	t.Parallel()
	svc, _ := newQuotaService(t)

	used, ceiling, err := svc.ClusterCapacity(context.Background(), "ghost")
	if err != nil {
		t.Fatalf("ClusterCapacity(unconfigured) = %v, want nil", err)
	}
	if used != 0 || ceiling != 0 {
		t.Fatalf("ClusterCapacity(unconfigured) = %d/%d, want 0/0", used, ceiling)
	}
}
