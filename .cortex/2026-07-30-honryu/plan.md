# 奔流 (Honryu) — Implementation Plan

**Date:** 2026-07-30
**Spec:** `.cortex/2026-07-30-honryu/spec.md`
**Tasks:** `.cortex/2026-07-30-honryu/tasks.md`
**Branch base:** `develop`

---

## Context

The root module `github.com/heridotlife/honryu` (renamed from `Setagaya` on 2026-07-30; Go 1.26, 153 Go files, ~16.8k LOC) is the completed v3 hexagonal rebuild: pure domain → app use-cases → ports → adapters, with a ≥90% coverage gate (`scripts/coverage.sh:12`) enforced in `.github/workflows/ci.yml`.

What exists today, verified by reading:

- **Domain.** `plan.Plan` (`internal/domain/plan/plan.go:24`) is a reusable test definition. `collection.Collection` (`internal/domain/collection/collection.go:22`) groups plans to run together. `execution.ExecutionPlan` / `execution.ExecutionCollection` (`internal/domain/execution/execution.go:26`) hold the *load configuration* — `PlanID, Concurrency, Rampup, Engines, Duration`. `run.Run` (`internal/domain/run/run.go:37`) is a run of a collection with an idle/deployed/running phase machine.
- **Executor port** (`internal/ports/executor.go:14`) is an **agent HTTP client**: `Trigger(ctx, engineURL, cfg)`, `Stop`, `Progress`, `Subscribe(ctx, engineURL) (<-chan engine.Metric, error)`. Two adapters implement it (`internal/adapters/executor/jmeter`, `.../k6`), fed by `engine.BuildConfigs` (`internal/domain/engine/build.go:44`).
- **Scheduler port** (`internal/ports/scheduler.go:68`) is collection/plan-shaped throughout: `DeployPlan`, `EngineURLs(collectionID, planID, engines)`, `CollectionStatus`, `PurgeCollection`, `PodLog`, `DeployedCollections`, `NodePools`.
- **Persistence.** 14 ordered migrations in `migrations/` carrying Shibuya-inherited table names (`collection`, `plan`, `collection_plan`, `plan_data`, `collection_run`, …), plus `v3_tenant` / `v3_role_grant`.
- **HTTP API.** 41 routes in `internal/adapters/httpapi/`; roughly half carry `/api/collections` or `/api/plans`.
- **Gaps.** No OpenAPI artifact exists despite the README's "API-first" claim. `cmd/agent/` and `cmd/controller/` are **empty placeholder directories**.

Phase 0 (see spec) proved the Taurus pivot viable and produced three findings that this plan builds around: results are engine-portable but **scenario definitions are not**; **labels and error text differ per engine** while response codes do not; and **engine images need per-engine runtime pins**.

---

## Approach

### The rename is a three-way collision

`internal/domain/execution` is already occupied by the load config, so `collection → execution` cannot be a straight swap. The mapping:

| Now | Becomes | Why |
|---|---|---|
| `plan.Plan` | `scenario.Scenario` | Taurus `scenarios` |
| `collection.Collection` | `execution.Execution` | Taurus `execution` — the runnable unit |
| `execution.ExecutionPlan` | `loadprofile.Entry` | scenario ref + concurrency/ramp/duration/engines — this **is** a Taurus execution entry |
| `execution.ExecutionCollection` | `loadprofile.Profile` | the set of entries |

The third rename is the one that gets missed; it must land **before** the second to free the package name.

### The Executor port inverts — the real change

Today `Executor` is an HTTP client that calls an agent on each engine and pulls a metric stream. Under Taurus there is no agent: `bzt` runs in the pod and a sidecar **pushes**. So:

- `Executor` stops being a network client and becomes **config compilation + a pod contract** (image, command, args, exit-code → verdict).
- A new **inbound** `MetricsIngest` port appears, fed by sidecar pushes.
- `engine.Config`, `engine.BuildConfigs`, and the whole `Subscribe` streaming path are **deleted, not renamed**.

This is why Phase 1 deliberately does *not* rename them: touching code that Phases 2–3 delete is wasted churn against a 90% coverage gate.

### Fresh baseline schema

