# Phase 20 — Profile selection & real RBAC enforcement

Status: agreed, ready to plan
Date: 2026-09-03

## Problem

Honryu has a complete RBAC domain that nothing enforces and no one can see.

- `internal/domain/rbac` defines 5 roles and a tested `Authorize`; `role_grant`
  (migration 0014) has persisted grants since Phase 13. The live database holds
  **0 grants**, 1 tenant, and runs `HONRYU_AUTH_MODE=none`.
- With `EnableRBAC=false`, `authapp.Service.Authorize` returns
  `{Allowed: true, Reason: "rbac disabled"}`. Every authorization site in the
  codebase is currently a no-op.
- **21 of 75 routes reach no authorization decision at all** (measured
  transitively through helpers) — all GETs, including `/api/runs/{run_id}/report`,
  the SSE stream, `/api/files/{kind}/{id}/{name}`, and `/api/usage/*`.
- **Nothing ever sets an execution's or scenario's `TenantID`.** `projectapp`
  has `CreateInTenant` (`service.go:73`); the execution and scenario paths have
  no equivalent. The field exists in the domain and is persisted by the repo
  (`execution_repository.go:24`) but is written by no use-case. Live proof:
  executions 2 and 3 belong to project 2, which is in tenant 1, and both are
  `NULL`.
- **Three of the seven resource constants are dead.** `ResourceExecution`,
  `ResourceRun` and `ResourceScenario` are defined in the domain and referenced
  by **zero** handlers. Every execution, scenario and lifecycle route funnels
  through `authorizeExecution` → `authorizeProject` → `ResourceProject` +
  `ActionUpdate` (`ownership.go:48`). Only `Project`, `Campaign`, `System` and
  `Tenant` are ever actually asked about.
- The SPA has no concept of identity. `client.ts:30` reads `honryu_token` from
  localStorage and nothing sets it — a seam Phase 19 deliberately left dormant.

### The three live bugs this hides

Because `Authorize` currently allows everything, none of these are visible:

1. **`tenant_viewer` is not read-only on executions — it is locked out of
   them.** Viewer holds `project: [read, list]`, no update. Every *gated*
   execution route demands `project:update`. The only reason a viewer can see
   anything is that the 21 read routes are ungated. Gate them with the existing
   helper and the viewer drops to **zero** access — which would look exactly
   like "RBAC working".
2. **`campaign_manager` gets 403 on its own campaign's verdict.**
   `getCampaignVerdict` → `authorizeAnyParticipatingProject`
   (`campaign_handlers.go:353`) → `authorizeProject` → demands `project:update`.
   campaign_manager holds `project: [read, list]`. The PM can create the event
   and cannot read its result.
3. **`tenant_admin` cannot administer its tenant.** It holds
   `project`/`execution`/`scenario`/`run` and **no `ResourceTenant`**.
   `tenantAdminGate` requires `tenant:admin`, so all 13 routes behind it —
   including the reservation calendar the Reservations page depends on — admit
   only `service_provider_admin`.

The consequence: tenant-scoped roles are not weak, they are **inert**. Turning
RBAC on today gives every non-admin persona an empty application, and that
failure is indistinguishable from correct enforcement.

## Goal

Four personas, four different applications, verified live — and `curl` agrees
with the UI in every case:

- **Alice** (`service_provider_admin`) administers clusters and sees the fleet.
- **Bob** (`tenant_editor` @ tenant 1) runs load tests; cannot touch the
  cluster registry.
- **Carol** (`tenant_viewer` @ tenant 1) reads Bob's results; cannot start,
  stop, or delete anything.
- **Dave** (`campaign_manager` @ tenants 1 **and** 2) — the loadtest project
  manager: prepares and edits a loadtest event, coordinates schedules across
  both tenants, and pulls the rolled-up report for every participating tenant,
  **without** holding edit rights on anyone's projects.

## Non-goals

- **Not a login system.** No passwords, no credential store, no lockout.
  Selecting a persona *is* the authentication, by explicit decision — which is
  why it ships off by default behind `demo.enabled`.
- **Not an OIDC rollout.** The `oidc` provider stays as-is and untouched; this
  phase does not stand up an IdP.
