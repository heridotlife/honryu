# 奔流 (Honryu) — Task List

**Plan:** `.cortex/2026-07-30-honryu/plan.md` · **Spec:** `.cortex/2026-07-30-honryu/spec.md`

Every task must leave `go test ./...`, `golangci-lint`, and `scripts/coverage.sh` (≥90%) green. Phases 1–4 are decomposed into individually-verifiable tasks; Phases 5–9 are phase-level goals to be decomposed when reached.

---

# Phase 1 — Vocabulary & domain realignment

Behaviour-preserving. No feature changes, no new dependencies. **No test may be deleted — only moved or renamed.**

### 1. Rename load-config domain `execution` → `loadprofile`
- **Files:** `internal/domain/execution/` → `internal/domain/loadprofile/` (+ tests), `internal/domain/run/run.go` (import), all importers
- **Criteria:** `ExecutionPlan` → `loadprofile.Entry`, `ExecutionCollection` → `loadprofile.Profile`, `Wrapper` retained for the uploaded config shape; YAML/JSON tags unchanged so persisted configs still parse; suite green
- **Satisfies:** plan "The rename is a three-way collision"
- **Depends on:** —
- **Note:** must land first — it frees the `execution` package name for task 3.

### 2. Rename `plan` → `scenario`
- **Files:** `internal/domain/plan/` → `internal/domain/scenario/` (+ tests), all importers
- **Criteria:** `plan.Plan` → `scenario.Scenario`; error identifiers reworded (`ErrNameRequired` message says `scenario:`); `MaxNameLen` comment references the new table; suite green
- **Satisfies:** spec "Vocabulary & migration"
- **Depends on:** —

### 3. Rename `collection` → `execution`
- **Files:** `internal/domain/collection/` → `internal/domain/execution/` (+ tests), `internal/domain/run/` (`Run.CollectionID` → `Run.ExecutionID`), all importers
- **Criteria:** `collection.Collection` → `execution.Execution`; `run.Run` and its phase machine reference executions; suite green
- **Satisfies:** spec "Vocabulary & migration"
- **Depends on:** 1

### 4. Rename repository ports, fakes, and conformance suites
- **Files:** `internal/ports/plan_repository.go`, `internal/ports/collection_repository.go`, `internal/ports/run_repository.go`, `internal/ports/fake/`, `internal/ports/repositorytest/`
- **Criteria:** `PlanRepository` → `ScenarioRepository`, `CollectionRepository` → `ExecutionRepository`; fakes and the shared conformance suites renamed in step; every existing conformance case still runs
- **Satisfies:** spec "Non-functional" — every port has a fake + conformance suite
- **Depends on:** 2, 3

### 4b. Rename the Scheduler port vocabulary
- **Files:** `internal/ports/scheduler.go`, `internal/ports/schedulertest/`, `internal/ports/fake/`, `internal/adapters/scheduler/k8s/`, `internal/app/lifecycleapp/`, `internal/app/adminapp/`
- **Criteria:** `DeployPlan` → `DeployScenario`, `CollectionStatus` → `ExecutionStatus`, `PurgeCollection` → `PurgeExecution`, `DeployedCollections` → `DeployedExecutions`, `PlanRef` → `ScenarioRef`, `CollectionDetail` → `ExecutionDetail`; parameters renamed to `executionID`/`scenarioID`; conformance suite still passes unchanged in substance
- **Satisfies:** plan "Verification — P1" (zero `collection`/`plan` in ports)
- **Depends on:** 4
- **Added mid-flight (2026-07-30):** `plan.md`'s Phase 1 verification required clean ports, but the original task list deferred the Scheduler port to task 22. Renaming now is orthogonal to task 22's *reshaping* (shards, clusters) and keeps Phase 2 from being written against mixed vocabulary.

### 5. Rename application use-case packages
- **Files:** `internal/app/planapp/` → `internal/app/scenarioapp/`, `internal/app/collectionapp/` → `internal/app/executionapp/`, `internal/app/lifecycleapp/`, `internal/app/metricsapp/`, `internal/app/usageapp/`, `internal/app/adminapp/`
- **Criteria:** use-case types, methods, and errors use the new vocabulary; no `Collection`/`Plan` identifiers remain in `internal/app`; suite green
- **Satisfies:** spec "Vocabulary & migration"
- **Depends on:** 4

### 6. Replace migrations with a fresh baseline schema
- **Files:** replace all of `migrations/*.sql` with a fresh baseline set, `migrations/embed.go`, `internal/adapters/repo/mysql/`
- **Criteria:** fresh baseline creates `project`, `scenario`, `execution`, `execution_scenario`, `scenario_data`, `scenario_test_file`, `execution_data`, `execution_run`, `execution_run_history`, `running_scenario`, `execution_launch`, `execution_launch_history`, `tenant`, `role_grant` (dropping the `v3_` prefix); MySQL adapter renamed to match; integration tests pass against testcontainers MySQL
- **Corrected mid-flight (2026-07-30):** originally specified as a *single* baseline file. `mysql.Migrate` (`internal/adapters/repo/mysql/migrate.go:22`) deliberately requires **one statement per file** so the app connection never enables `multiStatements` — a security-relevant choice. The baseline is therefore a fresh *set* of one-table files, not one file. History is still discarded, which is what "fresh schema" meant.
- **Satisfies:** spec "Vocabulary & migration"; plan "Fresh baseline schema"
- **Depends on:** 4

### 7. Rename the HTTP API surface
- **Files:** `internal/adapters/httpapi/` (all handlers, `router.go`), `test/e2e/`
- **Criteria:** `/api/plans*` → `/api/scenarios*`, `/api/collections*` → `/api/executions*` (and `/api/admin/collections` → `/api/admin/executions`); request/response JSON fields renamed (`plan_id` → `scenario_id`, `collection_id` → `execution_id`); **object-store key prefixes** and the `/api/files/{kind}` values renamed together (`plan/{id}/…` → `scenario/{id}/…`, `collection/{id}/…` → `execution/{id}/…`) since `{kind}` is user-visible and the two must stay consistent; no route or JSON key contains `plan` or `collection`; router and e2e tests updated
- **Satisfies:** spec AC "no `collection` or `plan` on any user-visible surface"
- **Depends on:** 5

### 8. Rebrand to Honryu
- **Files:** `cmd/api/`, `Makefile`, `.gitignore`, `internal/domain/engine/engine.go` (label helpers), `README.md`, `CHANGELOG.md`
- **Criteria:** binary is `honryu-api`; Kubernetes labels/name helpers use the `honryu` prefix; README and CHANGELOG present the product as 奔流 (Honryu); **Go module path unchanged** (moves with the repo rename); no user-facing string says "Setagaya"
- **Satisfies:** spec resolved decision #6
- **Depends on:** 7

