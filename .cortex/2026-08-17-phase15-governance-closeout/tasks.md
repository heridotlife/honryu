# Phase 15 — Governance closeout — Tasks

Continues the roadmap's global task numbering from Phase 14's last task (150).
**Spec:** `spec.md` · **Plan:** `plan.md`

**Phase-level precondition:** phase 14 (tasks 148-150) is merged to develop.
`ci.yml` is phase 14's territory until then, and a merge gate is meaningless
while the coverage gate is red.

## Group A — merge-time enforcement

### 151. Add the phase-close gate: `scripts/phase-merge.sh` + `make phase-merge`
- **Files:** `scripts/phase-merge.sh` (new), `Makefile`, `AGENTS.md`
- **Criteria:** script follows `scripts/coverage.sh`'s idiom (`set -euo pipefail`,
  header comment explaining itself, env-overridable knobs); requires a phase
  description (`PHASE=` or `$1`) and prints usage if absent. Refuses, each with a
  distinct message and non-zero exit, **before** running anything expensive:
  dirty working tree (`git status --porcelain` non-empty); current branch is not
  the working branch (`WORK_BRANCH`, default `feat/honryu`); `web/dist/.gitkeep`
  missing (a staged deletion breaks `go:embed all:dist` in fresh checkouts —
  phase 13 AC4, and the tree carries that deletion today); target branch
  (`TARGET_BRANCH`, default `develop`) absent locally. Then runs, stopping at the
  first failure: `gofmt -l` (empty), `go vet ./...`, `golangci-lint run`,
  `make test`, `./scripts/coverage.sh` (threshold unchanged, honours
  `COVERAGE_THRESHOLD`). Only then `git merge --no-ff` into the target with
  `chore: merge <WORK_BRANCH> (<PHASE>) into <TARGET_BRANCH>`; prints the
  resulting commit; **never pushes**, says so, and prints the exact `git push`
  command to run next (resolved decision 1). `--dry-run` (or
  `PHASE_MERGE_DRY_RUN=1`) runs every check and stops before the merge.
  `make phase-merge PHASE="…"` wraps it with a `##` help comment matching the
  existing targets. `AGENTS.md` documents it as *the* phase-close step, replacing
  "merge locally" with "run this". Each refusal path is exercised and shown;
  the happy path is rehearsed with `--dry-run`.
- **Satisfies:** spec Approach strand A step 1; AC1, AC2
- **Depends on:** —

### 152. Run CI on pushed feat branches
- **Files:** `.github/workflows/ci.yml`
- **Criteria:** `push.branches` gains `feat/**`; the existing comment explaining
  why push was scoped to `main` is **rewritten so it stays true** (it currently
  asserts a rationale this change supersedes). `security.yml` and `codeql.yml`
  are deliberately unchanged — the reason (already PR + weekly cron; govulncheck
  per feat push is not worth the minutes) recorded in the commit message, not
  only here. `npm run check` (yamllint/Prettier) green. Verified by pushing a
  commit to `feat/honryu` and observing the workflow run.
- **Satisfies:** spec Approach strand A step 2; AC3
- **Depends on:** —

## Group B — artifact truth

### 153. Amend the parent spec: decision #7 and the phasing table
- **Files:** `.cortex/2026-07-30-honryu/spec.md`
- **Criteria:** resolved decision #7 (`:226`) gains a dated **"Refined by Phase
  12 (2026-08-17)"** paragraph in the exact shape decision #10 already carries
  (`:236`): the BYOC *data plane* — per-cluster ingest tokens, hashed at rest,
  batch→cluster scoping — shipped in phase 12; customer-self-service and
  tenant-scoped registration remain later. The "Suggested phasing" table
  (`:298-309`) gains one row each for phases 10-14 in the existing
  `Phase | Delivers | Why here` shape, so the governing document stops implying
  the project ends at Phase 9. No restatement of detail the phase specs already
  hold — one line per phase.
- **Satisfies:** spec Approach strand B steps 3-4; AC4
- **Depends on:** —

### 154. Record the SPA/Grafana boundary and the carried-forward deferrals
- **Files:** `.cortex/2026-08-16-phase13-operator-ui/spec.md`,
  `.cortex/2026-07-30-honryu/spec.md` (open questions)
