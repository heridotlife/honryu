# Phase 28 — Run Detail: one dense workspace (k6-style)

Branch: feat/phase28-run-workspace (off develop @ 3dbced7)
Date: 2026-09-07

## Why

Current /reports/:runId is a vertical card stack — everything on one long
scroll. k6 Cloud's run view is one dense workspace: a sticky summary band,
tabs under the run, filters up top. Phase 27's list-first Reports proved
the pattern; this phase reorganizes run detail the same way. UI-only —
every endpoint needed already exists.

## Non-negotiable constraints

- **Every existing data-testid must keep passing**: the vitest suite
  (355 tests) and web/scripts/layout-check.js assert against ids like
  chart-vus-rps, chart-errors, chart-latency, chart-requested,
  series-empty, series-retry, pct-50, export-csv/json/pdf, copy-link,
  execution-{id}, manual-id-toggle, compare-runs-link. Hidden tab content
  must still exist in the DOM (tabs render panels lazily BUT tests query
  after mounting; safest = render all panels, hide inactive with CSS
  `hidden` attribute — DOM present, invisible).
- Tailwind classes only; existing ui/ primitives (Card, Button, Input).
- No new npm deps. No backend changes.

## Tasks

### T1 — ui/Tabs.tsx primitive
- `Tabs({ tabs: {id,label}[], active, onChange })` → nav[role=tablist],
  buttons role=tab aria-selected, keyboard Arrow keys move + activate,
  panels render children with `hidden` when inactive.
- Unit test: role attrs, arrow-key nav, inactive panel hidden-but-present.

### T2 — RunDetail workspace shell
- Replace the card stack in Reports.tsx ReportDetail with:
  - Sticky header band (not sticky-positioned; just first section):
    Run #, OutcomeBadge, engine/cluster badges, started→ended, export
    anchors + CopyLink (same testids), prev/next run arrows from
    listExecutionReports(report.execution_id) filtered to run_id neighbors.
  - Tabs bar: Overview | Time series | Labels | Errors | Config | Objects.
- Overview = LoadStat pair + latency percentile chips + attribution grid
  + correlation-id row (moves here).
- Time series = existing TimeSeriesSection + RequestedVsAchieved, intact.
- Labels = LabelsTable. Errors = error signatures card. Config = summary
  of report.requested (concurrency/engines/duration; reuse LoadStat).
  Objects = ShardObjects.
- Default tab: Overview; `?tab=` syncs (tab=overview|series|labels|errors|config|objects).

### T3 — Reports + Executions filter chips
- ReportsList: filter row above execution list — chips for engine
  (derived from list) and outcome is not on the summary; so: engine chips
  only + text filter by name/id. data-testid="filter-engine-{value}",
  "filter-search".
- Executions.tsx: same engine chip row, data-testid same convention.

### T4 — gates + close
- vitest 355+ new green; tsc clean; layout-check unchanged-pass vs seeded
  local stack (/tmp/p23_stack.sh + /tmp/p22-seed.sh plant).
- One commit per task. PR → develop. Deploy phase28 per
  /tmp/honryu_build.sh + /tmp/p28-values.yaml (copy p27-values, tags).
- Live-validate on prod: tab navigation, ?tab= sync, filters, exports.
