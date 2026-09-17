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
	"github.com/heridotlife/honryu/internal/app/calibrationapp"
	"github.com/heridotlife/honryu/internal/app/executionapp"
	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/app/projectapp"
	"github.com/heridotlife/honryu/internal/app/thresholdapp"
	"github.com/heridotlife/honryu/internal/domain/capacityprofile"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/domain/threshold"
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

// --- GET /api/runs/compare (phase 61) ---------------------------------------

// Three runs by repeated run_ids[] params: one 200 carrying all three
// reports, request order preserved -- the first id is the caller's baseline,
// so the wire must not reorder by id or recency.
func TestRunCompare_ThreeRunsInRequestOrder(t *testing.T) {
	t.Parallel()
	h, reports, _ := newReportEnv(t)
	for _, runID := range []int64{42, 43, 44} {
		if err := reports.SaveReport(context.Background(), sampleReport(1, runID)); err != nil {
			t.Fatalf("SaveReport(%d): %v", runID, err)
		}
	}

	rec := do(t, h, http.MethodGet, "/api/runs/compare?run_ids%5B%5D=44&run_ids%5B%5D=42&run_ids%5B%5D=43")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET compare = %d (%s)", rec.Code, rec.Body.String())
	}
	var got []report.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("compare = %d reports, want 3", len(got))
	}
	for i, wantID := range []int64{44, 42, 43} {
		if got[i].RunID != wantID {
			t.Errorf("compare[%d] = run %d, want %d (request order preserved)", i, got[i].RunID, wantID)
		}
	}
}

// One comma-separated run_ids= value selects the same N runs as the
// repeated spelling.
func TestRunCompare_CommaSeparatedRunIDs(t *testing.T) {
	t.Parallel()
	h, reports, _ := newReportEnv(t)
	for _, runID := range []int64{42, 43} {
		if err := reports.SaveReport(context.Background(), sampleReport(1, runID)); err != nil {
			t.Fatalf("SaveReport(%d): %v", runID, err)
		}
	}

	rec := do(t, h, http.MethodGet, "/api/runs/compare?run_ids=42,43")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET compare = %d (%s)", rec.Code, rec.Body.String())
	}
	var got []report.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].RunID != 42 || got[1].RunID != 43 {
		t.Errorf("compare = %+v, want runs 42 then 43", got)
	}
}

// The two-run pair form (run_a/run_b) keeps working -- the spelling older
// compare links baked into their URLs.
func TestRunCompare_PairFormRunAAndRunB(t *testing.T) {
	t.Parallel()
	h, reports, _ := newReportEnv(t)
	for _, runID := range []int64{42, 43} {
		if err := reports.SaveReport(context.Background(), sampleReport(1, runID)); err != nil {
			t.Fatalf("SaveReport(%d): %v", runID, err)
		}
	}

	rec := do(t, h, http.MethodGet, "/api/runs/compare?run_a=43&run_b=42")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET compare = %d (%s)", rec.Code, rec.Body.String())
	}
	var got []report.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].RunID != 43 || got[1].RunID != 42 {
		t.Errorf("compare = %+v, want runs 43 then 42", got)
	}
}

// The spellings mix: repeated run_ids[] and comma-separated run_ids values
// in one query all contribute, in request order.
func TestRunCompare_MixesSpellings(t *testing.T) {
	t.Parallel()
	h, reports, _ := newReportEnv(t)
	for _, runID := range []int64{42, 43, 44} {
		if err := reports.SaveReport(context.Background(), sampleReport(1, runID)); err != nil {
			t.Fatalf("SaveReport(%d): %v", runID, err)
		}
	}

	rec := do(t, h, http.MethodGet, "/api/runs/compare?run_ids%5B%5D=44&run_ids=42,43")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET compare = %d (%s)", rec.Code, rec.Body.String())
	}
	var got []report.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 3 || got[0].RunID != 44 || got[1].RunID != 42 || got[2].RunID != 43 {
		t.Errorf("compare = %+v, want runs 44, 42, 43", got)
	}
}

