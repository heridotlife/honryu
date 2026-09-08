You are working on Honryu web (React/TS, bun, Tailwind).

REPO: /home/coder/personal/honryu
BRANCH: feat/phase32-project-switcher (checked out, off develop @ 4426221)

Goal: Phase 32 — global project switcher in the top nav. Filtering the
operator's whole view by project, k6-style.

VERIFIED RECON:
- GET /api/projects → [{id, name, owner, tenant_id, created_time}] (live-checked).
- GET /api/executions → every execution carries project_id (live-checked:
  [{7,1},{6,1},{5,1},{4,1},{3,2},...] — project 1 "phase16-live", 2 "phase16-sched").
- web/src/api/executions.ts: Execution interface has project_id (line 10).
- web/src/pages/Reports.tsx: list-first executions page with engine filter
  chips (filter-engine-*) + search (filter-search) from phase 28.
- web/src/pages/Executions.tsx: list page w/ same filter chips.
- web/src/pages/NewTest.tsx: resolves project-if-absent by name (line 105-117).
- Nav: shared DashboardLayout component (src/components/DashboardLayout.tsx)
  renders top nav links (Reports, Executions, Reservations, Campaigns, Clusters).
- NO projects.ts api module exists. No project UI anywhere.

TASKS (one commit each, gates between):

T1 — projects api module
- web/src/api/projects.ts: Project interface {id, name, owner, tenant_id,
  created_time}; listProjects(): GET /projects → Project[] (normalize null→[]).
- web/src/api/projects.test.ts: mock fetch, null→[], passthrough shapes.

T2 — ProjectSwitcher component
- web/src/components/ProjectSwitcher.tsx: reads listProjects() once on mount.
  Renders a compact nav control: current project name (or "All projects"),
  click → dropdown LIST of projects (user rule: visible list, not a select
  input — but a popover menu is fine, k6 does this) + an "All projects" entry
  at top. Selection persists in localStorage key "honryu.project" (id or "").
  Emits selection up via prop callback. Empty/error state → hide control.
  data-testid: project-switcher, project-option-all, project-option-{id}.
- Mount in DashboardLayout nav (right side, before theme toggle).
- Test: renders options from API; click option persists to localStorage and
  fires callback; "All projects" default when nothing stored.

T3 — wire filtering into Reports + Executions
- Reports.tsx + Executions.tsx: consume selected project id (from the same
  localStorage key via a tiny useProjectSelection hook exported from
  ProjectSwitcher.tsx or a hooks file — your call, keep it simple).
  Filter execution lists client-side by project_id === selected when selected
  non-empty. Chip "project: {name}" with ✕ shown next to engine chips when
  active (data-testid="filter-project"), clicking ✕ clears to All.
- Reports.test.tsx / Executions.test.tsx (whichever exists — check first):
  test that with localStorage preset, only matching-execution rows render,
  and the filter chip shows; clearing shows all.

GATES after each: cd web && /home/coder/.bun/bin/bun run vitest run &&
/home/coder/.bun/bin/bun run tsc -b

COMMIT STYLE:
  feat(web): projects api module
  feat(web): global project switcher
  feat(web): project filtering on reports and executions

No PR, no push. vitest total must grow from 374 and stay green.