- **No profile-management UI.** Personas are deployment fixtures in Helm
  values, not domain data. Adding one is a `helm upgrade`, not a screen.
- **No new *roles*.** The 5 in `DefaultCatalog()` stay. New **resources** are
  in scope — that is the actual gap (see Approach B).
- **No per-event stream authorization.** The SSE stream authorizes once at
  open, not per event.
- **No campaign feature work beyond CRUD.** Update and abort complete the
  lifecycle; verdict/comparison/freeze semantics are untouched.

## Constraints

- **`demo.enabled=true` must force the session provider.** Left on
  `AUTH_MODE=none`, noauth returns `service_provider_admin` for every request
  and the picker's cookie is ignored — a picker that appears to work and
  enforces nothing. Config validation must reject the combination at startup
  rather than trust the operator.
- **The session must be an `HttpOnly` cookie, not a bearer token.**
  `status.ts:47` uses `EventSource`, which cannot set an `Authorization`
  header — `client.ts:27` says so in a comment. Bearer-in-localStorage kills
  Live Status the moment the stream is gated.
- **The backfill writes live data.** Migration 0049 touches 6 executions and 1
  project in the running homelab. Never edit an applied migration — this is a
  new number.
- **`UpdateCampaign` pays the hexagonal tax.** A new port method means the
  fake, the MySQL adapter, and the shared conformance suite all change
  together (AGENTS.md: a real adapter must pass the *same* suite as the fake).
- **Coverage gate ≥90%**, and the repo has sat at ~88.9% since before Phase 9.
  This phase adds a lot of branching authorization code; it must carry its own
  tests rather than widen a known gap.
- **Close out with `make phase-merge PHASE="phase 20 profiles rbac"`**, and add
  the new `/` picker route to `bun run layout-check`'s route list.

## Approach

Six blocks, in a hard order. **A → B → C must all be green before D starts** —
the enforcement has to be correct before anything renders it.

### A. Tenant propagation

`executionapp` and `scenarioapp` resolve their project's tenant on create and
stamp it. Migration `0049` backfills existing rows from their project, and
adopts the remaining NULL rows into the default tenant.

The backfill takes each row's tenant from its project. Rows whose project is
itself NULL are adopted into a `default` tenant the migration creates when
absent — deterministic on a fresh database as well as on the live one.

Invariant afterwards: **an execution's tenant always equals its project's
tenant**, and `SELECT COUNT(*) ... WHERE tenant_id IS NULL` is 0 for project,
scenario and execution.

**Tenant fixtures are a prerequisite, not a detail.** The live database holds
exactly **one** tenant. Dave is defined as `campaign_manager` in tenants 1 *and*
2, and acceptance criterion 8 requires a third he holds nothing in. Two more
tenants must exist before any persona demonstrates anything.

### B. Resource-model surgery

The vocabulary cannot currently express "may coordinate schedules, may not edit
projects". Two new resources close that, and reservations fold into schedules
rather than earning a third (a reservation *is* scheduled capacity,
materialized):

```
+ ResourceSchedule    schedules AND reservations
+ ResourceReport      reports, trend, error-signatures, shard log/config

  tenant_admin      += tenant:admin              (scoped to its own tenant)
  tenant_editor     += schedule:write, report:read+list
  tenant_viewer     += schedule:read+list, report:read+list
  campaign_manager  += schedule:read+list       sees every tenant's plan
                    += report:read+list          "report for participants"
```

**`campaign_manager` gets no write access anywhere except campaigns.** The
coordination itself happens in a meeting, outside Honryu — the PM needs to
*see* every participating tenant's schedule to run that meeting, not to change
it. A PM who also owns a tenant gets write there by **composition**: a separate
`tenant_editor` grant in their own tenant, which `Authorize` already unions
per-tenant. This keeps the oversight role incapable of editing other teams'
work by construction rather than by convention.

Three gates are re-pointed:

- **A read path that does not demand write.** Read routes ask
  `<resource>:read`, not `ResourceProject:update`. This is what makes
  `ResourceExecution`/`Run`/`Scenario` mean something for the first time.
