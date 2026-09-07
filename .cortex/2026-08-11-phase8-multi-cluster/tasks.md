# Phase 8 — Multi-cluster operation — Tasks

Global numbering continues Phase 7 (70–85). Phase 8 is **86–102**.

Spec: `.cortex/2026-08-11-phase8-multi-cluster/spec.md` · Plan: `plan.md`.

Groups: **86–89** registry domain + persistence + encryption · **90–92**
credential adapters · **93–94** execution cluster field + app service · **95–96**
registry-backed scheduler · **97–99** threading · **100** HTTP + RBAC ·
**101–102** verification.

---

## Group A — registry domain + persistence + encryption

### 86. Add the `clusterregistry` domain — **done, `abf3c84`**
- **Files:** `internal/domain/clusterregistry/clusterregistry.go`, `..._test.go`
- **Criteria:** a `Cluster` type with `Name` (the `ClusterRef`, plain string —
  domain imports no ports, mirroring `schedule.Schedule.Cluster`), `APIURL`,
  `CACert`, `IngestURL`, `SidecarImage`, `Namespace`, `SecretRef`, and
  `Origin` (`operator`|`byoc`); `Validate` rejects an empty/blank name, an
  unknown origin, and a BYOC entry missing its credential reference; a `Default`
  sentinel or predicate distinguishes the implicit default. Table-tested.
- **Satisfies:** spec "Approach — New domain + persistence"; AC1
- **Depends on:** —

### 87. Add the `ClusterRegistry` port + fake + conformance contract — **done, `6db6632`**
- **Files:** `internal/ports/clusterregistry.go`, `internal/ports/fake/clusterregistry.go`, `internal/ports/repositorytest/clusterregistry_contract.go`
- **Criteria:** port with `Create`/`Get`/`List`/`Update`/`Delete` and
  `Resolve(ctx, ClusterRef) (Cluster, error)` returning `ErrNotFound` for an
  unknown ref; a fake implementation; a conformance contract exercising
  round-trips, update, delete, list ordering, and not-found. Fake passes the
  contract.
- **Satisfies:** spec "Approach"; AC1, AC5
- **Depends on:** 86

### 88. Implement the MySQL registry store — **done, `5f69de4`**
- **Files:** `migrations/00NN_cluster_registry.sql`, `internal/adapters/repo/mysql/cluster_registry.go`, wiring in the mysql conformance test
- **Criteria:** a `cluster_registry` table with the non-secret columns +
  `secret_ref` + a nullable opaque `byoc_credential` BLOB (ciphertext, never
  plaintext) + `origin`; the store satisfies the task-87 conformance contract
  against real MySQL; the BLOB is written/read verbatim (encryption is task 89).
- **Satisfies:** spec "Constraints — mixed credential source"; AC1, AC2
- **Depends on:** 87

### 89. Add BYOC credential envelope encryption — **done, `1df926a`**
- **Files:** `internal/adapters/repo/mysql/cluster_credential.go` (or a small `internal/adapters/secretbox/`), `..._test.go`
- **Criteria:** encrypt/decrypt a BYOC kubeconfig with an app-held key sourced
  from config/env (AEAD, e.g. NaCl secretbox / AES-GCM with a random nonce per
  record); the mysql store persists only ciphertext; round-trip recovers the
  plaintext; a tampered ciphertext or wrong key fails closed; the plaintext never
  appears in logs. A missing/short key is a startup config error.
- **Satisfies:** spec "Constraints — mixed credential source"; AC2
- **Depends on:** 88

---

## Group B — credential adapters

### 90. Add self-contained-kubeconfig validation — **done, `efefccd`**
- **Files:** `internal/adapters/scheduler/k8s/kubeconfig.go`, `..._test.go`
- **Criteria:** parse a kubeconfig via client-go `clientcmd`; **reject** any
  context whose user carries an `exec` or `auth-provider` block with a clear
  error; accept an embedded-CA + bearer-token/client-cert config and extract
  `APIURL`/`CACert`/`token`. Table-tested over GKE-exec, EKS-exec, token, and
  cert fixtures.
