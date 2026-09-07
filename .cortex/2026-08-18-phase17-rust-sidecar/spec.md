# Phase 17 — Rust sidecar behind a language-agnostic contract (STUB)

**Status: recorded intent, not an agreed spec.** Captured 2026-08-18 from a
design discussion so the reasoning is not rediscovered later. Needs a proper
`brainstorm` before any `write-plan`. Sequenced after phase 16 (the platform
running on talos-homelab), and deliberately optional.

## Intent

Port the metrics sidecar (`internal/sidecar`, `cmd/sidecar`) from Go to Rust,
behind an explicit, versioned, language-agnostic wire contract. The control
plane and API stay Go — this is scoped to the one binary that runs inside the
engine pod.

## Why the sidecar, and only the sidecar

It is the sole Honryu component that shares a pod with a process deliberately
trying to saturate its CPU. Everything else — API, scheduler, calibrator,
report accumulation — runs on the control plane where footprint is
uninteresting.

**The justification is measurement fidelity, not throughput.** This must not
drift into a performance claim the phase cannot demonstrate:

- The load path contains **no Honryu code at all**: bzt/JMeter/k6 generate the
  requests (parent decision, `.cortex/2026-07-30-honryu/spec.md:39` — "Not a
  bespoke execution engine").
- The sidecar's own data rate is one HTTP POST per second per pod
  (`internal/sidecar/sidecar.go`: `FlushInterval` 1s, `PollInterval` 200ms).
  At the parent spec's own fan-out example (50,000 QPS ÷ 320 QPS/pod = 157
  engines, `:84`) that is ~157 req/s reaching ingest — roughly one Honryu
  message per 320 generated requests. That is not a Go problem in any language.
- What *is* plausibly real: a Go runtime and GC competing for cycles with the
  generator in the same pod. This project treats that as a correctness issue,
  not an efficiency one — `saturated_by: engine | target` (`:88`) and
  engine-vs-target attribution as a stated design invariant (`:106`). If
  Honryu's own sidecar contributes to a run reporting `saturated_by: engine`,
  every `CapacityProfile` derived from it under-provisions and the fan-out
  arithmetic quietly lies.

Secondary, evidenced: the sidecar's one production data race
(`87fcbdf` — "guard pending and sent behind a mutex", 31 lines) is a class of
bug Rust makes a compile error. Counterweight: CI already runs `-race`; that
bug survived because no test exercised the concurrency, not because Go could
not see it.

## Load-bearing decision: contract first, in Go, before any Rust exists

Define and version both wire boundaries with the **existing Go sidecar as the
reference implementation**, prove the contract tests catch the known failure
mode, and only then port. Rationale:

- Defining the boundary *during* a port debugs two things at once, and the
  contract ends up shaped by whatever the new code happens to emit.
- If the Rust work slips or is abandoned, the contract work has already
  hardened the system on its own. Contract-after-rewrite has no such fallback.

## The two boundaries

1. **Upstream — bzt's KPI stream**, written by `deploy/engine/honryu_kpi.py`
   (Python) and read by the sidecar. Already cross-language, already the site
   of a total-data-loss incident (phase 11), and currently specified nowhere.
2. **Downstream — `POST /api/ingest`**, currently the `metrics.Batch` Go struct
   compiled into both sides. This compile-time coupling is exactly what a Rust
   sidecar breaks, and what phase 11's Python↔Go float/`int64` bug proves is
   dangerous when left implicit.

## Defect to fix as part of the contract work (valuable with or without Rust)

`internal/sidecar/sidecar.go:271-277` logs and skips unparseable lines —
"one malformed line must not end collection." Sound for one line. Phase 11
showed the failure when **every** line is malformed (k6 emitting float
counters): the run "passed and finalized with zero samples in Honryu's report"
while bzt's own summary showed 55,024 samples. Silent total data loss, visible
only in pod logs.

The sidecar should **count skipped lines and surface them**, and a run that
dropped ~all of its intervals must not finalize as `passed` with zero samples —
that is an `error` outcome. Language-agnostic, fixes a defect already suffered.

## What replaces the conformance suite

The repo's core discipline — every port has a fake plus a shared conformance
suite the real adapter must also pass (`AGENTS.md`) — cannot survive a language
boundary: a Rust binary cannot run a Go `ports/<port>test` suite. Replacement:

- A **versioned schema** for both boundaries. JSON Schema preserves the existing
  JSON-over-HTTP wire; protobuf/CBOR would be cleaner but means changing the
  ingest handler and every test that builds a `metrics.Batch`.
- **Shared golden fixtures** both implementations test against: valid batches
  plus the nasty cases — float counters (the k6 bug), missing exit code, empty
  histograms, unicode labels, oversized batches.

Conformance-by-fixture rather than conformance-by-interface; the only form that
survives two languages.

## Constraints carried in

- **Coverage gate.** Rust sits outside `scripts/coverage.sh`. After phase 14
  spent an entire phase restoring that gate, the preference is a parallel gate
  (`cargo llvm-cov`) over a documented exclusion — otherwise the ≥90%
  constraint (`:48`, `:213`) silently stops covering a production component.
- **Survive engine exit to flush finals** (`:135`) — the reason a sidecar
  exists rather than a bzt reporter plugin. Needs an explicit Rust test.
- **`Final` + `ExitCode` semantics.** Phase 11's orphan-completion recording
  and `lifecycleapp.Reconcile` both key off these. Subtly wrong values break
  stranded-run recovery in a way no sidecar-local test would catch.
- Image and toolchain pinned, per the phase 11 scar (a mutated image under an
  unchanged tag served a stale digest).
- New CI lane (`cargo fmt`/`clippy`/`test`) alongside the Go one.

## Deferred, to do regardless

Take the sidecar CPU/RSS baseline against a saturating engine — **not** as a
gate on the decision, but as the *before* number, so the rewrite can be shown
to have achieved something. Phase 16's Prometheus makes this nearly free.

## Open questions for the brainstorm

1. Is the measurement-fidelity premise real? The baseline above answers it, and
   a null result would be worth recording rather than hiding.
2. JSON Schema (keep the wire) versus protobuf (cleaner contract, wider blast
   radius)?
3. Does the upstream KPI-stream contract also pull `honryu_kpi.py` into scope —
   i.e. is the Python reporter a third implementation of the same schema?
4. Parallel Rust coverage gate, or an explicit documented exclusion?
