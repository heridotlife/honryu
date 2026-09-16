package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/scenarioapp"
	"github.com/heridotlife/honryu/internal/app/thresholdapp"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/domain/threshold"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// newThresholdsRouter wires the threshold endpoints over fakes, with one
// scenario seeded, and returns the router and the scenario's id.
func newThresholdsRouter(t *testing.T) (http.Handler, int64) {
	t.Helper()
	store := fake.NewStore()
	h := httpapi.NewRouter(httpapi.Deps{
		Projects:      projectapp.NewService(store),
		Scenarios:     scenarioapp.NewService(store, fake.NewObjectStore()),
		Thresholds:    thresholdapp.NewService(store),
		DefaultOwners: []string{"honryu"},
	})
	projectID := decodeID(t, postForm(t, h, "/api/projects", url.Values{"name": {"web"}, "owner": {"honryu"}}))
	scenarioID := decodeID(t, postForm(t, h, "/api/scenarios", url.Values{"name": {"alpha"}, "project_id": {itoa(projectID)}}))
	return h, scenarioID
}

// thresholdRow is the wire shape the tests pin: the stored definition, ids
// included (the editor syncs on them).
type thresholdRow struct {
	ID         int64   `json:"id"`
	ScenarioID int64   `json:"scenario_id"`
	Metric     string  `json:"metric"`
	Comparison string  `json:"comparison"`
	Value      float64 `json:"value"`
}

func decodeThresholdRows(t *testing.T, body []byte) []thresholdRow {
	t.Helper()
	var out []thresholdRow
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode thresholds: %v (%s)", err, string(body))
	}
	return out
}

// putThresholds issues a replace-all PUT with the given rows.
func putThresholds(t *testing.T, h http.Handler, scenarioID int64, rows string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/scenarios/"+itoa(scenarioID)+"/thresholds", strings.NewReader(rows))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The GET/PUT round trip: PUT stores the list with assigned ids and GET
// reads it back, oldest first -- always an array.
func TestThresholds_PutThenGetRoundTrip(t *testing.T) {
	t.Parallel()
	h, scenarioID := newThresholdsRouter(t)

	rec := putThresholds(t, h, scenarioID, `{"thresholds":[
		{"metric":"http_p95_ms","comparison":"lt","value":300},
		{"metric":"throughput_qps","comparison":"gt","value":50}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d (%s)", rec.Code, rec.Body.String())
	}
	stored := decodeThresholdRows(t, rec.Body.Bytes())
	if len(stored) != 2 || stored[0].ID <= 0 || stored[1].ID <= 0 || stored[0].ID == stored[1].ID {
		t.Fatalf("PUT response = %+v, want two rows with distinct storage ids", stored)
	}
	if stored[0].ScenarioID != scenarioID || stored[0].Metric != "http_p95_ms" || stored[0].Comparison != "lt" || stored[0].Value != 300 {
		t.Errorf("row 0 = %+v, want the p95 ceiling under the scenario", stored[0])
	}

	rec = do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID)+"/thresholds")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d (%s)", rec.Code, rec.Body.String())
	}
	got := decodeThresholdRows(t, rec.Body.Bytes())
	if len(got) != 2 || got[0].ID != stored[0].ID || got[1].ID != stored[1].ID {
		t.Errorf("GET = %+v, want the stored set in definition order", got)
	}
}

// An unknown scenario is the usual 404, on both routes.
func TestThresholds_UnknownScenarioIs404(t *testing.T) {
	t.Parallel()
	h, _ := newThresholdsRouter(t)
	if rec := do(t, h, http.MethodGet, "/api/scenarios/999/thresholds"); rec.Code != http.StatusNotFound {
		t.Errorf("GET = %d, want 404", rec.Code)
	}
	if rec := putThresholds(t, h, 999, `{"thresholds":[]}`); rec.Code != http.StatusNotFound {
		t.Errorf("PUT = %d, want 404", rec.Code)
	}
}

// A definition outside a metric's legal range is a 400 carrying the domain's
// message, and the stored set is untouched.
func TestThresholds_InvalidDefinitionIs400(t *testing.T) {
	t.Parallel()
	h, scenarioID := newThresholdsRouter(t)
	if rec := putThresholds(t, h, scenarioID, `{"thresholds":[
		{"metric":"http_p95_ms","comparison":"lt","value":300},
		{"metric":"error_rate","comparison":"lt","value":2}]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT = %d (%s), want 400", rec.Code, rec.Body.String())
	}
	got := decodeThresholdRows(t, do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID)+"/thresholds").Body.Bytes())
	if len(got) != 0 {
		t.Errorf("list after rejected PUT = %+v, want the untouched empty set", got)
	}
}

