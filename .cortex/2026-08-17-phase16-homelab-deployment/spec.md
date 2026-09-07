# Phase 16 — Homelab deployment: declared, durable, and reachable

Agreed via brainstorm 2026-08-17, after phases 14 (CI green) and 15 (governance
closeout). Turns the hand-built talos-homelab deployment into a reproducible
artifact in this repository, fills the three capability gaps that make "ready to
test" untrue today, and puts the platform behind a real hostname.

## Problem

**The platform runs on talos-homelab and exists nowhere in this repo.**
`deploy/` holds three Dockerfiles and a Python KPI reporter — no Deployment,
Service, ConfigMap, Secret, StatefulSet, RBAC or Ingress. Meanwhile
`honryu-api:phase13`, `honryu-calibrator:latest` and `mysql:8.4` have run for
7d5h, created by hand, alongside a `ServiceAccount honryu`, a
`Role`/`RoleBinding honryu-scheduler`, and a cluster-scoped
`ClusterRole`/`ClusterRoleBinding honryu-nodes-reader`. Phases 10–13 each
hand-rolled a build → push → rollout, with the ritual recorded in prose in their
findings rather than in code. If that node died, nothing here would rebuild the
platform.

Five deficiencies sit inside that:

1. **No durable state.** No StorageClass, no PVCs, MySQL on `emptyDir`. Every
   project, execution, report and registry row survives only because one pod has
   not restarted. This makes the parent spec's central retention claim — results
   "outlive engines, Prometheus retention, and the campaign itself"
   (`.cortex/2026-07-30-honryu/spec.md:98`), the entire justification for the
   `ReportStore` port — impossible to demonstrate. The object store is *also*
   node-local (`hostPath /var/lib/honryu-storage`), which works only because
   there is currently one node.
2. **`honryu-scheduler` is not deployed.** Scheduling is the parent spec's
   headline missing feature (`:13`, AC `:178`) and it runs nowhere. Schedules,
   campaign windows and drain loops fire for no one.
3. **No Prometheus or Grafana.** AC `:196` — live QPS, error rate and latency
   percentiles via Prometheus/Grafana — is unproven in deployment. `/metrics`
   exists and is real (`internal/adapters/httpapi/router.go:128`, `promhttp`),
   but nothing scrapes it, and the repo's `grafana/` image with its provisioned
   dashboards has never run in this cluster.
4. **Secret and pinning hygiene.** `HONRYU_CLUSTER_CREDENTIAL_KEY` — the
   secretbox key decrypting every stored BYOC kubeconfig — is a plaintext
   `value:` in the Deployment spec, while `HONRYU_DB_DSN` and
   `HONRYU_INGEST_TOKEN` correctly use `secretKeyRef`. `honryu-calibrator` and
   the sidecar run `:latest`, against the doctrine phase 11 earned live.
5. **No real hostname.** Access is a port-forward or `api.honryu.local` over
   plain HTTP, though MetalLB (`10.10.10.90`) and ingress-nginx are installed.

## Goal

`helm upgrade --install` from this repo reproduces the entire platform on
talos-homelab — API (with its embedded SPA), scheduler, calibrator, MySQL on
durable storage, Prometheus and Grafana — reachable at
`honryu.pve.heri.life` over LAN and Tailscale, with every secret referenced
rather than inlined and every image pinned. "Is the platform ready to test?"
becomes a chart version rather than a memory of what was typed.

## Non-goals

- **Not internet-facing.** LAN + Tailscale only. `HONRYU_AUTH_MODE=none` stays,
  documented as bounded by the tailnet perimeter, with OIDC (already
  implemented, `config.go:351`) recorded as the seam, not built.
- **No TLS or cert-manager.** No ClusterIssuer exists; internal HTTP over a
  trusted perimeter is the accepted position, recorded as a seam.
- **No GitOps.** Flux/Argo is a third system to operate for one namespace.
- **No HA and no autoscaling of platform components** — single replicas.
- **No application code changes.** Anything the deployment reveals becomes a
  follow-up phase, not an opportunistic edit.
