# Phase 12 — BYOC (bring-your-own-cluster)

Agreed via brainstorm 2026-08-16, refined at write-plan the same day (Phase 11
landed first). Turns the seam Phases 5–8 designed in — cluster registry,
per-cluster credentials, push-based data plane — into the customer-facing
capability the parent spec always pointed at
(`.cortex/2026-07-30-honryu/spec.md`: "BYOC … is later. It is designed for as
a **seam**, not built now").

**Refined at write-plan after re-reading the code:** far more of this exists
than the draft assumed. Phase 8 already shipped BYOC registration end to end
(`clusterapp.RegisterBYOC`: parse → probe → encrypt (secretbox) → materialize
Secret → store, with rollback; exposed via `POST /api/clusters` with a
`kubeconfig` form field) and the per-cluster data plane
(`scheduler/k8s.ClientFactory.Resolve` builds each registered cluster's client
and overrides `SidecarImage`/`IngestURL` from the registry entry,
`factory.go:105`). The engine-pod ingest secret is already `Optional`
(`k8s.go:355-363`), so a customer cluster without our `honryu-ingest` Secret
deploys fine.

**The one genuine gap is ingest authentication**, which is still a single
global `HONRYU_INGEST_TOKEN` compared in one constant-time check
(`ingest_handlers.go:47-63`): a customer's engine fleet would hold the same
secret as every other customer's fleet, and nothing ties a pushed batch to the
cluster it claims to speak for.

## Problem

One shared ingest token authenticates every engine pod in the world, and
ingest cannot tell which cluster a batch came from — so a hosted control
plane cannot accept customer clusters: any customer's fleet (or anyone who
extracted the shared token from one) could push measurements into any
execution on the plane.

## Goal

Ingest authenticates per cluster: registration (BYOC) mints a cluster-scoped
token returned once to the customer; a batch is accepted with that token only
for executions routed to that cluster. The operator's global token keeps
working unchanged for operator-owned deployments. With that, a BYOC cluster
registered through the existing API runs executions end to end — scheduled
there, pushing back here — with nothing a customer's token can touch beyond
its own cluster's executions.

## Non-goals

- **No billing/metering** (usage attribution already exists).
- **No control-plane multi-region.** One plane; clusters dial in.
- **No customer-self-service roles.** Registration stays behind the same gate
  as today (platform-admin under RBAC; open in no-auth homelab mode).
  Customer-tenant-scoped registration is SaaS-auth work, not data-plane work.
- **No mTLS for v1.** Bearer-per-cluster with high-entropy tokens and hashed
  at rest is the bar; revisit with real customers.
- **No tunneling/mesh.** The customer cluster must reach the plane's ingest
  URL, and the plane must reach the customer's API server (documented
  prerequisites — the existing probe enforces the latter at registration).
- **No new sidecar token plumbing.** The customer creates their cluster's
  `honryu-ingest` Secret (key `token`) with the minted token — the same
  Optional-secret env path pods already use; the plane stores only the hash.

## Constraints

- **Tokens: mint once, store hashed.** 32 bytes `crypto/rand`, base64url,
  returned exactly once at mint/rotate; at rest only `SHA-256(token)` (64 hex
  chars) on the registry row. High entropy makes plain SHA-256 sufficient
  (lookup, not slow hashing — this is a bearer credential, not a password).
- **Batch→cluster binding is the security property.** `batch.ExecutionID` is
  attacker-controlled at this boundary: a per-cluster token authorizes
  *exactly* the executions whose `execution.cluster` names that cluster.
  Default-cluster (empty `cluster`) executions are operator territory: only
  the global token may push to them.
- **Global token = operator, unchanged.** Constant-time compare first; when
  it matches, behavior is byte-for-byte today's (any execution) — the
  operator already holds the plane's own credential.
- **Unknown token = 401; right token, wrong cluster = 403.** Distinct
  statuses so a customer misconfiguration is diagnosable from the sidecar log.
- **No `metricsapp` change.** Scoping is an adapter concern (authenticate →
  resolve → authorize the batch), decided before `Metrics.Ingest` is called.

## Approach

1. **Registry**: `Cluster` gains `IngestTokenHash string` (empty = no token);
   port gains `SetClusterIngestTokenHash(ctx, name, hash)` and
   `ClusterByIngestTokenHash(ctx, hash)`; migration `0048` adds the column
   with a unique key (a token maps to at most one cluster). Fake + shared
   conformance.
2. **Minting (clusterapp)**: `RegisterBYOC` mints and returns the token once
   (registration response carries it; the row stores the hash);
   `RotateIngestToken(ctx, name)` re-mints (old token dies atomically with
   the hash overwrite). Pure helpers (encode/hash) live in the domain
   package; `crypto/rand` stays in the app layer, mirroring phase 10's
   telemetry seam.
3. **Ingest auth (httpapi)**: Bearer → global-token constant-time match ⇒
   operator path (unchanged); else SHA-256 → registry lookup ⇒ cluster X ⇒
   load the batch's execution and require `execution.cluster == X.name`.
   Deps gains the two narrow collaborators (registry lookup, execution
   loader); both injectable for handler tests.