// A scenario with no thresholds answers an empty array, never null -- the
// client treats the list as a list.
func TestThresholds_EmptyIsArray(t *testing.T) {
	t.Parallel()
	h, scenarioID := newThresholdsRouter(t)
	rec := do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID)+"/thresholds")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d", rec.Code)
	}
	if got := rec.Body.String(); got == "null" || got == "" {
		t.Fatalf("GET body = %q, want []", got)
	}
	if rows := decodeThresholdRows(t, rec.Body.Bytes()); len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
}

// --- the run-report overlay (phase 72) --------------------------------------

// reportWithThresholds wires the report store and the threshold service the
// report overlay reads through, and seeds one scenario; its id is returned --
// the graded report is built against it.
func reportWithThresholds(t *testing.T) (http.Handler, *thresholdapp.Service, *fake.ReportStore, int64) {
	t.Helper()
	store := fake.NewStore()
	reports := fake.NewReportStore()
	svc := thresholdapp.NewService(store)
	h := httpapi.NewRouter(httpapi.Deps{
		Reports:       reports,
		Thresholds:    svc,
		Store:         fake.NewObjectStore(),
		DefaultOwners: []string{"honryu"},
	})
	sc, _ := scenario.New("thresholded", 1)
	scenarioID, err := store.CreateScenario(context.Background(), sc)
	if err != nil {
		t.Fatalf("seed scenario: %v", err)
	}
	return h, svc, reports, scenarioID
}

// thresholdResultRow is the overlay's wire shape the tests pin.
type thresholdResultRow struct {
	ThresholdID   int64    `json:"threshold_id"`
	Metric        string   `json:"metric"`
	Comparison    string   `json:"comparison"`
	Value         float64  `json:"value"`
	ObservedValue *float64 `json:"observed_value"`
	Satisfied     *bool    `json:"satisfied"`
	Reason        string   `json:"reason"`
}

// A run's report graded at finalisation time carries threshold_results on
// GET /api/runs/{run_id}/report: met, missed, and unknown rows in definition
// order, observed in the metric's own unit, and the run's own outcome
// untouched (thresholds are additive evidence, never the verdict).
func TestRunReport_CarriesThresholdResults(t *testing.T) {
	t.Parallel()
	h, svc, reports, scenarioID := reportWithThresholds(t)
	ctx := context.Background()

	if _, err := svc.Replace(ctx, scenarioID, []threshold.Threshold{
		{ScenarioID: scenarioID, Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: 300},
		{ScenarioID: scenarioID, Metric: threshold.MetricThroughputQPS, Comparison: threshold.ComparisonGT, Value: 50},
		{ScenarioID: scenarioID, Metric: threshold.MetricHTTPP99MS, Comparison: threshold.ComparisonLT, Value: 500},
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	// Rows expect missed, met, unknown: p95 observed at 500ms vs a 300ms
	// ceiling; throughput 80 vs a 50 floor; and a report that never
	// measured p99 -- a percentile missing from the stored map, which a
	// partial or legacy report can absolutely be.
	rep := report.Report{
		ExecutionID: 1, RunID: 42, ScenarioID: scenarioID,
		StartedAt: time.Unix(1000, 0).UTC(), EndedAt: time.Unix(1030, 0).UTC(),
		Outcome:   taurus.OutcomePassed,
		ErrorRate: 0.3,
		Latency:   report.Percentiles{95: 0.5},
		Achieved:  report.Load{Throughput: 80},
	}
	if err := reports.SaveReport(ctx, rep); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	if err := svc.EvaluateRun(ctx, rep); err != nil {
		t.Fatalf("EvaluateRun: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/runs/42/report")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET report = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Outcome          string               `json:"outcome"`
		ThresholdResults []thresholdResultRow `json:"threshold_results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The engine's verdict is untouched by the grading.
	if got.Outcome != "passed" {
		t.Errorf("outcome = %q, want passed (thresholds never rewrite the verdict)", got.Outcome)
	}
	if len(got.ThresholdResults) != 3 {
		t.Fatalf("threshold_results = %+v, want three rows", got.ThresholdResults)
	}
	p95 := got.ThresholdResults[0]
	if p95.Metric != "http_p95_ms" || p95.Comparison != "lt" || p95.Value != 300 {
		t.Errorf("p95 row = %+v, want the definition snapshot", p95)
	}
	if p95.ObservedValue == nil || *p95.ObservedValue != 500 {
		t.Errorf("p95 observed = %v, want 500ms (seconds crossed once)", p95.ObservedValue)
	}
	if p95.Satisfied == nil || *p95.Satisfied {
		t.Errorf("p95 satisfied = %v, want false", p95.Satisfied)
	}
	errRow := got.ThresholdResults[1]
	if errRow.Metric != "throughput_qps" {
		t.Fatalf("row 1 = %+v, want the throughput floor", errRow)
	}
	if errRow.ObservedValue == nil || *errRow.ObservedValue != 80 || errRow.Satisfied == nil || !*errRow.Satisfied {
		t.Errorf("throughput row = %+v, want observed 80 met", errRow)
	}
	unknown := got.ThresholdResults[2]
	if unknown.ObservedValue != nil || unknown.Satisfied != nil {
		t.Errorf("p99 row = %+v, want unknown (null observed and satisfied)", unknown)
	}
	if unknown.Reason == "" {
		t.Error("p99 row carries no reason; the reader is owed why it is unknown")
	}
}

// A run whose scenario defines no thresholds -- or that predates the feature
// -- carries an empty array, never null.
func TestRunReport_NoThresholdsIsEmptyArray(t *testing.T) {
	t.Parallel()
	h, _, reports, _ := reportWithThresholds(t)
	if err := reports.SaveReport(context.Background(), sampleReport(1, 42)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	rec := do(t, h, http.MethodGet, "/api/runs/42/report")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET report = %d", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, `"threshold_results":null`) {
		t.Fatalf("threshold_results is null on the wire: %s", body)
	}
	var got struct {
		ThresholdResults []thresholdResultRow `json:"threshold_results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ThresholdResults == nil || len(got.ThresholdResults) != 0 {
		t.Errorf("threshold_results = %#v, want an empty array", got.ThresholdResults)
	}
}

// --- phase-72 pins: replace-all semantics on the wire ------------------------

// PUT is idempotent end to end: saving the same list twice leaves the same
// state, down to the row ids -- a no-change editor save must not churn ids
// (and so must not orphan the results already recorded against them).
func TestThresholds_PutSameListTwiceIsTheSameState(t *testing.T) {
	t.Parallel()
	h, scenarioID := newThresholdsRouter(t)
	body := `{"thresholds":[
		{"metric":"http_p95_ms","comparison":"lt","value":300},
		{"metric":"error_rate","comparison":"lt","value":0.01}]}`
	if rec := putThresholds(t, h, scenarioID, body); rec.Code != http.StatusOK {
		t.Fatalf("first PUT = %d (%s)", rec.Code, rec.Body.String())
	}
	first := decodeThresholdRows(t, do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID)+"/thresholds").Body.Bytes())
	if rec := putThresholds(t, h, scenarioID, body); rec.Code != http.StatusOK {
		t.Fatalf("second PUT = %d (%s)", rec.Code, rec.Body.String())
	}
	second := decodeThresholdRows(t, do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID)+"/thresholds").Body.Bytes())
	if len(first) != len(second) {
		t.Fatalf("state changed size: %d rows -> %d rows", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("row %d changed across the re-save: %+v -> %+v (ids and values must hold)", i, first[i], second[i])
		}
	}
}