### 9. Publish an OpenAPI document for the renamed API
- **Files:** `api/openapi.yaml` (new), `internal/adapters/httpapi/router_test.go`
- **Criteria:** every route in `router.go` appears in the document with request/response schemas; a test asserts the router's route set and the document's path set match, so drift fails CI
- **Satisfies:** spec constraint "API-first" (no artifact exists today — see plan "Gaps")
- **Depends on:** 7

---

# Phase 2 — Taurus executor, config compiler, JMX import

Single-pod execution. Sharding is Phase 3.

### 10. Add Taurus config domain types
- **Files:** `internal/domain/taurus/` (new, + tests)
- **Criteria:** pure structs for `execution[]`, `scenarios{}`, `reporting[]`, and pass/fail `criteria[]` with YAML tags matching bzt 1.16; round-trips to YAML that `bzt` accepts; zero I/O imports
- **Satisfies:** spec "Approach — Taurus is the execution contract"
- **Depends on:** 5

### 11. Model scenario portability
- **Files:** `internal/domain/scenario/` (+ tests)
- **Criteria:** a `Scenario` is `Portable` (declarative requests) or `Native` (engine-pinned artefact); `SupportedEngines()` returns the engine set; selecting an unsupported engine returns a typed error naming the reason; k6 is script-only and JMeter/Gatling/Locust/ab/siege accept declarative requests, per Phase 0
- **Satisfies:** spec "Scenario portability"; AC "rejected with a stated reason, never silently mistranslated"
- **Depends on:** 2

### 12. Add the Taurus config compiler
- **Files:** `internal/domain/compile/`, `migrations/`, `internal/adapters/repo/mysql/`, `internal/ports/repositorytest/`
- **Prerequisite from task 11 (2026-07-30):** `Scenario.Kind` and `Scenario.Engine` exist in the domain but **are not persisted** — the schema has no columns for them. A scenario read back from MySQL therefore has `Kind == ""`, and `CanRunOn` refuses every engine for it (deliberately: assuming portable would let a JMeter-pinned scenario be scheduled onto k6). This task must add the columns, persist both fields, and extend the repository conformance suite to assert they round-trip — otherwise engine selection is broken end to end the moment the compiler consults the scenario.
- **Criteria:** compiles (Execution + Scenario + LoadProfile) → Taurus YAML verified by golden files; **labels are assigned by Honryu**, never inherited from engine defaults (Phase 0 finding 2); emits pass/fail criteria when the execution carries thresholds; refuses an engine the scenario cannot run on, naming the reason
- **Label finding, settled in task 12 (2026-07-30):** the compiler assigns every request a stable label derived from the scenario name (`checkout-cart`), and **JMeter reports it back verbatim** — verified by running a compiled golden config through real bzt. But apiritif and k6 ignore the configured label and report the URL (task 10). So config-level labelling is necessary and **not sufficient**: the ingest path must normalise engine-reported labels back onto Honryu's, or cross-engine comparison breaks. Carried to tasks 20/21 (sidecar + ingest).
- **Satisfies:** spec AC "Honryu-generated Taurus YAML is valid bzt input"; AC "labels are assigned by Honryu"
- **Shape changed mid-flight (2026-07-30):** planned as a `ConfigCompiler` port with an adapter, fake, and conformance suite. Compilation is a **pure transformation** with no I/O, and every other port in this codebase fronts infrastructure. A fake compiler emitting different YAML than the real one would be useless to test against, and a conformance suite both must pass would just be the real implementation's tests. Built as a pure domain function with golden-file tests instead; the app calls it directly.
- **Depends on:** 10, 11

### 13. Reshape the `Executor` port for Taurus
- **Files:** `internal/ports/executor.go`, `internal/ports/executortest/`, `internal/ports/fake/`
- **Criteria:** port drops `Trigger`/`Stop`/`Progress`/`Subscribe(engineURL)` and instead describes an engine: `Kind()`, `Image()`, the pod command/args contract for a compiled config, and exit-code → verdict mapping (**0 = pass, 3 = criteria failed, other = error**, per Phase 0); conformance suite rewritten against the new shape
- **Satisfies:** plan "The Executor port inverts"; spec "Verdict mechanism confirmed"
- **Depends on:** 12

### 14. Prove compiled configs run on real engines
- **Files:** `test/engine/` (new, `engine` build tag), `Makefile`
- **Criteria:** a config produced by `compile.Taurus` runs under real `bzt` on **JMeter and k6**, and the outcome Honryu records matches what happened: a healthy target passes, a failing target trips criteria. Engines differ only by executor selection and artefact form — no Honryu code changes between them. Skips with a stated reason when a toolchain is absent.
- **Satisfies:** spec AC "at least three engines through the same executor adapter" (Gatling deferred — see spec open question 1)
- **Shape changed mid-flight (2026-07-30):** planned as an adapter behind the Executor port. With that port retired (task 13), nothing in the control plane runs `bzt` — the pod does, and the Scheduler creates the pod. What was worth building is the *verification* the adapter existed to provide, so this is an integration test rather than production code.
- **Depends on:** 13

### 15. Delete the bespoke executors and agent protocol
- **Files:** delete `internal/adapters/executor/jmeter/`, `internal/adapters/executor/k6/`, `cmd/agent/`; strip `engine.Config`, `engine.Metric`, `engine.BuildConfigs`, `PlanInput` from `internal/domain/engine/`; update `internal/app/lifecycleapp/`
- **Criteria:** no agent HTTP client remains; `internal/domain/engine` retains only naming/label helpers; coverage still ≥90% after the deletions
- **Satisfies:** spec "Changes to the v3 codebase"
- **Depends on:** 14

### 16. Add the JMX importer
- **Files:** `internal/ports/importer.go`, `internal/ports/importertest/`, `internal/adapters/importer/jmx/`, `internal/adapters/httpapi/` (import endpoint)
- **Criteria:** a Shibuya `.jmx` imports to a runnable **Native/JMeter-pinned** scenario with no manual editing in the common case; unsupported constructs are reported explicitly in the response, never dropped silently; importing a malformed file fails with a stated reason
- **Satisfies:** spec AC "Vocabulary & migration"; "imported .jmx are native/JMeter-pinned by default"
- **Depends on:** 11

### 17. Add the engine image catalogue and config wiring
- **Files:** `internal/config/config.go`, `cmd/api/main.go`, `deploy/` (new)
- **Criteria:** each supported engine maps to a pinned image with a validated runtime (Phase 0 finding 3); the old `EXECUTOR` env knob is replaced by per-execution engine selection with a configured default; an unknown engine fails at startup with a clear error
- **Satisfies:** spec "Engine images are per-engine, with their own toolchain pins"
- **Depends on:** 14

---

# Phase 3 — Sharding, sidecar push, aggregation

### 18. Add shard-planning domain
- **Files:** `internal/domain/shard/` (new, + tests)
- **Criteria:** splits a `loadprofile.Entry` across N shards (concurrency and throughput divided, remainder distributed deterministically) so the shards sum to the requested profile; ramp is preserved per shard; table tests cover N=1, uneven division, and N > concurrency
- **Satisfies:** spec "Execution topology"; AC "requesting N engines produces N pods that together deliver the requested profile"
- **Depends on:** 12

