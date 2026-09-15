package digestapp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/app/digestapp"
	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/digest"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// seedScenario defines one scenario under projectID.
func seedScenario(t *testing.T, store *fake.Store, name string, projectID int64) int64 {
	t.Helper()
	sc, err := scenario.New(name, projectID)
	if err != nil {
		t.Fatalf("scenario.New(%s): %v", name, err)
	}
	id, err := store.CreateScenario(context.Background(), sc)
	if err != nil {
		t.Fatalf("CreateScenario(%s): %v", name, err)
	}
	return id
}

// seedCalibrationJob creates a job at the store clock's current time --
// SetNow first to place it in (or out of) the digest window.
func seedCalibrationJob(t *testing.T, store *fake.Store, executionID, scenarioID int64) int64 {
	t.Helper()
	id, err := store.CreateCalibrationJob(context.Background(), executionID, scenarioID)
	if err != nil {
		t.Fatalf("CreateCalibrationJob(exec %d): %v", executionID, err)
	}
	return id
}

// finishJob drives jobID to a terminal done search via the same
// derive-from-persisted shape the real caller records.
func finishJob(t *testing.T, store *fake.Store, jobID int64, qps float64, by calibration.SaturatedBy) {
	t.Helper()
	ctx := context.Background()
	persisted, err := store.GetCalibrationJob(ctx, jobID)
	if err != nil {
		t.Fatalf("GetCalibrationJob(%d): %v", jobID, err)
	}
	finished := persisted
	finished.Phase = calibration.PhaseDone
	finished.StepCount = 1
	finished.Result = &calibration.Result{SaturatedBy: by, PerPodQPS: qps}
	if err := store.RecordStep(ctx, jobID,
		calibration.Step{RequestedQPS: 10, AchievedQPS: qps, Classification: calibration.ClassificationClean}, finished); err != nil {
		t.Fatalf("RecordStep(%d): %v", jobID, err)
	}
}