- **Satisfies:** spec "Constraints — provider-neutral auth only"; AC2
- **Depends on:** 86

### 91. Add the k8s Secret materializer + reader — **done, `7aee0aa`**
- **Files:** `internal/adapters/scheduler/k8s/credential.go`, `..._test.go`
- **Criteria:** given a credential (`APIURL`, `CACert`, `token`), write a
  home-cluster k8s Secret under a deterministic name and read it back into a
  `rest.Config`; a reconcile updates an existing Secret in place; tested against
  the fake clientset. No secret values in logs.
- **Satisfies:** spec "Constraints — mixed credential source"; AC2, AC5
- **Depends on:** 86

### 92. Add the cluster connectivity/RBAC prober — **done, `063de6e`**
- **Files:** `internal/ports/clusterprober.go` (or fold into the scheduler port), `internal/adapters/scheduler/k8s/prober.go`, fake + `..._test.go`
- **Criteria:** build a client from a `rest.Config` and probe the target: server
  reachable (e.g. `/version` or a discovery call) **and** the namespace usable
  for the least-privilege verbs (list/create StatefulSets/ConfigMaps/pods);
  return a typed, message-bearing error for unreachable / unauthorized /
  under-privileged. A fake prober lets app tests inject outcomes.
- **Satisfies:** spec "Safety — registration validation, least privilege"; AC1
- **Depends on:** 91

---

## Group C — execution cluster field + app service

### 93. Add `Cluster` to `execution.Execution` — **done, `f0ccdb6`**
- **Files:** `internal/domain/execution/execution.go` (+ `New`/`Validate`), `internal/adapters/repo/mysql/execution*.go` + migration, `internal/ports/fake`, `internal/app/executionapp` config path, tests
- **Criteria:** `Execution.Cluster string` with the "empty means the deployment
  default" convention (mirroring `Engine`); persisted and round-tripped through
  mysql + fake; the config-upload path accepts/stores it; existing executions
  (no cluster) load as empty and behave as before.
- **Satisfies:** spec "Approach — Cluster on the execution"; AC3, AC4
- **Depends on:** —

