# Phase 21 — Run-results UI: k6-parity charts and endpoint breakdown
Status: complete
Date: 2026-09-04
Reference: k6 Cloud results page (external benchmark, not a dependency)

## Problem

Honryu's report page shows *verdicts* (load totals, percentile table,
attribution counts, error signatures) but not the *shape of the run*. A user
cannot answer the questions k6's results page answers at a glance:

1. **What did latency/RPS/VUs do over time?** The backend stores
   `report.Intervals` — every second, every pod, response-time buckets — but
   no HTTP endpoint serves them. The data is measured, persisted, and
   unreachable from the browser. The only chart in the SPA is `Sparkline`
   (phase 13: deliberately no charting library), used for trend shape only.
2. **Which endpoint was slow?** `LabelSummary` (per-label samples, errors,
   latency map) is served by `GET /api/runs/{run_id}/report`, typed in
   `web/src/api/reports.ts`, and rendered **nowhere**. The information exists
   one fetch away and the UI never asks for it.
3. **Requested vs achieved, overlaid.** Both `Load` structs render as separate
   cards in `Reports.tsx` (line 414). Without an overlay the eye cannot see
   where the run stopped keeping up — the exact moment honryu exists to find.
4. **Two runs side by side.** `Campaigns.tsx` has campaign-level comparison;
   there is no pick-two-runs compare for the common question "did the fix
   help?".

Current state (verified 2026-09-04, develop `84485dd`):
- `Reports.tsx` (495 lines): cards for Load, Latency percentiles, Attribution,
  Error signatures, correlation id, APM deep-link template, shard objects.
- `ReportsTrend.tsx` (206): run-over-run sparkline trend + signature history.
- Router already exposes: `GET /api/executions/{id}/reports`, `GET .../trend`,
  `GET .../error-signatures`, `GET /api/runs/{run_id}/report`.
- `metrics.Interval` (`internal/domain/metrics/interval.go`): `ts`, `label`,
  `concurrency`, `samples`, `succeeded/failed`, `bytes`, `Latency Histogram`
  (buckets, not percentiles — percentiles cannot be combined across pods, so
  the UI series must be computed server-side from merged buckets).
- House rule (phase 13, still standing): **no charting library** — plain SVG,
  `currentColor`, Tailwind classes; `Sparkline.tsx` is the precedent.
- `layout-check` (phase 20) asserts per-persona nav; new pages must join it.

## Goals

- **G1 Time-series charts on the run report.** Server computes per-second
  merged series (VUs, RPS, error rate, p50/p95/p99) across pods from stored
  Intervals; the browser renders them as interactive plain-SVG charts with
  axes, time ticks, and hover readout.
- **G2 Endpoint (label) breakdown table.** Sortable per-label table — samples,
  error rate, latency percentiles — with sparkline row shapes.
- **G3 Requested-vs-achieved overlay.** One chart: requested VUs/RPS vs
  achieved, so divergence is visible.
- **G4 Run comparison.** Pick any two runs of an execution; delta table
  (latency %, RPS %, error rate) + overlaid p95 series.
- **G5 Percentile selector.** p50/p90/p95/p99 toggle driving all charts.

## Non-goals (phase 22 candidates)

- Live/streaming run view (SSE charts while running) — the stream endpoint
  exists; charts-on-events is a separate phase.
- Visual load-profile editor (stages UI) — replaces TaurusEditor, bigger seam.
- Chart export (CSV/PNG), share links, dark mode, keyboard nav.
- Any change to RBAC/session/persona model — phase 20 is closed.

## Acceptance criteria

- **AC1** `GET /api/runs/{run_id}/series?p=95` returns per-second points
  `{ts, vus, rps, err_pct, latency}` merged across pods/shards, deduped by
  `(shard, seq)`, omitting `latency` on seconds with no samples. Conformance:
  fixtures with overlapping pods and a duplicate-superset batch produce the
  same series as the clean batch.
- **AC2** Charts render for a real historical run against the live site with
  zero console errors; axes and time ticks legible at 375px (mobile) and
  desktop widths.
- **AC3** Label table renders when `labels` present; sorts by any column;
  shows em-dash rows when a label lacks a percentile; hidden entirely (no
  empty card) when `labels` is absent.
- **AC4** Requested/achieved overlay shows both series plus legend; when a run
  never reached requested load, the divergence is visually attributable to a
  second (not guessed).
- **AC5** Compare view: selecting two runs shows delta table with
  signed percentages; same-direction ordering (improvement green).
- **AC6** Percentile selector re-renders all latency series without refetch
  (client-side percentile switch, server returns all percentiles per second).
- **AC7** All new UI behind existing RBAC personas: `tenant_viewer` sees
  charts/tables read-only (no new write actions anywhere in this phase);
  `layout-check` extended with new routes/sections per persona.
- **AC8** No new runtime dependencies in `web/package.json` (SVG hand-rolled);
  `bun run build`, `tsc -b`, vitest, `make test`, coverage gate all green.

## Approach

Backend-first, one seam: a read-only series endpoint computed from stored
Intervals (pure function over `[]Interval`, table-driven tests with hostile
fixtures: duplicate superset batches, pod skew, empty runs). Frontend builds
one reusable `TimeSeriesChart` primitive (axes/ticks/hover/legend, no brush —
cut) then composes pages from it. Comparison reuses the same primitive with
two series.
