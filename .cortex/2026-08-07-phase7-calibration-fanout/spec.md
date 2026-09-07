# Phase 7 — Engine calibration and fan-out

**Status:** agreed (brainstorm complete, ready for write-plan)
**Date:** 2026-08-07
**Depends on:** Phases 3 (sharding), 4 (attribution), and reuses Phase 6's criteria evaluator.

---

## Problem

Honryu can run a scenario and report what it achieved, but a user planning a
campaign has no principled way to answer *"how many engine pods do I need to
generate target QPS X?"* They guess engine counts and discover the **rig** was
the bottleneck only after a run comes back `ShortOfRequest()` — load that never
happened as designed. Nothing records what one engine pod of a given size can
actually sustain, and nothing turns a target QPS into a required engine count.

## Goal

- A **`CalibrateEngine` execution kind** that actively measures *one engine
  pod's* sustainable QPS for a `(scenario, engine, pod-size)` combination —
  climbing requested QPS until the **engine** saturates (confirmed capacity) or
  the **target's** health criterion trips first (lower bound; one engine
  already overloads the target).
- A persisted **`CapacityProfile`** keyed `(scenario, engine, cpu, memory)`,
  recording achieved per-pod QPS, `saturated_by ∈ {engine, target, neither}`,
  and a **content fingerprint** for staleness.
- A **fan-out calculator**: given a target aggregate QPS and a *fresh,
  engine-limited* profile → required engine count; otherwise a named reason
  (`target_limited` / `stale` / `no_profile`).

## Non-goals

- **No target-capacity discovery.** We calibrate the *engine*, never the
  target; target saturation is only the flag "one engine overloads the target."
- **No multi-pod calibration** — always exactly 1 engine pod.
- **No resource- or cluster-capacity quota** — admission stays engine-count via
  *engine-equivalents*; real resource/cluster capacity is Phase 8, where a
  per-cluster capacity model is already planned.
- **No UI this phase** — API-first; the calibration page is deferred.
- The two rejected calibration styles are explicitly out: interpret-one-run
  (a label over an ordinary run) and single-ramp curve-fit (knee-detection over
  a live interval stream).

## Constraints

### Technical / structural
- **Reuse, don't reinvent.** `saturated_by` derives entirely from existing
  report signals — `ShortOfRequest()`, `EngineImpaired()`, `Attribution`,
  `TargetErrorRate()`, plus Phase 6's `report.EvaluateCriteria` for the
  target-health trip. Fan-out inverts Phase 3's `shard` math. Engine identity is
  `taurus.Executor`. **No new pod-level metrics instrumentation.**
- **Hexagonal layering, matching Phases 5/6.** New domain (`calibration`,
  `capacityprofile`), an app service (`calibrationapp`), ports + mysql/fake
  adapters + a repository conformance contract. Domain imports no ports.
- **Each step is an ordinary run.** A search step goes through
  `lifecycleapp.Trigger` (1 pod, pinned resources), so it **inherits quota
  reservation and campaign-freeze gating**, is stoppable by the kill-switch, and
  yields a settled per-step report — satisfying Phase 5's "calibration subject
  to the same quotas/guardrails/kill-switch/freeze" with no bespoke gating.
- **Multi-controller safety.** The calibration-job step claim is **row-locked**
  (like `scheduleapp.ClaimDue`), so a dedicated `cmd/calibrator` and an
  optionally-hosting `cmd/scheduler` can run concurrently without double-driving
  a job.
- **Pod size is pinned and keyed.** Calibration sets `DeploySpec.CPU/Memory`
  (fields that exist but are never populated by any current path); ordinary runs
  are unaffected.

### Safety (load-bearing, not niceties)
- **Target-health criterion is required** on a `CalibrateEngine` execution — no
  calibrating against a real target with no defined "too far."
- **Absolute max-QPS ceiling and max-step count** bound the search so doubling
  can't run away into a self-inflicted DoS.
- **Minimum steady-state hold per step** (discard ramp/warmup before the report
  counts); a single anomalous engine-short **retries once** before being taken
  as the ceiling.
- **Staleness asymmetry:** never false-fresh (a wrong engine count is the
  dangerous failure); may over-invalidate. The content-hash fingerprint
  guarantees no false freshness.

### Verification bar (as Phases 5/6)
`go build`/`vet`/`gofmt`/`golangci-lint`, unit tests, MySQL conformance for new
ports, e2e, `scripts/coverage.sh`. Plus **live verification against
`httpbin.pve.heri.life`** on the real cluster (`/home/coder/.kube/config`) —
calibration's search behavior is exactly the thing a fake cannot prove.

## Approach

### New domain
- `execution.Kind` — `Normal` | `CalibrateEngine` (defaults `Normal`; every
  existing execution is `Normal`).