- `authorizeScheduleTenant`: `project:update` → `schedule:create`.
- `authorizeAnyParticipatingProject` (verdict, comparison): accept
  `campaign:read` on the campaign's own tenant, falling back to the existing
  participating-project check for roles that reach it that way.

### C. Full authorization audit — all 75 routes

Four rules, each with a precedent already in the tree:

1. **List endpoints scope down** by `acct.TenantIDs()` — precedent:
   `listProjects` (`project_handlers.go:63`).
2. **Read endpoints authorize** `<resource>:read` against the row's tenant.
3. **Mutations authorize** `<resource>:<action>` against the row's tenant.
4. **Platform-wide surfaces** (`/api/admin/*`, `/api/clusters/*`,
   **`/api/usage/*`**) require `system:admin` — precedent:
   `authorizeKillSwitch`, `clusterAdminGate`.

   `/api/usage/*` lands here rather than under rule 1 because
   `ports.LaunchRecord` has **no tenant dimension** — it rolls up by owner and
   context only. VUH accounting is billing, a service-provider concern, so
   Alice sees the fleet and every tenant role gets 403. Scoping it per tenant
   would mean adding `TenantID` to `LaunchRecord` and joining
   `execution_launch_history` through `execution` — a port change, deferred
   until tenants are actually billed.

Route-specific resolutions: `/api/files/{kind}/{id}/{name}` dispatches on the
already-validated `kind` to the scenario or execution read check; the SSE
stream authorizes once at open; `/api/admin/nodes` is infrastructure and stays
`system:admin`.

**The audit is enforced by a test, not a checklist.** A table maps every entry
in `routes` to its authorization decision, with explicit `public` markers for
`/healthz` and `/api/ingest` (which carries its own credential). A new route
added without an entry fails the build.

### D. Campaign CRUD completion

Create/read/list exist. Update and abort do not reach HTTP at all:

- `PUT /api/campaigns/{campaign_id}` — edit window and participating services.
  New `ports.CampaignRepository.UpdateCampaign` + fake + MySQL + conformance
  suite. Gated `campaign:update`.
- `POST /api/campaigns/{campaign_id}/abort` — exposes the existing
  `campaignapp.Abort`, which today is reachable only through the
  platform-admin kill-switch. Gated `campaign:delete`.
- `GET /api/campaigns` — the PM's cross-tenant view, scoped down by
  `acct.TenantIDs()` under rule C1, mirroring `listProjects`. Campaigns are
  otherwise listed only per tenant, leaving Dave with no single view of the
  event he is running. New repo method `ListCampaignsByTenants`.

**Editing is preparation-only:** a campaign may be updated while
`Window.Start` is in the future; once it has started, the window and service
set are frozen (409). Editing a live campaign would change what freeze applies
to mid-flight.

### E. Demo session

- `demo.enabled` (default **false** in `chart/honryu/values.yaml`, **true** in
  `honryu-homelab-values.yaml`) and `demo.profiles` in Helm values. The chart
  default stays off so no other deployment can inherit a credential-free front
  door; the homelab turns it on deliberately.
- New `internal/adapters/auth/session` provider verifying an HMAC-signed
  cookie; `AUTH_MODE=demo` selects it.
- `POST /api/session {profile}` → `Set-Cookie: honryu_session=…; HttpOnly;
  SameSite=Strict; Secure; Max-Age=8h`.
- `DELETE /api/session` → expires the cookie.
- `GET /api/session/profiles` → the picker's list; 404 when demo is off.
- `GET /api/me` → subject, name, roles, tenants, and the computed permission
  map the SPA shapes its UI from.
- Startup validation: `demo.enabled && auth.mode != "demo"` is a fatal config
  error, plus a loud log line whenever demo mode is on.

### F. Picker UI

- `/` renders the profile picker when unauthenticated, redirects to `/reports`
  when authenticated.
- `DashboardLayout` nav and every action button read one `useSession()` hook
  fed by `/api/me` — a single source, so the UI cannot drift from the server's
  answer.
- Logout → `DELETE /api/session` → back to `/`.
- A persistent banner whenever demo mode is on.

### Alternatives rejected

- **Do nothing.** Defensible on its face — the homelab has one operator. But
  the three bugs above are already in the tree and will be inherited by
  whatever real auth arrives later. The catalog claims a model the HTTP layer
  never implemented; every phase that passes widens the gap.
