# Phase 10 — Telemetry correlation — Tasks

Continues the roadmap's global task numbering from Phase 9's last task (111).
**Spec:** `spec.md` · **Plan:** `plan.md`

## Group A — pure domain: header rendering

### 112. Add the `internal/domain/telemetry` package
- **Files:** `internal/domain/telemetry/telemetry.go`, `internal/domain/telemetry/telemetry_test.go`
- **Criteria:** `TraceContext{TraceID, ParentID string}` (32 / 16 lowercase
  hex); `Identity{TenantID *int64, ProjectID, ExecutionID int64,
  RunCorrelationID string}`; `Headers(tc TraceContext, id Identity)
  map[string]string` returns exactly `traceparent`
  (`00-<32hex>-<16hex>-00`) and `baggage`
  (`honryu.tenant=…,honryu.service=…,honryu.execution=…,honryu.run=…`), with
  `honryu.tenant` omitted (not rendered empty) when `id.TenantID` is nil;
  pure, no I/O, no randomness; table-tested including the nil-tenant case.
- **Satisfies:** spec "Approach — minting and threading the id" step 2; AC1, AC3
- **Depends on:** —

## Group B — compile-time injection

### 113. Thread `Headers` through `compile.Input` into `taurus.Scenario.Headers`
- **Files:** `internal/domain/compile/compile.go`, `internal/domain/compile/compile_test.go`
- **Criteria:** `compile.Input` gains `Headers map[string]string`;
  `compileScenario` sets it unconditionally onto the resulting
  `taurus.Scenario.Headers` for both native and portable branches; a
  nil/empty `Headers` compiles to no `headers:` key (existing
  `TestTaurus_Golden` fixtures stay byte-identical, since none pass
  `Headers`); a populated map round-trips into the compiled scenario,
  asserted directly.
- **Satisfies:** spec "Approach — minting and threading the id" step 3; AC1
- **Depends on:** 112

## Group C — generation and threading through Deploy/Trigger/Run

### 114. Generate a fresh `TraceContext` once per `Deploy`, thread it through `compileShards`
- **Files:** `internal/app/lifecycleapp/service.go`, `internal/app/lifecycleapp/service_test.go`
- **Criteria:** `Service` gains an injectable trace-context generator (func
  field defaulting to a `crypto/rand`-backed implementation, mirroring the
  existing `now`/`WithNow` seam, `service.go:208`); `Deploy` calls it once,
  before its per-scenario loop, and the identical resulting header map is
  threaded into every `compile.Input` built by `compileShards` across every
  scenario and shard of that one `Deploy` call; a test with an injected fixed
  generator asserts two scenarios in one `Deploy` get byte-identical headers,
  and two separate `Deploy` calls get different ones.
- **Satisfies:** spec "Approach" steps 1, 3; constraint "one Deploy call = one run in practice"; AC2
- **Depends on:** 113

### 115. Persist the pending correlation id on the execution
- **Files:** `migrations/0044_execution_pending_correlation.sql`,
  `internal/ports/execution_repository.go`,
  `internal/adapters/repo/mysql/execution_repository.go`,
  `internal/ports/fake/repository.go`,
  `internal/ports/repositorytest/` (execution contract),
  `internal/app/lifecycleapp/service.go`
- **Criteria:** new `pending_correlation_id VARCHAR(32) NOT NULL DEFAULT ''`
  column on `execution`; `ExecutionRepository` gains
  `SetPendingCorrelationID`/`PendingCorrelationID`, shaped like
  `SetExecutionCriteria`/`CriteriaFor` (`execution_repository.go:37-40`) — no
  change to the `execution.Execution` domain struct; `Deploy` (task 114)
  persists the generated trace id here right after generating it; mysql and
  fake pass a shared conformance case; a second `Deploy` overwrites the
  first's pending value (last-deploy-wins), asserted.
- **Satisfies:** spec "Approach" step 4; risk "pending id going stale"
- **Depends on:** 114

### 116. Widen `StartRun`/`RunRecord` to carry the correlation id
- **Files:** `migrations/0045_execution_run_history_correlation.sql`,
  `internal/ports/run_repository.go`,
  `internal/adapters/repo/mysql/run_repository.go`,
  `internal/ports/fake/run_repository.go`,
  `internal/ports/repositorytest/run_contract.go`,
  `internal/app/lifecycleapp/service.go`
