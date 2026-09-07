# Phase 21 — Tasks
Plan: `.cortex/2026-09-04-phase21-results-ui/plan.md`
Spec: `.cortex/2026-09-04-phase21-results-ui/spec.md`
Hard gate: **A green before B; B.4 (primitive) green before C and D.**

## Block A — Series backend

### 1. Pure series builder
- **Files:** `internal/app/reportapp/series.go`, `internal/app/reportapp/series_test.go`
- **Criteria:** `BuildSeries(intervals []metrics.Interval, pcts []float64) []SeriesPoint` merges same-`ts` intervals across pods (sum concurrency/samples/succeeded/failed/bytes, union latency histograms), dedupes by `(shard, seq)` keeping the highest seq, returns ascending-`ts` points `{Ts, VUs, RPS, ErrPct, Latency map[pct]float64}`. Table tests: duplicate-superset batch ≡ clean batch; pod skew (same ts, 2 pods) sums; empty input → empty output; single-pod percentiles match direct histogram percentile.
- **Satisfies:** G1 (data), AC1
- **Depends on:** none

### 2. IntervalRepository.ListIntervalsByRun + MySQL + fake + contract
- **Files:** `internal/ports/interval_repository.go` (or existing file), `internal/adapters/repo/mysql/interval_repository.go`, `internal/ports/fake/interval_repository.go`, `internal/ports/repositorytest/interval_contract.go`
- **Criteria:** Port method returns all intervals for a run ordered by (shard, seq) or (ts, shard, seq); MySQL impl tested against live DB harness; fake + contract suite pattern-matched to campaign_repository (phase 20).
- **Satisfies:** G1
- **Depends on:** none

### 3. GET /api/runs/{run_id}/series endpoint
- **Files:** `internal/adapters/httpapi/report_handlers.go`, `report_handlers_test.go` (or new series_handlers), `api/openapi.yaml`
- **Criteria:** Responds `{points: [{ts, vus, rps, err_pct, latency:{"50":x,"90":…}}]}` computed via BuildSeries with pcts [50,90,95,99]. RBAC: same gate as run report (run read). 404 unknown run. OpenAPI documented. Handler test with fake repo covering happy + empty + 404.
- **Satisfies:** G1, AC1
- **Depends on:** 1, 2

## Block B — Chart primitive + run-report page

### 4. TimeSeriesChart primitive
- **Files:** `web/src/components/charts/TimeSeriesChart.tsx`, `TimeSeriesChart.test.tsx`
- **Criteria:** Props `{series: {name, color, points: {x,y}[]}[], yLabel, height, xType:'time'}`. Plain SVG, axes + 4-5 ticks + gridlines, legend, hover crosshair with readout of all series at nearest x, responsive via viewBox. No new deps. Vitest: series render count, tick math, nearest-point hover math, empty-series edge.
- **Satisfies:** G1, AC2
- **Depends on:** none (web-only)

### 5. Series API client
- **Files:** `web/src/api/series.ts`, `series.test.ts`
- **Criteria:** `fetchSeries(runId)` typed to endpoint shape; error mapping consistent with client.ts conventions; tests with mocked apiClient.
- **Satisfies:** G1
- **Depends on:** 3 (shape)

### 6. Run report page — time-series section
- **Files:** `web/src/pages/Reports.tsx`, `Reports.test.ts`
- **Criteria:** New "Time series" card: VUs+RPS dual chart, error-rate chart, latency chart. Percentile selector pills (p50/p90/p95/p99) switch latency series client-side without refetch (test spies apiClient call count). Loading + error + empty states. Charts from fixture data render without console errors.
- **Satisfies:** G1, G5, AC2, AC6
- **Depends on:** 4, 5

### 7. Requested-vs-achieved overlay chart
- **Files:** `web/src/pages/Reports.tsx` (extend), `Reports.test.ts`
- **Criteria:** TimeSeriesChart with two series — requested (from report.requested, expanded to a constant/step line across run duration) vs achieved VUs (from series). Legend labels "requested"/"achieved". Test asserts 2 rendered series + legend text; divergence case (achieved < requested) renders distinct paths.
- **Satisfies:** G3, AC4
- **Depends on:** 6

## Block C — Labels table

### 8. LabelsTable component + integration
- **Files:** `web/src/components/LabelsTable.tsx`, `LabelsTable.test.tsx`, `web/src/pages/Reports.tsx`
- **Criteria:** Sortable (label, samples, error rate, p50, p95, p99 from `latency` record). Em-dash for missing percentile. Card hidden entirely when `labels` absent/empty. Tests: sort toggling each column, hidden card, em-dash cells.
- **Satisfies:** G2, AC3
- **Depends on:** 4 (may precede 6; needs only primitive ready)

## Block D — Run comparison

