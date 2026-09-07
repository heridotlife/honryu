# Phase 15 — Governance closeout — Plan

**Spec:** `.cortex/2026-08-17-phase15-governance-closeout/spec.md`

## Context (read from the code, 2026-08-17)

- **The defect:** `clusterapp.Delete` (`internal/app/clusterapp/service.go:262-271`)
  guards on active runs, then calls `Registry.DeleteCluster`. It never touches
  the materialized credential Secret. That is the whole of the phase-12 leak.
- **The collaborator already exists.** `CredentialStore.Delete` is declared on
  clusterapp's own Deps interface (`service.go:46-52`), implemented at
  `internal/adapters/scheduler/k8s/credential.go:156-166`, already idempotent
  (`apierrors.IsNotFound` → nil), already used by `RegisterBYOC`'s rollback
  (`service.go:180`), and already covered by adapter tests
  (`credential_test.go:150,156`). No port widening, no new adapter method.
- **Operator Secrets are read, never materialized.** `RegisterOperator`
  (`service.go:112-115`) *reads* a Secret the operator manages out of band and
  fails if it is absent; the Deps doc states it: "Read backs operator
  registration (whose Secret is the source of truth); Materialize backs BYOC."
- **`CredentialStore` is an app-level Deps interface, not a `ports` interface**,
  so no `internal/ports/repositorytest` conformance suite covers it. clusterapp's
  own `fakeCredStore` (`service_test.go:33`) is the test seam.
- **CI triggers:** `ci.yml` and `security.yml` both carry
  `push: branches: [main]` + `pull_request:`; `codeql.yml` restricts
  `pull_request` to base `main`. Nothing runs on a feat branch.
- **Makefile idiom:** targets are one-liners wrapping `scripts/*.sh`
  (`cover-gate:` → `./scripts/coverage.sh`, `Makefile:29-31`), with `##` help
  comments.

## Corrections to the spec, from reading the code

1. **Strand C is far smaller than drafted** — roughly ten lines in
   `clusterapp.Delete` plus tests, not a port widening.
2. **Teardown must be gated on `Origin == clusterregistry.OriginBYOC`, and this
   is a safety requirement.** An operator entry's Secret is out-of-band
   infrastructure Honryu only reads; deleting it on deregistration would destroy
   operator infrastructure — a worse defect than the leak. The spec's "one
   shared teardown path" (step 8), taken literally, would have shipped that
   regression.
3. **Spec AC6's "shared conformance case (fake + real adapter)" does not map.**
   The equivalent rigour is clusterapp unit tests over the existing
   `fakeCredStore` (orchestration order, origin gating, abort-on-failure) plus
   the adapter's existing idempotence tests. The spec's AC is amended to match.

## Approach

**Credential teardown.** `Delete` becomes: fetch the entry (for `Origin` and
`SecretRef`), guard active runs as today, tear the Secret down **only when
`Origin == OriginBYOC`**, then delete the registry row. Secret-before-row keeps
the failure mode safe and retryable: a teardown failure aborts with both row and
Secret intact, and the adapter's not-found tolerance means a retry after a
partial attempt self-heals. The added fetch also preserves today's `ErrNotFound`
for an unknown name. Rejected: row-first (produces exactly today's orphan on
failure) and unconditional teardown (destroys operator Secrets).

**Merge-time enforcement.** `scripts/phase-merge.sh`, wrapped by
`make phase-merge PHASE=…`, following the `coverage.sh` idiom: refuse a dirty
tree, the wrong branch, or a missing `web/dist/.gitkeep`; run
gofmt/vet/lint/tests/coverage gate; then `git merge --no-ff` with the
conventional phase message. It never pushes. A `--dry-run` flag runs every check
and stops before the merge, so the happy path is testable without merging.
Rejected: branch protection (ceremony for one committer), a `pre-push` hook
(unversioned, `--no-verify`-able, and fires at the wrong moment since the merge
is local).

**Ordering.** The gate lands first, so every later task in this phase merges
through the mechanism the phase exists to build, and the phase's own close is the
gate's real-world proof.

## Risks

| Risk | Mitigation |
|---|---|
| Origin gating lost in review ⇒ an operator Secret is deleted | A named unit test asserting operator delete leaves the Secret untouched — the assertion is the deliverable, not the code |
| `phase-merge` performing a real `git merge` surprises or half-merges | Refuse unless the tree is clean, so an aborted run leaves nothing partial; merge is the final step after all checks; never push; `--dry-run` for rehearsal |
| Phase 14 still owns `ci.yml` (task 142) and `dependabot.yml` | Phase 15 begins only after phase 14's merge to develop; the push-trigger edit is one line in a file 14 has finished with |
| Live sweep needs an image build + rollout (phases 12/13 ritual) | Sequenced last, after the fix is committed; expected result is zero orphans, so the deliverable is evidence of absence |
| These corrections re-introduce spec drift | `spec.md` is amended in-phase (task 158) — stale artifacts are the thing this phase exists to fix |

## Out of scope

Branch protection or a mandatory PR lane; changing the coverage threshold or
`coverpkg`; reaching the coverage number itself (phase 14 task 148); Gatling;
deciding the operator SPA's tenancy stance (recorded as deferred); making
`openapi_test.go` assert schema properties as well as routes; any new product
capability, endpoint, or UI work.

## Verification

Per task: `go build`/`vet`/`gofmt`, `golangci-lint`, unit tests with `-race`.
Phase-level: the credential fix proven by unit tests over the fake **and** live
on the real cluster (audit sweep showing no orphaned credential Secret without a
registry row, plus a register→delete cycle leaving none behind); the gate proven
by exercising each refusal path and then closing the phase with
`make phase-merge` itself; artifact amendments proven by reading back the parent
spec's decision #7 and phasing table. Findings appended to `spec.md` in the
phases 7/10-13 format.