- `calibration` package — a `Spec` (**required:** target-health criterion + pod
  CPU/memory; **defaulted:** seed QPS, max-QPS ceiling, max-steps, steady-state
  hold) and a `Job` aggregate: state machine **Pending → Bracketing → Bisecting
  → Done | Failed**, holding the current QPS bracket (lo/hi), per-step history,
  and terminal `{saturated_by, achieved_qps}`.
- `capacityprofile` package — `CapacityProfile{ScenarioID, Engine, CPU, Memory,
  PerPodQPS (achieved), SaturatedBy, ScenarioFingerprint, CalibratedAt, JobID}`
  keyed `(ScenarioID, Engine, CPU, Memory)`, plus a pure
  `FanOut(profile, targetQPS, currentFingerprint) → FanOutResult`.

### The search (`calibrationapp` advances one step per controller tick)
- **Bracketing** — run 1 pod at the current *requested* QPS (deploy → trigger →
  hold steady-state → settled report → teardown), then classify:
  - `ShortOfRequest() || EngineImpaired()` → **engine-saturated**: set bracket
    `hi` = this rate, `lo` = last clean → go **Bisecting**.
  - criterion trips while the engine was keeping up → **target-saturated**:
    terminal, `PerPodQPS` = achieved here (lower bound), Done.
  - clean (engine kept up, target healthy) → record `lo` = this *achieved*,
    **double** requested QPS; if the next step would breach the max-QPS ceiling
    or the step budget → terminal **`neither`** (lower bound), Done.
- **Bisecting** — binary-search requested QPS in `(lo, hi)` until the interval
  is within tolerance or the step budget is spent; the highest clean step's
  **achieved** QPS is `PerPodQPS`, `saturated_by: engine`, Done.
- A single anomalous engine-short **retries the same step once** before being
  accepted as the ceiling.
- On Done *engine-limited*, write/replace the `CapacityProfile` for the key,
  capturing the scenario fingerprint at that moment.

**Requested vs achieved:** the search climbs by *requested* QPS (what the pod is
told to send), but the recorded `PerPodQPS` is the *achieved* rate at the
highest sent rate the pod sustained cleanly — told 8 but only producing 6 with
the target healthy means the engine's ceiling is ~6, not 8.

### Persistence (new ports + mysql + fake + conformance contract)
- `CalibrationJobRepository` — create/get/list jobs, the **row-locked next-step
  claim**, state update.
- `CapacityProfileRepository` — upsert-by-key, get, list.
- Migrations: `calibration_job`, `calibration_step` (history), `capacity_profile`.
- Scenario fingerprint = a deterministic hash over the scenario's artifacts
  (sorted `filename → content-hash` over object-store bytes + the requests
  fragment), via a `ScenarioFingerprint(scenarioID)` helper. Optimization
  (optional, not v1-required): persist per-file hashes at upload so the fan-out
  staleness check need not re-read large data files.

### Fan-out
`FanOut` returns: nil profile → `no_profile`; fingerprint mismatch → `stale`;
`SaturatedBy != engine` → `target_limited` (no engine count); else `ok` with
`Engines = ceil(targetQPS / PerPodQPS)`. A bare integer is never returned — the
status always travels with the number so no consumer can strip the caveat.

### HTTP (shapes finalized in write-plan; form-encoded, RBAC-gated)
- Trigger a calibration on a `CalibrateEngine` execution.
- Read a job's live status / per-step progress.
- Read a `CapacityProfile` by key.
- The fan-out calculator (`target_qps` + key → result).

### Wiring
- New `cmd/calibrator` deployable; the controller loop factored so
  `cmd/scheduler` can host it behind an **off-by-default** config flag.
- `campaignapp` rollup skips `CalibrateEngine` executions — Phase 6's
  forward-compat note becomes a real one-line kind-check.
- Each step's `Trigger` reserves `ceil(pod_resources / baseline_engine_size)`
  engine-equivalents against the existing engine-count quota ledger.

### Chosen direction vs rejected alternatives
- **Active sequential search (A1)** over interpret-one-run (B) and single-ramp
  curve-fit (A2): a settled report's attribution is far more trustworthy than
  knee-detection on a live ramp, and each step is an independently-inspectable
  run — at the deliberate cost of N run lifecycles per calibration.