- **Criteria:** phase 13's spec gains a short **"Boundary — SPA vs Grafana"**
  section tying its read-only/no-chart-library stance to the parent's non-goal
  (`:40`) and live-metrics AC (`:196`): the SPA is read-only navigation over the
  existing API; no charting library, no metric storage, no replacement for
  Grafana dashboards. The parent spec records the **operator-SPA tenancy stance**
  as decided (resolved decision 2: the SPA enforces nothing itself, inherits the
  API's scoping, and must never present an aggregate the API cannot scope) with
  the endpoint-scoping *verification* named as a phase-16 precondition; Gatling
  (#1) is left in the open questions with its stated JDK/Scala reason. Both are
  findable from the parent spec alone.
- **Satisfies:** spec Approach strand B steps 5-6; AC5
- **Depends on:** 153 (same parent file, sequential edits)

### 155. Document `cluster` on the `Report` schema
- **Files:** `api/openapi.yaml`
- **Criteria:** the `Report` schema (`:520-545`) gains `cluster` (type string,
  description matching `domain/report.Report`'s own comment: the load origin,
  empty = the deployment default), placed beside `engine`/`correlation_id` — the
  wire has carried it since phase 8 and phase 13's UI types and renders it.
  `TestOpenAPIMatchesRoutes`/`TestOpenAPITagsMatchRouteGroups` stay green
  (they assert routes and tags, not schema properties — which is why nothing
  caught this; extending them is explicitly out of scope).
- **Satisfies:** spec Approach strand B step 7; AC8
- **Depends on:** —

## Group C — credential teardown

### 156. Tear down the BYOC credential Secret when a cluster is deleted
- **Files:** `internal/app/clusterapp/service.go`,
  `internal/app/clusterapp/service_test.go`
- **Criteria:** `Delete` (`service.go:262`) fetches the entry, keeps today's
  active-run guard, tears the Secret down via the existing
  `Credentials.Delete(ctx, entry.SecretRef)` **only when
  `entry.Origin == clusterregistry.OriginBYOC`**, then deletes the registry row.
  Unit tests over the existing `fakeCredStore` assert, each named for the
  behaviour it protects: (a) deleting a BYOC cluster removes both the Secret and
  the row; (b) **deleting an operator cluster leaves its Secret untouched** —
  operator Secrets are read, never materialized (`service.go:112-115`), so
  deleting one would destroy out-of-band infrastructure; (c) a teardown failure
  aborts the delete with row *and* Secret intact, and a retry succeeds; (d) a
  Secret already absent still succeeds (adapter tolerates not-found,
  `credential.go:156-166`); (e) an unknown cluster still surfaces
  `ports.ErrNotFound`; (f) `RegisterBYOC`'s rollback tests stay green. The
  httpapi 204 path is unchanged (`cluster_handlers.go:205-216`).
- **Satisfies:** spec Approach strand C steps 8-10; AC6
- **Depends on:** —

## Group D — verification and close

### 157. Live: audit for orphaned credential Secrets, prove a clean register→delete
- **Files:** verification notes appended to `spec.md` ("Live verification
  findings", phases 7/10-13 format)
- **Criteria:** build and push the API image and roll it out (the phase 12/13
  ritual), then on the real cluster (`/home/coder/.kube/config`,
  `admin@talos-homelab`, ns `honryu`): list Secrets matching the materialized
  credential naming pattern and cross-reference against `GET /api/clusters` —
  report any Secret with no registry row (both known instances were removed by
  hand in phases 12/13, so **zero is the expected result and the deliverable is
  evidence of absence**, not cleanup); then one full register→delete cycle
  proving nothing is left behind, and one operator-origin entry confirming its
  Secret survives deletion if one can be arranged safely (otherwise state that
  the guarantee rests on task 156's unit test and say so). Cluster left clean,
  port-forwards stopped. Findings appended.
- **Satisfies:** spec AC7; the spec's "security-grade" framing
- **Depends on:** 156

### 158. Close the phase through its own gate
- **Files:** `.cortex/2026-08-17-phase15-governance-closeout/spec.md` (findings)
- **Criteria:** the spec's three plan-time corrections are already recorded (done
  at plan time: no port widening needed, teardown is origin-gated, AC6's test
  shape). Close the phase with
  `make phase-merge PHASE="phase 15 governance closeout"` — the gate's first
  real-world use is this phase's own merge — and record its output in the
  findings, including the coverage figure it enforced.
- **Satisfies:** spec Constraints (artifact truth); AC9; the phase's Goal
- **Depends on:** 151, 152, 153, 154, 155, 156, 157