### 19. Add histogram merge domain
- **Files:** `internal/domain/metrics/` (new, + tests)
- **Criteria:** merges `{response_time: count}` buckets across shards and computes percentiles; **property test** asserts merged-bucket percentiles equal a direct computation over the combined raw sample set; a regression test encodes the Phase 0 finding that averaging per-interval percentiles under-reports p95
- **Satisfies:** spec AC "aggregate percentiles are computed from per-shard histograms, not averaged"
- **Depends on:** —

### 20. Build the bzt reporter shim and metrics sidecar
- **Files:** `cmd/sidecar/` (new), `deploy/engine/honryu_kpi.py` (new)
- **Criteria:** the Python shim registers as a bzt `AggregatorListener` and streams unified per-second KPI records (including `hist` buckets, `rc`, and `errors`) to the Go sidecar, which pushes them to the control plane; the sidecar flushes final results when `bzt` exits and survives bzt crashing; prototype in the Phase 0 scratchpad is the starting point
- **Done in task 20 (2026-07-31):** the sidecar maps engine-reported labels back to Honryu's via `-label-map`, verified against real bzt: apiritif reported `http://127.0.0.1:8080/ok` and the control plane received `probe-ok`. Task 21 still needs to supply the map when it launches pods.
- **Carried from task 12 (2026-07-30):** engines disagree on labels — JMeter echoes Honryu's configured label, apiritif and k6 report the URL. The sidecar or the ingest endpoint must map engine-reported labels back onto the labels Honryu assigned at compile time, otherwise the same request appears under two names across engines and service analytics cannot compare runs.
- **Finding from task 13 (2026-07-30):** on SIGTERM — the signal Kubernetes sends when deleting a pod — **bzt dies immediately and writes no final stats**. Verified against bzt 1.16.51: its signal handlers live in `cli.py`'s `if __name__ == "__main__"` block, which neither the console script nor `python -m bzt` executes, so nothing catches SIGTERM. This makes the streaming sidecar load-bearing rather than merely convenient: results that were not already pushed are lost on every teardown, and "flush final results on engine exit" cannot mean "read bzt's final report".
- **Satisfies:** spec "Live metrics path — sidecar, pushing"
- **Depends on:** 19

### 21. Add the `MetricsIngest` inbound port and endpoint
- **Files:** `internal/ports/metricsingest.go`, `internal/ports/metricsingesttest/`, `internal/adapters/httpapi/ingest_handlers.go`, `internal/app/metricsapp/`
- **Criteria:** authenticated, tenant-scoped push endpoint accepting sidecar batches; out-of-order and duplicate batches are handled idempotently; a shard that dies mid-run does not corrupt the aggregate; **outbound push only** — nothing requires the control plane to reach into a cluster
- **Satisfies:** spec AC "Engine → control-plane data flow is outbound push only"
- **Depends on:** 20

### 22. Reshape the `Scheduler` port for shards and clusters
- **Files:** `internal/ports/scheduler.go`, `internal/ports/schedulertest/`, `internal/ports/fake/`
- **Criteria:** methods renamed to the new vocabulary and reshaped around shards rather than per-plan engine URLs; every method carries a **cluster reference** so Phase 8 adds a registry rather than re-architecting; `EngineURLs` is removed with the pull protocol
- **Satisfies:** plan risk "Multi-cluster retrofit"
- **Depends on:** 15, 18

### 23. Schedule shard pods on Kubernetes
- **Files:** `internal/adapters/scheduler/k8s/`
- **Criteria:** creates N shard pods, each running `bzt` with its shard's compiled config plus the sidecar; all shards start within a tolerance small relative to the ramp duration (spec resolved decision #9); abort tears down every shard within a bounded time; passes the scheduler conformance suite
- **Finding from task 13 (2026-07-30):** deleting a pod sends SIGTERM, which kills bzt outright — no graceful shutdown, no final artifacts. To let an engine finish cleanly the pod needs a preStop hook sending **SIGINT** (which bzt does handle, shutting down gracefully and writing final stats) plus a `terminationGracePeriodSeconds` long enough to cover it. Aborts must also be recorded from Honryu's own state, since no exit code identifies one: SIGINT yields 1, indistinguishable from a config error, and SIGTERM yields 143.
- **Satisfies:** spec AC "coordinated start"; AC "abort tears down all engines within a bounded time"
- **Depends on:** 22

### 23c. Let a run finish, and say how it ended — *added 2026-07-31*
- **Files:** `internal/adapters/scheduler/k8s/k8s.go` (`engineScript`), `internal/sidecar/`, `cmd/sidecar/`, `internal/domain/metrics/interval.go` (`Batch`)
- **Found building task 28b (2026-07-31):** three linked gaps in the pod contract, all blocking finalisation.
  1. **Nothing captures bzt's exit code.** `taurus.OutcomeFromExitCode` has existed since task 13 but has no feed: `engineScript` (`k8s.go:290`) ends in `exec bzt`, the Scheduler port exposes no exit status (`EngineDetail.Status` is a pod-phase string), and the sidecar does not supervise bzt. So `report.Meta.Outcome` — the verdict — has no source.
  2. **A finished run restarts itself.** Engine pods are a StatefulSet (`k8s.go:177`), whose pods may only have `restartPolicy: Always`. `engineScript` execs bzt as the container's PID 1, so on completion the container exits and kubelet restarts it, re-running the whole load profile until the execution is purged. Not caught because task 23's tests assert pod-spec shape against a fake clientset and never run a pod to completion. **Read from the manifest, not yet observed — confirm on a real/kind cluster before fixing (decided 2026-07-31).**
  3. **`Final` does not mean "the engine finished".** The sidecar's `done` closes only on SIGTERM (`cmd/sidecar/main.go:41`), so a shard's final batch is sent at pod teardown, not engine completion. This collapses task 28b's two finalisation triggers into one event and means a naturally-completed run is never recognised as complete.
