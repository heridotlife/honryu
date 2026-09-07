# Phase 18 — Gap closure: engine placement, node pools, execution resources, auth scoping (DRAFT)

**Status: drafted from recorded findings, not agreed.** Written 2026-08-19 by
sweeping every gap phases 14–16 logged but deliberately deferred. Needs a
`brainstorm` before `write-plan` — in particular, strand D is a security
posture decision, not a coding task, and should not be planned as one.

Every item here was found *live* and is already written down; this spec only
collects them, sizes them, and says which belong together. Nothing here is
newly discovered except the sizing.

## Problem

Phases 16's live verification and 15's endpoint audit both ended by logging
app-level gaps as follow-ups, because both phases had an explicit "no
application code changes" non-goal. That was the right call at the time — but
it means seven real defects now sit recorded and unowned, and two of them
directly undercut capabilities the platform advertises.

### A. Karpenter cannot be used, in practice

`internal/adapters/scheduler/k8s/k8s.go:323` sets only
`Resources: resourceRequirements(spec)` on an engine pod — the adapter has no
`NodeSelector`, `Affinity`, or `Tolerations` at all. Engine pods therefore
cannot be steered onto the Karpenter-provisioned `mi666-1` pool. Phase 16
confirmed this live: the pool sat at `nodes: 0` throughout, and a 500m engine
pod bin-packed onto the untainted control plane beside MySQL and Prometheus.

Compounding it, `NodePools()` (`k8s.go:635-647`) groups by `s.poolLabel`,
which falls back to `defaultPoolLabel = "cloud.google.com/gke-nodepool"`
(`k8s.go:64`) — a GKE label no node in this cluster carries — and `PoolLabel`
has no environment variable anywhere in `cmd/`. So the API reports one
`"default"` pool while the real pool is `mi666-1`, and no configuration can
correct it.

Together: the platform can neither place load generators away from its own
control plane nor accurately report the capacity it has. On a load-testing
platform, generators competing with the control plane is a measurement-fidelity
problem, not just a scheduling one — the same concern that motivates
`saturated_by` (parent spec `:88`).

### B. No API path to size an ordinary execution's engine pods

`createExecution` (`internal/adapters/httpapi/execution_handlers.go:63-84`)
accepts only `name`, `project_id`, `engine`, `cluster`. Migration `0035`'s own
comment confirms the omission is deliberate — "only a CalibrateEngine
execution ever pins them" — but the consequence is that an operator cannot
request larger engine pods for an ordinary load test at all.
`resourceRequirements` returns an empty `ResourceRequirements{}` when both are
unset (`k8s.go:448-460`), so those pods request *nothing*, which is also why
Karpenter never sees pressure (strand A). Phase 16 could only satisfy its own
"declare CPU/Memory" instruction with a direct `UPDATE` on
`execution.cpu`/`execution.memory`.

### C. A failed schedule firing is lost

Phase 16 fixed the readiness race in `fireOnce` (`cmd/scheduler/main.go`), but
its remaining doc comment is honest that everything else is still best-effort:
a `Deploy` failure, or a `Trigger` failure that is not the readiness pair,
logs and returns with the occurrence already consumed by `ClaimDue`. A
one-shot schedule that fails for a transient reason simply never runs, with no
retry and no surfaced state beyond a log line. Unattended execution is the
parent spec's headline feature (`:13`, AC `:178`); silently dropping a firing
is the failure mode most at odds with it.

### D. The API is unauthenticated by design, and its exposure is now real

Phase 16's endpoint audit (task 161, findings in that phase's spec) confirmed
against the live deployment that **every** endpoint the SPA consumes, plus
`/metrics` and `GET /api/clusters`, returns `200` with no credentials —
`noauth.Provider.Authenticate` (`internal/adapters/auth/noauth/noauth.go:37-40`)
unconditionally returns a fixed service-provider-admin account. Under
`HONRYU_AUTH_MODE=none` that is working as designed, and phase 16 accepted it
as bounded by the LAN/tailnet perimeter. Three specific gaps were logged as
outliving that decision:

1. `/metrics` is unauthenticated *even under RBAC*.
2. No ACL on tenant-id-in-path routes once auth is enabled — per-route
   authorize checks do not exist.
