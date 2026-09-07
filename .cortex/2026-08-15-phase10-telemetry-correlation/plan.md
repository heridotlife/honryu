# Phase 10 — Telemetry correlation — Plan

**Spec:** `.cortex/2026-08-15-phase10-telemetry-correlation/spec.md`

## Context

- `compile.Input` / `compileScenario` (`internal/domain/compile/compile.go:48-147`) is
  pure — no I/O, no randomness (package doc, `compile.go:1-8`). `ScriptPath` and
  `DefaultAddress` are already resolved by the caller and handed in as plain
  values; `Headers` will follow the same shape.
- `taurus.Scenario.Headers map[string]string` already exists
  (`internal/domain/taurus/taurus.go:104`) but `compileScenario` never sets it
  today — a clean, currently-dead injection point.
- `lifecycleapp.Service.Deploy` (`service.go:217-271`) calls `compileShards`
  (`service.go:568`) per scenario. `Trigger` (`service.go:334-421`) calls
  `StartRun` (`service.go:387`) — which mints the run id — strictly after
  `Deploy` already created pods that may already be executing `bzt`. Confirmed
  from real callers: `cmd/scheduler/main.go:180-184` calls `Deploy` then
  `Trigger` back-to-back for every scheduled firing, and a pod's `bzt` never
  reruns on its own once started (task 23c's exit-code-then-`sleep infinity`
  fix, `internal/adapters/scheduler/k8s/k8s.go`'s `configHashAnnotation`
  comment, `k8s.go:36-52`) — so one `Deploy` call is, in practice, one run.
- `ports.RunRepository.StartRun(ctx, executionID) (int64, error)`
  (`internal/ports/run_repository.go:35-37`) has exactly the implementers and
  callers a signature change must touch: mysql (`run_repository.go:12-29`,
  inserting into `execution_run` and `execution_run_history`), fake
  (`internal/ports/fake/run_repository.go:12`), the conformance contract
  (`internal/ports/repositorytest/run_contract.go`, 5 call sites), and one
  production caller (`lifecycleapp.Trigger`, `service.go:387`).
  `RunRecord` (`run_repository.go:16-21`) has no correlation field yet.
  `RunHistory` (`run_repository.go:47`, mysql `run_repository.go:61-78`) reads
  `execution_run_history` — the table that survives after `StopRun` deletes
  the `execution_run` pointer row.
- `ExecutionRepository` (`internal/ports/execution_repository.go`) already has
  narrow setters for state that isn't part of the `execution.Execution`
  aggregate itself — `SetExecutionCriteria`/`CriteriaFor` (`:37-40`) is the
  exact shape to mirror for a "pending deploy-time value" that shouldn't
  pollute the domain struct.
- `report.Meta` / `Report` (`internal/domain/report/report.go:101-115,155-183`)
  gained `Cluster` in Phase 8 as the precedent for adding a new identity
  field. `Accumulator.Report(m Meta) Report` (`accumulate.go:96`) is where
  `Meta` fields get copied onto the final `Report`.
- **Load-bearing gotcha, found reading `metricsapp.finalize`
  (`internal/app/metricsapp/service.go:132-167`):** `Engine` and `Cluster` are
  read from `exe` (a fresh `GetExecution` call at finalize time), with a
  comment at `:153-158` already acknowledging this is imprecise for `Engine`
  if it changed between deploy and finalize. `StartedAt`, by contrast, is read
  from `history` (`RunHistory`, `:141`) — the row captured *at the time this
  specific run started*. `CorrelationID` must follow the `history` pattern,
  not the `exe` pattern: the execution's pending id can already point at a
  *later* `Deploy` by the time an earlier run's report finalizes.
- `execution_report` persists `Report` via a shared `reportColumns` constant
  used in both the `INSERT` and every `SELECT`
  (`internal/adapters/repo/mysql/report_store.go:20,61,100,117,124`) — `cluster`
  is a real column there, so `correlation_id` needs the same treatment or it
  is lost on read-back after `SaveReport`/`GetReport` round-trips.