- **UI shaping only** (picker + role-aware nav, no server enforcement).
  Smallest phase, but the roles become a costume: a viewer who opens devtools
  has admin power.
- **Bearer token in localStorage.** Rejected on evidence — `EventSource`
  cannot send it, so Live Status breaks.
- **A new global `program_manager` role** spanning all tenants. Rejected:
  `Authorize`'s global branch ignores `TenantID` entirely, so it would be
  permanent blanket cross-tenant read, silently covering tenants created years
  later. Per-tenant `campaign_manager` grants keep the reach explicit and
  auditable.
- **Separate `ResourceReservation`.** Folded into `ResourceSchedule`; a
  reservation is scheduled capacity, materialized.

## Acceptance criteria

Enforcement:

1. `SELECT COUNT(*) FROM execution WHERE tenant_id IS NULL` returns 0 on the
   live database after migration; likewise `scenario` and `project`.
2. An execution created under a project in tenant N has `tenant_id = N`.
3. The route-table test fails when a route is added without an authorization
   entry.
4. `GET /api/clusters`: Alice 200, Bob 403, Carol 403, Dave 403.
5. Carol `GET /api/executions/1` → 200; `DELETE /api/executions/1` → 403.
   *(Today the GET is ungated and the DELETE would 403 her too — this is the
   viewer bug, fixed.)*
6. Dave `GET /api/campaigns/{id}/verdict` → 200. *(Today: 403.)*
7. `tenant_admin` @ 1 `GET /api/tenants/1/quota` → 200. *(Today: 403.)*
8. Dave `GET /api/tenants/{1,2}/reservations` → 200; tenant 3 → 403.
9. Dave `PUT /api/campaigns/{id}` → 200 while the window is future, 409 once
   it has started.
10. The role in three lines — Dave, in a tenant he oversees but does not own:
    `GET /api/executions/{id}/schedules` → 200 (sees the plan),
    `POST /api/executions/{id}/schedules` → 403 (cannot change it),
    `PUT /api/executions/{id}/config` → 403 (cannot edit).
    Granting Dave `tenant_editor` in his *own* tenant flips those to 200 there
    and leaves them 403 elsewhere.

Session and UI:

11. Live Status still streams after RBAC is on — the SSE cookie reaches
    `EventSource`.
12. `demo.enabled=true` with `auth.mode=none` → the API refuses to start.
13. Logout clears the cookie; the next `/api/me` is 401 and the SPA shows the
    picker.
14. Carol's UI renders no Deploy/Trigger/Stop/Delete control on any page.

Gates:

15. `make cover-gate` passes at ≥90%.
16. `bun run layout-check` passes with `/` added, across all 3 viewports.
17. Verified live against `honryu.pve.heri.life`, not only in tests — all four
    personas, by hand, with `curl` and the browser.

## Decided during brainstorm

- **Role name stays `campaign_manager`**, for consistency with the domain,
  which calls the event a campaign. "Loadtest project manager" is the human
  term; the role name is the domain term.
- **The PM gets no schedule write.** Coordination is a meeting, not an API
  call. Write arrives only by composition with a tenant role of their own.
- **`GET /api/campaigns` is in scope**, scoped down by tenant — Dave needs one
  view, not one request per tenant.
- **`/api/usage/*` is `system:admin` only.** No tenant dimension exists to
  scope by, and VUH is billing.
- **Demo mode: off in the chart, on in the homelab values.** Accepted and
  documented: anyone who reaches `honryu.pve.heri.life` can select Alice. The
  ingress carries no auth annotation and no source-range restriction, so the
  only thing limiting reach is DNS and network position.

## Open questions

None blocking. Two details to settle while planning:

1. **Session signing key.** The HMAC key backing the cookie needs a home —
   most likely a Secret alongside `honryu-cluster-credential-key`, generated on
   install. A restart must not silently invalidate every session, and two API
   replicas must agree on it.
2. **Usage response shape.** Gating `/api/usage/*` to `system:admin` means the
   SPA must hide the surface entirely for three of the four personas rather
   than render an empty page.
