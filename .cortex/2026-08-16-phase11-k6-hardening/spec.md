# Phase 11 — k6 engine + lifecycle hardening

Agreed via brainstorm 2026-08-16, continuing the brainstorm → write-plan →
execute-plan cadence of Phases 5–10. Two halves that belong together because
both come straight out of Phase 10's live verification (task 121,
`.cortex/2026-08-15-phase10-telemetry-correlation/spec.md`): the engine gap it
named (k6 untested live, no image built) and the operational gaps it hit
(trigger readiness, stranded runs).

## Problem

1. **k6 is a configured-but-unbuilt engine.** `deploy/engines/README.md`
   documents k6's pairing (k6 binary, script-only — bzt's k6 executor rejects
   the declarative form) and `make engine` exercises it locally, but there is
   no `engine-k6` image, no deployment configures one, and scenario-header
   behavior on k6 was never verified live. Phase 10's AC7 honestly reported
   k6 as "untested live"; that debt is now due.
2. **Trigger is not robust at the API boundary.** Phase 7 gave
   `cmd/scheduler` (and `calibrationapp`) a bounded `triggerWhenReady` retry
   because Deploy returning 200 only means the StatefulSet was created — but
   `POST /api/executions/{id}/trigger` still 409s immediately
   (`run.ErrNotDeployed` / `ErrEnginesNotReady`) until the pod happens to be
   ready. Every human or script client rediscovers and re-implements the
   retry.
3. **A late Trigger strands a run.** An engine pod starts generating load the
   moment it starts (task 23c). If Trigger lands after the engine already
   finished — easy to do by hand, possible via slow retries — `StartRun`
   opens a run whose engine has already sent its Final batch, and nothing
   ever finalizes that run: it sits `running` with no report until someone
   notices and calls Stop. Live-verified the hard way in task 121.

## Goal

- A deployment can run k6 scenarios end to end — image built, registered in
  `HONRYU_ENGINE_IMAGES`, telemetry headers verified on the wire live — with
  the script-only portability rule enforced, not assumed.
- Triggering through the public API is robust without client-side retries,
  and no code path can leave a run open with no engine behind it.

## Non-goals

- **No gatling.** The engines README lists the pairing (Gatling needs a
  JDK ≤ 17 for Scala 2.13.10); adding it is a separate decision with its own
  image-cost analysis. Not bundled here.
- **No declarative-form support on k6.** bzt's k6 executor rejecting the
  declarative form is the asymmetry `scenario.KindNative` exists to model;
  we document and enforce, we do not work around.
- **No scheduler/cron changes.** `cmd/scheduler` already triggers promptly;
  the hardening targets the HTTP boundary and the run lifecycle, not the
  scheduler.
- **No new metrics/UI surfaces.** Hardening is invisible when it works.

## Constraints

- **One image per engine** (deploy/engines/README.md's rule, forced by the
  JDK/Scala incompatibility): k6 gets its own `deploy/engines/k6/Dockerfile`,
  base image chosen for the k6 binary (no JVM needed — k6 is a single Go
  binary; the image is `bzt` + k6, not a JDK image).
- **Engine images are pinned** (`HONRYU_ENGINE_IMAGES` rejects untagged
  refs): the k6 image ships as `engine-k6:<version>` with a real tag.
- **Live verification is a gate** (Phases 7/10 precedent): the k6 header
  claim is only "confirmed" after a real run against `httpbin.pve.heri.life`
  on the Talos cluster, with findings appended to this spec — including a
  **negative control** proving whatever echo-assertion mechanism is used
  actually binds (task 121's `Asserion.test_strings` lesson: a malformed
  assertion fixture passes vacuously).
- **Trigger readiness must not change Deploy's contract.** Deploy stays
  "StatefulSet created"; the wait belongs at Trigger (or above it), exactly
  where calibrationapp already put it.
