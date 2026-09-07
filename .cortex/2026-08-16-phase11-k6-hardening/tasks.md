# Phase 11 — k6 engine + lifecycle hardening — Tasks

Continues the roadmap's global task numbering from Phase 10's last task (121).
**Spec:** `spec.md` · **Plan:** `plan.md`

## Group A — k6 engine + fail-fast portability

### 122. Build the k6 engine image
- **Files:** `deploy/engines/k6/Dockerfile`, `deploy/engines/k6/bzt-rc.yml`,
  `deploy/engines/k6/warmup.yml`, `deploy/engines/README.md` (k6 row: image
  now exists)
- **Criteria:** bzt + pinned k6 binary (no JVM) + `engine/honryu_kpi.py` KPI
  reporter via `.bzt-rc` (same mechanism as the jmeter image) + a build-time
  warm-up provisioning run that bakes k6 into bzt's tool cache so pods need
  no network; builds as `engine-k6:<k6-version>` with a real tag; a local
  smoke (`docker run` the image, bzt on a tiny k6 script config targeting a
  dead port) proves the executor starts and writes the KPI stream — the
  warm-up run's own log is the evidence.
- **Satisfies:** spec "Approach — k6 engine" step 1; AC1 (image half)
- **Depends on:** —

### 123. Fail fast: portable scenario × script-only engine
- **Files:** `internal/domain/compile/compile.go`,
  `internal/domain/compile/compile_test.go`,
  `internal/domain/taurus/taurus.go` (if the script-only set lives beside
  `Executor`)
- **Criteria:** a pure, table-tested rule — the v1 script-only executor set
  is plain data (`k6`; mirroring the engines README pairing table) — makes
  `compile.Taurus` return a typed error (e.g. `ErrEngineScriptOnly:
  scenario %q is portable but engine %q is script-only`) when a portable
  scenario is compiled under a script-only executor; `lifecycleapp.Deploy`
  surfaces it (no change needed if it already wraps compile errors — verify
  the 400 mapping); a native k6 scenario with a script still compiles; the
  golden fixtures stay byte-identical (none pair k6 with requests).
- **Satisfies:** spec "Approach — k6 engine" step 3; AC3
- **Depends on:** —

## Group B — trigger readiness at the HTTP boundary

### 124. Bounded readiness wait inside the trigger handler
- **Files:** `internal/adapters/httpapi/lifecycle_handlers.go`,
  `internal/adapters/httpapi/lifecycle_handlers_test.go`,
  `internal/config/config.go` (timeout knobs), `cmd/api/main.go` (wire),
  `api/openapi.yaml` (trigger description), `README.md` (config table)
- **Criteria:** `triggerExecution` retries `Lifecycle.Trigger` on exactly
  `run.ErrNotDeployed`/`run.ErrEnginesNotReady` — mirroring
  `calibrationapp.triggerWhenReady` (`step.go:233-252`, constants 2s/2m as
  `HONRYU_TRIGGER_READY_POLL`/`HONRYU_TRIGGER_READY_TIMEOUT`) — with the
  sleep injectable for tests, `r.Context()` cancellation honored
  (client disconnect stops waiting), and timeout expiry returning the last
  error (the existing 409 mapping) promptly — never hanging past the
  deadline. Unit tests: immediate success (no retries), retry-then-success
  via a first-not-ready fake, timeout expiry, context cancellation.
  Scheduler/calibrationapp keep their own retries unchanged.
- **Satisfies:** spec "Approach — Trigger hardening" step 4-5; AC4
- **Depends on:** —

## Group C — stranded runs

### 125. Orphan completions: record Finals that arrive with no open run; Trigger refuses corpses
- **Files:** `migrations/0047_execution_orphan_completion.sql`,
  `internal/ports/run_repository.go` (or a narrow new repo methods block on
  `ExecutionRepository` — same call site either way),
  `internal/adapters/repo/mysql/` (+ conformance),
  `internal/ports/fake/`, `internal/ports/repositorytest/`,
  `internal/app/metricsapp/service.go`,
  `internal/app/lifecycleapp/service.go` + tests
