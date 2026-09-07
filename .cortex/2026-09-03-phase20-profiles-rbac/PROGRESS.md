# Phase 20 Progress

## Block C (tasks 8-12) — 2026-09-03, complete

- **Task 8** (dce8e61): `authz_audit_test.go` — 76-entry decision table
  (public / system:admin / scoped-list / res:action), both-direction
  coverage vs `Routes()` (AC3 live), zero-grant 403 probe per entry.
  Committed red: the 20 ungated routes verified failing (200/404/409 to an
  ungranted caller), carried as named skips until their gating task.
  Fixture gained Lifecycle (was never wired — status/engines/podlog
  panicked), Reports, Usage, Events, Reservations.
- **Task 9** (16ccfaf): gated getProject/getScenario/listScenarioFiles/
  listExecutionFiles/getExecutionConfig/executionStatus/executionEngines/
  scenarioPodLog. NOTE: getProject + the three lifecycle-handler GETs are
  outside tasks.md's literal file list but inside its criteria (the "~9"
  ungated execution/scenario reads) — the audit table is the authority.
- **Task 10** (728496b): 6 report routes → ResourceReport/read; run-keyed
  routes resolve via new `lifecycleapp.Service.RunExecutionID` (RunHistory).
  Legacy (no-RBAC) stays open per the campaign/schedule-gate precedent —
  report-only routers wire no execution service.
- **Task 11** (3721027): usage/admin → shared `authorizeSystemAdmin`
  (kill-switch delegates); files download dispatches on kind; SSE
  authorizes once at open; reservations calendar tenant:admin →
  schedule:list (AC8). Audit table fully enforced — zero pending.
- **Task 12** (0508ca3): `TestRBAC_FourPersonas_AuditMatrix` (AC4/5/7/8/10
  incl. the composition flip). make test / integration / cover-gate green
  (92.2% ≥ 90%), lint clean.

### Findings for later blocks / closeout

1. **Pre-existing e2e breakage, fix-forwarded** (traced to 27ab4b5, Block
   A): executions now inherit their project's tenant, so trigger consults
   quota; `TestPhase8_MultiCluster/CampaignSpansClusters…` had no ceiling
   for tenant 9 → 500. Fixed test-side (SetCeiling on both clusters,
   mirroring its sibling) per 0028's fail-closed contract.
