# Phase 19b — review closure: plan

Spec: `.cortex/2026-09-02-phase19-review-closure/spec.md`
Date: 2026-09-02

## Context

Phase 19 landed 17 commits on `feat/phase19-operator-ui`, clean tree. Verified
during review:

- `go build ./...` clean, `make test` exit 0, `make lint` **0 issues**,
  159 web tests green.
- **`cover-gate` 92.1%** against a 90% threshold, with `test/e2e` passing inside
  it (171s). Note this supersedes the long-standing "repo sits at 88.9%" belief.
- `web/dist` **880K**, under the 1MB cap but at 88% of it.
- Live on the cluster: G1 returns executions newest-first with the right fields,
  G2 serves the fragment, G5 returns structured diagnostics, G6 anchors
  `think-time` to line 3 with the exact required wording.

The five defects and two gaps are stated in the spec. The load-bearing code:

- `internal/app/scenarioapp/diagnostics.go` — `uncompiledKeyDiagnostics`
  (triple decode, dead `errors.As`, message wording),
  `InvalidRequestsError.Error()` (uncovered).
- `internal/domain/compile/compile.go` — `compileScenario` reads
  `si.DefaultAddress`, `si.Headers`, `si.Requests`, `si.Timeout`,
  `si.KeepAlive`, and builds `DataSources` from `si.DataPaths`. That last one is
  the whole reason `data-sources` in a fragment is inert.
- `deploy/chart/honryu/values.yaml` + `deploy/chart/honryu-homelab-values.yaml`
  — still on `phase16c`/`phase16`/`phase16b`.

## Approach

Four small commits, ordered so the riskiest lands behind a proof.

**F1 first** — it is the only finding that is wrong *in production right now*,
and it is independent of the Go work. Every `helm upgrade` until it lands is a
loaded gun.

Then the diagnostics fixes (F2–F4), which share one file and one test file, and
whose correctness the new e2e (F5) then exercises end to end.

The modelled-but-uncompiled set is **explicit and hand-maintained**, guarded by
a test that fails if `compileScenario` starts compiling one of them. It cannot
be derived from the struct, because it is a fact about the compile path rather
than about the type — pretending otherwise is how it silently rots.

## Risks

| Risk | Mitigation |
|---|---|
| Reconciling the chart rolls the API and drops the running phase-19 build | Set the chart tags to the image that is *already running* (`phase19-seam` or its successor) rather than an older one, then `helm upgrade` and confirm the pod is not restarted into a different image |
| The uncompiled-key set rots as the compile path changes | F3's guard test asserts the set against what `compileScenario` reads; adding a compiled field without updating the set fails the build |
| Fixing the triple decode changes which diagnostics are emitted | F4 lands after F2/F3 with their tests already green, so a behavioural change shows up as a test failure rather than a silent difference |
| The new e2e is slow and destabilises `cover-gate` | Model it on the existing `portable_scenario_e2e_test.go`, which already exercises the fragment path; the suite runs 171s today with room in the 30m timeout |

## Out of scope

- Any phase 19 UI feature work.
- Reworking `KnownFields` detection (it is correct as far as it goes).
- The `web/dist` headroom question — 880K passes; if a future task needs room,
  that is that task's problem to raise.

## Verification

1. `make lint`, `make test`, `make e2e`, `make cover-gate` ≥ 90%.
2. Live: POST a fragment carrying `data-sources`, `script` and `think-time` to
   the validate endpoint and confirm **three** info diagnostics, each anchored
   to its own line, none naming a Go type.
3. **The G8 ship gate, for real**: deploy an execution whose fragment sets a
   header, trigger it, then read the compiled shard config back through
   `GET /api/runs/{id}/scenarios/{sid}/shards/{shard}/config` and confirm the
   header sits *alongside* `traceparent` and `baggage`. A green deploy does not
   prove this; only the compiled artefact does.
4. `helm get manifest honryu -n honryu | grep honryu-api:` matches the running
   pod's image.
5. CI green on the pushed branch.