- **No data migration.** Phases 10–13 dogfood fixtures (`p10-live`, `p11-live`,
  `phase12-dogfood`, `phase13-camp`, campaigns 1/2, scenarios 8/9) are discarded
  deliberately — see resolved decision 4.
- **The chart does not own engine pods.** They are created at runtime by the k8s
  scheduler adapter; the chart must not try to manage them.
- **The chart does not install cluster infrastructure.** MetalLB,
  ingress-nginx, Karpenter and the storage provisioner are cluster
  prerequisites: recorded and pinned, installed separately.

## Constraints

- **Phases 14 (148–150) and 15 (151–158) land first.** Phase 15's
  endpoint-scoping audit is a precondition of exposing anything, and phase 15
  hands this phase the Grafana Angular-panel migration (see strand D).
- Single node today: **`talos-cp`**, k8s **v1.31.4**, and it is **untainted** —
  which is why engine pods have been running on the control plane.
- **Karpenter with a Proxmox provider is installed**
  (`karpenter.proxmox.sinextra.dev`, NodePool `mi666-1`, requirements
  `arch=amd64, zone=mi666-1`, currently 0 nodes). Nodes therefore come and go,
  which makes node-local storage unsafe for anything that must survive — see
  strand A.
- MetalLB (`10.10.10.90`) and ingress-nginx (class `nginx`) are **already
  installed and must be used, not replaced**.
- Registry `registry.pve.heri.life` with the existing `registry-pve-heri-life`
  pull secret.
- **Every image the chart controls is pinned.** No `:latest`, per phase 11's scar
  (`imagePullPolicy: IfNotPresent` + a mutated tag served a stale digest).
- **Secrets never enter git.** The chart templates references; values come from
  Secrets created out of band.
- The SPA is `go:embed`-ed in the API binary — there is no separate UI
  deployable, Service or Ingress.
- Platform components request **as few resources as possible** while working
  (resolved decision 2), so the control plane stays responsive and Karpenter is
  driven by engine demand rather than by the platform.

## Approach

### A — Storage foundation, and where state is allowed to live

Install **local-path-provisioner** as the cluster's default StorageClass,
recorded as a pinned cluster prerequisite alongside MetalLB, ingress-nginx and
Karpenter (none of which exist in the repo today).

Because a local-path PV is bound to one node by affinity and Karpenter recycles
the nodes it provisions, **every stateful pod is pinned to the control-plane
node** (`nodeSelector: node-role.kubernetes.io/control-plane: ""`) — the one node
Karpenter will not consolidate. That applies to MySQL, Prometheus, **and the
object store**, whose current `hostPath` mount has the same latent flaw: if the
API pod were ever rescheduled onto a Karpenter node, engine logs would silently
split or vanish. The object store becomes a PVC on the same pinned node.

*Rejected:* network storage (NFS from Proxmox) — new infrastructure to operate
for a single-node-stateful workload; revisit if platform components ever need to
move nodes.

### B — Credential key (first task)

Rotate `HONRYU_CLUSTER_CREDENTIAL_KEY`, move it into a Secret consumed by
`secretKeyRef`. Only **BYOC** entries seal a kubeconfig (`RegisterOperator`
reads its Secret and never encrypts, `clusterapp/service.go:112-115`), so
rotation invalidates BYOC credentials only: enumerate `GET /api/clusters` for
`origin: byoc` first and re-register any. Sequenced first because the key was
exposed in plaintext and folding it in should not mean landing it last.

### C — The chart

A single chart, `deploy/chart/honryu/`, with per-component toggles:

- Deployments: `api`, **`scheduler` (new)**, `calibrator`
- **StatefulSet** `mysql` + PVC, replacing the `emptyDir` Deployment
- Services: `honryu-api` (ClusterIP 8080), `mysql` (headless)
- All five RBAC objects: `ServiceAccount honryu`,
  `Role`/`RoleBinding honryu-scheduler`,
  `ClusterRole`/`ClusterRoleBinding honryu-nodes-reader`