- **Stranded-run reconciliation must not guess verdicts.** A run that
  finalizes from a stranded state reports what the evidence supports
  (`error`/`aborted`-class outcome from engine state), never an invented
  `passed`.

## Approach

### k6 engine

1. `deploy/engines/k6/Dockerfile`: bzt + pinned k6 binary + the KPI reporter
   wiring the jmeter image already uses (`engine/honryu_kpi.py` +
   `.bzt-rc`), warm-up provisioning baked at build time (the Phase 7
   "pods need no network" lesson applies to k6's binary download too).
2. Register `k6=…engine-k6:<tag>` in the homelab deployment config; deploy
   one script-native k6 scenario live against `httpbin.pve.heri.life/headers`
   with response assertions on the echo, mirroring task 121's method.
3. Enforce script-only: a declarative (portable) scenario selecting k6 must
   fail at compile/deploy time with the portability error, not at the engine
   at 3am. (Domain rule exists via `scenario.KindNative`; verify the k6 path
   surfaces it, add tests where thin.)

### Trigger hardening

4. Move the bounded readiness retry **into the HTTP handler layer** (or a
   lifecycleapp wrapper): `POST .../trigger` polls
   `ErrNotDeployed`/`ErrEnginesNotReady` for a bounded window (the
   calibrationapp constants are the precedent), then 409s with the last
   error if still unready. Existing immediate-409 semantics survive as the
   timeout expiry path; no new endpoint, no client change required.
5. Scheduler/calibrationapp keep their own retries (belt and braces is fine;
   theirs also covers non-HTTP callers).

### Stranded-run reconciliation

6. Trigger refuses to open a run for engines that already finished: when
   `ExecutionStatus` reports the pool done (or the exit-code signal says so)
   and no run is open, Trigger returns a typed error ("re-deploy before
   triggering") instead of `StartRun`-ing a corpse.
7. Reconciliation sweep for the genuinely stranded case (crash between
   StartRun and Final, whatever the cause): a bounded, idempotent
   "runs with no live engine finalize as error" pass — shape decided at
   plan time (startup sweep vs periodic; must respect `-p 1` test lanes and
   the pure-app/adapter split). Reports produced this way carry engine-side
   attribution, matching how a no-sample run already reports today.

### Verification bar

`make engine` (bzt + k6 local), unit + integration + e2e as Phases 5–10,
plus **live verification on the Talos cluster** for: k6 native scenario with
headers on the wire (negative control included), API-trigger retry (deploy →
immediate trigger succeeds without client retry), and stranded-run recovery
(deploy, wait out the hold, trigger → typed error, not a stuck run).

## Acceptance criteria

1. `deploy/engines/k6/Dockerfile` builds a working pinned k6 engine image;
   `HONRYU_ENGINE_IMAGES` accepts it; a k6 script-native scenario passes a
   live run end to end with KPIs streaming and a finalized report.
2. k6 telemetry headers verified live against the echo target with a
   negative control; findings (including any k6-specific header limitation,
   named per Phase 10's rule) appended to this spec.
3. A portable/declarative scenario selecting k6 fails fast with the
   portability error, covered by tests.
4. `POST .../trigger` immediately after deploy succeeds without client-side
   retries (bounded internal wait), and 409s promptly (not hanging) when the
   execution genuinely cannot trigger.
5. Triggering after the engine finished returns a typed re-deploy error; no
   code path opens a run whose engine is already gone.
6. A stranded run (no live engine, no Final) reconciles to a finalized
   error-outcome report idempotently, with no invented pass verdicts.
7. Standard bar: gofmt/vet/golangci-lint clean, unit race tests, MySQL
   conformance for any widened ports, e2e, coverage gate ≥90%.

## Open questions (resolve at write-plan)

- Reconciliation sweep placement: startup-only vs periodic ticker vs
  on-read (Status) — cost/complexity trade per placement.
- Whether the trigger wait belongs in httpapi or lifecycleapp (hexagonal
  purity says the handler; but non-HTTP callers might want it too).
- k6 version pin and whether the KPI reporter needs any k6-specific glue in
  `.bzt-rc` (jmeter's warm-up trick may differ for k6's provisioning).

## Live verification findings (task 128, 2026-08-16)

Live verification on the real Talos cluster (`admin@talos-homelab`, ns `honryu`),
API image `honryu-api:phase11`, engine images `engine-k6:0.57.0-2` /
`engine-jmeter:5.6.3-2`, target `httpbin.pve.heri.life/headers`. **All AC live
gates met — and the pass caught one real bug the entire test suite could not.**

### k6 on the wire (AC1, AC2)

| Run | Image | Samples | Failures | Checks |
|-----|-------|---------|----------|--------|
| local pre-check (docker, bzt+headers) | 0.57.0 | — | 0% | echo `check`s 100% |
| local negative control (docker, raw k6) | 0.57.0 | 16,992 | checks 0/16,992 pass | binds |
| run 6 (live, first) | 0.57.0 | 55,024 | 0.00% | passed |
| run 8 (live, fixed reporter) | 0.57.0-2 | 55,313 | 0% | passed |

**k6 honors scenario-level `headers:` on scripts — Phase 10's named gap is
closed.** Proof method identical to task 121: the script `check`s the echo body
for `traceparent`/`honryu.run=`; a passing run means every sampled response
contained them; the negative control (same script, raw k6, no bzt injection)
fails 100% of checks, so they bind. Run 8's report carries the correlation id
matching its shard config's trace id, engine `k6`, one label, p95 1ms.

### The bug the pass caught: k6's float bytes killed the KPI stream

Run 6 **passed and finalized with zero samples in Honryu's report** while the
engine's own bzt summary showed 55,024 samples. Cause: k6 reports cumulative
`data_volume` as a float (`2669215744.0`); `honryu_kpi.py` serialized it
verbatim; Go's `metrics.Interval.bytes int64` refuses JSON floats, so the
sidecar logged `skipping unparseable line` for **every** interval of the run —
silent total data loss, k6-only (JMeter's counters arrive as ints). No test
lane could see this: the fakes emit ints, and `make engine` asserts outcomes,
not the Honryu-side report.

Fix (committed): `honryu_kpi.py` coerces every counter to `int` on the wire.
Verified live by run 8: 55,313 samples, percentiles, and labels all present.

Second operational lesson (already doctrine, now with a scar): re-pushing a
mutated image under the **same tag** does nothing — the k8s adapter pins
`imagePullPolicy: IfNotPresent`, so the node served the old digest while the
registry held the new one. The reporter fix shipped as `0.57.0-2`/`5.6.3-2`;
image content changes get a new tag, exactly as `deploy/engines/README.md`'s
"untagged image" rule implies.

### Trigger readiness (AC4)

Deploy→trigger back-to-back over plain HTTP succeeded **first try** (6.1s wall
clock, no client retry) with the fake-free real scheduler: the handler's
bounded wait absorbed pod scheduling/image-pull/startup. The task-121 dance
(deploy, 409, sleep, retry) is gone.

### Orphan guard (AC5)

Deployed a 10s k6 run and let it finish untethered; 75s later, trigger
returned exactly:

    409 {"message":"run: engines already finished, redeploy before triggering: 1 orphaned shard completion(s)"}

No stranded run was opened; the error is typed (no readiness-retry burn — the
wait does not retry it), and a re-deploy clears it (e2e-pinned). The reconcile
ticker ran its 1-minute passes throughout with zero warnings — silent no-ops,
as designed.

### Not live-verified

The genuine crash-strand path (run row open, engine gone mid-run) has no cheap
live trigger; its behavior is pinned by the e2e (`TestPhase11_ReconcileCloses
StrandedRun`) and the unit lane's four Reconcile cases. Honest scope: live
verification covered the guard, not a second way to strand.
