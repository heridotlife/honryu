package httpapi

import (
	"bytes"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/heridotlife/honryu/internal/app/lifecycleapp"
	"github.com/heridotlife/honryu/internal/app/reportapp"
	"github.com/heridotlife/honryu/internal/domain/rbac"
	"github.com/heridotlife/honryu/internal/domain/report"
)

// authorizeReport verifies the caller may read reports derived from
// executionID: rbac.ResourceReport/ActionRead against the execution's own
// tenant (Phase 20 -- report surfaces follow an execution's ownership
// without being its CRUD). In legacy mode there is no account to scope
// by, and report-only deployments need not wire the execution service at
// all, so the route stays open exactly as before -- the campaign/schedule
// gates' precedent.
func (h *handlers) authorizeReport(r *http.Request, executionID int64) error {
	if !h.rbacEnabled() {
		return nil
	}
	c, err := h.deps.Executions.Get(r.Context(), executionID)
	if err != nil {
		return err
	}
	return h.authorize(r.Context(), "", c.TenantID, rbac.ResourceReport, rbac.ActionRead)
}

// authorizeRunShard resolves a run's owning execution and authorizes the
// report read against its tenant. The run row itself carries no tenant,
// so the resolution is part of the authorization. Legacy mode skips both,
// like authorizeReport.
func (h *handlers) authorizeRunShard(r *http.Request, runID int64) error {
	if !h.rbacEnabled() {
		return nil
	}
	executionID, err := h.deps.Lifecycle.RunExecutionID(r.Context(), runID)
	if err != nil {
		return err
	}
	return h.authorizeReport(r, executionID)
}

// runReportResponse is the wire shape of GET /api/runs/{run_id}/report: the
// stored report's every field verbatim (the embedded domain type serializes
// flattened), plus the execution's configured pass/fail criteria and which of
// them this run tripped -- the verdict-at-a-glance layer (Phase 29). Both
// added arrays are always arrays, the series endpoint's own rule: an empty
// verdict renders as nothing, not as null.
type runReportResponse struct {
	report.Report
	Criteria        []string                   `json:"criteria"`
	FailingCriteria []failingCriterionResponse `json:"failing_criteria"`
}

// runReport returns a run's stored report -- the durable record of what it
// produced, retrievable after the engines and their pods are gone -- with the
// execution's configured criteria evaluated against it (Phase 29). Authorized
// via the report's own recorded ExecutionID, resolved before anything is
// written back (Phase 20).
func (h *handlers) runReport(w http.ResponseWriter, r *http.Request) {
	runID, ok := pathInt(r, "run_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	rep, err := h.deps.Reports.GetReport(r.Context(), runID)
	if err != nil {
		respondError(w, err)
		return
	}
	if err := h.authorizeReport(r, rep.ExecutionID); err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.withCriteriaVerdict(r, rep))
}

// withCriteriaVerdict layers the Phase 29 verdict fields onto a stored
// report: the execution's configured criteria as-is, and which of them the
// report's own measurements tripped via EvaluateCriteria (pure, tested domain
// logic). Strictly additive -- no execution service wired, or a criteria read
// that fails, yields empty verdict fields, never a failed report read: the
// report existed and was authorized before criteria entered the picture.
func (h *handlers) withCriteriaVerdict(r *http.Request, rep report.Report) runReportResponse {
	var crits []string
	if h.deps.Executions != nil {
		if c, err := h.deps.Executions.CriteriaFor(r.Context(), rep.ExecutionID); err == nil {
			crits = c
		}
	}
	failing := rep.EvaluateCriteria(crits)
	out := runReportResponse{
		Report:          rep,
		Criteria:        crits,
		FailingCriteria: make([]failingCriterionResponse, len(failing)),
	}
	for i, fc := range failing {
		out.FailingCriteria[i] = failingCriterionResponse{Criterion: fc.Criterion, Unparsed: fc.Unparsed}
	}
	if out.Criteria == nil {
		out.Criteria = []string{}
	}
	return out
}

// seriesResponse is the wire shape of GET /api/runs/{run_id}/series: points
// ascending by timestamp, and always an array, so an empty run charts nothing
// rather than null.
type seriesResponse struct {
	Points []reportapp.SeriesPoint `json:"points"`
}

