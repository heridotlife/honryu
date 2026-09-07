# Phase 16 — Homelab deployment — Tasks

Continues the roadmap's global task numbering from Phase 15's last task (158).
**Spec:** `spec.md` · **Plan:** `plan.md`

**Phase-level precondition:** phases 14 (149–150) and 15 (151–158) are merged to
develop. Phase 15 hands this phase the endpoint-scoping audit (task 161); phase
14 hands it the Grafana AngularJS panel migration (task 167).

## Group A — foundations, before anything is exposed

### 159. Record the cluster prerequisites and install a default StorageClass
- **Files:** `deploy/cluster/README.md` (new), `deploy/cluster/local-path-provisioner.yaml` (pinned manifest, new)
- **Criteria:** the four cluster prerequisites are recorded with **pinned
  versions and the exact install commands** — MetalLB (address pool
  `10.10.10.90`), ingress-nginx (class `nginx`), Karpenter + the Proxmox provider
  (`karpenter.proxmox.sinextra.dev`, NodePool `mi666-1`), and
  local-path-provisioner — so the cluster is reproducible, not only the app.
  Three already exist and are documented as-found (version captured from the
  live cluster, not guessed); local-path-provisioner is **installed** by this
  task as the cluster's **default** StorageClass. Verified: `kubectl get sc`
  shows exactly one default; a scratch PVC binds and is deleted. The Honryu
  chart must not install any of these (spec Non-goals).
- **Satisfies:** spec Approach strand A; AC2
- **Depends on:** —

### 160. Rotate the credential-encryption key into a Secret
- **Files:** none in-repo (live-cluster operation); notes captured for task 170's findings
- **Criteria:** enumerate `GET /api/clusters` for `origin: byoc` **first** — only
  BYOC entries seal a kubeconfig (`RegisterOperator` reads and never encrypts,
  `internal/app/clusterapp/service.go:112-115`), so only those are invalidated by
  a rotation. Generate a fresh 32-byte key, store it in a Secret, and patch the
  live `honryu-api` Deployment to consume it via `secretKeyRef` — matching how
  `HONRYU_DB_DSN` and `HONRYU_INGEST_TOKEN` already work, replacing the plaintext
  `value:`. Verified: the API starts, `/healthz` passes, operator-origin clusters
  still resolve, and any BYOC entry is re-registered (or deliberately removed,
  recorded either way). The old key value appears nowhere afterwards.
- **Satisfies:** spec Approach strand B; AC6
- **Depends on:** —
- **Done (live, 2026-08-18, for task 170 findings):** `GET /api/clusters`
  returned `[]` pre-rotation — **0 BYOC entries, 0 operator-origin**; nothing
  to re-register or remove (rotation invalidated no sealed kubeconfigs; MySQL
  holds none). Rotation path confirmed single-key with no dual-read:
  `secretbox.NewFromHex` (`cmd/api/main.go:312`) consumes a 64-char hex env
  (`HONRYU_CLUSTER_CREDENTIAL_KEY`), so a swap invalidates all sealed rows —
  the pre-check is mandatory, not ceremony. New 32-byte key (hex literal) in
  Secret `honryu-cluster-credential-key`, data key `credential-key`; env[13]
  patched via JSON patch (add `secretKeyRef`, remove plaintext `value`) — same
  shape as `HONRYU_DB_DSN`/`HONRYU_INGEST_TOKEN`. Verified: rollout clean,
  `/healthz` 200, `/api/clusters` `[]` post-rotation. Old-key scrub: 13
  occurrences pre-scrub (1 deployment env + 10 superseded ReplicaSets carrying
  plaintext revision history; no last-applied annotation, no secrets); deleted
  all `replicas=0` `honryu-api-*` RS, final namespace-wide grep
  (deploy,rs,po,secret,events) → **0 occurrences**. Operational notes for
  task 170: the Deployment is **not helm-managed** (no managed-by labels) —
  hand-patched live; also `pkill -f "port-forward …"` matches the invoking
  shell's argv and kills the session — kill by port (`fuser -k`) instead.

### 161. Audit endpoint scoping before exposure
- **Files:** `.cortex/2026-08-17-phase16-homelab-deployment/spec.md` (audit table appended)
- **Criteria:** against the **currently port-forwarded** deployment (i.e. before
  any hostname exists — this is what makes it a precondition), record for every
  endpoint the SPA consumes, plus `/metrics` and `GET /api/clusters`: the HTTP
  status and body shape when called with no credentials under
  `HONRYU_AUTH_MODE=none`, and whether tenant scoping is applied. `GET
  /api/clusters` is called out explicitly because it serves `api_url`,
  `namespace` and `secret_ref`. Deliverable is a table plus one plain sentence
  stating what a device on the LAN or tailnet can do. Any code-level gap is
  **logged as a follow-up phase, not fixed** (spec Non-goals).