- **Criteria:** `RunRepository.StartRun(ctx, executionID, correlationID
  string) (int64, error)`; `RunRecord` gains `CorrelationID string`; mysql's
  `StartRun` writes it into `execution_run_history.correlation_id`,
  `RunHistory` reads it back; fake mirrors it; the conformance contract
  asserts a started run's history round-trips the id it was given; `Trigger`
  (`service.go:387`) reads the pending id via task 115's getter (it already
  holds `coll` from `GetExecution`) and passes it to `StartRun`.
- **Satisfies:** spec "Approach" step 5; AC4
- **Depends on:** 115

## Group D — surfacing on the report

### 117. Add `CorrelationID` to `report.Meta`/`Report`
- **Files:** `internal/domain/report/report.go`,
  `internal/domain/report/accumulate.go`,
  `internal/domain/report/*_test.go`, `api/openapi.yaml`
- **Criteria:** `Meta` and `Report` both gain `CorrelationID string`
  (`json:"correlation_id,omitempty"`), placed beside `Cluster`
  (`report.go:108`, `:161`); `Accumulator.Report(m Meta) Report`
  (`accumulate.go:96`) copies it across, mirroring `Cluster`;
  `api/openapi.yaml`'s `Report` schema (`:520-551`) documents
  `correlation_id`; empty stays empty end to end.
- **Satisfies:** spec "Approach — surfacing"; AC4, AC5
- **Depends on:** —

### 118. Source the report's correlation id from run history, not the execution
- **Files:** `internal/app/metricsapp/service.go`, `internal/app/metricsapp/service_test.go`
- **Criteria:** `finalize` (`service.go:132`) sets `meta.CorrelationID` from
  `history.CorrelationID` (the `RunHistory` result) — **not** from `exe` —
  explicitly avoiding the imprecision already present for `Engine`/`Cluster`
  at `service.go:158,162`, called out in a comment; a test asserts a report's
  correlation id matches the run's stored value even when the execution's
  pending id has since moved on to a later `Deploy` (the test deliberately
  overwrites the pending id between this run's start and its finalize, and
  the report must still show the original).
- **Satisfies:** spec "Approach" step 6; the "sourced from history, not exe" decision
- **Depends on:** 116, 117

### 119. Persist the correlation id on the stored report
- **Files:** `migrations/0046_execution_report_correlation.sql`,
  `internal/adapters/repo/mysql/report_store.go`,
  `internal/ports/reportstoretest/contract.go`
- **Criteria:** new `correlation_id VARCHAR(32) NOT NULL DEFAULT ''` column
  on `execution_report`, added to `reportColumns`
  (`report_store.go:20,61,100,117,124`) and every `INSERT`/`SELECT` using it;
  a saved-then-refetched report round-trips its correlation id; the
  conformance contract covers it; an old report row (pre-migration, empty
  column) reads back with an empty correlation id, never an error.
- **Satisfies:** spec AC4, AC5, AC9
- **Depends on:** 118

## Group E — verification

### 120. e2e — fresh-per-deploy correlation id, end to end
- **Files:** `test/e2e/phase10_e2e_test.go`
- **Criteria:** against the fake scheduler + real MySQL (the phase8/9
  harness): a triggered run's compiled shard config (via the existing
  `GET /api/runs/{run_id}/scenarios/{scenario_id}/shards/{shard}/config`)
  contains a well-formed `traceparent` and `baggage`; the run's finalized
  report's `correlation_id` matches the trace id in that config; a second
  `Deploy`+`Trigger` cycle of the same execution produces a **different**
  correlation id, proving fresh-per-deploy rather than stable-per-execution.
- **Satisfies:** AC1, AC2, AC4, AC8
- **Depends on:** 116, 119

### 121. Live verification against `httpbin.pve.heri.life`
- **Files:** verification notes appended to `spec.md` (a new "Live
  verification findings" section, matching Phase 7 task 85's precedent)
- **Criteria:** on the real cluster (`/home/coder/.kube/config`), deploy and
  trigger at least one **native** (JMeter) scenario and one
  **portable/declarative** scenario pointed at `httpbin.pve.heri.life`'s
  `/headers` echo path; confirm from the observed traffic (engine-side
  captured logs, or the echoed response body if retrievable) whether
  `traceparent`/`baggage` actually arrived on the wire for each; any engine
  found not to honor scenario-level `headers:` is named explicitly in the
  findings, not silently assumed to work. Manual/cluster-dependent, a
  verification activity like task 85, not a committed automated test.
- **Satisfies:** spec AC7; constraint "live verification is a gate, not optional, for this claim"
- **Depends on:** 120
