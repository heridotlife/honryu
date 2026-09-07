# Phase 8 — Multi-cluster operation — Plan

Spec: `.cortex/2026-08-11-phase8-multi-cluster/spec.md`. Tasks: `tasks.md`
(86–102). Continues Phases 5/6/7's brainstorm → write-plan → execute-plan
cadence.

## Context (what exists now)

- `ports.ClusterRef` is threaded through every `ports.Scheduler` method
  (`internal/ports/scheduler.go:108-122`) as a Phase 3 forward-compat seam, but
  it is inert: `lifecycleapp.Deploy` builds its `DeploySpec` with `Cluster: ""`
  (`internal/app/lifecycleapp/service.go:254`) and `Trigger` passes `""` to
  `quota.Reserve` (`service.go:382`).
- The k8s scheduler holds **one** client built from `rest.InClusterConfig()`
  (`cmd/api/main.go:250`, `cmd/scheduler/main.go:335`). `Scheduler{client, ns,
  sidecarImage, ingestURL}` is single-valued (`internal/adapters/scheduler/k8s/
  k8s.go:107`), constructed by `New(client, Config)` (`k8s.go:118`); `Config`
  carries `SidecarImage`/`IngestURL` (`k8s.go:91-99`).
- `execution.Execution` already models `Engine`/`CPU`/`Memory` with the "empty
  means the deployment default" idiom (`internal/domain/execution/execution.go:
  60-82`) — the natural home for `Cluster`.
- `quotaapp.Reserve(ctx, tenantID, cluster string, …)` is **already
  cluster-scoped** (`internal/app/quotaapp/service.go:104`); the reservation
  ledger is per-`(tenant, cluster)`. Only the caller passes `""`.
- `schedule.Schedule.Cluster` (`internal/domain/schedule/schedule.go:44`) was
  added in Phase 5 and stored for quota, but `cmd/scheduler`'s fire path deploys
  via `Deploy(executionID)` and ignores it (`cmd/scheduler/main.go:177`) — the
  inconsistency task 99 removes.
- `report.Meta` (`internal/domain/report/report.go`) has no cluster field.
- `config.ClusterConfig` holds a single `SidecarImage`/`IngestURL` (wired in
  Phase 7, commit `6123985`).
- RBAC: `rbac.Authorize(acct, catalog, Request{Resource, Action})`
  (`internal/domain/rbac/rbac.go:114`); the platform-admin gate to mirror is
  `authorizeAdmin` (`internal/adapters/httpapi/tenant_handlers.go:63`), which the
  kill-switch expresses as `rbac.Request{Resource: rbac.ResourceSystem, Action:
  rbac.ActionAdmin}` (`admin_handlers.go:62`).

## Approach

A new `clusterregistry` domain plus ports / mysql / fake / conformance holds the
registry entity. The scheduler's single injected client becomes a **client
factory keyed by `ClusterRef`** that resolves a registry entry, reads its
home-cluster k8s Secret, and caches the built client (the default resolves to
`InClusterConfig`); per-cluster `SidecarImage`/`IngestURL` come from the entry.
`Cluster` is added to the execution and threaded through `Deploy`/`Trigger` to
the scheduler and `quota.Reserve`; `report.Meta` gains it as load origin.
`schedule.Cluster` is removed and derived from the execution. Registry CRUD is
HTTP-exposed, platform-admin-gated, with connectivity/RBAC validation on register
and self-contained-kubeconfig validation for BYOC.

Chosen over **control-plane-per-cluster** because the push-metrics model means the
central plane only ever *deploys* into a cluster, never scrapes it — one plane
suffices — and over **capacity-aware placement** because load origin is
user-chosen and meaningful, making a placement engine machinery for the "don't
care" minority (a later phase).

## Risks

1. **The scheduler client-factory rewrite is on the hot deploy path.** →
   Sequenced *behind* the tasks that prove the registry entity + resolution seam
   in isolation (86–88, 94); the default cluster keeps `InClusterConfig` as the
   unchanged fallback; the fake scheduler is untouched, so most tests never
   exercise the new path.
2. **BYOC credential handling is security-sensitive.** → Adapter-isolated
   (envelope encryption + Secret materialization out of the domain);
   self-contained-kubeconfig validation rejects `exec`/`auth-provider`;
   conformance and round-trip/tamper tests; secrets never logged.
3. **Removing `schedule.Cluster` changes Phase 5 schema/behavior.** → A migration
   drops the column and the fire path derives the cluster from the execution; the
   schedule conformance contract and the fire e2e cover it.
4. **Backward compatibility — existing `Cluster=""` runs must behave
   identically.** → The default cluster resolves to `InClusterConfig`; task 101
   asserts single-cluster behavior is unchanged.
5. **True cross-cloud reachability needs a second cluster to test.** → Task 102
   registers the existing `honryu` cluster as an *explicit* entry (not the
   implicit default), exercising the registry-backed client + Secret path against
   a real API server even on one physical cluster.

## Out of scope

Capacity-aware placement; provider exec-auth (GKE/EKS plugins); external secrets
manager (Vault); control-plane-per-cluster; a customer-facing BYOC self-service
UI; establishing cross-cloud networking (VPN/peering). The credential-read
interface is abstracted so a Vault backend can replace the k8s-Secret
materialization later without touching the scheduler.

## Verification

`go build`/`vet`/`gofmt`/`golangci-lint`, unit tests, MySQL conformance for the
new registry port, e2e for cluster-on-execution routing + schedule fire +
campaign-spanning + backward-compat default, and live verification (register the
`honryu` cluster explicitly, run an execution end-to-end through the
registry-backed client path). `scripts/coverage.sh` as a check against the
existing repo baseline.