// runSeries returns a run's per-second series -- the shape of the run (VUs,
// RPS, error rate, latency percentiles per second), as distinct from the
// verdicts runReport serves. The unknown-run 404 and the authorization gate
// are runReport's own: the stored report is what makes a run known, and its
// ExecutionID is what the report-read gate checks against. Runs that predate
// the series store have a report and no series, and chart empty.
func (h *handlers) runSeries(w http.ResponseWriter, r *http.Request) {
	if h.deps.Series == nil {
		writeError(w, http.StatusNotFound, "series not configured")
		return
	}
	runID, ok := pathInt(r, "run_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	rep, err := h.deps.Reports.GetReport(r.Context(), runID)
	if err != nil {
		respondError(w, err)
		return
	}
	if err := h.authorizeReport(r, rep.ExecutionID); err != nil {
		respondError(w, err)
		return
	}
	intervals, err := h.deps.Series.ListIntervalsByRun(r.Context(), runID)
	if err != nil {
		respondError(w, err)
		return
	}
	points := reportapp.BuildSeries(intervals, reportapp.SeriesPercentiles())
	if points == nil {
		points = []reportapp.SeriesPoint{}
	}
	writeJSON(w, http.StatusOK, seriesResponse{Points: points})
}

// runExportResponse is the wire shape of GET /api/runs/{run_id}/export
// ?format=json: the report and series under their own keys, each exactly the
// shape its dedicated endpoint serves, so a consumer of one consumer of the
// other needs no translation.
type runExportResponse struct {
	Report report.Report  `json:"report"`
	Series seriesResponse `json:"series"`
}

// runExport serves a run's data out of honryu: ?format=json returns the
// report and series in their endpoint shapes, ?format=csv a single
// two-section download (per-label table, then per-second series, each under
// a "# run {id} ..." comment header -- encoding/csv has no comment concept,
// so those lines are written raw). The authorization and data path are
// runSeries's own: the export is those two endpoints re-serialised, so it
// shares their gate (report:read against the owning execution's tenant) and
// their optional dependency (no series store wired -> 404, not a half
// export). CSV details, fixed deliberately: LF line endings (csv.Writer's
// default is CRLF; overridden because nothing in the house consumes CRLF and
// LF diffs cleanly), latency in seconds and error_rate as a fraction exactly
// as the JSON wire carries them (an export formats nothing), and an empty
// cell where a percentile was not measured (the table renders an em-dash for
// the same condition).
func (h *handlers) runExport(w http.ResponseWriter, r *http.Request) {
	// Input validated before the configuration check: a bad format is the
	// caller's error whatever the deployment wires, and the 400 needs no
	// store to answer.
	format := r.URL.Query().Get("format")
	if format != "json" && format != "csv" && format != "pdf" {
		writeError(w, http.StatusBadRequest, "format must be json, csv or pdf")
		return
	}
	if h.deps.Series == nil {
		writeError(w, http.StatusNotFound, "series not configured")
		return
	}
	runID, ok := pathInt(r, "run_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	rep, err := h.deps.Reports.GetReport(r.Context(), runID)
	if err != nil {
		respondError(w, err)
		return
	}
	if err := h.authorizeReport(r, rep.ExecutionID); err != nil {
		respondError(w, err)
		return
	}
	intervals, err := h.deps.Series.ListIntervalsByRun(r.Context(), runID)
	if err != nil {
		respondError(w, err)
		return
	}
	points := reportapp.BuildSeries(intervals, reportapp.SeriesPercentiles())
	if points == nil {
		points = []reportapp.SeriesPoint{}
	}

	w.Header().Set("Content-Disposition", `attachment; filename="run-`+strconv.FormatInt(runID, 10)+`.`+format+`"`)
	if format == "json" {
		writeJSON(w, http.StatusOK, runExportResponse{Report: rep, Series: seriesResponse{Points: points}})
		return
	}
	if format == "pdf" {
		w.Header().Set("Content-Type", "application/pdf")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(runExportPDF(runID, rep, points))
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// #nosec G705 -- Content-Type is text/csv; CSV cells are quoted by
	// encoding/csv, so a hostile label cannot break out into a formula or
	// HTML sink.
	_, _ = w.Write(runExportCSV(runID, rep, points))
}

// runExportCSV renders the CSV download: the per-label table then the
// per-second series, each preceded by a comment line naming the run. All
// cell content goes through encoding/csv into a bytes.Buffer so quoting is
// the library's problem, never string concatenation.
func runExportCSV(runID int64, rep report.Report, points []reportapp.SeriesPoint) []byte {
	var buf bytes.Buffer
	// csv.Writer buffers internally, so it must be flushed before any raw
	// write (comment lines, the blank separator) touches the same buffer.
	cw := csv.NewWriter(&buf)
	cw.UseCRLF = false // LF, documented on runExport

	fmt.Fprintf(&buf, "# run %d — labels\n", runID)
	_ = cw.Write([]string{"label", "samples", "error_rate", "p50", "p95", "p99"})
	for _, l := range rep.Labels {
		_ = cw.Write([]string{
			l.Label, strconv.FormatInt(l.Samples, 10), exportFloat(l.ErrorRate),
			exportPercentile(l.Latency, 50), exportPercentile(l.Latency, 95), exportPercentile(l.Latency, 99),
		})
	}
	cw.Flush()

	buf.WriteString("\n")
	fmt.Fprintf(&buf, "# run %d — per-second series\n", runID)
	_ = cw.Write([]string{"ts", "vus", "rps", "err_pct", "p50", "p90", "p95", "p99"})
	for _, p := range points {
		_ = cw.Write([]string{
			strconv.FormatInt(p.Ts, 10), exportFloat(p.VUs), exportFloat(p.RPS), exportFloat(p.ErrPct),
			exportPercentile(p.Latency, 50), exportPercentile(p.Latency, 90),
			exportPercentile(p.Latency, 95), exportPercentile(p.Latency, 99),
		})
	}
	cw.Flush()
	return buf.Bytes()
}

// exportFloat writes a float in its shortest round-trip form -- the export
// carries the wire's value, never a formatted or truncated one.
func exportFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// exportPercentile writes one percentile of a latency map, or an empty cell
// when that percentile was not measured.
func exportPercentile(p report.Percentiles, pct float64) string {
	if v, ok := p[pct]; ok {
		return exportFloat(v)
	}
	return ""
}

// executionReports lists an execution's reports, most recent first, so a
// service owner can see how it has behaved over time.
func (h *handlers) executionReports(w http.ResponseWriter, r *http.Request) {
	executionID, ok := pathInt(r, "execution_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid execution id")
		return
	}
	if err := h.authorizeReport(r, executionID); err != nil {
		respondError(w, err)
		return
	}
	reps, err := h.deps.Reports.ListReports(r.Context(), executionID, queryInt(r, "limit"))
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, reps)
}

type trendPointResponse struct {
	RunID                    int64   `json:"run_id"`
	Outcome                  string  `json:"outcome"`
	AchievedThroughput       float64 `json:"achieved_throughput"`
	RequestedThroughput      float64 `json:"requested_throughput"`
	ErrorRate                float64 `json:"error_rate"`
	P50                      float64 `json:"p50"`
	P90                      float64 `json:"p90"`
	P95                      float64 `json:"p95"`
	P99                      float64 `json:"p99"`
	HitTargetQPS             bool    `json:"hit_target_qps"`
	HasComparablePredecessor bool    `json:"has_comparable_predecessor"`
	Regressed                bool    `json:"regressed,omitempty"`
}

type trendResponse struct {
	ExecutionID int64                `json:"execution_id"`
	Points      []trendPointResponse `json:"points"`
}

func toTrendResponse(t report.Trend) trendResponse {
	points := make([]trendPointResponse, len(t.Points))
	for i, p := range t.Points {
		points[i] = trendPointResponse{
			RunID: p.RunID, Outcome: string(p.Outcome),
			AchievedThroughput: p.AchievedThroughput, RequestedThroughput: p.RequestedThroughput,
			ErrorRate: p.ErrorRate, P50: p.P50, P90: p.P90, P95: p.P95, P99: p.P99,
			HitTargetQPS: p.HitTargetQPS, HasComparablePredecessor: p.HasComparablePredecessor,
			Regressed: p.Regressed,
		}
	}
	return trendResponse{ExecutionID: t.ExecutionID, Points: points}
}

// executionTrend returns an execution's run-over-run trend: achieved QPS,
// requested QPS, latency percentiles, and error rate as raw advisory series,
// plus the one flagged signal (a run-over-run hit-target-QPS regression
// against its nearest comparable predecessor). Reads via ListReports, the
// same source and ?limit convention executionReports already uses.
func (h *handlers) executionTrend(w http.ResponseWriter, r *http.Request) {
	executionID, ok := pathInt(r, "execution_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid execution id")
		return
	}
	if err := h.authorizeReport(r, executionID); err != nil {
		respondError(w, err)
		return
	}
	reps, err := h.deps.Reports.ListReports(r.Context(), executionID, queryInt(r, "limit"))
	if err != nil {
		respondError(w, err)
		return
	}
	trend := report.BuildTrend(reps)
	trend.ExecutionID = executionID // reps may be empty; the path param is authoritative either way
	writeJSON(w, http.StatusOK, toTrendResponse(trend))
}

