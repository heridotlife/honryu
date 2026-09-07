# Phase 20 — Plan

Spec: `.cortex/2026-09-03-phase20-profiles-rbac/spec.md`

## Context

- `internal/domain/rbac/rbac.go` is the whole catalog: 7 resources (`ResourceProject`,
  `ResourceExecution`, `ResourceScenario`, `ResourceRun`, `ResourceTenant`,
  `ResourceSystem`, `ResourceCampaign`), 5 roles via `DefaultCatalog()`
  (rbac.go:139-201), and `Authorize` (rbac.go:113-131) — pure, already tested.
  `RoleTenantAdmin` (rbac.go:148) holds no `ResourceTenant` permission.
  `RoleCampaignManager` (rbac.go:189) holds `Campaign: all, Project: read,
  Execution: read` and nothing else.
- Every authorization decision in the HTTP layer funnels through
  `ownership.go`: `authorize` (line 27), `authorizeProject` (line 43 — always
  `ResourceProject`+`ActionUpdate`, never `read`). `execution_handlers.go:269`'s
  `authorizeExecution` and every scenario/run equivalent collapse to that one
  call — `ResourceExecution`/`ResourceRun`/`ResourceScenario` are declared but
  asked about nowhere.
- `schedule_handlers.go:172` `authorizeScheduleTenant` demands
  `project:update`. `campaign_handlers.go:351`
  `authorizeAnyParticipatingProject` (used by `getCampaignVerdict:247` and
  `getCampaignComparison:315`) does the same per participating project.
  `tenant_handlers.go:35` `tenantAdminGate` demands `tenant:admin` — 13 routes,
  admitted today only to `service_provider_admin`.
- `router.go` declares 75 routes. None stamp an execution's or scenario's
  tenant on create: `executionapp.Service.Create` (service.go:71-87) and
  `scenarioapp.Service.Create` (service.go:74-85) take a `projectID` and never
  touch `TenantID`. `projectapp.CreateInTenant` (projectapp/service.go:60-80)
  is the pattern to mirror. Both `fake.Store.GetProject`
  (`ports/fake/repository.go:223`) and `mysql.Repository.GetProject`
  (`repo/mysql/project_repository.go:33`) already exist, so widening
  `executionapp.Repo`/`scenarioapp.Repo` to include `GetProject` requires no
  new implementation.
- `campaign_repository.go` (ports) has `CreateCampaign`, `GetCampaign`,
  `ListCampaignsByTenant`, `ListActiveCampaigns`, `AbortCampaign` — no
  `UpdateCampaign`, no `ListCampaignsByTenants`. `mysql.Repository
  .CreateCampaign` (campaign_repository.go:22-50) is the
  `BeginTx`/insert-campaign/loop-insert-`campaign_service`/`Commit` pattern
  `UpdateCampaign` must mirror. `campaignapp.Service.Abort` (service.go:147)
  exists but is reachable only through the platform kill-switch, not HTTP.
- No identity concept in the SPA: `client.ts:33` reads `honryu_token` from
  `localStorage`, nothing ever sets it. `status.ts:47`'s `EventSource` cannot
  carry an `Authorization` header, forcing a cookie. `DashboardLayout.tsx:7-13`
  hardcodes 5 nav links; `layout-check.js:33,214` pins that count.
- `internal/adapters/auth/` has three providers today: `noauth`
  (fixed-account), `token` (bearer-token map, used by test fixtures), `oidc`.
  All implement `ports.AuthProvider{Authenticate(*http.Request)
  (account.Account, error)}` and pass `authtest.RunAuthProviderContract`.
  `config.AuthConfig.Mode` (config.go:65) is `none|oidc`, validated at
  config.go:351. `cmd/api/main.go:330` `newAuthProvider` switches on it.
- Tenants and role grants are already administrable over HTTP:
  `POST /api/tenants`, `POST /api/tenants/{id}/roles`, `POST /api/roles`
  (router.go:183,190,192) — creating fixture tenants and persona grants is an
  operational step, not new code.
- The chart is bring-your-own-secret throughout: `values.yaml:87-94` names
  secrets by key (`mysql-credentials`, `honryu-ingest`,
  `honryu-cluster-credential-key`) and creates none of them;
  `api-deployment.yaml:64-70` wires them to env vars. The session signing key
  follows the same convention.

## Approach

Six blocks, hard-ordered: **A → B → C gate D**. Enforcement must be correct
before anything (campaign CRUD, the picker) renders or depends on it.

- **A — Tenant propagation.** Stamp tenant on create (mirroring
  `CreateInTenant`), then migration 0049 backfills live rows, then provision
  the tenant/persona fixtures the later personas need. Ordered first because
  B and C's tests need real tenant IDs to assert against.
- **B — Resource-model surgery.** Add the two missing resources, repair the
  three roles per the spec's table, add a read-flavoured authorization path,
  and re-point the two handlers already known to be wrong
  (`authorizeScheduleTenant`, `authorizeAnyParticipatingProject`). Ends with a
  regression task that proves the three documented bugs are fixed — the
  checkpoint that the seam works before the 75-route sweep begins.