### 94. Add `clusterapp` — **done, `73d91bf`**
- **Files:** `internal/app/clusterapp/service.go`, `..._test.go`
- **Criteria:** `Register` (operator + BYOC), `Get`/`List`/`Update`/`Delete`, and
  `Resolve`; register validates the kubeconfig is self-contained (90), probes
  connectivity/RBAC (92), encrypts a BYOC credential (89), materializes the
  Secret (91), and stores the entry (87); `Delete` is **guarded** — rejected with
  a typed error when an execution bound to that cluster has an active run (via a
  repo query using task 93's `Execution.Cluster`). Fake-based tests cover
  success, each validation rejection, and the delete guard.
- **Satisfies:** spec "Approach — BYOC flow"; AC1, AC2, AC11
- **Depends on:** 87, 89, 90, 91, 92, 93

---

## Group D — registry-backed scheduler

### 95. Convert the k8s scheduler to a per-`ClusterRef` client factory + cache — **done, `410740b`**
- **Files:** `internal/adapters/scheduler/k8s/k8s.go`, `internal/adapters/scheduler/k8s/factory.go`, `cmd/api/main.go`, `cmd/scheduler/main.go`, `cmd/calibrator/main.go`, tests
- **Criteria:** the scheduler no longer holds a single client; each method
  resolves its `ClusterRef` to a client via a factory that reads the registry
  entry's Secret (91) and **caches** the built client, rebuilding lazily on a
  client error (covers credential rotation); the empty/default ref resolves to
  `InClusterConfig`. Existing k8s scheduler tests pass through the default path;
  a new test proves a non-default ref builds from a registered entry's Secret.
- **Satisfies:** spec "Approach — Registry-backed scheduler"; AC5
- **Depends on:** 87, 91

### 96. Source `SidecarImage`/`IngestURL` per-cluster from the resolved entry — **done, `b326393`**
- **Divergence:** the two `ClusterConfig` fields were **kept** (not removed) as
  the default cluster's settings (`DefaultDeploy`), since the default cluster has
  no registry row and legitimately sources them from config; removing them would
  force a synthetic default entry with a placeholder SecretRef. Registered
  clusters source ns/sidecar/ingest from their entry (AC7 core met), and
  `clusterregistry.Validate` now requires all three.
- **Files:** `internal/adapters/scheduler/k8s/k8s.go`, `internal/config/config.go` (remove the two `ClusterConfig` fields), `cmd/*/main.go`, tests
- **Criteria:** the deploy spec's sidecar image and ingest URL come from the
  resolved registry entry, not global config; the default cluster's entry carries
  what `ClusterConfig.SidecarImage`/`IngestURL` held; the k8s-scheduler config
  validation (Phase 7) moves to "every registered cluster must set them."
- **Satisfies:** spec "Constraints — the two single-valued config fields move
  per-cluster"; AC7
- **Depends on:** 95

---

## Group E — threading

### 97. Thread the execution's cluster through `Deploy`/`Trigger` — **done, `fa04c5a`**
- **Files:** `internal/app/lifecycleapp/service.go`, tests
- **Criteria:** `Deploy` sets `DeploySpec.Cluster` from the execution's cluster
  (replacing `""` at `service.go:254`) and `Trigger` passes it to `quota.Reserve`
  (replacing `""` at `service.go:382`); an empty cluster resolves to the default;
  status/logs/purge for the execution all target its cluster. Tests cover a named
  cluster and the empty-default case.
- **Satisfies:** spec "Approach — Cluster on the execution"; AC3, AC4, AC6
- **Depends on:** 93, 95

### 98. Add `Cluster` to `report.Meta` — **done, `da8f443`**
- **Files:** `internal/domain/report/report.go`, `internal/app/metricsapp/*.go`, `internal/adapters/repo/mysql/report_store.go` + migration, API response, tests
- **Criteria:** `report.Meta.Cluster` sourced from the execution at finalize time;
  persisted with the report and surfaced in the report API response (load
  origin); an execution on the default cluster records the default.
- **Satisfies:** spec "Approach"; AC8
- **Depends on:** 93

### 99. Remove `schedule.Cluster`; derive from the execution — **done, `f76c9e7`**
- **Files:** `internal/domain/schedule/schedule.go`, `internal/adapters/repo/mysql/schedule*.go` + migration, `internal/ports/repositorytest/schedule_contract.go`, `cmd/scheduler/main.go`, `internal/adapters/httpapi/schedule_handlers.go`, tests
- **Criteria:** `schedule.Schedule.Cluster` is removed (column dropped); the fire
  path (`cmd/scheduler` `fireOnce`) deploys and reserves quota against the
  **execution's** cluster; the schedule conformance contract and handlers no
  longer reference a schedule cluster; existing schedules keep firing (onto their
  execution's cluster, default for pre-Phase-8 rows).
- **Satisfies:** spec "Approach — schedule.Cluster removed"; AC10
- **Depends on:** 93, 97

---

## Group F — HTTP + RBAC

### 100. Add cluster-registry CRUD HTTP endpoints — **done, `6eb0c1a`**
- **Note:** the HTTP layer + tests are complete; wiring `clusterapp` into
  `cmd/api` (in-cluster client + config-provided credential-encryption key) is
  deferred to task 102, where it is exercised end-to-end live.
- **Files:** `internal/adapters/httpapi/cluster_handlers.go`, `internal/adapters/httpapi/router.go`, `internal/adapters/httpapi/errors.go`, tests
- **Criteria:** `POST /api/clusters` (operator + BYOC via a kubeconfig upload),
  `GET /api/clusters`, `GET/PUT/DELETE /api/clusters/{name}`; every route is
  **platform-admin-gated** (mirroring `authorizeAdmin` / `rbac.ResourceSystem`
  + `ActionAdmin`); form-encoded; clusterapp errors map to the right status
  (validation → 400, delete-guard → 409, not-found → 404, unauthorized probe →
  422/400 with reason). Handler tests cover the gate and each mapping.
- **Satisfies:** spec "Safety — RBAC"; AC1, AC2, AC12
- **Depends on:** 94

---

## Group G — verification

### 101. e2e — cluster routing, campaign spanning, backward-compat — **done, `6386bbc`**
- **Files:** `internal/adapters/httpapi/*_e2e_test.go` (or the existing e2e suite)
- **Criteria:** with two registered clusters (fake scheduler), an execution
  naming cluster B deploys to B and reserves B's quota; a campaign whose
  executions span A and B rolls up to one verdict; an execution with an empty
  cluster deploys to the default with behavior identical to the pre-Phase-8 path.
- **Satisfies:** AC3, AC4, AC6, AC9
- **Depends on:** 97, 98, 99, 100

### 102. Live verification against the `honryu` cluster registered explicitly — **done (2026-08-11), AC13 met**
- **Files:** verification notes appended to this `tasks.md`
- **Criteria:** register the live `honryu` cluster as an **explicit** registry
  entry (its own API URL/CA/token Secret, ingest URL, sidecar image), create an
  execution naming it, and run end-to-end through the **registry-backed client
  path** (not the implicit `InClusterConfig` default) against
  `httpbin.pve.heri.life`; confirm deploy/trigger/report/purge all target the
  registered cluster. Document results. (Manual / cluster-dependent — a
  verification activity, not a committed test.)
- **Satisfies:** spec "Verification bar — live"; AC13
- **Depends on:** all

#### Live verification results (2026-08-11, Talos homelab)

Rebuilt `honryu-api` → `registry.pve.heri.life/honryu/honryu-api:phase8` (Phase 8
code) and redeployed with `HONRYU_CLUSTER_CREDENTIAL_KEY` set; migrations
0040–0043 applied cleanly on startup.

- **Live finding — home-namespace RBAC gap:** the control plane's Role
  (`honryu-scheduler`) granted statefulsets/configmaps/pods but **not `secrets`**;
  the registry-backed path reads credential Secrets, so registration first
  failed 500 (`cannot get resource "secrets"`). Fixed by granting
  `secrets: get,list,create,update,delete` on the home-namespace Role — the
  documented RBAC contract for a Phase 8 control plane. (No code change; a
  deployment-manifest requirement.)
- **Explicit registration:** created Secret `cluster-honryu-explicit-creds`
  (`api-url=https://kubernetes.default.svc:443`, embedded `ca.crt`, a `honryu`
  SA token). `POST /api/clusters` (operator, `secret_ref`) → **201**: it read the
  Secret, **probed the target API server via the SA token and passed the
  least-privilege RBAC check**, and stored the entry with `api_url` taken from
  the Secret — i.e. the *registry-backed* credential, not `InClusterConfig`.
- **Execution on the registered cluster:** created an execution naming
  `honryu-explicit` (cluster surfaced in the API); **deploy** built a client from
  the entry's Secret and created the StatefulSet/pod (2/2 Running); the engine
  generated real load against `httpbin.pve.heri.life`. The sidecar image came
  from the entry (task 96).
- **Report (run 87):** `cluster=honryu-explicit`, `outcome=passed`,
  **≈6696 QPS**, 622,735 samples, p95 = 2 ms — the report records the load
  origin (task 98), produced entirely through the registry-backed path.
- **Timing note (pre-existing, not Phase 8):** bzt runs on pod start, so a slow
  deploy→trigger gap lets the engine push before `StartRun`, yielding sidecar
  409s (the Phase-7 sidecar-timing behaviour). Triggering promptly avoided it.
  The native JMX loops forever (`loops=-1`), so the run was finalized via
  `stop`, which captured the accumulated metrics above.
- **Purge/CRUD:** `purge` tore the resources down via the registered cluster;
  `GET /api/clusters` listed the entry; `DELETE /api/clusters/honryu-explicit`
  (no active run) → **204**; subsequent `GET` → **404**.

**Conclusion:** deploy, trigger, report, and purge all target the explicitly
registered cluster through the registry-backed client path — AC13 met. The
`honryu` namespace is left running on the `:phase8` image for future runs; the
live SA token + encryption key used for the run were scrubbed from scratch.