// PUT with an empty list clears the scenario's set -- the replace-all
// contract's clearing half. The response is the empty stored set.
func TestThresholds_EmptyPutClears(t *testing.T) {
	t.Parallel()
	h, scenarioID := newThresholdsRouter(t)
	if rec := putThresholds(t, h, scenarioID, `{"thresholds":[
		{"metric":"http_p95_ms","comparison":"lt","value":300},
		{"metric":"throughput_qps","comparison":"gt","value":50}]}`); rec.Code != http.StatusOK {
		t.Fatalf("seed PUT = %d (%s)", rec.Code, rec.Body.String())
	}
	rec := putThresholds(t, h, scenarioID, `{"thresholds":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clearing PUT = %d (%s)", rec.Code, rec.Body.String())
	}
	if rows := decodeThresholdRows(t, rec.Body.Bytes()); len(rows) != 0 {
		t.Errorf("clearing PUT response = %+v, want empty", rows)
	}
	got := decodeThresholdRows(t, do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID)+"/thresholds").Body.Bytes())
	if len(got) != 0 {
		t.Errorf("GET after clearing PUT = %+v, want none", got)
	}
}

// GET includes the threshold ids the editor syncs on and the results
// correlate against: every PUT response row's id shows up on the GET.
func TestThresholds_GetIncludesThresholdIds(t *testing.T) {
	t.Parallel()
	h, scenarioID := newThresholdsRouter(t)
	stored := decodeThresholdRows(t, putThresholds(t, h, scenarioID, `{"thresholds":[
		{"metric":"http_p95_ms","comparison":"lt","value":300},
		{"metric":"http_p99_ms","comparison":"lt","value":500}]}`).Body.Bytes())
	if len(stored) != 2 || stored[0].ID <= 0 || stored[1].ID <= 0 {
		t.Fatalf("PUT response rows carry no usable ids: %+v", stored)
	}
	got := decodeThresholdRows(t, do(t, h, http.MethodGet, "/api/scenarios/"+itoa(scenarioID)+"/thresholds").Body.Bytes())
	ids := make(map[int64]bool, len(got))
	for _, row := range got {
		ids[row.ID] = true
	}
	for _, want := range stored {
		if !ids[want.ID] {
			t.Errorf("GET is missing id %d from the stored set: %+v", want.ID, got)
		}
	}
}
