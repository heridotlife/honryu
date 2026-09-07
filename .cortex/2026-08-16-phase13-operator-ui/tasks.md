# Phase 13 — Operator UI — Tasks

Continues the roadmap's global task numbering from Phase 12's last task (133).
**Spec:** `spec.md` · **Plan:** `plan.md`

## Group A — API client layer (the SPA's ports)

### 134. Typed clients for trend, signatures, comparison, clusters; report + APM helpers
- **Files:** `web/src/api/reports.ts` (extend `Report`), `web/src/api/trends.ts`
  (new), `web/src/api/comparison.ts` (new), `web/src/api/clusters.ts` (new),
  `web/src/api/apm.ts` (new), `web/src/api/apm.test.ts`, plus vitest for the
  new modules' pure normalizers
- **Criteria:** `Report` gains optional `engine?`, `cluster?`,
  `correlation_id?` (wire is omitempty — absent, not empty; types mirror
  `domain/report.Report` exactly, noted in the header comment);
  `getExecutionTrend(id, limit?)` / `getErrorSignatures(id, by)` typed off
  `report_handlers.go`'s `trendPointResponse`/signature group-row structs;
  `getCampaignComparison(campaignId, baselineId?)` typed off
  `campaignComparisonResponse` (`services` normalized to `[]`, never
  undefined); `listClusters()` typed off `cluster_handlers.go`'s response
  (hash/credential fields absent on the wire — type reflects that, no
  `ingest_token_hash`); `apm.ts` exports pure
  `formatApmLink(template, correlationId): string | null` (null when
  template empty, placeholder `{correlation_id}` missing, or id empty) +
  `loadApmTemplate()`/`saveApmTemplate()` over localStorage key
  `honryu-apm-template`; all pure helpers table-tested; existing tests stay
  green; no page changes.
- **Satisfies:** spec Approach step 6; AC2 (helper half)
- **Depends on:** —

## Group B — pages

### 135. Reports: engine/cluster badges, correlation-id copy, APM deep link, shard-config viewer
- **Files:** `web/src/pages/Reports.tsx`, `Reports.test.ts`, `web/src/components/ui/`
  (copy-button primitive if shared), `web/src/api/reports.ts` (shard
  config/log fetchers if placed here rather than 134)
- **Criteria:** run rows show `engine` and `cluster` when present (omitted
  for legacy runs — no "default" noise: empty cluster renders nothing, not
  "default"); run detail shows the correlation id monospaced with a copy
  button (`navigator.clipboard.writeText` with a textarea select+copy
  fallback for insecure contexts, confirmed state on success) and, when a
  saved template exists, an APM deep link built by `formatApmLink`; detail
  exposes the shard-config/log viewer — scenario prefilled from the report,
  shard number input, fetches
  `/api/runs/{run_id}/scenarios/{scenario_id}/shards/{shard}/config` and
  `/log` and renders the text in a `<pre>`; template editing is a small
  settings row on the Reports page (save to localStorage, placeholder
  documented); exported pure helpers tested (copy-state reducer, shard URL
  builder).
- **Satisfies:** spec Approach step 1; AC2
- **Depends on:** 134

### 136. Reports: execution trend table + error-signature history
- **Files:** `web/src/pages/Reports.tsx` (or a sibling component file),
  matching `.test.ts`, possibly `web/src/components/TrendTable.tsx` +
  `Sparkline.tsx`
- **Criteria:** below the runs table on the execution-scoped list view:
  trend table (runs most-recent-first as served) with columns
  run/outcome/achieved vs requested QPS/p95/error rate, an inline SVG
  sparkline (`role="img"` + aria label, no chart lib) over achieved QPS,
  and explicit inline marks for the two flagged states — `regressed` rows
  flagged, no-baseline rows (`has_comparable_predecessor: false`) marked
  "no baseline", never conflated; error-signature history below it grouped
  by label with a by-label/by-code toggle (`?by=`), group `total_count`
  shown with `run_count` only on leaf rows (the double-count caveat from
  openapi rendered honestly: groups show total only); data loads with the
  same execution-id form as the runs table; exported normalizers/helpers
  (`toSparklinePoints`, signature-group sorting) tested.
