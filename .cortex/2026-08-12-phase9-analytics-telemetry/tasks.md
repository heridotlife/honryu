# Phase 9 — Analytics — Tasks

Global numbering continues Phase 8 (86–102). Phase 9 is **103–111**.

Spec: `.cortex/2026-08-12-phase9-analytics-telemetry/spec.md` · Plan: `plan.md`.

Scope: read-side analytics only. Telemetry correlation (traceparent/APM) is
deferred to its own future phase.

Groups: **103** verdict target-QPS gate · **104–106** campaign-over-campaign
comparison · **107–108** per-service trends · **109–110** error-signature
history · **111** verification.

---

## Group A — the gate (verifiable first)

### 103. Fold target-QPS into the campaign verdict — done, `6ebc260`
- **Files:** `internal/app/campaignapp/verdict.go`, `internal/app/campaignapp/verdict_test.go`
- **Criteria:** `ServiceVerdict` gains `ShortOfTargetQPS bool` plus the
  requested/achieved throughput so a no-go can name the shortfall;
  `serviceVerdict` sets `ShortOfTargetQPS` from the report's
  `report.ShortOfRequest()`; the `Go` gate also fails when a service is short of
  target QPS. A service that requested no target QPS (`Requested.Throughput <= 0`)
  is unaffected; a service that `passed` its criteria but achieved <95% of its
  target is `no-go` with the numbers surfaced.
- **Satisfies:** spec "Approach — the one domain change"; AC1, AC2
- **Depends on:** —

## Group B — campaign-over-campaign comparison

### 104. Add the pure comparison classifier — done, `0e94eff`
- **Files:** `internal/domain/campaign/compare.go`, `internal/domain/campaign/compare_test.go`
- **Criteria:** `Compare(current, baseline []ServiceSignal) []ServiceComparison`
  where `ServiceSignal` is a minimal `{ProjectID int64; Go bool}`; each project is
  classified as `improved` (no-go→go), `regressed` (go→no-go), `newly-at-risk`
  (no-go now, absent from baseline), `still-at-risk` (no-go in both), `new`
  (go now, absent from baseline), or `dropped` (present in baseline only). Matched
  by `ProjectID`. Table-tested over every transition.
- **Satisfies:** spec "Approach — campaign comparison"; AC3, AC4
- **Depends on:** —

### 105. Add `campaignapp.Compare` (baseline resolution + verdict mapping) — done, `6b38e21`
- **Files:** `internal/app/campaignapp/comparison.go`, `internal/app/campaignapp/comparison_test.go`
- **Criteria:** resolves the baseline campaign — the tenant's most-recent-prior
  campaign by `window_start` (excluding the target), or an explicit baseline id —
  computes both campaigns' verdicts (reusing `Verdict`, so the target-QPS gate
  applies to both sides), maps each `ServiceVerdict` to a `ServiceSignal`, and
  calls `campaign.Compare`; a target campaign with no prior returns an
  explanatory empty comparison, not an error. No new storage.
- **Satisfies:** spec "Approach — campaign comparison"; AC3, AC9
- **Depends on:** 103, 104

### 106. Expose `GET /api/campaigns/{campaign_id}/comparison` — done, `53f1cc8`
- **Files:** `internal/adapters/httpapi/campaign_handlers.go`, `internal/adapters/httpapi/router.go`, `api/openapi.yaml`, tests
- **Criteria:** `?baseline={campaign_id}` overrides the default baseline; the
  route is authorized exactly like the verdict route; the openapi parity + tag
  tests pass; the no-prior-campaign case returns a clear empty/explanatory
  response (not 404/500).
- **Satisfies:** spec "Approach — HTTP"; AC3, AC8
- **Depends on:** 105

## Group C — per-service trends

### 107. Add the pure trend builder — done, `1113243`
- **Files:** `internal/domain/report/trend.go`, `internal/domain/report/trend_test.go`
- **Criteria:** `BuildTrend([]Report) Trend` over reports ordered most-recent
  first; each point carries achieved QPS, p50/95/99, error rate, outcome, and the
  hit-target-QPS signal; a point is flagged `regressed` iff its hit-target-QPS
  signal flipped from a *comparable* predecessor, and `no baseline` when it has no
  comparable predecessor. `comparable(a, b)` = same execution + requested load
  (concurrency/throughput/duration) + engine + cluster, a table-tested predicate.
  p95/p99/error-rate are carried as raw series, never auto-flagged.
- **Satisfies:** spec "Approach — per-service trends"; AC5, AC6
- **Depends on:** —

### 108. Expose `GET /api/executions/{execution_id}/trend` — done, `d6810c2`
- **Files:** `internal/adapters/httpapi/report_handlers.go` (or a new `analytics_handlers.go`), `internal/adapters/httpapi/router.go`, `api/openapi.yaml`, tests
- **Criteria:** reads the execution's reports via `ListReports(exec, limit)`
  (reusing the existing `limit` convention) and returns `BuildTrend`'s series;
  authorized like the reports routes; openapi parity/tag tests pass.
- **Satisfies:** spec "Approach — HTTP"; AC5, AC8
- **Depends on:** 107

## Group D — error-signature history

### 109. Add the cross-run error-signature-history read query — done, `1c8680f`
- **Note:** `SignatureHistoryRow` landed in `internal/domain/report/signature.go`
  (not `internal/ports`, as the file list implied) -- required so task 110's
  pure grouping function can consume it without a domain package importing
  ports, per "domain imports no ports."
- **Files:** `internal/ports/reportstore.go`, `internal/adapters/repo/mysql/report_store.go`, `internal/ports/fake/reportstore.go`, `internal/ports/reportstoretest/contract.go`, mysql integration test
- **Criteria:** a new read method aggregates `report_error_signature` across an
  execution's runs — JOIN `execution_report` on `run_id` WHERE `execution_id = ?`
  — returning per-`(label, response_code, side)` cross-run totals (summed count)
  and how many runs each appeared in. The fake and the MySQL adapter both pass a
  repository conformance contract for it.
- **Satisfies:** spec "Approach — error-signature history"; AC7, AC9
- **Depends on:** —

### 110. Aggregate + expose `GET /api/executions/{execution_id}/error-signatures` — done, `d7e9f75`
- **Files:** `internal/domain/report/signature_history.go` (+ test), `internal/adapters/httpapi/report_handlers.go`, `internal/adapters/httpapi/router.go`, `api/openapi.yaml`, tests
- **Criteria:** a pure domain grouping presents the task-109 rows **by label** and
  **by response code** independently, selected via `?by=label|code` (default
  `label`); authorized like the reports routes; openapi parity/tag tests pass.
- **Satisfies:** spec "Approach — error-signature history"; AC7, AC8
- **Depends on:** 109

## Group E — verification

### 111. e2e — QPS-flip verdict + campaign comparison + trend — done, `1d23eea`
- **Files:** `test/e2e/phase9_e2e_test.go`
- **Criteria:** against real MySQL + the fake scheduler: a service that passes its
  criteria but whose run ingested <95% of its target throughput flips its
  campaign to **no-go** (naming the shortfall); a second campaign compared to the
  first classifies a project as improved/regressed; a per-service trend returns
  the run series. Follows the phase 6/8 e2e harness.
- **Satisfies:** AC1, AC3, AC5, AC10
- **Depends on:** 106, 108, 110