- `Ingress` on class `nginx`, host from values
- A shared ConfigMap for non-secret env — `cmd/api` and `cmd/scheduler` both use
  `config.Load(getenv)`, so they share one env template
- Secrets **by reference only**; `values.yaml` names existing Secrets
- `values.yaml`: hostname, pinned image tags, engine-image map, storage sizes,
  minimal resource requests, `prometheus.enabled`, `grafana.enabled`
- `NOTES.txt` printing the resulting URL

`helm lint` and `helm template` join CI (resolved decision 6), so a broken chart
fails a check rather than a deploy.

### D — Monitoring

A **minimal Prometheus** (Deployment, PVC, scrape config for
`honryu-api:8080/metrics`) plus **Grafana from the repo's own image** with its
provisioned dashboards and datasource repointed at the in-cluster Prometheus.
Includes phase 14's handover: migrate `grafana/dashboards/honryu.json`'s
`grafana-piechart-panel` to Grafana's built-in `piechart` and drop the vendored
AngularJS plugin — Angular was defaulted off in Grafana 11 and removed in 12, so
that panel cannot render on the pinned image at all.

`grafana/metrics-dashboard/` is **deleted**: an abandoned `helm create` scaffold
(`description: "A Helm chart for Kubernetes"`, `appVersion: '1.16.0'`, scaffold
`hpa.yaml`/`tests/`) whose values point at `localhost/grafana` with
`pullPolicy: Never` in a namespace (`honryu-executors`) that does not exist. The
image build inputs it sits beside — `grafana/Dockerfile`, `dashboards/`,
`datasources/`, `provisioning/`, `config.ini` — are kept.

*Rejected:* kube-prometheus-stack — an operator, CRDs, Alertmanager and
node-exporter for one scrape target, whose bundled Grafana would displace the
provisioned image this repo already builds. *Rejected:* keeping monitoring as a
second chart — the components are meaningless apart, and two releases add a
version-skew surface for no optionality that `enabled` toggles do not give.

### E — Exposure

Ingress host becomes **`honryu.pve.heri.life`**, served by ingress-nginx on the
existing MetalLB address; the DNS record is added out of band (resolved decision
1). HTTP only. The SPA appears at `/` for free. **`/metrics` is blocked at the
ingress** (resolved decision 5) — it is an operational surface, not a public one,
and Prometheus reaches it in-cluster.

### F — Endpoint-scoping audit

Phase 15's handover: for every endpoint the SPA consumes, plus `/metrics` and
`GET /api/clusters` (which serves `api_url`, `namespace`, `secret_ref`), record
what a device on the LAN or tailnet can actually reach and do under
`HONRYU_AUTH_MODE=none`. Findings only — any code-level gap is logged as a
follow-up phase, per the non-goal.

### G — Cutover, clean

`helm upgrade --install` onto a namespace whose hand-made objects have been
removed. **No data is preserved** (resolved decision 4). MySQL is changing object
kind (Deployment → StatefulSet), so it could not be adopted in place regardless.

*Rejected:* Helm adoption via `meta.helm.sh/*` annotations and
`app.kubernetes.io/managed-by` labels — it cannot cover the MySQL kind change,
and half-adopting the rest preserves exactly the ambiguous state this phase
exists to eliminate. Homelab downtime is free.

## Endpoint-scoping audit (task 161, 2026-08-18)

Run against the live port-forwarded deployment (`honryu-api:phase15`,
`HONRYU_AUTH_MODE=none`), before any hostname exists — the precondition this
task exists to satisfy. Confirmed in code first:
`internal/adapters/auth/noauth/noauth.go:37-40` — `Authenticate` takes any
`*http.Request` and **unconditionally returns a fixed
service-provider-admin account**, no branch on presence or content of
credentials, no branch on HTTP method. Then verified live: every endpoint the
SPA consumes, plus `/metrics` and `GET /api/clusters`, called with **no
`Authorization` header at all**.