- **Satisfies:** spec Approach strand F; AC9; phase 15 resolved decision 2
- **Depends on:** —
- **Done (live, 2026-08-18):** all 15 endpoints probed with no
  `Authorization` header, all returned **200**, none 401/403 — confirmed
  against `noauth.Provider.Authenticate` (`internal/adapters/auth/noauth/
  noauth.go:37-40`), which unconditionally returns a fixed
  service-provider-admin account for any request. Full table + plain-language
  statement appended to `spec.md`. No code changes; the gap is logged as a
  follow-up phase per this task's own scope.

## Group B — the chart, proven before anything is destroyed

Every task in this group ends with `helm lint`, `helm template`, and
`kubectl apply --dry-run=server` clean. Nothing is applied for real until 169.

### 162. Scaffold the chart with the API, calibrator, services and RBAC
- **Files:** `deploy/chart/honryu/{Chart.yaml,values.yaml,.helmignore}`, `deploy/chart/honryu/templates/{_helpers.tpl,configmap.yaml,api-deployment.yaml,calibrator-deployment.yaml,api-service.yaml,rbac.yaml,serviceaccount.yaml,NOTES.txt}`, `deploy/chart/honryu-homelab-values.yaml`
- **Criteria:** a real chart, not a `helm create` scaffold — no placeholder
  `description`, no unused `hpa.yaml`/`tests/`. Templates reproduce the live
  objects: `honryu-api` and `honryu-calibrator` Deployments, the `honryu-api`
  Service (ClusterIP 8080), and all five RBAC objects (`ServiceAccount honryu`,
  `Role`/`RoleBinding honryu-scheduler`,
  `ClusterRole`/`ClusterRoleBinding honryu-nodes-reader`). A **shared ConfigMap**
  carries non-secret env, consumed by both the API and (task 164) the scheduler,
  since `cmd/api` and `cmd/scheduler` use the same `config.Load`
  (`cmd/scheduler/main.go:73`). Secrets are **referenced by name only** — the
  chart contains no secret value and `git grep` proves it. The object store moves
  from `hostPath` to a **PVC pinned with
  `nodeSelector: node-role.kubernetes.io/control-plane: ""`** (a Karpenter node
  would otherwise take the logs with it). **Every image tag is pinned** — no
  `:latest`, so `honryu-calibrator` and the sidecar get real tags. Minimal
  resource requests per spec decision 2. `NOTES.txt` prints the URL.
