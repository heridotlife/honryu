# Phase 22 Plan — Tier 2 UI + prod data bootstrap
Spec: .cortex/2026-09-05-phase22-tier2-ui/spec.md

## Block D — environment bootstrap (task 0, no code)
Task 0 (manual, by operator-not-pi): register Talos cluster (POST /api/clusters
or chart-side registration path per phase-16), fire 2 executions against the
demo target with small load (2 VU / 30s each), verify intervals land
(execution_report_series rows > 0, /api/runs/{id}/series non-empty), verify
phase-21 charts render. Output: run IDs recorded in PROGRESS.md.

## Block A — live run chart (Execution.tsx + TimeSeriesChart)
Task 1: pure reducer `liveSeries(events: ReceivedMetric[]): SeriesPoint[]`
 — aggregates EngineMetric stream into per-second buckets {t, vus, rps, p95}.
   Hostile fixtures: empty, single event, out-of-order timestamps, label mix.
   No React, no SSE — unit-testable.
Task 2: `useLiveSeries(executionId)` hook — wraps streamExecutionMetrics +
   eventsRef/prune logic (currently inline in Execution.tsx), exposes
   {series, connected}. Extraction, not rewrite; existing tests keep passing.
Task 3: Execution page live chart section — TimeSeriesChart fed by useLiveSeries,
   percentile toggle (p50/p95/p99), idle state ("waiting for first events…"),
   running state with VUs + req/s dual series. layout-check assertion.
Task 4: vitest coverage for 1–3 + Execution.test.ts extensions; tsc clean.

## Block B — visual stage editor (NewTest step 5)
Task 5: pure `stagesToConfig(rows: StageRow[], scenarioId): ConfigJSON` and
   `configToStages(cfg): StageRow[]` — round-trip pair, mirrors buildConfig
   output byte-for-byte for single-row case. Fixtures incl. zero-throughput
   (unlimited), multi-row.
Task 6: `StageEditor` component — table rows (concurrency, rampup, throughput,
   duration), add/remove, validation copy mirroring Entry.Validate errors,
   raw-JSON toggle (textarea ↔ table, content preserved).
Task 7: NewTest integration — replace step-5 JSON textarea with StageEditor;
   submit path unchanged (same PUT payload); tests for flow.

## Block C — capacity meters (Clusters page)
Task 8: `useClusterCapacity` fetch (existing GET /api/clusters/{name} fields —
   extend api/clusters.ts types only if needed); `CapacityMeter` component
   (plain SVG gauge or bar, house style) per cluster row.
Task 9: Clusters page integration + honest empty state + layout-check row.

## Block E — close
Task 10: layout-check extensions for all new surfaces (task 3/7/9 assertions).
Task 11: full gate run (make test, coverage, lint, vitest, tsc, build,
   layout-check vs prod), PROGRESS.md, PR.

## Batching for pi (quota-gated, 32M/5h, halt ≥70%)
- B1 = tasks 1–4 (live chart) — biggest, chart + hook + page
- B2 = tasks 5–7 (stage editor)
- B3 = tasks 8–9 (capacity) + 10–11 (close + gates)
- Task 0 runs before B1 (live chart needs a live run to eyeball, and its
  fixtures come from real stream shapes).

## Verification discipline (unchanged from phase 20/21)
- pi never pushes/merges/branches; one commit per task; tests before commit.
- Every pi claim re-verified by operator: git log, gate re-run, browser check.
- Deviations documented in PROGRESS.md + PR comment, not silently smoothed.
