package httpapi_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

func newReportEnv(t *testing.T) (http.Handler, *fake.ReportStore, *fake.ObjectStore) {
	t.Helper()
	reports := fake.NewReportStore()
	obj := fake.NewObjectStore()
	h := httpapi.NewRouter(httpapi.Deps{Reports: reports, Store: obj, DefaultOwners: []string{"honryu"}})
	return h, reports, obj
}

func sampleReport(executionID, runID int64) report.Report {
	return report.Build(report.Input{
		ExecutionID: executionID, RunID: runID,
		Engine:    taurus.ExecutorJMeter,
		StartedAt: time.Unix(1000, 0).UTC(),
		EndedAt:   time.Unix(1030, 0).UTC(),
		Outcome:   taurus.OutcomeFailed,
		Requested: report.Load{Concurrency: 10, DurationSeconds: 30},
		Intervals: []metrics.Interval{{
			Timestamp: 1000, Label: "checkout", Samples: 10, Failed: 3, Succeeded: 7,
			Latency:       metrics.Histogram{0.01: 7, 0.5: 3},
			ResponseCodes: map[string]int64{"200": 7, "404": 3},
			Errors:        []metrics.ErrorGroup{{Message: "Not Found", ResponseCode: "404", Count: 3}},
		}},
	})
}

func TestReportHTTP_FetchesAStoredReport(t *testing.T) {
	t.Parallel()
	h, reports, _ := newReportEnv(t)
	want := sampleReport(1, 42)
	if err := reports.SaveReport(context.Background(), want); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/runs/42/report")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET report = %d (%s)", rec.Code, rec.Body.String())
	}
	var got report.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RunID != 42 || got.Outcome != taurus.OutcomeFailed {
		t.Errorf("report = %+v", got)
	}
	if len(got.Errors) != 1 || got.Errors[0].Count != 3 {
		t.Errorf("errors = %+v, want one signature counted 3", got.Errors)
	}
	// The per-label status breakdown rides the same wire, dominant first.
	wantStatuses := []report.StatusLabel{{Code: "200", Count: 7}, {Code: "404", Count: 3}}
	if len(got.Labels) != 1 || !reflect.DeepEqual(got.Labels[0].Statuses, wantStatuses) {
		t.Errorf("label statuses = %+v, want %+v", got.Labels, wantStatuses)
	}
}

// newCriteriaReportEnv wires the execution service alongside the report
// store -- the deployment shape the run report's Phase 29 verdict layer reads
// criteria from.
func newCriteriaReportEnv(t *testing.T) (http.Handler, *fake.ReportStore, *fake.Store) {
	t.Helper()
	reports := fake.NewReportStore()
	store := fake.NewStore()
	obj := fake.NewObjectStore()
	h := httpapi.NewRouter(httpapi.Deps{
		Reports: reports, Store: obj,
		Executions:    executionapp.NewService(store, obj, 100),
		DefaultOwners: []string{"honryu"},
	})
	return h, reports, store
}

// verdictShape decodes the Phase 29 run-report wire: the embedded domain
// report plus the two verdict arrays.
type verdictShape struct {
	report.Report
	Criteria        []string `json:"criteria"`
	FailingCriteria []struct {
		Criterion string `json:"criterion"`
		Unparsed  bool   `json:"unparsed"`
	} `json:"failing_criteria"`
}

