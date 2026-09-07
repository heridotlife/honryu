# Phase 5 — Scheduling, quota, guardrails, and a first UI — Tasks

**Plan:** `.cortex/2026-08-05-phase5-scheduling-quota-ui/plan.md` · **Spec:** `.cortex/2026-08-05-phase5-scheduling-quota-ui/spec.md`

Continues the parent `.cortex/2026-07-30-honryu/tasks.md` numbering (last task there was 30, plus 23b). Every task must leave `go test ./...`, `golangci-lint`, and `scripts/coverage.sh` (≥90%) green.

**Status (2026-08-05): task 31 done (`e2df80f`); task 23b done and reviewed in the interim (see `.cortex/2026-08-05-23b-declarative-requests/`). Resuming at task 32.** Migration numbers below bumped by one throughout (`0026`→`0027`, `0027`→`0028`, `0028`/`0029`→`0029`/`0030`): task 23b's own task 49 claimed the real `migrations/0026_scenario_requests.sql` first, as that plan's own numbering note anticipated.

---

## Group 1 — Reservation ledger & quota

### 31. Add the reservation domain — **done, `e2df80f`**
- **Files:** `internal/domain/reservation/reservation.go` (+ tests)
- **Criteria:** `Reservation{TenantID, Cluster ports.ClusterRef, EngineCount int, Start, End time.Time, ExecutionID int64}`; pure `Overlaps(other Reservation) bool` and `Validate() error` (`EngineCount > 0`, `End > Start`); property-style tests cover boundary adjacency (`End == other.Start` is *not* an overlap; `End == other.Start+1ns` is)
- **Satisfies:** spec "Approach — Quota & reservation ledger"
- **Depends on:** —

### 32. Persist reservations — **done, `9317976`**
- **Files:** `migrations/0027_reservation.sql`, `internal/ports/reservation_repository.go` (new port: `Create`, `Delete`, `InWindow(tenant, cluster, start, end) ([]Reservation, error)`), `internal/adapters/repo/mysql/reservation_repository.go`, `internal/ports/fake/repository.go`, `internal/ports/repositorytest/reservation_contract.go`
- **Criteria:** conformance suite passes against both fake and real MySQL; `InWindow` returns every reservation overlapping `[start, end)`, proven by a case straddling the boundary
- **Satisfies:** spec "Approach — Quota & reservation ledger"
- **Depends on:** 31

### 33. Add a tenant quota ceiling, per cluster — **done, `8f4ed49`**
- **Files:** `migrations/0028_tenant_quota.sql` (new `tenant_quota(tenant_id, cluster, ceiling)` table — not a column on `tenant`, since ceiling is keyed by cluster too), `internal/ports/reservation_repository.go` (add `GetCeiling`/`SetCeiling`), MySQL + fake + conformance, `internal/app/tenantapp/service.go` (`SetQuota`/`GetQuota`, mirroring `SetStatus` at `tenantapp/service.go:58`), new admin HTTP route
- **Criteria:** an admin can set a tenant's per-cluster ceiling via API; an unset ceiling reads as 0 (nothing runs until explicitly configured — no accidental unlimited default)
- **Satisfies:** spec "Open questions — how is a tenant's quota ceiling configured"
- **Depends on:** 31

### 34. Add the shared quota-check/reserve function — **done, `db144be`**
- **Files:** `internal/app/quotaapp/service.go` (new app package), tests
- **Criteria:** `Reserve(ctx, tenant, cluster, engineCount, start, end) (Reservation, error)` sums `InWindow`'s existing reservations' `EngineCount` against the ceiling, creates and returns a reservation if it fits, returns a stated-reason error if not — the *only* place the admit decision is made, called by both task 35 and task 38 rather than each reimplementing it
- **Satisfies:** spec "Constraints — same reservation mechanism, not two implementations"
- **Depends on:** 32, 33

### 35. Gate manual Trigger on quota, release on Stop — **done, `e053494`**
- **Files:** `internal/app/lifecycleapp/service.go` (`Trigger` at `service.go:186-250`; `teardown`), tests
- **Criteria:** a `Trigger` call that would breach quota is rejected before any pod is deployed, with the quota error surfaced to the caller; `Stop`ping a run releases its reservation's remaining window immediately, not at its originally declared end
- **Satisfies:** spec "Acceptance criteria — manual Trigger quota-checked... Stop releases immediately"
- **Depends on:** 34