// Malformed or absent selections are a 400 naming the problem, never a
// guess; the count cap keeps one request from spinning the store.
func TestRunCompare_RejectsBadSelections(t *testing.T) {
	t.Parallel()
	h, reports, _ := newReportEnv(t)
	if err := reports.SaveReport(context.Background(), sampleReport(1, 42)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	t.Run("no ids at all", func(t *testing.T) {
		rec := do(t, h, http.MethodGet, "/api/runs/compare")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("GET compare = %d, want 400", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "no runs to compare") {
			t.Errorf("body = %s, want the no-runs guidance", rec.Body.String())
		}
	})

	t.Run("non-numeric id", func(t *testing.T) {
		rec := do(t, h, http.MethodGet, "/api/runs/compare?run_ids=42,abc")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("GET compare = %d, want 400", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "invalid run id") {
			t.Errorf("body = %s, want the invalid-id naming", rec.Body.String())
		}
	})

	t.Run("over the cap", func(t *testing.T) {
		q := "run_ids=42"
		for i := 0; i < 20; i++ {
			q += fmt.Sprintf("&run_ids%%5B%%5D=%d", 100+i)
		}
		rec := do(t, h, http.MethodGet, "/api/runs/compare?"+q)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("GET compare = %d, want 400", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "too many runs") {
			t.Errorf("body = %s, want the cap naming", rec.Body.String())
		}
	})
}

// One unknown run fails the whole request with the store's own 404: a
// compare of N-1 runs answers a question nobody asked.
func TestRunCompare_UnknownRunFailsWholeRequest(t *testing.T) {
	t.Parallel()
	h, reports, _ := newReportEnv(t)
	if err := reports.SaveReport(context.Background(), sampleReport(1, 42)); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/runs/compare?run_ids=42,999")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET compare = %d, want 404", rec.Code)
	}
}

// Every element carries the Phase 29 verdict layer, exactly as the
// single-run route serves it -- the compare endpoint is a batch of that
// route, not a new shape.
func TestRunCompare_CarriesCriteriaVerdictPerRun(t *testing.T) {
	t.Parallel()
	h, reports, store := newCriteriaReportEnv(t)
	ctx := context.Background()
	for _, runID := range []int64{42, 43} {
		// Same shape the single-run verdict test uses: p95 at 0.7s trips
		// "p95>500ms", the for-window clause is unparsed.
		rep := report.Build(report.Input{
			ExecutionID: 1, RunID: runID,
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
			t.Fatalf("SaveReport(%d): %v", runID, err)
		}
	}
	if err := store.SetExecutionCriteria(ctx, 1, []string{"failures>50%", "p95>500ms", "p99<1s for 5s"}); err != nil {
		t.Fatalf("SetExecutionCriteria: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/runs/compare?run_ids=42,43")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET compare = %d (%s)", rec.Code, rec.Body.String())
	}
	var got []verdictShape
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("compare = %d reports, want 2", len(got))
	}
	for i, g := range got {
		if len(g.Criteria) != 3 {
			t.Errorf("compare[%d].criteria = %v, want the execution's three", i, g.Criteria)
		}
		if len(g.FailingCriteria) != 2 {
			t.Errorf("compare[%d].failing_criteria = %+v, want the tripped p95 and the unparsed window clause", i, g.FailingCriteria)
		}
	}
}