// A configured criterion the report's own measurements trip is named in
// failing_criteria; one that passes is absent; one outside the grammar is
// named as unparsed, never silently dropped.
func TestReportHTTP_ReportCarriesCriteriaVerdict(t *testing.T) {
	t.Parallel()
	h, reports, store := newCriteriaReportEnv(t)
	ctx := context.Background()
	// 30% failures, p95 at 0.7s: "failures>50%" passes, "p95>500ms" trips,
	// and the "for" window is outside the grammar the evaluator understands.
	rep := report.Build(report.Input{
		ExecutionID: 1, RunID: 42,
		Engine:    taurus.ExecutorJMeter,
		StartedAt: time.Unix(1000, 0).UTC(),
		EndedAt:   time.Unix(1030, 0).UTC(),
		Outcome:   taurus.OutcomeFailed,
		Requested: report.Load{Concurrency: 10, DurationSeconds: 30},
		Intervals: []metrics.Interval{{
			Timestamp: 1000, Label: "checkout", Samples: 10, Failed: 3, Succeeded: 7,
			Latency: metrics.Histogram{0.01: 7, 0.7: 3},
			Errors:  []metrics.ErrorGroup{{Message: "Not Found", ResponseCode: "404", Count: 3}},
		}},
	})
	if err := reports.SaveReport(ctx, rep); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	if err := store.SetExecutionCriteria(ctx, 1, []string{"failures>50%", "p95>500ms", "p99<1s for 5s"}); err != nil {
		t.Fatalf("SetExecutionCriteria: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/runs/42/report")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET report = %d (%s)", rec.Code, rec.Body.String())
	}
	var got verdictShape
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The report itself stays verbatim -- the verdict is layered on, never a
	// rewrite.
	if got.RunID != 42 || got.Outcome != taurus.OutcomeFailed || len(got.Errors) != 1 {
		t.Errorf("report fields = run %d outcome %s errors %d, want the stored report verbatim", got.RunID, got.Outcome, len(got.Errors))
	}
	wantCriteria := []string{"failures>50%", "p95>500ms", "p99<1s for 5s"}
	if len(got.Criteria) != len(wantCriteria) {
		t.Fatalf("criteria = %v, want %v", got.Criteria, wantCriteria)
	}
	for i, w := range wantCriteria {
		if got.Criteria[i] != w {
			t.Fatalf("criteria = %v, want %v in order", got.Criteria, wantCriteria)
		}
	}
	if len(got.FailingCriteria) != 2 {
		t.Fatalf("failing_criteria = %+v, want the tripped p95 and the unparsed window clause", got.FailingCriteria)
	}
	if got.FailingCriteria[0].Criterion != "p95>500ms" || got.FailingCriteria[0].Unparsed {
		t.Errorf("failing_criteria[0] = %+v, want p95>500ms parsed and tripped", got.FailingCriteria[0])
	}
	if got.FailingCriteria[1].Criterion != "p99<1s for 5s" || !got.FailingCriteria[1].Unparsed {
		t.Errorf("failing_criteria[1] = %+v, want the for-window clause reported unparsed", got.FailingCriteria[1])
	}
}

// No criteria configured: both verdict keys answer empty -- the report
// itself is untouched.
func TestReportHTTP_NoConfiguredCriteriaYieldEmptyVerdicts(t *testing.T) {
	t.Parallel()
	h, reports, _ := newCriteriaReportEnv(t)
	if err := reports.SaveReport(context.Background(), sampleReport(1, 42)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/runs/42/report")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET report = %d (%s)", rec.Code, rec.Body.String())
	}
	var got verdictShape
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Criteria) != 0 || len(got.FailingCriteria) != 0 {
		t.Errorf("verdict = criteria %v failing %+v, want both empty", got.Criteria, got.FailingCriteria)
	}
	if got.RunID != 42 {
		t.Errorf("run_id = %d, want 42", got.RunID)
	}
}

// criteriaFailStore forces the criteria read to fail while everything else
// stays the embedded fake's -- the verdict layer is additive, so a criteria
// store failure must never fail the report read.
type criteriaFailStore struct {
	*fake.Store
}

func (criteriaFailStore) CriteriaFor(context.Context, int64) ([]string, error) { return nil, errBoom }

func TestReportHTTP_CriteriaReadFailureStillServesTheReport(t *testing.T) {
	t.Parallel()
	reports := fake.NewReportStore()
	store := &criteriaFailStore{fake.NewStore()}
	obj := fake.NewObjectStore()
	h := httpapi.NewRouter(httpapi.Deps{
		Reports: reports, Store: obj,
		Executions:    executionapp.NewService(store, obj, 100),
		DefaultOwners: []string{"honryu"},
	})
	if err := reports.SaveReport(context.Background(), sampleReport(1, 42)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/runs/42/report")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET report = %d (%s), want 200 despite the criteria read failing", rec.Code, rec.Body.String())
	}
	var got verdictShape
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Criteria) != 0 || len(got.FailingCriteria) != 0 {
		t.Errorf("verdict = criteria %v failing %+v, want both empty when the read failed", got.Criteria, got.FailingCriteria)
	}
	if got.RunID != 42 || got.Outcome != taurus.OutcomeFailed {
		t.Errorf("report = %+v, want the stored report served verbatim", got.Report)
	}
}

