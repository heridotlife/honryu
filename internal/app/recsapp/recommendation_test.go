package recsapp

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/domain/threshold"
)

// missedResult builds one missed threshold result: observed 700ms against a
// "lt 300" p95 bound.
func missedResult() threshold.Result {
	observed := 700.0
	no := false
	return threshold.Result{
		ThresholdID: 1, ExecutionID: 1, RunID: 42,
		Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: 300,
		Observed: &observed, Satisfied: &no,
	}
}

// metResult builds one met threshold result (satisfied true).
func metResult() threshold.Result {
	observed := 200.0
	yes := true
	return threshold.Result{
		ThresholdID: 2, ExecutionID: 1, RunID: 42,
		Metric: threshold.MetricThroughputQPS, Comparison: threshold.ComparisonGT, Value: 50,
		Observed: &observed, Satisfied: &yes,
	}
}

// unknownResult builds one result with no observed figure -- unknown, never
// missed.
func unknownResult() threshold.Result {
	return threshold.Result{
		ThresholdID: 3, ExecutionID: 1, RunID: 42,
		Metric: threshold.MetricHTTPP99MS, Comparison: threshold.ComparisonLT, Value: 500,
		Reason: "no p99 latency in the report",
	}
}

// cleanReport is a run nothing complains about: tight error rate, tight
// spread, requested throughput met, nothing missed.
func cleanReport() report.Report {
	return report.Report{
		ExecutionID: 1, ScenarioID: 2, RunID: 42,
		Requested: report.Load{Concurrency: 10, Throughput: 100, DurationSeconds: 30},
		Achieved:  report.Load{Concurrency: 10, Throughput: 98, Samples: 3000},
		ErrorRate: 0.001,
		Latency:   report.Percentiles{50: 0.05, 99: 0.1},
	}
}

// cleanInput wraps a report in an Input whose overlay evidence reads
// healthy: a capacity profile exists for the scenario's exact pod size and
// was calibrated an hour ago. A rule's test embeds it so only the rule
// under test fires; the test passes the layer it exercises on top.
func cleanInput(rep report.Report) Input {
	qps, age := 120.0, time.Hour
	return Input{Report: rep, CalibratedPerPodQPS: &qps, ProfileAge: &age}
}

func TestAnalyze_HighErrorRate(t *testing.T) {
	rep := cleanReport()
	rep.ErrorRate = 0.032
	rep.Attribution = report.Attribution{Target: 25, Engine: 3, Unknown: 4}

	got := Analyze(cleanInput(rep))
	if len(got) != 1 {
		t.Fatalf("Analyze = %+v, want exactly the high-error-rate recommendation", got)
	}
	rec := got[0]
	if rec.ID != IDHighErrorRate || rec.Severity != SeverityWarning {
		t.Errorf("rec = %+v, want id %q severity warning", rec, IDHighErrorRate)
	}
	// Actionable: names the rate, the attribution split, the target's
	// health, and the Checks tab.
	for _, want := range []string{"3.2%", "25 target-side", "Checks tab"} {
		if !contains(rec.Detail, want) {
			t.Errorf("detail %q missing %q", rec.Detail, want)
		}
	}

	// Exactly at the threshold is not above it: no fire.
	rep.ErrorRate = highErrorRateThreshold
	if got := Analyze(cleanInput(rep)); len(got) != 0 {
		t.Errorf("Analyze at exactly 1%% = %+v, want empty", got)
	}
}

