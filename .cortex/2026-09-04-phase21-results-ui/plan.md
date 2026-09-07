# Phase 21 — Plan
Spec: `.cortex/2026-09-04-phase21-results-ui/spec.md`

## Context
- **Data exists, unreachable.** `report.Intervals` (per-second, per-pod,
  bucket histograms) persisted via sidecar pushes; no endpoint serves them.
  `GET /api/runs/{run_id}/report` returns the `Report` (incl. `labels`) but
  not the series.
- **Report page today.** `web/src/pages/Reports.tsx` (495 lines): Load /
  percentiles / attribution / signatures / correlation-id / APM-template /
  shard-objects cards. Only chart: `Sparkline` (no-charting-library rule,
  phase 13). `web/src/api/reports.ts` types mirror domain JSON tags exactly.
- **Series math.** `metrics.Interval.Latency` is a `Histogram` of response-
  time buckets per second per pod. Percentiles can't be combined across pods,
  so the server must merge same-second buckets across pods/shards, then
  compute percentiles on the merged histogram per second. Dedup key:
  `(shard, seq)` — sidecar retry semantics send duplicate supersets
  (interval.go documents this contract).
- **House rules.** No charting library (plain SVG + currentColor + Tailwind);
  bun workspace; one commit per task; tests before commit; `layout-check`
  covers new routes per persona; OpenAPI must document new endpoints.

## Block layout
```
A. Series backend (endpoint + pure series math + hostile-fixture tests)   [G1 data]
B. Chart primitive + run-report page integration                          [G1/G3/G5 UI]
C. Labels table + percentile selector                                     [G2/G5]
D. Run comparison view                                                    [G4]
E. Layout-check extension + docs + phase close                            [AC7]
```

## Block A — Series backend
1. **Pure series builder** `internal/app/reportapp/series.go`:
   `BuildSeries(intervals []metrics.Interval, pcts []float64) []SeriesPoint`.
   Merge same-`ts` across pods (sum samples/succeeded/failed/concurrency,
   histogram union), dedupe by `(shard, seq)` keeping highest seq, output
   per-second `{Ts, VUs, RPS, ErrPct, Latency map[pct]float64}`.
   Table-driven tests: duplicate-superset batch, pod skew (same ts, different
   pods), empty input, single-pod.
2. **Endpoint** `GET /api/runs/{run_id}/series`: handler pulls intervals
   (needs repo method `ListIntervalsByRun(ctx, runID)`), calls BuildSeries
   with fixed pcts `[50, 90, 95, 99]`, responds `[{ts, vus, rps, err_pct,
   latency:{"50":…}}]`. OpenAPI entry. Conformance test via httpapi harness.
3. **Repo method** `ListIntervalsByRun` on IntervalRepository port + MySQL
   impl + fake + contract test. (If a suitable read method already exists,
   reuse; verify during task 1.)

## Block B — Chart primitive + page
4. **TimeSeriesChart primitive** `web/src/components/charts/TimeSeriesChart.tsx`:
   props `{series: {name, color, points}[], yLabel, xType: 'time', height}`.
   Plain SVG: x/y axes, 4-5 ticks, gridlines, legend, hover crosshair +
   readout (nearest point), `currentColor`-based stroke colors, responsive
   via viewBox + container width. Vitest: renders N series, tick generation,
   hover math (faked pointer events), empty series edge.
5. **Series fetcher** `web/src/api/series.ts` + tests.
6. **Run report page** `Reports.tsx` gains "Time series" section: VUs+RPS
   dual chart, error-rate chart, latency chart with **percentile selector**
   (p50/p90/p95/p99 pills) switching series client-side (server sends all
   percentiles per point). Tests: selector switches series without refetch
   (spy on apiClient), charts render from fixtures, loading/error states.
7. **Requested-vs-achieved overlay**: same chart, two series (requested
   constant/staged vs achieved VUs). Legend "requested/achieved". Test:
   divergence visible = two distinct lines (assert on rendered series count
   and legend labels).

## Block C — Labels table
8. **LabelsTable component** `web/src/components/LabelsTable.tsx`:
   sortable columns (label, samples, error rate, p50/p95/p99 from
   `latency` map), em-dash for missing percentile, whole-card hidden when
   `labels` absent, sparkline per row (samples distribution not available —
   skip sparkline, table only). Page integration + tests (sort logic,
   absent-labels hidden, em-dash rendering).

## Block D — Comparison
9. **Compare view** `web/src/pages/RunCompare.tsx` (route `/executions/:id/
   compare?runs=a,b`): two dropdowns (runs of execution), delta table (p50/
   p95/p99 %, RPS %, error-rate delta), overlaid p95 chart (TimeSeriesChart,
   two series). Tests: delta math, same-direction color semantics, dropdown
   wiring.
10. **Nav + route registration**: add "Compare runs" entry visible to
    viewers+ (read-only), DashboardLayout link, router path in App.tsx.

## Block E — Close
11. **layout-check extension**: new routes + per-persona nav assertions for
    compare route; run against local demo stack.
12. **Docs**: spec status → complete; PROGRESS.md; README screenshots section
    stub (optional, cut if time).
```
Do not start B before A is green. C and D depend on B.4 (primitive).
```

## Risks / mitigations
- **Interval data volume**: runs have `pods × seconds` intervals; series
  endpoint must aggregate server-side (never ship raw intervals). Mitigated
  by design (AC1).
- **Histogram merge math wrong** → wrong percentiles silently. Mitigated by
  hostile fixtures in A.1 (duplicate superset, pod skew) + property-ish test:
  merged percentiles of single-pod run equal direct computation.
- **SVG chart a11y/resize**: hand-rolled charts regress on small screens.
  Mitigated: AC2 mobile width assertion via layout-check/vitest.
- **Scope creep into live streaming**: explicitly non-goal; SSE untouched.

## Gates
- Every task: vitest/tsc green before commit (web), go test + contract green
  (backend), OpenAPI updated when routes change.
- Phase gate (execute): `make test` + integration + coverage ≥ 90% + lint +
  `bun run build` + layout-check local + (deploy step separate, like phase 20).
