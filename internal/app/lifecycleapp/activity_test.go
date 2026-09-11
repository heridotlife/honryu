package lifecycleapp_test

import (
	"context"
	"testing"
)

// Every lifecycle event that proves an execution's engines are in use must
// restamp its idle clock -- the TTL reaper measures that clock, so a missing
// stamp tears down live engines and a stale one keeps dead ones forever. The
// touch counter (not the timestamps) is what pins each path: deploy, trigger,
// and teardown all land in the same second against a fake clock.
func TestLifecycleStampsEngineActivity(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 2)
	ctx := context.Background()

	if got := e.store.TouchActivityCount(e.executionID); got != 0 {
		t.Fatalf("activity touches before any lifecycle call = %d, want 0", got)
	}

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if got := e.store.TouchActivityCount(e.executionID); got != 1 {
		t.Fatalf("activity touches after Deploy = %d, want 1", got)
	}

	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if got := e.store.TouchActivityCount(e.executionID); got != 2 {
		t.Fatalf("activity touches after Trigger = %d, want 2", got)
	}

	if err := e.svc.Stop(ctx, e.executionID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := e.store.TouchActivityCount(e.executionID); got != 3 {
		t.Fatalf("activity touches after Stop = %d, want 3 (teardown restamps)", got)
	}

	last, ok, err := e.store.LastActivity(ctx, e.executionID)
	if err != nil || !ok {
		t.Fatalf("LastActivity = %v,%v; want true,nil", ok, err)
	}
	if last.IsZero() {
		t.Fatal("LastActivity stamp is zero")
	}
}

// Purge runs teardown too (stopping an in-progress run, or completing the
// teardown a naturally-finished run never got), so it restamps as well --
// pointlessly for the pods it is about to delete, but harmlessly and without
// a special case.
func TestPurgeStampsEngineActivityViaTeardown(t *testing.T) {
	t.Parallel()
	e := setup(t, false, 1)
	ctx := context.Background()

	if err := e.svc.Deploy(ctx, e.executionID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := e.svc.Trigger(ctx, e.executionID); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	before := e.store.TouchActivityCount(e.executionID)

	if err := e.svc.Purge(ctx, e.executionID); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if got := e.store.TouchActivityCount(e.executionID); got != before+1 {
		t.Fatalf("activity touches after Purge = %d, want %d (teardown restamps)", got, before+1)
	}
}