- **Criteria:** when a Final batch (exit code present) arrives for an
  execution with **no open run**, `metricsapp` records an orphan completion
  (execution, scenario, shard, exit code, finished-at) instead of silently
  discarding it; `Trigger` — after its existing readiness checks — returns a
  typed `run.ErrEnginesFinished` ("re-deploy before triggering") when an
  orphan completion exists for the execution; `Deploy` clears the
  execution's orphan rows (new engines, new evidence); mysql + fake pass a
  shared conformance case (record, read, clear); httpapi maps the typed
  error to a 409 with that exact message. Test asserts the task-121
  scenario: Final-then-StartRun ordering is now impossible.
- **Satisfies:** spec "Approach — Stranded-run reconciliation" step 6; AC5
- **Depends on:** —

### 126. Reconcile open-but-engineless runs
- **Files:** `internal/app/lifecycleapp/service.go` (Reconcile method),
  `internal/app/lifecycleapp/reconcile_test.go`,
  `internal/config/config.go` (`HONRYU_RECONCILE_INTERVAL`, 0=off, default
  on), `cmd/api/main.go` (ticker wiring)
- **Criteria:** `Reconcile(ctx)` scans open runs (`RunRepository` listing —
  widen if needed, conformance both adapters), and for each whose every
  shard is engineless (orphan completions present covering the profile, or
  scheduler reports the pool gone) past a quiet period, calls the exported
  `metricsapp.Finalize` with the evidence-based outcome
  (mirror `stopOutcome`'s `WorstOutcome` over shard states; no invented
  passes — engine-side attribution for a no-evidence run). Idempotent:
  second call is a no-op (first `SaveReport` wins, `CurrentRun` already
  closed). The metrics collector is an existing seam on the Service
  (`WithMetrics`) — reuse it, no new wiring. Config-gated ticker in
  `cmd/api`; unit tests with fakes cover: stranded-then-finalized,
  live-engine-untouched, idempotent re-run.
- **Satisfies:** spec "Approach — Stranded-run reconciliation" step 7; AC6
- **Depends on:** 125

## Group D — verification

### 127. e2e — readiness wait, fail-fast, orphan guard, reconciliation
- **Files:** `test/e2e/phase11_e2e_test.go` (phase 9/10 harness pattern),
  possibly `internal/ports/fake/scheduler.go` (a "report not-ready once"
  knob, if the fake's synchronous Deploy doesn't exercise the retry)
- **Criteria:** over real HTTP + real MySQL + fake scheduler: (a) trigger
  immediately after deploy succeeds without client retry (fake made to
  report not-ready on the first status call); (b) a portable scenario
  configured under executor k6 fails deploy with the typed error, 400;
  (c) ingest a Final for an execution with no open run, then trigger ⇒
  409 `ErrEnginesFinished`-shaped message; deploy clears it; (d) an open
  run with orphan completions reconciles to an `error`-outcome report with
  engine attribution, idempotently.
- **Satisfies:** AC3, AC4, AC5, AC6 end to end
- **Depends on:** 123, 124, 125, 126

### 128. Live verification — k6 on the wire, hardened lifecycle, on the Talos cluster
- **Files:** verification notes appended to `spec.md` ("Live verification
  findings", Phase 7/10 format); `deploy/` notes for the homelab
  registration if not already documented
- **Criteria:** build+push `engine-k6:<tag>`, add to the homelab deployment's
  `HONRYU_ENGINE_IMAGES`, roll out; run **one script-native k6 scenario**
  against `httpbin.pve.heri.life/headers` whose script `check`s the echo
  body for `traceparent`/`honryu.run=` — **plus a negative control** (the
  same script under raw k6, no bzt header injection, must fail the checks)
  proving the assertions bind; a k6 run passes end to end with KPIs and a
  finalized report carrying the correlation id. Also live: immediate
  deploy→trigger succeeds first try (no client retry); deploy → outwait the
  hold → trigger returns the typed re-deploy error, no stranded run left
  behind. Any k6 header limitation is **named** in the findings, not
  assumed away. Cluster left clean (pods purged, port-forwards stopped).
- **Satisfies:** AC1, AC2, and the spec's verification bar; Phase 10 task
  121's named k6 debt
- **Depends on:** 122, 123, 124, 125, 126, 127
