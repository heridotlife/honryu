# Phase 13 — Operator UI — Plan

**Spec:** `.cortex/2026-08-16-phase13-operator-ui/spec.md`

## Context

- **The SPA stops at phase 6's vocabulary.** `web/` has four pages (Reports,
  Reservations, LiveStatus, Campaigns) built on plain React 19 +
  react-router 7 + Tailwind 4 + lucide-react, with typed fetchers in
  `src/api/*.ts` mirroring Go JSON tags, pure-helper vitest tests (jsdom,
  see `LiveStatus.test.ts`), and `DashboardLayout` nav. Since then the API
  gained: per-execution trend + error-signature history (Phase 9), campaign
  comparison (Phase 9), correlation ids in reports (Phase 10), the cluster
  registry (Phase 8/12), k6 as an engine (Phase 11). None of it renders.
- **The transport question is already answered in code.** LiveStatus
  consumes the SSE feed today (`streamExecutionMetrics`, EventSource in
  `api/status.ts:46`); `getExecutionStatus` already types the phase-11
  status shape (`phase`/`pool_size`/`ScenarioStatus` with engines
  wanted/deployed/reachable). LiveStatus's upgrade is rendering, not
  plumbing.
- **The wire has fields the UI doesn't.** `domain/report.Report` carries
  `engine`, `cluster`, `correlation_id` (all `omitempty`, report.go:166-176);
  `web/src/api/reports.ts`'s `Report` type has none of them.
- **Four client modules are missing entirely:** trend
  (`/api/executions/{id}/trend` → `report_handlers.go` `trendPointResponse`,
  most-recent-first, `has_comparable_predecessor`/`regressed` flags),
  error-signatures (`/api/executions/{id}/error-signatures?by=label|code`,
  group/leaf shape with safe re-summed totals), campaign comparison
  (`/api/campaigns/{id}/comparison` → `campaignComparisonResponse`,
  `has_baseline` false ⇒ empty services, not an error), cluster list
  (`GET /api/clusters` — read-only; registration/rotate/delete stay
  API/CLI per Non-goals).
- **Contract constraints:** openapi.yaml is frozen for this phase (no new
  endpoints); no charting library (SVG sparklines / text tables, like
  phase 5's calendar); no new runtime deps; the SPA embeds via
  `go:embed all:dist` (`web/embed.go`) so `dist/.gitkeep` mechanics must
  survive (`bun run build` deleting it locally is expected, never committed).
- **Resolved open questions** (recorded in the spec): keep SSE for metrics;
  no phase-11/12 endpoint dependencies beyond what's listed; correlation-id
  copy = `navigator.clipboard` with select-fallback, APM deep link = a
  localStorage URL template (`honryu-apm-template`,
  `{correlation_id}` placeholder) rendered as a link only when set —
  matching the `honryu-theme` precedent, zero server involvement.

## Approach

One horizontal foundation task, five independent page tasks, then
verification — UI mirror of the hexagonal rule: the `api/` layer is the
port, pages are adapters over it.

**Foundation (task 134).** Extend `api/reports.ts`'s `Report` with optional
`engine`/`cluster`/`correlation_id` (normalized like campaigns.ts does for
omitempty arrays); add `api/trends.ts`, `api/clusters.ts`,
`api/comparison.ts` typed off the Go response structs; add
`api/apm.ts` — pure `formatApmLink(template, correlationId)` + localStorage
accessors. Pure helpers (`normalizeTrend`, `groupSignatures`) exported for
vitest. No page changes yet; existing tests stay green.

**Pages (tasks 135–139).**
*Reports* (135): run rows gain engine/cluster badges; run detail gains
correlation-id copy button + conditional APM deep link + a settings input
for the template; detail links the shard config/log viewer (existing
object endpoints, scenario prefilled, shard number chosen).
*Reports analytics* (136): the execution-scoped list view gains, below the
runs table, the trend table (sparkline column via inline SVG over
achieved/requested QPS, `regressed`/no-baseline made visible inline) and
the error-signature history grouped by label with a `by=code` toggle.
*Campaigns* (137): comparison panel beside the verdict — per-service
status chips (improved/regressed/newly_at_risk/still_at_risk/steady/new/
dropped), baseline id shown, `has_baseline: false` rendered as "first
campaign — no baseline", not an error.
*Clusters* (138): new page + nav item; table of registry entries with
origin (operator/byoc), engine images, scheduling enablement; read-only.
*LiveStatus* (139): engine-kind awareness (execution's engine from
`getExecution` labels the panel; no jmeter assumptions in copy or math)
and the lifecycle snapshot rendered beside the stream: phase badge,
per-scenario engines wanted vs deployed vs reachable with shortfall
highlighting.

**Verification (task 140).** `vitest run` + `bun run build` green;
`go test` untouched-but-green (embed compiles); manual dogfood against the
homelab deployment (phase-12 image is live there): trend/comparison over
the phase-12 dogfood runs' data, cluster list showing `dogfood-byoc`
post-registration, correlation-id copy + template link exercised; findings
appended to the spec in the phase 7/10/11/12 format.

Rejected: a charting library (bundle + embed weight, spec non-goal); server
-side APM template config (new endpoint ⇒ contract change, non-goal);
websocket/JSON-poll refactor of LiveStatus (SSE already works); write
surfaces for clusters (auth-surface non-goal).

## Risks

| Risk | Mitigation |
|---|---|
| TS types drift from Go tags. | Types copied field-for-field from the handler response structs, with the struct named in each module's header comment (existing convention in campaigns.ts/client.ts). |
| SSE feed shape differs per engine (k6 vs jmeter). | `EngineMetric` is engine-agnostic already (phase 11 hardened the plane side); pages must not branch on engine kind for math — verified in task 139's tests. |
| Sparkline SVG a11y/bloat. | Plain `<svg>` with `role="img"` + text fallback table value; no library. |
| localStorage template contains a bad placeholder. | `formatApmLink` is pure and total-tested: no `{correlation_id}` ⇒ return null (no link rendered), empty id ⇒ null. |
| `dist/.gitkeep` deletion sneaks into a commit. | Never `git add web/dist`; the embed task re-verifies `git status` after `bun run build` (AGENTS.md rule). |
| Dogfood data lacks a regressed run / second campaign for comparison views. | Homelab has runs 1–10 across executions incl. k6 and jmeter; worst case the no-baseline / no-comparable-predecessor branches are what get verified live — those are tested branches too. |

## Out of scope

Any API change; write surfaces (deploy/trigger/purge/register); auth in the
SPA; charting libs; real-time cluster health probing (list view shows
stored state only); mobile-specific redesign beyond existing responsive
layout; i18n.

## Verification

Standard bar: `vitest run` from `web/` green; `bun run build` green and
`go:embed` still compiles (gofmt/vet/golangci-lint + `make test` untouched
but re-run to prove the embed lane); no new runtime deps (`bun install`
frozen — `bun.lock` unchanged). **Dogfood on the homelab deployment is the
gate for the Goal questions** (each answerable from the SPA against real
phase-12-era data); findings appended to the spec.
