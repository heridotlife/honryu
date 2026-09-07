# Phase 8 — Multi-cluster operation

Agreed via brainstorm 2026-08-11, continuing the brainstorm → write-plan →
execute-plan cadence of Phases 5/6/7. Roadmap one-liner
(`.cortex/2026-07-30-honryu/tasks.md` line 337-338): "Cluster registry with
per-cluster credentials and quotas; scheduling into any registered cluster;
campaigns spanning clusters yielding one verdict; BYOC left as an unimplemented
seam. Depends on: Phases 5, 6 (built on the cluster-addressable seam from
Phase 3)."

## Problem

Honryu can only generate load from the single cluster its control plane runs in.
`cmd/*`'s `newScheduler` builds one k8s client from `rest.InClusterConfig()`,
and `lifecycleapp` always passes `ports.ClusterRef("")` (`service.go:254`), so
the `ClusterRef` threaded through every scheduler method (a Phase 3 forward-compat
seam) is inert. A customer therefore cannot test from where their users actually
are, and is implicitly locked to whichever cloud hosts that one cluster —
migrating clouds means redeploying the whole platform. The reservation ledger and
schedules are already cluster-scoped (Phase 5), but the dimension is always
`"default"`/`""`.

## Goal

Let an operator register multiple Kubernetes clusters — across any provider
(GKE/EKS/AKS/on-prem) — and choose, per run, which cluster generates the load,
so **load origin is explicit and the platform is provider-neutral**. A cluster is
a user-chosen property of the run, surfaced in the report. A customer can migrate
clouds by registering a new cluster and re-pointing runs at it, with no platform
redeploy.

## Non-goals

- **No capacity-aware placement engine.** Automatic placement is a **static
  default cluster** only: an execution that names no cluster runs on the
  configured default; the user picks a different registered cluster explicitly.
  Health-aware failover and capacity-aware selection are later phases.
- **No provider-specific auth.** Static bearer-token / client-cert only. No GKE
  exec-plugin, no aws-iam-authenticator, no cloud SDKs in the scheduler —
  provider-neutrality *requires* this, and exec-based auth would not work from
  the control-plane pod anyway.
- **No external secrets manager (Vault) yet.** Credentials are consumed as native
  k8s Secrets in the home cluster; an external-manager backend is a future seam
  behind the same credential-read interface.
- **No control-plane-per-cluster.** One central control plane reaches out to all
  registered clusters.
- **No cross-cloud networking solution.** Honryu assumes each cluster's API server
  is reachable from the control plane and each cluster's ingest URL is reachable
  from that cluster; establishing that reachability (VPN/peering) is the
  operator's job. Honryu surfaces failures clearly but does not create
  connectivity.
- **No customer-facing BYOC self-service UI.** BYOC registration is implemented
  (API-first), but a polished customer self-service flow is out of scope.

## Constraints

### Technical / structural
- **Provider-neutral auth only.** A registered cluster authenticates with a
  static bearer token (a ServiceAccount token) or client cert, plus an embedded
  CA and API-server URL. A BYOC kubeconfig must be **self-contained** (embedded
  CA + token/cert, no `exec` / `auth-provider` block) and is rejected at
  registration otherwise.
- **Backward compatible.** Every existing execution/schedule carries
  `Cluster=""`; that must keep resolving to a **default cluster** — the control
  plane's own `InClusterConfig`, the implicit default registry entry — with
  behavior identical to today.
- **Hexagonal, matching Phases 5–7.** A new `clusterregistry` domain and an app
  service (`clusterapp`, or folded into an existing app), ports + mysql/fake
  adapters + a repository conformance contract. Domain imports no ports.
  Credential materialization (reading/writing a k8s Secret, encrypting the MySQL
  BYOC store) is an **adapter** concern, not domain.
- **Mixed credential source, uniform consumption.** Every registry entry
  references a **k8s Secret** in the home cluster, and the scheduler *always*
  builds its client by reading that Secret — one consumption path. An
  operator-registered cluster's Secret is its source of truth. A **BYOC**
  cluster's source of truth is its self-contained kubeconfig, stored
  **encrypted-at-rest in MySQL** and **materialized into a k8s Secret** for the
  controller; a reconcile keeps the Secret in sync. The external secrets manager
  (future) replaces the materialization layer, not the interface.
- **The two single-valued config fields move per-cluster.** `SidecarImage` and
  `IngestURL` (wired onto `ClusterConfig` in Phase 7, commit `6123985`) become
  properties of each **registry entry**, since a GKE cluster and an on-prem
  cluster may not share one reachable ingest address or image source.

### Safety (load-bearing)
- **Least privilege.** The token Honryu uses needs only StatefulSets, ConfigMaps,
  pods, and pod logs in one namespace. This is the documented setup contract (the
  Role an operator must grant), and registration validation checks it.
- **Registration validation.** On register, build a client and probe the target
  cluster (server version + namespace access); reject an unreachable /
  unauthorized / under-privileged cluster with a stated reason rather than
  discovering it when a run fails.
- **Deletion guard.** Deleting a cluster with active deployments is rejected. An
  unreachable cluster at deploy time fails the run with a clear "cluster
  unreachable" error, best-effort like other deploy failures.
- **RBAC.** Cluster-registry management (register/list/update/delete) is gated to
  a **platform-admin** scope, not a tenant-level one.

