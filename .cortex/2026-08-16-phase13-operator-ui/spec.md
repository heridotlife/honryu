# Phase 13 — Operator UI

Agreed via brainstorm 2026-08-16. Surfaces Phases 8–12 in the operator SPA
(`web/`): today's four pages predate the analytics, multi-cluster, telemetry,
and BYOC capabilities. **Refined at phase start 2026-08-17 (post-Phases
11–12): open questions resolved below, web/ surveyed — plan and tasks follow.**

## Problem

`web/` (read-only operator SPA: Reports, LiveStatus, Reservations,
Campaigns) stops at roughly Phase 6's vocabulary. Since then the API gained:
per-execution trends and error-signature history (Phase 9), campaign
comparison (Phase 9), report correlation ids that deep-link into a
customer's APM (Phase 10), cluster registry and routing (Phase 8), and —
after Phases 11–12 — k6 engine runs, hardened lifecycle states, and BYOC
cluster registration. None of it is visible; an operator reads JSON with
curl today.

## Goal

An operator opens one page per question: "what happened across this
execution's runs" (trend + signatures + correlation ids), "is this campaign
improving" (comparison), "where is my load running" (clusters), "what's
running right now" (live status incl. k6 engines and reconciled runs).

## Non-goals

- **Stays read-only.** No deploy/trigger/purge buttons, no registration
  forms in v1 — the write path stays API/CLI. (Revisit after operator
  feedback; the SPA embeds in the API binary and must not become an auth
  surface.)
- **No charting library.** Phase 5's calendar and the existing pages render
  with plain React + Tailwind; sparklines/trends render as SVG or text
  tables the same way. Bundle cost and the go:embed build path both argue
  against a chart dependency.
- **No new API endpoints.** UI consumes what exists (openapi.yaml is the
  contract); gaps found while building become API-phase proposals, not
  UI-side workarounds.

## Boundary — SPA vs Grafana

**Added by Phase 15 (2026-08-17), the boundary this phase's own non-goals kept
to without stating it.** The parent spec is explicit that Honryu is *"not
replacing Grafana/Prometheus with custom observability"*
(`.cortex/2026-07-30-honryu/spec.md:40`) and names Prometheus/Grafana as the
live-metrics surface (`:196`). This SPA stays on the right side of that line:
read-only navigation over the existing API, no charting library, no metric
storage of its own, no dashboarding — trend tables and sparklines are thin SVG
over API responses, not a query engine. Grafana remains the tool for ad hoc
exploration and alerting; this SPA is the tool for "what happened on this run"
without curl. If a future phase wants query flexibility, panel composition, or
alerting from the operator SPA, that is the signal it has crossed into
Grafana's territory and should be reconsidered, not extended.

## Constraints

- **bun build, go:embed, `web/dist/.gitkeep`** mechanics are settled
  (AGENTS.md); UI changes must keep `vitest run` green from `web/`.
- **Correlation id is a copy-paste affordance, not decoration**: clicking /
  copying must yield the raw trace id (the entire point of Phase 10 is
  "paste into your APM").
- **Engine variety is data, not layout**: pages must not assume jmeter or a
  single engine kind (Phase 11 adds k6; reports already carry `engine`).

## Approach (sketch — full approach at write-plan)

1. Reports view: per-run row gains correlation id (copy affordance),
   engine, cluster; run detail links the shard-config viewer via the
   existing object endpoints.
2. Execution view: trend series (Phase 9 endpoint) as a table/sparkline;
   error-signature history inline.
3. Campaigns view: comparison (baseline vs current, per-service status)
   beside the existing verdict.
4. Clusters view: registry list with origin (operator/byoc), routing
   health; read-only.
