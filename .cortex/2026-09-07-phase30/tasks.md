You are working on Honryu (Go backend + React/TS web).

REPO: /home/coder/personal/honryu
BRANCH: feat/phase30-status-breakdown (checked out, off develop)

Goal: Phase 30 — per-endpoint HTTP status-code breakdown. Engines already push
metrics.Interval.ResponseCodes map[string]int64 per second per label
(internal/domain/metrics/interval.go line 44; Merge at line 100-105 already
sums maps). The accumulator DROPS them. Plumb to the UI.

VERIFIED RECON:
- internal/domain/report/accumulate.go: labelState struct (line 27) holds
  samples/failed/latency only. Add(iv.Label) at ~line 65: st.samples += ...,
  st.failed += ..., st.latency.Merge(...). Report() builds LabelSummary list.
- internal/domain/report/report.go: LabelSummary (line 91-97) — Samples,
  Failed, ErrorRate, Latency Percentiles. Report.Labels []LabelSummary (193).
- internal/adapters/repo/mysql/report_store.go (or report_progress.go):
  persists labels as JSON into execution_report.labels — LabelSummary
  marshals as-is, so a new field rides along FREE (JSON column, no DDL).
- web/src/api/reports.ts: LabelSummary interface — add statuses field.
- web/src/pages/Reports.tsx: Labels tab panel — per-label results table
  (6 cols: Label, Samples, Error rate, p50, p95, p99).

TASKS (one commit each, gates between):

T1 — domain: accumulate response codes per label
- accumulate.go: labelState gains codes map[string]int64 (nil-safe). In Add:
  if len(iv.ResponseCodes) > 0 && st.codes == nil { init }; sum each code.
- Report(): LabelSummary gains Statuses []StatusLabel sorted DESC by count
  then code. New type in report.go:
    type StatusLabel struct {
        Code  string `json:"code"`
        Count int64  `json:"count"`
    }
  LabelSummary field: Statuses []StatusLabel `json:"statuses,omitempty"`.
- accumulate_test.go: interval with ResponseCodes {"200": 90, "500": 10} x2
  merges → LabelSummary.Statuses [{200,180},{500,20}]; absent map → nil.
- go test ./internal/domain/report/ — green.

T2 — store round-trip + wire
- Verify (read the store code first) labels JSON persists LabelSummary as-is.
  Add a store test ONLY if an existing round-trip test file pattern makes it
  cheap; otherwise rely on JSON marshaling (omitempty keeps old rows clean —
  old reports simply lack statuses).
- internal/adapters/httpapi: no change needed if report marshals through.
  Verify with existing tests; add one handler test only if report_handlers_
  test.go has a cheap pattern: report w/ statuses → wire carries statuses.
- go test ./internal/... — green.

T3 — web: Labels tab status breakdown
- web/src/api/reports.ts: LabelSummary gains statuses?: {code: string,
  count: number}[] (normalize absent → undefined ok, do NOT materialize []).
- Reports.tsx Labels tab: under the per-label table row's last cell OR as a
  7th column "Statuses": render compact badges "200×180" / "500×20" (code
  then ×count), 2xx green / 4xx amber / 5xx red, other gray. Sorted by the
  API order (already count-desc). If statuses absent (old runs) render "—".
  data-testid: status-badge-{labelIdx}-{code}.
- Test Reports.test.tsx: label with statuses [{200,180},{500,20}] renders
  both badges with correct text; label without → "—".

GATES:
  T1/T2: go test ./internal/...
  T3: cd web && /home/coder/.bun/bin/bun run vitest run && tsc -b

COMMIT STYLE:
  feat(report): accumulate per-label response codes
  test(report): status round-trip coverage
  feat(web): status badges in labels table

No PR, no push. Verify vitest total grows from 366 and stays green.