- **Shape:** the engine script captures bzt's status to the shared `kpi` volume instead of `exec`ing it, and stays alive afterwards — `bzt …; echo $? > /kpi/exit-code; sleep infinity`. The sidecar already tails that volume: it watches for `exit-code`, treats its appearance as engine completion, and carries the code on its final batch. One change, all three gaps, and the outcome arrives by the same outbound push as everything else — no control-plane reach into a cluster, nothing to redo for multi-cluster.
- **Criteria:** a completed run does not restart; the run's outcome is derived from bzt's exit code via `taurus.OutcomeFromExitCode`; a shard's final batch is sent when the engine finishes rather than when the pod is torn down; abort stays distinguishable from Honryu's own state, since SIGINT yields 1 and is indistinguishable from a config error (task 23 finding)
- **Confirmed on a real kind cluster (2026-07-31), before writing the fix, per the standing decision:** a container running only `sleep 3; exit 0` inside a StatefulSet restarted three times within a minute, each a clean `exitCode 0` / `reason Completed`. The fix pattern (capture exit code, then `exec tail -f /dev/null`) held at `restartCount: 0` in the same cluster. `kind` needed two rootless-Docker workarounds inside this sandbox, neither of which is a permanent environment change: `/dev/kmsg` doesn't exist, so kubelet crash-loops on it (worked around with `ln -s /dev/console /dev/kmsg` inside the node container); and the kubelet needs `KubeletInUserNamespace` (passed via `kubeadmConfigPatches` in the kind config) or it fails on `/proc/sys/...` permission errors. Cluster was a scratch resource, torn down after.
- **Also fixed, done in task 23c (2026-07-31):** the sidecar's own container needed the identical keep-alive treatment. `restartPolicy` is pod-wide, not per-container — a sidecar that exited cleanly after its final push would be restarted exactly like the engine was, and would re-read the KPI stream from the start under a new stream id, which the control plane takes for an unrelated stream and absorbs all over again, doubling every measurement already pushed. `Batch` gained `ExitCode *int`; the sidecar's `checkExitCode` polls the same file the engine writes.
- **Depends on:** 23
- **Blocks:** 28b stage 4 (finalisation) — now unblocked

### 24. Retire the pull-based metrics path
- **Files:** `internal/adapters/metrics/prometheus/`, `internal/app/metricsapp/`, `internal/adapters/httpapi/stream_handlers.go`
- **Criteria:** live metrics are served from ingested push data; Prometheus receives pushed series rather than scraping engine pods; the SSE stream endpoint serves aggregated shard data; no scrape target is registered per engine pod
- **Satisfies:** spec "Live metrics path"
- **Depends on:** 21, 23

---

# Phase 4 — Results, reports, logs, attribution

### 25. Add the report domain and `ReportStore` port
- **Files:** `internal/domain/report/` (new), `internal/ports/reportstore.go`, `internal/ports/reportstoretest/`, `internal/ports/fake/`
- **Criteria:** an `ExecutionReport` captures requested vs achieved load, latency percentiles from merged histograms, error breakdown, criteria evaluation, and a reference to the run's Taurus YAML; reports are immutable once written and outlive engines, Prometheus retention, and the campaign
- **Satisfies:** spec AC "Execution report persists after engine teardown"
- **Depends on:** 19, 21

### 26. Add fault attribution
- **Files:** `internal/domain/report/attribution.go` (+ tests)
- **Criteria:** every error is classified **engine-side** (generator stderr, resource exhaustion, harness failure) or **target-side** (response codes, assertion failures); an unattributed aggregate error count can never be the headline figure of a report; shares the axis used later by `saturated_by`
- **Satisfies:** spec "Fault attribution — a design invariant"
- **Depends on:** 25

### 27. Add error-signature normalisation
- **Files:** `internal/domain/report/signature.go` (+ tests)
- **Criteria:** signatures key on **response code + Honryu-assigned label**, never engine message text; a test asserts the three Phase 0 message variants for the same 404 (`"Request to … didn't succeed (404)"`, `"Not Found"`, `"Response code: 404"`) collapse to **one** signature; exemplars are retained bounded, raw errors are never stored unbounded
- **Key widened in task 27 (2026-07-31):** the signature is `(label, response code, side)`, not just `(label, response code)`. Where **no** response code arrived the side is derived from the message, so a generator that ran out of sockets and a target refusing connections both key on `(label, "")` — merging them would push engine-side exhaustion into the target's error count and undo task 26's invariant. Side is itself derived, never engine text, so the "never message text" rule holds.
- **Also changed:** `AttributedError` (task 26) is replaced by `ErrorSignature`, which embeds `Signature` and keeps `Exemplars []string` in place of the single `Message`. Grouping moved from `(code, message)` to the signature, so errors are now also separated **per label** — the report says which request failed, not just that something did. Bounds: `MaxExemplars = 3` distinct wordings, `MaxExemplarLen = 300` runes; dropping an exemplar never drops a count.
- **Satisfies:** spec resolved decision #11 as refined by Phase 0
- **Depends on:** 26

### 28. Implement the MySQL report store
- **Files:** ~~`migrations/0002_report.sql`~~ → `migrations/0018_execution_report.sql` + `migrations/0019_report_error_signature.sql` (the baseline had already grown to `0017`; migrations are tracked by filename, so they are additive), `internal/adapters/repo/mysql/report_store.go`
- **Criteria:** persists report summaries and error signatures with counts and rates; passes the `reportstoretest` conformance suite; integration tests run against testcontainers MySQL
- **Done (2026-07-31):** two tables — `execution_report` keyed by run, and `report_error_signature` keyed by `(run_id, label, response_code, side)`. The signature's three fields are **indexed columns, not `Signature.String()`**: labels are user-named and may contain the separator, and Phase 9 wants to group by label or code independently anyway. Summary and signatures are written in one transaction, and a re-save replaces the signatures rather than merging them. Per-signature `share` is stored derived at write time so trend queries can compare runs of different sizes without joining back.
- **Review findings folded in (2026-07-31):** `limit <= 0` now documented on the port as "no limit" and implemented by **omitting** the clause — SQL's `LIMIT 0` means the opposite, and the conformance suite passes 0 expecting every row. `Signature.String()` is documented as a display form, not an identity.
- **Satisfies:** spec resolved decision #8 — "aggregated error signatures to MySQL"
- **Depends on:** 25