- `api/openapi.yaml`'s `TestOpenAPIMatchesRoutes`/`TestOpenAPITagsMatchRouteGroups`
  (`internal/adapters/httpapi/openapi_test.go`) check routes and tags only —
  schema properties are hand-maintained, not exhaustively enforced (confirmed:
  `cluster` is already missing from the `Report` schema, `openapi.yaml:520-551`,
  a pre-existing gap this phase won't repeat for `correlation_id` but also
  isn't on the hook to fix for `cluster`).
- The literal `traceparent`/`baggage` strings a run actually used are already
  retrievable with no new endpoint, via the existing
  `GET /api/runs/{run_id}/scenarios/{scenario_id}/shards/{shard}/config`
  (`openapi.yaml:1600`).
- `crypto/rand` is used today only in `internal/adapters/secretbox/secretbox.go`
  (production) — never inside `internal/domain/`. Confirms generation belongs
  in `lifecycleapp` (app layer, already does I/O), not in the new pure
  `telemetry` package.
- Migrations are at `0043` (`migrations/0043_schedule_drop_cluster.sql`); this
  phase claims `0044`–`0046`. `0041_execution_cluster.sql` /
  `0042_report_cluster.sql` are the direct style precedent: an additive,
  `NOT NULL DEFAULT ''` column per table, one migration per table per task.

## Approach

Two layers. **Pure formatting** — `internal/domain/telemetry`: `TraceContext`
(trace-id/parent-id, plain data), `Identity` (tenant/service/execution/run
ids), and a pure `Headers(tc, id) map[string]string` rendering `traceparent`
+ `baggage`. **Generation + threading** — `lifecycleapp.Deploy` gets an
injectable trace-context generator (mirrors the existing `now`/`WithNow` seam,
`service.go:208`), called once per `Deploy`, before the scenario loop. The
resulting header map threads through `compileShards` into every
`compile.Input` in that call. The trace id is persisted as "pending" on the
execution (new narrow setter/getter, no domain-struct change), read back by
`Trigger`, and passed into a widened `StartRun`, which persists it onto
`execution_run_history`. `report.Meta`/`Report` gain `CorrelationID`, sourced
at finalize from `RunHistory` (not `GetExecution`), and persisted through
`execution_report`.

Rejected alternative (from brainstorm): a deterministic id derived from
stable identifiers, recomputed on demand, no new persistence. Cheaper, but
every run of a recurring execution would share one id forever. See spec
"Rejected alternatives" for the full reasoning.

## Risks

| Risk | Mitigation |
|---|---|
| Native (non-JMeter) engines may not honor scenario-level `headers:` for opaque scripts — the spec's one open verification item. | Sequenced last (task 121), after the mechanism exists to test. Live-verified against `httpbin.pve.heri.life`'s `/headers` echo endpoint, findings documented in spec.md exactly like Phase 7 task 85. Any engine that doesn't honor it is named, not silently assumed to work. |
| Widening `StartRun`'s signature. | One task (116), compiler-enforced across all 3 implementers + the contract. |
| A "pending correlation id" on the execution looking stale if `Deploy` fires twice before `Trigger`. | Last-deploy-wins is correct by construction — the next `Trigger` always runs against whichever pods the *latest* `Deploy` created. Documented, not specially handled; asserted in task 115's test. |
| Copy-pasting the `Cluster`/`Engine` finalize-from-`exe` pattern for `CorrelationID` would silently reintroduce the same imprecision, this time load-bearing (a wrong id makes a deep link point at the wrong run's traffic). | Task 118 explicitly sources from `RunHistory`, with a test asserting correctness even when the execution's pending id has moved on. |
| Schema changes to three already-live tables (`execution`, `execution_run_history`, `execution_report`). | Additive, nullable-by-default (`''`) columns only — no backfill, no behavior change for pre-Phase-10 rows. |

## Out of scope

Everything the spec non-goals (real spans, per-request unique ids,
native-script rewriting beyond Taurus's own `headers:`, a sampling toggle,
UI). Also **not** retrofitting the pre-existing `Engine`/`Cluster`
finalize-from-`exe` imprecision (`metricsapp/service.go:153-158`) — noted,
not fixed, here. **No backfill** of correlation ids onto historical
runs/reports.

## Verification

Standard bar: `go build`/`vet`/`gofmt`/`golangci-lint`, unit tests (table
tests for the pure `telemetry` package and `compile` changes), MySQL
conformance for every widened/new repository method, `scripts/coverage.sh`.
e2e (task 120) proves the whole chain against the fake scheduler + real
MySQL. Live verification (task 121) against `httpbin.pve.heri.life` on the
real cluster (`/home/coder/.kube/config`) is a **gate**, not optional, for
whether native engines actually deliver the headers — the one claim this
plan cannot verify by code inspection alone.