- **Satisfies:** spec Approach step 2; AC1 (trend + signature questions)
- **Depends on:** 134, 135 (same view)

### 137. Campaigns: comparison panel beside the verdict
- **Files:** `web/src/pages/Campaigns.tsx`, `Campaigns.test.ts`
- **Criteria:** when a campaign is selected, alongside the existing
  verdict: comparison panel with baseline campaign id (or "first campaign —
  no baseline" when `has_baseline: false`, rendered as information, not an
  error) and per-service rows with status chips for all seven
  classifications (improved/regressed/newly_at_risk/still_at_risk/steady/
  new/dropped) mapping to distinct accessible colors/labels;
  `?baseline=` override via a small input; existing create/verdict flow
  untouched; classification label mapping exported and table-tested.
- **Satisfies:** spec Approach step 3; AC1 ("is this campaign improving")
- **Depends on:** 134

### 138. Clusters: read-only registry page
- **Files:** `web/src/pages/Clusters.tsx` (new), `Clusters.test.ts`,
  `web/src/App.tsx` (route), `web/src/components/DashboardLayout.tsx` (nav
  item)
- **Criteria:** new `/clusters` route + nav entry "Clusters"; table of
  registry entries: name, origin (operator/byoc), engine images, scheduling
  enabled, created/updated timestamps as served by `GET /api/clusters`;
  explicitly read-only — no register/rotate/delete affordances anywhere on
  the page, with a one-line note pointing to the API for writes; empty
  state ("only the default cluster" wording when the list is empty);
  formatters exported and tested; no cluster health probing.
- **Satisfies:** spec Approach step 4; AC1 ("where is my load running")
- **Depends on:** 134

### 139. LiveStatus: engine-kind awareness + lifecycle snapshot
- **Files:** `web/src/pages/LiveStatus.tsx`, `LiveStatus.test.ts`
- **Criteria:** panel header names the execution's engine (via
  `getExecution`) so k6 and jmeter runs read correctly — copy and math must
  not branch on engine kind (`EngineMetric` is engine-agnostic; e.g. never
  label the thread gauge "threads (jmeter)" or assume jmeter status codes);
  beside the existing SSE stream summary, the lifecycle snapshot from
  `getExecutionStatus` refreshed on an interval (10s) while the page is
  open, cleaned up on unmount: phase badge (idle/deployed/running),
  per-scenario engines wanted vs deployed vs reachable with shortfall
  highlighted (deployed < wanted, reachable false ⇒ distinct marks);
  existing `summarize` behavior and tests untouched; new derivation helpers
  (e.g. `engineShortfall(scenario)`) exported and tested.
- **Satisfies:** spec Approach step 5; AC1 ("what's running right now")
- **Depends on:** 134

## Group C — verification

### 140. Build, embed lane, dogfood on the homelab, findings
- **Files:** verification notes appended to `spec.md` ("Live verification
  findings", Phases 7/10/11/12 format); `bun.lock` must be unchanged
- **Criteria:** from `web/`: `vitest run` green, `bun run build` green;
  root lane re-proven: gofmt/vet/golangci-lint clean, `make test` green
  (proves the go:embed path with the fresh dist), `git status` shows no
  `dist/` content staged or committed (`.gitkeep` absence is local-only);
  no new runtime deps (`bun.lock` byte-identical); dogfood against the
  homelab deployment (phase-12 image running there; port-forward +
  `honryu_token` as in phase 12): load a phase-12-era execution in Reports
  (trend + signatures render on real multi-run data incl. k6 and/or
  correlation-id-carrying runs), copy a correlation id and exercise the
  APM template link, register nothing — but if `dogfood-byoc` is
  re-registered temporarily the Clusters page must list it (or verify with
  whatever entries exist; the empty state is a valid check too), watch a
  live run in LiveStatus with the lifecycle snapshot present; findings
  appended to the spec; cluster state left clean if anything was
  re-registered.
- **Satisfies:** AC1–AC4
- **Depends on:** 135, 136, 137, 138, 139
