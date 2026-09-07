# Phase 5 — Scheduling, quota, guardrails, and a first UI — Plan

**Spec:** `.cortex/2026-08-05-phase5-scheduling-quota-ui/spec.md` · **Parent plan:** `.cortex/2026-07-30-honryu/plan.md` · **Parent tasks:** `.cortex/2026-07-30-honryu/tasks.md` (this becomes tasks 31-48 there)

## Context

What exists now, grounded in the actual code:

- `lifecycleapp.Trigger` (`internal/app/lifecycleapp/service.go:186-250`) validates via `run.CanTrigger`, then calls `s.repo.StartRun` — the exact point-of-no-return where a quota/reservation check needs to gate, before `StartRun` runs.
- `loadprofile.Entry` (`internal/domain/loadprofile/loadprofile.go:25-37`) already carries `Duration int` and `Engines int`; `Profile.TotalEngines()` (line 84) sums across entries — both directly usable for a reservation's `engine_count`/`end_time` without new fields.
- `ports.ClusterRef` is `type ClusterRef string` (`internal/ports/scheduler.go:19`), already threaded through every `Scheduler` method — the seam Phase 5 reuses rather than inventing.
- `tenant.Tenant` (`internal/domain/tenant/tenant.go`) has no quota field today — `Name`, `DisplayName`, `Status`, `CreatedTime` only.
- The row-lock-then-act idiom lives in `lockShard` (`internal/adapters/repo/mysql/report_progress.go:88-121`) — INSERT-if-missing, then `SELECT ... FOR UPDATE`. This is the pattern `cmd/scheduler`'s due-occurrence claiming reuses.
- `SetScenarioKind` (`internal/ports/fake/repository.go:282-293`, mirrored in the MySQL adapter) is the direct template for a new repo mutation method: map lookup, `ports.ErrNotFound` if absent, mutate, store back.
- `cmd/api/main.go` wires every adapter and app service in `run()`; `cmd/scheduler` follows the same shell shape but wires a narrower set.
- `internal/adapters/httpapi/router.go:150-152` builds routes on a plain `http.ServeMux` — no static-file serving exists yet; the UI needs that added.
- Migrations run through `0025_report_progress_shard_scenario_id.sql`; new ones continue that numbering.
- `internal/ports/repositorytest/` holds one contract file per port family — new ports need their own contract file there, run against both the fake and MySQL.
- `adminapp.Service` (`internal/app/adminapp/service.go`) already hosts fleet-wide operational methods (`NodePools`, `AutoPurgeStale`, `RunningExecutions`) — the natural home for the kill-switch.
- The existing SSE stream route (`GET .../stream`, wired to `metricsapp`'s event bus) and execution-status endpoint already provide everything the live-status UI page needs — no new backend work for that page.
- Report endpoints from task 30 (`GET /api/runs/{run_id}/report`, `GET /api/executions/{execution_id}/reports`) already provide everything the report/history UI page needs.

## Approach

Three roughly-independent seams, built bottom-up so each is verifiable before the next depends on it:

1. **Reservation ledger** (domain + persistence + the quota-check itself) — the foundation everything else calls into. A time-bounded `Reservation(tenant, cluster, engine_count, start, end)`; every accepted run (manual `Trigger` or a scheduled occurrence) gets one for `[start, start+duration)`. Admission is an interval-overlap check against a tenant+cluster ceiling, not a running counter — chosen over a simpler live-usage-count check specifically because a week-out one-shot must be *guaranteed*, not best-effort.
2. **Scheduling** (schedule domain + persistence + `cmd/scheduler` firing/horizon-rolling) + **kill-switch** — both consume the reservation ledger. `cmd/scheduler` is a separate deployment from `cmd/api` so a scheduler stall doesn't ride along with API restarts; safe multi-replica operation comes from row-locking (the existing `lockShard` idiom), not leader election. A recurring schedule reserves a rolling 7-day-ahead window, extended by a background job whose staleness is observable. An execution that overruns its declared duration is tolerated until the same tenant's own new reservation needs that capacity, at which point it's force-stopped.
3. **UI** — consumes the REST API surface the above two produce. React + Tailwind v4 SPA, static assets served by `cmd/api` (one deployable, same k8s rollout), styled after heridotlife's admin-dashboard design language. Read-only this phase: report/run history, a reservation calendar, live status.

## Risks

| Risk | Mitigation |
|---|---|
| Reservation overlap-check gets interval arithmetic wrong at window boundaries, silently over- or under-admitting. | Property-style tests in the pure-domain package before any persistence exists, covering exact-boundary adjacency. |
| `cmd/scheduler` double-fires a due occurrence under >1 replica. | Row-locking claim (proven pattern already in this codebase), verified with a concurrency test mirroring `TestMySQLReportProgress_ConcurrentShardsDoNotLoseEachOthersLatency`. |
| The horizon-extension job silently stalls, leaving future occurrences unguarded. | Job records its own last-successful-run timestamp, exposed so staleness is observable, not silent. |
| UI scope creep — a write action sneaking into a "read-only" page. | Task list enforces read-only explicitly across every UI task; a write action is out of scope for all of them this phase. |

## Out of scope

Everything the spec excluded: multi-cluster registry (Phase 8), campaign freeze (Phase 6), the `CalibrateEngine` execution kind itself (Phase 7 — the guardrail mechanism must be generic enough to govern it later, but Phase 5 doesn't build it), UI write actions, and task 23b (declarative scenario requests — scoped and executed separately).

## Verification

Per-task: `go build ./...`, `go test ./...`, `golangci-lint run`, `scripts/coverage.sh` (≥90%) — same discipline as every prior phase. New ports get a fake + conformance suite before any adapter consumes them. `cmd/scheduler` gets a concurrency integration test proving no double-fire under concurrent replicas. UI tasks get a manual smoke check since there's no existing frontend test harness to extend yet.
