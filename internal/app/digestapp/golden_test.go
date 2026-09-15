package digestapp_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/digestapp"
	"github.com/heridotlife/honryu/internal/app/sloapp"
	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/digest"
	"github.com/heridotlife/honryu/internal/domain/taurus"
)

// goldenKeys is the EXACT top-level key set the report.digest payload may
// carry. This test is the guard against silent shape drift: a field added
// to digestapp.Payload must be consciously added here (and a field removed
// consciously dropped) -- the section names are the wire contract every
// receiver codes against.
var goldenKeys = []string{
	"by_outcome", "calibrations", "event", "executions", "period",
	"project_id", "runs_total", "slo_budgets", "threshold_failures",
	"window_end", "window_start",
}

// keysOf sorts a decoded JSON object's keys.
func keysOf(t *testing.T, m map[string]any) []string {
	t.Helper()
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// decode into a fresh map, failing the test on any mismatch.
func mustDecode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return m
}

// assertKeys fails the test unless got matches want exactly.
func assertKeys(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s keys = %v, want exactly %v -- the wire shape changed; update the golden shape deliberately", what, got, want)
	}
}

// TestBuildDigestGoldenShape pins the digest payload's FULL wire shape --
// every section (runs, outcomes, threshold failures, executions,
// slo_budgets, calibrations) with explicit per-field assertions and exact
// key sets at every level. A future addition that drops or renames a
// field, or lets an optional one silently vanish from the wire, fails
// here instead of in a receiver's parser. (Additions are welcome -- but
// they must update this shape on purpose.)
func TestBuildDigestGoldenShape(t *testing.T) {
	f := newFixture(t)
	start, end := window()
	in := start.Add(2 * time.Hour)

	// Runs: one of each of the three wire buckets, plus an error-outcome
	// run that counts in runs_total but lands in no bucket.
	saveRun(t, f.store, f.execA1, 1, in, taurus.OutcomePassed)
	saveRun(t, f.store, f.execA1, 2, in.Add(time.Hour), taurus.OutcomeFailed)
	saveRun(t, f.store, f.execA2, 3, in.Add(2*time.Hour), taurus.OutcomeAborted)
	saveRun(t, f.store, f.execA2, 4, in.Add(3*time.Hour), taurus.OutcomeError)

	// SLO budgets: one grade with both worst fields set.
	f.svc = f.svc.WithSLOGrader(&stubGrader{grades: []sloapp.WindowOutcome{
		windowOutcome(11, "checkout", true, "p95_ms", 12.5),
	}})

	// Calibrations: one finished search (result fields on) and one failed
	// one (failure_reason on, result fields off the wire except the null
	// per_pod_qps).
	checkout := seedScenario(t, f.store, "checkout", f.projA)
	execCal := mkExecution(t, f.store, "calib-engine", f.projA)
	f.store.SetNow(func() time.Time { return start.Add(4 * time.Hour) })
	doneID := seedCalibrationJob(t, f.store, execCal, checkout)
	finishJob(t, f.store, doneID, 9.5, calibration.SaturatedByEngine)
	failedID := seedCalibrationJob(t, f.store, execCal, checkout)
	if err := f.store.MarkFailed(context.Background(), failedID, "step deploy failed"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	f.store.SetNow(time.Now)

	d, err := f.svc.BuildDigest(context.Background(), f.projA, digest.PeriodDaily, start, end)
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	p := mustDecode(t, d.Payload)

	// 1. The top-level shape: every section present, none extra.
	assertKeys(t, "payload", keysOf(t, p), goldenKeys)

	// 2. Identity and window, field by field.
	if p["event"] != digestapp.EventDigest {
		t.Errorf("event = %v, want %q", p["event"], digestapp.EventDigest)
	}
	if _, ok := p["project_id"].(float64); !ok {
		t.Errorf("project_id = %v, want a number", p["project_id"])
	}
	if p["period"] != string(digest.PeriodDaily) {
		t.Errorf("period = %v, want %q", p["period"], digest.PeriodDaily)
	}
	if _, ok := p["window_start"].(string); !ok {
		t.Errorf("window_start = %v, want an RFC3339 string", p["window_start"])
	}
	if _, ok := p["window_end"].(string); !ok {
		t.Errorf("window_end = %v, want an RFC3339 string", p["window_end"])
	}
	if p["runs_total"] != float64(4) {
		t.Errorf("runs_total = %v, want 4 (the error run counts too)", p["runs_total"])
	}
	if p["threshold_failures"] != float64(1) {
		t.Errorf("threshold_failures = %v, want the 1 failed run", p["threshold_failures"])
	}

	// 3. by_outcome: exactly the three wire buckets.
	byOutcome := p["by_outcome"].(map[string]any)
	assertKeys(t, "by_outcome", keysOf(t, byOutcome), []string{"aborted", "failed", "passed"})
	if byOutcome["passed"] != float64(1) || byOutcome["failed"] != float64(1) || byOutcome["aborted"] != float64(1) {
		t.Errorf("by_outcome = %v, want one of each bucket (the error run in none)", byOutcome)
	}

	// 4. executions: one summary line per run-carrying execution.
	execs := p["executions"].([]any)
	if len(execs) != 2 {
		t.Fatalf("executions = %v, want 2 lines", execs)
	}
	assertKeys(t, "executions[0]", keysOf(t, execs[0].(map[string]any)), []string{"execution_id", "name", "runs", "worst_outcome"})
	if first := execs[0].(map[string]any); first["name"] != "checkout" || first["runs"] != float64(2) || first["worst_outcome"] != string(taurus.OutcomeFailed) {
		t.Errorf("executions[0] = %v, want checkout, 2 runs, worst failed", first)
	}

	// 5. slo_budgets: the grader's verdict with its worst fields.
	budgets := p["slo_budgets"].([]any)
	if len(budgets) != 1 {
		t.Fatalf("slo_budgets = %v, want 1 line", budgets)
	}
	assertKeys(t, "slo_budgets[0]", keysOf(t, budgets[0].(map[string]any)),
		[]string{"compliant", "name", "slo_id", "worst_budget_remaining_pct", "worst_metric"})
	line := budgets[0].(map[string]any)
	if line["slo_id"] != float64(11) || line["name"] != "checkout" || line["compliant"] != true ||
		line["worst_metric"] != "p95_ms" || line["worst_budget_remaining_pct"] != 12.5 {
		t.Errorf("slo_budgets[0] = %v, want the checkout grade verbatim", line)
	}

	// 6. calibrations, newest first: the failed search leads. It carries
	// failure_reason; per_pod_qps stays ON the wire as an explicit null,
	// and saturated_by is absent where it has nothing to say. The finished
	// search carries every field.
	cals := p["calibrations"].([]any)
	if len(cals) != 2 {
		t.Fatalf("calibrations = %v, want both jobs", cals)
	}
	failed := cals[0].(map[string]any)
	assertKeys(t, "calibrations[failed]", keysOf(t, failed),
		[]string{"created_time", "failure_reason", "job_id", "per_pod_qps", "phase", "scenario_id", "scenario_name"})
	if v, ok := failed["per_pod_qps"]; !ok || v != nil {
		t.Errorf("calibrations[failed].per_pod_qps = %v (present=%t), want an explicit null on the wire", v, ok)
	}
	if failed["failure_reason"] != "step deploy failed" || failed["phase"] != "failed" {
		t.Errorf("calibrations[failed] = %v, want phase failed with the reason carried", failed)
	}
	if _, present := failed["saturated_by"]; present {
		t.Error("calibrations[failed] carries saturated_by, want it absent until a search concludes")
	}
	done := cals[1].(map[string]any)
	assertKeys(t, "calibrations[done]", keysOf(t, done),
		[]string{"created_time", "job_id", "per_pod_qps", "phase", "saturated_by", "scenario_id", "scenario_name"})
	if done["job_id"] != float64(doneID) || done["phase"] != "done" || done["scenario_name"] != "checkout" ||
		done["saturated_by"] != "engine" || done["per_pod_qps"] != 9.5 {
		t.Errorf("calibrations[done] = %v, want the finished search's verdict", done)
	}
	if _, present := done["failure_reason"]; present {
		t.Error("calibrations[done] carries failure_reason, want it absent for a search that concluded cleanly")
	}
}