type signatureHistoryRowResponse struct {
	Label        string `json:"label"`
	ResponseCode string `json:"response_code,omitempty"`
	Side         string `json:"side"`
	TotalCount   int64  `json:"total_count"`
	RunCount     int    `json:"run_count"`
}

type signatureBreakdownResponse struct {
	Key        string                        `json:"key"`
	TotalCount int64                         `json:"total_count"`
	Rows       []signatureHistoryRowResponse `json:"rows"`
}

type errorSignatureHistoryResponse struct {
	ExecutionID int64                        `json:"execution_id"`
	GroupedBy   string                       `json:"grouped_by"`
	Groups      []signatureBreakdownResponse `json:"groups"`
}

func toErrorSignatureHistoryResponse(executionID int64, by report.SignatureGroupBy, groups []report.SignatureBreakdown) errorSignatureHistoryResponse {
	out := make([]signatureBreakdownResponse, len(groups))
	for i, g := range groups {
		rows := make([]signatureHistoryRowResponse, len(g.Rows))
		for j, r := range g.Rows {
			rows[j] = signatureHistoryRowResponse{
				Label: r.Label, ResponseCode: r.ResponseCode, Side: string(r.Side),
				TotalCount: r.TotalCount, RunCount: r.RunCount,
			}
		}
		out[i] = signatureBreakdownResponse{Key: g.Key, TotalCount: g.TotalCount, Rows: rows}
	}
	return errorSignatureHistoryResponse{ExecutionID: executionID, GroupedBy: string(by), Groups: out}
}

