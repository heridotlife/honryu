# Phase 12 — BYOC — Plan

**Spec:** `.cortex/2026-08-16-phase12-byoc/spec.md`

## Context

- **Phase 8 shipped more of BYOC than the draft spec assumed.**
  `clusterapp.RegisterBYOC` (`service.go:114-152`) does parse → probe →
  seal (secretbox) → `CreateCluster` → materialize home-cluster Secret →
  `SetClusterCredential`, with full rollback; `POST /api/clusters` exposes it
  via a `kubeconfig` form field (`cluster_handlers.go:97-98`). The
  scheduler's `ClientFactory.Resolve` (`scheduler/k8s/factory.go:79-118`)
  builds a per-registered-cluster client from the entry's SecretRef and
  overrides `sidecarImage`/`ingestURL` from the entry (`factory.go:105`) —
  the per-cluster data plane exists. The engine pod's ingest credential is
  an Optional Secret env (`k8s.go:350-363`), so customer clusters without a
  `honryu-ingest` Secret still schedule.
- **The gap is ingest auth, and only that.** `authorizeIngest`
  (`httpapi/ingest_handlers.go:47-63`) knows one global
  `Deps.IngestToken` (constant-time compare; empty ⇒ reject everything) and
  `ingest` then hands `Metrics.Ingest` a batch whose `ExecutionID` names its
  target — attacker-controlled at a hosted boundary. Nothing today can tell
  cluster A's pods from cluster B's, or from anyone holding the shared
  token.
- `cluster_registry` (`migrations/0040_cluster_registry.sql`) has no token
  column; `name` is the primary key and the ClusterRef. Migrations are at
  `0047`; this phase claims `0048`.
- The registry port (`ports/clusterregistry.go`) already has the
  credential-custody precedent `SetClusterCredential(ctx, name, ciphertext)`
  (`:37-42`) — the exact shape `SetClusterIngestTokenHash` mirrors.
- Execution→cluster routing is one field read:
  `lifecycleapp.clusterFor` resolves `execution.Cluster` (empty = the
  deployment default, `service.go:603-612`); the ingest side needs the same
  one-field read via `GetExecution`, which the mysql repo and fake both
  already implement (`ports.Repository`).
- Minting precedent: phase 10's telemetry seam — pure formatting in the
  domain (`telemetry.TraceContext`), `crypto/rand` in the app
  (`lifecycleapp.randomTraceContext`). Token encode/hash helpers follow it:
  pure, table-tested, in `domain/clusterregistry`; randomness in
  `clusterapp`.
- Token shape: 32 random bytes, base64url (`43` chars) — high-entropy bearer
  credential. At rest `hex(SHA-256(token))` (64 chars): plain SHA-256 is
  correct here *because* lookup-by-hash of a high-entropy value needs no
  slow KDF (a password does; a 256-bit random token does not).
- Authz statuses: `writeError(401)` unknown token (matches today's invalid
  credentials), `403` right-token-wrong-cluster — distinct so a sidecar log
  names the misconfiguration. The global token path must stay
  byte-for-byte today's behavior.

## Approach

One vertical slice, three layers top-down, then verification:

**Registry (persistence).** `clusterregistry.Cluster` gains
`IngestTokenHash string` (json-excluded; never surfaced — same rule as
`byoc_credential`). Port gains `SetClusterIngestTokenHash` and
`ClusterByIngestTokenHash`; migration `0048` adds
`ingest_token_hash CHAR(64) NULL UNIQUE` (NULL for operator rows and BYOC
rows pre-rotation; unique because a token must resolve to at most one
cluster). Fake + conformance case (set, resolve, clear-by-rotate, no
collision).

**Minting (app).** Pure `clusterregistry.MintIngestToken` shape:
`EncodeToken(b []byte) string` / `HashToken(token string) string` (domain,
table-tested); `clusterapp` holds `crypto/rand`. `RegisterBYOC` mints after
the existing probe passes, stores the hash with the entry, and returns the
token once — signature change rippling to the handler and its tests.
`RotateIngestToken(ctx, name)` (BYOC-or-any cluster) re-mints and overwrites
atomically; old token dies with the write. Exposed as
`POST /api/clusters/{name}/rotate-ingest-token`.

**Ingest auth (adapter).** `authorizeIngest` becomes resolve-shaped:
global-token constant-time match ⇒ operator path (accept, unchanged);
otherwise SHA-256 the presented token and look the cluster up — miss ⇒ 401;
hit ⇒ load `batch.ExecutionID`'s execution and require
`execution.Cluster == cluster.Name`; empty `Cluster` (default-cluster
execution) fails this path by construction (a registered cluster's name is
never empty). 403 on mismatch. `Deps` gains two narrow collaborators
(`ClusterTokenResolver`, `ExecutionClusterLoader`), both satisfied by the
repo; nil resolver ⇒ global-token-only mode (tests/local) so existing
harnesses need no rewiring unless they exercise the new path.

**Verification.** Handler unit matrix (global/valid/unknown/cross-cluster ×
default/routed executions); e2e with two registered fake clusters and the
full token×routing matrix; live dogfood: register the homelab as
`dogfood-byoc` (its own kubeconfig), run one execution routed to it end to
end (schedules, KPIs, report), curl the token matrix against both a routed
and a default execution. Findings appended to the spec, Phases 7/10/11
format.

Rejected: per-request bcrypt verification (wrong tool for 256-bit bearer
tokens, and it would cap ingest throughput); embedding the token in the pod
spec from the plane (forces plaintext custody on the plane — the customer's
own Optional Secret is custody-correct); mTLS (deferred, spec non-goal).

## Risks

| Risk | Mitigation |
|---|---|
| Widening `RegisterBYOC`'s signature breaks callers. | Compiler-enumerated: one app caller (the handler) + its tests; single task. |
| Hash-lookup timing side channel. | Non-issue at this entropy, but the lookup is a single indexed equality on a fixed-length hex column — no per-byte signal. |
| A customer rotates while their fleet is mid-run. | Documented semantics: old token dies atomically; in-flight pushes 401 and the sidecar retries with whatever Secret the customer updated — the same recovery path a plane outage already has. |
| e2e needs two clusters with real hashes in the fake registry. | The registry is repo-backed in e2e (real MySQL) — seed via the same `SetClusterIngestTokenHash` the app uses; no fake-specific behavior. |
| Dogfood curls hitting the orphan guard (no open run). | Target a live run (the dogfood execution's own), or assert on the 401/403 authz verdicts which fire *before* run-state checks. |

## Out of scope

Customer-tenant registration roles (SaaS auth), billing, mTLS, multi-region
plane, agent-side relaying, changing the sidecar's token plumbing (the
Optional Secret env is the contract), and any UI (Phase 13).

## Verification

Standard bar: gofmt/vet/golangci-lint, unit race (domain helpers table
tests, handler matrix, clusterapp mint/rotate), MySQL conformance for the
two new registry methods, e2e token×routing matrix, coverage gate. **Live
dogfood is the gate for the seam claim** (a BYOC-registered cluster runs
executions end to end), findings appended to the spec.