// Phase 73 pin: the run-detail result tabs read three layers off ONE
// response -- the engine's failing_criteria (Checks tab), the scenario's
// threshold_results (Thresholds tab), and the latency percentiles map
// (Percentiles tab). This holds GET /api/runs/{run_id}/report to carrying
// all three at once, so a tab can only go blank because the data is
// genuinely absent, never because a field quietly left the wire.
func TestRunReport_CarriesChecksThresholdsAndLatency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := fake.NewStore()
	obj := fake.NewObjectStore()
	reports := fake.NewReportStore()
	svc := thresholdapp.NewService(store)
	h := httpapi.NewRouter(httpapi.Deps{
		Reports:       reports,
		Thresholds:    svc,
		Store:         obj,
		Executions:    executionapp.NewService(store, obj, 100),
		DefaultOwners: []string{"honryu"},
	})

	sc, err := scenario.New("tabbed", 1)
	if err != nil {
		t.Fatalf("scenario.New: %v", err)
	}
	scenarioID, err := store.CreateScenario(ctx, sc)
	if err != nil {
		t.Fatalf("CreateScenario: %v", err)
	}
	if err := store.SetExecutionCriteria(ctx, 1, []string{"failures>50%", "p95>500ms"}); err != nil {
		t.Fatalf("SetExecutionCriteria: %v", err)
	}
	// 30% failures keep "failures>50%" green; p95 lands at 700ms, tripping
	// "p95>500ms" -- and grading missed against the scenario's 300ms bound.
	rep := report.Build(report.Input{
		ExecutionID: 1, ScenarioID: scenarioID, RunID: 42,
		Engine:    taurus.ExecutorJMeter,
		StartedAt: time.Unix(1000, 0).UTC(),
		EndedAt:   time.Unix(1030, 0).UTC(),
		Outcome:   taurus.OutcomeFailed,
		Requested: report.Load{Concurrency: 10, DurationSeconds: 30},
		Intervals: []metrics.Interval{{
			Timestamp: 1000, Label: "checkout", Samples: 10, Failed: 3, Succeeded: 7,
			Latency: metrics.Histogram{0.01: 7, 0.7: 3},
		}},
	})
	if err := reports.SaveReport(ctx, rep); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	if _, err := svc.Replace(ctx, scenarioID, []threshold.Threshold{
		{ScenarioID: scenarioID, Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: 300},
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if err := svc.EvaluateRun(ctx, rep); err != nil {
		t.Fatalf("EvaluateRun: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/runs/42/report")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET report = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Latency         map[string]float64 `json:"latency"`
		FailingCriteria []struct {
			Criterion string `json:"criterion"`
			Unparsed  bool   `json:"unparsed"`
		} `json:"failing_criteria"`
		ThresholdResults []thresholdResultRow `json:"threshold_results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The checks layer: exactly the tripped criterion, parsed -- the
	// passing one is absent, never listed as failing.
	if len(got.FailingCriteria) != 1 || got.FailingCriteria[0].Criterion != "p95>500ms" || got.FailingCriteria[0].Unparsed {
		t.Errorf("failing_criteria = %+v, want exactly the tripped p95, parsed", got.FailingCriteria)
	}
	// The thresholds layer: one row, missed, observed 700ms (the wire's
	// seconds crossed to milliseconds exactly once).
	if len(got.ThresholdResults) != 1 {
		t.Fatalf("threshold_results = %+v, want one row", got.ThresholdResults)
	}
	p95 := got.ThresholdResults[0]
	if p95.Metric != "http_p95_ms" || p95.Satisfied == nil || *p95.Satisfied {
		t.Errorf("p95 threshold row = %+v, want http_p95_ms missed", p95)
	}
	if p95.ObservedValue == nil || *p95.ObservedValue != 700 {
		t.Errorf("p95 observed = %v, want 700ms", p95.ObservedValue)
	}
	// The latency layer: the percentiles map the Percentiles tab renders,
	// p50 at the 10ms floor and p95 at the 700ms tail.
	if len(got.Latency) == 0 {
		t.Fatal("latency is empty: the Percentiles tab would render nothing for a measured run")
	}
	if got.Latency["50"] != 0.01 {
		t.Errorf("latency[p50] = %v, want 0.01", got.Latency["50"])
	}
	if got.Latency["95"] != 0.7 {
		t.Errorf("latency[p95] = %v, want 0.7", got.Latency["95"])
	}
}

// Phase 78: the recommendations endpoint. One synthetic report trips two
// rules at once (a missed scenario threshold and a high error rate); a clean
// report -- thresholds defined, met, and nothing wrong with the telemetry --
// yields exactly []. Both cases read the same wire shape the SPA renders.
func TestRunRecommendations_FireOnMissedThresholdAndHighErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := fake.NewStore()
	reports := fake.NewReportStore()
	svc := thresholdapp.NewService(store)
	h := httpapi.NewRouter(httpapi.Deps{
		Reports:       reports,
		Thresholds:    svc,
		DefaultOwners: []string{"honryu"},
	})

	sc, err := scenario.New("recs", 1)
	if err != nil {
		t.Fatalf("scenario.New: %v", err)
	}
	scenarioID, err := store.CreateScenario(ctx, sc)
	if err != nil {
		t.Fatalf("CreateScenario: %v", err)
	}
	// 30% of requests fail (well past the 1% line) with a flat latency
	// profile, so exactly the error-rate and threshold rules have evidence:
	// the observed 30% error rate misses the scenario's 5% ceiling, and the
	// 2x latency spread stays inside the spread rule's 4x factor.
	rep := report.Build(report.Input{
		ExecutionID: 1, ScenarioID: scenarioID, RunID: 77,
		Engine:    taurus.ExecutorJMeter,
		StartedAt: time.Unix(1000, 0).UTC(),
		EndedAt:   time.Unix(1030, 0).UTC(),
		Outcome:   taurus.OutcomeFailed,
		Requested: report.Load{Concurrency: 10, DurationSeconds: 30},
		Intervals: []metrics.Interval{{
			Timestamp: 1000, Label: "checkout", Samples: 10, Failed: 3, Succeeded: 7,
			Latency: metrics.Histogram{0.01: 7, 0.02: 3},
		}},
	})
	if err := reports.SaveReport(ctx, rep); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	if _, err := svc.Replace(ctx, scenarioID, []threshold.Threshold{
		{ScenarioID: scenarioID, Metric: threshold.MetricErrorRate, Comparison: threshold.ComparisonLT, Value: 0.05},
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if err := svc.EvaluateRun(ctx, rep); err != nil {
		t.Fatalf("EvaluateRun: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/runs/77/recommendations")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET recommendations = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Recommendations []struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Detail   string `json:"detail"`
			Severity string `json:"severity"`
		} `json:"recommendations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Recommendations) != 2 {
		t.Fatalf("recommendations = %+v, want exactly the two fired rules", got.Recommendations)
	}
	// Fixed rule order: the error-rate rule's output precedes the threshold
	// rule's, independent of store or map ordering.
	if got.Recommendations[0].ID != "high-error-rate" || got.Recommendations[1].ID != "threshold-missed" {
		t.Errorf("ids = %q, %q; want high-error-rate then threshold-missed",
			got.Recommendations[0].ID, got.Recommendations[1].ID)
	}
	for _, r := range got.Recommendations {
		if r.Severity != "warning" {
			t.Errorf("rec %q severity = %q, want warning", r.ID, r.Severity)
		}
		if r.Title == "" || r.Detail == "" {
			t.Errorf("rec %q missing title or detail", r.ID)
		}
	}
	// The threshold rule names the missed metric and its bound -- actionable,
	// not just loud.
	if detail := got.Recommendations[1].Detail; !strings.Contains(detail, "error_rate") || !strings.Contains(detail, "5.0%") {
		t.Errorf("threshold detail = %q, want the metric and the bound named", detail)
	}
}

func TestRunRecommendations_CleanReportIsEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := fake.NewStore()
	reports := fake.NewReportStore()
	svc := thresholdapp.NewService(store)
	h := httpapi.NewRouter(httpapi.Deps{
		Reports:       reports,
		Thresholds:    svc,
		DefaultOwners: []string{"honryu"},
	})

	sc, err := scenario.New("clean", 1)
	if err != nil {
		t.Fatalf("scenario.New: %v", err)
	}
	scenarioID, err := store.CreateScenario(ctx, sc)
	if err != nil {
		t.Fatalf("CreateScenario: %v", err)
	}
	// A clean run: no failures, flat latency, and a met threshold -- every
	// rule's evidence absent, including no-thresholds (the set is defined).
	if _, err := svc.Replace(ctx, scenarioID, []threshold.Threshold{
		{ScenarioID: scenarioID, Metric: threshold.MetricHTTPP95MS, Comparison: threshold.ComparisonLT, Value: 300},
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	rep := report.Build(report.Input{
		ExecutionID: 1, ScenarioID: scenarioID, RunID: 78,
		Engine:    taurus.ExecutorJMeter,
		StartedAt: time.Unix(1000, 0).UTC(),
		EndedAt:   time.Unix(1030, 0).UTC(),
		Outcome:   taurus.OutcomePassed,
		Requested: report.Load{Concurrency: 10, DurationSeconds: 30},
		Intervals: []metrics.Interval{{
			Timestamp: 1000, Label: "checkout", Samples: 100, Failed: 0, Succeeded: 100,
			Latency: metrics.Histogram{0.01: 90, 0.02: 10},
		}},
	})
	if err := reports.SaveReport(ctx, rep); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	if err := svc.EvaluateRun(ctx, rep); err != nil {
		t.Fatalf("EvaluateRun: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/runs/78/recommendations")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET recommendations = %d (%s)", rec.Code, rec.Body.String())
	}
	// A clean run renders [], never null -- the series endpoint's own rule.
	if !strings.Contains(rec.Body.String(), `"recommendations":[]`) {
		t.Errorf("body = %s, want an empty recommendations array", rec.Body.String())
	}
}

// recsIDs decodes a recommendations payload into its fired rule ids, in
// wire order -- the fixed rule order the tests pin.
func recsIDs(t *testing.T, body string) []string {
	t.Helper()
	var got struct {
		Recommendations []struct {
			ID       string `json:"id"`
			Severity string `json:"severity"`
		} `json:"recommendations"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ids := make([]string, 0, len(got.Recommendations))
	for _, r := range got.Recommendations {
		ids = append(ids, r.ID+"/"+r.Severity)
	}
	return ids
}

// newCapacityRecsEnv wires the recommendations endpoint with the phase-84
// capacity layer: an execution pinning the engine and pod size, a report for
// a paced (fixed-rate) scenario, and a calibration service reading the fake
// store's profiles. Returns the router, the report store, and the exact key
// the run's evidence resolves to, so a test seeds (or does not seed) a
// profile for it.
func newCapacityRecsEnv(t *testing.T) (http.Handler, *fake.Store, *fake.ReportStore, capacityprofile.Key) {
	t.Helper()
	ctx := context.Background()
	store := fake.NewStore()
	reports := fake.NewReportStore()
	obj := fake.NewObjectStore()
	executions := executionapp.NewService(store, obj, 100)
	h := httpapi.NewRouter(httpapi.Deps{
		Reports:       reports,
		Executions:    executions,
		Calibrations:  calibrationapp.NewService(store),
		Store:         obj,
		DefaultOwners: []string{"honryu"},
	})

	executionID, err := store.CreateExecution(ctx, execution.Execution{
		Name: "paced", ProjectID: 1, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi",
	})
	if err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}
	// A clean, paced run: 100 req/s requested, 3000 samples over the 30s
	// window, no failures, flat latency -- the capacity rules are the only
	// ones with anything to say.
	rep := report.Build(report.Input{
		ExecutionID: executionID, ScenarioID: 2, RunID: 79,
		Engine:    taurus.ExecutorJMeter,
		StartedAt: time.Unix(1000, 0).UTC(),
		EndedAt:   time.Unix(1030, 0).UTC(),
		Outcome:   taurus.OutcomePassed,
		Requested: report.Load{Concurrency: 10, Throughput: 100, DurationSeconds: 30},
		Intervals: []metrics.Interval{{
			Timestamp: 1000, Label: "checkout", Samples: 3000, Succeeded: 3000,
			Latency: metrics.Histogram{0.01: 2700, 0.02: 300},
		}},
	})
	if err := reports.SaveReport(ctx, rep); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	return h, store, reports, capacityprofile.Key{
		ScenarioID: 2, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi",
	}
}

// Phase 84: the capacity evidence layer. A paced run with no profile for
// its exact pod size reads as unverified (info); seeding a profile for the
// exact key the execution pins stands that down; the same profile calibrated
// past the 30-day line turns into capacity-outdated instead; and a profile
// for a DIFFERENT pod size never matches -- the key is the ask. Each case
// gets its own environment because a profile, once seeded, cannot be
// un-seeded.
func TestRunRecommendations_CapacityEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// No profile anywhere: the run's fixed-rate ask is unverified.
	h, _, _, key := newCapacityRecsEnv(t)
	rec := do(t, h, http.MethodGet, "/api/runs/79/recommendations")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET recommendations = %d (%s)", rec.Code, rec.Body.String())
	}
	if ids := recsIDs(t, rec.Body.String()); len(ids) != 1 || ids[0] != "capacity-unverified/info" {
		t.Fatalf("ids = %v, want exactly capacity-unverified/info", ids)
	}

	// A fresh profile for the exact key: nothing left to complain about.
	h, store, _, key := newCapacityRecsEnv(t)
	if err := store.UpsertCapacityProfile(ctx, capacityprofile.CapacityProfile{
		Key: key, PerPodQPS: 120, CalibratedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("UpsertCapacityProfile: %v", err)
	}
	rec = do(t, h, http.MethodGet, "/api/runs/79/recommendations")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET recommendations = %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"recommendations":[]`) {
		t.Errorf("body = %s, want [] once the exact key is calibrated", rec.Body.String())
	}

	// The same profile 31 days old: capacity-outdated replaces it.
	h, store, _, key = newCapacityRecsEnv(t)
	if err := store.UpsertCapacityProfile(ctx, capacityprofile.CapacityProfile{
		Key: key, PerPodQPS: 120, CalibratedAt: time.Now().Add(-31 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("UpsertCapacityProfile: %v", err)
	}
	rec = do(t, h, http.MethodGet, "/api/runs/79/recommendations")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET recommendations = %d (%s)", rec.Code, rec.Body.String())
	}
	if ids := recsIDs(t, rec.Body.String()); len(ids) != 1 || ids[0] != "capacity-outdated/info" {
		t.Fatalf("ids = %v, want exactly capacity-outdated/info for a 31-day profile", ids)
	}

	// A profile for a different pod size is no profile for this run: the
	// ask stays unverified, exactly as before anything was seeded.
	h, store, _, key = newCapacityRecsEnv(t)
	other := key
	other.CPU = "2"
	if err := store.UpsertCapacityProfile(ctx, capacityprofile.CapacityProfile{
		Key: other, PerPodQPS: 240, CalibratedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("UpsertCapacityProfile: %v", err)
	}
	rec = do(t, h, http.MethodGet, "/api/runs/79/recommendations")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET recommendations = %d (%s)", rec.Code, rec.Body.String())
	}
	if ids := recsIDs(t, rec.Body.String()); len(ids) != 1 || ids[0] != "capacity-unverified/info" {
		t.Fatalf("ids = %v, want capacity-unverified/info: another pod size's profile is no match", ids)
	}
}

// The tolerance paths: capacity evidence that cannot be read is unknown, and
// unknown reads as unverified for a paced run -- never as a failed request.
// Both unwired services (the report-only deployment) and an unreadable
// execution (the run's pod size cannot be pinned) degrade the same way.
func TestRunRecommendations_CapacityUnknownIsNotAnError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// No calibration service wired at all: a paced run still answers, with
	// the unverified advisory rather than a 500 or a silent gap.
	reports := fake.NewReportStore()
	h := httpapi.NewRouter(httpapi.Deps{Reports: reports, DefaultOwners: []string{"honryu"}})
	rep := report.Build(report.Input{
		ExecutionID: 1, ScenarioID: 2, RunID: 80,
		Engine:    taurus.ExecutorJMeter,
		StartedAt: time.Unix(1000, 0).UTC(),
		EndedAt:   time.Unix(1030, 0).UTC(),
		Outcome:   taurus.OutcomePassed,
		Requested: report.Load{Concurrency: 10, Throughput: 100, DurationSeconds: 30},
		Intervals: []metrics.Interval{{
			Timestamp: 1000, Label: "checkout", Samples: 3000, Succeeded: 3000,
			Latency: metrics.Histogram{0.01: 2700, 0.02: 300},
		}},
	})
	if err := reports.SaveReport(ctx, rep); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	got := do(t, h, http.MethodGet, "/api/runs/80/recommendations")
	if got.Code != http.StatusOK {
		t.Fatalf("GET recommendations (unwired) = %d (%s)", got.Code, got.Body.String())
	}
	if ids := recsIDs(t, got.Body.String()); len(ids) != 1 || ids[0] != "capacity-unverified/info" {
		t.Errorf("ids = %v, want exactly capacity-unverified/info with the layer unwired", ids)
	}

	// Wired, but the report names an execution the store cannot read: the
	// pod size cannot be pinned, the evidence stays unknown, and the read
	// still succeeds with the same advisory.
	full, _, fullReports, _ := newCapacityRecsEnv(t)
	orphan := report.Build(report.Input{
		ExecutionID: 999, ScenarioID: 2, RunID: 81,
		Engine:    taurus.ExecutorJMeter,
		StartedAt: time.Unix(1000, 0).UTC(),
		EndedAt:   time.Unix(1030, 0).UTC(),
		Outcome:   taurus.OutcomePassed,
		Requested: report.Load{Concurrency: 10, Throughput: 100, DurationSeconds: 30},
		Intervals: []metrics.Interval{{
			Timestamp: 1000, Label: "checkout", Samples: 3000, Succeeded: 3000,
			Latency: metrics.Histogram{0.01: 2700, 0.02: 300},
		}},
	})
	if err := fullReports.SaveReport(ctx, orphan); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	got = do(t, full, http.MethodGet, "/api/runs/81/recommendations")
	if got.Code != http.StatusOK {
		t.Fatalf("GET recommendations (unreadable execution) = %d (%s)", got.Code, got.Body.String())
	}
	if ids := recsIDs(t, got.Body.String()); len(ids) != 1 || ids[0] != "capacity-unverified/info" {
		t.Errorf("ids = %v, want exactly capacity-unverified/info when the execution cannot be read", ids)
	}
}