---

## Group 2 — Scheduling, `cmd/scheduler`, kill-switch

### 36. Add the schedule domain — **done, `b6eb218`**
- **Files:** `internal/domain/schedule/schedule.go` (+ tests)
- **Criteria:** `Schedule{ID, ExecutionID, TenantID, Cluster, Kind (OneShot|Recurring), FireAt *time.Time, Recurrence string, Active bool}`; pure `Occurrences(from, to time.Time) ([]time.Time, error)` computing fire times within a window — cron expression for `Recurring` (new dependency: a well-known cron-parsing library for expression parsing/next-time computation only, not its own loop), `FireAt` alone for `OneShot`
- **Satisfies:** spec "Open questions — recurrence expression format" (resolved: cron), "Approach — Scheduler seam"
- **Depends on:** —

### 37. Persist schedules and their occurrences — **done, `e5eca7d`**
- **Files:** `migrations/0029_schedule.sql`, `migrations/0030_schedule_occurrence.sql`, `internal/ports/schedule_repository.go` (new port), MySQL + fake + conformance
- **Criteria:** `ScheduleRepository`: create/get/list/delete a schedule (delete cascades occurrences); each occurrence row tracks `(schedule_id, fire_time, status: reserved|rejected|fired|completed, reservation_id nullable)`; conformance suite passes against fake and MySQL
- **Satisfies:** spec "Approach — recurring schedule... rolling 7-day lookahead"
- **Depends on:** 36, 32

### 38. Add `scheduleapp`: create, list, delete — **done, `86f802d`**
- **Files:** `internal/app/scheduleapp/service.go` (+ tests)
- **Criteria:** `Create` computes occurrences (one-shot: its single fire time; recurring: bounded to the next 7 days), calls `quotaapp.Reserve` per occurrence, persists each with its resulting status — partial success, every occurrence's outcome visible on the stored schedule, not all-or-nothing; `Delete` releases every not-yet-fired occurrence's reservation
- **Satisfies:** spec "Acceptance criteria — partial success... visible to owner"
- **Depends on:** 34, 37

### 39. Add schedule HTTP routes — **done, `7651dd8`**
- **Files:** `internal/adapters/httpapi/schedule_handlers.go`, `router.go`, `api/openapi.yaml`
- **Criteria:** `POST /api/executions/{execution_id}/schedules`, `GET .../schedules` (lists occurrences and their statuses), `DELETE .../schedules/{schedule_id}` — following existing handler conventions (`pathInt`, ownership check, `badRequestErrors` table)
- **Depends on:** 38

### 40. Add `cmd/scheduler`: wiring + fire-due-occurrences loop — **done, `63a48f4`**
- **Files:** `cmd/scheduler/main.go` (new), `internal/app/scheduleapp/service.go` (add `ClaimDue`)
- **Criteria:** ticks on an interval; claims a due occurrence via row-locking (`SELECT ... FOR UPDATE`, mirroring `lockShard` at `report_progress.go:88-121`) so more than one replica can run without double-firing; on claim, calls `lifecycleapp.Deploy` then `Trigger` (pods created just-in-time at fire time, not held idle since schedule creation) and marks the occurrence fired; a concurrency test — N goroutines racing to claim the same due occurrence, asserting exactly one wins — mirrors `TestMySQLReportProgress_ConcurrentShardsDoNotLoseEachOthersLatency`
- **Satisfies:** spec "Acceptance criteria — more than one replica... without double-firing"
- **Depends on:** 39

### 41. Add horizon-extension to `cmd/scheduler` — **done, `1b7b616`**
- **Files:** `cmd/scheduler/main.go`, `internal/app/scheduleapp/service.go`
- **Criteria:** on a slower interval (daily), every active recurring schedule's occurrences are extended to maintain a 7-day-out horizon; records its own last-successful-run timestamp somewhere queryable, so a stalled job is observable rather than silently leaving future occurrences unguarded
- **Satisfies:** spec "Acceptance criteria — horizon always ≥7 days... observable"
- **Depends on:** 40