| Endpoint | Status | Notes |
|---|---|---|
| `GET /api/executions/{id}` | 200 | full execution incl. load profile |
| `GET /api/executions/{id}/status` | 200 | lifecycle snapshot |
| `GET /api/executions/{id}/reports` | 200 | includes `correlation_id` |
| `GET /api/executions/{id}/trend` | 200 | |
| `GET /api/executions/{id}/error-signatures` | 200 | |
| `GET /api/runs/{id}/report` | 200 | |
| `GET /api/runs/{id}/scenarios/{id}/shards/{n}/config` | 200 | raw compiled Taurus YAML |
| `GET /api/runs/{id}/scenarios/{id}/shards/{n}/log` | 200 | raw engine stdout |
| `GET /api/clusters` | 200 | `[]` today; would carry `api_url`/`namespace`/`secret_ref` per row |
| `GET /api/campaigns/{id}` | 200 | |
| `GET /api/campaigns/{id}/verdict` | 200 | |
| `GET /api/campaigns/{id}/comparison` | 200 | |
| `GET /api/tenants/{id}/campaigns` | 200 | tenant 1's data returned with no tenant credential |
| `GET /api/tenants/{id}/reservations` | 200 | |
| `GET /metrics` | 200 | Prometheus internals |

**Every probe returned 200, none 401/403.** `POST` (e.g. `createCampaign`,
`web/src/api/campaigns.ts`) was not fired live to avoid mutating dogfood
state, but is inferred identical by the same code evidence:
`Authenticate(*http.Request)` does not branch on method, so a write carries
the same unconditional admin identity as a read.

**Plain statement:** any device that can route to the API — today the LAN
or tailnet, per phase 16's exposure plan — has unauthenticated
service-provider-admin access to every execution, report, campaign, tenant,
raw shard log/config, and the cluster registry, with **no tenant scoping
applied at all**. This is the resolved stance from phase 15 (parent spec
open question 2): the SPA enforces nothing of its own and cannot leak what
the API will not serve — but the API currently serves everything to everyone
under `Mode: "none"`. Bounded, for phase 16, by "LAN + Tailscale only,
`HONRYU_AUTH_MODE=none` stays" (Non-goals) — an explicit, accepted-for-now
perimeter argument, not a fixed one. **Logged as a follow-up phase, not
fixed here**, per this task's own scope: closing it means either OIDC (the
already-implemented seam, `config.go:351`) or per-tenant scoping logic that
does not exist today, both real design work.

## Resolved decisions (2026-08-17, binding)

1. **Hostname `honryu.pve.heri.life`**; the DNS record is added by the operator,
   following the same mechanism as `httpbin.pve.heri.life`.
2. **Minimal resource requests** on every platform component — enough to work,
   no more — so Karpenter is driven by engine fan-out rather than by the control
   plane. **Caveat established at plan time:** the chart cannot make Karpenter
   fire. `resourceRequirements` returns an empty `ResourceRequirements{}` when an
   execution declares no `CPU`/`Memory`
   (`internal/adapters/scheduler/k8s/k8s.go:448-460`, fed from
   `internal/domain/execution/execution.go:71-76`), and Karpenter only provisions
   for *Pending* pods — so a default execution's engines always fit on the
   untainted `talos-cp` and `mi666-1` stays at zero. Minimal platform requests
   merely leave more room for that to happen. Executions used for verification
   must therefore declare resources, which is an operator responsibility to
   document, not a chart setting.
3. **One chart.** `grafana/metrics-dashboard/` is superseded and deleted;
   Grafana and Prometheus become toggleable components of the Honryu chart.
4. **Start clean.** No dump, no restore; the discarded data is dogfood fixtures.
5. **`/metrics` blocked at the ingress.**
6. **`helm lint` / `helm template` join CI.**

## Acceptance criteria

1. `helm upgrade --install honryu deploy/chart/honryu -f <homelab values>`
   reproduces the platform from scratch on a cluster meeting the documented
   prerequisites; `helm lint` and a server-side dry-run are clean; **no
   `:latest`** on any image the chart controls.
2. MetalLB, ingress-nginx, Karpenter and local-path-provisioner are recorded
   in-repo with pinned versions and install commands — the cluster is
   reproducible, not only the app — and are **not** installed by the chart.