### 28b. Accumulate pushed intervals into a run's report — *added 2026-07-31*
- **Files:** `migrations/` (working tables), `internal/adapters/repo/mysql/`, `internal/app/metricsapp/`, `internal/app/lifecycleapp/`, `internal/ports/`
- **Found reviewing Phase 4 (2026-07-31):** nothing produces the `[]metrics.Interval` that `report.Build` consumes. `metricsapp.Ingest` forwards each interval to the Prometheus sink and the event bus and **persists nothing**, so `report.Build` has no data source and tasks 28's store has nothing to store. `spec.md:92` requires results "persisted durably at run completion", which is what justifies `ReportStore` over querying Prometheus.
- **Decided (2026-07-31), option C of four:** merge each pushed batch into **bounded MySQL working tables** during ingest — `(run, label)` for histogram and counters, `(run, second)` for summed concurrency, `(run, label, code, side)` for signatures — then finalize into the report at run completion. Rejected: accumulating in memory (a control-plane restart mid-run loses the report, and it commits to a single replica); persisting raw `(shard, label, second)` rows (~2.3M for an hour on 32 shards); appending batches to the ObjectStore (`ports.ObjectStore` has no list operation, and a dead shard makes reassembly unreliable). A sidecar-sent final summary was ruled out by task 20's finding that bzt writes nothing on SIGTERM.
- **Finalization:** on **all shards `Final`, or lifecycle teardown/`StopRun`, whichever comes first** — a shard killed by SIGTERM never sends `Final`, and a run whose pods all exit without teardown would otherwise leave no report. `SaveReport` is already idempotent per run, so a double finalize is safe.
- **Criteria:** an ingested run yields a stored report whose percentiles, concurrency, and error signatures match `report.Build` over the same intervals; the working state survives a control-plane restart mid-run; finalizing twice leaves one report; working rows are dropped once finalized
- **Also fixes:** `seen.forget` (`internal/app/metricsapp/ingest.go:98`) drops the dedup memory for the **whole execution** when any one shard sends `Final`, so a later retry from a still-running shard is counted twice — inflating the very totals this task persists. Dedup state moves with the accumulation.
- **Dedup decided (2026-07-31), option C of four:** the sidecar stamps a **monotonic sequence per interval** and the control plane keeps one high-water mark per `(run, shard)`. Two facts from `sidecar.go` rule out the cheaper schemes: `flush` clears `pending` only on success (`sidecar.go:256`), so a retry is a **superset** batch, not an identical re-send — a whole-batch skip would discard new data; and `drain` is independent of the flush ticker (`sidecar.go:108`), so one second can be **split across batches** — a timestamp watermark would silently drop the second label's data for that second. Rejected: persisting today's `(shard, ts, label)` key (exact, but shards × seconds × labels ≈ 2.3M rows on a long run); a bounded recent window (silently double-counts a retry arriving after a sustained outage — the case retries exist for); byte-identical batch re-sends (needs a batch queue in the sidecar, with its own depth problem under the conditions that caused the failure).
- **Unsequenced batches are rejected (2026-07-31):** a batch whose intervals carry no sequence cannot be deduplicated, so it is refused with a stated error rather than absorbed. Only an older sidecar image pinned against a newer control plane can produce one; failing loudly beats silently double-counting a retry into a report kept forever.
- **Restart guard:** the sidecar stamps a `StreamID` per instance on the batch. A pod restart means bzt restarted, so the sequence resets; the control plane resets the watermark when the stream id changes rather than skipping the new stream's low sequences.
- **Satisfies:** spec "Prometheus is live/short-retention, so summarised results must be persisted durably at run completion"
- **Done (2026-08-01):** `metricsapp.Ingest` calls `ReportProgress.Absorb` for every batch (validated before any forwarding, so a malformed batch is rejected atomically rather than after the live view already published it) and, once all planned shards have gone `Final`, rolls their exit codes up via `taurus.CombineOutcomes` and finalizes. `lifecycleapp.teardown` finalizes as `OutcomeAborted` before `StopRun` clears the run's identity, best-effort like the adjacent `usage.RecordFinish` — a customer must be able to stop a broken execution even if writing its report fails. Both paths share one `finalize`, idempotent on `ReportStore.GetReport`, so whichever trigger arrives first decides the outcome and neither can overwrite the other's verdict.
- **Two supporting port additions:** `ports.RunRepository.RunHistory` (the fake already tracked `RunRecord` internally with nothing to read it back) supplies `StartedAt`; `ReportProgress.ShardsFinished` widened to `ShardStates` (index, finished, exit code) so an outcome can be derived per shard, not just counted.
- **ScenarioID and Requested load, when an execution bundles several scenarios:** `Report.ScenarioID` is populated only when the profile has exactly one entry (ambiguous otherwise; `Labels[]` already carries the per-request breakdown). `Requested` collapses the profile the same way usage accounting already does — concurrency via the existing `run.VirtualUsers`, throughput summed, duration taking the longest scenario.
- **Known limitation, left open:** natural completion does not clear `running_scenario` markers, close the usage launch, or advance `execution_run`'s active flag — only an explicit Stop/Purge does. Confirmed deliberate: an existing test (`TestIngest_FinalBatchReleasesDeduplicationState`) already relies on a stray post-final push still being accepted rather than rejected, which calling `StopRun` automatically would break. An execution whose run finished naturally therefore still reads as "running" everywhere except its report until a human (or, later, Phase 5 scheduling) calls Stop/Purge.
- **Verified (2026-08-01):** full suite, `golangci-lint`, `scripts/coverage.sh` at 90.0%; e2e (`TestPhase3_MetricsUsageAdminEndToEnd`) drives `Stop` through the real MySQL adapter and asserts the persisted report's identity, outcome, and achieved samples.
- **Depends on:** 28
- **Blocks:** 30 — now unblocked

### 29. Capture engine-side logs to the object store
- **Files:** `internal/app/lifecycleapp/`, `internal/adapters/storage/`, `cmd/sidecar/`
- **Criteria:** `bzt`/engine output is captured per shard and written to the existing `ObjectStore` keyed by run with a bounded TTL; logs survive pod teardown; capture failure degrades the run's diagnosability but never fails the run itself
- **Satisfies:** spec resolved decision #8 — "raw engine-side logs to the ObjectStore port"
- **Done (2026-08-03):** captured in `lifecycleapp.Purge`, the one choke point every teardown path (manual, or the auto-purge sweep) already goes through — right after `teardown` (so `Finalize`/`StopRun` have already run) and right before `sched.PurgeExecution` deletes the pods holding the logs. `Stop` alone doesn't capture: it doesn't delete pods, so on-demand `PodLog` still works until Purge eventually runs. Keyed `run/{runID}/scenario-{id}/shard-{n}.log`, best-effort per shard — one unreachable pod or one failed upload does not stop the others, and never fails `Purge` itself.
- **Shape changed mid-flight — `cmd/sidecar/` untouched:** the task list anticipated a sidecar-push path, but `ports.Scheduler.PodLog` already existed and, once shard-addressing was fixed (below), was sufficient: capture reads it directly from the control plane at Purge time, no new pod→control-plane protocol needed.
- **TTL is an infra concern, not application code:** `ObjectStore` has no TTL concept and nothing else in this codebase expires objects by time (scenario/execution files live until explicitly deleted); bounding log retention is left to a lifecycle policy on the underlying bucket (GCS/Nexus), matching the spec's "reuses adapters that already exist" — building an expiry sweep would duplicate a capability object storage already has.
- **Found starting this task, fixed first (`5a0ccb9`):** `PodLog`'s own doc comment promises per-shard addressing, but the k8s adapter always read `pods.Items[0]` regardless of the `shard` argument, and `lifecycleapp`'s own wrapper hardcoded shard 0 — so "capture per shard" would have silently captured shard 0's log N times. Untested until now: the conformance suite only ever exercised shard 0. Fixed to select the pod by StatefulSet ordinal suffix and name the `engine` container explicitly (required once a pod has more than one, true since the sidecar moved in — the real API rejects an unqualified `GetLogs` on those, unlike the fake clientset used by every prior test).
- **Verified:** full suite, `golangci-lint`, `scripts/coverage.sh` at 90.1%; all four e2e tests green, including `TestPhase3`'s `/purge` call which now exercises capture end-to-end (fake scheduler/local object store — no assertion on content yet, left for task 30's retrieval endpoint).
- **Depends on:** 23, 25