func TestAnalyze_LatencySpread(t *testing.T) {
	rep := cleanReport()
	rep.Latency = report.Percentiles{50: 0.05, 99: 0.4} // 8x spread

	got := Analyze(cleanInput(rep))
	if len(got) != 1 || got[0].ID != IDLatencySpread {
		t.Fatalf("Analyze = %+v, want exactly the latency-spread recommendation", got)
	}
	for _, want := range []string{"8.0x p50", "50 ms", "400 ms", "connection reuse"} {
		if !contains(got[0].Detail, want) {
			t.Errorf("detail %q missing %q", got[0].Detail, want)
		}
	}

	// A 4x spread sits on the safe side: the rule fires beyond, not at, the
	// factor.
	rep.Latency = report.Percentiles{50: 0.05, 99: 0.2}
	if got := Analyze(cleanInput(rep)); len(got) != 0 {
		t.Errorf("Analyze at exactly 4x = %+v, want empty", got)
	}

	// Missing figures or a zero median cannot a ratio make.
	for _, latency := range []report.Percentiles{
		{99: 0.4},        // no p50
		{50: 0.05},       // no p99
		{50: 0, 99: 0.4}, // zero median
	} {
		rep.Latency = latency
		if got := Analyze(cleanInput(rep)); len(got) != 0 {
			t.Errorf("Analyze with latency %v = %+v, want empty", latency, got)
		}
	}
}

func TestAnalyze_ThresholdMissed(t *testing.T) {
	// Two missed bounds: both named, plural title, deterministic result
	// (evaluation order).
	in := cleanInput(cleanReport())
	in.ThresholdResults = []threshold.Result{
		metResult(), missedResult(), unknownResult(),
		{ThresholdID: 4, Metric: threshold.MetricErrorRate, Comparison: threshold.ComparisonLT,
			Value: 0.005, Observed: ptr(0.02), Satisfied: ptr(false)},
	}
	got := Analyze(in)
	if len(got) != 1 {
		t.Fatalf("Analyze = %+v, want exactly the threshold-missed recommendation", got)
	}
	rec := got[0]
	if rec.ID != IDThresholdMissed || rec.Severity != SeverityWarning {
		t.Errorf("rec = %+v, want id %q severity warning", rec, IDThresholdMissed)
	}
	if rec.Title != "Thresholds missed" {
		t.Errorf("title = %q, want the plural for two misses", rec.Title)
	}
	// The met result is absent, the unknown one is not convicted, and the
	// two misses carry their observed figures and bounds in order.
	if contains(rec.Detail, "50.0 req/s") {
		t.Errorf("detail %q names the met threshold", rec.Detail)
	}
	if contains(rec.Detail, "unknown") && contains(rec.Detail, "http_p99_ms") {
		t.Errorf("detail %q reads the unknown p99 as missed", rec.Detail)
	}
	wantP95 := "http_p95_ms observed 700 ms against a bound of lt 300 ms"
	wantErr := "error_rate observed 2.0% against a bound of lt 0.5%"
	if !contains(rec.Detail, wantP95) || !contains(rec.Detail, wantErr) {
		t.Errorf("detail %q missing %q / %q", rec.Detail, wantP95, wantErr)
	}
	if !contains(rec.Detail, "recalibrate") {
		t.Errorf("detail %q missing the recalibrate/compare advice", rec.Detail)
	}

	// One miss reads singular.
	in = cleanInput(cleanReport())
	in.ThresholdResults = []threshold.Result{missedResult()}
	got = Analyze(in)
	if len(got) != 1 || got[0].Title != "Threshold missed" {
		t.Fatalf("Analyze = %+v, want the singular threshold-missed recommendation", got)
	}

	// Unknown-only evidence is not a miss.
	in = cleanInput(cleanReport())
	in.ThresholdResults = []threshold.Result{unknownResult()}
	got = Analyze(in)
	if len(got) != 0 {
		t.Errorf("Analyze = %+v, want empty for unknown-only results", got)
	}
}