// The window's calibration searches join the digest (phase 71): finished
// searches carry per_pod_qps and saturated_by, failed searches carry
// failure_reason (and no result fields -- omitempty keeps the absent ones
// off the wire), pending ones neither; a job surfaces even when its
// scenario produced no runs at all (a failed calibration IS digest-worthy);
// other projects' jobs never leak in; jobs outside the window are the
// neighbours' digests' business.
func TestBuildDigestCarriesCalibrations(t *testing.T) {
	f := newFixture(t)
	start, end := window()

	checkoutA := seedScenario(t, f.store, "checkout", f.projA)
	checkoutB := seedScenario(t, f.store, "search", f.projB)
	execCalA := mkExecution(t, f.store, "calib-engine", f.projA)

	// Three jobs under project A, created oldest-first so the payload's
	// newest-first order is observable: pending, done, failed.
	store := f.store
	store.SetNow(func() time.Time { return start.Add(2 * time.Hour) })
	pendingID := seedCalibrationJob(t, store, execCalA, checkoutA)
	store.SetNow(func() time.Time { return start.Add(3 * time.Hour) })
	doneID := seedCalibrationJob(t, store, execCalA, checkoutA)
	finishJob(t, store, doneID, 9.5, calibration.SaturatedByNeither)
	// The failed job's execution has no runs in (or out of) the window at
	// all: a job is digest-worthy by its own existence, not its scenario's
	// run count.
	execNoRuns := mkExecution(t, f.store, "never-ran", f.projA)
	store.SetNow(func() time.Time { return start.Add(4 * time.Hour) })
	failedID := seedCalibrationJob(t, store, execNoRuns, checkoutA)
	if err := store.MarkFailed(context.Background(), failedID, "step deploy failed"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	// Project B's own search: never project A's business.
	execCalB := mkExecution(t, f.store, "calib-b", f.projB)
	store.SetNow(func() time.Time { return start.Add(5 * time.Hour) })
	seedCalibrationJob(t, store, execCalB, checkoutB)
	// Out of window: created before the digest's start -- the previous
	// digest's business.
	store.SetNow(func() time.Time { return start.Add(-time.Hour) })
	seedCalibrationJob(t, store, execCalA, checkoutA)
	store.SetNow(time.Now) // leave the clock as we found it

	d, err := f.svc.BuildDigest(context.Background(), f.projA, digest.PeriodDaily, start, end)
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	p, err := digestapp.DecodePayload(d.Payload)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if len(p.Calibrations) != 3 {
		t.Fatalf("calibrations = %+v, want exactly project A's 3 in-window jobs", p.Calibrations)
	}
	// Newest first: failed, then done, then pending.
	if p.Calibrations[0].JobID != failedID || p.Calibrations[1].JobID != doneID || p.Calibrations[2].JobID != pendingID {
		t.Fatalf("calibration order = [%d %d %d], want newest-first [%d %d %d]",
			p.Calibrations[0].JobID, p.Calibrations[1].JobID, p.Calibrations[2].JobID, failedID, doneID, pendingID)
	}

	failed := p.Calibrations[0]
	if failed.Phase != "failed" || failed.FailureReason != "step deploy failed" {
		t.Errorf("failed line = %+v, want phase failed with the failure reason carried", failed)
	}
	if failed.PerPodQPS != nil || failed.SaturatedBy != "" {
		t.Errorf("failed line result fields = (%v, %q), want nil/empty -- a failed search never concluded", failed.PerPodQPS, failed.SaturatedBy)
	}
	if failed.ScenarioID != checkoutA || failed.ScenarioName != "checkout" {
		t.Errorf("failed line scenario = (%d, %q), want the joined (%d, \"checkout\")", failed.ScenarioID, failed.ScenarioName, checkoutA)
	}

	done := p.Calibrations[1]
	if done.Phase != "done" || done.PerPodQPS == nil || *done.PerPodQPS != 9.5 || done.SaturatedBy != "neither" {
		t.Errorf("done line = %+v, want phase done with the terminal verdict carried", done)
	}

	pending := p.Calibrations[2]
	if pending.Phase != "pending" || pending.PerPodQPS != nil || pending.SaturatedBy != "" || pending.FailureReason != "" {
		t.Errorf("pending line = %+v, want a bare pending search", pending)
	}

	// Field-presence law on the raw wire bytes: per_pod_qps is nullable
	// (null until done), saturated_by/failure_reason are absent until set.
	var payloadMap struct {
		Calibrations []map[string]any `json:"calibrations"`
	}
	if err := json.Unmarshal(d.Payload, &payloadMap); err != nil {
		t.Fatalf("decode calibrations: %v", err)
	}
	lines := payloadMap.Calibrations
	if _, ok := lines[0]["per_pod_qps"]; !ok {
		t.Error("failed line omits per_pod_qps entirely, want an explicit null -- the key is part of the shape")
	}
	if _, ok := lines[0]["saturated_by"]; ok {
		t.Error("failed line carries saturated_by, want it absent until a search concludes")
	}
	if v, ok := lines[1]["per_pod_qps"].(float64); !ok || v != 9.5 {
		t.Errorf("done line per_pod_qps = %v, want 9.5", lines[1]["per_pod_qps"])
	}
	if _, ok := lines[2]["failure_reason"]; ok {
		t.Error("pending line carries failure_reason, want it absent")
	}
	if strings.Contains(string(d.Payload), `"calibrations":null`) {
		t.Error("payload serialized calibrations as null, want an array")
	}
}

// An empty window (or a project with no calibrations at all) must
// serialize calibrations as an empty array, never null -- the same law
// slo_budgets obeys, so a receiver renders "none", not noughts.
func TestBuildDigestCalibrationsEmptyWindowIsEmptyArray(t *testing.T) {
	f := newFixture(t)
	start, end := window()

	d, err := f.svc.BuildDigest(context.Background(), f.projA, digest.PeriodDaily, start, end)
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	if !strings.Contains(string(d.Payload), `"calibrations":[]`) {
		t.Fatalf("payload = %s, want literal \"calibrations\":[]", d.Payload)
	}
	p, err := digestapp.DecodePayload(d.Payload)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if p.Calibrations == nil || len(p.Calibrations) != 0 {
		t.Fatalf("calibrations = %#v, want empty non-nil", p.Calibrations)
	}
}