3. A default StorageClass exists; MySQL is a StatefulSet with a PVC and the
   object store is a PVC; both are pinned to the control-plane node.
   **Deleting the MySQL pod preserves reports** — the direct demonstration of
   parent AC `:98`, which `emptyDir` made impossible.
4. **A scheduled execution fires unattended and produces a report with no human
   trigger** — parent AC `:178`, never once demonstrated in a deployment,
   because the scheduler was never deployed.
5. Prometheus scrapes `/metrics` with persistent storage; Grafana renders a
   provisioned dashboard against **live data from a real run**, with the
   AngularJS panel replaced (parent AC `:196`).
6. `HONRYU_CLUSTER_CREDENTIAL_KEY` is a `secretKeyRef`, rotated, and no secret
   value appears in the chart or anywhere in git.
7. The platform answers at `honryu.pve.heri.life` over LAN and Tailscale, the
   SPA loads at `/`, `/metrics` returns 404/403 through the ingress while
   Prometheus still scrapes it in-cluster, and a full deploy→trigger→report
   cycle is driven **through the UI at that hostname**.
8. `helm lint` and `helm template` run in CI and fail the build on a broken
   chart.
9. The endpoint-scoping audit is recorded as a table; gaps logged as follow-ups.
10. No application code changed; findings appended in the phases 7/10–13 format.

## Open questions — resolved at plan time (2026-08-17)

