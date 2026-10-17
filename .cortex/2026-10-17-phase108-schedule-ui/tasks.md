# Phase 108 tasks

1. **API contract recon + client wrappers** — read scheduleapp handler +
   OpenAPI for exact request shapes; expose typed create/list/delete wrappers
   in web/src/api (reuse generated client where shapes match; hand-write only
   if the generated one is wrong). One commit.
2. **ScheduleModal component** — drawer with list + create form + delete,
   house modal/CardTable patterns, full testid coverage. One commit.
3. **Executions page wiring** — Schedule action button per row opens modal,
   refresh list after mutations. One commit.
4. **Gates + polish** — vitest suite for all three, tsc, any test fixes. One
   commit only if needed.
