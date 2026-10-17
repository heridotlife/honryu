# Phase 108 — Schedule management UI (Executions page)

## Problem
Backend for scheduled/recurring runs is COMPLETE (scheduleapp, cron domain,
ClaimDue firing loop in cmd/scheduler, POST/GET/DELETE
/api/executions/{id}/schedules, generated client functions) but there is ZERO
web UI: an operator cannot create/view/delete a schedule without curl. k6
Cloud parity requires schedule management in-product.

## Goal
Schedule CRUD surface on the Executions page: per-execution "Schedule" action
opens a drawer/modal with the execution's schedules + create/delete.

## Design

### API shapes (from generated.ts + scheduleapp, verify against OpenAPI before coding)
- POST /api/executions/{id}/schedules — body carries kind ("one_shot"|"recurring"),
  fire_at (one-shot ISO instant) or recurrence (cron expr), window fields if
  the endpoint requires (READ scheduleapp.ScheduleView + the httpapi handler
  for the exact request/response contract — do not guess).
- GET /api/executions/{id}/schedules — list of ScheduleView.
- DELETE /api/executions/{id}/schedules/{schedule_id}.

### UI (mirror house patterns: CalibrateScenarioModal for modal, CardTable for lists)
- Executions.tsx row actions gain a clock icon "Schedule" button per execution.
- Drawer/modal: list of existing schedules (kind badge, cron/fire-at, next
  fire time if the view carries it, delete button per row with confirm);
  create form: kind segmented (One-shot | Recurring), One-shot → datetime
  input; Recurring → cron text input with 3-4 preset chips (hourly `0 * * * *`,
  daily `0 6 * * *`, weekly `0 6 * * 1`, custom). Validate client-side: cron
  field non-empty for recurring, future fire_at for one-shot. Errors from the
  API render inline (ActionErrorDetails component if it fits).
- data-testids throughout: schedule-button, schedule-drawer, schedule-row,
  schedule-kind-input, schedule-cron-input, schedule-fireat-input,
  schedule-create, schedule-delete-N.
- Empty state: EmptyState component, "No schedules — this execution fires only
  when triggered manually."

### Out of scope
- Editing an existing schedule (delete + recreate is the v1 path).
- Calendar visualization. Next-fire preview computation client-side.

## Acceptance
- Create one-shot + recurring via UI on prod; list shows them; delete works.
- vitest: modal open/close, list render, create validation (missing cron,
  past fire_at), delete confirm flow, API error rendering.
- Full gates green.