3. `GET /api/clusters` serves `api_url`, `namespace`, and `secret_ref` to any
   authenticated caller regardless of role — a candidate for admin-only.

The platform is now reachable at a hostname, which is what moves these from
theoretical to worth deciding.

### E. The Helm chart sets no pod or container security context — DONE 2026-08-19

**Closed before this phase started**, in 072ddb0 (PR #208), because the user
asked for PR #207's failing check fixed rather than deferred. Recorded here
in full anyway: the *reason* it was originally deferred turned out to be
correct, just not a reason to postpone.

All six workloads now run non-root with a read-only root filesystem, all
capabilities dropped, RuntimeDefault seccomp, no privilege escalation, each
at the uid its image already used (verified against running pods). Trivy
reports 0 findings on the chart under default values and 0 under the
homelab values.

Two defects fell out of *rolling it out*, neither visible to any scanner:

1. **Prometheus deadlocks itself on upgrade.** RollingUpdate on a
   ReadWriteOnce PVC cannot roll — the incoming pod dies on "lock DB
   directory: resource temporarily unavailable" while the outgoing pod holds
   the TSDB lock, and the outgoing pod is never retired because the incoming
   one never goes Ready. Latent since the component was added. Both it and
   grafana are now `Recreate`.
2. **Hardening silently orphans existing object-store data.** Content
   written while the pods ran as root is mode 0750 root-owned and
   unreadable to uid 10001, and `fsGroup` does not fix it — Kubernetes skips
   fsGroup ownership management for the hostPath volumes local-path hands
   out. Pods come up *healthy*; it presents as empty scenarios and missing
   run artifacts. This is the one that vindicates the original "do not patch
   blind" instinct: a rushed pass would have shipped green CI and a platform
   that had quietly lost its stored artifacts.

Still open, found alongside and deliberately not fixed there:
**`grafana/Dockerfile` bakes `GF_SECURITY_ADMIN_PASSWORD=honryu` into an
image layer** (Trivy DS-0031, CRITICAL — the three `GF_AUTH_ANONYMOUS_*`
matches beside it are false positives, this one is not). Anyone who can pull
the image has Grafana admin, which means datasource edit and arbitrary
query. Fixing it needs a Secret created out-of-band plus an image rebuild
and retag, so it belongs in a phase, not in a merge. Carried as **strand F**.

### E-original. The Helm chart sets no pod or container security context

Found 2026-08-19 on PR #206's own CI run: Trivy raises **75 alerts (16 high)**
against `deploy/chart/honryu/templates/*` and `deploy/cluster/`, the headline
one being "Default security context configured" on all four workloads (api,
calibrator, scheduler, mysql) — no `runAsNonRoot`, no `seccompProfile`, no
`allowPrivilegeEscalation: false`, no dropped capabilities, no
`readOnlyRootFilesystem`.

This is not a regression: the hand-made Deployments the chart replaced had no
security context either. It became *visible* only because phase 16 finally put
the manifests in the repository, where Trivy can scan them — the same class of
improvement as phase 16 making the deployment reproducible at all. The check is
non-required, so it does not block merges (`mergeable: MERGEABLE`, state
`UNSTABLE`), which is exactly why it will sit unaddressed unless owned.

Deliberately not fixed in phase 16: each workload needs its own answer and its
own live verification, and a blind hardening pass would risk breaking a
deployment that was just proven working. `mysql:8.4`'s entrypoint expects to
start privileged before dropping to its own user; Grafana already runs as 472
(phase 16 set a matching `fsGroup`); Prometheus defaults to 65534; the honryu
binaries run from alpine with a PVC mount, so `readOnlyRootFilesystem` and
`runAsNonRoot` both interact with `/honryu-storage`'s ownership. The vendored
`deploy/cluster/` findings are upstream's to own, not ours to restyle.

## Goal

Close the gaps that make advertised capabilities untrue (A, B, C), harden the
chart (E), and make a deliberate decision about D rather than continuing to
inherit it.

## Non-goals

- **Not turning on OIDC.** Whether to leave `AUTH_MODE=none` on a trusted
  perimeter is exactly the decision strand D exists to make; this phase should
  not presume the answer.
- **No new engine images, no Gatling** (parent open question 1 stays open).
- **No multi-node storage rework.** Strand A changes where *engine* pods land;
  the stateful pods stay pinned to the control plane for the reasons phase 16
  established (local-path binds a PV to its node).
- **Not the Rust sidecar** (phase 17's stub, independent).

## Constraints

- Every port touched needs its fake plus the shared conformance suite the real
  adapter also passes (parent non-functional AC `:213`).
- The ≥90% coverage gate holds; it currently sits at 92.3%, so there is margin
  but not a licence to skip tests.
- Strand A must not regress the phase-16 finding that stateful pods stay on the
  control-plane node.
- Live verification on the real cluster, in the phases 7/10–16 format —
  strand A in particular is unprovable without watching `mi666-1` actually
  scale.

## Approach (sketch — real shape at write-plan)

Four strands, in dependency order. **B before A**: engine pods that request
nothing can never create scheduling pressure, so Karpenter cannot be
demonstrated until executions can declare resources.

1. **B — execution resources through the API.** Widen `createExecution` (and
   the OpenAPI schema) to accept optional `cpu`/`memory`, validated as
   `resource.Quantity` strings. The columns already exist (`0035`), the domain
   field already exists, and `lifecycleapp` already threads them to the deploy
   spec — this is an API-surface gap, not a data-model one.
2. **A — placement and pool reporting.** Add `NodeSelector`/`Tolerations` (and
   possibly affinity) to the engine pod spec, sourced from configuration, and
   make `PoolLabel` env-configurable so `NodePools()` reports the real pool.
   Verify live that `mi666-1` scales when a sized execution fans out.
3. **C — firing failure policy.** Decide and implement what a failed occurrence
   does: retry with backoff on a subsequent tick, mark the occurrence failed
   rather than fired, or surface it for an operator. Requires a decision about
   `ClaimDue`'s consume-on-claim semantics, which currently make a firing
   unrepeatable by construction.
4. **E — chart security context.** Per-workload `securityContext`, decided
   individually rather than applied uniformly, each verified by an actual
   rollout: the four honryu binaries (alpine + a PVC mount), mysql (privileged
   entrypoint that drops itself), prometheus (65534), grafana (472, already
   has a matching `fsGroup`). Vendored `deploy/cluster/` findings stay
   upstream's. Success is Trivy's four "Default security context configured"
   failures clearing, not the warning count reaching zero.
5. **D — auth posture.** A decision, then whatever follows from it: keep
   `none` on a trusted perimeter with the exposure documented, or enable OIDC
   and add the per-route authorization the audit found missing. The three
   logged gaps are the agenda, not the answer.

## Acceptance criteria (provisional)

1. An ordinary execution's engine pod resources are settable through the API,
   documented in `openapi.yaml`, and reach the pod spec — verified live, not
   by SQL.
2. Engine pods can be steered onto a named node pool, and a sized fan-out
   demonstrably scales `mi666-1` from 0 — or, if it does not, the reason is
   recorded rather than the claim quietly dropped.
3. `GET /api/.../nodes` reports the cluster's real pool names on this cluster.
4. A schedule occurrence whose firing fails does not silently vanish; its
   behaviour is specified and tested.
5. Strand D's decision is written down in the parent spec's resolved
   decisions, with whatever code it implies landed or explicitly deferred.
6. Standard bar: gofmt/vet/golangci-lint, unit race, conformance for any
   widened port, e2e, coverage ≥90%, `npm run check`, `helm lint`.

## Open questions

1. **Does strand D belong in this phase at all?** It is a security posture
   decision with a much larger blast radius than A–C, and bundling it risks
   holding three mechanical fixes behind one hard conversation. Splitting it
   into its own phase is a live option and probably the better one.
2. **What should a failed firing do** (strand C) — retry, dead-letter, or
   surface-and-stop? Needs the quota implications thought through:
   `ClaimDue` already reserved capacity for the occurrence.
3. **Is engine placement configuration or per-execution data?** A cluster-wide
   "engines go on pool X" setting is simpler; a per-execution override is more
   flexible and mirrors how `cluster` already works.
