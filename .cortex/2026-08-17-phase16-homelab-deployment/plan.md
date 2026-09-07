# Phase 16 — Homelab deployment — Plan

**Spec:** `.cortex/2026-08-17-phase16-homelab-deployment/spec.md`

## Context (read from the code and the live cluster, 2026-08-17)

**Live state, declared nowhere in the repo.** Namespace `honryu`: Deployments
`honryu-api` (image `:phase13`, `imagePullPolicy: Always`, port 8080, readiness
`/healthz`, `serviceAccountName: honryu`, `hostPath /var/lib/honryu-storage` →
`/honryu-storage`, 14 env vars), `honryu-calibrator` (`:latest`), `mysql`
(`mysql:8.4`, **`emptyDir`**); Services `honryu-api` (ClusterIP 8080) and `mysql`
(ClusterIP 3306); Ingress `honryu-api` (class `nginx`, host
`api.honryu.local`, address `10.10.10.90`); `ServiceAccount honryu`,
`Role`/`RoleBinding honryu-scheduler`,
`ClusterRole`/`ClusterRoleBinding honryu-nodes-reader`; Secrets `honryu-ingest`
(key `token`), `mysql-credentials` (key `dsn`), `cluster-honryu-explicit-creds`,
`registry-pve-heri-life`. **No `honryu-scheduler` Deployment, no Prometheus, no
Grafana, no PVC, and no StorageClass in the cluster at all.**

**Cluster.** One node, `talos-cp`, k8s v1.31.4, **untainted** — which is why
engine pods have been running on the control plane. Karpenter with the Proxmox
provider (`karpenter.proxmox.sinextra.dev`) is installed; NodePool `mi666-1`
(requirements `arch=amd64`, `zone=mi666-1`) currently has **0 nodes**.

**Engine pod placement and resources.**
`internal/adapters/scheduler/k8s/k8s.go:323` sets only
`Resources: resourceRequirements(spec)` — the adapter has **no `NodeSelector`,
`Affinity` or `Tolerations`**. `resourceRequirements`
(`k8s.go:448-460`) returns an **empty** `ResourceRequirements{}` when both
`spec.CPU` and `spec.Memory` are empty, and those come from the *execution's own*
fields (`internal/domain/execution/execution.go:71-76` →
`internal/app/lifecycleapp/service.go:338`). Karpenter only provisions for
Pending pods, so a default execution's engines always fit on `talos-cp` and
`mi666-1` never scales.

**Node-pool reporting is misconfigured for this cluster.** `NodePools()`
(`k8s.go:635-647`) groups by `s.poolLabel`, which falls back to
`defaultPoolLabel = "cloud.google.com/gke-nodepool"` (`k8s.go:64`) — a GKE label
no node here carries — and `PoolLabel` has **no env var anywhere in `cmd/`**
(only `factory.go:167` passes it through). So the API reports one `"default"`
pool while the real pool is `mi666-1`.

**Other facts the chart depends on.** `/metrics` is a real route
(`internal/adapters/httpapi/router.go:128`, `promhttp.Handler()`, tagged
`health`). `cmd/scheduler` loads config identically to the API
(`cmd/scheduler/main.go:73`, `config.Load(getenv)`), so one env template serves
both. `grafana/dashboards/honryu.json` renders
`"type": "grafana-piechart-panel"` (vendored AngularJS plugin v1.3.3);
dashboards are `schemaVersion: 16`. `grafana/metrics-dashboard/` is a
`helm create` scaffold with untouched defaults pointing at
`localhost/grafana` (`pullPolicy: Never`) in a nonexistent namespace.

## Corrections to the spec, from this reading

1. **Spec open question 1 is answered: engine steering needs code.** With no
   placement fields in the adapter, engines cannot be directed onto Karpenter
   nodes — a follow-up phase, recorded in task 170.
