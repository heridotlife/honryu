# Phase 6 — Campaign, freeze, verdict rollup: tasks

Continues the parent tasks.md's global numbering. Phase 5 was tasks 31-48
(done); task 23b (interleaved, separately-scoped) took 49-53 (done). Phase 6
starts at **54**.

Groups, for orientation:

- **54-59** — campaign domain, persistence, app service, RBAC, and HTTP routes (create/list/get).
- **60-63** — freeze: shared in-scope resolver, `lifecycleapp.Trigger` gating, `cmd/scheduler`'s drain loop, kill-switch wiring.
- **64-66** — verdict: criteria evaluator, rollup + HTTP route, campaign report's "other load" annotation.
- **67-68** — UI: campaigns API client, campaigns page.
- **69** — end-to-end proof of the whole loop.

---

### 54. Add the campaign domain type — **done, `1a22114`**
- **Files:** `internal/domain/campaign/campaign.go`, `internal/domain/campaign/campaign_test.go`
- **Criteria:** `Campaign{ID, Name, TenantID, Window{Start,End}, Services []Service{ProjectID, ExecutionID}, AbortedAt *time.Time}`; `Validate()` (name required, window end after start, at least one service, no duplicate ProjectID across services); `IsActive(now time.Time) bool` derived from window + AbortedAt (mirrors `run.DerivePhase`, `internal/domain/run/run.go:55-59` — no stored status enum). Pure domain, no I/O, table-driven tests for Validate and IsActive's window/abort boundaries.
- **Satisfies:** spec "Approach — Domain"; Non-goal "no new Service aggregate" (Campaign references Project/Execution IDs only)
- **Depends on:** none

### 55. Persist campaigns and their service bindings — **done, `2dced0c`**
- **Files:** `migrations/0032_campaign.sql`, `migrations/0033_campaign_service.sql`, `internal/ports/campaign_repository.go`, `internal/adapters/repo/mysql/campaign_repository.go`, `internal/ports/fake/campaign_repository.go`, `internal/ports/repositorytest/campaign_contract.go`, `internal/adapters/repo/mysql/campaign_integration_test.go`
- **Criteria:** two tables mirroring `migrations/0029_schedule.sql`/`0030_schedule_occurrence.sql`'s shape (one row per campaign; one row per service binding, FK'd by campaign_id). `ports.CampaignRepository`: `CreateCampaign`, `GetCampaign`, `ListCampaignsByTenant`, `ListActiveCampaigns` (window contains now, not aborted — needed by both freeze and the drain loop), `AbortCampaign` (sets AbortedAt). Conformance suite run against fake + real MySQL, including a case proving `ListActiveCampaigns` excludes both not-yet-started, already-ended, and aborted campaigns.
- **Satisfies:** spec "Approach — Persistence"
- **Depends on:** 54