- **Satisfies:** spec Approach strand C; AC1, AC3 (object store half)
- **Depends on:** 159
- **Done (2026-08-18):** chart at `deploy/chart/honryu/` — 10 objects
  (ServiceAccount, ConfigMap, PVC, ClusterRole+Binding, Role+Binding, Service,
  2 Deployments). `helm lint` clean; `helm template` renders all 10; verified
  in the render: zero `:latest`, `HONRYU_ENGINE_IMAGES` carries both
  jmeter+k6 on **both** api and calibrator (fixes a live drift — the
  hand-made `honryu-calibrator` Deployment had silently dropped k6 from its
  list), 5 `secretKeyRef`s and zero literal secret values (`git grep`-clean),
  real (non-empty) resource requests/limits on both Deployments (the live
  originals set none at all), `node-role.kubernetes.io/control-plane`
  nodeSelector on both.
  **Unplanned but necessary sub-step:** `honryu-calibrator` and
  `honryu-sidecar` had never been retagged past `:latest`, and
  `honryu-scheduler` had never been built at all — task 162's own "every
  image tag is pinned" criterion cannot be met by reference alone. Built and
  pushed all three as `:phase16` from current `feat/honryu` HEAD
  (`registry.pve.heri.life/honryu/honryu-{calibrator,sidecar,scheduler}:phase16`);
  `honryu-api` stays at its already-pinned `:phase15` (task 162 didn't ask to
  rebuild it, and it is already a real tag, not `:latest` — though it now
  predates the go_modules bumps from phase-14 task 149, a minor vintage
  mismatch left unfixed since fixing it isn't this task's job).
  **Dry-run finding, expected and validating:** `kubectl apply
  --dry-run=server` against the real `honryu` namespace applied 8/10 objects
  clean ("created"/"configured") but **both Deployments failed**:
  `spec.selector: field is immutable` — the live hand-made
  `honryu-api`/`honryu-calibrator` Deployments select on a plain `app: ...`
  label, the chart's on the standard `app.kubernetes.io/{name,instance,
  component}` triple, and Kubernetes forbids changing a Deployment's selector
  in place. This is **not a chart defect**: re-rendered under a
  non-colliding release name (`honryu-schema-check`), the identical manifests
  applied with **zero errors across all 10 objects** — proving the YAML
  itself is schema-valid and the failure is purely the pre-existing objects'
  incompatible selector. It is exactly why the plan rejected Helm adoption
  (`meta.helm.sh/*` annotations) in favor of a real cutover (task 169: delete
  the hand-made objects, then `helm upgrade --install`) — this dry-run is
  live proof that adoption-in-place would have hit this same wall.

### 163. Add MySQL as a StatefulSet with a pinned PVC
- **Files:** `deploy/chart/honryu/templates/{mysql-statefulset.yaml,mysql-service.yaml}`, `values.yaml`
- **Criteria:** MySQL becomes a **StatefulSet** with a `volumeClaimTemplate`
  (size from values) and a headless Service, replacing the `emptyDir`
  Deployment — the change that makes parent AC `:98` demonstrable at all.
  Pinned to the control-plane node by `nodeSelector`, because a local-path PV is
  bound to its node by affinity and Karpenter recycles the nodes it provisions.
  DSN still consumed from the existing `mysql-credentials` Secret. Rendered
  output asserts the nodeSelector and the claim template; server-side dry-run
  clean.
- **Satisfies:** spec Approach strand A; AC3
- **Depends on:** 162
- **Done (2026-08-18):** `mysql-statefulset.yaml` + `mysql-service.yaml`.
  Live cluster check first: the existing `mysql-credentials` Secret's `dsn`
  resolves the bare hostname `mysql` — so the headless Service is named
  literally `mysql`, not fullname-prefixed, deliberately preserving DNS
  compatibility so cutover needs no secret update. `serviceName: mysql`,
  `volumeClaimTemplate` on `local-path`, `nodeSelector` pinning to the
  control-plane node, `MYSQL_ROOT_PASSWORD`/`MYSQL_PASSWORD` via the same
  `mysql-credentials` Secret's `root-password`/`password` keys the live
  Deployment already used.
  `helm lint` clean; `helm template` renders 12 objects (10 from 162 + mysql
  Service + StatefulSet); rendered output confirmed: `nodeSelector`,
  `serviceName: mysql`, `clusterIP: None`, and the claim template on
  `local-path`.
  **Dry-run, same class of expected finding as 162:** under the real release
  name, the `mysql` Service fails —
  `spec.clusterIPs[0]: Invalid value: []string{"None"}: may not change once
  set` — because the live `mysql` Service is a normal ClusterIP Service and
  `clusterIP` is immutable, exactly like the Deployments' `spec.selector`.
  The StatefulSet itself (a new name, `honryu-mysql`, no live collision)
  applied clean. Re-rendered under the non-colliding `honryu-schema-check`
  release name: **only** the `mysql` Service still failed, for the one
  reason it necessarily must — it is deliberately the one object *not*
  fullname-prefixed, so it collides by name under any release name. Every
  other object, including the StatefulSet, applied clean. The error text
  itself ("may not change once set") is an immutability conflict against an
  existing object, not a schema defect — confirms the same cutover-is-
  required conclusion as task 162, one object wider.

### 164. Add the scheduler Deployment
- **Files:** `deploy/chart/honryu/templates/scheduler-deployment.yaml`, `values.yaml`
- **Criteria:** a `honryu-scheduler` Deployment — **the component that has never
  been deployed** — running `cmd/scheduler`, consuming task 162's shared
  ConfigMap and the same Secrets, on the `honryu` ServiceAccount (it deploys
  engine pods, so it needs the same RBAC). Pinned image, minimal requests, a
  liveness/readiness approach appropriate to a loop process (documented if it has
  no HTTP endpoint). Rendered and dry-run clean.
- **Satisfies:** spec Problem item 2; AC4 (deployment half)
- **Depends on:** 162
- **Done (2026-08-18):** `scheduler-deployment.yaml`, plus a `scheduler:`
  values block (replicas 1 — deliberately, per spec Non-goals, even though
  `cmd/scheduler`'s own package doc says >1 is safe: row-locked claims, no
  leader election). Runs `/honryu-scheduler`, consuming task 162's shared
  ConfigMap, on the `honryu` ServiceAccount (needs the same RBAC as api — it
  deploys/tears down engine pods itself when a scheduled occurrence fires).
  **Traced two things in code before wiring, rather than copying the live
  Deployments blind:** (a) `cmd/scheduler/main.go` calls
  `lifecycleapp.NewService(repo, sched, store, ...)` and `lifecycleapp`
  genuinely uploads/downloads shard configs and run logs through that store
  (`service.go:690,710,714,761,836`) — so scheduler needs the **same**
  object-store PVC as api/calibrator, not its own, and is pinned to the same
  control-plane node to co-locate with that ReadWriteOnce volume; (b)
  `cfg.Cluster.IngestToken` is read only by `cmd/api`
  (`cmd/api/main.go:168`, wired into the ingest-auth check) — grepped
  `cmd/scheduler/main.go` and `cmd/calibrator/main.go`, neither references
  it. The live `honryu-calibrator` Deployment sets `HONRYU_INGEST_TOKEN`
  anyway; that's unused cruft, not a requirement, and the scheduler
  Deployment deliberately omits it, documented inline.
  No liveness/readiness probe, documented inline per the task's own
  allowance: `cmd/scheduler` has no HTTP/TCP listener and no Service in
  front of it; a crash already exits non-zero and the kubelet restarts the
  container, and detecting hung-but-not-crashed would need a heartbeat this
  binary doesn't emit — out of scope (no application code changes).
  `helm lint` clean; `helm template` renders 13 objects. Dry-run: the
  scheduler Deployment itself applies **clean** under both the real release
  name and the non-colliding check-name (a genuinely new object, nothing
  live to collide with) — the only failure in either run is the
  already-documented `mysql` Service name collision from task 163.

### 165. Add the Ingress with the hostname, and block `/metrics`
- **Files:** `deploy/chart/honryu/templates/ingress.yaml`, `values.yaml`, `deploy/chart/honryu-homelab-values.yaml`
- **Criteria:** Ingress on class `nginx` with the host from values
  (`honryu.pve.heri.life` in the homelab values), replacing
  `api.honryu.local`. `/metrics` is **blocked at the ingress** (spec decision 5)
  while remaining reachable in-cluster for Prometheus — these are two separate
  assertions, and the task is not done until both are expressed: the ingress
  denies the path, and no `NetworkPolicy` or Service change prevents an
  in-cluster scrape. TLS deliberately absent (spec Non-goals), recorded in a
  template comment so its absence reads as a decision rather than an oversight.
- **Satisfies:** spec Approach strand E; AC7
- **Depends on:** 162
- **Done (2026-08-18):** `ingress.yaml`, `metrics-block-service.yaml`, plus
  `ingress:` values (`enabled`/`className`/`host`, off by default, enabled
  with `host: honryu.pve.heri.life` in the homelab values).
  **Checked live before choosing a design, not assumed:** ingress-nginx has
  defaulted `allow-snippet-annotations` to `false` since v1.9 (a deliberate
  hardening against snippet-injection); this controller is **v1.12.1**
  (`kubectl -n ingress-nginx get pods -o jsonpath=...image`) and its
  ConfigMap (`kubectl -n ingress-nginx get cm ingress-nginx-controller
  -o jsonpath='{.data}'`) sets **no override** — confirmed empty. A
  `server-snippet`-based `/metrics` block would therefore silently do
  nothing. Used a selector-less "black hole" Service instead: `/metrics`
  (exact path) routes to `honryu-metrics-block`, which has no selector and
  therefore no Endpoints, so ingress-nginx 503s it — a native Kubernetes
  primitive, no controller feature required. `/` (prefix) routes to the real
  api Service unchanged. Prometheus (task 166) scrapes that Service
  directly, in-cluster, never through this Ingress — no `NetworkPolicy`
  needed, satisfying the criteria's second assertion by construction. TLS
  absence documented inline as the deliberate Non-goal it is.
  `helm lint` clean; `helm template` renders 15 objects (13 from 162-164 +
  Ingress + block Service); host, both path rules, and the absent TLS block
  all confirmed in the render.
  **Dry-run finding, and a genuine contrast with 162/163:** both new
  objects apply clean under the real release name; the **only** errors are
  the three already-documented ones (api/calibrator selectors, mysql
  clusterIP) — no new collisions. Notably, `ingress.networking.k8s.io/
  honryu-api configured (server dry run)` **succeeds in place**, unlike the
  Deployments/Service: `host`/`rules`/`paths` are mutable Ingress fields, so
  the live `api.honryu.local` Ingress can be updated by a normal
  `helm upgrade` without deletion — not everything needs task 169's
  delete-first treatment, only the specifically-immutable-field objects do.
  Re-confirmed clean (both new objects `created`, zero new errors) under the
  non-colliding check-name.
  **The functional claim — a real 503 through the live ingress — is
  deliberately not tested here.** This group's own rule is "nothing applied
  for real until 169"; the actual behavior is task 170's job, which already
  names it explicitly ("`/metrics` refused through the ingress while
  Prometheus still scrapes it in-cluster").

### 166. Add the Prometheus component
- **Files:** `deploy/chart/honryu/templates/prometheus-{deployment,service,configmap,pvc}.yaml`, `values.yaml`
- **Criteria:** a minimal Prometheus behind a `prometheus.enabled` toggle:
  Deployment, Service, scrape config targeting `honryu-api:8080/metrics`
  (a real route — `internal/adapters/httpapi/router.go:128`), and a **PVC pinned
  to the control-plane node**. Retention window and volume size come from values
  and are deliberately small: the parent spec treats Prometheus as live/
  short-retention with durable summaries in `ReportStore` (`:92`, `:98`), so a
  large TSDB would contradict the design. No operator, no CRDs (spec rejects
  kube-prometheus-stack). Rendered and dry-run clean.
- **Satisfies:** spec Approach strand D; AC5 (scrape half)
- **Depends on:** 162, 159
- **Done (2026-08-18):** `prometheus-{deployment,service,configmap,pvc}.yaml`
  + a `prometheus:` values block (`enabled: false` by default, on with
  `size: 2Gi` in the homelab values). Image `prom/prometheus:v3.0.1` —
  existence checked live with `docker manifest inspect` before pinning it,
  since `helm template`/dry-run never pulls an image and a typo'd tag would
  only surface as a live `ImagePullBackOff`. Scrape config targets
  `{{ fullname }}-api.{{ namespace }}.svc.cluster.local:8080` (confirmed in
  the render: `honryu-api.honryu.svc.cluster.local:8080`) — the real
  `/metrics` route, reached via the api Service's in-cluster DNS, never
  through the Ingress (which blocks exactly that path, task 165). Retention
  `3d` and size `2Gi` both from values, deliberately small per the parent
  spec's live/short-retention design. PVC pinned to the control-plane node,
  same as every other stateful mount in this chart. No operator, no CRDs —
  a plain Deployment.
  `helm lint` clean; `helm template` renders 19 objects (15 from 162-165 + 4
  Prometheus objects); render confirms the scrape target, `3d` retention,
  the pinned tag, and the nodeSelector (now on 5 components: api,
  calibrator, scheduler, mysql, prometheus).
  Dry-run: all 4 new objects apply clean under both the real release name
  and the non-colliding check-name — no new collisions, only the same three
  already-documented ones (api/calibrator selectors, mysql clusterIP).

### 167. Add the Grafana component, migrate the AngularJS panel, delete the scaffold
- **Files:** `deploy/chart/honryu/templates/grafana-{deployment,service,pvc}.yaml`, `values.yaml`, `grafana/dashboards/honryu.json`, `grafana/datasources/local.yml`, `grafana/metrics-dashboard/` (**deleted**)
- **Criteria:** Grafana behind a `grafana.enabled` toggle, running the repo's own
  image (pinned; 13.1.3 after phase 14 task 149) with its provisioned dashboards,
  and `datasources/local.yml` repointed at the in-cluster Prometheus Service.
  **Phase 14's handover:** `grafana/dashboards/honryu.json`'s
  `"type": "grafana-piechart-panel"` is migrated to Grafana's built-in
  `piechart` and the vendored AngularJS plugin directory is dropped from the
  image build — Angular was defaulted off in Grafana 11 and removed in 12, so
  that panel cannot render on the pinned image. `grafana/metrics-dashboard/` is
  **deleted** (a `helm create` scaffold with untouched defaults pointing at
  `localhost/grafana`, `pullPolicy: Never`, in the nonexistent namespace
  `honryu-executors`); the image build inputs beside it — `grafana/Dockerfile`,
  `dashboards/`, `datasources/`, `provisioning/`, `config.ini` — are kept.
  Rendering against live data is **not** claimed here; that is task 170.
- **Satisfies:** spec Approach strand D; AC5 (dashboard half); spec decision 3; phase 14 handover
- **Depends on:** 166
- **Done (2026-08-18):**
  **Panel migration.** The dashboard actually has **three** instances of
  `grafana-piechart-panel` (ids 38/39/40, "Response Status"), not one —
  nested inside the collapsed "Plan" and "Label" row panels, invisible to a
  top-level-only scan. All three migrated to the built-in `piechart` type
  (same query, same layout; the old plugin's "combine small slices into
  Others" threshold feature has no built-in equivalent and is dropped, not
  silently pretended-preserved). Done as a **surgical text splice** —
  brace-matched to each panel's exact byte range and replaced in place —
  not a full-file `json.load`/`json.dump` reserialize, which was tried
  first and rejected: it reformatted ~30 unrelated lines (other panels'
  compact `colors` arrays) as a side effect of Python's json module not
  preserving the source's array-wrapping style. Verified twice: a
  revert-and-compare check (swap the 3 migrated blocks back to their
  originals, assert byte-for-byte equality with the pre-migration file)
  before ever writing, and `git diff` afterward showing only the 3 panel
  blocks touched.
  **Scaffold deleted:** `grafana/metrics-dashboard/` (12 files) and the
  vendored plugin `grafana/plugins/grafana-piechart-panel/` (51 files,
  940K) — the plugin directory removal wasn't explicitly listed in the
  task's Files but is the direct consequence of "dropped from the image
  build" the criteria asks for; kept as source until nothing referenced it.
  **Real bug found and fixed, live, via a local Docker smoke test — not
  claimed as "task 170's job," since it blocks provisioning entirely,
  independent of live data:** `grafana/Dockerfile`'s
  `COPY --chmod=644 ./dashboards /var/lib/grafana/dashboards` sets the
  *directory* itself to 644 — no execute bit, non-traversable even by its
  owner. Running the built image locally
  (`docker run registry.pve.heri.life/honryu/grafana:phase16`) surfaced it
  immediately: `Failed to provision dashboard: lstat
  .../honryu.json: permission denied`. This predates the panel migration
  entirely and is the real reason "Grafana has never actually run in this
  cluster" (task 14's finding) — the dashboards could never have loaded,
  ever, on any prior image build. Fixed by extending the existing
  chown+find permission-repair pass (already applied to
  `/etc/grafana/provisioning`) to also cover
  `/var/lib/grafana/dashboards`. Rebuilt, reran the smoke test: **"finished
  to provision dashboards"**, `GET /api/search` returned all three
  dashboards, and `GET /api/dashboards/uid/...` confirmed the three
  migrated panels load as `type: piechart` with no plugin-not-found error
  — genuine load-success verification, not the visual/live-data proof task
  170 owns.
  **Datasource:** `grafana/datasources/local.yml` needed **no value
  change** — its `url: http://prometheus:9090` already matched the literal
  Service name chosen in the Prometheus Service fix below; only a comment
  added explaining why.
  **Correction to task 166, discovered while wiring this:**
  `prometheus-service.yaml`'s Service was fullname-prefixed
  (`honryu-prometheus`), but `datasources/local.yml` is baked into the
  Grafana image at **docker build time** and cannot be Helm-templated — it
  is a fixed value regardless of Helm release name. Renamed the Prometheus
  Service to the literal `prometheus`, mirroring the mysql precedent
  (task 163) exactly, rather than trying to make a static file follow a
  dynamic release name.
  **Image:** built and pushed
  `registry.pve.heri.life/honryu/grafana:phase16` (the repo's own build,
  not the upstream version number — that's already pinned in the
  Dockerfile's `FROM` line). Pushed twice under the same tag: the first
  push carried the permission bug, corrected before anything ever
  consumed it (never deployed to the cluster) — not the phase-11
  stale-digest scenario, since nothing pulled the broken one.
  **PVC placement:** mounting the PVC at `/var/lib/grafana` (the obvious
  choice) would have shadowed the image's baked-in
  `/var/lib/grafana/dashboards` with an empty volume, silently defeating
  provisioning a second, different way. Used `GF_PATHS_DATA=/var/lib/
  grafana-data` and mounted the PVC there instead, with `fsGroup: 472` on
  the pod (matching the Dockerfile's own `chown -R 472:0` and the
  non-root `grafana` user) so the fresh volume is writable.
  `helm lint` clean; `helm template` renders 22 objects. Dry-run: all 3
  new Grafana objects (PVC, Service, Deployment) apply clean under both
  the real release name and the non-colliding check-name — no new
  collisions, only the same three already-documented ones.

### 168. Add `helm lint` and `helm template` to CI
- **Files:** `.github/workflows/ci.yml`, `Makefile` (a `helm-lint` target wrapping both)
- **Criteria:** a CI step runs `helm lint deploy/chart/honryu` and
  `helm template` against the homelab values, failing the build on a broken
  chart (spec decision 6). Helm is pinned by version, and the action pins follow
  the repo's SHA + `# vN` convention (phase 14 task 142's form). A `make
  helm-lint` target gives the same check locally, matching the Makefile's
  one-liner-wrapping-a-script idiom. `npm run check` (yamllint/Prettier) stays
  green. Deliberately no cluster access in CI — template only, no dry-run.
- **Satisfies:** spec AC8
- **Done (2026-08-18):** `scripts/helm-lint.sh` (matching `coverage.sh`/
  `phase-merge.sh`'s idiom — env-overridable `HELM_CHART`/`HELM_VALUES`,
  `set -euo pipefail`) runs `helm lint` then `helm template` against the
  homelab values, wrapped by `make helm-lint`. New CI job `helm-lint`,
  parallel to `lint-and-unit`/`coverage`, no Go setup needed. Helm pinned to
  `v3.21.3` (matching the version verified locally throughout this phase);
  `azure/setup-helm` pinned by commit SHA — resolved live via
  `gh api repos/Azure/setup-helm/git/refs/tags/v5.0.1` (confirmed
  `object.type: commit`, so the ref's sha is directly usable, not an
  annotated-tag object needing a second dereference) — `# v5.0.1`, matching
  the repo's SHA + `# vN` convention.
  **`npm run check` was already red before this task**, for reasons
  spanning several pre-existing files unrelated to it: `api/openapi.yaml`
  (the same gap phase 15 already found — `openapi_test.go` checks routes/
  tags, not yamllint compliance), `.codecov.yml`, four `internal/domain/
  compile/testdata/*.yaml` fixtures, and `storage-data/*.yml` — the last is
  gitignored local test-run output that won't exist on a fresh CI checkout
  at all. Verified none of task 168's own files (`ci.yml`, `Makefile`,
  `scripts/helm-lint.sh`) appear anywhere in the report — "stays green"
  read honestly as "introduces no new findings," since the literal
  precondition (currently green) does not hold.
  Verified: `./scripts/helm-lint.sh` and `make helm-lint` both pass
  locally; `ci.yml` and `Makefile` parse/validate.
- **Depends on:** 167

## Group C — cutover and proof

### 169. Cut over: remove the hand-made objects and install the chart
- **Files:** none in-repo (live-cluster operation); notes for task 170
- **Criteria:** with the chart fully dry-run clean, remove the hand-built objects
  in `honryu` (three Deployments, two Services, the Ingress, and the RBAC/SA the
  chart now owns), then `helm upgrade --install honryu deploy/chart/honryu -f
  deploy/chart/honryu-homelab-values.yaml`. **No data is preserved** (spec
  decision 4) — the discarded fixtures are named in the findings so the loss is
  deliberate and recorded. Verified: every pod Ready including
  `honryu-scheduler`, MySQL bound to a PVC, migrations applied cleanly on
  startup, `/healthz` green, and the SPA loading at `/` on the hostname. Nothing
  hand-made remains in the namespace (`kubectl get all` reconciles with
  `helm get manifest`).
- **Satisfies:** spec Approach strand G; AC1, AC7 (reachability half)
- **Depends on:** 160, 163, 164, 165, 168
- **Done (live, 2026-08-18):** deleted exactly the enumerated hand-made
  objects (3 Deployments, 2 Services, 1 Ingress, Role+RoleBinding,
  ClusterRole+ClusterRoleBinding, ServiceAccount) — confirmed by listing
  before and after; only `serviceaccount/default` (namespace-native, not
  chart-owned) and the 5 preserved Secrets remained. `helm upgrade --install
  honryu deploy/chart/honryu -n honryu -f
  deploy/chart/honryu-homelab-values.yaml` → revision 1, `STATUS: deployed`.
  **Discarded, per decision 4 — the loss is deliberate:** all phases 10–13
  dogfood data (`p10-live`, `p11-live`, `phase12-dogfood`,
  `phase13-dogfood`/`phase13-camp`, campaigns 1/2, scenarios 8/9, and
  everything from task 157's own `phase15-audit` cycle, already
  self-cleaned). The live MySQL was `emptyDir` regardless, so nothing
  extra was actually lost by not dumping it — decision 4 cost nothing it
  hadn't already accepted.
  **Result: 6/6 pods Running and Ready** (api, calibrator, scheduler,
  mysql-0, prometheus, grafana), **4/4 PVCs Bound**. `api`/`scheduler` each
  restarted twice/thrice in the first ~2 minutes on
  `ping mysql: ... no such host` — a normal cold-start DNS race (all pods
  starting simultaneously, mysql's Service/DNS not yet resolvable) that
  self-healed via Kubernetes' restart policy; stable with zero further
  restarts afterward.
  **Migrations: verified directly against the schema**, not inferred from
  logs (the app logs no explicit migration line at INFO level) — all 48
  migrations (`0001_project.sql` … `0048_cluster_ingest_token_hash.sql`)
  present in `schema_migrations`, all applied within the same second the
  pod started.
  `/healthz` → `{"status":"ok"}`. SPA → `200 text/html` via port-forward,
  **and via the real Ingress** (`curl -H 'Host: honryu.pve.heri.life'
  http://10.10.10.90/` → 200); `/metrics` through the same Ingress → 503,
  confirming task 165's block Service live, not just in dry-run.
  **Real bug found and fixed mid-cutover**, unrelated to anything planned:
  Grafana sat `0/1 Running` for 3.5+ minutes, readiness probe reporting
  "connection refused" the whole time. Logs showed Grafana 13's plugin
  "preinstall" feature reaching `grafana.com` to auto-install a default
  catalog (six app plugins) on every cold start — this cluster has no
  public internet route, so each attempt burned ~10-15s timing out before
  Grafana's HTTP listener ever bound. Fixed with
  `ENV GF_PLUGINS_PREINSTALL_DISABLED=true` in `grafana/Dockerfile`,
  verified locally first (readiness dropped from 3.5+ min to **7s**, log
  confirms the override took effect, zero plugin-install attempts logged),
  then rebuilt, pushed (still `:phase16` — the broken digest was never
  consumed by anything, unlike the phase-11 stale-digest scenario), and
  rolled out live with `kubectl rollout restart`: ready in seconds.
  **`kubectl get all` reconciled against `helm get manifest`, exactly**: 5
  Deployments, 1 StatefulSet, 5 Services, 1 Ingress, the RBAC quintet, 2
  ConfigMaps, 4 PVCs (one StatefulSet-templated, three chart-declared) — no
  hand-made object survives; every live object traces to the chart.
  **Flagged, not fixed — outside this task's reach:** `honryu.pve.heri.life`
  **already resolves**, but to `10.10.10.1`, not the MetalLB Ingress address
  `10.10.10.90` — `10.10.10.1` answers with its own `301` to
  `https://honryu.pve.heri.life/` (almost certainly the LAN gateway's own
  admin UI, not this platform). The Ingress route itself is proven correct
  (Host-header test above); the DNS record is wrong or not yet pointed at
  MetalLB. Per spec decision 1, adding the DNS record is the operator's own
  step, not this phase's — recorded here so it is not mistaken for a chart
  problem.

### 170. Live verification: durability, unattended scheduling, live dashboards, findings
- **Files:** `.cortex/2026-08-17-phase16-homelab-deployment/spec.md` ("Live verification findings", phases 7/10–13 format)
- **Criteria:** on the real cluster, prove the three parent-spec ACs that have
  never been demonstrable in a deployment, each as a separate observation:
  (a) **durability** — run an execution to a finalized report, then
  `kubectl delete pod mysql-0`, and after the pod returns the report is still
  served (parent AC `:98`); (b) **unattended scheduling** — a *scheduled*
  execution fires with **no human trigger** and produces a report, which the
  missing `honryu-scheduler` made impossible until now (parent AC `:178`);
  (c) **live dashboards** — a Grafana dashboard **renders** live run data, with
  the AngularJS panel gone (parent AC `:196`). Also: a full
  deploy→trigger→report cycle driven **through the UI at
  `honryu.pve.heri.life`** over both LAN and Tailscale; `/metrics` refused
  through the ingress while Prometheus still scrapes it in-cluster. The
  verification execution **declares `CPU`/`Memory`** — engine pods request
  nothing otherwise (`k8s.go:448-460`), so Karpenter would never provision;
  record whether `mi666-1` actually scaled, and if the single node saturated
  instead, record that as a finding rather than omitting it. Findings also log
  the two app-level gaps as **follow-up phases**: engine-pod placement/affinity
  (no `NodeSelector`/`Affinity`/`Tolerations` in the adapter, `k8s.go:323`) and
  the hardcoded GKE pool label (`k8s.go:64`, not env-configurable) that makes
  `NodePools()` report `"default"` instead of `mi666-1`. Cluster left clean,
  port-forwards stopped.
- **Satisfies:** spec AC3, AC4, AC5, AC7, AC9, AC10; the phase's Goal
- **Depends on:** 169
- **Done (live, 2026-08-18):** full findings in `spec.md`'s "Live
  verification findings" section. Summary: (a) durability proven —
  byte-identical report after `kubectl delete pod honryu-mysql-0` and its
  return. (b) unattended scheduling **failed on the first live attempt** —
  `cmd/scheduler`'s `fireOnce` had no readiness wait before `Trigger`
  (calibrationapp and the HTTP handler both already had one; scheduler
  never got it, since it had never been deployed anywhere before this
  phase). **Fixed live** (commit `e00878b`, TDD'd, 3 new tests, full repo
  green), rebuilt as `:phase16b`, rolled out, **re-verified cleanly on a
  fresh execution with zero human trigger**: report `outcome: passed`,
  86,313 samples. (c) live dashboards — structural proof (task 167) plus
  confirmed real Prometheus data matching the exact panel queries; no
  visual screenshot (no image-renderer plugin available). UI-driven cycle
  and Tailscale reachability handed to the user per an agreed split
  (no browser automation or Tailscale client in this session). Also found
  and restored an out-of-band Ingress regression (the `/metrics` block had
  been overwritten by a manual `kubectl apply`, unrelated to this session,
  confirmed with the user before fixing). Four follow-up gaps recorded
  (engine placement/affinity, GKE pool label, no API path for a normal
  execution's CPU/Memory, `fireOnce`'s remaining lack of a general
  firing-failure retry/backoff). Cluster left clean: 3 test executions
  purged, both ad hoc port-forwards stopped; dogfood projects/scenarios/
  executions left in place matching every prior phase's doctrine.