5. LiveStatus: engine-kind awareness; reconciled/stranded runs surfaced
   distinctly (Phase 11's states).
6. All of it typed against the existing `api/` client modules; tests per
   page, matching the current vitest patterns.

## Acceptance criteria

1. An operator can answer each Goal question from the SPA without curl, on
   data produced by a real run (verified against the homelab deployment).
2. Correlation id is copyable raw; when an APM template is saved, a deep
   link renders from it (client-side localStorage template — resolved above).
3. `bun run build` + `vitest run` green; no new runtime deps.
4. Standard Go bar untouched where the API is unchanged (embed build must
   stay green in CI: `go:embed all:dist` finds `dist/.gitkeep` in fresh
   checkouts).

## Open questions — resolved 2026-08-17 (survey of web/, openapi.yaml, handlers)

- **SSE vs poll for LiveStatus: keep SSE.** `web/src/api/status.ts` already
  exposes `streamExecutionMetrics` (EventSource against
  `/api/executions/{id}/stream`) and LiveStatus consumes it. Phase 13 only
  extends rendering; no transport change. Per-execution lifecycle status
  (phase, engines wanted/deployed/reachable) arrives via the existing
  `getExecutionStatus` fetch, refreshed on an interval — it is not on the
  stream.
- **Phase 11/12 endpoint enumeration: no new dependencies.** Phases 11–12
  landed; the UI-relevant surface is already in openapi.yaml: `GET
  /api/clusters` (registry list; registration/rotation/delete stay
  API/CLI-only per Non-goals), `GET /api/executions/{id}` (engine kind, incl.
  k6), `GET /api/executions/{id}/status` (phase + engines wanted/deployed/
  reachable). Note: there is no distinct "reconciled/stranded" state in the
  API — the Approach sketch's language resolves to surfacing the *derived*
  phase (`idle`/`deployed`/`running`, `internal/domain/run/run.go:18`) plus
  the reachable/wanted/deployed engine counts, which is what phase 11's
  hardening actually exposes.
- **Copy affordance + APM template: localStorage-configured client-side
  template.** No API endpoint, no server config. The raw id is always
  copyable (`navigator.clipboard`, falling back to a select-and-copy
  textarea for embedded/insecure contexts); when the operator has saved a
  URL template in localStorage (`honryu-apm-template`, e.g.
  `https://apm.example.com/trace/{correlation_id}`), a deep link renders
  beside it. Settings affordance lives on the Reports page; this matches the
  existing `honryu-theme` localStorage precedent in DashboardLayout.

## Survey addenda that shape the plan

- `web/src/api/reports.ts`'s `Report` type predates phases 8/10: the wire
  format (domain/report) carries `engine`, `cluster`, `correlation_id` (all
  `omitempty`) — the TS type must gain them as optional fields, normalized
  like `failing_criteria`/`other_load` are in campaigns.ts.
- No client modules exist yet for trend, error-signatures, cluster list, or
  campaign comparison — all four are new `api/` modules typed off the Go
  response structs (`report_handlers.go`, `campaign_handlers.go`,
  `cluster_handlers.go`).
- `api/campaigns.ts` already contains a write path (`createCampaign`) —
  read-only Non-goals apply to *new* surfaces; existing ones are untouched.
- vitest pattern: tests target exported pure helpers (`summarize` in
  LiveStatus.test.ts), jsdom environment, globals on (`vite.config.ts`). New
  pages export their formatters/derivers for the same style of test.

## Live verification findings (task 140, 2026-08-17)

Live dogfood on the real Talos cluster (`admin@talos-homelab`, ns `honryu`),
API image `honryu-api:phase13` (built + pushed this task from
`deploy/honryu/Dockerfile`, rolled out clean — no new migrations this phase),
engine `engine-k6:0.57.0-2`, target `httpbin.pve.heri.life`. **All live gates
met** — full local bar first (vitest 99/99, `bun run build`, gofmt/vet/
golangci-lint 0 issues, `make test` with the fresh dist embedded, `bun.lock`
byte-identical, no `dist/` content staged), then the served SPA asset
(`index-CkhCaOaM.js`) matched the fresh build hash — the embed lane proven
end to end.

### Reports on real phase-10/11/12 data (AC2)

Phase-12 run 9 (`on-byoc`): report carries `cluster: dogfood-byoc` +
`correlation_id 4de733f0…` and **no engine field** — exactly the
absent-renders-nothing case: cluster badge shows, engine badge doesn't.
Phase-11 run 6 carries `engine: k6` — the engine badge's real kind. Shard
viewer verified against its wire objects: run 9's shard-0 config is the
compiled Taurus YAML (executor jmeter, baggage headers embedded), the log is
the captured Taurus CLI output, both `text/plain`. Correlation-id copy and
the APM template link render from these same report fields (localStorage
`honryu-apm-template` + `{correlation_id}`, unit-tested contract); the live
check confirms the wire inputs, the affordances are pure rendering of them.

### Trend + error signatures on a real erroring run (AC2)

Fresh k6 run (execution 11, `p13-live-k6`): script deliberately sent 25% of
traffic to `/status/500` → **111,761 samples, 22,397 failed, outcome
passed** (threshold `rate<1.0`), correlation id present, engine `k6`.
Signatures non-empty on real data for the first time: by-label groups
`https://httpbin.pve.heri.life/status/500` (total 22,397, side target) and
by-code groups `500` with the same row — both groupings, `run_count` on the
leaf. Trend endpoint returns one point (achieved 2,725.9/s, error_rate
0.2004, `hit_target_qps` true, no comparable predecessor — the single-point
sparkline degenerate case exercised live).

### Campaigns comparison on a real campaign pair (AC3)

Tenant `phase13`, project `phase13-camp`, execution 12 (same k6 script,
23,378 failed — errors inside the window). `p13-baseline` window ends
08:20:30Z, active `p13-current` starts 08:20:31Z:
`GET /api/campaigns/2/comparison` → `has_baseline: true`,
`baseline_campaign_id: 1`, service **`steady`** with `go` and `baseline_go`
both true (same execution both windows — steady is the honest verdict).
Campaign 1 → `has_baseline: false`, `services: []` — the "first campaign"
informational path.

### Clusters registry page (AC4)

Empty registry `[]` verified first (empty-state is real, not an error), then
`dogfood-byoc` re-registered (namespace/ingest/sidecar + admin kubeconfig)
⇒ **201**, origin `byoc`, 43-char one-time token minted, and
`GET /api/clusters` serves the full routing row (api-url, ingest, sidecar,
namespace, secret_ref) — never the token or its hash (phase-12 property
re-confirmed on phase-13).

### LiveStatus during a live run (AC1)

Execution 14 (`p13-stream-check`): `GET /api/executions/14` carries
`engine: k6` (badge source), `/status` observed transitioning
`deployed → running` with `1/1 engines deployed, reachable, in_progress` —
the lifecycle snapshot LiveStatus polls. The SSE stream captured
concurrently mid-run delivered **12 `data:` events** carrying both labels
(`/headers` status 200, `/status/500` status 500) — exactly the shape
`summarize()` folds into the trailing-window stats.

### Serendipitous live findings

- **Campaign freeze fired live, unprompted**: re-triggering an execution on
  the campaign's project while `p13-current` was active returned
  `lifecycleapp: blocked by an active campaign's freeze: p13-current` —
  phase 6's contract enforcing itself during an unrelated UI-phase check.
- `GET /api/admin/executions` returned `[]` while executions 1–10 existed
  (its filters weren't chased — no phase-13 surface consumes it); recorded
  as an observed oddity for whoever owns admin next.
- Post-finalize, `/status` kept `in_progress: true` for ~2 min and
  re-trigger 409'd ("a run is already in progress") until reconcile caught
  up — known lag shape, worth remembering when watching short runs.

### Cleanup

Executions 11–14 purged (`execution purged` ×4, engine pods all gone),
`dogfood-byoc` deleted (**204 — and the credential Secret was again left
behind; the phase-12 gap persists**, removed manually), port-forward
stopped. Left in place (same doctrine as `p10-live`/`p11-live`/
`phase12-dogfood`): projects `phase13-dogfood`/`phase13-camp`, tenant
`phase13`, campaigns 1/2 (no campaign delete route exists), scenarios 8/9.
The plane now runs `honryu-api:phase13`.