// A report-only deployment wires no execution service: the report still
// serves, with empty verdicts -- the pre-Phase-29 behaviour plus two empty
// keys.
func TestReportHTTP_NoExecutionServiceStillServesTheReport(t *testing.T) {
	t.Parallel()
	h, reports, _ := newReportEnv(t)
	if err := reports.SaveReport(context.Background(), sampleReport(1, 42)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/runs/42/report")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET report = %d (%s)", rec.Code, rec.Body.String())
	}
	var got verdictShape
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Criteria) != 0 || len(got.FailingCriteria) != 0 {
		t.Errorf("verdict = criteria %v failing %+v, want both empty with no execution service", got.Criteria, got.FailingCriteria)
	}
	if got.RunID != 42 {
		t.Errorf("run_id = %d, want 42", got.RunID)
	}
}

func TestReportHTTP_UnknownRunIs404(t *testing.T) {
	t.Parallel()
	h, _, _ := newReportEnv(t)

	rec := do(t, h, http.MethodGet, "/api/runs/999/report")
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET unknown report = %d, want 404", rec.Code)
	}
	// Phase 24 regression: the details envelope is strictly opt-in -- a
	// message-only 404 must stay byte-identical to the pre-envelope shape.
	if got := strings.TrimSpace(rec.Body.String()); got != `{"message":"ports: not found"}` {
		t.Errorf("GET unknown report body = %s, want the message-only envelope", got)
	}
}

func TestReportHTTP_ListsAnExecutionsReportsMostRecentFirst(t *testing.T) {
	t.Parallel()
	h, reports, _ := newReportEnv(t)
	ctx := context.Background()

	older := sampleReport(7, 1)
	older.StartedAt = time.Unix(1000, 0).UTC()
	newer := sampleReport(7, 2)
	newer.StartedAt = time.Unix(2000, 0).UTC()
	if err := reports.SaveReport(ctx, older); err != nil {
		t.Fatalf("SaveReport(older): %v", err)
	}
	if err := reports.SaveReport(ctx, newer); err != nil {
		t.Fatalf("SaveReport(newer): %v", err)
	}
	// A different execution's report must not appear.
	if err := reports.SaveReport(ctx, sampleReport(9, 3)); err != nil {
		t.Fatalf("SaveReport(other execution): %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/executions/7/reports")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET reports = %d (%s)", rec.Code, rec.Body.String())
	}
	var got []report.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("reports = %d, want 2", len(got))
	}
	if got[0].RunID != 2 || got[1].RunID != 1 {
		t.Fatalf("reports = %+v, want run 2 before run 1", got)
	}
}

func TestReportHTTP_ListLimitIsRespected(t *testing.T) {
	t.Parallel()
	h, reports, _ := newReportEnv(t)
	ctx := context.Background()
	for i, ts := range []int64{1000, 2000, 3000} {
		r := sampleReport(7, int64(i+1))
		r.StartedAt = time.Unix(ts, 0).UTC()
		if err := reports.SaveReport(ctx, r); err != nil {
			t.Fatalf("SaveReport: %v", err)
		}
	}

	rec := do(t, h, http.MethodGet, "/api/executions/7/reports?limit=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET reports = %d", rec.Code)
	}
	var got []report.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].RunID != 3 {
		t.Fatalf("reports = %+v, want only the most recent (run 3)", got)
	}
}

// A malformed limit degrades to "no limit" rather than rejecting an otherwise
// valid request over a hint.
func TestReportHTTP_MalformedLimitIsIgnored(t *testing.T) {
	t.Parallel()
	h, reports, _ := newReportEnv(t)
	if err := reports.SaveReport(context.Background(), sampleReport(7, 1)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/executions/7/reports?limit=not-a-number")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET reports = %d", rec.Code)
	}
	var got []report.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("reports = %+v, want the one report despite the bad limit", got)
	}
}