func TestAnalyze_LowThroughput(t *testing.T) {
	rep := cleanReport()
	rep.Achieved.Throughput = 70 // 70% of the requested 100

	got := Analyze(cleanInput(rep))
	if len(got) != 1 || got[0].ID != IDLowThroughput {
		t.Fatalf("Analyze = %+v, want exactly the low-throughput recommendation", got)
	}
	for _, want := range []string{"70.0 req/s", "100.0 req/s", "calibration"} {
		if !contains(got[0].Detail, want) {
			t.Errorf("detail %q missing %q", got[0].Detail, want)
		}
	}

	// Within tolerance (95%): no fire.
	rep.Achieved.Throughput = 96
	if got := Analyze(cleanInput(rep)); len(got) != 0 {
		t.Errorf("Analyze = %+v, want empty at 96%% of requested", got)
	}

	// The reachability decision: an open-ended scenario (no requested rate)
	// skips the rule gracefully -- no requested figure, nothing to fall
	// short of.
	rep.Achieved.Throughput = 1
	rep.Requested.Throughput = 0
	if got := Analyze(Input{Report: rep}); len(got) != 0 {
		t.Errorf("Analyze = %+v, want empty when no rate was requested", got)
	}
}

func TestAnalyze_NoThresholds(t *testing.T) {
	// Known and empty: the rule fires, info-level, pointing at the editor.
	in := cleanInput(cleanReport())
	in.ThresholdsKnown = true
	got := Analyze(in)
	if len(got) != 1 || got[0].ID != IDNoThresholds {
		t.Fatalf("Analyze = %+v, want exactly the no-thresholds recommendation", got)
	}
	if got[0].Severity != SeverityInfo {
		t.Errorf("severity = %q, want info", got[0].Severity)
	}
	if !contains(got[0].Detail, "threshold editor") {
		t.Errorf("detail %q missing the editor pointer", got[0].Detail)
	}

	// Known and non-empty: no fire.
	in.ThresholdsDefined = []threshold.Threshold{{ID: 1, ScenarioID: 2,
		Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: 300}}
	got = Analyze(in)
	if len(got) != 0 {
		t.Errorf("Analyze = %+v, want empty when thresholds are defined", got)
	}

	// Unknown set (service unwired, read failed): never reported as empty.
	got = Analyze(cleanInput(cleanReport()))
	if len(got) != 0 {
		t.Errorf("Analyze = %+v, want empty for an unknown threshold set", got)
	}
}

func TestAnalyze_AbortedRun(t *testing.T) {
	rep := cleanReport()
	rep.Outcome = taurus.OutcomeAborted

	got := Analyze(cleanInput(rep))
	if len(got) != 1 || got[0].ID != IDAbortedRun {
		t.Fatalf("Analyze = %+v, want exactly the aborted-run recommendation", got)
	}
	rec := got[0]
	if rec.Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning", rec.Severity)
	}
	// Actionable: names the partial window and points at both where to see
	// it and what to do about it.
	for _, want := range []string{"partial window", "per-second series", "re-run"} {
		if !contains(rec.Detail, want) {
			t.Errorf("detail %q missing %q", rec.Detail, want)
		}
	}

	// Every other outcome stands the rule down -- including error, which is
	// a statement about the engine, not an interruption of the window.
	for _, outcome := range []taurus.Outcome{taurus.OutcomePassed, taurus.OutcomeFailed, taurus.OutcomeError, ""} {
		rep.Outcome = outcome
		if got := Analyze(cleanInput(rep)); len(got) != 0 {
			t.Errorf("Analyze with outcome %q = %+v, want empty", outcome, got)
		}
	}
}

