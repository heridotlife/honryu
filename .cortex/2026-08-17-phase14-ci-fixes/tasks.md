# Phase 14 — CI green — Tasks

Continues the roadmap's global task numbering from Phase 13's last task (140).
**Spec:** `spec.md` · **Plan:** `plan.md`

## Group A — Toolchain and action runtimes

### 141. Pin Go 1.26.6 everywhere (workflows, Dockerfile, go.mod)
- **Files:** `.github/workflows/ci.yml` (lines 23, 47), `.github/workflows/security.yml` (24, 41), `.github/workflows/codeql.yml` (27), `deploy/honryu/Dockerfile` (FROM line), `go.mod`, `go.sum` (if touched by the directive change)
- **Criteria:** all five `go-version:` pins say `"1.26.6"`; Dockerfile builder pins `golang:1.26.6` (explicit patch, not floating); go.mod carries `go 1.26.6` **and** `toolchain go1.26.6` (plan's resolved spelling); local `go version` reports go1.26.6 via GOTOOLCHAIN auto-download before other checks; `govulncheck ./...` locally reports 0 findings (the six GO-2026-* vulns gone); `make lint` (v2.12.2) and `make test` green on the new toolchain — if lint rejects the directive, apply the documented fallback (`go 1.26.0` + `toolchain go1.26.6`) and record it in the commit message; Docker image builds (`docker build` smoke or the phase-12 build ritual) from the pinned builder.
- **Satisfies:** AC1; Approach strand A (toolchain half)
- **Depends on:** —

### 142. Bump behind actions to Node-24 runtimes (setup-go v6, upload-artifact v7, codeql-action v4)
- **Files:** `.github/workflows/ci.yml` (setup-go ×2, upload-artifact ×1), `.github/workflows/security.yml` (setup-go ×2, codeql-action/upload-sarif ×5), `.github/workflows/codeql.yml` (setup-go ×1, codeql-action/init+analyze)
- **Criteria:** `actions/setup-go` → v6.5.0, `actions/upload-artifact` → v7.0.1, `github/codeql-action/{init,analyze,upload-sarif}` → v4.37.0, each pinned by the exact commit SHA with `# vN` comment **copied from the corresponding dependabot PR diffs** (#188, #186, #184/#187/#185); checkout v7, codecov v5, trivy, anchore, hadolint, scorecard confirmed already-latest (no change); every workflow still passes `yamllint`/Prettier (`npm run check`); grep proves no `setup-go@.*v5`/`upload-artifact@.*v4`/`codeql-action@.*v3` pins remain.
- **Satisfies:** AC3; Approach strand A (actions half)
- **Depends on:** 141 (same files, sequential edits)

## Group B — Coverage to ≥90.5% with genuine tests

### 143. Delegation and one-liner wins (k8s Router, nexus, sidecar, adminapp)
- **Files:** `internal/adapters/scheduler/k8s/factory_test.go`, `internal/adapters/scheduler/k8s/k8s_test.go`, `internal/adapters/storage/nexus/nexus_test.go`, `internal/sidecar/sidecar_test.go`, `internal/app/adminapp/service_test.go`
- **Criteria:** unit tests cover the six 0% Router methods (`ExecutionStatus`, `EngineDetail`, `PurgeExecution`, `PodLog`, `DeployedExecutions`, `NodePools` — routed + unrouted-cluster error paths via the existing fake/registry harness in factory_test.go) and `scenarioFileKey` (k8s.go:215, table incl. cluster-scoped keys); `nexus.WithClient`; `sidecar.Sent` (sent/unsent paths); `adminapp.InScopeExecutions` + `adminapp.Abort` (in/out-of-scope, repo error); unit lane stays Docker-free; `go test -race ./...` green.
- **Satisfies:** AC2 (first tranche); Approach strand B (step: cheap wins)
- **Depends on:** —

### 144. lifecycleapp: Purge, FinalizeOrphaned, WithNow
- **Files:** `internal/app/lifecycleapp/service_test.go` (or new `purge_test.go` following the package's per-concern test files)
- **Criteria:** `Purge` covered (happy path via the ports fakes, missing-execution/not-found branch, repo/adapter error propagation — assert the repo→scheduler orchestration order with the existing fake call-log pattern); `FinalizeOrphaned` (orphan found+finalized, none found, error branch); `WithNow` (returned copy uses injected clock); tests follow the existing lifecycleapp style (fakes from `internal/ports/fake`, table tests).
- **Satisfies:** AC2; Approach strand B
- **Depends on:** —

### 145. httpapi: updateCluster handler + top long-tail branches
- **Files:** `internal/adapters/httpapi/cluster_handlers_test.go`, `internal/adapters/httpapi/handlers_coverage_test.go`
- **Criteria:** `updateCluster` (cluster_handlers.go:186) covered end-to-end through the existing router test harness: happy update (fields changed + 200 shape matches openapi), validation error (400), not-found (404), repo error (500), auth/RBAC path per the package's router test conventions; then, using the coverage profile as the map, knock down the largest remaining uncovered branches in httpapi (error-mapping paths first — the handlers_coverage_test.go precedent) until httpapi's package gap is ≲130 statements; openapi conformance test still green (no behavior change — tests only).
- **Satisfies:** AC2; Approach strand B
- **Depends on:** —

### 146. cmd wiring: deepen run()/loop coverage
- **Files:** `cmd/api/main_test.go`, `cmd/api/wiring_test.go`, `cmd/scheduler/main_test.go`, `cmd/calibrator/main_test.go`, `cmd/sidecar/main_test.go`
- **Criteria:** no production refactor unless a branch is genuinely untestable (mains are already thin over `run()`); cmd/api `run()` error branches (config load, repo/migrations failure paths injectable via getenv) take the package ≥88%; cmd/scheduler loop functions (`runLoop`, `runHorizonLoop`, `runDrainLoop`, `runCalibratorLoop` — unexported, same-package tests) covered incl. tick/error/cancel paths; cmd/calibrator + cmd/sidecar `run()` branches to ≥85%; integration-tagged tests (if any new ones need Docker) stay behind tags; unit lane Docker-free.
- **Satisfies:** AC2; Approach strand B
- **Depends on:** —

### 147. mysql repo: integration tests for uncovered row-mapping and error paths
- **Files:** `internal/adapters/repo/mysql/` (extend the existing `*_test.go` files behind `-tags=integration`, `test/dbtest` harness)
- **Criteria:** driven by the per-function coverage report of a local `make cover-gate` run: cover the biggest uncovered mysql paths — ErrNoRows→domain-mapping branches, transaction rollback paths, cluster/report/usage row scans with edge values (NULLs, empty engine images, zero timestamps) — each test asserts observable domain behavior, not SQL text; runs in the existing `-p 1` Docker lane; repositorytest conformance suite stays green; target ≈+80 covered statements from this package (gap 235 → ≲155).
- **Satisfies:** AC2; Approach strand B
- **Depends on:** —

### 148. Coverage audit and top-up to the gate
- **Files:** wherever the audit points (expected: `internal/app/scheduleapp/`, `internal/app/clusterapp/`, `internal/app/metricsapp/`, remaining httpapi/mysql stragglers)
- **Criteria:** run `make cover-gate` locally (Docker lane) + the artifact method (per-package gaps via `go tool cover -func`) on the combined branch; if total <90.5%, add tests strictly in value order from the ranked long tail until ≥90.5% (working target ≈91%); record the final number, the before/after per-package table, and the list of formerly-0% functions now covered (the proof the gain is real tests — AC2's verification clause); no changes to `scripts/coverage.sh`, threshold, or coverpkg.
- **Satisfies:** AC2 (the gate itself); Approach strand B (audit step)
- **Depends on:** 143, 144, 145, 146, 147

## Group C — Dependabot drain and verification

### 149. Apply go_modules + grafana bumps, retarget dependabot, close the PRs
- **Files:** `go.mod`, `go.sum` (k8s.io/api+apimachinery+client-go 0.36.3, prometheus/client_golang 1.24.1, testcontainers/mysql 0.44.0), `grafana/Dockerfile` (image pin 12.4.4→13.1.3, new digest), `.github/dependabot.yml`
- **Criteria:** go_modules bumps applied on feat/honryu (equivalent to PRs #190–#193, #196) with `go mod tidy` clean, then the full bar green — `make lint`, `make test`, integration lane, **and `make cover-gate`**: these bumps touch the scheduler adapter (k8s.io) and the metrics sink (prometheus), so a signature change can move statement counts, and task 150's acceptance criterion is a coverage number. Headroom is comfortable (148 measured **92.3%** against a 90 threshold), so this is confirmation, not a risk.
  **Grafana: take the 13.1.3 bump** (resolved 2026-08-17 on evidence, superseding this task's original "bump-or-defer" choice). `grafana/dashboards/honryu.json` renders `"type": "grafana-piechart-panel"` — the vendored **AngularJS** plugin (v1.3.3, `grafana/plugins/`). Angular was defaulted off in Grafana 11 and removed in 12, so that panel is already unsupported on the *currently pinned* 12.4.4; 13.1.3 breaks nothing new and deferring protects nothing. The dashboards are also `schemaVersion: 16` (Grafana-6 era), relying on auto-migration that has never been exercised because **Grafana has never actually run in the homelab** (no monitoring namespace). Migrating the panel to Grafana's built-in `piechart`, dropping the vendored plugin, and verifying a *rendered* dashboard is handed to **phase 16**, which is the phase that deploys Grafana and can therefore test the claim.
  `dependabot.yml` gains `target-branch: develop` on the gomod, docker, npm, and github-actions ecosystems; then close #184–#188, #190–#193, #196, #198 each with a comment pointing at the applying phase-14 commit. Closing #184–#188 as already-applied is **verified honest**: the SHAs task 142 pinned are byte-identical to dependabot's (`setup-go@924ae3a1`, `codeql-action@99df26d4`). **Do not close any PR whose bump is deferred** — closing a dependabot PR unmerged suppresses recreation for that version, so a deferred dependency would go unwatched until its next release; leave it open as the tracking artifact instead. After the phase merge to develop, confirm dependabot does not reopen the closed set.
- **Satisfies:** AC4; Approach strand C (dependency half)
- **Depends on:** 142 (actions PRs closed only after their bumps are applied)

### 150. PR-lane verification, phase merge, PR #197 green, findings
- **Files:** findings appended to `spec.md` ("Live verification findings", phase 7/10–13 format); no production files
- **Criteria:** **before pushing**, confirm the tree is clean and `web/dist/.gitkeep` is present — a `bun run build` deletes it locally, and if that deletion is swept into a commit (`git add -A`, `git commit -a`, `git add web/`) then `go:embed all:dist` fails on fresh checkouts and turns #197 red for a reason unrelated to this phase. Stage explicit paths, never `-A`. Then push feat/honryu and open the feat→develop PR: on the PR, CI green (lint+unit, integration, coverage gate **≥90.5%** at threshold 90 — measured **92.3%** locally at task 148) and Security green (govulncheck 0, gosec/trivy/grype/hadolint/scorecard pass), with **zero Node-20 deprecation annotations** on any job (screenshot-checked task list); merge to develop with the conventional `chore: merge feat/honryu (phase 14 ci fixes) into develop` (--no-ff); auto-pr updates #197 and its full suite — CI, Security, CodeQL on main-target — goes green (the phase's Goal); local bar re-proven on develop post-merge; findings record final coverage %, actions versions, dependabot dispositions, and any oddities. If the CI coverage job times out rather than failing on a percentage, the fix is a larger `-timeout` (raised to 60m in `e53fae9` precisely because the 30m figure was measured on a developer box), **never** a threshold or scope change.
- **Satisfies:** AC1–AC5; Goal
- **Depends on:** 141, 142, 148, 149