func TestExecutionTrendHTTP_EmptyExecution(t *testing.T) {
	t.Parallel()
	h, _, _ := newReportEnv(t)

	rec := do(t, h, http.MethodGet, "/api/executions/7/trend")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET trend = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		ExecutionID int64         `json:"execution_id"`
		Points      []interface{} `json:"points"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ExecutionID != 7 || len(got.Points) != 0 {
		t.Fatalf("trend = %+v, want execution_id 7 and no points", got)
	}
}

// End to end: two comparable runs, the newer one short of its target QPS
// while the older hit it -- the trend surfaces regressed:true on the newer
// point, most-recent-first.
func TestExecutionTrendHTTP_FlagsRegression(t *testing.T) {
	t.Parallel()
	h, reports, _ := newReportEnv(t)
	ctx := context.Background()

	older := report.Report{
		ExecutionID: 7, RunID: 1, Engine: taurus.ExecutorJMeter,
		StartedAt: time.Unix(1000, 0).UTC(), Outcome: taurus.OutcomePassed,
		Requested: report.Load{Concurrency: 10, Throughput: 100, DurationSeconds: 60},
		Achieved:  report.Load{Throughput: 100},
	}
	newer := report.Report{
		ExecutionID: 7, RunID: 2, Engine: taurus.ExecutorJMeter,
		StartedAt: time.Unix(2000, 0).UTC(), Outcome: taurus.OutcomePassed,
		Requested: report.Load{Concurrency: 10, Throughput: 100, DurationSeconds: 60},
		Achieved:  report.Load{Throughput: 50},
	}
	if err := reports.SaveReport(ctx, older); err != nil {
		t.Fatalf("SaveReport(older): %v", err)
	}
	if err := reports.SaveReport(ctx, newer); err != nil {
		t.Fatalf("SaveReport(newer): %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/executions/7/trend")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET trend = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		ExecutionID int64 `json:"execution_id"`
		Points      []struct {
			RunID                    int64   `json:"run_id"`
			AchievedThroughput       float64 `json:"achieved_throughput"`
			HitTargetQPS             bool    `json:"hit_target_qps"`
			HasComparablePredecessor bool    `json:"has_comparable_predecessor"`
			Regressed                bool    `json:"regressed"`
		} `json:"points"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.ExecutionID != 7 || len(got.Points) != 2 {
		t.Fatalf("trend = %+v, want execution_id 7 and 2 points", got)
	}
	// Most recent first: run 2 (newer, regressed) before run 1 (older, baseline).
	if got.Points[0].RunID != 2 || got.Points[0].HitTargetQPS || !got.Points[0].HasComparablePredecessor || !got.Points[0].Regressed {
		t.Fatalf("newer point = %+v, want run 2, missed target, comparable predecessor, regressed", got.Points[0])
	}
	if got.Points[1].RunID != 1 || !got.Points[1].HitTargetQPS {
		t.Fatalf("older point = %+v, want run 1, hit target", got.Points[1])
	}
}

