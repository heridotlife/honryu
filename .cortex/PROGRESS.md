# Phase 20 progress

## 2026-09-03 — Block A complete + Block B complete (tasks 1–7)

- Task 1 `76522f1`, task 2 `6998187` (prior session).
- Task 3: live tenants provisioned on honryu.pve.heri.life — exactly ids
  1 (phase16-live-tenant), 2 (phase20-tenant-b), 3 (phase20-tenant-c), all
  ACTIVE, verified via GET /api/tenants.
- Task 4 `40adc44`: ResourceSchedule/ResourceReport + role repairs. Kept the
  WIP's tenant_admin schedule/report `all` grants (not in the spec's delta
  table) so extracting the resources doesn't demote tenant_admin below
  tenant_editor once task 6 re-points the schedule gate.
- Task 5 `471620e`: authorizeProject/Execution/Scenario parameterized,
  authorizeRun added (ResourceRun finally asked), getExecution gated read.
  Scope note: also stamps TenantID in `calibrationapp.Create` (mirrors
  executionapp) — a task-1 gap; without it calibration executions authorize
  against a nil tenant and the calibration routes 403 their own editors.
- Task 6 `312a80d`: authorizeScheduleTenant → schedule:create;
  authorizeAnyParticipatingProject tries campaign:read on the campaign's
  tenant, falls back to project:read (was project:update).
- Task 7 `27ab4b5`: three-bug regression checkpoint green. Scope note: also
  scoped `tenantAdminGate`/`authorizeAdmin` by the path's {tenant_id} — the
  catalog change alone cannot fix bug 3 because the old check sent
  TenantID=nil, which no tenant-scoped grant can ever satisfy. Bug 2's
  regression test landed with its fix in the task-6 commit (same file,
  TestRBAC_CampaignManagerReadsOwnCampaignVerdict).

Gate: `make test` 54 ok / 0 FAIL; golangci-lint 0 issues; no push, no merge.
Next: Block C (tasks 8–12), starting from the red route-authorization table.

# Phase 37 progress

## 2026-09-09 — all 3 tasks complete

- Task 1 `cb8fcdb`: APMConfig.LinkTemplates via HONRYU_APM_LINK_TEMPLATES
  (JSON-array env, demo.profiles route); {{correlation_id}}/{{execution_id}}/
  {{run_id}}/{{project_id}} closed set, unknown placeholder fails startup;
  GET /api/apm-links (group "apm", decisionAuthed in the audit table);
  chart env gated on non-empty apm.linkTemplates; no chart version bump.
  Scope note: single braces in urlTemplate (Grafana JSON payloads) are
  deliberately not placeholders -- only the doubled {{...}} form matches.
- Task 2 `c685280`: FailureHistoryCard on Execution.tsx after Past runs,
  fed by the phase 9 endpoint. Group rows carry NO run count (summing leaf
  run_counts double-counts a run under one label -- domain rule is the
  layout); expandable leaf rows carry side|code|label, total, run_count.