2. **ErrOverQuota maps to HTTP 500** (quotaapp.ErrOverQuota is not in
   httpapi's error map). Pre-existing, now user-visible on a common path
   once executions carry tenants. Needs a 4xx mapping decision (409?) —
   suggest handling in block D/E or closeout.
3. **Live-DB operational note for AC17:** every homelab tenant whose
   executions get stamped by 0050-0052 needs a `tenant_quota` ceiling per
   cluster or trigger will fail-closed after deploy. Verify before the
   live persona pass.

## Block D — Campaign CRUD completion (tasks 13–16, 2026-09-04)

- **Task 13** (a7fe6dc): `UpdateCampaign` on the port + fake + MySQL
  (CreateCampaign's tx shape: UPDATE row, DELETE+re-INSERT campaign_service,
  RowsAffected/no-op ambiguity resolved like AbortCampaign) + conformance
  cases (stale-service drop, identity/AbortedAt preservation, idempotent
  update, missing→NotFound). MySQL contract green under `-tags=integration`.
- **Task 14** (842ce7b): `ListCampaignsByTenants` (empty slice → empty list,
  IN-clause mirrors ListProjectsByTenants) + conformance (union ordered by
  window start, unqueried tenant excluded, nil → 0). MySQL contract green.
- **Task 15** (d59385a): `campaignapp.Service.Update` (preparation-only:
  `ErrCampaignStarted` once the *stored* window opened, added to
  conflictErrors → 409; identity/AbortedAt from the stored row;
  `verifyServices` extracted and re-run by Update); `ListByTenants`;
  routes `PUT /api/campaigns/{id}` (campaign:update), `POST
  /api/campaigns/{id}/abort` (campaign:delete, exposes Abort without the
  kill-switch, audited), `GET /api/campaigns` (scoped-list by
  acct.TenantIDs()); audit-table entries + "Supersale" added to the
  scoped-list leak probe; openapi.yaml entries (TestOpenAPIMatchesRoutes).
- **Task 16** (4179333): `campaignapp.Update` unit tests (AC9 boundary:
  start−1ns allowed, start/mid-window/after-end → 409, nothing mutated;
  smuggled-service edit rejected via Create's checks) + Dave HTTP test
  (PUT 200 future / 409 live; editor 403; AC6 verdict on the edited
  campaign; abort own 200 + editor 403; cross-tenant list shows acme+globex,
  never initech; nobody → empty).

Block D bar: make test green, make lint 0 issues, gofmt/vet clean, MySQL
campaign contract green under integration. cover-gate not re-run this block
(cover-gate after D is plan Verification 2 — run at block E/checkpoint;
last measured 92.2% at task 12).

### Notes

- openapi.yaml was already not prettier-clean before Block D (34 yamllint
  warnings pre-existing, unchanged after); new entries match surrounding
  style rather than reformatting 68 unrelated lines.

## Block E — Demo session (tasks 17-20, 2026-09-04)

Commits (one per task):
- 17 `91b770d` feat(phase20): demo session provider (HMAC-signed cookie)
- 18 `5df122b` feat(phase20): session endpoints -- POST/DELETE /api/session, profiles, /api/me
- 19 `002cdaa` feat(phase20): demo auth mode -- config, startup wiring, Helm values
- 20 `30fa02c` feat(phase20): demo session acceptance tests -- AC12, AC13

Bar: make test green (55 pkgs) after every task; make lint 0 issues;
gofmt/vet clean; make cover-gate PASS 91.8% >= 90% (first 600s attempt
timed out mid-run -- incomplete, not failed; rerun with a longer window
completed and passed).

### Decisions worth remembering

- Cookie signs the base64url payload (JWT-segment style): replicas share
  only the HMAC key; no session store; restart invalidates nothing.
- Logout is client-side expiry (Max-Age=0): a stateless HMAC cookie stays
  cryptographically valid until its TTL -- the SPA/browser drops it, which
  is what AC13 asserts (cookieless /api/me -> 401).
- Audit table gained a decision vocabulary member: "authenticated" (any
  authenticated account may call; /api/me). Session mint/list/expiry are
  public (selecting a persona IS the authentication); the auth middleware
  exempts them via publicAPIPath(), which now also covers /api/ingest.
- Validation beyond AC12 (in-spec spirit, "reject rather than trust"):
  mode=demo also requires ENABLE_RBAC, DEMO_SIGNING_KEY, >=1 profile; demo
  profile role names are validated against rbac.DefaultCatalog() so a
  Helm-values typo fails the pod, not the persona's first request.
- Chart: demo env gated on .Values.demo.enabled in api-deployment.yaml --
  secretKeyRef with a missing Secret blocks pod startup, so default
  installs must not reference honryu-session-key. HONRYU_ENABLE_RBAC moved
  into the shared ConfigMap (config.enableRbac, default false; homelab
  true). Signing key follows bring-your-own-secret (secrets.demoSessionKey,
  key signing-key). Homelab personas: alice/bob/carol/dave per spec; dave
  holds campaign_manager in tenants 1+2, nothing in 3.
- openapi.yaml gained the 4 session routes (TestOpenAPIMatchesRoutes
  forces it) with a session tag; still not prettier-clean (pre-existing,
  see Block D note), new entries match surrounding style.
- httpapi (non-test) now imports adapters/auth/session for the Profile
  type behind the consumer-side SessionService interface -- same
  composition main.go already does; hexagonal rules (domain purity,
  app->ports) untouched.

AC status for this block: AC12 pinned (config tests), AC13 pinned
(demo-router HTTP lifecycle test), cookie attributes HttpOnly /
SameSite=Strict / Secure / Max-Age=8h asserted in the audit probes and the
lifecycle test. Not done here (Block F's): AC11 live SSE, AC14 UI, AC16
layout-check, AC17 live verification.

## Block F (tasks 21-24) — picker UI, 2026-09-04

Retry of a crashed run; the crashed run's untracked partials were salvaged
(see below), nothing was blindly recreated. One commit per task:

- 21 b478d27 session.ts + useSession.tsx (fetch /api/me once, 401 is the
  unauthenticated state not an error; can() mirrors Permission.Allows).
- 22 e894b6e ProfilePicker at / (App's unconditional redirect replaced;
  SessionProvider wraps the routed app).
- 23 be7cb6e navItemsFor(can) replaces the fixed nav array (mirrors the
  audit table's resource:action), gateControls on the hub's lifecycle
  buttons, create-gates on NewTest/Campaigns/+New-test, demo banner inside
  <main> (so the nav-height/main-offset invariants hold), logout in the
  banner. Carol: zero lifecycle controls anywhere (AC14).
- 24 a8a0a70 layout-check: / added to ROUTES, per-persona nav/drawer
  assertions (select each profile, compare rendered hrefs to the
  permission map), unauthenticated drawer asserted empty, honest skip when
  a target offers no demo profiles.

Salvage notes from the crashed run: session.ts, session.test.ts,
useSession.tsx, useSession.test.tsx were kept essentially verbatim after
verifying wire shapes against session_handlers.go and wildcard semantics
against rbac.Permission.Allows -- the crash left one test unfinished
(refresh-flips-to-authed never POSTed the persona select; fixed to call
createSession first), one unused import, and one tsc narrowing error.

Verified locally beyond vitest: full demo stack (AUTH_MODE=demo,
ENABLE_RBAC=true, fake DB, the homelab's four personas) served by cmd/api
with the embedded dist; layout-check passed exit-0 across all 3 viewports
incl. / and all four persona nav/drawer maps; curl: select 204 -> me shows
Carol's map -> logout 204 -> me 401 (AC13). Clusters answers 404 locally
because the fake deployment has no cluster registry configured
(clusterAdminGate's unconfigured check fires before RBAC) -- AC4's 403
lives in the Go audit tests and the live run.

Not done here (needs the deployment, by design): AC17 live verification
against honryu.pve.heri.life -- the live site still runs phase19b (404 on
/api/session/profiles); run layout-check + curl personas after deploy,
then make phase-merge.

## Skip closure ledger (2026-09-04, post-deploy)

- AC4 live clusters-403: CLOSED. Live proof post-phase20 deploy: carol
  GET /api/clusters -> 403, alice -> 200 (server-enforced via curl).
- AC17 live layout-check: CLOSED. LAYOUT_CHECK_URL=https://honryu.pve.heri.life
  bun run layout-check -> 24/24 ok, all four persona nav maps pass.
- quotaapp.ErrOverQuota 500-vs-429 (reported not fixed in batch 2): FIXED
  in 9d174fc -- central respondError mapping to 429 + OpenAPI 429 response
  + TestTenantQuota_TriggerOverQuotaIs429Not500 (red 500 before, green 429
  after). make test 55 pkgs ok.
