# Phase 22 — Tier 2 UI: live run view, visual load editor, capacity meters
Status: complete
Date: 2026-09-05
Reference: k6 Cloud live view + test builder (external benchmark, not a dependency)
Tier lineage: phase-21 brainstorm Tier 2 (phase-21 spec Non-goals, verbatim candidates)

## Problem

Phase 21 shipped results-page parity, but three Tier-2 gaps remain, and one
environment gap hides even Tier-1 work:

0. **Prod has zero interval data.** Every run in prod (1–5) predates the
   series pipeline; `/api/runs/{id}/series` returns `{"points":[]}` for all.
   The phase-21 charts render honest empty states, so the UI "looks
   unchanged". Clusters list is `[]` — no cluster is registered, so no new
   execution can run either. Fix the environment before adding UI.
1. **Live run view is numbers-only.** `Execution.tsx` subscribes to
   `GET /api/executions/{id}/stream` (SSE, `EngineMetric`: threads, latency,
   label, status, run_id…) but renders only 3 rolling numbers + raw pod log.
   k6 shows the run *shaping* in real time — chart-on-events is the ask.
2. **Load profile is a JSON textarea.** `NewTest.tsx` step 5 PUTs a config
   JSON built from form fields (`buildConfig`); the shape is flat — one
   concurrency/rampup/throughput per entry (`loadprofile.Entry`). k6's stage
   builder (add row, set target/duration) is the UX bar.
3. **Cluster capacity is thin.** `CapacityPanel.tsx` exists but the
   Clusters page is a stub in prod (empty state). Phase-8 multi-cluster
   laid the backend; meters per cluster (capacity headroom) were Tier-2.

## Current state (verified 2026-09-05, develop dc845fa, prod phase21)

- SSE client `streamExecutionMetrics` exists (`web/src/api/status.ts:46`);
  events land in `eventsRef` rolling window (60s), pruned each second.
- `TimeSeriesChart` (309 lines, phase 21): plain-SVG, axes/ticks/hover/
  legend, multiple series. House rule: no charting library.
- `loadprofile.Entry`: Name, ScenarioID, Concurrency, Rampup, Engines,
  Throughput (0=unlimited), Duration, CSVSplit + `Validate()`.
- NewTest flow: project → scenario → execution → requests fragment (YAML,
  TaurusEditor, untouched) → config JSON (PUT /executions/{id}/config).
- Clusters API: POST/GET `/api/clusters`, rotate-ingest-token, per-name GET.
- `yaml` npm dep present; dark mode toggle already exists (Tier-3 item done).
- Quota 2.1% at plan time; window to 15:23Z.

## Goals

- **G0 Prod data.** Cluster registered; ≥2 executions complete with
  per-second intervals; phase-21 charts + compare render real data.
- **G1 Live chart.** Execution page draws a live TimeSeriesChart from SSE
  events: VUs (threads), req/s, latency percentile toggle. Rolling window,
  no persistence, degrades to current 3-number view when idle.
- **G2 Stage editor.** NewTest step 5 becomes a visual stages table
  (concurrency target, ramp-up, throughput, duration per row, add/remove
  rows) that emits the exact same config JSON. Raw-JSON toggle for escape
  hatch. Validation mirrors `Entry.Validate` messages.
- **G3 Capacity meters.** Clusters page shows per-cluster capacity panel
  (engines available/quota headroom) from existing backend data; honest
  empty state when cluster reports nothing.

## Non-goals (Tier 3 → phase 23 candidates)

- Chart export (CSV/PNG), share links, keyboard nav, URL filtering.
- Threshold coloring/SLO tint (needs thresholds in config model — seam).
- Replacing TaurusEditor (requests YAML stays verbatim).
- Any engine/scheduler/backend protocol change — SSE payload as-is.

## Acceptance criteria

- **AC0** `GET /api/runs/{new_run_id}/series` returns non-empty points for
  runs created this phase; Reports page chart + Compare page render them.
- **AC1** During a live execution, Execution page chart updates ≥1/s from
  SSE; switching percentile toggles series without resubscribe.
- **AC2** Stage editor round-trips: build → submit → re-open → same values;
  raw-JSON toggle preserves content both ways; validation blocks
  concurrency≤0, engines≤0, duration≤0 with Entry.Validate-mirrored copy.
- **AC3** Clusters page renders capacity meter per registered cluster.
- **AC4** layout-check extended: Clusters capacity row, NewTest editor
  route, Execution live chart placeholder — per-persona where applicable.
- **AC5** Gates: make test, coverage ≥90%, golangci-lint 0, vitest, tsc,
  bun build, layout-check green.
