# Phase 6 — Campaign, freeze, verdict rollup: plan

Spec: `.cortex/2026-08-07-phase6-campaign-freeze-verdict/spec.md` (agreed, zero open questions). Tasks: `.cortex/2026-08-07-phase6-campaign-freeze-verdict/tasks.md`.

## Context

- `lifecycleapp.Trigger` (`internal/app/lifecycleapp/service.go:234-271`) is the single funnel both manual triggers and scheduled fires go through. It already has an opt-in `Quota` interface (`WithQuota`, lines 82-93/157-163), checked at line 265-271 using `coll.TenantID` — the exact pattern `Freeze` mirrors, using `coll.ProjectID` instead.
- `adminapp.Service.matchingExecutions` (`internal/app/adminapp/service.go:162-172`) has a deliberate `ScopeCampaign` stub at line 166-167 (`return nil, nil`) waiting for this phase.
- `cmd/scheduler/main.go`'s `run()` joins `runLoop` (fire-due, on `cfg.Scheduler.TickInterval`, default 30s) and `runHorizonLoop` via one `sync.WaitGroup` — a third loop joins the same way.
- `rbac.go`: `Resource` consts at lines 22-31, `Role`/`Permission` structs at 47/66, `DefaultCatalog()` at 122 with 4 existing roles (1 global, 3 tenant-scoped).
- `run.DerivePhase` (`internal/domain/run/run.go:55-59`) is the exact precedent for "derive state from facts, don't store a redundant status" that `Campaign`'s active/closed state follows.
- `migrations/0029_schedule.sql` + `0030_schedule_occurrence.sql` are the two-table persistence precedent `campaign`/`campaign_service` mirrors.
- `report.Report` (`internal/domain/report/report.go:73-83`) carries `ErrorRate`, `Achieved.Throughput`, `Latency` (a `Percentiles` map) — what the criteria evaluator reads. `taurus.Reporter.Criteria []string` (`internal/domain/taurus/taurus.go:141`) already carries the configured pass/fail expressions, retrievable via the existing config-upload route.
- `internal/adapters/httpapi/schedule_handlers.go`'s `authorizeScheduleTenant` (added this session, fixing a Phase 5 review finding) is the direct precedent for `authorizeCampaignTenant` — a client-declared `tenant_id` checked against the caller's actual RBAC grant for that specific tenant, not merely resource ownership. `internal/adapters/httpapi/rbac_router_test.go`'s fixture (`newRBACFixture`, `createTenant`, `assignRole`) is the direct precedent for this phase's own RBAC tests.

## Approach

New `campaign` domain package + `campaignapp` use-case + two-table persistence (mirrors schedule); a `Freeze` interface on `lifecycleapp.Service` structured identically to `Quota`, checked first in `Trigger` (before any other I/O, right after `coll` loads); a third `cmd/scheduler` tick loop on `TickInterval` for draining; one shared "resolve in-scope executions for a campaign" function in `campaignapp` used by both the drain loop and `adminapp`'s `ScopeCampaign` wiring; a new `RoleCampaignManager`/`ResourceCampaign` RBAC pair; a pure criteria evaluator over `report.Report` + `taurus.Reporter.Criteria`, bounded to subjects Report actually has data for; a campaign report annotation reusing Phase 5's `ReservationsInWindow`/`usageapp` history; new HTTP routes; one new SPA page.

## Risks

1. **Freeze check ordering in `Trigger`.** Inserting it must not change existing error semantics for the non-frozen path. Mitigation: insert immediately after `coll` loads, before `scenarios`/`ensureTestFiles`/`CurrentRun` — a pure existence check needing only `coll.ProjectID` and `executionID`.
2. **Drain sweep and kill-switch could disagree on "in scope."** Mitigation: one function in `campaignapp` (task 60), both call sites (62, 63) depend on it — no duplicated logic to drift. Note the one deliberate asymmetry: the drain loop excludes each service's own designated execution (it's meant to keep running); the kill-switch's total abort does not (an abort tears down everything, including the readiness tests).
3. **bzt's criteria grammar is richer than what Report can evaluate.** Mitigation: explicit bounded subject support (already a stated non-goal), degrade to "outcome known, criterion not named" rather than guess.
4. **Two new RBAC-gated route groups.** Mitigation: reuse the `rbac_router_test.go` fixture pattern already proven this session for schedule's tenant-scoped check, rather than re-deriving.

## Out of scope

Calibration rollup exclusion (label-only, `CalibrateEngine` doesn't exist until Phase 7); cluster-level freeze or declared-dependency mechanisms; a general bzt criteria-grammar parser; multi-cluster campaign scoping (single implicit cluster, same as Phase 5).

## Verification

Same battery as every prior phase: `go build`/`vet`, unit tests, `golangci-lint`, a shared conformance suite for `campaign`/`campaign_service` run against fake + real MySQL, all e2e tests (task 69 adds a 7th), `scripts/coverage.sh` ≥90%.
