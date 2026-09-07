# Phase 20 — Tasks

Plan: `.cortex/2026-09-03-phase20-profiles-rbac/plan.md`
Spec: `.cortex/2026-09-03-phase20-profiles-rbac/spec.md`

Hard gate: **A → B → C must all be green before D starts** (task 15 is the gate).

## Block A — Tenant propagation

### 1. Stamp tenant on execution and scenario create
- **Files:** `internal/app/executionapp/service.go`, `internal/app/executionapp/service_test.go`, `internal/app/scenarioapp/service.go`, `internal/app/scenarioapp/service_test.go`
- **Criteria:** `executionapp.Repo` and `scenarioapp.Repo` gain `GetProject(ctx, id) (project.Project, error)` (both `fake.Store` and `mysql.Repository` already implement it — no new adapter code). `Service.Create` resolves the project and sets the created row's `TenantID` to the project's `TenantID`, including the nil case. Unit test: execution/scenario created under a project with `TenantID=&N` gets `TenantID=&N`; under a project with `TenantID=nil` gets `nil`.
- **Satisfies:** spec Approach A; AC2
- **Depends on:** none

### 2. Migration 0049: backfill tenant_id on project, scenario, execution
- **Files:** `migrations/0049_tenant_backfill.sql`
- **Criteria:** Creates a `default` tenant only if none exists (idempotent). Backfills any `project` row with `NULL tenant_id` to the default tenant, then backfills `scenario`/`execution` from their (now non-null) project's `tenant_id`. Applies cleanly in the standard fresh-DB integration run (`TestMigrate_IsIdempotent`-style: `project_repository_integration_test.go:88`). Live check by hand: `SELECT COUNT(*) FROM project|scenario|execution WHERE tenant_id IS NULL` is 0 before merge closes, and every execution's `tenant_id` equals its project's.
- **Satisfies:** spec Approach A; AC1
- **Depends on:** none

### 3. Provision live tenant fixtures
- **Files:** none (operational — `POST /api/tenants`, already routed at `router.go:183`)
- **Criteria:** Live database has exactly 3 tenants (ids 1, 2, 3), confirmed via `GET /api/tenants`. Prerequisite for Dave (campaign_manager in tenants 1 and 2, holding nothing in 3 — spec AC8) and for block E's `demo.profiles` values, which reference these ids by number.
- **Satisfies:** spec Approach A "Tenant fixtures are a prerequisite"; unblocks AC8
- **Depends on:** 2

## Block B — Resource-model surgery

### 4. Add ResourceSchedule/ResourceReport and repair role permissions
- **Files:** `internal/domain/rbac/rbac.go`, `internal/domain/rbac/rbac_test.go`
- **Criteria:** New constants `ResourceSchedule = "schedule"`, `ResourceReport = "report"`. `DefaultCatalog()` changes exactly as the spec's table (Approach B): `tenant_admin += tenant:admin`; `tenant_editor += schedule:write, report:read+list`; `tenant_viewer += schedule:read+list, report:read+list`; `campaign_manager += schedule:read+list, report:read+list` and gains no write permission anywhere outside `campaign`. Table-driven unit test, one case per added/changed `Role.Can(resource, action)` combination, including the negative (`campaign_manager` cannot `schedule:create`).
- **Satisfies:** spec Approach B; sets up bug 2 and bug 3 fixes
- **Depends on:** none

### 5. Parameterize authorizeProject/Execution/Scenario by action; add authorizeRun
- **Files:** `internal/adapters/httpapi/ownership.go`, `execution_handlers.go`, `scenario_handlers.go`, `lifecycle_handlers.go`, `schedule_handlers.go`, `calibration_handlers.go`, `project_handlers.go`, and their `_test.go` files
- **Criteria:** `authorizeProject` (`ownership.go:43`), `authorizeExecution` (`execution_handlers.go:269`), `authorizeScenario` (`scenario_handlers.go:331`) each take an explicit `rbac.Action` instead of hardcoding `ActionUpdate`. New `authorizeRun(ctx, executionID, action)` uses `rbac.ResourceRun` (the resource `rbac.go` reserves for lifecycle actions — currently referenced by zero handlers) and replaces `lifecycle_handlers.go:32,91`'s calls to `authorizeExecution`. Every one of the ~20 existing call sites (`execution_handlers.go`, `scenario_handlers.go`, `calibration_handlers.go`, `schedule_handlers.go`, `project_handlers.go`) passes the action matching its HTTP verb: `ActionRead`/`ActionList` for GETs, `ActionCreate`/`ActionUpdate`/`ActionDelete` for mutations. Test: `tenant_viewer` (holds `project/execution/scenario: read,list` only) can `GET` an execution it can see (200) and gets 403 on `DELETE` — the concrete fix for bug 1.
- **Satisfies:** spec Approach B "A read path that does not demand write"; fixes bug 1
- **Depends on:** 4