### 30. Expose report, log, and config endpoints
- **Files:** `internal/adapters/httpapi/report_handlers.go` (new), `api/openapi.yaml`, `test/e2e/`
- **Criteria:** a deliberately failed run is **fully diagnosable from the API alone** — report, engine-side logs, attributed errors, and the run's generated Taurus YAML — with no cluster access; e2e test drives a failing run against the Phase 0 stub and asserts each is retrievable
- **Satisfies:** plan "Verification — P4"; spec AC "retrievable per run via API"
- **Done (2026-08-03):** four routes — `GET /api/runs/{run_id}/report`, `GET /api/executions/{execution_id}/reports` (most recent first, `?limit=`), and per-shard `GET /api/runs/{run_id}/scenarios/{scenario_id}/shards/{shard}/{log,config}`. `Deps` gained `Reports ports.ReportStore`; log/config reuse the existing `Store ports.ObjectStore` field. Read-only, unauthorized like the existing `executionStatus`/`scenarioPodLog` GETs — no new authorization requirement introduced beyond that precedent.
- **A new gap surfaced getting here: nothing persisted the compiled Taurus YAML.** Task 25's own criteria said the report should carry "a reference to the run's Taurus YAML," but `report.Report` never gained such a field, and the compiled config only ever lived in the StatefulSet's ConfigMap — gone the moment `PurgeExecution` deletes it. Fixed without changing `Report`: `compileShards` (deploy time) now best-effort stages each shard's compiled YAML at `scenario/{id}/compiled/shard-{n}.yml`; `Trigger` (once a run id exists) snapshots it to `run/{runID}/scenario-{id}-shard-{n}.yml` — immune to a later re-deploy changing the staged copy, so a run's config endpoint always reflects what that run actually used, not whatever is currently deployed.
- **Key format is a deliberate duplicate, not a shared package.** `report_handlers.go` builds `run/%d/scenario-%d-shard-%d.%s` as a literal, matching `lifecycleapp.runLogKey`/`runConfigKey` exactly rather than importing them — the same duplication already exists between `lifecycleapp.scenarioKey` and `downloadFile`'s `%s/%s/%s` in the existing `files` route. A centralizing package would be new structure for a pattern this codebase already accepts duplicated.
- **e2e shape decided:** "against the Phase 0 stub" is realized as the same fake-scheduler-plus-real-MySQL harness phases 1–3 already use, pushing the actual apiritif 404 wording and bzt's criteria-failed exit code (3) through `/api/ingest` rather than spinning up real bzt/engine images — consistent with how Phase 3's e2e already simulates load this way. `test/engine`'s real-bzt harness (task 14) is a different, narrower test and was not extended.
- **Verified:** full suite, `golangci-lint`, `scripts/coverage.sh` at 90.5%; five e2e tests green, including the new `TestPhase4_DiagnosisEndToEnd`, which drives a two-shard run to a real `OutcomeFailed` report (target attribution 6, one 404 signature) via real MySQL, purges it, and retrieves both shards' log and config by HTTP GET alone.
- **Depends on:** 28, 29

**Phase 4 complete.**

---

### 23b. Give portable scenarios a source of requests — **done, both options**
- **Files:** `internal/app/scenarioapp/`, `internal/adapters/httpapi/`, `internal/ports/`, `migrations/`
- **Found in task 23 (2026-07-31):** a scenario is created with only a name, so it is Portable with no requests, and uploading a `.jmx` does not change that. `compile.Taurus` therefore refuses it — "portable scenario needs requests" — and **only scenarios created through JMX import can actually run**. Two ways out, not yet chosen:
  1. uploading an engine-native artefact pins the scenario to that engine (`.jmx` → JMeter, `.js` → k6), mirroring what import already does; and/or
  2. an API for defining declarative requests, which is what makes a scenario portable in the first place.
- **Done (2026-07-31):** option 1. Uploading a `.jmx` or `.js` pins the scenario to JMeter or k6 respectively, persisted through a new `SetScenarioKind` and asserted by the repository conformance suite. The e2e suite covers the flow, which was broken before: create a scenario, upload a script, deploy, run.
- **Was open:** option 2 — a way to supply declarative requests. Until it existed a scenario was only portable in principle: nothing could create one that compiled, so every runnable scenario was engine-pinned and the portability model had one live branch.
- **Deferred deliberately (2026-07-31), after Phase 3:** it blocked nobody on the migration path, whose scenarios are all JMX, and Phase 4 was the gate before Phase 5 makes runs unattended.
- **Done (2026-08-05), tasks 49-53:** exactly the shape predicted below — a Taurus `scenarios:` fragment, upload-and-validate, no new vocabulary. Full design and task breakdown at `.cortex/2026-08-05-23b-declarative-requests/{plan,tasks}.md`. New `scenario_requests` table (migration `0026`) + `SetScenarioRequests`/`GetScenarioRequests` on `ScenarioRepository` (`77b10c5`); `scenarioapp.SetRequests` validating before storing (`c69bc96`); `PUT /api/scenarios/{scenario_id}/requests` (`02c505c`); `compileShards` wired to actually feed a portable scenario's stored requests into `compile.ScenarioInput` (`945d6da`) — the critical, easy-to-miss half that makes the feature real rather than an upload endpoint into a void. Writing the final e2e test (`1f74051`) caught a second gap the plan hadn't anticipated: `ensureTestFiles` required a script for every scenario regardless of `Kind`, so `Trigger` still rejected a portable scenario post-fix; fixed alongside, plus mapping `compile.ErrRequestsRequired` to HTTP 400 (was an unmapped 500).
- **Likely shape (predicted 2026-07-31, confirmed accurate):** accept a Taurus `scenarios:` fragment rather than designing a request-definition API — it is already the compiler's input shape, and inventing a parallel vocabulary would have been the mistake this rebuild exists to undo.
- **Depends on:** 11, 16

### Ultrareview — Phase 4 hardening (2026-08-03 – 2026-08-04)

A `code-review --level high` (ultrareview) pass against the whole session's cumulative diff surfaced 15 findings across tasks 25–30, ranked most-severe first. Each was fixed in its own commit, verified against the full suite, `golangci-lint`, all five e2e tests, and `scripts/coverage.sh` (≥90%) — the MySQL-touching fixes additionally against real MySQL via testcontainers integration tests.