- Task 3 `dc96dc5`: getApmLinks + substituteApmLink in api/apm.ts (a
  template the report can't fully populate renders NO button); links card
  below the correlation card, invisible when unconfigured/unauthenticated
  (the public share page gets the same nothing). Backend: runReportResponse
  gains project_id + baggage via new telemetry.Baggage(Identity) in
  withCriteriaVerdict -- same tolerance as the criteria layer; traceparent's
  parent-span id is NOT stored and is not faked.

Ops notes: pre-existing uncommitted WIP (abandoned-run reconcile) was
stashed to keep commits clean and restored uncommitted afterwards (one
same-spot conflict in TestLoad_Defaults resolved by keeping both
assertions). yamllint warnings on api/openapi.yaml are pre-existing on
develop (36 lines before and after); prettier warns on the same 3 files
it already warned about on develop.
Gate: go build/vet clean; go test -race ./... ok; config+httpapi ok with
WIP applied; web tsc strict + 440 vitest green; helm lint ok; env renders
only when templates set. No push, no PR, no merge.

## 2026-09-15 — phase 71 complete (3 tasks)

- Task 1 `2d95f88`: report.digest payload gains calibrations[] (always an
  array), newest first, scenario name joined. New port method
  CalibrationJobRepository.ListCalibrationJobsByProject (mysql: JOIN
  execution for project scope, LEFT JOIN scenario for the name; fake: same
  join derived from the store maps). Done jobs carry per_pod_qps +
  saturated_by; failed ones failure_reason; a failed calibration surfaces
  even with zero runs. Calibration contract suite widened
  (repositorytest.CalibrationWorldRepo) with the by-project cases; fake and
  mysql pass the same suite.
- Task 2 `7becaf7`: SloPanel -- budget fetch phase machine (skeleton rows
  while a window refetch is in flight, explicit error row instead of
  eternal loading, newest-response-wins), same-window re-pick no longer
  refetches (except as retry after failure), delete confirm names the SLO,
  target validation mirrors the API (p95 must be > 0 -- refused at the
  field; error_rate/success_ratio 0 is legal and reaches the wire), helper
  note under the fields. Rounding to 1 decimal was already there; now
  pinned by test. vitest 623 -> 628.
- Task 3 `bdc5f28`: digest golden-shape test -- exact top-level key set +
  per-field assertions in every section (incl. null-vs-absent laws on
  calibrations lines).

Zero-semantics finding: slo.SLO.Validate rejects target_p95_ms<=0 but
accepts 0 for error_rate and success_ratio (0..1 range) -- the UI's
"0 = unset" fear was unfounded (it dispatched on string emptiness); the
real fix was refusing p95=0 client-side while letting rate 0 through.
Ops: golangci-lint binary not installed locally; hard rules followed
manually, CI runs the pinned v2.12.2. No push, no PR, no merge.

## 2026-09-17 — phase 77 complete (a11y polish, 3 tasks)

Branch feat/phase77-a11y-polish (even with develop@65dd9a0). Commits:
ad94a86 (Modal trap), 89a901a (blur+summary), b1de1a1 (reduced motion).

- Task 1: NEW shared base ui/Modal.tsx (the two dialogs shared zero code —
  chrome extracted, not wrapped): useId-labelled title (aria-labelledby
  replaces aria-label), focus-first-focusable on open, document-level
  Tab/Shift+Tab trap (index-of-activeElement; -1 pulls stray focus back),
  Escape, backdrop tap-away, opener captured at mount and restored on
  unmount. Both modals migrated; their overlay/dialog testids unchanged,
  their duplicated overlay/header/Escape code deleted. SloPanel +
  WebhooksCard armed delete confirms disarm on Escape and outside-mousedown
  (row-scoped via data-testid contains()); WebhooksCard's add form resets
  on Escape scoped to the form. Note: WebhooksCard's comment CLAIMED a
  click-outside disarm that never existed — now implemented.
- Task 2: useFieldValidation (touched-per-field + submitted; components
  keep values+validators), ui/ErrorSummary (role=alert, tabIndex -1,
  self-focus on appear, anchor entries with preventDefault+focus),
  ui/FieldError (icon+text; Input's error line gained the icon too).
  Blur validation on NewTest name/targetUrl and SloPanel p95/errorRate/
  successRatio; submit summaries on NewTest (newtest-error-summary),
  SloPanel (pinned slo-form-error testid now names the summary), and
  ThresholdEditor (save failures only — its row errors stay EAGER, tests
  pin immediate display; eager is a superset of blur). Gotcha: create()
  must call validationEntries() inline, not a render-derived array —
  markSubmitted's re-render lands after the handler.
- Task 3: globals.css ALREADY had the reduce clamp (4c79ae7) — review
  text predated it. Added explicit .animate-pulse → animation:none under
  reduce (static gray blocks); sparklines are static SVG, nothing to
  disable; CSS-only. Content test pins the block (jsdom computes no
  styles). Tailwind's vite plugin empties CSS ?raw/glob-raw reads, so the
  test reads the file with node:fs via a 1-line ambient declaration in
  vite-env.d.ts (no @types/node dependency added).
Gates: go build/vet clean; go test 63 pkgs ok; gofmt+goimports empty;
TestOpenAPIMatchesRoutes/RefsResolve/TagsMatchRouteGroups/SpecStructure
all PASS (no routes touched); web tsc clean; vitest 73 files / 699 tests
(679 baseline + 20). No push, no PR, no merge.

## 2026-09-19 — phase 90 complete (simplified load modes, 4 commits)
Branch feat/phase90-load-modes. Spec .cortex/2026-09-18-phase90-load-modes/.
Commits 94b89d9 loadmode domain + eager resolution + migration 0074;
c5aefe0 httpapi wire contract + 409 refusal matrix; d12e34f web
Simple|Advanced toggle, ModeForm, ModeConfigCard, vitest 765/765; fc9c93d
e2e + integration pins, mode.go 100% coverage. PR #379 merged all-16-green;
main e30043e. Deployed :phase91 — build hiccup: helm user-values pinned
grafana phase49, fixed with --reuse-values --set per tag. Live: SPA
index-BQHM4gZv.js, migration 0074 applied (execution_scenario.mode
VARCHAR(8) NULL). Design: 3 inputs — mode burst/ramp/soak, target_qps,
duration; server derives concurrency via Little's Law on measured p95
(floor 20, headroom 3.0), engines via FanOut, ramp policy burst 0 /
ramp clamp d/5 60..600 / soak 60s warmup; mode rides as provenance; eager
resolution in StoreConfig so compile never sees mode; no usable capacity
profile means 409 with remediation. Deferred to future phases:
multi-stage/staircase shapes, per-mode default criteria and SLOs,
re-resolve-config action, multi-scenario Simple.
Same day: dependabot sweep 34 to 0 — otel 1.45 bump, grpc excluded from
module graph, 3 stale setagaya/go.mod alerts dismissed; PR #380 main 7944ddc.