### 6. Re-point the two broken tenant gates
- **Files:** `internal/adapters/httpapi/schedule_handlers.go`, `internal/adapters/httpapi/campaign_handlers.go`, their `_test.go` files
- **Criteria:** `authorizeScheduleTenant` (`schedule_handlers.go:172`) authorizes `rbac.ResourceSchedule`/`ActionCreate` instead of `ResourceProject`/`ActionUpdate`. `authorizeAnyParticipatingProject` (`campaign_handlers.go:351`, used by `getCampaignVerdict:247` and `getCampaignComparison:315`) first tries `campaign:read` on the campaign's own tenant (mirroring `authorizeCampaignTenant:379`), falling back to the existing per-participating-project check. Test: `campaign_manager` `GET /api/campaigns/{id}/verdict` → 200.
- **Satisfies:** spec Approach B; fixes bug 2
- **Depends on:** 4

### 7. Regression checkpoint: the three documented bugs, fixed
- **Files:** `internal/adapters/httpapi/rbac_router_test.go`
- **Criteria:** Three `rbacFixture`-based tests, one per bug from the spec's "three live bugs" section — `tenant_viewer` reads-but-cannot-delete an execution; `campaign_manager` reads its own campaign's verdict; `tenant_admin` reaches a `tenant:admin`-gated route (e.g. `GET /api/tenants/{id}/quota`) — all green. `make test` passes.
- **Satisfies:** spec "the three live bugs this hides"; gates block C
- **Depends on:** 5, 6

## Block C — Full authorization audit (75 routes)

### 8. Build the route-authorization table test (red)
- **Files:** `internal/adapters/httpapi/authz_audit_test.go` (new)
- **Criteria:** A table mapping every entry in `httpapi.Routes()` to its required decision (`public`, `system:admin`, or `<resource>:<action>`), structured like `openapi_test.go:18`'s `TestOpenAPIMatchesRoutes` — fails if a route has no table entry, fails if a table entry matches no route. For each non-public entry, a sub-test drives an `rbacFixture` request with an account holding none of the required permission and asserts 403. Committed red: fails against the 21 routes that reach no authorization decision today.
- **Satisfies:** spec Approach C "The audit is enforced by a test, not a checklist"; AC3
- **Depends on:** 7

### 9. Gate the read-your-own-resource bucket
- **Files:** `internal/adapters/httpapi/execution_handlers.go`, `scenario_handlers.go`, `calibration_handlers.go`
- **Criteria:** The ~9 currently-ungated GET routes on executions/scenarios (e.g. `listExecutionFiles`, calibration reads keyed by execution/scenario) call `authorizeExecution`/`authorizeScenario(ctx, id, rbac.ActionRead)` (or `ActionList`). Table 8's entries for this bucket flip green.
- **Satisfies:** spec Approach C rule 2; AC5
- **Depends on:** 8

### 10. Gate the run/report bucket
- **Files:** `internal/adapters/httpapi/report_handlers.go` (`executionReports`, `executionTrend`, `executionErrorSignatureHistory`, `runReport`, `runShardLog`, `runShardConfig` — `router.go:169-174`)
- **Criteria:** Each of the 6 routes authorizes `rbac.ResourceReport`/`ActionRead` (execution-keyed routes) or resolves through the owning execution/project for run-keyed routes, against the row's tenant. Table 8's entries for this bucket flip green.
- **Satisfies:** spec Approach C rule 2 and "Route-specific resolutions"
- **Depends on:** 8

### 11. Gate the remaining bucket: aggregates, usage, files, SSE
- **Files:** `internal/adapters/httpapi/usage_handlers.go`, `files_handlers.go`, `stream_handlers.go`, and any cross-tenant list handlers rule C1 still leaves unscoped
- **Criteria:** `/api/usage/history` and `/api/usage/summary` (`router.go:176-177`) require `system:admin` (Alice 200, Bob/Carol/Dave 403) — no tenant dimension exists in `ports.LaunchRecord` to scope by. `GET /api/files/{kind}/{id}/{name}` (`router.go:214`) dispatches on the already-validated `kind` to the scenario or execution read check. `GET /api/executions/{id}/stream` (`router.go:163`) authorizes once at open, before upgrading to SSE. Remaining cross-tenant aggregate list routes scope down by `acct.TenantIDs()` per rule C1 (precedent: `listProjects`, `project_handlers.go:63`). Table 8 is fully green — all 75 routes accounted for.
- **Satisfies:** spec Approach C rules 1 and 4; AC4
- **Depends on:** 8

