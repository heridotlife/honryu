package repositorytest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/reservation"
	"github.com/heridotlife/honryu/internal/ports"
)

// NewReservationRepo builds a fresh, empty ReservationRepository for one test.
type NewReservationRepo func(t *testing.T) ports.ReservationRepository

func at(seconds int) time.Time { return time.Unix(int64(seconds), 0).UTC() }

// RunReservationRepositoryContract pins the behaviour every
// ReservationRepository must share.
func RunReservationRepositoryContract(t *testing.T, newRepo NewReservationRepo) {
	t.Helper()

	t.Run("CreateAndDelete", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		id, err := repo.CreateReservation(ctx, reservation.Reservation{
			TenantID: 1, Cluster: "default", EngineCount: 3,
			Start: at(0), End: at(60), ExecutionID: 100,
		})
		if err != nil {
			t.Fatalf("CreateReservation: %v", err)
		}
		if id <= 0 {
			t.Fatalf("CreateReservation id = %d, want > 0", id)
		}

		got, err := repo.ReservationsInWindow(ctx, 1, "default", at(0), at(60))
		if err != nil {
			t.Fatalf("ReservationsInWindow: %v", err)
		}
		if len(got) != 1 || got[0].EngineCount != 3 || got[0].ExecutionID != 100 {
			t.Fatalf("ReservationsInWindow = %+v, want the one reservation just created", got)
		}

		if err := repo.DeleteReservation(ctx, id); err != nil {
			t.Fatalf("DeleteReservation: %v", err)
		}
		got, err = repo.ReservationsInWindow(ctx, 1, "default", at(0), at(60))
		if err != nil {
			t.Fatalf("ReservationsInWindow after delete: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("ReservationsInWindow after delete = %+v, want none -- deleting frees capacity immediately", got)
		}

		if err := repo.DeleteReservation(ctx, id); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("DeleteReservation (already gone) = %v, want ErrNotFound", err)
		}
	})

	// Unlike DeleteReservation (by id), releasing an execution with no
	// reservation at all is not an error -- Stop/teardown calls this
	// unconditionally, and most executions never had one to begin with.
	t.Run("ReleaseForExecutionIsIdempotentAndScoped", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		if err := repo.ReleaseReservationsForExecution(ctx, 999); err != nil {
			t.Fatalf("ReleaseReservationsForExecution(nothing to release) = %v, want nil", err)
		}

		mustReserve(t, repo, 1, "default", at(0), at(60), 100)
		mustReserve(t, repo, 1, "default", at(0), at(60), 101) // different execution, same window

		if err := repo.ReleaseReservationsForExecution(ctx, 100); err != nil {
			t.Fatalf("ReleaseReservationsForExecution: %v", err)
		}
		got, err := repo.ReservationsInWindow(ctx, 1, "default", at(0), at(60))
		if err != nil {
			t.Fatalf("ReservationsInWindow: %v", err)
		}
		if len(got) != 1 || got[0].ExecutionID != 101 {
			t.Fatalf("ReservationsInWindow after release = %+v, want only execution 101's reservation left", got)
		}
	})

	// The exact case a naive query would get wrong: a reservation whose
	// window straddles the query window's boundary must still be found, and
	// one that merely touches it (ends exactly where the query starts, or
	// starts exactly where it ends) must not.
	t.Run("InWindowMatchesOnlyTrueOverlaps", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		straddlesStart := mustReserve(t, repo, 1, "default", at(0), at(20), 1) // ends inside [10,30)
		straddlesEnd := mustReserve(t, repo, 1, "default", at(20), at(40), 2)  // starts inside [10,30)
		fullyInside := mustReserve(t, repo, 1, "default", at(15), at(18), 3)   // entirely inside
		mustReserve(t, repo, 1, "default", at(-10), at(10), 4)                 // ends exactly at query start
		mustReserve(t, repo, 1, "default", at(30), at(50), 5)                  // starts exactly at query end
		mustReserve(t, repo, 1, "default", at(-20), at(-5), 6)                 // entirely before
		mustReserve(t, repo, 1, "default", at(60), at(80), 7)                  // entirely after

		got, err := repo.ReservationsInWindow(ctx, 1, "default", at(10), at(30))
		if err != nil {
			t.Fatalf("ReservationsInWindow: %v", err)
		}
		gotExec := make(map[int64]bool, len(got))
		for _, r := range got {
			gotExec[r.ExecutionID] = true
		}
		for _, want := range []int64{straddlesStart, straddlesEnd, fullyInside} {
			if !gotExec[want] {
				t.Errorf("ReservationsInWindow missing execution %d, which genuinely overlaps [10,30)", want)
			}
		}
		if len(got) != 3 {
			t.Errorf("ReservationsInWindow = %d reservations, want exactly 3 (abutting/outside windows must not match): %+v", len(got), got)
		}
	})

	t.Run("InWindowScopesByTenantAndCluster", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		mustReserve(t, repo, 1, "default", at(0), at(100), 1)
		mustReserve(t, repo, 2, "default", at(0), at(100), 2) // different tenant
		mustReserve(t, repo, 1, "eu-west", at(0), at(100), 3) // different cluster

		got, err := repo.ReservationsInWindow(ctx, 1, "default", at(0), at(100))
		if err != nil {
			t.Fatalf("ReservationsInWindow: %v", err)
		}
		if len(got) != 1 || got[0].ExecutionID != 1 {
			t.Fatalf("ReservationsInWindow = %+v, want only tenant 1's default-cluster reservation", got)
		}
	})

	// An unconfigured ceiling reads as 0 -- unconfigured, not unlimited -- and
	// is scoped per cluster, not shared across a tenant's clusters.
	t.Run("CeilingDefaultsToZeroAndIsPerCluster", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		got, err := repo.GetCeiling(ctx, 1, "default")
		if err != nil {
			t.Fatalf("GetCeiling before configured: %v", err)
		}
		if got != 0 {
			t.Fatalf("GetCeiling before configured = %d, want 0", got)
		}

		if err := repo.SetCeiling(ctx, 1, "default", 10); err != nil {
			t.Fatalf("SetCeiling: %v", err)
		}
		got, err = repo.GetCeiling(ctx, 1, "default")
		if err != nil {
			t.Fatalf("GetCeiling: %v", err)
		}
		if got != 10 {
			t.Fatalf("GetCeiling = %d, want 10", got)
		}

		if got, err := repo.GetCeiling(ctx, 1, "eu-west"); err != nil || got != 0 {
			t.Fatalf("GetCeiling(other cluster) = %d,%v, want 0,nil -- ceiling is per cluster", got, err)
		}

		if err := repo.SetCeiling(ctx, 1, "default", 20); err != nil {
			t.Fatalf("SetCeiling (overwrite): %v", err)
		}
		if got, err := repo.GetCeiling(ctx, 1, "default"); err != nil || got != 20 {
			t.Fatalf("GetCeiling after overwrite = %d,%v, want 20,nil", got, err)
		}
	})

	// Cluster-scoped quota listing: what a capacity aggregate sums across
	// tenants. Scoped to the one cluster, ordered by tenant ID, reflects
	// overwrites, and empty (not an error) for a cluster nobody configured.
	t.Run("ListClusterQuotasScopesByCluster", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		if err := repo.SetCeiling(ctx, 1, "default", 10); err != nil {
			t.Fatalf("SetCeiling(1): %v", err)
		}
		if err := repo.SetCeiling(ctx, 2, "default", 5); err != nil {
			t.Fatalf("SetCeiling(2): %v", err)
		}
		if err := repo.SetCeiling(ctx, 1, "eu-west", 7); err != nil {
			t.Fatalf("SetCeiling(other cluster): %v", err)
		}

		got, err := repo.ListClusterQuotas(ctx, "default")
		if err != nil {
			t.Fatalf("ListClusterQuotas: %v", err)
		}
		want := []ports.TenantQuota{{TenantID: 1, Ceiling: 10}, {TenantID: 2, Ceiling: 5}}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("ListClusterQuotas = %+v, want %+v (only default-cluster rows, by tenant ID)", got, want)
		}

		// An overwrite is the row's new truth -- the same contract
		// GetCeiling's overwrite case pins for one tenant.
		if err := repo.SetCeiling(ctx, 1, "default", 4); err != nil {
			t.Fatalf("SetCeiling (overwrite): %v", err)
		}
		got, err = repo.ListClusterQuotas(ctx, "default")
		if err != nil {
			t.Fatalf("ListClusterQuotas (after overwrite): %v", err)
		}
		if len(got) != 2 || got[0].Ceiling != 4 {
			t.Fatalf("ListClusterQuotas (after overwrite) = %+v, want tenant 1's ceiling at 4", got)
		}

		other, err := repo.ListClusterQuotas(ctx, "ghost")
		if err != nil {
			t.Fatalf("ListClusterQuotas(unconfigured cluster): %v, want nil", err)
		}
		if len(other) != 0 {
			t.Fatalf("ListClusterQuotas(unconfigured cluster) = %+v, want empty", other)
		}
	})

	// Unlike InWindow, ReservationsForTenant ignores windows entirely -- it's
	// what an overrun-reclaim pass scans to find every reservation for a
	// tenant+cluster regardless of whether it overlaps anything in
	// particular, including ones whose declared window is long past.
	t.Run("ForTenantIgnoresWindowButScopesByTenantAndCluster", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		mustReserve(t, repo, 1, "default", at(-1000), at(-900), 1) // long past
		mustReserve(t, repo, 1, "default", at(0), at(100), 2)
		mustReserve(t, repo, 2, "default", at(0), at(100), 3) // different tenant
		mustReserve(t, repo, 1, "eu-west", at(0), at(100), 4) // different cluster

		got, err := repo.ReservationsForTenant(ctx, 1, "default")
		if err != nil {
			t.Fatalf("ReservationsForTenant: %v", err)
		}
		gotExec := make(map[int64]bool, len(got))
		for _, r := range got {
			gotExec[r.ExecutionID] = true
		}
		if len(got) != 2 || !gotExec[1] || !gotExec[2] {
			t.Fatalf("ReservationsForTenant = %+v, want executions 1 and 2 (tenant 1's default-cluster reservations, any window)", got)
		}
	})

	// This is the guarantee quotaapp.Reserve relies on: two concurrent
	// admission decisions for the same tenant+cluster must not interleave,
	// or each could read the same "used capacity" before either commits its
	// own reservation, jointly admitting more than any ceiling allows.
	t.Run("WithTenantLockSerializesConcurrentCallsForTheSameKey", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		var mu sync.Mutex
		inside, maxConcurrent := 0, 0
		var wg sync.WaitGroup
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = repo.WithTenantLock(ctx, 1, "default", func(context.Context) error {
					mu.Lock()
					inside++
					if inside > maxConcurrent {
						maxConcurrent = inside
					}
					mu.Unlock()

					time.Sleep(10 * time.Millisecond)

					mu.Lock()
					inside--
					mu.Unlock()
					return nil
				})
			}()
		}
		wg.Wait()
		if maxConcurrent != 1 {
			t.Fatalf("max concurrent holders of the same tenant+cluster lock = %d, want 1", maxConcurrent)
		}
	})

	// A different tenant+cluster must not queue behind an unrelated one --
	// only the actual contended resource should serialize.
	t.Run("WithTenantLockDoesNotBlockADifferentKey", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		started := make(chan struct{}, 2)
		release := make(chan struct{})
		var wg sync.WaitGroup
		for _, key := range []struct {
			tenantID int64
			cluster  string
		}{{1, "default"}, {2, "default"}} {
			key := key
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = repo.WithTenantLock(ctx, key.tenantID, key.cluster, func(context.Context) error {
					started <- struct{}{}
					<-release
					return nil
				})
			}()
		}
		for i := 0; i < 2; i++ {
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("a different tenant+cluster's WithTenantLock blocked on an unrelated one")
			}
		}
		close(release)
		wg.Wait()
	})

	// fn's error must reach the caller unchanged, and the lock must still be
	// released -- otherwise every future admission for the tenant+cluster
	// would hang after the first rejection.
	t.Run("WithTenantLockPropagatesFnErrorAndStillReleasesTheLock", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		sentinel := errors.New("boom")

		err := repo.WithTenantLock(ctx, 1, "default", func(context.Context) error { return sentinel })
		if !errors.Is(err, sentinel) {
			t.Fatalf("WithTenantLock error = %v, want %v", err, sentinel)
		}

		done := make(chan error, 1)
		go func() {
			done <- repo.WithTenantLock(ctx, 1, "default", func(context.Context) error { return nil })
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("WithTenantLock after a prior fn error = %v, want nil", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("WithTenantLock after a prior fn error hung -- lock not released")
		}
	})
}

func mustReserve(t *testing.T, repo ports.ReservationRepository, tenantID int64, cluster string, start, end time.Time, executionID int64) int64 {
	t.Helper()
	_, err := repo.CreateReservation(context.Background(), reservation.Reservation{
		TenantID: tenantID, Cluster: cluster, EngineCount: 1,
		Start: start, End: end, ExecutionID: executionID,
	})
	if err != nil {
		t.Fatalf("CreateReservation: %v", err)
	}
	return executionID
}
