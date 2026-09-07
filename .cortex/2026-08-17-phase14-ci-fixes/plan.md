# Phase 14 — CI green — Plan

**Spec:** `.cortex/2026-08-17-phase14-ci-fixes/spec.md`

## Context

- **Latest stable Go is 1.26.6** (go.dev/dl checked 2026-08-17; 1.27 exists
  only as rc3 — unstable, not pinnable). The six stdlib vulns
  (GO-2026-5026/5972/6088/6089/6090/6218) are all fixed in 1.26.6, so
  "latest stable" and "the security fix" are the same pin.
- **Go-version pin inventory** (complete): `go-version: "1.26.5"` ×5
  (ci.yml:23,47 · codeql.yml:27 · security.yml:24,41);
  `deploy/honryu/Dockerfile:12` `FROM golang:1.26` (floating patch tag);
  `go.mod` `go 1.26.0` with **no toolchain directive**. `.golangci.yml`
  pins no Go version (CI installs golangci-lint v2.12.2 by curl — repo
  rule keeps that pin). No Go pins in Makefile/scripts.
- **Actions pin inventory** (complete): already latest (no open dependabot
  PR ⇒ satisfied): checkout v7 ×6, codecov-action v5, trivy v0.36.0,
  anchore/scan-action v7, hadolint v3.3.0, scorecard v2.4.3. Behind:
  setup-go v5 ×5, upload-artifact v4 ×1 (ci.yml:64), codeql-action v3 ×7
  (codeql.yml init/analyze; security.yml upload-sarif ×5). These three
  families are the Node-20 deprecation sources; their latest majors
  (setup-go v6.5.0, upload-artifact v7.0.1, codeql-action v4.37.0 — the
  versions in dependabot PRs 188/186/184+185+187) run Node 24. No
  workflow sets up Node explicitly, so there is no node-version pin to
  add; runtime Node comes from the actions themselves.
- **Coverage math from the failed run's own cover.out** (artifact of run
  32014537960): 6,886/7,759 statements = 88.6%. ≥90.5% needs 7,022
  covered (**+136 statements**); we target ≈91% (+175) for margin.
  Ranked gaps: mysql repo 235, httpapi 198 (updateCluster handler at
  0%), k8s scheduler 69 (six 0% Router delegation methods +
  scenarioFileKey), lifecycleapp 54 (Purge/FinalizeOrphaned/WithNow at
  0%), cmd wiring ~146 across api(44)/scheduler(24)/calibrator(18)/
  sidecar(18 at 59.1%)+run() branches, long tail ≈120 (scheduleapp,
  clusterapp, metricsapp, adminapp Abort/InScopeExecutions, nexus
  WithClient, sidecar Sent).
- **The ground is prepared for cheap, honest tests**: every cmd already
  has a thin `main()` over an extracted `run()` plus existing test files
  (cmd/scheduler even exposes testable loop functions); httpapi has a
  `handlers_coverage_test.go` precedent; the k8s Router methods are pure
  delegation over a fake-able Scheduler.