4. **e2e**: two registered clusters with distinct tokens; matrix of
   token×execution-routing outcomes; global token still universal.
5. **Live dogfood**: register the homelab itself as a pseudo-BYOC cluster
   (its own kubeconfig), run one execution routed to it end to end, and
   curl-prove the token matrix (right token + right cluster accepted; right
   token + default-cluster execution 403; garbage 401). Findings appended
   here, Phases 7/10/11 format.

## Acceptance criteria

1. BYOC registration returns a one-time ingest token; the registry stores
   only its SHA-256; rotation replaces it atomically.
2. Ingest: per-cluster token ⇒ only that cluster's executions; global token
   ⇒ everything (unchanged); unknown ⇒ 401; cross-cluster ⇒ 403 — covered by
   handler unit tests and e2e.
3. A BYOC-registered cluster schedules executions and returns reports end to
   end (live dogfood on the homelab).
4. Phase-8 quota/routing semantics untouched (existing suites stay green).
5. Standard bar: gofmt/vet/golangci-lint, unit race, MySQL conformance for
   the widened registry, e2e, coverage gate; live findings appended.

## Open questions

None blocking. (Draft's mTLS / public-images / agent-relay questions are
resolved as non-goals or documented prerequisites above.)

## Live verification findings (task 133, 2026-08-17)

Live dogfood on the real Talos cluster (`admin@talos-homelab`, ns `honryu`),
API image `honryu-api:phase12` (built + pushed this task; migration `0048`
auto-applied on rollout, startup clean), engine `engine-jmeter:5.6.3`
(declarative `requests.yml` → `https://httpbin.pve.heri.life/headers`). **All
live gates met first try, no retries — and the pass surfaced one real gap the
suites do not cover.**

### BYOC registration of the plane's own cluster (AC1, AC3)

`POST /api/clusters` with the homelab's own admin kubeconfig (self-contained:
embedded CA + client cert, server `https://10.10.10.60:6443`) ⇒ **201**,
origin `byoc`, `secret_ref honryu-cluster-dogfood-byoc` materialized as a
real home-cluster Secret (api-url matches), one-time token minted: 43-char
base64url. The probe passed from inside the control-plane pod — the plane
reached the API server it runs on. `GET /api/clusters/dogfood-byoc` never
surfaces the token or its hash.

### Dogfood run end to end (AC3, AC5)

Execution `on-byoc` (cluster `dogfood-byoc`) deployed + triggered: engine pod
`engine-3-9-7-0` 2/2 Running on the same cluster — scheduled through the
BYOC client built from the materialized Secret, per-cluster
`IngestURL`/`SidecarImage` overrides in effect. The sidecar authenticated
with the *global* engine token (existing `honryu-ingest` Secret, Optional
env — seam unchanged), KPIs flowed, report finalized naturally:
`outcome=passed`, **147,596 samples**, `cluster: "dogfood-byoc"` recorded,
`correlation_id 4de733f0ed8dab2a…`. Control execution `on-default` (no
cluster) on the default plane in parallel: `passed`, correlation id present,
no cluster field — operator path untouched.

### Token matrix live (AC2)

All curls against open runs, verdicts on first try:

| Probe | Verdict |
|-------|---------|
| dogfood token + `on-byoc` batch | **202** |
| dogfood token + `on-default` batch | **403** |
| garbage token + `on-byoc` batch | **401** |
| old dogfood token after rotate | **401** |
| new dogfood token after rotate, same open run | **202** |

Rotation mid-run is a hard cut: the old token 401s immediately, the new one
absorbs into the same run. Authz verdicts fire before run-state checks, so
403/401 hold regardless of run state (proven: the 403 probe targeted a live
default-plane run and was rejected before its interval could land — run 10's
report stayed clean).

### Gap found: cluster delete leaves the credential Secret behind

`DELETE /api/clusters/dogfood-byoc` ⇒ 204, registry row gone, active-run
guard honored — but the materialized home-cluster Secret
`honryu-cluster-dogfood-byoc` **remained**. Registration's *rollback* paths
clean it up; Delete does not (phase 8 scoped Delete to the registry entry).
Deleted manually here; left as a noted follow-up, not a phase-12 blocker
(the Secret holds the customer's own credential on the operator's cluster).

### Verification artifact worth recording

My accepted probe batches carried epoch-scale `ts` while engine intervals
are relative seconds, so run 9's `achieved.duration_seconds` reported the
epoch span (~1.79e9) instead of ~69s — operator artifact of the probe
method, authz and absorption unaffected. It does surface a pre-existing
property of `report.Accumulator.measuredSpanSeconds`: any *accepted* batch
with a far-future `ts` inflates the measured span. Not phase-12 (the
accumulator predates it), recorded for whoever owns reports next.

### Cleanup

`dogfood-byoc` deleted (204), credential Secret removed, both executions
purged (engine pods gone), port-forward stopped (18080 closed). Project
`phase12-dogfood` left in place (delete 409s on existing executions, same
as `p10-live`/`p11-live`). The plane now runs `honryu-api:phase12`.