- **C — Full audit.** One table test drives it (mirroring
  `openapi_test.go`'s `TestOpenAPIMatchesRoutes`): every route gets an
  explicit entry, `public`/`system:admin`/`<resource>:<action>`. Write the
  table red (it fails against all 75 routes today), then gate bucket by
  bucket until green. A new route without an entry fails the build from here
  on.
- **D — Campaign CRUD.** Only starts once C is green. `UpdateCampaign` pays
  the full hexagonal tax (port → fake → MySQL → conformance suite) because
  AGENTS.md requires a real adapter to pass the same suite as the fake.
- **E — Demo session.** A fourth `ports.AuthProvider` (HMAC cookie), gated
  behind `demo.enabled`/`AUTH_MODE=demo`, with the config validation that
  makes the dangerous combination (`demo.enabled` + `mode=none`) a startup
  error rather than a silent no-op.
- **F — Picker UI.** One `useSession()` hook, fed by `/api/me`, is the single
  source the nav and every action button read — chosen over per-page role
  checks so the UI cannot drift from what the server just decided.

Rejected: shipping B/C without A (the audit would gate on a `TenantID` field
that's `NULL` everywhere, making every tenant-scoped grant fail closed
regardless of role — indistinguishable from the bug this phase fixes); doing
E/F before C (a picker over unenforced RBAC is exactly the "roles become a
costume" alternative the spec rejects).

## Risks

| Risk | Mitigation |
|---|---|
| Migration 0049 writes live data (6 executions, 1 project today) | Idempotent, derives tenant from project, creates a `default` tenant only if none exists; counts checked before/after by hand. A migration, not a task the CI pipeline reruns destructively. |
| Flipping 21 routes from open to gated in one phase regresses something CI won't catch (`EventSource`, file downloads) | C is decomposed into small buckets, each with its own test, gated behind the B4 regression checkpoint; AC11 (SSE) and AC14 (UI controls) are explicit acceptance criteria, not incidental. |
| Coverage sits at ~88.9% against a ≥90% gate (pre-existing, not this phase's doing — see `[[coverage-gate-preexisting-gap]]`) | Every task in A–E carries its own unit/integration tests; `rbacFixture` (`rbac_router_test.go:47`) makes an HTTP-level RBAC test ~5 lines, so the audit doesn't have to trade coverage for speed. |
| Session signing key: two API replicas must agree, and a restart must not silently log everyone out | Key lives in an operator-created Secret (mirroring `honryu-cluster-credential-key`), mounted by both replicas — not generated in-process. |
| `campaign_service` rows are multi-row per campaign; a naive `UPDATE` orphans stale rows | `UpdateCampaign` mirrors `CreateCampaign`'s transaction: delete existing `campaign_service` rows for the campaign, re-insert, commit — same shape already proven by the conformance suite. |
| `layout-check` hardcodes both the route list and a single nav-link count (`layout-check.js:33,214`) | Becomes a per-persona assertion in block F rather than a single constant — task F4 owns this explicitly so it isn't discovered as a surprise CI failure. |
| `/api/usage/*` has no tenant dimension (`ports.LaunchRecord`, no `TenantID`) to scope by | Per spec: gated `system:admin` outright rather than half-scoped; SPA hides the surface for the other three personas (open question 2, resolved by F3). |

## Out of scope

- No OIDC changes; no login/password/lockout; no profile-management screen —
  personas are Helm values, not domain data (spec Non-goals).
- No per-event SSE re-authorization — one check at stream open.
- No campaign feature work beyond CRUD (verdict/comparison/freeze semantics
  untouched).
- No `/api/usage/*` tenant scoping — deferred until tenants are billed
  (would need a `LaunchRecord.TenantID` port change).
- No new roles. The resource vocabulary grows by two; the role list of five
  does not change.

## Verification

1. `make test` after every task; `make integration` once a task touches a
   port (D, and A's migration).
2. `make cover-gate` after C and after D — the two blocks with the largest
   branching-code surface.
3. `bun run layout-check` after F, all 3 viewports.
4. The 10 enforcement acceptance criteria (spec AC1–10) as Go tests using
   `rbacFixture`, run at the end of C (the four-persona matrix) and again
   after D (AC6, AC9 depend on campaign CRUD).
5. The 7 session/UI criteria (AC11–17) exercised through `make e2e` where a
   harness reaches (11–14), by hand otherwise (16–17).
6. **Live, by hand, last**: all four personas against
   `honryu.pve.heri.life`, `curl` and browser in agreement — AC17. This repo
   has shipped scanner-green and broken before (`[[honryu-live-verification-pays-off]]`);
   this phase changes what every route in the app is allowed to do, so this
   step is not optional.
7. `make phase-merge PHASE="phase 20 profiles rbac"` to close out, per
   `[[honryu-promote-pr-is-draft]]` — `gh pr ready` before merging
   develop→main, output not suppressed.
