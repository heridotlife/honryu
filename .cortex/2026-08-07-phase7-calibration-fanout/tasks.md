# Phase 7 — Engine calibration and fan-out — Tasks

Global numbering continues Phase 6 (54–69). Phase 7 is **70–85**.

Spec: `.cortex/2026-08-07-phase7-calibration-fanout/spec.md` · Plan: `plan.md`.

Groups: **70–72** domains · **73–76** persistence · **77–80** app service + controller · **81–83** quota/campaign/HTTP · **84–85** e2e + live.

---

## Group A — domains (pure, table-tested, verifiable immediately)

### 70. Add `Kind` to the execution domain — **done, `ff0c557`**
- **Files:** `internal/domain/execution/execution.go`, `internal/domain/execution/execution_test.go`
- **Criteria:** `Kind` type (`Normal` | `CalibrateEngine`); `New` defaults to `Normal`; `Validate` rejects an unknown kind; existing constructors/call sites unaffected.
- **Satisfies:** spec AC1; Approach "New domain — `execution.Kind`"
- **Depends on:** —

### 71. Add the `calibration` search domain (Spec, Job, decision function) — **done, `edd423b`**
- **Files:** `internal/domain/calibration/calibration.go`, `internal/domain/calibration/calibration_test.go`
- **Criteria:** `Spec` (criterion + pod CPU/memory required; seed/ceiling/max-steps/hold defaulted, validated); `Job` state machine (`Pending`→`Bracketing`→`Bisecting`→`Done`/`Failed`, bracket lo/hi, step history); a **pure `Next(job, lastClassification) → Action`** implementing double-to-bracket then bisect, terminating with `saturated_by ∈ {engine, target, neither}`; safety bounds (never exceed max-QPS / max-steps) enforced and table-tested.
- **Satisfies:** spec AC4, AC7, AC8 (bounds); Approach "The search"
- **Depends on:** —

### 72. Add the `capacityprofile` domain + `FanOut` — **done, `842c234`**
- **Files:** `internal/domain/capacityprofile/capacityprofile.go`, `internal/domain/capacityprofile/capacityprofile_test.go`
- **Criteria:** `CapacityProfile{ScenarioID, Engine, CPU, Memory, PerPodQPS, SaturatedBy, ScenarioFingerprint, CalibratedAt, JobID}` + key; pure `FanOut(profile, targetQPS, currentFingerprint) → {Engines, Status}` covering all four statuses (`ok` = `ceil(target/PerPodQPS)`, `target_limited`, `stale`, `no_profile`), table-tested.
- **Satisfies:** spec AC10; Approach "Fan-out"
- **Depends on:** —
- **Corrected during execution:** the spec named four statuses; a fresh profile whose search ended `SaturatedByNeither` (never found either ceiling) didn't fit any of them -- labeling it `target_limited` would falsely claim the target was the bottleneck. Added a fifth status, `StatusInconclusive`.

## Group B — persistence

### 73. Persist execution kind + pinned pod resources, wire into the deploy path — **done, `43b9468`**
- **Files:** `migrations/0035_execution_kind_resources.sql`, `internal/adapters/repo/mysql/execution_repository.go`, `internal/ports/fake/repository.go`, `internal/ports/repositorytest/scenario_execution_contract.go`, `internal/app/lifecycleapp/service.go`
- **Criteria:** `execution.kind`, `cpu`, `memory` columns round-trip through create/get; `compileShards` sets `DeploySpec.CPU/Memory` from the execution (empty for existing rows → unchanged behavior); conformance contract covers it.
- **Satisfies:** spec AC1; Constraints "Pod size is pinned and keyed"
- **Depends on:** 70

