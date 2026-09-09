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