### 12. Full-suite green checkpoint: four-persona proof
- **Files:** `internal/adapters/httpapi/rbac_router_test.go` (or a new fixture test)
- **Criteria:** `make test`, `make integration`, `make cover-gate` all pass. One `rbacFixture` test exercises Alice/Bob/Carol/Dave against AC4, AC5, AC7, AC10 in one place (AC6 and AC9 wait for campaign CRUD in block D). **This task is the A→B→C gate — block D does not start until it is done.**
- **Satisfies:** plan Verification 1–2; spec "A → B → C must all be green before D starts"
- **Depends on:** 9, 10, 11

## Block D — Campaign CRUD completion

### 13. Add UpdateCampaign to the port, fake, MySQL adapter, and conformance suite
- **Files:** `internal/ports/campaign_repository.go`, `internal/ports/fake/campaign_repository.go` (or wherever `fake.Store`'s campaign methods live), `internal/adapters/repo/mysql/campaign_repository.go`, `internal/ports/repositorytest/campaign_contract.go`
- **Criteria:** `UpdateCampaign(ctx, campaign.Campaign) error` added to the port. MySQL implementation mirrors `CreateCampaign`'s transaction shape (`campaign_repository.go:22-50`): `BeginTx`, update the `campaign` row, delete existing `campaign_service` rows for it, re-insert the new set, `Commit`. `RunCampaignRepositoryContract` gains an update case; both fake and MySQL pass it (AGENTS.md: a real adapter passes the same suite as the fake).
- **Satisfies:** spec Approach D; a prerequisite for task 15
- **Depends on:** 12

### 14. Add ListCampaignsByTenants to the port, fake, MySQL adapter, and conformance suite
- **Files:** same set as task 13
- **Criteria:** `ListCampaignsByTenants(ctx, tenantIDs []int64) ([]campaign.Campaign, error)` added to the port, implemented in fake and MySQL, covered by the conformance suite.
- **Satisfies:** spec Approach D "New repo method ListCampaignsByTenants"
- **Depends on:** 12

### 15. Wire campaign update, abort, and cross-tenant list to HTTP
- **Files:** `internal/app/campaignapp/service.go`, `internal/adapters/httpapi/campaign_handlers.go`, `internal/adapters/httpapi/router.go`, `internal/adapters/httpapi/authz_audit_test.go`
- **Criteria:** `campaignapp.Service` gains `Update`, enforcing preparation-only editing: 409 once `Window.Start` has passed, 200 otherwise. New routes: `PUT /api/campaigns/{campaign_id}` gated `campaign:update`; `POST /api/campaigns/{campaign_id}/abort` exposing the existing `campaignapp.Abort` (`service.go:147`, today reachable only through the platform kill-switch), gated `campaign:delete`; `GET /api/campaigns` scoped by `acct.TenantIDs()` via task 14's `ListCampaignsByTenants`, mirroring `listProjects`. All three routes added to task 8's authorization table.
- **Satisfies:** spec Approach D; AC9
- **Depends on:** 13, 14

### 16. Tests: campaign CRUD acceptance criteria
- **Files:** `internal/app/campaignapp/service_test.go`, `internal/adapters/httpapi/rbac_router_test.go`
- **Criteria:** Unit test on `campaignapp.Update`'s 409-once-started boundary. HTTP test: Dave `PUT /api/campaigns/{id}` → 200 while the window is future, 409 once started (AC9); Dave `GET /api/campaigns/{id}/verdict` → 200 (AC6, re-confirmed now that CRUD exists end to end).
- **Satisfies:** AC6, AC9
- **Depends on:** 15

## Block E — Demo session

### 17. Demo session provider (HMAC-signed cookie)
- **Files:** `internal/adapters/auth/session/session.go` (new), `internal/adapters/auth/session/session_test.go`
- **Criteria:** New `ports.AuthProvider` mirroring the shape of `token.Provider` (`auth/token/token.go`) and `noauth.Provider`: `Authenticate` reads the `honryu_session` cookie, verifies its HMAC signature and expiry, and reconstructs the `account.Account` (subject, `Global`, `Tenants`) encoded in it. `Issue(profile) (cookieValue string, err error)` mints a signed cookie from a configured persona. Passes `authtest.RunAuthProviderContract` like the other three providers.
- **Satisfies:** spec Approach E "HMAC-signed cookie"; constraint "session must be an HttpOnly cookie, not a bearer token"
- **Depends on:** none

### 18. Session HTTP endpoints
- **Files:** `internal/adapters/httpapi/session_handlers.go` (new), `internal/adapters/httpapi/router.go`, `authz_audit_test.go`
- **Criteria:** `POST /api/session {profile}` → `Set-Cookie: honryu_session=…; HttpOnly; SameSite=Strict; Secure; Max-Age=8h` (public route — it's what authenticates). `DELETE /api/session` → expires the cookie. `GET /api/session/profiles` → the picker's list, `public` when `demo.enabled`, 404 when it's off. `GET /api/me` → subject, name, roles, tenants, and the permission map the SPA renders from — computed from the authenticated account, needs no new permission concept. All four routes entered in task 8's table.
- **Satisfies:** spec Approach E; AC13
- **Depends on:** 17

### 19. Config, startup wiring, and Helm values for demo mode
- **Files:** `internal/config/config.go`, `cmd/api/main.go`, `deploy/chart/honryu/values.yaml`, `deploy/chart/honryu/templates/api-deployment.yaml`, `deploy/chart/honryu-homelab-values.yaml`
- **Criteria:** `AuthConfig.Mode` gains `"demo"`; new `DemoConfig{Enabled bool, Profiles []Profile}`. `Validate` (`config.go:351`) rejects `demo.Enabled && Mode != "demo"` as a fatal startup error. `newAuthProvider` (`main.go:330`) gains a `"demo"` case constructing `session.Provider`; a loud `slog` line whenever demo mode is on (mirroring the existing `"auth configured"` log at `main.go:149`). Chart: `demo.enabled: false` default in `values.yaml`; `honryu-homelab-values.yaml` sets `demo.enabled: true` and `demo.profiles` (Alice/Bob/Carol/Dave, referencing the live tenant ids from task 3); session signing key follows the existing bring-your-own-secret convention (`values.yaml:87-94`) — named by key, created by the operator, not the chart.
- **Satisfies:** spec Approach E; AC12
- **Depends on:** 17, 18

### 20. Tests: demo session acceptance criteria
- **Files:** `internal/config/config_test.go`, `internal/adapters/httpapi/rbac_router_test.go`
- **Criteria:** Config test: `demo.enabled=true` with `mode=none` fails `Validate`. HTTP test: `POST /api/session` mints a cookie, a subsequent authenticated request succeeds, `DELETE /api/session` clears it, and the next `GET /api/me` is 401 (AC13).
- **Satisfies:** AC12, AC13
- **Depends on:** 19

## Block F — Picker UI

### 21. Session API client and useSession hook
- **Files:** `web/src/api/session.ts` (new), `web/src/hooks/useSession.tsx` (new), tests alongside each
- **Criteria:** `session.ts` wraps `GET /api/session/profiles`, `POST /api/session`, `DELETE /api/session`, `GET /api/me` using `apiClient` (`client.ts`). `useSession()` is a React context provider fetching `/api/me` once on mount, exposing `{loading, session, logout()}` — the single source every consumer in tasks 22–23 reads.
- **Satisfies:** spec Approach F "a single useSession() hook"
- **Depends on:** 18

### 22. Profile picker at `/`
- **Files:** `web/src/pages/ProfilePicker.tsx` (new), `web/src/App.tsx`
- **Criteria:** `/` renders the picker (profile cards from `useSession`'s profile list) when unauthenticated; redirects to `/reports` when a session already exists. Selecting a card calls `session.ts`'s select, then navigates to `/reports` — replacing `App.tsx`'s current unconditional `/` → `/reports` redirect.
- **Satisfies:** spec Approach F; AC13 ("the SPA shows the picker" after logout)
- **Depends on:** 21

### 23. Role-filtered nav, action gating, logout, demo banner
- **Files:** `web/src/components/DashboardLayout.tsx`, page components with Deploy/Trigger/Stop/Delete controls
- **Criteria:** `navItems` (`DashboardLayout.tsx:7-13`) becomes a function of `useSession()`'s permission map rather than a fixed array. Action buttons gate on the same map — Carol (`tenant_viewer`) renders no Deploy/Trigger/Stop/Delete control on any page (AC14). A logout control calls `DELETE /api/session` then navigates to `/`. A persistent banner renders whenever `useSession()` reports demo mode on.
- **Satisfies:** spec Approach F; AC14
- **Depends on:** 21

### 24. layout-check update and live verification
- **Files:** `web/scripts/layout-check.js`
- **Criteria:** `ROUTES` (`layout-check.js:33`) includes `/`. The nav-link-count assertion (`layout-check.js:214`, currently a single constant) becomes per-persona: select each demo profile, assert the visible nav-link count matches that persona's permission map. `bun run layout-check` passes across all 3 viewports (AC16). **Closing step, done by hand, not by a test:** all four personas verified live against `honryu.pve.heri.life`, `curl` and browser in agreement (AC17) — this repo has shipped scanner-green and broken before. Then `make phase-merge PHASE="phase 20 profiles rbac"`, running `gh pr ready` before merging develop→main with output shown, not suppressed.
- **Satisfies:** AC16, AC17; plan Verification 6–7
- **Depends on:** 22, 23