1. **Engine-pod placement toward Karpenter nodes: needs app code, so it becomes a
   follow-up phase.** The k8s scheduler adapter sets only
   `Resources: resourceRequirements(spec)` on the engine pod spec
   (`k8s.go:323`) — there is no `NodeSelector`, `Affinity` or `Tolerations`
   anywhere in it. Engines therefore cannot be steered onto `mi666-1`. Karpenter
   still absorbs *overflow* without any code change, because it provisions for
   Pending pods (subject to resolved decision 2's caveat), so this is an
   optimisation rather than a blocker. Logged as a follow-up in task 170.
2. **Prometheus retention and PVC size: small and short, from values** (task
   166). The parent spec treats Prometheus as the live/short-retention surface
   with durable summaries in `ReportStore` (`:92`, `:98`), so a large TSDB would
   contradict the design rather than serve it.

## Additional finding at plan time (out of scope, logged as a follow-up)

**Honryu's node-pool view is wrong on this cluster.** `NodePools()`
(`k8s.go:635-647`) groups nodes by `s.poolLabel`, which falls back to
`defaultPoolLabel = "cloud.google.com/gke-nodepool"` (`k8s.go:64`) — a GKE label
no node here carries — and `PoolLabel` has **no environment variable anywhere in
`cmd/`** (only `factory.go:167` passes the struct field through). So the API
reports a single `"default"` pool while the real Karpenter pool is `mi666-1`
(labelled `karpenter.sh/nodepool`). Fixing it is a config knob plus wiring, i.e.
app code, which this phase excludes; recorded in task 170's findings.

## Task 161 deliverable — endpoint-scoping audit (live, port-forwarded, 2026-08-18)

Method: every route below called against the running `honryu-api` via
port-forward with **no credentials** (no Authorization header) under the
deployed `HONRYU_AUTH_MODE=none`. `authenticate` (router.go:248) is a
pass-through in that mode, so these are the true LAN/tailnet answers. SPA
surface taken from `web/src/api/*.ts` production modules (13 fetch calls +
the SSE stream), plus `/metrics` per the task.

| Method & path (SPA consumes) | No-cred status | Body shape | Tenant scoping |
|---|---|---|---|
| GET /api/clusters | 200 | `Cluster[]`: `name, api_url, ingest_url, sidecar_image, namespace, secret_ref, origin, created_by, created_time` (live: `[]`) | **None — global registry.** Unauthenticated caller receives `api_url`, `namespace`, `secret_ref` for every cluster the moment one is registered |
| GET /api/tenants/{id}/campaigns | 200 | `Campaign[]` (`id, name, tenant_id, window_*`) | Path-param filter only — data is scoped to the requested tenant, but **any anonymous caller may pass any tenant id** (no ACL) |
| POST /api/tenants/{id}/campaigns | 400 (validation; reachable & writable) | `{"message": "invalid window_start …"}` | Same — anonymous create against any tenant once the body validates |
| GET /api/campaigns/{id} | 200 | `Campaign` object incl. `tenant_id` | None — global id, no tenant check in no-auth mode |
| GET /api/campaigns/{id}/comparison | 200 | `{campaign_id, has_baseline, services[]}` | None (derived from the campaign's executions) |
| GET /api/campaigns/{id}/verdict | 200 | `{campaign_id, services[{project_id, execution_id, has_report, outcome…}]}` | None |
| GET /api/executions/{id} | 200 | `ExecutionInfo` (`id, name, project_id, …`) | None — global id |
| GET /api/executions/{id}/status | 200 | `{phase, pool_size, status[]}` | None |
| GET /api/executions/{id}/reports?limit=5 | 200 | `Report[]` | None |
| GET /api/executions/{id}/trend?limit=5 | 200 | `{execution_id, points[]}` | None |
| GET /api/executions/{id}/error-signatures | 200 | `{execution_id, grouped_by, groups[]}` | None |
| GET /api/executions/{id}/stream (SSE) | 200 `text/event-stream` | event stream (idle: no events) | None |
| GET /api/runs/{id}/report | 200 | full `Report` (all scenario samples) | None — global id |
| GET /api/tenants/{id}/reservations | 200 | `Reservation[]` | Path-param filter only, no ACL |
| GET /metrics (task-mandated) | 200 `text/plain` | Prometheus exposition (go_*, process_*, honryu_*); **not under `/api/`, so not even RBAC mode would gate it** | None |

Beyond the SPA surface, probed for the exposure sentence: GET
`/api/admin/executions` → 200 `[]`, GET `/api/admin/nodes` → 200 (pool view),
POST `/api/admin/abort` → 400 (reachable; a valid body **aborts running
executions**). `admin_handlers.go:54-58` documents this as intended: "in
no-auth mode the operator has full access, matching every other admin route
today."

**One plain sentence:** a device on the LAN or tailnet can, without any
credentials, read every tenant's campaigns, executions, reports and the
cluster registry (including `api_url`/`namespace`/`secret_ref`), create
campaigns, stream live metrics, and fire the admin kill-switch — i.e. under
`HONRYU_AUTH_MODE=none` the API must never be exposed beyond a trusted
network, which is precisely why this phase front-loads the audit as a
precondition (phase 15 resolved decision 2).

**Code-level gaps logged as follow-up (per spec Non-goals, not fixed here):**
(1) `/metrics` unauthenticated even under RBAC; (2) no ACL on
tenant-id-in-path routes once auth is enabled — need per-route authorize
checks; (3) `/api/clusters` serves infra coordinates to any authenticated
caller regardless of role — candidate for admin-only. All recorded for task
170's findings.

## Live verification findings (task 170, 2026-08-18)

Proves the three parent-spec ACs that have never been demonstrable in a
deployment (`:98`, `:178`, `:196`), each as a separate live observation, plus
a genuine defect found and fixed mid-task.

### Ingress drift-and-restore, before verification began

Before any of the below, `honryu.pve.heri.life` was found routing (via the
operator's haproxy front end, fixed live during this task) to a **regressed**
Ingress: `kubectl.kubernetes.io/last-applied-configuration` showed a manual
`kubectl apply` had replaced the chart's two-path Ingress (task 165's
`/metrics` block + `/` route) with a plain single-path version — `/metrics`
returned `200` again, live, through the real hostname. Not caused by this
session; the Helm ownership annotations (`meta.helm.sh/release-name: honryu`)
survived the manual apply untouched, confirming it was an out-of-band edit
against the same object, not a fork. Confirmed with the user before touching
it (their own infra work was in progress); `helm upgrade --install` restored
it in one step. Re-verified over the **real hostname this time** (not the
Host-header bypass task 169 used): `/` → 200, `/metrics` → 503,
`/healthz` → `{"status":"ok"}` — a stronger proof than task 169 had.

### (a) Durability — parent AC `:98`

A full deploy→trigger→report cycle driven through the same API endpoints the
SPA itself calls (project → scenario → declarative `requests:` fragment
against `https://httpbin.pve.heri.life/headers` → execution → load profile →
deploy → trigger), all via `https://honryu.pve.heri.life`: run 1 finalized
`outcome: passed`, 85,302 samples, 0% errors, correlation id present.
`kubectl delete pod honryu-mysql-0`, waited for it to return Ready, then
`GET /api/runs/1/report` immediately — **byte-identical response**, no
manual intervention beyond the delete itself (the api pod's own DB
connection self-healed). The direct demonstration `emptyDir` made
impossible.

### (b) Unattended scheduling — parent AC `:178`, and the phase's most severe live finding

**First live attempt failed**, and not narrowly: `cmd/scheduler`'s `fireOnce`
called `Deploy` then `Trigger` back-to-back with **no wait at all**.
`Deploy` returns as soon as the StatefulSet is *accepted*, not when the pod
is *ready*. The scheduler log showed exactly one attempt:
`error="run: engines are not deployed"`, no retry, occurrence permanently
consumed (one-shot). Proven **not** to be a rare race: both the engine and
sidecar images were already cached on the node (confirmed via `kubectl get
events` — zero pull time) and it still failed, because two sequential Go
calls will essentially always outrun pod scheduling + container start.
`calibrationapp.triggerWhenReady` (phase 7) and the HTTP trigger handler
(phase 11 task 124) both already carry this exact bounded wait;
`cmd/scheduler` never got it, almost certainly because `honryu-scheduler`
had never been deployed anywhere before this phase — nothing had ever hit
the race to notice it needed one.

**Fixed live** (commit `e00878b`): extracted `triggerWhenReady` mirroring the
existing two implementations exactly (retry only `ErrNotDeployed`/
`ErrEnginesNotReady`, 2s poll / 2m timeout, the same constants used
elsewhere), TDD'd against the fake scheduler's existing `NotReadyCalls` knob
(built for phase 11 task 127, reused here unmodified) with 3 new tests
proving retry-then-succeed, timeout-rather-than-forever, and
non-readiness-errors-return-immediately — all passing in 30ms, zero real
sleeps, zero shared global state (parameterized poll/timeout rather than a
fake clock, safe under the suite's `t.Parallel()`). Full repo build/vet/test
green, no regressions. Rebuilt and pushed as `honryu-scheduler:phase16b` (a
new tag, not a mutated `phase16`, per the phase-11 pinning doctrine), rolled
out via `helm upgrade`.

**Re-verified live, cleanly, on a fresh execution (3) that had never been
deployed before** — avoiding the ambiguity of execution 2's history (its
engine pod's one-and-only `bzt` run had already completed **before** the
first, unfixed attempt even tried to trigger it — task 23c's "pods run bzt
immediately" behavior colliding with my own test sequencing, not a second
defect; stopped and left as a recorded artifact of the process, not
retried). Scheduler log: `firing due occurrence` → 2 second gap (**one poll
interval — the fix retrying live, not succeeding by luck**) →
`fired due occurrence`. No `/deploy` or `/trigger` call was made by
anything other than `cmd/scheduler` itself. Report: `outcome: passed`,
86,313 samples, 0% errors, correlation id present, run_id 3.

**A fourth follow-up gap, found while setting up this test:** there is
**no API path to set an execution's `cpu`/`memory`** for a normal
(`KindNormal`) execution — `createExecution`
(`internal/adapters/httpapi/execution_handlers.go:63-84`) accepts only
`name`/`project_id`/`engine`/`cluster`, and migration `0035`'s own comment
confirms this is deliberate: "only a CalibrateEngine execution ever pins
them." Task 170's own instruction to "declare CPU/Memory" on the
verification execution could only be satisfied with a **direct SQL UPDATE**
on `execution.cpu`/`execution.memory` — a real operator has no way to do
this today outside calibration.

### Karpenter observation

Execution 2's engine pod carried the SQL-set `500m`/`512Mi` request
(confirmed via `kubectl get pod ... -o jsonpath='{.spec.containers[?(@.name
=="engine")].resources}'` — the mechanism threads correctly end to end) but
scheduled onto `talos-cp` anyway; `kubectl get nodepool mi666-1` showed
`nodes: 0` throughout. **Expected, not a defect**: the control-plane node is
untainted with ample headroom for one small pod, so kube-scheduler bin-packed
it locally with zero Pending pressure — Karpenter only activates when the
default scheduler cannot find a fit. A single 500m pod was never going to
produce that pressure. Demonstrating real Karpenter scale-up would need
either many concurrent/larger executions exhausting the node, or the
already-identified (task 170's own spec-level follow-up) engine-pod
placement/affinity capability, which does not exist in the adapter today.

### (c) Live dashboards — parent AC `:196`

Not independently screenshotted — this image has no `grafana-image-renderer`
plugin, so a true visual render proof is outside what this session can
produce. Two things **are** proven: (1) task 167's structural proof stands
(the migrated `piechart` panels load with no plugin-not-found error); (2)
queried Prometheus directly for the **exact metric and label set** the
dashboard's panels query —
`honryu_status_counter{run_id="3", status="200"}` — and got a real,
populated result. Together this is the strongest available evidence the
panels would render real values if viewed, short of an actual screenshot.

### UI-driven cycle and Tailscale reachability

Per an explicit split agreed with the user (no browser automation or
Tailscale client available in this session — checked via `ToolSearch`, and
this sandbox is LAN-only at `10.10.10.39`): the API-level cycle above proves
the backing functionality; **the user separately confirms the UI-driven
cycle and Tailscale reachability themselves**, once haproxy routing (fixed
during this task) is live.

### `/metrics` block, re-confirmed post-fix

`https://honryu.pve.heri.life/metrics` → 503; Prometheus's own scrape
(`up{job="honryu-api"} == 1`, confirmed via direct query) is unaffected,
since it targets the Service in-cluster, never through the Ingress.

### Follow-up gaps for a future phase (not fixed here, per spec Non-goals)

1. **Engine-pod placement/affinity** — no `NodeSelector`/`Affinity`/
   `Tolerations` in `internal/adapters/scheduler/k8s/k8s.go:323`; engines
   cannot be steered toward Karpenter-provisioned nodes.
2. **Hardcoded GKE pool label** — `defaultPoolLabel =
   "cloud.google.com/gke-nodepool"` (`k8s.go:64`), not env-configurable;
   `NodePools()` reports `"default"` instead of the real `mi666-1`.
3. **No API path to set a normal execution's CPU/Memory** — confirmed live
   in this task; only `CalibrateEngine` executions can pin pod resources
   today (migration `0035`'s own comment documents this as deliberate, but
   it means no operator can request bigger engine pods for an ordinary
   load test without a direct database write).
4. **`fireOnce`'s readiness fix (this task) has no retry/backoff beyond the
   one bounded wait** — a `Deploy` failure, or a `Trigger` failure that
   *isn't* the readiness pair, still logs and gives up with the occurrence
   already consumed. Matches `fireOnce`'s own remaining doc comment
   ("out of this task's scope"); worth a firing-failure retry/backoff policy
   in its own right, separate from the readiness race this task closed.

### Cleanup

All three test executions purged (`execution purged` ×3, engine pods
terminated, confirmed via `kubectl get pods`); both ad hoc port-forwards
(api `18090`, prometheus `19090`) stopped by PID (`fuser -k` did not
reliably kill either, matching task 157's same finding). Left in place,
matching every prior phase's live-verification doctrine (`p10-live`,
`p11-live`, `phase12-dogfood`, `phase13-dogfood`): projects `phase16-live`/
`phase16-sched`, tenant `phase16-live-tenant`, scenarios 1/2, executions
1/2/3, schedules 1/2/3. The concurrent session's own `18080` port-forward
(PID `1073315`) was never touched.
