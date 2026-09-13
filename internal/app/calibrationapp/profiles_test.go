package calibrationapp_test

import (
	"context"
	"testing"

	"github.com/heridotlife/honryu/internal/app/calibrationapp"
	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/capacityprofile"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// upsertProfile seeds one CapacityProfile, tolerating the error only to
// keep table tests one line per row.
func upsertProfile(t *testing.T, store *fake.Store, p capacityprofile.CapacityProfile) {
	t.Helper()
	if err := store.UpsertCapacityProfile(context.Background(), p); err != nil {
		t.Fatalf("UpsertCapacityProfile(%+v): %v", p.Key, err)
	}
}

// TestProfiles_OrdersByScenarioThenBiggestPodFirst pins the list order the
// capacity-matrix endpoint serves: scenarios ascending, then pod size
// biggest first -- cpu compared as milli-cores, the order lexicographic
// comparison gets wrong ("1" < "250m" < "2" as strings; 1000 > 500 > 250
// as milli). Rows are seeded deliberately out of order.
func TestProfiles_OrdersByScenarioThenBiggestPodFirst(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)
	ctx := context.Background()

	rows := []capacityprofile.CapacityProfile{
		{Key: capacityprofile.Key{ScenarioID: 6, Engine: taurus.ExecutorJMeter, CPU: "250m", Memory: "256Mi"}, PerPodQPS: 8.24, SaturatedBy: calibration.SaturatedByTarget},
		{Key: capacityprofile.Key{ScenarioID: 6, Engine: taurus.ExecutorJMeter, CPU: "2", Memory: "2Gi"}, PerPodQPS: 4543.9, SaturatedBy: calibration.SaturatedByEngine},
		{Key: capacityprofile.Key{ScenarioID: 6, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "1Gi"}, PerPodQPS: 1832.7, SaturatedBy: calibration.SaturatedByEngine},
		{Key: capacityprofile.Key{ScenarioID: 6, Engine: taurus.ExecutorJMeter, CPU: "500m", Memory: "512Mi"}, PerPodQPS: 608.5, SaturatedBy: calibration.SaturatedByEngine},
		{Key: capacityprofile.Key{ScenarioID: 2, Engine: taurus.ExecutorJMeter, CPU: "500m", Memory: "512Mi"}, PerPodQPS: 100, SaturatedBy: calibration.SaturatedByEngine},
	}
	for _, p := range rows {
		upsertProfile(t, store, p)
	}

	got, err := svc.Profiles(ctx)
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	wantCPUOrder := []string{"2", "1", "500m", "250m"} // scenario 6, biggest pod first
	if len(got) != len(rows) {
		t.Fatalf("Profiles returned %d rows, want %d", len(got), len(rows))
	}
	if got[0].ScenarioID != 2 {
		t.Fatalf("first row is scenario %d, want 2 (scenarios ascending)", got[0].ScenarioID)
	}
	for i, want := range wantCPUOrder {
		if got[i+1].CPU != want {
			t.Fatalf("scenario 6 row %d cpu = %q, want %q (order: %v)",
				i, got[i+1].CPU, want, wantCPUOrder)
		}
	}
}

// TestProfiles_FallbackOrderStaysDeterministic covers the unparseable-cpu
// fallback: rows whose cpu does not parse as a k8s quantity must not fail
// the list, just fall back to deterministic tiebreaks.
func TestProfiles_FallbackOrderStaysDeterministic(t *testing.T) {
	t.Parallel()
	store := fake.NewStore()
	svc := calibrationapp.NewService(store)

	rows := []capacityprofile.CapacityProfile{
		{Key: capacityprofile.Key{ScenarioID: 1, Engine: "k6", CPU: "bogus", Memory: "1Gi"}},
		{Key: capacityprofile.Key{ScenarioID: 1, Engine: "jmeter", CPU: "1", Memory: "1Gi"}},
	}
	for _, p := range rows {
		upsertProfile(t, store, p)
	}

	got, err := svc.Profiles(context.Background())
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Profiles returned %d rows, want 2", len(got))
	}
	// Same scenario and milli-tiebreak unavailable: engine breaks the tie
	// ascending, so jmeter precedes k6.
	if got[0].Engine != "jmeter" || got[1].Engine != "k6" {
		t.Fatalf("engine tiebreak order = %q, %q; want jmeter, k6", got[0].Engine, got[1].Engine)
	}
}

// TestProfiles_EmptyIsAnEmptySliceNotAnError pins the empty state the
// matrix UI renders its "no calibrations yet" from.
func TestProfiles_EmptyIsAnEmptySliceNotAnError(t *testing.T) {
	t.Parallel()
	svc := calibrationapp.NewService(fake.NewStore())
	got, err := svc.Profiles(context.Background())
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Profiles on an empty ledger = %+v, want none", got)
	}
}