func TestAnalyze_CapacityUnverified(t *testing.T) {
	// cleanReport requests a fixed 100 req/s; no profile exists for the
	// scenario's exact pod size.
	got := Analyze(Input{Report: cleanReport()})
	if len(got) != 1 || got[0].ID != IDCapacityUnverified {
		t.Fatalf("Analyze = %+v, want exactly the capacity-unverified recommendation", got)
	}
	rec := got[0]
	if rec.Severity != SeverityInfo {
		t.Errorf("severity = %q, want info", rec.Severity)
	}
	for _, want := range []string{"no capacity profile", "exact pod size", "Calibrate to know your ceiling"} {
		if !contains(rec.Detail, want) {
			t.Errorf("detail %q missing %q", rec.Detail, want)
		}
	}

	// A profile for the exact key stands this rule down -- even a stale one:
	// staleness is the outdated rule's evidence, never this one's, and the
	// two never convict together.
	in := cleanInput(cleanReport())
	stale := 60 * 24 * time.Hour
	in.ProfileAge = &stale
	got = Analyze(in)
	if len(got) != 1 || got[0].ID != IDCapacityOutdated {
		t.Fatalf("Analyze with a stale profile = %+v, want only capacity-outdated", got)
	}

	// Open-ended scenario: no requested rate, no ask to verify -- the rule
	// never fires for one, even with capacity evidence absent.
	rep := cleanReport()
	rep.Requested.Throughput = 0
	if got := Analyze(Input{Report: rep}); len(got) != 0 {
		t.Errorf("Analyze open-ended = %+v, want empty", got)
	}
}

func TestAnalyze_CapacityOutdated(t *testing.T) {
	in := cleanInput(cleanReport())
	stale := 31 * 24 * time.Hour
	in.ProfileAge = &stale

	got := Analyze(in)
	if len(got) != 1 || got[0].ID != IDCapacityOutdated {
		t.Fatalf("Analyze = %+v, want exactly the capacity-outdated recommendation", got)
	}
	rec := got[0]
	if rec.Severity != SeverityInfo {
		t.Errorf("severity = %q, want info", rec.Severity)
	}
	for _, want := range []string{"31 days ago", "changed since calibration", "re-calibrate"} {
		if !contains(rec.Detail, want) {
			t.Errorf("detail %q missing %q", rec.Detail, want)
		}
	}

	// Exactly at the 30-day line is not past it: no fire.
	atLine := 30 * 24 * time.Hour
	in.ProfileAge = &atLine
	if got := Analyze(in); len(got) != 0 {
		t.Errorf("Analyze at exactly 30 days = %+v, want empty", got)
	}

	// Unknown age never fires: absence of evidence is not staleness, even
	// with a profile present.
	in.ProfileAge = nil
	if got := Analyze(in); len(got) != 0 {
		t.Errorf("Analyze with unknown age = %+v, want empty", got)
	}
}

// TestAnalyze_IndependentAndOrdered pins the engine's two structural
// contracts: every rule judges its own evidence alone, and the output order
// is the fixed rule order. Here the capacity layer's evidence is healthy
// (fresh profile) so the five telemetry/threshold rules stand or fall on
// their own; the full eight-rule order, capacity rules included, is pinned
// by TestAnalyze_RuleOrderDrift.
func TestAnalyze_IndependentAndOrdered(t *testing.T) {
	rep := cleanReport()
	rep.ErrorRate = 0.05
	rep.Latency = report.Percentiles{50: 0.05, 99: 0.5}
	rep.Achieved.Throughput = 40

	in := cleanInput(rep)
	in.ThresholdsKnown = true
	in.ThresholdResults = []threshold.Result{missedResult()}
	got := Analyze(in)
	want := []string{IDHighErrorRate, IDLatencySpread, IDThresholdMissed, IDLowThroughput, IDNoThresholds}
	if len(got) != len(want) {
		t.Fatalf("Analyze = %d recommendations %+v, want %d", len(got), got, len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("got[%d].ID = %q, want %q", i, got[i].ID, id)
		}
	}

	// Independence: each rule's evidence removed un-fires exactly that rule,
	// leaving the others' order intact.
	rep.ErrorRate = 0
	rep.Latency = report.Percentiles{50: 0.05, 99: 0.1}
	in = cleanInput(rep)
	in.ThresholdsKnown = true
	in.ThresholdResults = []threshold.Result{metResult()}
	got = Analyze(in)
	if len(got) != 2 || got[0].ID != IDLowThroughput || got[1].ID != IDNoThresholds {
		t.Errorf("Analyze = %+v, want [low-throughput no-thresholds] in order", got)
	}
}