func TestExecutionTrendHTTP_LimitIsRespected(t *testing.T) {
	t.Parallel()
	h, reports, _ := newReportEnv(t)
	ctx := context.Background()
	for i, ts := range []int64{1000, 2000, 3000} {
		r := report.Report{ExecutionID: 7, RunID: int64(i + 1), StartedAt: time.Unix(ts, 0).UTC(), Outcome: taurus.OutcomePassed}
		if err := reports.SaveReport(ctx, r); err != nil {
			t.Fatalf("SaveReport: %v", err)
		}
	}

	rec := do(t, h, http.MethodGet, "/api/executions/7/trend?limit=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET trend = %d", rec.Code)
	}
	var got struct {
		Points []struct {
			RunID int64 `json:"run_id"`
		} `json:"points"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Points) != 1 || got.Points[0].RunID != 3 {
		t.Fatalf("trend points = %+v, want only the most recent (run 3)", got.Points)
	}
}

func TestExecutionTrendHTTP_InvalidExecutionIDIsBadRequest(t *testing.T) {
	t.Parallel()
	h, _, _ := newReportEnv(t)
	if rec := do(t, h, http.MethodGet, "/api/executions/not-a-number/trend"); rec.Code != http.StatusBadRequest {
		t.Fatalf("GET trend (invalid id) = %d, want 400", rec.Code)
	}
}

func withError(executionID, runID int64, at time.Time, label, code string, side report.Side, count int64) report.Report {
	return report.Report{
		ExecutionID: executionID, RunID: runID, StartedAt: at, Outcome: taurus.OutcomeFailed,
		Errors: []report.ErrorSignature{{Signature: report.Signature{Label: label, ResponseCode: code, Side: side}, Count: count}},
	}
}

func TestErrorSignatureHistoryHTTP_DefaultGroupsByLabel(t *testing.T) {
	t.Parallel()
	h, reports, _ := newReportEnv(t)
	ctx := context.Background()
	if err := reports.SaveReport(ctx, withError(7, 1, time.Unix(1000, 0), "checkout", "500", report.SideTarget, 3)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	if err := reports.SaveReport(ctx, withError(7, 2, time.Unix(2000, 0), "checkout", "404", report.SideTarget, 1)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	// A different execution's signature must not appear.
	if err := reports.SaveReport(ctx, withError(9, 3, time.Unix(3000, 0), "checkout", "500", report.SideTarget, 100)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/executions/7/error-signatures")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET error-signatures = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		ExecutionID int64  `json:"execution_id"`
		GroupedBy   string `json:"grouped_by"`
		Groups      []struct {
			Key        string `json:"key"`
			TotalCount int64  `json:"total_count"`
			Rows       []struct {
				ResponseCode string `json:"response_code"`
			} `json:"rows"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.ExecutionID != 7 || got.GroupedBy != "label" {
		t.Fatalf("response = %+v, want execution_id 7 grouped_by label", got)
	}
	if len(got.Groups) != 1 || got.Groups[0].Key != "checkout" || got.Groups[0].TotalCount != 4 {
		t.Fatalf("groups = %+v, want one checkout group totalled 4 (3+1)", got.Groups)
	}
	if len(got.Groups[0].Rows) != 2 {
		t.Fatalf("groups[0].Rows = %+v, want both response codes broken out", got.Groups[0].Rows)
	}
}

