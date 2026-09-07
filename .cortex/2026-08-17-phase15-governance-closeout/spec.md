# Phase 15 — Governance closeout: merge-time enforcement, artifact truth, credential teardown

Agreed via brainstorm 2026-08-17, following a review of phases 10–13 against the
parent spec (`.cortex/2026-07-30-honryu/spec.md`). This phase adds **no product
capability**. It closes a loop: the three phases that landed between 2026-08-15
and 2026-08-17 each breached or outran a governing constraint, and the mechanisms
that allowed it are still in place.

## Problem

**1. The coverage constraint was breachable, and got breached three times.**
The parent spec makes ≥90% coverage a *constraint* (`spec.md:48`) and a
non-functional acceptance criterion (`:213`). It has been red since 2026-07-12
(CI run 29190837185), sitting at 88.6% when phases 10–12 merged. The mechanism:
`ci.yml` and `security.yml` trigger on `push: branches: [main]` and
`pull_request:` only — so a local `feat→develop` merge, which is how phases 10,
11, 12 and 13 all landed (`chore: merge feat/honryu (phase N …) into develop`),
runs **no CI whatsoever**. The code's first gate is the develop→main promote PR
(#197), by which point it is already on develop. Compounding it, the working
branch is not pushed during a phase either (7 unpushed commits on `feat/honryu`
at the time of writing), so no CI-side instrument fires unless pushing becomes
part of the ritual. Phase 14 diagnosed this correctly and explicitly left the
fix out of scope ("Process gap worth flagging (not this phase's work)").

**2. The parent spec no longer describes the project it governs.** Resolved
decision #7 still reads "BYOC is later, designed for as a seam only" (`:226`,
restated at `:50` and `:144`) — while phase 12 shipped per-cluster ingest
authentication and ran a live BYOC dogfood. The phasing table stops at Phase 9
(`:298–309`), so phases 10–14 are invisible to any reader of the governing
document. Phase 10 set the correct precedent by returning to amend decision #10
(`:236`); phase 12 did not. Separately, phase 13 never engages the parent
non-goal most relevant to it — "Not replacing Grafana/Prometheus with custom
observability" (`:40`) — while adding trend tables, sparklines and a live view;
its own "read-only, no charting library" non-goals are what keep it on the right
side of that line, incidentally rather than by decision. This matters because
every phase spec opens by citing the parent for authority, so a stale decision
misleads the *next* brainstorm.

