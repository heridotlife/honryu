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