### Verification bar (as Phases 5–7)
`go build`/`vet`/`gofmt`/`golangci-lint`, unit tests, MySQL conformance for new
ports, e2e where relevant, `scripts/coverage.sh` as a check. Plus **live
verification** on the real cluster: register the existing `honryu`-namespace
cluster as an explicit registry entry (rather than the implicit default) and run
an execution against it end-to-end, proving the registry-backed client path — not
just the `InClusterConfig` default.

## Approach

### New domain + persistence
A `clusterregistry` entity: `{ name (the ClusterRef), api_url, ca_cert,
ingest_url, sidecar_image, namespace, secret_ref, origin: operator|byoc,
byoc_credential (encrypted, BYOC only), health/created metadata }`. New ports
(register/get/list/update/delete + resolve-for-scheduling), a MySQL adapter, a
fake, and a repository conformance contract. The BYOC ciphertext column and its
envelope encryption (app-held key) live in the adapter.

### Cluster on the execution
Add `Cluster string` to `execution.Execution` (empty = default), the same
"empty means the deployment default" convention `Engine`/`CPU`/`Memory` already
use (`execution.go:60`). `lifecycleapp.Deploy`/`Trigger` resolve it and pass the
real `ClusterRef` to the scheduler (replacing the hardcoded `""` at
`service.go:254`) and to `quota.Reserve` (already cluster-scoped). `report.Meta`
gains a `Cluster` field (load origin), surfaced via the API.

### Registry-backed scheduler (the one real rewrite)
The k8s scheduler adapter stops holding a single client. Given a `ClusterRef`, it
resolves the registry entry, reads its k8s Secret, and builds — and caches — a
client for that cluster; the cache rebuilds lazily on failure (covers credential
rotation). The default cluster still resolves to `InClusterConfig`. Per-cluster
`SidecarImage`/`IngestURL` come from the resolved entry. Everything else in the
adapter is unchanged.

### BYOC flow
A customer submits a self-contained kubeconfig → validated (self-contained shape
+ connectivity/RBAC probe) → stored encrypted-at-rest in MySQL → materialized
into a home-cluster k8s Secret → registry entry references it. The credential-read
interface is abstracted so a Vault backend later replaces the materialization.

### schedule.Cluster removed
Since cluster is now a property of the execution (the single source of truth),
`schedule.Cluster` (Phase 5) is dropped: a scheduled run deploys to — and reserves
quota against — its **execution's** cluster. This also fixes an existing
inconsistency where the schedule stored a cluster for quota but `cmd/scheduler`'s
fire path deployed to the one `InClusterConfig` cluster regardless.

### Rejected alternative
**Control-plane-per-cluster with coordination** — rejected (matches roadmap):
far more machinery, and the push-based metrics model already means the control
plane never *scrapes* into a cluster, only *deploys* into it, which one central
plane does fine. **Capacity-aware placement** — rejected for v1: load origin is
usually user-chosen and meaningful, so a placement engine is machinery for the
"don't care" minority; it reads better as its own later phase.

## Acceptance criteria

1. An operator registers a cluster (`name, api_url, ca, ingest_url,
   sidecar_image, namespace` + credential); registration validates connectivity
   and namespace RBAC and rejects an unreachable / unauthorized / under-privileged
   one with a stated reason.
2. A self-contained BYOC kubeconfig registers successfully; one carrying an
   `exec`/`auth-provider` block is rejected with a clear message. Its credential
   is stored encrypted-at-rest in MySQL and materialized into a home-cluster k8s
   Secret.
3. An execution can name a target cluster (empty ⇒ default); both manual
   `Trigger` and scheduled fire deploy it to that cluster.
4. Empty cluster resolves to the default (control plane's own `InClusterConfig`,
   the implicit default entry); every pre-Phase-8 execution/schedule behaves
   identically to today.
5. The scheduler builds and caches a per-cluster client from the registry entry's
   Secret; deploy / status / logs / purge for an execution all target its cluster.
6. Engine-equivalents quota reserves against the execution's cluster (existing
   per-`(tenant,cluster)` ledger); an over-quota cluster rejects the run, scoped
   to that cluster.
7. The sidecar image + ingest URL for an execution's engines come from its
   cluster's registry entry, not the global config.
8. The report records the cluster (load origin), surfaced via the API.
9. A campaign whose executions span clusters yields one verdict (rollup is
   cluster-agnostic).
10. `schedule.Cluster` is removed; a scheduled run's cluster (deploy + quota) is
    its execution's.
11. Deleting a cluster with active deployments is rejected; an unreachable cluster
    at deploy time fails the run with a clear "cluster unreachable" error.
12. Cluster-registry management (register/list/update/delete) is RBAC-gated to a
    platform-admin scope, not tenant-level.
13. **Live:** the existing `honryu`-namespace cluster, registered as an explicit
    entry, runs an execution end-to-end through the registry-backed client path.

## Open questions

Deferred to write-plan; none block the design.

- Encryption scheme for the MySQL BYOC store — envelope encryption with an
  app-held key (from config/env); key rotation left as a detail.
- Is the default cluster a real registry row or a synthetic/implicit entry
  (affects list/delete semantics)?
- Client-cache invalidation on credential rotation — lean "lazy, rebuild on next
  use / on failure."
- Cluster health beyond register-time — lean "lazy fail-at-use" for v1; periodic
  health check deferred.
- Exact HTTP endpoint shapes — deferred to write-plan, as in prior phases.
- External secrets manager & customer-facing BYOC self-service UI — explicit
  future seams behind the abstracted credential-read interface.