func TestErrorSignatureHistoryHTTP_GroupsByResponseCode(t *testing.T) {
	t.Parallel()
	h, reports, _ := newReportEnv(t)
	ctx := context.Background()
	if err := reports.SaveReport(ctx, withError(7, 1, time.Unix(1000, 0), "checkout", "500", report.SideTarget, 3)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	if err := reports.SaveReport(ctx, withError(7, 2, time.Unix(2000, 0), "cart", "500", report.SideTarget, 2)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/executions/7/error-signatures?by=code")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET error-signatures?by=code = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		GroupedBy string `json:"grouped_by"`
		Groups    []struct {
			Key        string `json:"key"`
			TotalCount int64  `json:"total_count"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.GroupedBy != "response_code" {
		t.Fatalf("grouped_by = %q, want response_code", got.GroupedBy)
	}
	if len(got.Groups) != 1 || got.Groups[0].Key != "500" || got.Groups[0].TotalCount != 5 {
		t.Fatalf("groups = %+v, want one 500 group totalled 5 (3+2)", got.Groups)
	}
}

func TestErrorSignatureHistoryHTTP_EmptyExecution(t *testing.T) {
	t.Parallel()
	h, _, _ := newReportEnv(t)
	rec := do(t, h, http.MethodGet, "/api/executions/7/error-signatures")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET error-signatures = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Groups []interface{} `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Groups) != 0 {
		t.Fatalf("groups = %+v, want empty", got.Groups)
	}
}

func TestErrorSignatureHistoryHTTP_InvalidExecutionIDIsBadRequest(t *testing.T) {
	t.Parallel()
	h, _, _ := newReportEnv(t)
	if rec := do(t, h, http.MethodGet, "/api/executions/not-a-number/error-signatures"); rec.Code != http.StatusBadRequest {
		t.Fatalf("GET error-signatures (invalid id) = %d, want 400", rec.Code)
	}
}

func TestReportHTTP_FetchesACapturedShardLog(t *testing.T) {
	t.Parallel()
	h, _, obj := newReportEnv(t)
	if err := obj.Upload(context.Background(), lifecycleapp.RunShardKey(42, 3, 1, "log"), strings.NewReader("boom: 500")); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/runs/42/scenarios/3/shards/1/log")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET log = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "boom: 500" {
		t.Errorf("log body = %q", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
}

func TestReportHTTP_FetchesTheSnapshottedShardConfig(t *testing.T) {
	t.Parallel()
	h, _, obj := newReportEnv(t)
	if err := obj.Upload(context.Background(), lifecycleapp.RunShardKey(42, 3, 1, "yml"), strings.NewReader("execution: []")); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/runs/42/scenarios/3/shards/1/config")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET config = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "execution: []" {
		t.Errorf("config body = %q", got)
	}
}

func TestReportHTTP_UncapturedShardObjectsAre404(t *testing.T) {
	t.Parallel()
	h, _, _ := newReportEnv(t)

	for _, path := range []string{
		"/api/runs/1/scenarios/1/shards/0/log",
		"/api/runs/1/scenarios/1/shards/0/config",
	} {
		if rec := do(t, h, http.MethodGet, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}

// seedExportRun provisions the run both export formats read: a stored
// report (one "checkout" label, 10 samples) plus two measured seconds
// across two shards -- the same shape TestSeriesHTTP_ServesAMergedRun uses,
// because the export is that endpoint's data re-serialised.
func seedExportRun(t *testing.T, h http.Handler, reports *fake.ReportStore, progress *fake.ReportProgress) {
	t.Helper()
	if err := reports.SaveReport(context.Background(), sampleReport(1, 42)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	mustAbsorb(t, progress, seriesBatch(42, 0, true,
		metrics.Interval{
			Seq: 1, Timestamp: 1000, Label: "checkout", Concurrency: 5,
			Samples: 10, Succeeded: 9, Failed: 1, Latency: metrics.Histogram{0.01: 9, 0.2: 1},
		},
		metrics.Interval{
			Seq: 2, Timestamp: 1001, Label: "checkout", Concurrency: 5,
			Samples: 8, Succeeded: 8, Latency: metrics.Histogram{0.01: 8},
		},
	))
	mustAbsorb(t, progress, seriesBatch(42, 1, true,
		metrics.Interval{
			Seq: 1, Timestamp: 1000, Label: "checkout", Concurrency: 4,
			Samples: 5, Succeeded: 4, Failed: 1, Latency: metrics.Histogram{0.01: 5},
		},
	))
}

// format=json is the two endpoint shapes side by side: the report key is
// what GET /report serves, the series key what GET /series serves.
func TestRunExport_JSONCombinesReportAndSeries(t *testing.T) {
	t.Parallel()
	h, reports, progress := newSeriesEnv(t)
	seedExportRun(t, h, reports, progress)

	rec := do(t, h, http.MethodGet, "/api/runs/42/export?format=json")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET export json = %d (%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="run-42.json"` {
		t.Errorf("Content-Disposition = %q, want run-42.json attachment", cd)
	}
	var got struct {
		Report struct {
			RunID   int64  `json:"run_id"`
			Outcome string `json:"outcome"`
			Labels  []struct {
				Label   string `json:"label"`
				Samples int64  `json:"samples"`
			} `json:"labels"`
		} `json:"report"`
		Series struct {
			Points []struct {
				Ts  int64   `json:"ts"`
				VUs float64 `json:"vus"`
				RPS float64 `json:"rps"`
			} `json:"points"`
		} `json:"series"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.Report.RunID != 42 || got.Report.Outcome != string(taurus.OutcomeFailed) {
		t.Errorf("report = %+v, want run 42 with its verdict", got.Report)
	}
	if len(got.Report.Labels) != 1 || got.Report.Labels[0].Label != "checkout" || got.Report.Labels[0].Samples != 10 {
		t.Errorf("labels = %+v, want checkout with 10 samples", got.Report.Labels)
	}
	if len(got.Series.Points) != 2 || got.Series.Points[0].Ts != 1000 || got.Series.Points[1].Ts != 1001 {
		t.Fatalf("points = %+v, want 1000 and 1001", got.Series.Points)
	}
	if got.Series.Points[0].VUs != 9 || got.Series.Points[0].RPS != 15 {
		t.Errorf("first point = %+v, want vus 9 rps 15 (both shards summed)", got.Series.Points[0])
	}
}

// format=csv is one download, two sections, each under a "# run" comment
// header -- parsed back with csv.Reader to prove the quoting is real
// library output, not concatenated strings.
func TestRunExport_CSVPairsLabelAndSeriesSections(t *testing.T) {
	t.Parallel()
	h, reports, progress := newSeriesEnv(t)
	seedExportRun(t, h, reports, progress)

	rec := do(t, h, http.MethodGet, "/api/runs/42/export?format=csv")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET export csv = %d (%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="run-42.csv"` {
		t.Errorf("Content-Disposition = %q, want run-42.csv attachment", cd)
	}
	body := rec.Body.String()
	// The section markers, verbatim: they are the download's table of
	// contents, and the blank line between sections.
	if !strings.Contains(body, "# run 42 — labels\n") || !strings.Contains(body, "\n\n# run 42 — per-second series\n") {
		t.Fatalf("section markers missing:\n%s", body)
	}
	// LF, never CRLF (documented on the handler).
	if strings.Contains(body, "\r") {
		t.Errorf("csv contains CR; the export is LF-only")
	}

	// FieldsPerRecord = -1: the download deliberately pairs two tables of
	// different widths (6-column labels, 8-column series).
	cr := csv.NewReader(rec.Body)
	cr.FieldsPerRecord = -1
	cr.Comment = '#'
	rows, err := cr.ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	// Header + 1 label row, then header + 2 series rows (comments and the
	// blank line are skipped by the reader, not counted as records).
	want := [][]string{
		{"label", "samples", "error_rate", "p50", "p95", "p99"},
		{"checkout", "10", "0.3", "0.01", "0.5", "0.5"},
		{"ts", "vus", "rps", "err_pct", "p50", "p90", "p95", "p99"},
		{"1000", "9", "15", "13.333333333333334", "0.01", "0.01", "0.2", "0.2"},
		{"1001", "5", "8", "0", "0.01", "0.01", "0.01", "0.01"},
	}
	if len(rows) != len(want) {
		t.Fatalf("csv rows = %d, want %d:\n%v", len(rows), len(want), rows)
	}
	for i, w := range want {
		if strings.Join(rows[i], ",") != strings.Join(w, ",") {
			t.Errorf("row %d = %v, want %v", i, rows[i], w)
		}
	}
}

// A missing or unknown format is the caller's error: 400 before any store
// is consulted, whatever the deployment wires.
func TestRunExport_RejectsMissingAndUnknownFormat(t *testing.T) {
	t.Parallel()
	h, _, _ := newSeriesEnv(t)
	for _, q := range []string{"", "?format=xml", "?format=CSV"} {
		path := "/api/runs/42/export" + q
		rec := do(t, h, http.MethodGet, path)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, rec.Code)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != `{"message":"format must be json, csv or pdf"}` {
			t.Errorf("GET %s body = %s", path, got)
		}
	}
}

// A report-store-only deployment has no interval store wired: the export
// answers 404 rather than serving half a download, the series endpoint's
// own optional-dependency rule.
func TestRunExport_UnwiredSeriesIs404(t *testing.T) {
	t.Parallel()
	h := httpapi.NewRouter(httpapi.Deps{
		Reports: fake.NewReportStore(), Store: fake.NewObjectStore(),
		DefaultOwners: []string{"honryu"},
	})
	rec := do(t, h, http.MethodGet, "/api/runs/42/export?format=json")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET export (unwired series) = %d, want 404", rec.Code)
	}
}

func TestReportHTTP_InvalidPathParamsAreBadRequest(t *testing.T) {
	t.Parallel()
	h, _, _ := newReportEnv(t)

	for _, path := range []string{
		"/api/runs/not-a-number/report",
		"/api/executions/not-a-number/reports",
		"/api/runs/not-a-number/scenarios/1/shards/0/log",
		"/api/runs/1/scenarios/not-a-number/shards/0/log",
		"/api/runs/1/scenarios/1/shards/not-a-number/log",
		// Parseable as int64 but out of range for the int the object-store
		// key builder takes. Without the bound these wrap on a 32-bit build:
		// 4294967296 truncates to 0 and would serve shard 0's log under a
		// request for a shard that does not exist. Negative is rejected for
		// the same reason -- a shard index is an ordinal, not an offset.
		"/api/runs/1/scenarios/1/shards/4294967296/log",
		"/api/runs/1/scenarios/1/shards/2147483648/log",
		"/api/runs/1/scenarios/1/shards/-1/log",
		"/api/runs/1/scenarios/1/shards/4294967296/config",
	} {
		if rec := do(t, h, http.MethodGet, path); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, rec.Code)
		}
	}
}

// Phase 37: the run report's APM depth layer. With the execution service
// wired, the response carries the execution's project_id (the fourth value
// an APM link-out substitutes) and the exact baggage string the run's load
// carried, rebuilt from the stored identity -- the trace id doubles as the
// correlation id so baggage survives the engines; traceparent's parent id
// does not, and is deliberately not faked. Strictly additive: no execution
// service (the share path's own tolerance), or a run that predates
// correlation ids, and both fields simply stay absent.
func TestRunReport_APMDepthLayer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := fake.NewStore()
	obj := fake.NewObjectStore()
	executions := executionapp.NewService(store, obj, 100)
	h := httpapi.NewRouter(httpapi.Deps{
		Executions:    executions,
		Reports:       store,
		Store:         obj,
		DefaultOwners: []string{"honryu"},
	})

	proj, err := projectapp.NewService(store).Create(ctx, "proj", "owner", "123")
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}
	exec, err := executions.Create(ctx, "exec", proj.ID, taurus.ExecutorJMeter, "")
	if err != nil {
		t.Fatalf("Create execution: %v", err)
	}

	rep := sampleReport(exec.ID, 42)
	rep.CorrelationID = "4bf92f3577b34da6a3ce929d0e0e4736"
	if err := store.SaveReport(ctx, rep); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/runs/42/report")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET report = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		ProjectID int64  `json:"project_id"`
		Baggage   string `json:"baggage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ProjectID != proj.ID {
		t.Errorf("project_id = %d, want the execution's project %d", got.ProjectID, proj.ID)
	}
	// No tenant on the project, so no honryu.tenant entry (omitted, never
	// rendered empty) -- the same rule Headers applies on the wire.
	wantBaggage := fmt.Sprintf("honryu.service=%d,honryu.execution=%d,honryu.run=%s",
		proj.ID, exec.ID, rep.CorrelationID)
	if got.Baggage != wantBaggage {
		t.Errorf("baggage = %q, want %q", got.Baggage, wantBaggage)
	}
}

// The tolerance half: no execution service wired, or a report with no
// correlation id, and both phase 37 fields are absent rather than errors --
// the report predates the layer and must keep serving exactly as before.
func TestRunReport_APMDepthLayerOmitted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("no execution service wired", func(t *testing.T) {
		t.Parallel()
		h, reports, _ := newReportEnv(t)
		rep := sampleReport(7, 43)
		rep.CorrelationID = "abc"
		if err := reports.SaveReport(ctx, rep); err != nil {
			t.Fatalf("SaveReport: %v", err)
		}
		rec := do(t, h, http.MethodGet, "/api/runs/43/report")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET report = %d (%s)", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `"project_id"`) || strings.Contains(rec.Body.String(), `"baggage"`) {
			t.Errorf("report without execution service carries the phase 37 fields: %s", rec.Body.String())
		}
	})

	t.Run("run predates correlation ids", func(t *testing.T) {
		t.Parallel()
		store := fake.NewStore()
		obj := fake.NewObjectStore()
		executions := executionapp.NewService(store, obj, 100)
		h := httpapi.NewRouter(httpapi.Deps{
			Executions:    executions,
			Reports:       store,
			Store:         obj,
			DefaultOwners: []string{"honryu"},
		})
		proj, err := projectapp.NewService(store).Create(ctx, "proj", "owner", "123")
		if err != nil {
			t.Fatalf("Create project: %v", err)
		}
		exec, err := executions.Create(ctx, "exec", proj.ID, taurus.ExecutorJMeter, "")
		if err != nil {
			t.Fatalf("Create execution: %v", err)
		}
		// sampleReport carries no correlation id: the run predates telemetry.
		if err := store.SaveReport(ctx, sampleReport(exec.ID, 44)); err != nil {
			t.Fatalf("SaveReport: %v", err)
		}
		rec := do(t, h, http.MethodGet, "/api/runs/44/report")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET report = %d (%s)", rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if _, ok := got["baggage"]; ok {
			t.Errorf("baggage present for a correlation-less run: %v", got["baggage"])
		}
	})
}