### 74. Add `CalibrationJobRepository` with a row-locked next-step claim — **done, `296cda3`**
- **Files:** `migrations/0036_calibration_job.sql`, `migrations/0037_calibration_job_step.sql`, `internal/ports/calibration_repository.go`, `internal/adapters/repo/mysql/calibration_repository.go`, `internal/ports/fake/calibration_repository.go`, `internal/ports/repositorytest/calibration_contract.go`, `internal/adapters/repo/mysql/calibration_integration_test.go`
- **Criteria:** create/get/list jobs + append/read steps + update state; a `ClaimNextStep`-style **`SELECT … FOR UPDATE LIMIT 1` + status transition in one tx** (mirroring `ClaimDueOccurrence`); contract proves two concurrent claims never return the same job.
- **Satisfies:** spec AC3, AC13; Approach "Persistence", "Multi-controller safety"
- **Depends on:** 71
- **Corrected during execution:** the plan's single `0036_calibration_job.sql` held two `CREATE TABLE` statements; this codebase's migration runner applies one statement per file (`internal/adapters/repo/mysql/migrate.go`'s own documented convention). Split into `0036_calibration_job.sql` + `0037_calibration_job_step.sql`, which pushed task 75's migration to `0038` and task 77's to `0039` (already updated below). Also: the claim is a **lease** (`claimed_at` + a caller-supplied `leaseFor` duration), not the row lock alone -- a step's real-world run (Deploy/Trigger/hold/Stop) takes minutes, far longer than one transaction, so `phase` alone (unlike `schedule_occurrence.status`) can't double as the claim marker.

### 75. Add `CapacityProfileRepository` (upsert-by-key, get, list) — **done, `fa63922`**
- **Files:** `migrations/0038_capacity_profile.sql`, `internal/ports/capacity_profile_repository.go`, `internal/adapters/repo/mysql/capacity_profile_repository.go`, `internal/ports/fake/capacity_profile_repository.go`, `internal/ports/repositorytest/capacity_profile_contract.go`, mysql integration test
- **Criteria:** upsert replaces the profile for a `(scenario, engine, cpu, memory)` key; get-by-key and list round-trip; contract covers replace-on-recalibrate.
- **Satisfies:** spec AC5, AC6; Approach "Persistence"
- **Depends on:** 72

### 76. Add the scenario content fingerprint helper — **done, `86b9ef7`**
- **Files:** `internal/app/scenarioapp/service.go` (or a shared reader package), `*_test.go`
- **Criteria:** `ScenarioFingerprint(ctx, scenarioID)` = deterministic hash over `ScenarioFilesFor` bytes (sorted by filename) + `GetScenarioRequests`; identical content → identical hash; any byte change → different hash.
- **Satisfies:** spec AC11; Approach "Scenario fingerprint"
- **Depends on:** —
- **Corrected during execution:** `ScenarioFilesFor` never errors, even for an unknown scenario id -- caught while writing the NotFound test. Without an explicit `GetScenario` existence check up front, a deleted scenario would have silently fingerprinted as "empty" rather than erroring, risking a false-freshness match against a profile calibrated when the scenario likewise had no content yet.

## Group C — calibration app service + controller (the search)

### 77. Add `calibrationapp`: create/trigger/get/list a calibration job — **done, `72a07df`**
- **Files:** `internal/app/calibrationapp/service.go`, `internal/app/calibrationapp/service_test.go`, `migrations/0039_calibration_spec.sql`
- **Criteria:** creating a calibration validates the `Spec` (criterion via existing `execution_criteria`; pod size + search bounds persisted; **criterion & pod-size required**); triggering a `CalibrateEngine` execution creates a `Pending` job; get/list expose state + step history.
- **Satisfies:** spec AC2, AC3; Approach "New domain / calibrationapp"
- **Depends on:** 71, 73, 74

### 78. Add the one-step runner (drive a single 1-pod run at a chosen QPS) — **done, `0f502ad`**
- **Files:** `internal/app/calibrationapp/step.go`, `internal/app/calibrationapp/step_test.go`
- **Criteria:** given a job + a requested QPS, rewrite the calibration execution's profile to `{1 engine, throughput=QPS, concurrency high, duration=hold}` at the pinned pod size, `Deploy`→`Trigger`→(hold)→`Stop`→`GetReport(runID)`, returning the settled report; verified against the fake scheduler + a scripted report.
- **Satisfies:** Approach "The step-execution mechanism"; spec AC9 (1 pod, pinned size, via Trigger)
- **Depends on:** 73, 75, 77
- **Corrected during execution:** the requested-QPS-to-concurrency mapping is not named in the plan ("concurrency high" only) -- a flat constant would itself become the bottleneck at high QPS, false-reading as engine saturation. Scales concurrency with requested QPS (floor 20, 2 VUs/QPS, Little's Law headroom) instead. `StepRunner` is its own type (not a `Service` method) per service.go's task-77 doc comment pointer; task 79 wires it into `Service`.

### 79. Add `AdvanceOne`: classify, decide, persist, and write the profile on terminal — **done, `a15868b`**
- **Files:** `internal/app/calibrationapp/service.go`, `internal/app/calibrationapp/service_test.go`
- **Criteria:** claim a pending-step job (row-locked), run the step (78), classify the report (`ShortOfRequest`/`EngineImpaired` → engine; `EvaluateCriteria` trip → target; safety bound → neither), feed `calibration.Next` (71), persist the step + next state, retry once on an anomalous engine-short, and on terminal write the `CapacityProfile` (75) with the fingerprint (76); full state machine covered engine-limited / target-limited / neither.
- **Satisfies:** spec AC4, AC5, AC6, AC7, AC8; Approach "The search"
- **Depends on:** 74, 75, 76, 78
- **Corrected during execution:** spec.md/plan.md's lines 178-184 turned out to require writing a `CapacityProfile` for **every** terminal outcome (engine, target, *and* neither), not just engine-saturation -- confirmed by task 72's own `StatusInconclusive` (FanOut has nothing to read back if a "neither" profile is never stored). `Runner`/`ScenarioFingerprinter` are optional `With*` collaborators (lifecycleapp's own pattern) rather than new required `NewService` params, so task 77's 19 existing test call sites needed no changes.

### 80. Add `cmd/calibrator` + a scheduler-optional calibration loop — **done, `df9ccbc`**
- **Files:** `cmd/calibrator/main.go`, `cmd/calibrator/main_test.go`, `cmd/scheduler/main.go`, `internal/config/config.go`
- **Criteria:** `cmd/calibrator` ticks `AdvanceOne` over a `WaitGroup`-joined loop; the loop is factored so `cmd/scheduler` can host it behind an **off-by-default** config flag; a test proves the loop ticks and stops on context cancel.
- **Satisfies:** spec AC3, AC13; Approach "Wiring"
- **Depends on:** 79
- **Corrected during execution:** "factored so cmd/scheduler can host it" can't mean a shared function -- `cmd/calibrator` and `cmd/scheduler` are separate `package main`s, and Go disallows importing one main package from another. Each hosts its own small `runCalibratorLoop`/`advanceCalibrationOnce` pair (identical shape, ~20 lines), matching cmd/scheduler's own existing convention of one small ticker-loop function per concern (`runLoop`, `runHorizonLoop`, `runDrainLoop` already do this) rather than factoring a generic helper.

## Group D — quota, campaign exclusion, HTTP

### 81. Reserve engine-equivalents for a calibration step — **done, `87677c9`**
- **Files:** `internal/app/calibrationapp/step.go` (or the lifecycle seam it uses), `internal/app/quotaapp/*` if a signature change is needed, tests
- **Criteria:** a step reserves `ceil(pod_resources / baseline_engine_size)` engine-units (baseline a named constant), not a flat 1; an over-quota tenant rejects the step exactly as an ordinary Trigger does; campaign freeze still applies.
- **Satisfies:** spec AC9; Constraints "engine-equivalents"
- **Depends on:** 78
- **Corrected during execution:** landed in `lifecycleapp.Trigger` itself (the "lifecycle seam"), not `calibrationapp/step.go` -- Trigger already computes and reserves engine count from `ec.TotalEngines()` in one place, so generalizing that single call site to read the execution's own pinned `CPU`/`Memory` (empty for every non-calibration execution, ratio 1.0, byte-for-byte unchanged behavior) was smaller and more principled than teaching calibrationapp to reserve separately. No `quotaapp` signature change was needed -- `Reserve` already took a plain `engineCount int`. Baseline picked as `500m`/`512Mi` (spec.md left the exact constant to the plan, alongside the other deferred tuning values).

### 82. Exclude `CalibrateEngine` executions from campaign rollup — **done, `720e3ec`**
- **Files:** `internal/app/campaignapp/verdict.go`, `internal/app/campaignapp/verdict_test.go`
- **Criteria:** a designated execution of kind `CalibrateEngine` is skipped in `Verdict` (does not contribute to go/no-go); a regression test covers it.
- **Satisfies:** spec AC12; Approach "Wiring — Phase 6 note made real"
- **Depends on:** 70

### 83. Add the calibration + fan-out HTTP surface — **done, `2d2ee73`**
- **Files:** `internal/adapters/httpapi/calibration_handlers.go`, `internal/adapters/httpapi/calibration_handlers_test.go`, `internal/adapters/httpapi/router.go`, `internal/adapters/httpapi/errors.go`, `api/openapi.yaml`, `internal/adapters/httpapi/rbac_router_test.go`
- **Criteria:** routes to trigger a calibration, read a job's status/progress, read a profile by key, and the fan-out calculator (`target_qps` + key → `{engines, status}`); RBAC-gated; form-encoded; OpenAPI updated.
- **Satisfies:** spec AC3, AC10; Approach "HTTP"
- **Depends on:** 77, 79, 72
- **Corrected during execution:** also added `calibrationapp.Service.ProfileFor`/`FanOut` (`internal/app/calibrationapp/service.go`, not in the original file list) -- HTTP handlers call app-service methods, not repos/domain functions directly, and neither existed yet. Also wired `Deps.Calibrations` into `cmd/api/main.go` (required for the routes to do anything outside tests) and into the shared RBAC fixture. Route shapes: `POST /api/calibrations` (create, mirroring `POST /api/executions`'s form shape) and `POST /api/executions/{execution_id}/calibration/trigger` (a distinct path from the ordinary `/trigger`, since a CalibrateEngine execution is never triggered through the ordinary lifecycle route).

## Group E — end-to-end + live

### 84. End-to-end: full calibration loop against the fake scheduler — **done, `56dd37d`**
- **Files:** `test/e2e/phase7_e2e_test.go`
- **Criteria:** scripted per-step reports drive: engine-limited happy path → profile written + fan-out `ok`; a target-limited run → `target_limited`; a safety-bound run → `neither`; a scenario-content change → fan-out `stale`; a missing profile → `no_profile`; and two controllers never double-drive a job.
- **Satisfies:** spec AC4–AC13 in one integrated proof
- **Depends on:** 80, 81, 82, 83
- **Corrected during execution:** the "stale" proof needed a scenario never bound to any execution's config — `scenarioapp.ErrScenarioInUse` makes a bound scenario's files permanently immutable, and every calibration binds one. Used a dedicated, never-bound scenario instead, seeding a profile keyed to its real pre-change fingerprint (computed via `scenarioapp.ScenarioFingerprint`, not fabricated) so the subsequent real content change (a second, data-only file — a scenario allows only one test script) is what actually produces the mismatch. `StepRunner.WithSleep` doubles as the "run the scripted step" hook: it ingests the next queued outcome via the real `/api/ingest` HTTP path instead of sleeping, so `Stop`+`GetReport` read a genuinely settled report.

### 85. Live verification against `httpbin.pve.heri.life` — **done (2026-08-10)**
- **Files:** verification notes appended to this `tasks.md` and to `spec.md` ("Live verification findings")
- **Criteria:** a real calibration on the cluster (`/home/coder/.kube/config`) against `httpbin.pve.heri.life` completes; the search deploys/classifies/brackets/bisects/terminates correctly; single-engine capacity established; results documented. (Manual / cluster-dependent — a verification activity, not a committed test.)
- **Satisfies:** spec AC14; Verification "live"
- **Depends on:** 84
- **Outcome:** **AC14 met.** Deployed the full stack to the `honryu` namespace on the real Talos cluster (`cmd/api` + `cmd/calibrator` as pods — the k8s scheduler needs `rest.InClusterConfig()`, so it can't run externally — plus in-cluster MySQL, RBAC, a shared `hostPath` object store, and images built/pushed to the user's Zot registry at `registry.pve.heri.life`). Ran calibration searches end-to-end against `httpbin.pve.heri.life`: real pods deploy, generate real load, settle real reports, and the search advances one row-locked step per tick with a fresh pod per step. See spec.md's "Live verification findings" for the full write-up.
- **Bugs found & fixed (11 total, none caught by the fake-scheduler suite):** config wiring `6123985`; Deploy→Trigger readiness race + report-polling + OOM/clock-injection `978b14a`; read-only ConfigMap vs bzt JMX rewrite `5ccce9d`; stale-pod reuse across re-deploys `fce1cdd`/`c7c3099`; terminating-pod counted ready `71d8290`; **throughput measured over wall clock instead of the load window** `331fdbb` (the root cause of every step reading engine-saturated); **adaptive Little's-Law VU sizing from measured latency** `7954068`; light-RT-probe first attempt (removes OOM + connection-storm from over-provisioned first guesses) `a4ae57d`/`cab8b61`; non-HTTP `response_code` overflow crashing ingest `b63b745`; **engine required on calibration Create** (else an unqueryable profile) `0f1bf9c`.
- **Empirical single-engine capacity:** a direct load probe proved the calibration's low readings were a measurement artifact — a 2000m-CPU pod sustains **~1000 QPS at 2ms/0% fail** and genuinely peaks around **~10000 QPS** (latency 2ms→50-97ms as throughput plateaus).
- **Final converged calibration (job 21, exec 12, scenario 2, cpu 2000m/mem 2Gi, hold 90s, seed 2000):** textbook bracket-then-bisect —
  `2000→1929 clean · 4000→3840 clean · 8000→7371 SAT · 6000→5750 clean · 7000→6649 SAT · 6500→6231 clean · 6750→6422 clean · 6875→6607 clean` →
  terminated `saturated_by: engine`, **PerPodQPS = 6606.7**. (The sustainable-at-≥95% rate, more useful than the ~10k degraded-latency peak.) Fan-out math: `ceil(target/6606.7)` → 20k QPS = **4 engines**, 50k = 8, 100k = 16.
- **Fan-out HTTP endpoint verified live (job 22, exec 13, created with `engine=jmeter`):** reproduced the same convergence (`4000→3855 clean · 8000→7424 SAT · 6000→5786 clean · 7000→6671 clean` → `saturated_by: engine`, PerPodQPS **6670.5**), then queried the API end-to-end: `GET /api/scenarios/2/capacity-profile?engine=jmeter&cpu=2000m&memory=2Gi` returned the settled profile (per_pod_qps 6670.5, fingerprint, calibrated_at, job_id), and `GET .../capacity-profile/fanout?...&target_qps=N` returned `{"status":"ok","engines":ceil(N/6670.5)}` — 6670→1, 20000→3, 50000→8, 100000→15. AC14's fan-out clause is now demonstrated through the live HTTP API, not just the math. (Also confirmed live that `POST /api/calibrations` with no engine is now rejected 400 per `0f1bf9c`.)
- **Cleanup:** live-test executions purged via the app's own `/purge`. The `honryu` namespace (Deployments/Services/Secrets/RBAC/MySQL, the `pod-security` privileged label, the shared `hostPath`, and the pushed images at `registry.pve.heri.life`) is **deliberately kept up for future ad-hoc runs** (user's choice, 2026-08-11). Tear down with `kubectl delete namespace honryu` when no longer needed.
