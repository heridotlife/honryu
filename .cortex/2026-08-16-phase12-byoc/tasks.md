# Phase 12 — BYOC — Tasks

Continues the roadmap's global task numbering from Phase 11's last task (128).
**Spec:** `spec.md` · **Plan:** `plan.md`

## Group A — registry: token hash column + lookup

### 129. Cluster ingest-token hash on the registry
- **Files:** `internal/domain/clusterregistry/clusterregistry.go` (+ token
  encode/hash helpers in a new `token.go`, table-tested),
  `migrations/0048_cluster_ingest_token_hash.sql`,
  `internal/ports/clusterregistry.go`,
  `internal/adapters/repo/mysql/cluster_registry.go`,
  `internal/ports/fake/clusterregistry.go`,
  `internal/ports/repositorytest/cluster_contract.go`
- **Criteria:** `Cluster.IngestTokenHash string` (json-excluded, never
  surfaced — same rule as the encrypted credential); pure helpers
  `EncodeToken(raw []byte) string` (base64url, no padding) and
  `HashToken(token string) string` (64 lowercase hex) in the domain package,
  table-tested, no I/O; port gains `SetClusterIngestTokenHash(ctx, name,
  hash string) error` (overwrite = rotation; empty hash clears — shaped like
  `SetClusterCredential`, `clusterregistry.go:37-42`) and
  `ClusterByIngestTokenHash(ctx, hash string) (Cluster, error)`
  (`ports.ErrNotFound` when no row); migration adds
  `ingest_token_hash CHAR(64) NULL UNIQUE`; mysql + fake pass one shared
  conformance case (set on BYOC row, resolve round-trip, rotate overwrites,
  operator row with NULL never resolves, second cluster same hash is
  rejected/unique-violation surfaced as a domain error).
- **Satisfies:** spec Approach step 1; AC1 (storage half)
- **Depends on:** —

## Group B — minting

### 130. Mint at BYOC registration; rotate on demand
- **Files:** `internal/app/clusterapp/service.go`, `service_test.go`,
  `internal/adapters/httpapi/cluster_handlers.go`,
  `cluster_handlers_test.go`, `internal/adapters/httpapi/router.go` (route),
  `api/openapi.yaml`
- **Criteria:** `clusterapp.RegisterBYOC` returns
  `(clusterregistry.Cluster, error)` today — widened to return the minted
  token once: new result shape `RegisterResult{Cluster Cluster; IngestToken
  string}` (token empty when minting is disabled/unneeded); mint happens
  only after the existing probe passes, hash stored via task 129's setter
  before `CreateCluster` commits (a failed store fails registration — no
  token-less half-registered cluster); randomness (`crypto/rand`, 32 bytes)
  lives in clusterapp behind an injectable seam (mirroring phase 10's
  `WithTraceContext`), domain stays pure; `RotateIngestToken(ctx, name)`
  re-mints, overwrites the hash atomically, returns the token once; route
  `POST /api/clusters/{name}/rotate-ingest-token` (same gate as cluster
  CRUD); registration response JSON gains `ingest_token` (presented once,
  documented as such in openapi); the cluster GET/list responses never
  include the hash.
- **Satisfies:** spec Approach step 2; AC1
- **Depends on:** 129

## Group C — ingest auth

### 131. Per-cluster ingest authentication and batch scoping
- **Files:** `internal/adapters/httpapi/ingest_handlers.go`,
  `ingest_handlers_test.go`, `internal/adapters/httpapi/router.go` (Deps),
  `cmd/api/main.go` (wire repo as both collaborators)
- **Criteria:** auth order is exactly: (1) global-token constant-time match ⇒
  accept, behavior byte-identical to today for every batch; (2) else
  SHA-256(presented) → `ClusterByIngestTokenHash` — miss ⇒ 401 "invalid
  ingest credentials" (today's wording); (3) hit ⇒ load the execution
  (`ExecutionClusterLoader` = `GetExecution`, nil-tolerant Deps seam) and
  require `execution.Cluster == cluster.Name` — mismatch or default-cluster
  (empty `Cluster`) execution ⇒ 403 "ingest token is not valid for this
  execution"; (4) only then the existing decode/`Metrics.Ingest` path,
  unchanged. Auth runs *before* the body decode? — no: keep decode first
  (batch.ExecutionID is needed for step 3; today's malformed-batch 400 for
  unauthenticated requests changes to 401 only when the token itself is
  unknown — covered by tests); handler unit matrix: global/unknown/valid+
  routed/valid+default/valid+other-cluster × run-state irrelevance; nil
  resolver in Deps ⇒ global-token-only (existing harnesses unchanged unless
  they opt in).
- **Satisfies:** spec Approach step 3; AC2
- **Depends on:** 129

## Group D — verification

### 132. e2e — token × routing matrix
- **Files:** `test/e2e/phase12_e2e_test.go` (phase 8/11 harness pattern)
- **Criteria:** over real HTTP + MySQL: register two clusters (`byoc-a`,
  `byoc-b`) via the public API with distinct minted tokens (fake-probe path
  where the real probe can't run in e2e — the registration seam stays real);
  executions `on-a` (cluster: byoc-a) and `on-default` (no cluster):
  byoc-a token + on-a execution ⇒ ingest accepted (run opened, batch
  absorbed); byoc-a token + on-default ⇒ 403; byoc-a token + on-b execution
  ⇒ 403; global token + all three ⇒ accepted (operator path unchanged);
  garbage token ⇒ 401; rotating byoc-a's token kills the old one (401) and
  admits the new; phase-8 suites stay green (quota/routing untouched).
- **Satisfies:** AC2, AC4
- **Depends on:** 130, 131

### 133. Live dogfood — homelab as a BYOC cluster, token matrix live
- **Files:** verification notes appended to `spec.md` ("Live verification
  findings", Phases 7/10/11 format)
- **Criteria:** on the real cluster: `POST /api/clusters` with the homelab's
  own kubeconfig registers `dogfood-byoc` (probe passes — the plane reaches
  the API server it runs on); capture the one-time token; create the
  `honryu-ingest` Secret semantics are documented (Optional env — for the
  dogfood run itself the global token path serves, proving the seam
  unchanged); run one k6 or jmeter execution routed to `dogfood-byoc` end to
  end — pods schedule (on the same cluster, that is the dogfood), KPIs flow,
  report finalizes with correlation id; curl the matrix live: dogfood token
  + a `dogfood-byoc`-routed execution's batch ⇒ accepted authz verdict;
  same token + a default-cluster execution ⇒ 403; garbage ⇒ 401 (target a
  live run or assert the authz verdicts which precede run-state checks);
  rotate and observe old-token 401. Cluster left clean (dogfood entry
  deleted, pods purged, port-forwards stopped).
- **Satisfies:** AC3, AC5 (live half)
- **Depends on:** 132
