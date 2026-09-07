# Phase 11 — k6 engine + lifecycle hardening — Plan

**Spec:** `.cortex/2026-08-16-phase11-k6-hardening/spec.md`

## Context

- The k6 pairing is already documented (`deploy/engines/README.md`: k6 binary,
  script-only — "bzt's k6 executor rejects the declarative form") and
  `make engine` already drives k6 locally through the compiler
  (`test/engine/engine_test.go`'s `k6ScriptScenario`). What's missing is an
  image: `deploy/engines/jmeter/Dockerfile` is the pattern — bzt + engine
  binary + `engine/honryu_kpi.py` + `.bzt-rc` + a build-time warm-up run that
  bakes the tool cache ("pods need no network", Phase 7's lesson). k6 needs no
  JVM: the base is bzt+python, with a pinned k6 binary.
- **Fail-fast gap confirmed by inspection:** `compile` guards script-vs-
  requests (`ErrScriptRequired`/`ErrRequestsRequired`, `compile.go:29-30`)
  but never executor-vs-form — a portable (declarative) scenario under
  `executor: k6` compiles fine and dies at the engine at 3am.
  `lifecycleapp.compileShards` branches only on `scenario.Kind`
  (`service.go:676-678`), and `engineOf(coll, s.defaultEngine)`
  (`service.go:308`) picks the executor with no portability check.
- **Trigger readiness precedent exists to copy:** `calibrationapp`'s
  `triggerWhenReady` (`internal/app/calibrationapp/step.go:233-252`) retries
  `Trigger` on exactly `run.ErrNotDeployed`/`run.ErrEnginesNotReady` with
  constants `triggerReadyPollInterval = 2s`, `triggerReadyTimeout = 2m`
  (`step.go:49-57`). The HTTP handler is thinner than thin —
  `triggerExecution` delegates to `lifecycleMutation`
  (`internal/adapters/httpapi/lifecycle_handlers.go:13-16,30-45`), which maps
  domain errors to status codes via `respondError`. The wait belongs inside
  the handler layer (r.Context() gives per-request cancellation; the app
  layer stays pure).
- **How the stranded run happened (task 121, live):** engine pods start bzt
  at deploy; Trigger landed minutes later. `StartRun` opened a run whose
  engine had already sent its Final batch — which arrived while no run was
  open and was discarded. The run then sits `running` (`CurrentRun` row,
  `DerivePhase(·, running=true)` → `PhaseRunning`) with nothing that will
  ever finalize it. Trigger itself has no signal: pods stay Ready forever
  (`sleep infinity` after bzt, task 23c), so `CanTrigger`'s
  `enginesReady == TotalEngines` check happily passes over a corpse.
- **The control plane *does* see the evidence — ingest just throws it away.**
  Batches identify by execution/scenario/shard; `metricsapp` maps them to the
  current run (task 21). A Final batch (carrying the shard's exit code)
  arriving while no run is open is precisely "engines finished, nobody
  triggered" — today unrecorded. Recording it as an *orphan completion*
  (execution-scoped, cleared by the next Deploy) gives Trigger's guard its
  typed error without touching the scheduler port, which genuinely cannot
  know (the exit-code file lives in a pod-local emptyDir, `k8s.go:84-87`).
- **Reconciliation's finalize path already exists:** `metricsapp.Finalize(
  ctx, executionID, runID)` is exported (`metricsapp/service.go:91`) and is
  how Stop finalizes — `stopOutcome` (`service.go:100-126`) derives the
  outcome from shard states (evidence-based, never an invented pass), and
  `finalize` itself is idempotent by design (first `SaveReport` wins,
  `report_store.go`'s duplicate-key-is-success). A sweep is therefore:
  find runs open-but-engineless, call `Finalize` with an error-class outcome
  from whatever shard evidence exists.
- Migrations are at `0046`; the orphan-completion marker claims `0047`.
  `0034_execution_criteria.sql` is the narrow-column precedent.
- Live verification uses the standing Phase 7/10 rig: Talos cluster
  (`admin@talos-homelab`, ns `honryu`), `httpbin.pve.heri.life/headers` echo,
  response-assertion proof **with a negative control** (task 121's
  `Asserion.test_strings` lesson: a malformed assertion fixture passes
  vacuously — k6 assertions are script-side `check`s, so the negative control
  runs the script raw without bzt header injection).

## Approach

Three layers, in dependency order.

**A. k6 engine.** New `deploy/engines/k6/Dockerfile` (bzt + pinned k6 +
KPI reporter + warm-up), registered in the homelab deployment config. The
compile-level fail-fast for portable-scenario-under-script-only-engine
closes the 3am gap: a pure `taurus`/`compile` rule (k6 is the v1 script-only
executor set, matching the engines README's table) surfacing as a typed
error from `Deploy`.

**B. Trigger readiness.** The bounded `ErrNotDeployed`/`ErrEnginesNotReady`
retry moves into the httpapi trigger handler — request-scoped (r.Context()
cancellation), mirroring calibrationapp's constants, with injectable sleep
for tests. Immediate-409 semantics survive as the timeout-expiry path.

**C. Stranded runs.** Two guards: (1) *orphan completions* — `metricsapp`
records execution-scoped Final-arrived-with-no-open-run markers (new table
`0047`, narrow repo methods); `Trigger` refuses with a typed
"re-deploy before triggering" error when a marker exists; `Deploy` clears
markers (a new deploy is genuinely new engines). (2) *reconciliation* — a
`lifecycleapp.Reconcile` pass finalizing open-but-engineless runs via the
exported `metricsapp.Finalize`, evidence-based outcome, idempotent; wired in
`cmd/api` on a config-gated ticker (default on) so it also covers runs
stranded by crashes, which the Trigger guard alone cannot.

Rejected: wall-clock heuristics ("deployment older than hold+margin ⇒
finished") — the exact class of assumption Phase 7's achievedSeconds fix
buried; and a scheduler-port "engines finished" probe — the k8s API cannot
see it (pods Ready forever, exit code pod-local), it would be a fake signal.

## Risks

| Risk | Mitigation |
|---|---|
| Orphan-completion markers going stale (deploy crashed after engines ran, marker persists forever). | Markers are cleared by Deploy, and a marker is only consulted by Trigger — worst case is a typed "re-deploy" error for an execution whose engines are indeed gone. A marker without engines is harmless; a missing marker falls through to reconciliation. |
| Trigger wait holds HTTP workers for up to 2m per request. | Context cancellation on client disconnect; timeout configurable (`HONRYU_TRIGGER_*`); calibrationapp's identical constants have run in production since Phase 7. |
| Reconciliation racing a genuinely-slow engine (Final still in flight) → false error finalize. | Reconcile only touches runs whose *every* shard is engineless per the orphan/scheduler evidence plus a configurable quiet period, and `SaveReport` first-wins makes a late-arriving truth harmless — the evidence-based `WorstOutcome` path already models this (`stopOutcome`). |
| k6 header behavior differs from JMeter (script-only engines may ignore scenario `headers:`). | That's the point of the live gate: verified with echo assertions + negative control, and any non-honoring engine is *named* in the findings per Phase 10's rule — a documented limitation, not a blocker. |
| Compile-level script-only rule encoding engine knowledge in pure domain. | The set mirrors the engines README's pairing table as plain data (`k6` today), table-tested; the alternative (per-deployment config) was rejected — the bzt behavior is engine-inherent, not deployment policy. |

## Out of scope

gatling (separate decision, JDK-constrained), declarative-form support on k6
(bzt's asymmetry is modeled, not worked around), scheduler changes (it
already triggers promptly), any UI (Phase 13), and **no backfill** of
historical stranded runs — reconciliation handles them on sight the first
time it runs, which is enough.

## Verification

Standard bar: gofmt/vet/golangci-lint, unit race tests, MySQL conformance
for the new orphan-completion repo methods, e2e for all three behaviors
(task 127: immediate-trigger-after-deploy succeeds; portable+k6 fails fast;
stranded run reconciles; post-finish trigger returns the typed error), and
`make engine` (k6 lane already exists locally). **Live gate** (task 128) on
the Talos cluster: k6 image built/pushed/registered, k6 native run against
the echo target with negative control, immediate-trigger live, orphan-guard
live (deploy → outwait the hold → trigger ⇒ typed error), findings appended
to the spec exactly as Phases 7/10 did.