2. **Decision 2 needs a caveat: the chart cannot make Karpenter fire.** Engine
   pods request nothing unless an execution declares `CPU`/`Memory`, so minimal
   platform requests do not cause node provisioning — they merely leave room for
   engines to keep landing on the control plane. Verification executions must
   declare resources for AC-relevant Karpenter behaviour to be observable at all,
   and that is an operator responsibility to document, not a chart setting.
3. **New finding, out of scope here:** the hardcoded GKE pool label makes
   Honryu's node-pool view wrong on this cluster. Logged as a follow-up in
   task 170, not fixed (no app code changes).

## Approach

Prerequisites (StorageClass) and the credential-key rotation land first as
independent, verifiable steps. **The endpoint-scoping audit runs third — before
anything is exposed** — against the current port-forwarded deployment, which is
what makes it a precondition rather than a postmortem.

The chart is then built component by component, each step verified by
`helm lint`, `helm template`, and a **server-side dry-run**
(`kubectl apply --dry-run=server`), so the whole chart is proven while the
hand-built deployment is still serving. Cutover is deliberately late and clean:
remove the hand-made objects, `helm upgrade --install`, nothing to migrate.

Live verification is last and targets the three parent-spec acceptance criteria
that have never been demonstrable in a deployment: report durability across a
MySQL pod deletion (`:98`), a scheduled execution firing unattended (`:178`), and
a Grafana dashboard rendering live data (`:196`).

*Rejected:* Helm adoption of the existing objects via `meta.helm.sh/*`
annotations — MySQL changes kind (Deployment → StatefulSet) so it cannot be
adopted in place, and half-adopting the rest preserves the ambiguity this phase
exists to remove.

## Risks

| Risk | Mitigation |
|---|---|
| Cutover destroys the platform and the chart fails to come up | Every component server-side dry-run (162–167) before 169 deletes anything; start-clean means there is no data to lose |
| A stateful pod lands on a Karpenter node and loses its PV | `nodeSelector: node-role.kubernetes.io/control-plane: ""` on MySQL, Prometheus and the object store — asserted in rendered output, not merely intended |
| Key rotation breaks BYOC credential decryption | 160 enumerates `origin: byoc` first; only those seal a kubeconfig (`clusterapp/service.go:112-115`), and re-registration is the remedy |
| The `/metrics` ingress block also blocks Prometheus | Prometheus scrapes the Service in-cluster, never through the ingress; 165 asserts both halves separately |
| Grafana still will not render after the panel migration | schemaVersion 16 auto-migration is unproven (Grafana has never run here); 167's criterion is a *rendered* dashboard against live data, not a ready pod |
| One node hosts control plane + monitoring + 50k-QPS engines | Minimal platform requests; verification executions declare CPU/Memory so Karpenter can absorb fan-out. If the node still saturates, that is a finding to record, not to hide |
| Chart drifts from the live deployment over time | 168 puts `helm lint`/`template` in CI, and after 169 the cluster has no hand-made objects left to drift from |

## Out of scope

Engine-pod placement/affinity and the hardcoded GKE pool label (both need app
code — follow-up phases); TLS/cert-manager; OIDC; GitOps; HA or platform
autoscaling; preserving phases 10–13 data; any application code change.

## Verification

Per task: `helm lint`, `helm template`, `kubectl apply --dry-run=server`, and for
the chart as a whole a from-scratch `helm upgrade --install`. Phase-level, on the
real cluster: platform reachable at `honryu.pve.heri.life` over LAN and
Tailscale with the SPA at `/` and `/metrics` blocked through the ingress while
Prometheus still scrapes in-cluster; a full deploy→trigger→report cycle driven
through the UI; a scheduled execution firing with no human trigger; reports
surviving `kubectl delete pod mysql-0`; a Grafana dashboard rendering live run
data. Findings appended to `spec.md` in the phases 7/10–13 format, including the
two app-level gaps logged as follow-ups.