### 56. Add campaignapp: Create, Get, List, Abort — **done, `0ff775d`**
- **Files:** `internal/app/campaignapp/service.go`, `internal/app/campaignapp/service_test.go`
- **Criteria:** `Create` validates and persists a campaign (each `ExecutionID` in `Services` must belong to the stated `ProjectID` — cross-check via the execution's own `ProjectID`, mirroring the project/execution ownership check `authorizeExecution` already does at the HTTP layer, but here as a domain-level invariant). `Get`/`List` read-through. `Abort` sets `AbortedAt` via `AbortCampaign` — engine teardown and the shared in-scope resolver land in task 63, not here; this task is the CRUD spine only.
- **Satisfies:** spec "Acceptance criteria — a caller can create a campaign: name, window, and one or more (Project, Execution) service bindings"
- **Depends on:** 55

### 57. Add ResourceCampaign and the tenant-scoped RoleCampaignManager — **done, `1e00418`**
- **Files:** `internal/domain/rbac/rbac.go`, `internal/domain/rbac/rbac_test.go`
- **Criteria:** new `ResourceCampaign = "campaign"` alongside the existing consts (`internal/domain/rbac/rbac.go:22-31`); new `RoleCampaignManager = "campaign_manager"` alongside `RoleTenantAdmin`/`Editor`/`Viewer` (line ~38-41); added to `DefaultCatalog()` (line 122) as `TenantScoped: true` with `{Resource: ResourceCampaign, Actions: all}` (create/read/update/delete/list/admin) — the tenant-scoped role can also read `ResourceProject`/`ResourceExecution` (read only) so a PM can see what they're binding without needing a separate editor grant. Table test proves a plain `tenant_editor` cannot satisfy `ResourceCampaign`/`ActionCreate`, and a `campaign_manager` can.
- **Satisfies:** spec "Constraints — campaign creation requires RoleCampaignManager, not merely project-edit rights"
- **Depends on:** none (parallel to 54-56)

### 58. Add campaign HTTP routes: create, list, get — **done, `925be53`**
- **Files:** `internal/adapters/httpapi/campaign_handlers.go`, `internal/adapters/httpapi/campaign_handlers_test.go`, `internal/adapters/httpapi/rbac_router_test.go`, `internal/adapters/httpapi/router.go`, `internal/adapters/httpapi/errors.go`, `api/openapi.yaml`
- **Criteria:** `POST /api/tenants/{tenant_id}/campaigns`, `GET /api/tenants/{tenant_id}/campaigns`, `GET /api/campaigns/{campaign_id}`. `authorizeCampaignTenant` requires `ResourceCampaign`/`ActionCreate` scoped to the URL's `tenant_id` (global or tenant-scoped grant) — same shape as `authorizeScheduleTenant` (`internal/adapters/httpapi/schedule_handlers.go`) added this session, reusing the `rbac_router_test.go` fixture pattern (alice/globex-style cross-tenant-denial test) rather than re-deriving it. No-auth mode: unchanged/open, matching every other route.
- **Satisfies:** spec "Acceptance criteria — a RoleCampaignManager-authorized caller can create a campaign... a caller without that role is rejected"
- **Depends on:** 56, 57

### 59. Add ListActiveCampaigns-backed campaign lookup by tenant, and campaign detail response shape — **done, folded into `925be53`**
- **Files:** `internal/adapters/httpapi/campaign_handlers.go`, `internal/adapters/httpapi/campaign_handlers_test.go`
- **Criteria:** campaign detail response includes computed `active`/`aborted` fields (from `IsActive`, task 54) alongside the raw window/services, so a caller never has to derive activity state client-side. List response is ordered by window start. (Split from 58 only because 58 already covers the RBAC-gating proof; this task covers response-shape correctness and is small — fold into 58's commit if it turns out trivial once written.)
- **Satisfies:** spec "Approach — Campaign.Active is derived, not stored" surfaced through the API
- **Depends on:** 58

### 60. Add campaignapp's shared in-scope-execution resolver — **done, `082973e`**
- **Files:** `internal/app/campaignapp/service.go`, `internal/app/campaignapp/service_test.go`
- **Criteria:** `InScopeExecutions(ctx, campaignID) ([]int64, error)` returns every *currently-deployed* execution belonging to one of the campaign's participating projects, **excluding** each service's own designated execution. `IsFrozen(ctx, projectID, executionID) (blocked bool, campaignName string, err error)` — true when `executionID` is not a designated execution for any active campaign whose services include `projectID`. Both are pure queries against `campaignapp.Repo` (no side effects) so tasks 61-63 can each depend on them without duplicating the "what counts as in-scope" logic (plan risk #2).
- **Satisfies:** spec "Approach — the same 'resolve in-scope executions' function is reused by the kill-switch's ScopeCampaign case"
- **Depends on:** 56

### 61. Gate lifecycleapp.Trigger on campaign freeze — **done, `5d5b3b5`**
- **Files:** `internal/app/lifecycleapp/service.go`, `internal/app/lifecycleapp/freeze_test.go`
- **Criteria:** new `Freeze` interface (`IsFrozen(ctx, projectID, executionID int64) (blocked bool, campaignName string, err error)`), `noopFreeze` default, `WithFreeze`, a `freeze` field — structured identically to `Quota`/`noopQuota`/`WithQuota` (`internal/app/lifecycleapp/service.go:82-93,157-163`). `Trigger` calls it immediately after `coll` loads (before `scenarios`/`ensureTestFiles`/`CurrentRun`, per plan risk #1) using `coll.ProjectID` and `executionID`; a true result returns a new `ErrCampaignFrozen` naming the blocking campaign, before any other Trigger side effect. Existing behavior (no campaign wired, or execution's project not in any active campaign) is provably unchanged — a regression test triggers successfully with `WithFreeze` unset.
- **Satisfies:** spec "Acceptance criteria — Trigger rejects... with a stated reason identifying the blocking campaign... the designated execution... can still be triggered normally"
- **Depends on:** 60

### 62. Add cmd/scheduler's campaign-drain tick loop — **done, `3e72b33`**
- **Files:** `cmd/scheduler/main.go`, `cmd/scheduler/main_test.go`, `cmd/scheduler/main_integration_test.go`
- **Criteria:** `runDrainLoop`/`drainOnce`, joined via the existing `sync.WaitGroup` alongside `runLoop`/`runHorizonLoop`, on `cfg.Scheduler.TickInterval` (reuses the existing interval — spec's "within one scheduler tick" — no new config field). Each tick: `ListActiveCampaigns`, then for each, `InScopeExecutions` (task 60) and `lifecycle.Stop` on every result. Best-effort per execution (one failure logged, doesn't block the rest), matching `fireOnce`'s existing error-handling style.
- **Satisfies:** spec "Acceptance criteria — an in-flight non-campaign execution within scope at window-open is stopped within one scheduler tick"
- **Depends on:** 60

### 63. Wire adminapp's ScopeCampaign and close-on-abort — **done, `f91ad1d`**
- **Files:** `internal/app/adminapp/service.go`, `internal/app/adminapp/service_test.go`, `cmd/api/main.go`, `cmd/scheduler/main.go`
- **Criteria:** new `CampaignScoper` dependency interface (`InScopeExecutions`, `AbortCampaign`) satisfied by `campaignapp.Service`; `matchingExecutions`'s `ScopeCampaign` case (`internal/app/adminapp/service.go:166-167`) parses `value` as a campaign ID and calls `InScopeExecutions` — now also including each service's *designated* execution if currently deployed (unlike the drain loop, kill-switch tears down everything, including the readiness tests themselves — an abort is total). `Abort` calls `AbortCampaign` after the purge loop when `scope == ScopeCampaign`, regardless of whether every execution purged cleanly (matching the existing "partial abort is visible, not silently complete" philosophy) — freeze lifts immediately since `IsActive`/`IsFrozen` (tasks 54, 60) are derived from `AbortedAt`, not a separate flag to keep in sync.
- **Satisfies:** spec "Acceptance criteria — aborting a campaign tears down every currently-deployed in-scope execution and marks the campaign closed; freeze lifts immediately"
- **Depends on:** 60

### 64. Add the criteria evaluator — **done, `d681992`**
- **Files:** `internal/domain/report/criteria.go`, `internal/domain/report/criteria_test.go`
- **Corrected during execution:** the plan originally placed this in `internal/domain/taurus`, taking a `report.Report` parameter -- impossible, since `report` already imports `taurus` (`internal/domain/report/report.go:20`) and Go forbids the reverse import. Moved to `internal/domain/report` instead, as a method on `Report` itself (`func (r Report) EvaluateCriteria(criteria []string) []FailedCriterion`) -- no new package, no cycle, and it's naturally colocated with the type it reads.
- **Criteria:** `Report.EvaluateCriteria(criteria []string) []FailedCriterion` parses a bounded subject set bzt actually supports that `Report` (`internal/domain/report/report.go:73-83`) has data for — at minimum `failures`/`fail` (against `ErrorRate`) and whichever percentile keys are present in `Latency` (a `Percentiles` map) — against a simple `subject op threshold[%|ms|s]` grammar. A criterion whose subject isn't supported is returned as "unparsed" (named separately from a genuinely failing one), never silently dropped or mis-evaluated. Table tests cover: a passing criterion, a failing one (named exactly), an unparsed subject, and a criterion with no unit suffix.
- **Satisfies:** spec "Non-goals — a defined practical subset... unparseable criteria degrade to 'outcome known, criterion text not shown', not a wrong label"; "Approach — Verdict"
- **Depends on:** none (parallel to 54-63; pure function over existing `report.Report`/`taurus.Reporter.Criteria` types)

### 65. Add campaign verdict rollup and its HTTP route — **done, `cc41d61`, `9a9d3a8`**
- **Files:** `internal/app/campaignapp/verdict.go`, `internal/app/campaignapp/verdict_test.go`, `internal/adapters/httpapi/campaign_handlers.go`, `internal/adapters/httpapi/campaign_handlers_test.go`, `api/openapi.yaml`
- **Criteria:** `Verdict(ctx, campaignID) (CampaignVerdict, error)` — per service: the designated execution's latest `report.Report` (existing report retrieval), its `Outcome`, and (when `failed`) the named failing criteria via task 64's evaluator against that execution's own configured criteria (existing `GET .../config` retrieval). Overall go/no-go: go only if every service's outcome is `passed`. `GET /api/campaigns/{campaign_id}/verdict` returns it; read access requires visibility into at least one participating project (reuses `authorizeProject`'s existing per-project check, not a fresh `RoleCampaignManager` requirement — a service owner should be able to see their own campaign's verdict without a PM-level grant).
- **Satisfies:** spec "Acceptance criteria — per-service outcome, specific failing criteria named, one overall go/no-go"
- **Depends on:** 56, 64

### 66. Annotate the campaign report with other load active during the window — **done, `a6053c6`**
- **Files:** `internal/app/campaignapp/verdict.go`, `internal/app/campaignapp/verdict_test.go`
- **Criteria:** `Verdict`'s response additionally lists every reservation/execution active in the campaign's cluster(s) during its window, sourced from `ports.ReservationRepository.ReservationsInWindow` and `usageapp`'s existing history query (both from Phase 5, no new instrumentation), filtered to exclude the campaign's own participating (designated) executions.
- **Satisfies:** spec "Acceptance criteria — the campaign's report records every other reservation/execution active in its cluster(s)... excluding its own participating executions"; parent spec residual risk #12's "minimum mitigation"
- **Depends on:** 65
- **Implementation notes:** Added `OtherLoad`/`otherLoadResponse` end to end (`campaignapp.Verdict` → `getCampaignVerdict` → `api/openapi.yaml`). `campaignapp.Repo` gained `ReservationsInWindow`/`LaunchHistory`; `cmd/scheduler`'s local `repository` interface needed `ports.UsageRepository` added for the same reason task 65 needed `ports.ReportStore`. Reservations are scoped to the campaign's tenant by `ReservationsInWindow` itself; launch history has no such scoping (it's a global log), so records are matched back to their execution and dropped unless that execution's `TenantID` equals the campaign's, to avoid leaking other tenants' execution ids to a viewer authorized via only one participating project.
- **Coverage gate discovery:** `scripts/coverage.sh`'s repo-wide ≥90% threshold was already failing at task 65's committed HEAD (89.5%, confirmed by stashing this task's changes and re-running) — pre-existing debt in adminapp/lifecycleapp/quotaapp unrelated to Phase 6. This task's own code is thoroughly tested (96%+ on campaignapp, every new branch covered) and nudges the total up slightly; closing the rest is out of scope here. User confirmed: land as-is, track the repo-wide shortfall separately.

### 67. Add the campaigns API client — **done, `fae1144`**
- **Files:** `web/src/api/campaigns.ts`, `web/src/api/campaigns.test.ts`
- **Criteria:** types + fetchers for create/list/get/verdict, mirroring `web/src/api/reservations.ts`'s and `web/src/api/status.ts`'s conventions exactly (including normalizing any Go nil-slice-as-`null` field the same way `listExecutionReports`/`getExecutionStatus` already had to — check each new list-shaped response field against a fresh fake-store server before assuming it's safe, per this session's established pattern of two prior real bugs caught exactly this way).
- **Satisfies:** spec "Approach — UI"
- **Depends on:** 58, 65
- **Corrected during execution:** `ApiClient` (`web/src/api/client.ts`) only had `get` — Phase 5's SPA was read-only, so no mutating call existed yet. Added `ApiClient.post` (form-urlencoded, matching every Go handler's `r.ParseForm()`) plus its own `client.test.ts` case, since `createCampaign` needed it and no other file was a better home for shared HTTP-client infrastructure.

### 68. Add the Campaigns page — **done, `005e298`**
- **Files:** `web/src/pages/Campaigns.tsx`, `web/src/pages/Campaigns.test.ts`, `web/src/App.tsx`, `web/src/components/DashboardLayout.tsx`
- **Criteria:** a create form (name, window start/end, add service rows each naming a project + its designated execution) and a detail view (per-service status badges, overall go/no-go, named failing criteria, other-load annotation) — styled consistently with `Reports.tsx`/`Reservations.tsx`/`LiveStatus.tsx`. New nav entry. Verified against a live `cmd/api` instance (fake driver), not just unit tests, per this session's established verification bar.
- **Satisfies:** spec "Acceptance criteria — the SPA has a Campaigns page: create a campaign, and view a campaign's rolled-up verdict"
- **Depends on:** 67
- **Verification caveat:** no browser/screenshot tool was available this session, so the page's actual rendering/interaction was not visually observed. Verified instead: `tsc -b`/`vite build` clean, pure logic (`campaignStatus`/`serviceStatus`) unit-tested, the built bundle served correctly through a live `cmd/api` at `/` and `/campaigns` (SPA fallback), and the page's exact create-form POST (content-type + body) round-tripped against the real route via curl.

### 69. End-to-end: full campaign happy path — **done, `e151037`**
- **Files:** `test/e2e/phase6_e2e_test.go`
- **Criteria:** create a campaign with 2 participating services; confirm a non-designated execution under a participating project is rejected with a stated reason while the window is open; confirm the designated execution triggers normally; start a non-campaign execution just before window-open and confirm the drain loop stops it within one tick; close the window (or abort); retrieve the verdict and confirm per-service outcomes, named failing criteria for an intentionally-failing service, and the overall go/no-go; confirm the kill-switch's `ScopeCampaign` tears down and closes an active campaign. Runs alongside the existing 6 e2e tests (`make e2e`).
- **Satisfies:** spec "Acceptance criteria" (all of them, in one integrated proof)
- **Depends on:** 61, 62, 63, 65, 66
- **Implementation note:** `lifecycleapp.Stop` (what the drain sweep calls) only ends a run -- unlike `Purge`, it never calls `sched.PurgeExecution`, so the stopped execution's pods stay deployed. The drained execution therefore still shows up in `InScopeExecutions` afterward, and the later kill-switch abort tears down three executions (both designated plus the drained one), not two -- the test's assertion reflects this rather than assuming Stop undeploys.

## Post-completion review fixes (2026-08-07)

`requesting-code-review` over the full `195dbf9..HEAD` diff (all of Phase 6) surfaced three findings, all fixed:

1. **`campaignapp.Create` never checked a participating project belonged to the campaign's own tenant** — a campaign manager in one tenant could name another tenant's project/execution ids and have freeze, the drain sweep, and the kill-switch's `ScopeCampaign` all act on that project without its tenant's knowledge. Fixed by checking the project's own `TenantID` against the campaign's, via a new `GetProject` dependency on `campaignapp.Repo`. Commit `2bc7c24`.
2. **`Verdict`'s `OtherLoad` launch-history filter checked `execution.TenantID`, which no execution-creation path populates** — silently dead in production (only test fixtures set it directly). Fixed by resolving tenancy via the execution's own project instead, sharing the same `GetProject` dependency. Commit `2bc7c24`.
3. **`executionapp.StoreConfig`'s two repo writes (load profile, then criteria) weren't atomic** — a transient failure between them could desync the two while the caller saw a hard failure implying nothing was saved. Fixed with a new `ExecutionRepository.StoreExecutionConfig` that replaces both in one transaction. Commit `174d81d`.

All fixes verified: full unit suite, full e2e suite (including a live re-run of the cross-tenant exploit via curl confirming 400, and the MySQL integration contract for the new combined write), `golangci-lint`, `gofmt`.