Migration from Shibuya is **import-only** (spec decision #5) and there is no deployed Setagaya v3 database, so there is no data to preserve. Migrations `0001`–`0014` are **replaced by a single new baseline** in the new vocabulary. Later phases add migrations additively from that baseline.

### Sequencing principle

Prove the seam before building on it, and keep something verifiable early:

1. **Phase 1** is a mechanical, behaviour-preserving rename — the whole suite must stay green, which is itself the proof.
2. **Phase 2** swaps the engine underneath a stable domain, with the config compiler verified by golden files before any pod runs.
3. **Phase 3** takes the riskiest step (sharding + push metrics) only after single-pod Taurus execution works.
4. **Phase 4** makes failures diagnosable *before* Phase 5 makes runs unattended.

---

## Risks

| Risk | Mitigation |
|---|---|
| **Rename churn breaks the 90% coverage gate.** Renames touch ~all 153 files; tests move with them and coverage can dip if any path is dropped. | Phase 1 is behaviour-preserving: no test may be deleted, only moved/renamed. Run `scripts/coverage.sh` as the acceptance check on every Phase 1 task. |
| **Deleting the agent protocol removes live metrics before the replacement exists.** Phase 2 deletes `Subscribe`; Phase 3 delivers push. Between them there is no live streaming. | Accept the gap deliberately and keep it inside one un-released branch: Phases 2 and 3 land together before any deployment. Post-run results still exist via bzt's own artifacts. |
| **Sharded percentiles computed wrongly would silently corrupt verdicts.** Phase 0 measured naive averaging under-reporting p95 by up to 7.6% — in the direction that *passes* a sale that should fail. | Histogram merge is a pure-domain function with property tests comparing merged-bucket percentiles against a direct computation over the combined raw sample set. |
| **Per-engine image pinning.** bzt's bundled Gatling 3.9.5 cannot run on JDK 21 (Phase 0). | Engine images are built and tested per engine, each with a pinned runtime; an engine is only "supported" once its image passes the executor conformance suite. Gatling is deferred; JMeter and k6 are unblocked. |
| **Scenario portability confuses users.** "Any engine" is true for portable scenarios only; k6 is script-only. | Model portability explicitly in the domain, expose supported engines per scenario in the API, and reject incompatible engine selection with a stated reason. |
| **Multi-cluster retrofit.** Building clusters in at Phase 8 could force rework of the scheduler and quota code from Phase 5. | Phase 3 reshapes `Scheduler` to be **cluster-addressable** and Phase 5 makes quota **per-cluster** from the start; Phase 8 then adds registry + credentials, not a re-architecture. |
| **Verdict distortion from uncontrolled concurrent load** (spec residual risk on freeze scoping). | Phase 6 records what other load was active in the campaign's clusters during the window on every campaign report. |

---

## Out of scope

- BYOC (customer-registered clusters) — seam only, no implementation.
- ~~A UI/SPA. The API is the deliverable; the OpenAPI document is the contract.~~ — **reversed 2026-08-05**: Phase 5 adds a first, read-only UI (React + Tailwind v4 SPA, static assets served by `cmd/api`, styled after `heridotlife`'s admin-dashboard design language). The API remains the contract — OpenAPI still describes every capability, and the UI is a consumer of it like any other client — but a UI is no longer categorically excluded. See `.cortex/2026-08-05-phase5-scheduling-quota-ui/spec.md`.
- Target-side APM/tracing storage — Honryu propagates correlation IDs and deep-links out.
- Target-saturation benchmarking — engine capacity only.
- Shibuya database/state migration — JMX asset import only.
- Gatling engine support — deferred behind its JDK pairing question.
- ~~Renaming the Go module path or the GitHub repo~~ — **done 2026-07-30**: the repository was renamed to `heridotlife/honryu` and the module path moved with it (spec decision #6).

---

## Verification

**Per task:** `make build`, `go test ./...`, `golangci-lint`, and `scripts/coverage.sh` at ≥90%. Every new port ships an in-memory fake plus a conformance suite in `internal/ports/<name>test` that real adapters must also pass — the existing pattern in `internal/ports/repositorytest`.

**Per phase, end to end:**

- **P1** — full suite green with zero occurrences of `collection`/`plan` in domain, ports, app, API routes, or schema; behaviour unchanged.
- **P2** — one execution runs on JMeter *and* on k6 against a stub target from the same portable scenario, with the generated Taurus YAML retrievable and the verdict derived from bzt's exit code.
- **P3** — an execution sharded across N pods produces one aggregate result whose p95 matches a single-pod run of the same total load within tolerance; per-shard metrics arrive by push.
- **P4** — a deliberately failed run is fully diagnosable from the API alone: report, engine-side logs, and every error attributed engine-side or target-side, with no cluster access.
- **P5–P9** — verified against the phase acceptance criteria in `tasks.md` when reached.

**Regression guard:** the Phase 0 stub target and its induced error mix are reusable as the e2e fixture; keep the harness under `test/e2e`.