- **1. `mergeLabel`/`mergeSignature` raced on concurrent histogram merges** (`report_progress.go`) — two shards flushing the same label near-simultaneously could silently drop one's latency buckets. Fixed by locking each row (`SELECT ... FOR UPDATE`) before the read-merge-write (`b30dd8e`).
- **2. `report_progress_shard`'s key omitted scenario, colliding across scenarios in one run** — `shard_index` is a per-scenario StatefulSet ordinal, so two scenarios' shard 0 collided on one progress row. Fixed by keying on `(run_id, scenario_id, shard_index)` (`53525f5`).
- **3. `finalize()`'s idempotency guard raced** (`metricsapp/service.go`) — a plain `GetReport`-then-`SaveReport` check-then-act let a concurrent natural-completion and Honryu-initiated Stop both pass the guard and overwrite each other's verdict. Fixed by making `SaveReport` itself insert-once (first save wins, later saves no-op); this also fixed a second bug the same restructuring exposed — a `Discard` that failed after a successful `SaveReport` was never retried (`aa09f9e`).
- **4. Sidecar SIGTERM never reached the process** (`k8s.go`) — `/bin/sh` is the sidecar container's PID 1 and does not forward pod-teardown SIGTERM to its foreground child, so a deleted pod lost whatever the sidecar had buffered since its last periodic flush. Fixed the same way the engine container already solves the identical problem: a `PreStop` hook runs `pkill` as a new process, reaching the sidecar directly by command match and bypassing PID 1 (`3d43ae8`).
- **5. A same-pod sidecar-only restart would double-count** (`sidecar.go`) — if the sidecar container alone were ever restarted by kubelet, it would re-read the shared KPI stream from byte zero under a fresh `streamID`, and the control plane's restart handling (any `streamID` change resets the watermark to 0) would absorb everything a second time. **Closed without a fix (2026-08-04), after reconsideration:** the finding's own scenario assumed finding 10 would be fixed by letting a sidecar crash propagate as the container's own exit status ("per the next finding") — but 10 was deliberately fixed the other way, logging the crash without ever letting `/bin/sh` (PID 1) exit, specifically to avoid enabling this trigger. With that in place, no realistic path restarts *only* the sidecar container while the pod's EmptyDir survives: OOM-killing or panicking the sidecar process kills the child, not PID 1; the only ways PID 1 actually exits are whole-pod teardown (already the correctly-handled fresh-EmptyDir case) or a node-level event that recreates the whole pod anyway. Building the persisted-resume-state feature (new writable volume, streamID/seq/offset persistence, resume logic) was judged not worth it against a trigger this narrow. If a future change reopens the path — e.g. a liveness probe gets added to the sidecar container — revisit.
- **6. `finalizeCompleted` silently excluded finished-but-exit-code-less shards** (`ingest.go`) — a shard torn down before it could write bzt's exit code was dropped from the outcome rollup instead of tainting it as inconclusive. Fixed via an `exitCodeUnknown` sentinel that reuses `CombineOutcomes`'s existing dominance logic (`4e66dfb`).
- **7. `report.Meta` never set `Engine`** (`metricsapp/service.go`) — every production report was persisted with `Engine == ""`. Fixed by threading the execution's configured engine through `finalize()` (`36a9ea7`).
- **8. The reachable exit-code-check path skipped `checkExitCode()`** (`sidecar.go`) — `Run`'s `<-ctx.Done()` branch called it, but production always calls `Run` with `context.WithoutCancel`, so that branch is dead; the actually-reachable `<-done` branch did not call it, risking a `nil` exit code on a narrow timing race. Fixed by calling it unconditionally on every shutdown path — it is idempotent (`3d43ae8`, bundled with finding 4).
- **9. See finding 3** — folded into the same fix.
- **10. A crashed sidecar left no trace** (`k8s.go`) — the wrapper script never captured `/honryu-sidecar`'s exit status, so a crash fell straight through to `exec tail -f /dev/null` and the container reported `Running` forever while the shard's progress row was never marked finished. **Not** fixed by letting the crash propagate as the container's own exit status (that would recreate finding 5's double-counting risk); fixed by logging the captured exit code to stderr instead, so `kubectl logs` surfaces it even though the container's own status still won't (`fdbd838`).
- **11. The run-artefact object-store key format was typed out three times** (`lifecycleapp/service.go`, `httpapi/report_handlers.go`, both packages' tests) — a future layout change had to be applied by hand in all three. Fixed by exporting `lifecycleapp.RunShardKey` as the one builder (`98c6b35`).
- **12. `captureLogs`/`snapshotConfigs` fetched every shard sequentially** (`lifecycleapp/service.go`) — on the Purge/Trigger request paths a customer is waiting on, despite each shard's I/O being independent. Fixed with a goroutine per shard plus `sync.WaitGroup` (`e11961e`).
- **13. `Finalize` hardcoded `OutcomeAborted`, ignoring evidence a shard had already finished with** (`metricsapp/service.go`) — a real criteria failure or engine error from a shard that finished naturally just before Stop reached the run was silently overwritten with "aborted". **Not** simply switched to consulting shard exit codes for everything: `OutcomeFromExitCode`'s own doc comment establishes bzt's real exit codes cannot signal "aborted" in practice. Fixed by folding Honryu's own `OutcomeAborted` together with any already-finished shard's outcome via a new `taurus.WorstOutcome`, taking the more severe of the two (`45283cc`).
- **14. `mergeLabel`/`mergeSignature` were an N+1 pattern** (`report_progress.go`) — one insert-if-missing/lock/read/write round trip per distinct label or signature, costing 60-90 sequential round trips for a batch touching 20-30 labels. Fixed by batching each into three round trips total regardless of batch size; verified deadlock-safe under concurrent overlapping label sets by a dedicated test against real MySQL (`668698e`).
- **15. `nullIntPtr` duplicated the pre-existing `nullInt64`** (`repository.go`) — differing only in pointer element type. Unified into one generic `nullPtr[T any]` (`3ad98d5`).

**All 15 findings resolved.** 14 fixed; finding 5 closed without a fix (see above) after its trigger turned out to already be closed off by finding 10's fix. The five lower-priority cleanup findings (11, 12, 14, 15, and the `Finalize`/`finalizeCompleted` outcome-source split noted in 13) were all fixed rather than skipped, since none turned out to conflict with a documented design constraint the way finding 5 did.

# Phases 5–9 — outlines

To be decomposed into tasks when reached. Each carries the spec's acceptance criteria for its area.

### Phase 5 — Scheduling, quota, guardrails, and a first UI
Time-triggered executions (one-shot and recurring) on a cluster-aware scheduler seam; a time-bounded reservation ledger enforcing tenant- and cluster-scoped engine quotas (chosen over a simpler live-usage counter specifically to *guarantee* a scheduled run's capacity, not merely check it best-effort); a caller-scoped kill-switch; calibration runs subject to the same rules once Phase 7 exists. Also reverses the parent plan's prior "no UI" exclusion: a first, read-only UI ships this phase (React + Tailwind v4 SPA served by `cmd/api`, styled after `heridotlife`'s admin-dashboard design language). **Depends on:** Phase 4 — unattended runs must be diagnosable before they exist.

**Decomposed 2026-08-05** into tasks 31-48, via a dedicated brainstorm + write-plan pass — see `.cortex/2026-08-05-phase5-scheduling-quota-ui/spec.md`, `plan.md`, and `tasks.md` for the full design (reservation-ledger semantics, `cmd/scheduler`'s row-locking multi-replica safety, overrun/preemption rules, kill-switch scope, and the three UI pages). Task numbers below are placeholders pointing at that document until execution actually begins; not duplicated here to avoid the two copies drifting apart.

- **31-35** — reservation domain, persistence, tenant quota ceiling, the shared quota-check used by both manual `Trigger` and scheduled firing, and quota gating wired into `lifecycleapp.Trigger`/`Stop`.
- **36-43** — schedule domain, persistence, `scheduleapp`, HTTP routes, `cmd/scheduler` (fire-due-occurrences, horizon-extension, overrun-reclaim), and the kill-switch.
- **44-48** — `web/` toolchain, static-asset serving from `cmd/api`, and three read-only pages (report/run history, reservation calendar, live status).

### Phase 6 — Campaign, freeze, verdict rollup
Campaign as a pure coordination layer (window, participating services, per-service thresholds); freeze scoped to the campaign's services and clusters, blocking all non-campaign executions within scope and draining in-flight ones; rolled-up verdict naming every failing criterion; campaign report records **what other load was active** in its clusters during the window (spec residual risk on freeze scoping). **Depends on:** Phase 5.

**Decomposed 2026-08-07** into tasks 54-69, via a dedicated brainstorm + write-plan pass — see `.cortex/2026-08-07-phase6-campaign-freeze-verdict/spec.md`, `plan.md`, and `tasks.md` for the full design (Project as the "service" concept, one designated execution per participating service, freeze hooked into `lifecycleapp.Trigger` alongside the quota check, a new tenant-scoped `RoleCampaignManager` RBAC role, and the bounded criteria evaluator for named failing criteria). Task numbers below are placeholders pointing at that document until execution actually begins; not duplicated here to avoid the two copies drifting apart.

- **54-59** — campaign domain, persistence, app service, RBAC, and HTTP routes (create/list/get).
- **60-63** — freeze: shared in-scope resolver, `lifecycleapp.Trigger` gating, `cmd/scheduler`'s drain loop, kill-switch wiring.
- **64-66** — verdict: criteria evaluator, rollup + HTTP route, campaign report's "other load" annotation.
- **67-68** — UI: campaigns API client, campaigns page.
- **69** — end-to-end proof of the whole loop.

### Phase 7 — Engine calibration and fan-out
`CalibrateEngine` execution kind producing a `CapacityProfile` keyed by `(scenario, engine, pod resources)` with `saturated_by: engine|target|neither`; target-limited profiles presented as **lower bounds**, never confirmed capacity; fan-out calculator returning required engine count for a target QPS, stating clearly when no valid profile exists or it is stale; staleness on change of scenario content, engine, or pod resources. Reuses Phase 4's attribution axis and Phase 3's sharding math. **Depends on:** Phases 3, 4.

**Decomposed 2026-08-07** into tasks 70-85, via a dedicated brainstorm + write-plan pass — see `.cortex/2026-08-07-phase7-calibration-fanout/spec.md`, `plan.md`, and `tasks.md` for the full design (active QPS saturation search on a single engine pod: double-to-bracket then bisect, `saturated_by` derived from existing report signals + Phase 6's criteria evaluator; a persisted calibration-job state machine driven by a new `cmd/calibrator` — loop optionally hosted by `cmd/scheduler` — with a row-locked step claim; `CapacityProfile` keyed `(scenario, engine, cpu, memory)` with true-content-hash staleness; engine-equivalents quota; fan-out inverting the shard math; API-first, UI deferred; live verification against `httpbin.pve.heri.life`). Task numbers below are placeholders pointing at that document until execution actually begins; not duplicated here to avoid the two copies drifting apart.

### Phase 8 — Multi-cluster operation
Cluster registry with per-cluster credentials and quotas; scheduling into any registered cluster; campaigns spanning clusters yielding one verdict; BYOC left as an unimplemented seam. **Depends on:** Phases 5, 6 (built on the cluster-addressable seam from Phase 3).

**Decomposed 2026-08-11** into tasks 86-102, via a dedicated brainstorm + write-plan pass — see `.cortex/2026-08-11-phase8-multi-cluster/spec.md`, `plan.md`, and `tasks.md` for the full design (one central control plane driving a cluster registry; the k8s scheduler's single `InClusterConfig` client becomes a per-`ClusterRef` client factory + cache reading a per-cluster k8s Secret, default → `InClusterConfig`; provider-neutral static-token auth only, self-contained kubeconfigs required; mixed credential source — operator entries reference a k8s Secret directly, BYOC kubeconfigs stored encrypted-at-rest in MySQL and materialized into a Secret — with an external secrets manager as a future seam; `Cluster` added to the execution and threaded through Deploy/Trigger → scheduler + cluster-scoped quota, surfaced in the report as load origin; per-cluster sidecar-image/ingest-URL move off `ClusterConfig` onto the registry entry; `schedule.Cluster` removed in favour of the execution's; registry CRUD platform-admin-gated with connectivity/RBAC validation on register). **Two deliberate divergences from this one-liner, agreed in brainstorm:** BYOC is **implemented** (not just a seam), and automatic placement is limited to a **static default cluster** (no capacity-aware placement engine — deferred to a later phase). Task numbers here are placeholders pointing at that document until execution begins; not duplicated to avoid the two copies drifting apart.

### Phase 9 — Analytics and telemetry correlation
Per-service trends across runs (achieved QPS, p95/p99, error rate, pass/fail history) with regression flagged against the previous comparable run; campaign-over-campaign comparison marking improved/regressed/newly-at-risk services; error-signature history; W3C `traceparent` plus Honryu correlation headers propagated and surfaced for deep-linking into the customer's APM, with **no spans emitted by Honryu**. **Depends on:** Phases 4, 6 — trends need accumulated history.

**Decomposed 2026-08-12** into tasks 103-111, via a dedicated brainstorm + write-plan pass — see `.cortex/2026-08-12-phase9-analytics-telemetry/spec.md`, `plan.md`, and `tasks.md`. **Divergence from this one-liner:** the phase is **scoped to the read-side analytics half (A) only** — the go/no-go for a *campaign* (not a release) gains a **target-QPS-achieved** gate (a criteria-pass under <95% of intended load is no longer a false green, keyed on the existing `report.ShortOfRequest`); campaign-over-campaign comparison is advisory, matched by project against the tenant's most-recent-prior campaign; per-service trends flag only the binary hit-target-QPS signal (p95/error shown but not auto-flagged); error-signature history is a new cross-run read query. **No new persistence, no UI (API-first).** The **telemetry-correlation half (B)** — W3C `traceparent`/APM header injection into the generated load, surfaced for deep-linking, no spans — is **deferred to its own future phase** (it is per-engine config injection with its own granularity design, unrelated to the read-side analytics).