// TestAnalyze_RuleOrderDrift pins the wire contract phase 84 freezes: the
// full fixed rule order, exactly. One input cannot fire all eight rules at
// once -- capacity-unverified and capacity-outdated are mutually exclusive
// by construction, because a profile for the exact key either exists or
// does not -- so two synthetic inputs jointly pin every position: one with
// no profile, one with a profile past the freshness line. The stand-down
// direction is pinned at the Analyze level too: every layer's evidence
// healthy collapses to an empty, never nil, list, which is the nil-filter
// itself.
func TestAnalyze_RuleOrderDrift(t *testing.T) {
	// Everything wrong at once: errors above the line, an 8x tail, a missed
	// bound, 40% of the requested rate, an aborted outcome, and a scenario
	// with no thresholds defined.
	rep := cleanReport()
	rep.ErrorRate = 0.05
	rep.Attribution = report.Attribution{Target: 25, Engine: 3, Unknown: 4}
	rep.Latency = report.Percentiles{50: 0.05, 99: 0.5}
	rep.Achieved.Throughput = 40
	rep.Outcome = taurus.OutcomeAborted

	base := func() Input {
		in := Input{Report: rep, ThresholdsKnown: true}
		in.ThresholdResults = []threshold.Result{missedResult()}
		return in
	}

	// No profile for the exact key: seven rules fire, in order.
	got := Analyze(base())
	assertRuleOrder(t, got, []string{
		IDHighErrorRate, IDLatencySpread, IDThresholdMissed, IDLowThroughput,
		IDAbortedRun, IDCapacityUnverified, IDNoThresholds,
	})

	// A profile past the freshness line: the same seven positions, with
	// capacity-outdated in capacity-unverified's seat.
	in := base()
	qps, stale := 120.0, 31*24*time.Hour
	in.CalibratedPerPodQPS, in.ProfileAge = &qps, &stale
	got = Analyze(in)
	assertRuleOrder(t, got, []string{
		IDHighErrorRate, IDLatencySpread, IDThresholdMissed, IDLowThroughput,
		IDAbortedRun, IDCapacityOutdated, IDNoThresholds,
	})

	// The stand-downs, through Analyze's own nil-filtering: every rule
	// returned nil, and the output is empty and non-nil -- [] on the wire,
	// never null and never a stale row surviving from the fired cases.
	fresh := cleanInput(cleanReport())
	fresh.ThresholdsKnown = true
	fresh.ThresholdsDefined = []threshold.Threshold{{ID: 1, ScenarioID: 2,
		Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: 300}}
	fresh.ThresholdResults = []threshold.Result{metResult()}
	if got = Analyze(fresh); got == nil || len(got) != 0 {
		t.Fatalf("Analyze = %#v, want empty non-nil: stand-down is the nil filter", got)
	}
}

// assertRuleOrder fails unless got's ids equal want, in order and exactly.
func assertRuleOrder(t *testing.T, got []Recommendation, want []string) {
	t.Helper()
	ids := make([]string, len(got))
	for i, rec := range got {
		ids[i] = rec.ID
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("rule order drift: got %v, want %v", ids, want)
	}
}

func TestAnalyze_CleanReportIsEmpty(t *testing.T) {
	in := cleanInput(cleanReport())
	in.ThresholdsKnown = true
	in.ThresholdsDefined = []threshold.Threshold{{ID: 1, ScenarioID: 2,
		Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: 300}}
	in.ThresholdResults = []threshold.Result{metResult()}
	got := Analyze(in)
	if len(got) != 0 {
		t.Fatalf("Analyze = %+v, want empty for a clean report", got)
	}
	if got == nil {
		t.Fatal("Analyze returned nil: the wire would carry null instead of []")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func ptr[T any](v T) *T { return &v }