### 9. RunCompare page + delta math
- **Files:** `web/src/pages/RunCompare.tsx`, `RunCompare.test.tsx`, `web/src/api/comparison.ts` (extend if needed)
- **Criteria:** Route `/executions/:id/compare?runs=a,b`. Two run selectors; delta table (p50/p95/p99 %, RPS %, error-rate delta) with signed percentages, improvement green / regression red; overlaid p95 TimeSeriesChart. Tests: delta math, color semantics, selector wiring, no-runs state.
- **Satisfies:** G4, AC5
- **Depends on:** 4, 5

### 10. Route + nav registration
- **Files:** `web/src/App.tsx`, `web/src/components/DashboardLayout.tsx`, related tests
- **Criteria:** Compare route registered; "Compare runs" nav item follows persona visibility (read-capable roles); per-persona nav tests updated (pattern from phase 20 t23). Deep-link `?runs=a,b` preselects.
- **Satisfies:** AC7 (part), G4
- **Depends on:** 9

## Block E — Close

### 11. layout-check extension + local demo verification
- **Files:** `web/scripts/layout-check.js`
- **Criteria:** New routes asserted for personas that can read executions/reports; compare route included. `bun run layout-check` green against local demo stack. No console errors on new pages.
- **Satisfies:** AC2, AC7
- **Depends on:** 6, 8, 9, 10

### 12. Phase close: docs + full gates
- **Files:** `.cortex/2026-09-04-phase21-results-ui/PROGRESS.md`, spec status flip
- **Criteria:** `make test` + integration + coverage ≥ 90% + golangci + `bun run build` + `tsc -b` + vitest + layout-check all green, documented in PROGRESS.md; deviations logged honestly.
- **Satisfies:** AC8
- **Depends on:** all

## Execution log — batch 1 (tasks 1–3), 2026-09-04

Deviations found while executing Block A, documented in commit bodies:

1. **Spec premise "backend stores report.Intervals" is false.** Intervals are
   transient: Absorb → bounded working state → report → Discard (migration 0018
   says the buckets are "gone once the pods are"). Task 2 therefore added the
   write side the plan assumed existed: migration 0053 `execution_report_series`
   (permanent, per-second, pod/label-merged, written inside Absorb's tx so the
   shard watermark dedups retries before any row lands), not just a read port.
   Runs finalised before this phase have a report but no series → `points: []`.
2. **Dedup key in task 1 ("(shard, seq) keeping highest seq") is not computable
   from `[]metrics.Interval`** — shard/stream live on Batch, and per-stream
   seqs collide across pods at run start. BuildSeries dedups exact
   (ts, label, seq) repeats only; cross-pod summing stays in Absorb where the
   watermark lives. Documented in series.go.
3. Task 1 also delivered the domain merge rule `report.MergeSecond` (used by
   BuildSeries, the MySQL adapter, and the fake) so the series and the report
   cannot disagree about what a second means.

## Execution log — batch 2 (tasks 4–7), 2026-09-04

All four committed on `feat/phase21-results-ui`; 229 vitest tests + `tsc -b` + `bun run build` green before each commit. Deviations, also in commit bodies:

1. **Reports.test.ts → Reports.test.tsx** (git mv, task 6): the mounted tests
   need JSX for the MemoryRouter/Routes harness; esbuild refuses JSX in `.ts`.
2. **Hover-leave test dispatches `pointerout` with relatedTarget null** (task 4):
   React derives enter/leave from native out/over, so a natively dispatched
   `pointerleave` is inert in jsdom. (Separately, vitest's failure formatter
   crashes pretty-printing an SVG node, masking the first AssertionError as a
   TypeError — scratch-test bisect found both.)
3. **VUs and RPS share one y axis** on the dual chart, exactly as task 6
   specified a dual-series single chart; the legend names each unit so the
   mixed scale reads honestly.
4. Task 7: `Load` has no stages field (read as instructed) — requestedLine
   expands a constant line; duration_seconds extends it to first+duration.

## Execution log — batch 3 (tasks 8–10), 2026-09-04

All three committed on `feat/phase21-results-ui`; 256 vitest tests + `tsc -b`
+ `bun run build` green before each commit. Deviations, also in commit bodies:

1. **Task 9 did not touch `web/src/api/comparison.ts`** — it serves the
   campaign comparison domain. The run list already ships via
   `listExecutionReports` and series via `fetchSeries`, so no new fetcher.
2. **Task 10: `DashboardLayout.tsx` unchanged.** The top nav is a flat
   fixed-href surface list (`navItemsFor`); an execution-scoped route cannot
   be a top-nav item, so the task's offered alternative carries the
   registration: a "Compare runs" link on the Reports page header, gated by
   `can('report','read')` (the Reports nav item's exact grant) and shown only
   once the loaded execution has ≥ 2 runs. Per-persona presence tests extend
   `Reports.test.tsx` with DashboardLayout.test.tsx's persona-map pattern;
   the `navItemsFor` assertions themselves are untouched because the top nav
   did not change.
3. Task 9 semantics choice: delta reads "B against A" with A = baseline, so
   without `?runs=` the default preselect is A = oldest run, B = newest (the
   reports list arrives most-recent-first). The overlaid-p95 card hides not
   only on an empty series but also on a series fetch failure — the delta
   table stays the page's primary source either way.