// executionErrorSignatureHistory returns an execution's error signatures
// aggregated across every run, grouped by label (?by=label, the default) or
// by response code (?by=code) -- independently, per the Phase 4 annotation
// that anticipated exactly this. Authorization matches the sibling reports
// routes.
func (h *handlers) executionErrorSignatureHistory(w http.ResponseWriter, r *http.Request) {
	executionID, ok := pathInt(r, "execution_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid execution id")
		return
	}
	if err := h.authorizeReport(r, executionID); err != nil {
		respondError(w, err)
		return
	}
	by := report.GroupByLabel
	if r.URL.Query().Get("by") == "code" {
		by = report.GroupByResponseCode
	}
	rows, err := h.deps.Reports.ErrorSignatureHistory(r.Context(), executionID)
	if err != nil {
		respondError(w, err)
		return
	}
	groups := report.GroupSignatureHistory(rows, by)
	writeJSON(w, http.StatusOK, toErrorSignatureHistoryResponse(executionID, by, groups))
}

// runShardLog serves a shard's captured engine output, durable after the pod
// that produced it is deleted.
func (h *handlers) runShardLog(w http.ResponseWriter, r *http.Request) {
	h.runShardObject(w, r, "log", "text/plain; charset=utf-8")
}

// runShardConfig serves a shard's compiled Taurus config exactly as the run
// used it -- a later re-deploy does not change what this returns.
func (h *handlers) runShardConfig(w http.ResponseWriter, r *http.Request) {
	// text/plain rather than a YAML content-type: what matters is that a
	// browser or curl renders it readably, and there is no registered
	// application/yaml behaviour worth relying on across clients.
	h.runShardObject(w, r, "yml", "text/plain; charset=utf-8")
}