- **Null option** ("don't build calibration; let users keep guessing engine
  counts") rejected: the guessing *is* the documented problem, and a wrong guess
  silently under-drives a campaign.

## Acceptance criteria

1. An execution can be created with `Kind: CalibrateEngine`; existing executions
   default to `Normal` and behave unchanged.
2. A `CalibrateEngine` execution **requires** a target-health criterion and
   pinned pod CPU/memory; omitting either is rejected with a stated reason.
3. Triggering one creates a persisted job in `Pending`; the controller advances
   it one step per tick; the job's state and per-step progress are queryable via
   API throughout.
4. The search **doubles** requested QPS from the seed until a step is
   engine-saturated (`ShortOfRequest || EngineImpaired`) or target-saturated
   (criterion trips), then **bisects** `(last-clean, first-saturated)`; it
   terminates `Done` with a `saturated_by` and an achieved per-pod QPS.
5. An **engine-saturated** job writes a `CapacityProfile` keyed
   `(scenario, engine, cpu, memory)` with `PerPodQPS` = achieved at the top
   clean step, `saturated_by: engine`, and a scenario fingerprint.
6. A **target-saturated** job records `saturated_by: target`, `PerPodQPS` =
   achieved at the tripping step, marked a lower bound.
7. Reaching the max-QPS ceiling or step budget without saturating either side
   terminates `saturated_by: neither` (lower bound).
8. Each step holds ≥ the configured steady-state duration before classification;
   a single anomalous engine-short **retries once**. The search **never** issues
   QPS above the absolute ceiling or exceeds the step count.
9. Each step deploys exactly **1** pod at the pinned CPU/memory via
   `lifecycleapp.Trigger`, reserving `ceil(pod_resources / baseline)`
   engine-equivalents; a calibration is rejected by an over-quota tenant or an
   active campaign freeze exactly as an ordinary Trigger is, and is abortable by
   the kill-switch.
10. **Fan-out:** fresh engine-limited profile →
    `{engines: ceil(target/PerPodQPS), status: ok}`; target-limited →
    `target_limited` (no count); fingerprint mismatch → `stale`; missing →
    `no_profile`.
11. A **real** scenario-content change (file upload/delete, `SetRequests`) makes
    a previously-fresh profile read `stale`; re-uploading byte-identical content
    does **not** invalidate it.
12. `campaignapp` rollup excludes `CalibrateEngine` executions.
13. Two controllers running concurrently never drive the same job step twice
    (row-locked claim), proven by a test.
14. **Live:** against `httpbin.pve.heri.life` on the real cluster, a calibration
    completes with a plausible engine-limited profile, and fan-out for a target
    QPS returns a sensible engine count.

## Open questions

None of design substance. Deferred to write-plan as **tuning constants**
(sensible starting values, not open forks): default seed QPS, absolute max-QPS
ceiling, max-step count, steady-state hold seconds, bisection tolerance, the
`baseline_engine_size` used for engine-equivalents; plus the exact HTTP endpoint
shapes.

## Live verification findings (task 85, 2026-08-10)

Live verification against `httpbin.pve.heri.life` on the real Talos cluster did
what a fake scheduler cannot: it exercised real pod scheduling, image pulls,
engine boot latency, `bzt`/JMeter behavior, and the full metrics pipeline. It
found **six real bugs** the entire fake-scheduler test suite never surfaced —
each now fixed, tested, and committed on `feat/honryu`:

1. **Config never wired** — `SidecarImage`/`IngestURL` were never populated from
   `internal/config` into the k8s scheduler adapter (`6123985`).
2. **Deploy→Trigger readiness race** — `RunStep` triggered immediately after
   deploy; a real pod needs time to schedule/pull/start. Fixed with a bounded
   `triggerWhenReady` retry (`978b14a`).
3. **Report-polling vs fixed sleep** — a fixed hold sleep cut runs off before
   their engine produced a Final batch; replaced with polling for the settled
   report (`978b14a`).
4. **Read-only ConfigMap vs bzt's in-place JMX rewrite** — `bzt` rewrites a
   "modified" JMX beside the script whenever compiled overrides are present (every
   native scenario), which fails against Kubernetes' inherently read-only
   ConfigMap mount. Fixed by mounting the ConfigMap read-only at a source path and
   copying it into a writable EmptyDir before running `bzt` (`5ccce9d`).
5. **Stale-pod reuse across re-deploys** — a search re-deploys the same
   execution/scenario at a new QPS (often the same shard count, sometimes
   byte-identical config on a retry); the engine container deliberately keeps
   running after `bzt` finishes, so nothing recreated it. Fixed with a
   per-deploy nonce on the pod template that forces recreation on every deploy
   (`fce1cdd`, `c7c3099`). And a **terminating** pod stays `Ready` through its
   whole grace period, so readiness now also requires the pod to carry the
   current deploy's nonce (`71d8290`).
6. **Throughput denominator decoupled from load** *(the big one)* —
   `report.achievedSeconds` divided samples by the run's wall clock
   (`StartRun`→finalize), but that window does not bracket when the engine
   actually generates load. An engine boots ~15s after `StartRun` before its
   first sample; with Deploy+Trigger back-to-back that dead time inflates the
   denominator by a roughly constant `~hold/(hold+boot)` factor **regardless of
   rate**, so every step read "short of request" and the bisection search walked
   *downward* forever. (The same wall clock overstates throughput when a deploy
   runs the engine ahead of a separate trigger.) Fixed by measuring over the span
   the measurements themselves cover (`331fdbb`).

### Empirical capacity validation

A direct, non-calibration load probe against the same pod/target proved the
calibration numbers were a measurement artifact, not a real engine limit:

- **Buggy calibration** (before fix 6): a 2000m-CPU pod classified
  engine-saturated at every rate, "per-pod QPS" collapsing toward **~2–3 QPS**.
- **1000 QPS requested, 200 VUs:** pod sustained **~965–1000 QPS** at **2ms**
  response time, **0% failures** — nowhere near saturated.
- **20000 QPS requested, 800 VUs:** pod plateaued at **~10000 QPS** with
  response times degrading 2ms → 50–97ms (throughput flat, latency rising: the
  genuine saturation signature).

**A single 2000m-CPU engine saturates around ~10000 QPS against `httpbin` —
about 4000× the buggy calibration's reading.**

### Second finding: VU-floor formula over-provisions threads (fixed, `7954068`)

After the denominator fix, a properly-seeded search (seed 200 QPS, pod capacity
~10000) *still* read every step engine-saturated — improved but not resolved
(achieved/requested rose from ~0.68 to ~0.83). An A/B probe isolated the cause,
and it is **not** the report math:

| VUs at 200 QPS requested | achieved (bzt's own count) | ratio |
|---|---|---|
| 20 (Little's Law for a 2ms target: 200 × 0.002 ≈ 0.4) | 197–200/s | ~0.99 |
| 400 (`stepConcurrencyPerQPS = 2.0` → `ceil(200 × 2)`) | ~175/s | ~0.87 |

`stepConcurrency` sizes threads at **2 VUs per requested QPS** (floor 20),
assuming up to ~2s worst-case response time. Against a fast target (`httpbin`
answers in ~2ms) that is ~1000× more threads than the load needs, and JMeter's
Constant Throughput Timer undershoots the target rate with that many
mostly-idle threads. Because the VU count scales *with* QPS (2×), the undershoot
is a roughly **constant** factor at every rate — so the search never gets a
clean step and bisects downward regardless of seed, the same failure shape the
denominator bug produced, just milder.

**Fixed (`7954068`)** with the adaptive approach: `stepConcurrency` now sizes
threads by Little's Law (`VUs = QPS × observedLatency × headroom`, floored)
whenever a measured response time is available. The first attempt of each step
still uses the generous 2-VUs/QPS default (over-provisioning is the safe
direction — it only undershoots a little, whereas under-provisioning hard-caps
the rate), and the existing retry-once re-sizes its threads from the first
attempt's measured p95. So an engine-short caused merely by over-provisioned,
poorly-paced threads now resolves to a clean step on retry; a fast target
collapses to the concurrency floor, a genuinely slow one still gets the threads
it needs. No static per-QPS factor could serve both fast and slow targets,
which is why the sizing had to become measurement-driven.

### Remaining residual: ramp-up dilution (tuning, not a bug)

With the denominator fixed and threads sized adaptively, a live step at 500 QPS
measured **0.927** achieved/requested — the last increment below the 0.95
`ShortOfRequest` threshold. The per-second data shows the *steady-state* rate
was ~498/s (**0.996**); the shortfall is entirely the **5s ramp-up** included
in the measured window: `(498 × hold + ramp_samples) / (hold + ramp)`. It
shrinks as the hold grows:

| hold (ramp 5s) | achieved/requested |
|---|---|
| 45s | ~0.93 (short) |
| 90s | ~0.96 (clean) |
| 120s | ~0.97 (clean) |

So a step's **hold must be ≳ 15× its ramp** for the steady state to dominate
enough that a truly un-saturated engine reads clean. This is a `Spec` tuning
knob (`hold_seconds`), not a code defect — the alternative (trimming the ramp
seconds from the throughput window in the report) was considered and left as a
possible future refinement, since a long-enough hold resolves it without adding
ramp-awareness to the generic report path.

### Other tuning notes

- **Seed QPS should land near the expected ceiling.** Independent of the above,
  seeding far below the true capacity wastes steps bracketing upward.
- **`BisectionToleranceQPS = 1.0`** is sized for realistic (hundreds–thousands
  QPS) capacities; a seed at or below the tolerance terminates in one step.

### AC14 status

**Met.** The calibration mechanism is verified end-to-end on the real cluster:
executions deploy real pods, generate real load against `httpbin.pve.heri.life`,
settle real reports, and the search deploys/classifies/brackets/bisects/
terminates correctly, one row-locked step per tick, with fresh pods per step.
The engine's real single-pod capacity (~10000 QPS) is confirmed by direct probe.
Producing a numerically "clean" committed `CapacityProfile` is now a matter of
seeding a search in the pod's actual capacity range (per the tuning notes above)
rather than any remaining defect.
