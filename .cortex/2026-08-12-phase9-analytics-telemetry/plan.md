# Phase 9 — Analytics — Plan

Spec: `.cortex/2026-08-12-phase9-analytics-telemetry/spec.md`. Tasks: `tasks.md`
(103–111). Continues Phases 5/6/7/8's brainstorm → write-plan → execute-plan
cadence. Scoped to **read-side analytics (A)**; telemetry correlation (B) is
deferred to its own future phase.

## Context (what exists now)

- `report.ShortOfRequest()` (`internal/domain/report/report.go`) is the exact
  target-QPS primitive: returns false when `Requested.Throughput <= 0` (no target
  — unlimited), else `Achieved.Throughput < 0.95 * Requested.Throughput`. Nothing
  consumes it for the campaign verdict yet.
- The campaign verdict (`internal/app/campaignapp/verdict.go`): `serviceVerdict`
  (`:174`) reads the designated execution's latest report via
  `ListReports(exec, 1)`; the `Go` gate (`:87`) is outcome-only
  (`!sv.HasReport || sv.Outcome != taurus.OutcomePassed`); a `CalibrateEngine`
  execution is already skipped (`:80`).
- Report persistence: `execution_report` (per run, now carrying `cluster`) +
  `report_error_signature` (keyed `(run_id, label, response_code, side)`, with a
  derived `share`). `ListReports(exec, limit)`, `ReportsSince`, `GetReport`, and
  `errorSignatures(runID)` exist (`report_store.go`); the signature read is
  **per-run only** — there is no cross-run history query.
- Campaign services are keyed by `ProjectID` (a project appears at most once per
  campaign — `campaign.go:82`); `ListCampaignsByTenant` returns a tenant's
  campaigns `ORDER BY window_start` (`campaign_repository.go`).
- `report.EvaluateCriteria` + `report.FailedCriterion` already back
  `ServiceVerdict.FailingCriteria`.
- HTTP: `GET /api/campaigns/{id}/verdict`, `/api/executions/{id}/reports`,
  `/api/runs/{run_id}/report`. The openapi parity + tag tests
  (`openapi_test.go`) enforce that every route is documented in
  `api/openapi.yaml` with its route group as the operation tag.

## Approach

One domain change: fold target-QPS into the verdict via the existing
`ShortOfRequest`. Everything else is read-side and pure. Two pure classifiers
live in the **existing** domain packages, not a new one: `campaign.Compare`
(over minimal `ServiceSignal{ProjectID, Go}` matched by project) and
`report.BuildTrend` (over an execution's reports, with a comparable-predecessor
predicate). App services resolve the inputs — the baseline campaign, the report
list — and call the pure functions. A new cross-run error-signature query
(JOIN `report_error_signature` with `execution_report`) feeds a pure grouping.
Three new read HTTP endpoints. **No new persistence table.**

Chosen over a **precomputed analytics/trends table**: the source rows already
exist and per-execution volumes are modest, so query-time computation is
simpler, always fresh, and adds no write path or migration — reconsidered only
if a long-history trend query becomes a measured hotspot.

## Risks

1. **The verdict change flips currently-green campaigns red.** →
   `ShortOfRequest`'s no-target guard means only a service that set a target QPS
   *and* missed it flips; verdict tests assert a no-target service and a
   target-hit service stay `go`, and the e2e proves a real QPS shortfall flips
   the campaign.
2. **"Comparable run" is 4-dimensional and easy to get subtly wrong.** →
   Encoded as a pure predicate (same execution + requested load + engine +
   cluster) with table tests over each dimension differing; "no baseline" is a
   distinct state, never a false regression.
3. **Baseline resolution is ambiguous.** → Default is the tenant's most-recent
   prior campaign by `window_start` (excluding this one); an explicit baseline id
   overrides; the "no prior campaign" case returns an explanatory empty result,
   not an error.
4. **Cross-run signature query correctness** (the JOIN, independent by-label /
   by-code grouping). → MySQL conformance for the new read query, mirroring the
   existing report-store contract.
5. **The openapi parity test breaks on undocumented routes.** → Each HTTP task
   adds the route and its `openapi.yaml` entry (with the correct tag) together.

## Out of scope

Telemetry correlation (B — traceparent/APM header injection); any new
persistence table or rollup job; the read-only UI; per-service QPS-tolerance
override; auto-flagging of noisy metrics (p95/p99/error-rate) as regressions;
cross-tenant analytics.

## Verification

`go build`/`vet`/`gofmt`/`golangci-lint`; unit tests for the verdict extension,
the two pure classifiers, and the signature grouping; MySQL conformance for the
new error-signature-history query; e2e covering a campaign flipped to no-go on
QPS plus a campaign comparison and a trend; `scripts/coverage.sh` as a check
against the repo baseline.
