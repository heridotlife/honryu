# Phase 7 — Engine calibration and fan-out — Plan

**Spec:** `.cortex/2026-08-07-phase7-calibration-fanout/spec.md` (agreed).
**Tasks:** 70–85 (see `tasks.md`). Global numbering continues Phase 6 (54–69).

## Context (what exists now)

- Executions are kindless — `execution.Execution` (`internal/domain/execution/execution.go:27`), `New`/`Validate` (`:44`/`:54`). Add `Kind`.
- `DeploySpec.CPU/Memory` (`internal/ports/scheduler.go:32-33`) exist but are never populated; `lifecycleapp` builds the spec with them empty (`internal/app/lifecycleapp/service.go:247`).
- Run path: `Trigger` (`internal/app/lifecycleapp/service.go:269`) → freeze check (`:277`) → `quotaapp.Reserve` engine-*count* via `ec.TotalEngines()` (`internal/app/quotaapp/service.go:104`) → `StartRun`; `Stop` (`:356`) → teardown → `metricsapp.Finalize` → `SaveReport`; the settled report is read via `ReportStore.GetReport(runID)` (`internal/ports/reportstore.go:30`).
- Row-locked claim precedent: `scheduleapp.ClaimDue` (`internal/app/scheduleapp/service.go:182`) over mysql `ClaimDueOccurrence` (`internal/adapters/repo/mysql/schedule_repository.go:196`) — `SELECT … FOR UPDATE LIMIT 1` + status transition in one tx. The calibration step-claim mirrors this exactly.
- `saturated_by` signals all exist: `ShortOfRequest` (`internal/domain/report/report.go:212`), `EngineImpaired` (`internal/domain/report/attribution.go:130`), `TargetErrorRate` (`attribution.go:118`), Phase 6 `EvaluateCriteria` (`internal/domain/report/criteria.go:44`). Fan-out inverts `shard.Plan`/`shard.Total` (`internal/domain/shard/shard.go:52`/`91`).
- Fingerprint sources: `ScenarioFilesFor` + `GetScenarioRequests` (`internal/ports/scenario_repository.go:29`/`46`); mutated by scenarioapp `UploadFile`/`DeleteFile`/`SetRequests` (`internal/app/scenarioapp/service.go:185`/`225`/`249`).
- Campaign exclusion point: `campaignapp.Verdict`/`serviceVerdict` (`internal/app/campaignapp/verdict.go:63`/`162`).
- Scheduler background loops join a `WaitGroup` (`cmd/scheduler/main.go:102-117`); `SchedulerConfig` (`internal/config/config.go:31`). Latest migration is `0034`; Phase 7 adds `0035`+.

## Approach

A `CalibrateEngine` execution is an ordinary one-scenario execution plus a stored `CalibrationSpec` (target-health criterion via the existing `execution_criteria` table; pinned pod CPU/memory; defaulted seed/ceiling/max-steps/hold). Triggering it creates a persisted `Job`. The `cmd/calibrator` controller (loop reusable by `cmd/scheduler`) claims a pending-step job via a row-locked claim and advances one step:

1. rewrite the calibration's **own dedicated** execution config to the step's requested QPS (`1 engine, throughput=QPS, concurrency high, duration=hold`) at the pinned pod size;
2. `Deploy` 1 pod → `Trigger` (inheriting freeze + engine-equivalents quota) → hold the steady-state duration → `Stop` (deterministic step end, finalizes via the existing teardown path) → `GetReport(runID)`;
3. classify the settled report via the existing signals into `saturated_by`;
4. decide the next QPS through the pure `calibration.Next` (double while bracketing, bisect once bracketed);
5. persist the step + next state; on terminal, write a `CapacityProfile` keyed `(scenario, engine, cpu, memory)` with a scenario fingerprint.

Fan-out is a pure function inverting shard math, returning an engine count only for a fresh engine-limited profile. Chosen over interpret-one-run / single-ramp (spec) because each step being a real settled run is what makes `saturated_by` trustworthy and reuses the whole existing pipeline.

## Risks → mitigations

1. **Per-step config rewrite racing another actor** → the execution is calibration-dedicated (not shared with normal runs) and the row-locked claim serializes steps; only one controller touches it at a time.
2. **Unbounded step duration** waiting for a pod's natural end → the controller `Stop`s after the steady-state hold (deterministic), finalizing via the existing teardown→`SaveReport` path.
3. **Runaway load / target DoS** → mandatory target-health criterion + absolute max-QPS ceiling + max-step count, enforced in the search domain and unit-tested.
4. **Noise mis-brackets** (one unlucky report) → minimum steady-state hold before classifying + a single retry on an anomalous engine-short.
5. **Two controllers double-driving a job** → the row-locked claim, proven by an integration test.
6. **Coverage** (repo ~89.7%, pre-existing debt) → the search state machine and fan-out are pure domain, highly testable; keep new code well-covered and don't regress the total.

## Out of scope

Target-capacity discovery; multi-pod calibration; a resource/cluster-capacity quota ledger (engine-equivalents only — real resource/cluster capacity is Phase 8); UI; the interpret-one-run and single-ramp calibration styles; the per-file-hash fingerprint optimization (fan-out re-reads artifacts for v1).

## Verification

Unit tests for the search state machine (bracket/bisect/`saturated_by`/safety bounds) and fan-out (all four statuses); MySQL conformance for the two new repositories, including the row-locked step claim; a `test/e2e/phase7_e2e_test.go` driving a full calibration against the fake scheduler with scripted per-step reports (engine-limited happy path, target-limited, neither, staleness, fan-out, no-double-drive); and a **live** run against `httpbin.pve.heri.life` on the real cluster (`/home/coder/.kube/config`) producing a plausible engine-limited profile and a sensible fan-out number.