// runShardObject serves the object a run/scenario/shard key addresses. Log and
// config are the only two kinds task 30 exposes; kind is which one, and
// selects the extension the writing side used.
func (h *handlers) runShardObject(w http.ResponseWriter, r *http.Request, kind, contentType string) {
	runID, ok := pathInt(r, "run_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	scenarioID, ok := pathInt(r, "scenario_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scenario id")
		return
	}
	// Bounded before the int conversion below, not merely parsed. pathInt
	// returns an int64 and RunShardKey takes an int, so on a 32-bit build a
	// path like /shard/4294967296 would silently wrap to 0 and serve shard
	// 0's log under a request for a shard that does not exist. Rejecting is
	// the honest answer: a shard index is a small ordinal, and nothing
	// legitimate reaches even MaxInt32.
	shard, ok := pathInt(r, "shard")
	if !ok || shard < 0 || shard > math.MaxInt32 {
		writeError(w, http.StatusBadRequest, "invalid shard")
		return
	}
	// A shard object is run-keyed but owned by the run's execution:
	// authorize report:read against that execution's tenant (Phase 20) --
	// before touching the object store.
	if err := h.authorizeRunShard(r, runID); err != nil {
		respondError(w, err)
		return
	}
	key := lifecycleapp.RunShardKey(runID, scenarioID, int(shard), kind)
	data, err := h.deps.Store.Download(r.Context(), key)
	if err != nil {
		respondError(w, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	// #nosec G705 -- Content-Type is text/plain (set above); a captured engine
	// log or config is never interpreted as HTML/JS, so there is no XSS sink.
	_, _ = w.Write(data)
}

// queryInt parses an optional integer query parameter, defaulting to 0
// (ListReports/ReportsSince read that as "no limit") when absent or unparsable
// -- a malformed limit degrading to "everything" is safer than rejecting an
// otherwise valid request over a hint.
func queryInt(r *http.Request, name string) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return 0
	}
	return v
}

// --- Share links (phase 34) -------------------------------------------------

// shareMaxExpiryHours caps the lifetime a share link may be minted with
// (720h = 30 days). Clamped, not rejected: an over-long ask still gets the
// longest link the deployment hands out -- the asker can already read the
// report, so the cap protects the link's afterlife, not the report.
const shareMaxExpiryHours = 720

// shareTokenHexBytes is a share token's entropy: 32 crypto/rand bytes,
// served as 64 lowercase hex chars. A share link is a bare capability, so
// guessing a live token must be harder than being handed one.
const shareTokenHexBytes = 32

// shareMaxBodyBytes bounds the issue request's body, which is one optional
// integer and nothing else.
const shareMaxBodyBytes = 1 << 14

// shareResponse is the wire shape of POST /api/runs/{run_id}/share: the
// token, the SPA path that resolves it, and when -- if ever -- the link
// dies.
type shareResponse struct {
	Token     string  `json:"token"`
	URL       string  `json:"url"`
	ExpiresAt *string `json:"expires_at"`
}

// shareLinkResponse is one row of GET /api/runs/{run_id}/share: what the
// issuing UI needs to show and revoke existing links. The token itself
// rides along -- revoking is keyed by it, and re-showing an existing link
// must not require minting another.
type shareLinkResponse struct {
	Token       string  `json:"token"`
	CreatedBy   string  `json:"created_by"`
	CreatedTime string  `json:"created_time"`
	ExpiresAt   *string `json:"expires_at"`
}

// authorizeShareGate is the issue/list/delete gate: the report read the
// link will grant, checked before any token is minted or listed -- an
// unknown run 404s exactly like the report route, and report:read against
// the owning execution's tenant (Phase 20) decides who may hand that
// report to the world.
func (h *handlers) authorizeShareGate(r *http.Request, runID int64) error {
	rep, err := h.deps.Reports.GetReport(r.Context(), runID)
	if err != nil {
		return err
	}
	return h.authorizeReport(r, rep.ExecutionID)
}

