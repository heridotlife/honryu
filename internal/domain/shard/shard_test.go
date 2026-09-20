package shard_test

import (
	"errors"
	"testing"

	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/shard"
)

func entry(concurrency, engines int) loadprofile.Entry {
	return loadprofile.Entry{
		ScenarioID: 1, Concurrency: concurrency, Engines: engines,
		Rampup: 30, Duration: 600,
	}
}

func TestPlan_SplitsConcurrency(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		conc int
		n    int
		want []int // concurrency per shard
	}{
		{"single shard takes everything", 50, 1, []int{50}},
		{"even split", 50, 5, []int{10, 10, 10, 10, 10}},
		{"remainder goes to the earliest shards", 10, 3, []int{4, 3, 3}},
		{"remainder of one", 7, 2, []int{4, 3}},
		{"one user per shard", 4, 4, []int{1, 1, 1, 1}},
		// A pod with no users generates nothing and bzt rejects concurrency 0,
		// so the plan uses fewer shards rather than empty ones.
		{"more shards than users", 3, 10, []int{1, 1, 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := shard.Plan(entry(tc.conc, tc.n), tc.n)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d shards, want %d: %+v", len(got), len(tc.want), got)
			}
			total := 0
			for i, s := range got {
				if s.Concurrency != tc.want[i] {
					t.Errorf("shard %d concurrency = %d, want %d", i, s.Concurrency, tc.want[i])
				}
				if s.Index != i {
					t.Errorf("shard %d has index %d", i, s.Index)
				}
				total += s.Concurrency
			}
			if total != tc.conc {
				t.Errorf("shards total %d users, want %d", total, tc.conc)
			}
		})
	}
}

// The load the shards produce together must be the load that was asked for.
// Under-shooting silently would make a target look healthier than it is.
func TestPlan_ShardsAlwaysSumToTheRequestedLoad(t *testing.T) {
	t.Parallel()

	for conc := 1; conc <= 64; conc++ {
		for n := 1; n <= 20; n++ {
			shards, err := shard.Plan(entry(conc, n), n)
			if err != nil {
				t.Fatalf("Plan(conc=%d, n=%d): %v", conc, n, err)
			}
			total, min, max := 0, shards[0].Concurrency, shards[0].Concurrency
			for _, s := range shards {
				total += s.Concurrency
				if s.Concurrency < min {
					min = s.Concurrency
				}
				if s.Concurrency > max {
					max = s.Concurrency
				}
				if s.Concurrency < 1 {
					t.Fatalf("Plan(conc=%d, n=%d) produced an empty shard", conc, n)
				}
			}
			if total != conc {
				t.Fatalf("Plan(conc=%d, n=%d) totals %d", conc, n, total)
			}
			// Uneven shards mean uneven pods; keep them within one user of
			// each other so no pod becomes the straggler that ends the run late.
			if max-min > 1 {
				t.Fatalf("Plan(conc=%d, n=%d) is unbalanced: min=%d max=%d", conc, n, min, max)
			}
		}
	}
}

func TestPlan_SplitsThroughput(t *testing.T) {
	t.Parallel()

	e := entry(10, 4)
	e.Throughput = 1000
	got, err := shard.Plan(e, 4)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	total := 0
	for _, s := range got {
		total += s.Throughput
	}
	if total != 1000 {
		t.Errorf("shard throughput totals %d, want 1000", total)
	}

	// Throughput is optional; zero means unlimited and must stay unlimited
	// rather than becoming a cap of zero, which would generate no load at all.
	e.Throughput = 0
	got, err = shard.Plan(e, 4)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, s := range got {
		if s.Throughput != 0 {
			t.Errorf("unlimited throughput became %d", s.Throughput)
		}
	}
}

// Every shard runs the same ramp and hold. Dividing the ramp would make each pod
// reach full load at a different time and the aggregate profile would not be the
// one requested.
func TestPlan_PreservesTiming(t *testing.T) {
	t.Parallel()

	got, err := shard.Plan(entry(9, 3), 3)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, s := range got {
		if s.RampupSeconds != 30 || s.DurationSeconds != 600 {
			t.Errorf("shard %d timing = ramp %d hold %d, want 30/600",
				s.Index, s.RampupSeconds, s.DurationSeconds)
		}
	}
}

func TestPlan_Errors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		entry   loadprofile.Entry
		n       int
		wantErr error
	}{
		{"zero shards", entry(10, 0), 0, shard.ErrShardsInvalid},
		{"negative shards", entry(10, -1), -1, shard.ErrShardsInvalid},
		{"zero concurrency", entry(0, 2), 2, shard.ErrConcurrencyInvalid},
		{"negative concurrency", entry(-5, 2), 2, shard.ErrConcurrencyInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := shard.Plan(tc.entry, tc.n); !errors.Is(err, tc.wantErr) {
				t.Errorf("Plan = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// Planning must be deterministic: the same entry always shards the same way, or
// a re-deploy would move load between pods and make runs incomparable.
func TestPlan_IsDeterministic(t *testing.T) {
	t.Parallel()

	first, err := shard.Plan(entry(17, 5), 5)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for i := 0; i < 10; i++ {
		again, err := shard.Plan(entry(17, 5), 5)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		for j := range first {
			if again[j] != first[j] {
				t.Fatalf("shard %d differs between runs: %+v vs %+v", j, again[j], first[j])
			}
		}
	}
}

func TestTotal(t *testing.T) {
	t.Parallel()

	e := entry(10, 3)
	e.Throughput = 900
	shards, err := shard.Plan(e, 3)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	conc, tput := shard.Total(shards)
	if conc != 10 || tput != 900 {
		t.Errorf("Total() = (%d, %d), want (10, 900)", conc, tput)
	}
	if conc, tput := shard.Total(nil); conc != 0 || tput != 0 {
		t.Errorf("Total(nil) = (%d, %d), want (0, 0)", conc, tput)
	}
}

// Split is Plan's own share discipline extracted for multi-stage entries
// (phase 98's staircase): base division with the remainder to the earliest
// shards, never dropping load and never handing a later shard more than an
// earlier one.
func TestSplit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		total, n int
		want     []int
	}{
		{10, 2, []int{5, 5}},
		{7, 3, []int{3, 2, 2}},
		{2, 5, []int{1, 1, 0, 0, 0}},
		{20, 1, []int{20}},
	}
	for _, tc := range cases {
		got := make([]int, tc.n)
		sum := 0
		for i := range got {
			got[i] = shard.Split(tc.total, tc.n, i)
			sum += got[i]
			if i > 0 && got[i] > got[i-1] {
				t.Errorf("Split(%d, %d, %d) = %d rises above its predecessor %d", tc.total, tc.n, i, got[i], got[i-1])
			}
		}
		if sum != tc.total {
			t.Errorf("Split(%d, %d) summed %d, want %d", tc.total, tc.n, sum, tc.total)
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("Split(%d, %d) = %v, want %v", tc.total, tc.n, got, tc.want)
				break
			}
		}
	}
}
