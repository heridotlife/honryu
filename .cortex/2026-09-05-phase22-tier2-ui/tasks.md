# Phase 22 Tasks — Tier 2 UI
Spec: .cortex/2026-09-05-phase22-tier2-ui/spec.md · Plan: plan.md
Branch: feat/phase22-tier2-ui (off develop dc845fa)

## Task 0 — Prod data bootstrap (operator, no pi)
Register Talos cluster; fire 2 small executions (2 VU / 30s); verify
`/api/runs/{id}/series` non-empty for new runs; record run IDs in PROGRESS.md.
- [ ] AC0

## Task 1 — liveSeries reducer (pure)
`web/src/lib/liveSeries.ts` + `liveSeries.test.ts`. Input ReceivedMetric[],
output per-second {t, vus, rps, p50, p95, p99}. Hostile: empty, single,
out-of-order, label-mixed, 60s+ overflow prune. No React/SSE imports.
- [ ] unit tests green

## Task 2 — useLiveSeries hook extraction
`web/src/hooks/useLiveSeries.ts` — move eventsRef/prune/subscribe from
Execution.tsx; expose {series, connected, lastEventAt}. Execution.tsx
consumes hook; existing Execution.test.ts all pass unchanged.
- [ ] refactor, zero behavior change

## Task 3 — Execution live chart
TimeSeriesChart on Execution page: dual series (VUs left, req/s right or
toggle), percentile selector p50/p95/p99, idle "waiting for first events…",
running state. Reuse TimeSeriesChart as-is; no new chart code paths.
- [ ] AC1 · layout-check assertion added (task 10 collates)

## Task 4 — vitest + tsc for Block A
New tests for liveSeries + hook + chart section; `tsc -b` clean;
Execution.test.ts extended for chart idle/running states.
- [ ] gates

## Task 5 — stages round-trip pair (pure)
`web/src/lib/stagesConfig.ts` + tests: stagesToConfig / configToStages.
Single-row must equal current buildConfig output exactly. Zero-throughput
= unlimited (omit key). Multi-row, CSVSplit flag.
- [ ] round-trip property test

## Task 6 — StageEditor component
`web/src/components/StageEditor.tsx`: rows {concurrency, rampup, throughput,
duration}, add/remove, validation copy mirrors Entry.Validate (concurrency>0,
engines>0, duration>0), raw-JSON toggle preserving content both directions.
- [ ] component tests

## Task 7 — NewTest step-5 integration
Replace JSON textarea with StageEditor; same PUT payload; NewTest.test.tsx
flow tests updated; escape hatch = raw toggle.
- [ ] AC2

## Task 8 — CapacityMeter + cluster fetch
api/clusters.ts types extended if backend fields exist; `CapacityMeter.tsx`
plain-SVG bar/gauge (house style, currentColor); per-cluster value from
GET /api/clusters — honest "no capacity reported" when absent.
- [ ] component tests

## Task 9 — Clusters page meters
Cluster rows show CapacityMeter; empty state kept; test updates.
- [ ] AC3

## Task 10 — layout-check extensions
Assert: Execution live-chart section heading; NewTest stage editor row;
Clusters capacity row — per-persona (alice full; bob per tenant; carol view;
dave view) where routes differ.
- [ ] AC4

## Task 11 — Phase close
Full gates (make test, coverage ≥90%, golangci 0, vitest, tsc, build,
layout-check vs prod), PROGRESS.md, push + PR to develop. Operator merges.
- [ ] AC5