// createRunShare issues a share link for a run's report. The body is
// optional: absent mints a never-expiring link, {"expires_in_hours": N}
// one that dies after N hours (clamped to shareMaxExpiryHours). Multiple
// live tokens per run are allowed -- two customers, two links, one
// revocable without breaking the other.
func (h *handlers) createRunShare(w http.ResponseWriter, r *http.Request) {
	if h.deps.Shares == nil {
		writeError(w, http.StatusNotFound, "share links not configured")
		return
	}
	runID, ok := pathInt(r, "run_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	var body struct {
		ExpiresInHours *int64 `json:"expires_in_hours"`
	}
	// An absent body decodes to EOF -- that is the no-expiry default, not a
	// malformed request.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, shareMaxBodyBytes)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "body must be JSON with an optional expires_in_hours")
		return
	}
	var expires *time.Time
	if body.ExpiresInHours != nil {
		if *body.ExpiresInHours <= 0 {
			writeError(w, http.StatusBadRequest, "expires_in_hours must be positive")
			return
		}
		t := time.Now().Add(time.Duration(min(*body.ExpiresInHours, shareMaxExpiryHours)) * time.Hour).UTC()
		expires = &t
	}
	if err := h.authorizeShareGate(r, runID); err != nil {
		respondError(w, err)
		return
	}
	raw := make([]byte, shareTokenHexBytes)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand failing is the platform's entropy source failing;
		// nothing about the request can fix that.
		slog.Error("httpapi: share token entropy", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	token := hex.EncodeToString(raw)
	createdBy := accountFrom(r.Context()).Name
	if err := h.deps.Shares.CreateShare(r.Context(), runID, token, createdBy, expires); err != nil {
		respondError(w, err)
		return
	}
	resp := shareResponse{Token: token, URL: "/share/" + token}
	if expires != nil {
		s := expires.Format(time.RFC3339)
		resp.ExpiresAt = &s
	}
	writeJSON(w, http.StatusCreated, resp)
}

// listRunShares lists a run's links in issue order, so the share dialog can
// show what is already out in the world and revoke it.
func (h *handlers) listRunShares(w http.ResponseWriter, r *http.Request) {
	if h.deps.Shares == nil {
		writeError(w, http.StatusNotFound, "share links not configured")
		return
	}
	runID, ok := pathInt(r, "run_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	if err := h.authorizeShareGate(r, runID); err != nil {
		respondError(w, err)
		return
	}
	shares, err := h.deps.Shares.ListSharesByRun(r.Context(), runID)
	if err != nil {
		respondError(w, err)
		return
	}
	out := make([]shareLinkResponse, 0, len(shares)) // always an array, never null
	for _, s := range shares {
		row := shareLinkResponse{
			Token: s.Token, CreatedBy: s.CreatedBy,
			CreatedTime: s.CreatedTime.UTC().Format(time.RFC3339),
		}
		if s.Expires != nil {
			v := s.Expires.UTC().Format(time.RFC3339)
			row.ExpiresAt = &v
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// deleteRunShare revokes one link. The token is a path segment, not a body:
// the revoke URL is the share URL's management twin.
func (h *handlers) deleteRunShare(w http.ResponseWriter, r *http.Request) {
	if h.deps.Shares == nil {
		writeError(w, http.StatusNotFound, "share links not configured")
		return
	}
	runID, ok := pathInt(r, "run_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	if err := h.authorizeShareGate(r, runID); err != nil {
		respondError(w, err)
		return
	}
	if err := h.deps.Shares.DeleteShare(r.Context(), runID, r.PathValue("token")); err != nil {
		respondError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sharedReport is the public face of a share link: the run report payload
// IDENTICAL to GET /api/runs/{id}/report -- the same withCriteriaVerdict
// layer, criteria and failing_criteria included -- served to callers with
// no session at all (the route sits in publicAPIPath, outside the auth
// middleware; the token is the entire authorization). Exactly one run's
// report and nothing else: no execution listing, no shard objects. Unknown
// and expired tokens answer the same 404, so a guesser learns nothing
// about which tokens exist.
func (h *handlers) sharedReport(w http.ResponseWriter, r *http.Request) {
	if h.deps.Shares == nil {
		writeError(w, http.StatusNotFound, "share links not configured")
		return
	}
	share, err := h.deps.Shares.GetShareByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		respondError(w, err)
		return
	}
	if share.ExpiredAt(time.Now()) {
		writeError(w, http.StatusNotFound, "share link expired")
		return
	}
	rep, err := h.deps.Reports.GetReport(r.Context(), share.RunID)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.withCriteriaVerdict(r, rep))
}