**3. A customer credential survives deregistration, and no phase owns it.**
`DELETE /api/clusters/{name}` removes the registry row but leaves the
materialized kubeconfig Secret on the operator's cluster. Registration's
*rollback* paths clean it up; Delete does not (phase 8 scoped Delete to the
registry entry). Found live in phase 12's dogfood, hit **again** in phase 13's
cleanup ("204 — and the credential Secret was again left behind; the phase-12
gap persists"), deferred by phase 12, out-of-scoped by phase 14. Both known
instances were removed by hand. For the SaaS direction phase 12 advanced, a
customer's cluster credential outliving their deregistration is a compliance
problem, not a tidy-up.

## Goal

Make the derail impossible to repeat, the governing spec true again, and the
orphaned credential defect owned — then stop.

## Non-goals

- **No branch protection, no mandatory PR lane.** One committer; the gate must
  make failure visible before a merge, not impose ceremony.
- **Not re-opening the coverage number.** Phase 14 task 148 owns reaching
  ≥90.5%. Phase 15 assumes it landed and only makes the bar *enforced at merge
  time*.
- **No threshold or `coverpkg` change**, in either direction.
- **No Gatling.** Parent open question #1 stays open with its stated JDK/Scala
  reason; this phase only makes the deferral explicit rather than forgotten.
- **No tenancy *implementation* work.** The stance is recorded (below); verifying
  auth/RBAC and tenant scoping on the endpoints the SPA consumes is handed to
  phase 16 as a precondition of exposing them at a domain. No endpoint changes
  here.
- **The parent spec does not become a changelog.** Amend decision #7 and extend
  the phasing table; do not restate what the phase specs already say.
- **No new product capability, no new endpoint, no UI work.**

## Constraints

- **Phase 14 must land first** (tasks 148–150). It owns `ci.yml` (task 142) and
  `dependabot.yml`, and a merge gate is meaningless while the gate itself is red.
- Secret-teardown work crosses `clusterapp` → the credentials port, so it needs
  an in-memory fake plus a shared conformance case the real adapter also passes
  (parent non-functional AC `:213`).
- The audit sweep needs the real cluster (`/home/coder/.kube/config`,
  `admin@talos-homelab`, namespace `honryu`).
- Scripts follow the existing `scripts/coverage.sh` idiom: a shell script the
  Makefile wraps, not logic buried in a Make recipe.
- Repo convention: one task per commit, conventional messages, live findings
  appended to this spec in the phases 7/10–13 format.

## Approach

### Strand A — merge-time enforcement

1. **`scripts/phase-merge.sh` + `make phase-merge PHASE="phase N slug"`.** The
   phase-close ritual, where the merge *is* the check so it cannot be skipped
   separately. In order: refuse a dirty working tree; refuse unless on the
   working branch; assert `web/dist/.gitkeep` is present (a staged deletion
   breaks `go:embed all:dist` in fresh checkouts — phase 13 AC4, and the tree
   carries that deletion today); run gofmt/vet/golangci-lint, `make test`, then
   `./scripts/coverage.sh` at the unchanged threshold; only on success perform
   `git merge --no-ff` into develop with the conventional phase message. It
   **never pushes** — pushing stays a separate, deliberate act.
2. **`feat/**` push backstop.** Add `feat/**` to `ci.yml`'s `push.branches` so a
   pushed working branch is checked even outside the ritual. `security.yml` is
   deliberately left alone: it already runs on PRs plus a weekly schedule, and
   govulncheck on every feat push buys little for the Actions minutes. The
   accepted cost is a duplicate run in the rare case a feat→develop PR is open
   at the same time.

### Strand B — artifact truth

3. **Amend parent decision #7** with a dated "Refined by Phase 12" paragraph,
   exactly as decision #10 carries its phase-10 refinement: the BYOC *data
   plane* (per-cluster ingest tokens, hashed at rest, batch→cluster scoping)
   shipped in phase 12; customer-self-service and tenant-scoped registration
   remain later.
4. **Extend the parent phasing table with phases 10–14**, one line each
   (delivers / why here), so the governing document stops implying the project
   ends at Phase 9.
5. **State the SPA/Grafana boundary** in phase 13's spec: the operator SPA is
   read-only navigation over the existing API — no charting library, no metric
   storage, no replacement for Grafana dashboards; Prometheus/Grafana remain the
   live-metrics surface per parent AC `:196`.
6. **Record the carried-forward items** in the parent spec's open questions so
   they cannot evaporate: the SPA tenancy stance, and Gatling (already #1).
7. **Document `cluster` on the `Report` schema** in `api/openapi.yaml`. Same
   class of defect — the contract not describing the wire: `domain/report.Report`
   marshals `cluster`, phase 13's UI types and renders it, and `openapi_test.go`
   only checks routes and tags, so nothing catches the omission. One line;
   strike it from scope if it belongs to a product phase instead.

### Strand C — credential teardown (security-grade)

8. **Origin-gated teardown.** `Delete` tears the materialized Secret down via the
   credential store's existing `Delete` — **only when
   `Origin == clusterregistry.OriginBYOC`**. An operator entry's Secret is
   out-of-band infrastructure Honryu only *reads*
   (`clusterapp/service.go:112-115`), so removing one on deregistration would
   destroy operator infrastructure: a worse defect than the leak this phase
   fixes. *Corrected at plan time:* `CredentialStore.Delete` already exists, is
   already idempotent, and is already used by `RegisterBYOC`'s rollback
   (`k8s/credential.go:156-166`, `clusterapp/service.go:180`) — no port widening
   is needed, and the original "one shared teardown path" wording, taken
   literally, would have shipped the operator-Secret regression.
9. **Ordering, decided:** tear the Secret down **first**, then delete the
   registry row. A teardown failure aborts the delete with both row and Secret
   intact — safe and retryable. The reverse order is what produces exactly
   today's bug. Deleting a cluster whose Secret is already absent must succeed
   (idempotent), so a partial previous attempt self-heals on retry.
10. **Pinned by tests, not vigilance:** `clusterapp` unit tests over its existing
    `fakeCredStore` (`service_test.go:33`) assert the orchestration order, the
    origin gating (an operator cluster's Secret survives deletion), the
    already-absent tolerance, and the abort-on-failure behavior. *Corrected at
    plan time:* `CredentialStore` is an app-level Deps interface, not a `ports`
    interface, so there is no `repositorytest` conformance suite to extend; the
    adapter's own tests already cover its half.
11. **Live audit sweep:** on the real cluster, list Secrets matching the
    materialized naming pattern and cross-reference against registry rows,
    reporting any orphan. Both known instances were hand-deleted, so the
    expected result is zero — the deliverable is *proving* absence. Then one
    register→delete cycle live, confirming nothing is left behind.

### Rejected alternatives

- **Branch protection requiring a feat→develop PR.** Strongest guarantee and
  cannot be forgotten, but imposes a PR plus a ~12-minute Docker gate on every
  phase merge for a single committer. The weakest instrument that makes failure
  visible before a merge is the right one.
- **A `pre-push` git hook.** Hooks are not versioned by default, are bypassed
  with `--no-verify`, and fire on push — which is not when the damage happens
  here, since the merge is local.
- **CI-side enforcement only** (push triggers, no local ritual). Gives no signal
  during the long local stretches when the branch is not pushed, which is
  precisely the window phases 10–13 lived in.
- **Do nothing; fix the leak inline during phase 14.** Rejected: the artifact
  staleness compounds with every phase that cites the parent for authority, and
  the credential defect has already been orphaned twice.

## Acceptance criteria

1. `make phase-merge PHASE=…` refuses, with a stated reason and a non-zero exit,
   on each of: dirty working tree, wrong branch, missing `web/dist/.gitkeep`,
   failing gofmt/vet/lint, failing tests, coverage below threshold. Each refusal
   path is exercised and shown.
2. On success it produces the conventional `--no-ff` phase merge commit into
   develop and does **not** push.
3. A push to a `feat/**` branch triggers the CI workflow, proven by a real
   pushed commit and its run; `security.yml` is unchanged and the reason is
   recorded.
4. Parent decision #7 carries a dated phase-12 refinement naming what shipped
   and what remains later; the parent phasing table lists phases 10–14.
5. Phase 13's spec states the SPA/Grafana boundary; the parent spec's open
   questions carry the SPA tenancy stance and Gatling as explicitly deferred.
6. `DELETE /api/clusters/{name}` removes the materialized credential Secret **for
   a BYOC entry**, and leaves an **operator** entry's Secret untouched (a named
   test asserts this); deleting a cluster whose Secret is already absent
   succeeds; a teardown failure aborts the delete leaving row and Secret intact
   and succeeds on retry. Covered by `clusterapp` unit tests over its existing
   `fakeCredStore`, plus the credential adapter's existing tests.
7. Live: an audit of the real cluster reports no orphaned credential Secret
   without a registry row, and a register→delete cycle leaves none behind.
   Findings appended to this spec.
8. `api/openapi.yaml`'s `Report` schema documents `cluster` (if kept in scope).
9. Standard bar green after the change: gofmt/vet/golangci-lint, unit race
   tests, MySQL conformance, e2e, coverage gate at its phase-14 level.

## Live verification findings (task 157, 2026-08-17)

Live audit and register→delete cycle on the real Talos cluster
(`admin@talos-homelab`, ns `honryu`), API image
`registry.pve.heri.life/honryu/honryu-api:phase15` (built and rolled out this
task from `feat/honryu` at `b34881e`, task 156's fix). **The phase-12 leak is
closed and proven closed live.**

### Before rollout: sweep of the currently-running (pre-fix) deployment

`GET /api/clusters` → `[]`; `kubectl -n honryu get secrets` showed only the four
pre-existing Secrets (`cluster-honryu-explicit-creds`, `honryu-ingest`,
`mysql-credentials`, `registry-pve-heri-life`) — no `honryu-cluster-*` pattern
present. **Zero orphaned credential Secrets**, matching phase 13's cleanup note
that both known instances (`dogfood-byoc` from phases 12 and 13) had already
been removed by hand. The sweep found nothing to clean up; its value was
evidence of absence, as the plan anticipated.

### Rollout

`docker build` from `deploy/honryu/Dockerfile` → pushed as
`honryu-api:phase15` → `kubectl set image` → `rollout status`: successfully
rolled out, `/healthz` green, `GET /api/clusters` still `[]` post-rollout
(confirms the rollout didn't silently inherit or fabricate state).

### Register→delete cycle, proving the fix

`POST /api/clusters` with the homelab's own self-contained kubeconfig (same
dogfood pattern as phase 12) registered `phase15-audit`: **201**, origin
`byoc`, `secret_ref honryu-cluster-phase15-audit`, one-time token minted.
Confirmed materialized: `kubectl get secret honryu-cluster-phase15-audit` →
present, `Opaque`, created at registration time.

`DELETE /api/clusters/phase15-audit` → **204**. Immediately after:

| Check | Result |
|---|---|
| `kubectl get secret honryu-cluster-phase15-audit` | `Error from server (NotFound)` |
| `GET /api/clusters/phase15-audit` | `404 {"message":"ports: not found"}` |
| `GET /api/clusters` | `[]` |

Both the Secret and the registry row are gone in the same delete — the exact
outcome phases 12 and 13 could not produce (a 204 that left the Secret behind).
The origin-gated design holds in production, not just against the fake: only
`RegisterOperator`-origin clusters would have skipped the teardown, and this was
a BYOC entry.

### Cleanup

`phase15-audit` fully removed by its own delete call (nothing left to clean up
manually, unlike phases 12/13). Final `kubectl -n honryu get secrets` shows only
the four pre-existing Secrets. Port-forward stopped. `honryu-api` Deployment
`1/1` Ready, still on `honryu-api:phase15`.

## Resolved decisions (2026-08-17, binding)

1. **`make phase-merge` does not push.** Merging and publishing stay separate
   acts; the script prints the exact `git push` command on success. Noted while
   deciding: `origin/feat/honryu` is 29 commits behind local and phases 10–14 all
   ran that way — that is a *backup* exposure, not a gate weakness, and is
   addressed by pushing regularly (which the `feat/**` backstop then rewards with
   CI), not by welding a publish step onto the merge.
2. **Operator-SPA tenancy stance, recorded here:** the SPA is embedded in the API
   binary and calls the same API under the same auth, so **it enforces nothing
   itself and cannot leak what the API will not serve**. Its obligation is only
   that it must never present an aggregate the API cannot scope. There is no
   separate SPA tenancy model to design — the real question is API-side, and it
   becomes load-bearing the moment phase 16 exposes the API at a domain.
   Therefore: **verifying auth/RBAC and tenant scoping on every endpoint the SPA
   consumes — `GET /api/clusters` first, since it serves `api_url`, `namespace`
   and `secret_ref` — is a precondition of phase 16's exposure work**, not a
   phase 15 task.
3. **`security.yml` is not extended to `feat/**`.** It already runs on PRs and a
   weekly cron; govulncheck's signal concerns dependency/toolchain CVEs that move
   on a weekly timescale, and phase 14 is the proof that lane catches them.
4. **The openapi `cluster` fix stays in phase 15** — not because it is cheap, but
   because phase 16 publishes this API at a hostname, at which point
   `openapi.yaml` becomes the public contract. Correcting it before publication
   is materially different from correcting it after.

## Closeout (task 158, 2026-08-17) — honest deviation record

Task 158's criterion was to close the phase through its own gate —
`make phase-merge` as the gate's first real-world use. That did not
happen, and this section records why rather than reenacting it.

### (a) Tasks 151–157: verified done

| Task | Delivered | Evidence |
|---|---|---|
| 151 phase-close gate | `scripts/phase-merge.sh` + `make phase-merge` | `6cb2af1` |
| 152 CI on pushed feat branches | `feat/**` added to ci.yml push trigger | `5b9a57e` (superseded — see (d)) |
| 153 parent-spec amendments | decision #7 refinement + phasing table 10–14 | artifact edit (`.cortex/` is gitignored; no commit by design) |
| 154 SPA/Grafana boundary + carried deferrals | phase-13 spec boundary statement; parent open questions | artifact edit, as above |
| 155 `cluster` on Report schema | `api/openapi.yaml` | `a7746f1` |
| 156 BYOC Secret teardown | origin-gated, Secret-first ordering, unit-pinned | `b34881e` |
| 157 live audit + register→delete | zero orphans; clean cycle on real cluster | findings above (spec line 204), image `honryu-api:phase15` from `b34881e` |

### (b) Deviation: no dedicated phase-15 merge exists

Phase-15's commits were authored on `feat/honryu` **before** phase 14's
merge landed, so phase 15 entered develop **inside phase 14's merge
commit** `d3f76dc` ("chore: merge feat/honryu (phase 14 ci fixes) into
develop", via PR #199) — verified: `6cb2af1`, `5b9a57e`, `a7746f1`,
`b34881e` are all ancestors of `d3f76dc`. Consequently
`make phase-merge` was **never exercised on this phase's own merge**: the
gate could not gate its author phase, because by the time it existed as a
ritual the phase was already merged. The enforcement actually applied to
these commits was the phase-14 PR lane (CI lint/unit, coverage gate
**92.3% ≥ 90**, Security, CodeQL — all green on `cd54b4d`), which is
equivalent in strength but is not the instrument this phase built.

### (c) The gate's first real-world use: reassigned to phase 16

Phase 16's closeout tasks explicitly close through
`make phase-merge PHASE="phase 16 homelab deployment"`; its first
real-world use — including the coverage figure it enforces and its
refusal paths, should any fire — is thereby reassigned to phase 16's
close. Until then the gate is verified only by its own unit/refusal-path
checks (task 151), not by production use.

### (d) Superseding note: task 152's `feat/**` push trigger removed

Task 152 added `feat/**` to ci.yml's push trigger (`5b9a57e`). Phase 14
later **deliberately removed it** (`1ed3925`) after live evidence showed
push+pull_request double-runs that a concurrency group could not dedupe
(~3s registration race; `eb67d26`/`dca8008` were the intermediate
attempts). The standing mechanism since phase 14 is: **PR-only triggers
plus a concurrency group** (`ci-${{ github.head_ref || github.ref_name }}`,
cancel-in-progress) — every phase closes through a feat→develop PR since
phase 14, so pull_request alone covers what 152's push trigger did, and
phase-merge's printed `git push` advice feeds that PR. Task 152 is done
in intent (pushed feat branches get CI) by a different, better
instrument than it specified.
