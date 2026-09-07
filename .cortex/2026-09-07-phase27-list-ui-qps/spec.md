# Phase 27 — list-first UI + visible target QPS

Branch: feat/phase27-list-ui-qps (off develop)
Date: 2026-09-07

## Why

Two operator gaps, found live on prod:

1. **Input-first where data exists.** Reports page demands a typed
   execution id before showing anything — but GET /api/executions already
   lists everything with names. Operators guess ids or bounce off the
   Executions page.
2. **Target QPS invisible after creation.** The Execution page never
   shows the config's throughput. NewTest's stage table collects it
   (empty = unlimited) but once the execution exists there is no place to
   see or change target QPS — the config PUT endpoint exists but no UI.

Also: NewTest's Engine select hardcodes jmeter/gatling; k6 is a
first-class executor in the domain (ExecutorK6, deploy/engines/k6).

## Changes

### T1 — Reports list-first
- On mount, fetch executions (listExecutions). Render as a simple list
  (same visual language as Executions page: name · engine · date). Each
  row is a link that loads that execution's reports inline (replaces the
  typed id as the primary path). Keep the id input as a secondary
  "advanced" affordance for deep links, hidden behind a toggle.

### T2 — Execution page: config section with target QPS
- New "Configuration" card on /executions/:id (idle state only): GET
  /api/executions/:id/config, render per-test rows (name, scenario,
  concurrency, rampup, engines, throughput — "unlimited" when omitted —
  duration). Editable throughput field + Save (PUT full config back,
  same {"multi-test": …} wrapper shape). RBAC: same grant as the
  TaurusEditor save (execution:update).

### T3 — Engine list
- NewTest Engine select gains <option value="k6">k6</option>. (Full
  dynamic engine discovery is overkill; domain has exactly three.)

### T4 — close
- Gates, PROGRESS, deploy phase27, live-validate: Reports list loads on
  prod, config card shows p22-data-1's throughput as unlimited, save
  round-trips.