- **Dependabot targets main, not develop** (no `target-branch` in
  `.github/dependabot.yml` ⇒ default branch = main). All 11 open PRs
  (#184–#188 actions, #190–#193+#196 go_modules, #198 grafana) are based
  on main: merging them would bypass the develop flow and collide with
  promote PR #197. They must be closed and their changes applied on
  `feat/honryu` instead; `target-branch: develop` is added to
  dependabot.yml so future updates enter through the flow.
- **Pre-merge gating exists already**: ci.yml and security.yml run on
  `pull_request` (any base), so opening a feat/honryu→develop PR runs
  the full bar *before* anything lands on develop — the lane phases
  10–12 skipped by merging locally. This phase uses it; making it
  mandatory is a policy change left out of scope.
- **Resolved open question (spec)**: go.mod gets **both** `go 1.26.6`
  (floor = the fixed release, so no pre-fix toolchain can build the
  module) **and** `toolchain go1.26.6` (explicit default per the binding
  decision; redundant today, honest when either line is bumped later).

## Approach

Three strands — toolchain/runtime (Group A), coverage (Group B),
dependabot (Group C) — then verification. A and B are independent; C's
actions half lands after A only to keep workflow diffs conflict-free.

**A — Go 1.26.6 + Node-24 actions (tasks 141–142).** 141 pins 1.26.6 in
all five workflow spots, pins the Dockerfile builder to `golang:1.26.6`,
and sets go.mod's `go` + `toolchain` directives; verified locally via
GOTOOLCHAIN auto-download (govulncheck clean, golangci-lint v2.12.2
still green) — this alone turns the Security workflow's govulncheck job
green. 142 bumps the three behind-actions families to the exact SHAs
from the dependabot PR diffs (SHA + `# vN` comment form preserved),
killing every Node-20 warning.

**B — Coverage to ≥90.5% by real tests (tasks 143–148).** Value-ordered:
143 takes the trivial delegation/one-liner wins (k8s Router ×6 +
scenarioFileKey, nexus WithClient, sidecar Sent, adminapp
Abort/InScopeExecutions); 144 tests lifecycleapp
Purge/FinalizeOrphaned/WithNow; 145 the httpapi updateCluster handler
plus its top long-tail branches; 146 deepens the cmd run()/loop tests;
147 mines the mysql repo gap with integration-tagged tests (Docker lane,
  `-p 1`); 148 re-measures with the artifact method + local
`make cover-gate` and tops up from the ranked long tail until the gate
reads ≥90.5% (target ≈91%). No threshold change, no scope change.

**C — Dependabot drain + verification (tasks 149–150).** 149 applies
the go_modules bumps (k8s.io ×3, prometheus, testcontainers) and the
grafana image bump on feat/honryu, adds `target-branch: develop` to
dependabot.yml, then closes each PR with a pointer to the phase-14
commit. **Grafana triage resolved 2026-08-17: take 13.1.3.** The
dashboards' vendored `grafana-piechart-panel` is an AngularJS plugin
already unsupported on the currently pinned 12.4.4 (Angular defaulted
off in Grafana 11, removed in 12), so the bump breaks nothing new and
deferring protects nothing; migrating that panel to the built-in
`piechart` and verifying a rendered dashboard passes to phase 16, which
actually deploys Grafana. Deferral is no longer an expected outcome —
and if any bump *is* deferred, its PR stays open, since closing one
unmerged stops dependabot recreating it for that version. 150 runs the bar
through the PR lane (feat→develop PR: CI + Security green on the PR,
including a ≥90.5% coverage-gate job and zero Node-20 warnings), merges
to develop with the conventional phase commit, and watches PR #197's
full suite (CI + Security + CodeQL) go green.

Rejected: pinning go1.27rc3 (unstable); merging dependabot PRs into main
(bypasses develop, conflicts #197); lowering the threshold or carving
cmd/ out of coverpkg; vulnerability suppressions.

## Risks

| Risk | Mitigation |
|---|---|
| golangci-lint v2.12.2 (built with go1.26.5) rejects `go 1.26.6` directive. | Patch versions share minor 1.26 — expected fine; verified in task 141 before anything else lands. Fallback: keep `go 1.26.0` + `toolchain go1.26.6` (still auto-switches local/CI to the fixed toolchain). |
| Local go1.26.5 toolchain drift. | `toolchain go1.26.6` + GOTOOLCHAIN=auto fetches 1.26.6 on first use; task 141 proves `go version` reports it. |
| Coverage estimate overshoots (0% functions are tiny). | Need +136, identified pools total ≈500; task 148 is an explicit measure-and-top-up gate — the phase does not end below 90.5%. |
| mysql integration tests slow/fragile. | They extend the existing dbtest lane (`-p 1`, Docker-gated tags) — no new infrastructure. |
| k8s.io/prometheus minor bumps shift APIs or lint results. | Their dependabot CI runs were green; full bar re-run on the combined branch in task 149 before merge. |
| Grafana 13 breaks dashboard provisioning. | Defer-with-reason is an accepted outcome (AC4); the bump is a dashboard-image pin, not app code. |
| Dependabot re-opens PRs after closes. | Bumps are already applied (versions satisfied) and dependabot.yml retargets to develop in the same phase merge; closes reference the applying commit. |
| Workflow edits conflict between tasks 141/142. | Sequential on the same branch; 142 builds on 141's files. |

## Out of scope

Making feat→develop PRs mandatory (workflow/policy redesign); CodeQL
beyond using it as-is; the cluster-delete credential-Secret gap and the
`/api/admin/executions` empty-array oddity (product phases); grafana
dashboard content work; renovate/bundler alternatives; threshold or
coverpkg changes of any kind.

## Verification

Standard bar (`make test`, `make lint`, gofmt clean) plus the CI-lane
gates this phase exists to fix: on the feat→develop PR — Security
workflow green (govulncheck 0 findings), coverage gate ≥90.5% at
threshold 90, no Node-20 deprecation annotations; after merge — PR
#197's CI + Security + CodeQL all green. Findings (final coverage %,
newly-covered function list, dependabot dispositions) appended to the
spec in the phase 7/10–13 format.