### 42. Add overrun reclaim to `cmd/scheduler` — **done, `63c74cd`**
- **Files:** `cmd/scheduler/main.go`, `internal/app/quotaapp/service.go`
- **Criteria:** when a tenant's new reservation can't fit because that same tenant's own earlier run has overrun its declared duration and is still occupying the capacity, the overrunning execution is torn down (existing `Stop`) to free it before the new reservation proceeds; an overrunning run whose capacity nobody needs keeps running untouched
- **Satisfies:** spec "Acceptance criteria — overrun tolerated if capacity allows; force-stopped if needed"
- **Depends on:** 40, 35

### 43. Add the kill-switch — **done, `c541e6f`**
- **Files:** `internal/app/adminapp/service.go`, new HTTP route, tests
- **Criteria:** `Abort(scope: tenant|cluster|campaign|executionList, value)` tears down every matching in-flight execution via existing `Stop`/`Purge`, within a bounded time (grace period reused from `TerminationGracePeriodSeconds`); `campaign` is a valid enum value now with nothing to match yet (returns "nothing to abort", not an error) — Phase 6 won't need to touch this endpoint
- **Satisfies:** spec "Acceptance criteria — kill-switch scoped to tenant/cluster/campaign/execution list"
- **Depends on:** —

---

## Group 3 — UI

### 44. Set up the `web/` toolchain + base API client — **done, `21a9ea3`**
- **Files:** `web/package.json`, `web/vite.config.ts`, `web/src/styles/globals.css` (Tailwind v4 + design tokens — sky/blue palette, light/dark via a `.dark` class — following heridotlife's design language, not its code), `web/src/components/ui/{Button,Card,Input}.tsx`, `web/src/components/DashboardLayout.tsx`, `web/src/api/client.ts` (base fetch wrapper: base URL, auth header attachment, typed error surfacing from the existing `httpapi` error shape)
- **Criteria:** `bun run build` produces static assets in `web/dist`; a placeholder page renders inside `DashboardLayout`; light/dark toggle works; `client.ts` has a passing unit test against a mock endpoint
- **Satisfies:** spec "Constraints — React + Tailwind v4... styled after heridotlife's admin dashboard language"
- **Depends on:** —

### 45. Serve UI static assets from `cmd/api` — **done, `0fdd889`**
- **Files:** `cmd/api/main.go`, `internal/adapters/httpapi/` (new static-serving handler)
- **Criteria:** `web/dist` assets are embedded and served; unmatched non-`/api/` paths fall back to `index.html` for client-side routing; existing `/api/*` routes are unaffected
- **Satisfies:** spec "Constraints — static assets served by cmd/api... one deployable"
- **Depends on:** 44

### 46. Report/run history page — **done, `9baba61`**
- **Files:** `web/src/pages/Reports.tsx`, `web/src/api/reports.ts`
- **Criteria:** lists an execution's reports most-recent-first (existing `GET /api/executions/{execution_id}/reports`), drills into one report's detail (existing `GET /api/runs/{run_id}/report`) — outcome, requested vs. achieved load, latency percentiles, error signatures. No new backend work.
- **Satisfies:** spec "Acceptance criteria — UI shows report/run history"
- **Depends on:** 45

### 47. Reservation calendar page — **done, `f42f941`**
- **Files:** `internal/adapters/httpapi/` (new `GET /api/tenants/{tenant_id}/reservations?from=&to=`, reusing `ReservationRepository.InWindow` from task 32), `web/src/pages/Reservations.tsx`, `web/src/api/reservations.ts`
- **Criteria:** a calendar/timeline view shows every reservation for a tenant+cluster over a selected date range, each labelled with its owning execution/schedule and status
- **Satisfies:** spec "Acceptance criteria — reservation calendar per tenant/cluster"
- **Depends on:** 32, 45

### 48. Live status page — **done, `0431843`**
- **Files:** `web/src/pages/LiveStatus.tsx`, `web/src/api/status.ts`
- **Criteria:** shows in-flight executions' current QPS/error-rate/latency, consuming the existing SSE stream endpoint (`GET .../stream`) plus execution status — no new backend work
- **Satisfies:** spec "Acceptance criteria — live status for in-flight executions"
- **Depends on:** 45
