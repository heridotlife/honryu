# Phase 19b — review closure

Status: agreed, ready to plan
Date: 2026-09-02
Follows: `.cortex/2026-09-01-phase19-operator-ui/`

## Problem

The phase 19 review found the implementation sound — `make lint` clean,
`cover-gate` at **92.1%** against a 90% threshold, 159 web tests green, and the
two criteria most at risk of being faked (byte-identical form edits, telemetry
winning the header collision) genuinely well tested.

It also found five defects and two verification gaps. One defect is live on the
cluster right now.

### The findings

1. **Deployment drift, live (high).** The running pod is
   `honryu-api:phase19-seam`; helm's stored manifest renders `phase16c`, and
   `deploy/` is untouched across the whole 50-file phase 19 diff. The Deployment
   was changed outside helm, so **the next `helm upgrade` silently reverts the
   API**, removing `GET /api/executions` and the new SPA. This is the same class
   as the ConfigMap that never reached its pods — except the drift already
   exists.

   **Scope expanded during F1's execution (2026-09-03), confirmed with the
   user.** Two further things surfaced: a scratch `honryu-api-seam`
   Deployment/Service left outside helm entirely (already crash-looping, not
   traffic-serving — safe debris); and a second, more serious instance of the
   same class of bug. `cmd/scheduler` (`scheduler/main.go:199`) and
   `cmd/calibrator` (`calibrationapp/step.go:186`) are separate binaries that
   also call `lifecycleapp.Deploy`, each statically linking their own copy of
   the G8-fixed compile path — but they were still running `phase16b`/`phase16`
   while `honryu-api` ran phase 19 code. A scheduled or calibration-driven
   deploy would silently not get G8's header/timeout/keepalive fidelity while a
   manually-triggered one would. F1 now rebuilds and redeploys all three.

2. **Criterion 15 catches the wrong half (medium).** `uncompiledKeyDiagnostics`
   detects keys via `yaml.Decoder.KnownFields(true)`, which by definition only
   finds keys **absent** from `taurus.Scenario`. But `data-sources` and `script`
   *are* on the struct and are still never compiled from the fragment:
   `compileScenario` builds `DataSources` from `si.DataPaths` (uploaded files),
   never from `frag`. Confirmed live — a fragment carrying `data-sources` on
   line 4 returned **zero diagnostics** while `think-time` on line 3 was
   correctly flagged. An operator parameterises a test in the editor, sees no
   warning, and gets an unparameterised run.

3. **Three strict decodes and a dead statement (low).**
   `uncompiledKeyDiagnostics` strict-decodes the same document three times, and
   `_ = errors.As(isUnknownFieldErrSource(raw), &typeErr)` has no effect —
   `typeErr` is immediately reassigned by the `errors.As(err2, &typeErr)` below.

4. **`InvalidRequestsError.Error()` is never executed (low).** Both handlers
   call `inv.Err.Error()` (the wrapped sentinel), never `inv.Error()`, so its
   `"line %d: %s"` join formatting is at 0% coverage.

5. **A Go type name leaks into operator-facing text (low).** Diagnostics read
   `think-time not found in type taurus.Scenario: …`. `taurus.Scenario` is an
   implementation detail in a message aimed at a person.

### The verification gaps

6. **No phase-19 e2e test.** Every prior phase has one (`phase2`–`phase9`,
   `portable_scenario`); phase 19 has none, so the journey has no automated
   equivalent.

7. **G8's own ship gate was never run.** Its commit message says so: reading a
   compiled shard config back through
   `GET /api/runs/{id}/scenarios/{sid}/shards/{shard}/config`. G8 is
   well unit-tested, but it changed the compile path — the one place a defect
   stays invisible — and it is what makes R5's headers field honest.

## Goal

Close all five defects, give phase 19 the e2e test the repo's convention
expects, and verify G8 through a real deploy rather than a unit test.

## Non-goals

- No new UI features. Phase 19's scope is done; this is closure.
- No change to the diagnostics wire format (`{severity, message, line, col,
  path}`) — it is correct and already consumed by the editor.
- No rework of the `KnownFields` detection itself. It does its job; it is simply
  not the whole job.

## Constraints

- `cover-gate` must stay at or above 90% (currently 92.1%).
- The diagnostics contract is load-bearing for R6's editor rendering; adding
  findings must not change how existing ones serialise.
- The chart fix must be reconciled through helm, not by another out-of-band
  `kubectl` edit — that would recreate the same drift with a newer tag.

## Approach

**Detection gains a second, explicit source.** `KnownFields` finds unmodelled
keys; a small hand-maintained set names the keys that are modelled but not
compiled from the fragment (`data-sources`, `script`). The two feed one
diagnostic list. The set is explicit and commented precisely because it cannot
be derived — it is a fact about `compileScenario`, not about the struct — so it
must be kept in step with the compile path by a test that fails when they
diverge.

**Rejected:** deriving the set by reflecting over which `taurus.Scenario` fields
`compileScenario` reads. It would be clever, fragile, and would still need the
same test to be trustworthy.

## Acceptance criteria

1. A fragment carrying `data-sources` receives an `info` diagnostic anchored to
   its line, stating the key is stored but not compiled.
2. The same holds for `script` in a portable fragment.
3. `think-time` keeps its existing diagnostic unchanged — the `KnownFields`
   path is not regressed.
4. A test asserts the modelled-but-uncompiled set matches what
   `compileScenario` actually reads, and fails if a future change compiles one
   of them without updating the set.
5. `uncompiledKeyDiagnostics` strict-decodes the document **once**.
6. `InvalidRequestsError.Error()` has direct test coverage of both its
   line-anchored and unanchored branches.
7. No diagnostic message contains a Go type name.
8. A `phase19_e2e_test.go` covers the journey: create → requests fragment with
   headers → config → deploy → compiled shard config read back → report.
9. That e2e asserts a fragment header reaches the compiled config **alongside**
   `traceparent` and `baggage`, not instead of them.
10. `deploy/chart/honryu/values.yaml` and the homelab values carry the phase 19
    image tags, and `helm get manifest` matches the running pod's image.
11. `cover-gate` stays ≥ 90%.
12. CI is green on the pushed branch.

## Open questions

1. **Engines run to completion on deploy, before any trigger.** Found while
   running the G8 ship gate on 2026-09-02: `POST /deploy` for execution 5
   brought up `engine-1-5-3-0`, and within ~50s bzt had run the whole profile
   (`g8-shipgate-get OK 100.00%`, `Done performing with code: 0`) and finished.
   The subsequent `POST /trigger` returned **409 `run: engines already
   finished, redeploy before triggering: 1 orphaned shard completion(s)`**, and
   no report was persisted (`GET /executions/5/reports` → `null`).

   The orphan guard behaved correctly — this is phase 11's `ErrEnginesFinished`
   doing its job, not a new defect, and it predates phase 19. But it has a
   direct consequence for phase 19's execution hub: **the Trigger button will
   409 whenever the operator is slower than the engine**, which for a short
   profile is most of the time. Deploy-then-trigger may not be a two-button
   flow at all for short runs.

   Not scoped here. Worth its own brainstorm: is trigger meaningful for a
   profile the engine starts immediately, or should the hub deploy-and-trigger
   as one action with trigger reserved for long-running pools?
