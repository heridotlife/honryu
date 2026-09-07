# 奔流 (Honryu) — Specification

**Date:** 2026-07-30
**Status:** Agreed (open questions outstanding)
**Lineage:** Evolves the v3 hexagonal rebuild (formerly `github.com/heridotlife/Setagaya`, renamed to `github.com/heridotlife/honryu` on 2026-07-30), which itself replaced the `master`-branch predecessor **Shibuya**. Same repo lineage — not a rewrite.

---

## Problem

A large marketplace company runs load tests before major supersale events in order to make a **go/no-go readiness judgement** on the platform. They depend on **Shibuya**, which has three load-bearing defects:

1. **No scheduling.** Every run is triggered manually by individual service owners. There is no unattended/time-based execution, and no way to *coordinate* many services into one pre-sale event — coordination happens in people's heads and spreadsheets.
2. **Non-community vocabulary.** `collection` / `plan` force users to mentally translate to the industry-standard `execution` / `scenarios` (Taurus terms). A tax paid on every interaction, doc, and handover.
3. **Weak reporting/monitoring and rigid engine coupling.** Results and live run health are thin, and the platform is welded to specific engines rather than the engine-agnostic model the community expects.

The v3 rebuild fixed *internal architecture* (pure domain, ports/adapters, ≥90% coverage) but **kept the wrong vocabulary**, still **lacks scheduling**, and **hand-rolled per-engine executors** — so it does not yet serve this customer either.

**Reframe that drives this spec:** the customer's job-to-be-done is not "run load tests," it is **"declare the platform ready for the supersale."** Shibuya and v3 both model the mechanism (fire a run) rather than the job (produce a readiness judgement). That gap is why scheduling hurts most: a product that understood "we are running a coordinated readiness campaign" would treat scheduling as its spine, not a bolt-on.

**Key architectural insight:** "community terminology" means **Taurus (bzt)** — `execution` and `scenarios` are literally its top-level YAML keys. So *align vocabulary*, *import JMX*, and *support many engines* are not three requirements but **one decision: adopt Taurus as Honryu's configuration and execution lingua franca.**

---

## Goal

Evolve v3 into **奔流 (Honryu)** — a Kubernetes-native load-testing platform whose domain model matches how practitioners actually work:

- **Taurus-native** configuration and vocabulary; any Taurus-supported engine usable with no Honryu code change.
- **Scheduling** for self-service *dedicated* tests and PM-coordinated *multiservice campaigns*.
- A first-class **go/no-go verdict** as the product's output.
- **Engine capacity benchmarking** so engine fan-out is calculated, not guessed.
- **Reporting and analytics** at execution, service, and campaign level.
- **Low-friction Shibuya migration** via JMX → Taurus YAML import.

### Non-goals

- Not a from-scratch rewrite. Keep repo lineage, hexagonal ports, k8s scheduler, MySQL, auth/RBAC/tenancy.
- Not a bespoke execution engine. Delegate execution to Taurus; do not reinvent `bzt`.
- Not replacing Grafana/Prometheus with custom observability.
- Not adopting a third-party orchestrator (Testkube / k6-operator) — Honryu owns scheduling and campaigns, which is its irreducible value.
- Not target-saturation benchmarking in this version (engine capacity only — see Benchmark).

---

## Constraints

- **Keep repo lineage & hexagonal core.** `domain → app → ports → adapters`; TDD; **≥90% coverage gate**; Go 1.26.
- **Taurus is the execution contract.** Honryu generates Taurus YAML and runs `bzt` in-cluster. Engine support == whatever Taurus supports.
- **Multi-tenant by construction (SaaS-ready).** The marketplace is tenant #1, not the ceiling. No design may assume a single tenant or a single cluster. **Multi-cluster execution is in scope now**; BYOC is a designed seam for later.
- **Kubernetes-native**; MySQL for state; Prometheus/Grafana for live metrics (all present in v3).
- **Shibuya migration must be cheap:** existing `.jmx` assets import and convert.
- **Guardrails are in-scope, not a later hardening pass.** Unattended scheduled runs can self-inflict a fleet-wide traffic storm; blast-radius caps and a kill-switch ship *with* scheduling.

---

## Approach

### Domain re-conception

| Honryu concept | Was (Shibuya / v3) | What it is |
|---|---|---|
| **Scenario** | `plan` | A named workload definition (Taurus `scenarios` entry) plus assets (`.jmx`, scripts, data). Owned by a service owner. |
| **Execution** | `collection` | A run of one or more scenarios with a load profile — Taurus `execution`. Engine chosen here. |
| **Dedicated test** | — | Self-service execution owned by one service owner, optionally scheduled (one-shot or recurring). No PM in the loop. |
| **Campaign** | — *(new)* | **Pure coordination layer** above executions: a PM-owned readiness event (e.g. "Supersale 11.11") with a window, participating services, per-service thresholds, and a rolled-up verdict. Each service defines its own scenario. Campaigns hold no execution semantics of their own. |
| **Verdict** | — *(new)* | The go/no-go judgement: per-execution pass/fail from Taurus criteria, rolled up per service and per campaign. **This is the product's output.** |
| **CapacityProfile** | — *(new)* | Result of engine calibration: `(scenario, engine, pod resources) → QPS per engine`, reusable and invalidatable. |
| **Engine pool / quota** | ad-hoc | Tenant-scoped finite capacity that scheduling allocates against, with blast-radius ceilings. |

### Execution kinds

`sustain`, `burst`, and `long run (soak)` all answer the same question — *how does the target behave under load profile X?* Engine capacity answers a different question — *what can one generator pod produce?* Peers in an enum should answer the same question, so capacity is a distinct **kind**, not a fourth profile:

```
Execution
  ├─ Kind: MeasureTarget   → Profile: sustain | burst | soak(long-run)
  │                          → produces Verdict (pass/fail vs thresholds)
  └─ Kind: CalibrateEngine → produces CapacityProfile (no verdict)
```

Why this shape earns its keep:

- **Fan-out calculator.** A stored `CapacityProfile` turns "how many engines?" from a guess into arithmetic: 50 000 target QPS ÷ 320 QPS/pod = 157 engines.
- **Invalidation.** When scenario content, engine, or pod resources change, the profile goes **stale** and Honryu prompts recalibration. A run buried in a list of runs cannot do this.
- **Rollup hygiene.** Calibration runs must never count toward campaign readiness.

**Measurement-honesty requirement.** Calibration fires at the real target, so if the *target* saturates first you have measured the target, not the generator — and the fan-out math silently lies. `CapacityProfile` must therefore record **`saturated_by: engine | target | neither`** (engine = generator CPU/thread-bound; target = error rate/latency climbed while the generator had headroom). Only `engine` yields a trustworthy fan-out number; `target` is still useful as an early saturation signal but must be labelled as such.

### Reporting vs analytics (three levels)

A *report* describes one run; *analytics* compares runs over time. Readiness is therefore not only this campaign's verdict but the **trend**. Prometheus is live/short-retention, so summarised results must be **persisted durably at run completion** — this is what justifies a `ReportStore` port rather than querying Prometheus after the fact.

1. **Execution report** — requested vs achieved load, latency percentiles, error breakdown, criteria evaluation, link to the run's Taurus YAML.
2. **Service analytics** — one service across runs: trend of achieved QPS, p95/p99, error rate, pass/fail history; regression flagged against the previous comparable run.
3. **Campaign report & analytics** — overall go/no-go, per-service breakdown naming each failing criterion, aggregate load achieved vs sale target, and comparison against the previous campaign (improved / regressed / newly at risk).

Retention: result summaries outlive engines, Prometheus retention, and the campaign itself.

### Fault attribution — a design invariant

Analytics must answer *"did my generator break, or did the service under test break?"* This is the most consequential distinction in load testing: an error spike is routinely the generator exhausting CPU / file descriptors / connections, misread as a target failure — and wrong go/no-go calls are made on it.

This is the **same axis as `saturated_by`** in calibration, so it is one principle applied twice, not two features:

> **Every error and every saturation signal is attributed to `engine-side` or `target-side`.**

Three observability classes, distinguished:

- **Metrics** (live + trend) — QPS, latency percentiles, error rate. Prometheus live; summarised to `ReportStore` for trends.
- **Logs** — **engine-side** (`bzt`/engine stderr, harness errors, resource exhaustion) and **target-side** (HTTP status classes, response error bodies/assertion failures as observed by the generator), each visible per execution and attributed.
- **Telemetry** — correlation identifiers (run ID, execution ID, service, tenant) propagated as request headers, plus optional trace context, so a run can be joined to the target's own traces.

**Scope boundary:** Honryu owns engine-side logs and telemetry (it runs those pods). **Target-side deep telemetry stays in the customer's existing APM/observability stack** — Honryu **correlates and deep-links** (run ID, service, time window, trace/correlation IDs) rather than re-implementing APM. This keeps scope contained and is more useful, since their APM already holds the target's internals.

**Volume constraint (load-bearing):** a 50 000 QPS run emitting per-request error logs is terabytes; a raw log firehose is not viable. Error analytics therefore **aggregate by error signature** — error class/message shape → count and rate over time → a bounded number of retained exemplars (full sample payloads) — with sampling. Raw engine logs are retrievable for a bounded window; aggregated signatures are retained long-term for trends.

### Execution topology — Honryu shards, one bzt per pod

Load is distributed by **Honryu**, not by the engine: N independent engine pods, each running its own `bzt` with **1/N of the load profile**; Honryu aggregates the results.

Rejected: **bzt native distributed mode** (one controller driving engine slaves). bzt's distributed support is effectively **JMeter-only** — Gatling OSS has no distributed mode at all, and k6 OSS distributes only via execution-segment sharding. Depending on it would make engine support uneven and reintroduce exactly the per-engine coupling this rebuild deletes, while adding JMeter RMI in Kubernetes (callback ports, hostname resolution) and a controller bottleneck at high QPS.

Honryu sharding is also the model the rest of the design already assumes: `CapacityProfile`'s "QPS per engine × N engines" **is** a sharding calculation, and it matches the Shibuya/v3 lineage of N engine pods with central aggregation.

Consequences Honryu must own:

- **Coordinated start and ramp** across pods, so the shards sum to the intended profile.
- **Cross-pod aggregation via histograms.** Percentiles cannot be averaged — if each pod reports its own p95, a correct global p95 is unrecoverable. Shards must emit histogram buckets (or t-digests) so the control plane can compute true aggregate percentiles.

### Live metrics path — sidecar, pushing

A **Go sidecar** in each engine pod consumes bzt's output and **pushes** metrics to the control plane (remote-write / OTLP). Chosen over the two alternatives:

- **Over a bzt reporter/listener plugin:** that means shipping and maintaining Python coupled to `bzt`'s internal API inside every engine image, and it dies with the bzt process. A Go sidecar keeps the logic in the codebase's language, survives engine exit (can flush final results), and is the natural home for engine-vs-target attribution.
- **Over Prometheus scraping:** engine pods are short-lived and numerous — scraping loses final samples and churns service discovery. Push is also the **only** model that works with multi-cluster execution, where scraping across cluster boundaries would require opening network paths inward.

**Constraint on the sidecar:** it must consume bzt's **unified** KPI stream. If it parses JTL for JMeter, JSON for k6, and `simulation.log` for Gatling, per-engine coupling returns. Which continuous unified output bzt can actually emit is the one genuine unknown — see spike below.

### Multi-cluster — one control plane, many execution clusters

A **single control plane** (API, MySQL, reporting, analytics) schedules engine pods into any registered execution cluster. Requires a cluster registry, per-cluster credentials and quotas, and engine → control-plane push (already the chosen metrics path). Campaigns may span clusters.

**BYOC (bring-your-own-cluster)** — customers registering their own clusters against a hosted control plane — is the eventual SaaS direction but is **later**. It is designed for as a **seam**, not built now: the cluster registry, credential handling, and push-based data plane must not assume Honryu owns the cluster.

### Changes to the v3 codebase

- **Delete** `internal/adapters/executor/jmeter` and `internal/adapters/executor/k6` plus the bespoke agent protocol → replace with a single `internal/adapters/executor/taurus`.
- **Add ports:** `Scheduler`/`Clock` (time-triggered), `ConfigCompiler` (domain → Taurus YAML), `Importer` (JMX → Taurus YAML), `ReportStore`.
- **Add domains:** `scenario`, `execution` (renamed from collection), `campaign`, `verdict`, `capacity`, `schedule`.
- **Rename** brand and module to Honryu: `honryu` as the ASCII technical slug (module path, binaries, k8s labels), 奔流 as display/brand name.

### Alternatives considered and rejected

- **Keep v3's custom executors, translate Taurus YAML into them.** Rejected: re-implements `bzt`, caps engine support at what Honryu hand-codes, and preserves the maintenance burden that stopped v3 differentiating.
- **Build the campaign layer on Testkube / k6-operator.** Rejected: drops v3's working scheduler and couples to a large external dependency. Generic orchestrators run *tests*; none model a readiness campaign, the loadtest-PM role, or a rolled-up verdict — which is precisely Honryu's value.
- **Do nothing / ship v3 + two features.** Rejected: the reframe (readiness judgement, campaigns, PM role, calculated fan-out) is a genuine re-conception, not a rename. Retrofitting campaigns into a model that never had them is the more expensive path.
- **Blank-page rewrite in a new repo.** Rejected: v3's ports/adapters, auth/tenancy, and test discipline are sound scaffolding worth keeping.

---

## Acceptance criteria

### Vocabulary & migration
- API, schema, docs, and UI use `scenario` / `execution` throughout; no `collection` or `plan` on any user-visible surface.
- A Shibuya `.jmx` asset imports and produces a runnable Taurus YAML scenario with no manual editing in the common case; unsupported constructs are reported explicitly, never failing silently.

### Taurus-native execution
- Any Taurus-supported engine is selectable per execution with **zero Honryu code changes** — proven by running at least three engines through the same executor adapter. *(Phase 0: demonstrated with apiritif, JMeter, and k6.)*
- A scenario declares whether it is **portable** (declarative `requests:`) or **native** (engine-pinned artefact), and the API states which engines it can run on. Selecting an incompatible engine is rejected with a stated reason, never silently mistranslated.
- Metric **labels are assigned by Honryu**, not inherited from engine defaults, so the same request compares across engines and across runs.
- Honryu-generated Taurus YAML is valid `bzt` input and is retrievable per run via API (debuggability + escape hatch to plain Taurus).

- Load is distributed by sharding the profile across N independent engine pods; requesting N engines produces N pods that together deliver the requested profile.
- Aggregate percentiles are computed from per-shard histograms, not averaged from per-shard percentiles.

### Scheduling & guardrails
- A service owner can schedule a dedicated execution (one-shot at time T, and recurring) and it runs unattended with results available afterward.
- Concurrent engine usage never exceeds the tenant's quota; a scheduled run that would breach it is **queued or rejected with a stated reason**, never silently degraded.
- Any running execution or campaign can be aborted via API, and abort tears down all engines within a bounded time.
- While a campaign window is open, **any execution not belonging to that campaign, within the campaign's services and clusters, is rejected with a stated reason** — including participating services' own dedicated tests — and such runs in flight at window open are drained or aborted. Services outside the campaign's scope are unaffected.
- A campaign report records what other load was active in its clusters during the window, so a distorted verdict can be identified rather than silently trusted.
- Calibration executions are subject to the same quotas, guardrails, kill-switch, and freeze rules as any other execution.

### Campaign & verdict
- A PM can define a campaign (window, participating services' executions, per-service thresholds); each service owner supplies their own scenario.
- After a campaign the API returns a rolled-up verdict: per-execution pass/fail, per-service status, one overall go/no-go — with failing criteria named.
- Campaign rollups exclude `CalibrateEngine` executions.

### Benchmarking (engine capacity)
- A calibration execution yields a `CapacityProfile` keyed by `(scenario, engine, pod resources)` including QPS/engine and `saturated_by`.
- Given a target QPS, the API returns the required engine count derived from the latest valid profile, and states clearly when no valid profile exists or it is stale.
- Changing scenario content, engine, or pod resources marks the profile stale.

### Reporting, monitoring & analytics
- Live: an in-flight execution exposes current QPS, error rate, and latency percentiles (Prometheus/Grafana), per service within a campaign.
- Execution report persists after engine teardown and is retrievable by API.
- Service analytics return per-service trends across runs and flag regression against the previous comparable run.
- Campaign analytics return overall verdict plus comparison against the previous campaign.
- Every reported error is attributed **engine-side** or **target-side**, and no report presents an unattributed aggregate error count as the headline number.
- Engine-side logs (`bzt`/engine output, resource exhaustion) are retrievable per execution for a bounded retention window, without needing cluster access.
- Target-side errors are aggregated by **error signature** with counts, rate over time, and a bounded set of retained exemplars — never an unbounded raw log dump.
- Each run propagates correlation identifiers (run, execution, service, tenant) to the target so results can be joined to the customer's own APM/tracing; the report surfaces the identifiers and time window needed to do so.

- A `CapacityProfile` whose run was limited by the target records `saturated_by: target` and is presented as a **lower bound**, never as a confirmed engine capacity.

### Multi-cluster
- Engine pods can be scheduled into any registered execution cluster from a single control plane, with per-cluster credentials and quotas.
- A campaign can span services running in different clusters and still produce one rolled-up verdict.
- Engine → control-plane data flow is **outbound push only**; nothing requires the control plane to reach into a cluster's network to collect metrics.

### Non-functional
- ≥90% coverage gate holds; every new port has an in-memory fake plus a shared conformance suite that real adapters also pass.
- No design assumes a single tenant or a single cluster, or that Honryu owns the cluster it schedules into (BYOC seam).

---

## Resolved decisions

1. **Distribution** — Honryu shards; one `bzt` per pod, N pods, central aggregation. (See *Execution topology*.)
2. **Live metrics** — Go sidecar per engine pod, **pushing** to the control plane; histogram buckets for correct cross-pod percentiles. (See *Live metrics path*.)
3. **Campaign freeze** — during a campaign window, **all non-campaign executions are blocked**, including participating services' own dedicated tests. Interference would distort the readiness verdict, and verdict integrity is the product. In-flight non-campaign runs are drained or aborted at window open.
4. **Calibration target** — always the **real service**, with the target defined by the user. No stub/echo target.
5. **Shibuya migration** — **import only** (JMX assets → Taurus YAML). No Shibuya database/state migration.
6. **Rename** — brand, binaries, and Kubernetes labels become `honryu` / 奔流; the Go module path moves **with** the repository rename, so `go get` breaks once rather than twice. **Done 2026-07-30:** the repo was renamed to `github.com/heridotlife/honryu` and the module path followed in the same change.
7. **Multi-cluster** — one control plane, many registered execution clusters, **in scope now**. **BYOC is later**, designed for as a seam only.
    - **Refined by Phase 12 (2026-08-17):** the BYOC **data plane** shipped — a customer registers their own cluster via the existing `POST /api/clusters` (kubeconfig parsed, probed, encrypted at rest, materialized as a Secret), and ingest authenticates **per cluster**: a minted token, hashed at rest, scopes a pushed batch to the executions routed at that cluster, so one customer's fleet cannot push into another's or into the operator's default plane. What remains later, per the parent non-goals, is customer-**self-service**: registration stays behind the same platform-admin gate as any other cluster CRUD, and tenant-scoped registration (a customer registering only against their own tenant) is SaaS-auth work, not data-plane work. See `.cortex/2026-08-16-phase12-byoc/spec.md`.
8. **Log store** — raw engine-side logs go to the existing `ObjectStore` port (local/Nexus/GCS), keyed by run with a bounded TTL; aggregated error signatures go to MySQL via `ReportStore` for long-term trends. No Loki/OpenSearch dependency; reuses adapters that already exist.
9. **Ramp profile** — **defined per service**, in that service's execution. Shards of an execution inherit their service's ramp and must start closely enough together that the declared ramp is honoured end to end (a start tolerance small relative to ramp duration; no hard barrier required for minute-scale ramps).

### Consequences of #4 (calibration against a real target)

- `saturated_by: target` will be **common**, not exceptional. When the target limits first, the profile is recorded as a **lower bound** on engine capacity and must be labelled as such — fan-out derived from it under-provisions.
- Calibration deliberately pushes to saturation, so it **is itself a blast-radius event**: it is subject to the same quotas, guardrails, kill-switch, and campaign freeze as any other execution, and should generally be scheduled rather than run ad hoc against a live-traffic target.

10. **Telemetry** — **propagate and deep-link.** Engines send W3C `traceparent` plus Honryu correlation headers (run, execution, service, tenant); reports surface those identifiers and the run's time window so users jump into their own APM. Honryu emits **no spans of its own** and does not own a trace pipeline.
    - **Refined by Phase 10 (2026-08-15):** the "run" identifier is a trace id **minted fresh at `Deploy` time** (not derived from the DB run id, which does not exist yet when the config compiles, and not derived from stable identifiers, which would make it stable across an execution's whole history) — one `Deploy` call corresponds to one real run in practice, so this still lands as per-run. The "correlation headers" are the W3C `baggage` header (`honryu.tenant/service/execution/run=…`), not a bespoke header. `traceparent`'s sampled flag is always off, so a compliant target SDK is never forced to export a run's traffic as one giant reused-trace-id span tree. See `.cortex/2026-08-15-phase10-telemetry-correlation/spec.md`.
11. **Error-signature grouping** — **built-in heuristics first:** normalise by status class + error type + message with UUIDs, numbers, and timestamps stripped. Per-scenario override rules are deferred until real data shows the heuristics failing.
    - **Refined by Phase 0:** signatures must key on **response code + Honryu label**, *not* engine message text. The same 404 is reported as three different strings by apiritif, JMeter, and k6, so message-keyed grouping would fragment the same failure per engine and break cross-engine trend analytics. Response codes were consistent across all engines; message text is a display detail and an exemplar, never the grouping key.
12. **Freeze scope** — **scoped to the campaign's services and the clusters it touches**, not tenant-wide. Within that scope every non-campaign execution is blocked (including participants' own dedicated tests, per #3); unrelated teams keep working.

### Residual risk on #12

Service-scoped freezing is only as trustworthy as Honryu's notion of blast radius. A **non-participating** service can still distort a campaign's readiness numbers by contending for **shared dependencies** — the same database, cache, message broker, or network path — even though it was never registered in the campaign. Service-level scoping cannot see that.

Mitigation options, deliberately not chosen yet (no evidence which is needed): cluster-level freezing as a coarse fallback; declared service dependencies; or simply surfacing "other load active during this window" as an annotation on the campaign report so a suspicious verdict can be explained rather than silently trusted. **A verdict that was distorted by uncontrolled concurrent load and does not say so is the most dangerous failure mode in this product**, so at minimum the report should record what else was running.

## Phase 0 spike results (2026-07-30) — RESOLVED

Run against bzt 1.16.51 / Python 3.13, one scenario, a local stub target with an induced ~10% 500 / ~5% 404 error rate, executed on **apiritif, JMeter, and k6** by changing only `execution.0.executor`.

### The unified stream exists — verdict: proceed

bzt's `ConsolidatingAggregator` emits a normalised per-second `DataPoint` to any module implementing `AggregatorListener.aggregated_second(data)`. Verified fields, identical across all three engines: `ts, label, concurrency, throughput, succ, fail, bytes, avg_rt, avg_lt, avg_ct, perc, hist, rc, errors`.

- **A ~40-line Python shim is sufficient** for the sidecar feed (prototype: `honryu_kpi.HonryuKPIDump`, registered via `reporting:` + a `modules:` alias). This is the minimum coupling to bzt internals and it is small enough to own.
- **Response times are an `HdrHistogram`.** `RespTimesCounter.__json__()` emits `{response_time_seconds: count}` buckets and `merge()` performs an exact HDR add — so bzt's own data model solves cross-shard aggregation.
- **`bzt.modules.influxdb_reporter` is a working precedent** for push-based reporting over this same listener, but it ships **computed percentiles**, so it cannot be the sidecar feed.

### Histogram merging validated — and naive aggregation fails in the dangerous direction

Merging per-interval buckets and recomputing gave the correct aggregate percentile; averaging per-interval p95 under-reported it by **1.1%–7.6%** even in these short, evenly-loaded runs (skewed shards would be worse). The error direction matters: naive averaging **under-reports latency**, which would pass a sale that should have failed. Confirms the histogram requirement is a correctness constraint, not an optimisation.

### Verdict mechanism confirmed

`bzt.modules.passfail` evaluates Taurus criteria and bzt exits with **code 3** on criteria failure (0 = pass, 1 = config/tool error). So per-execution pass/fail can be read from bzt directly; Honryu's Verdict rolls those up.

### Three findings that change the spec

1. **Scenario definitions are NOT portable across engines — only results are.** `k6` is **script-only** (`'script' should be present for k6 executor`); `jmeter`, `gatling`, `locust`, `ab`, and `siege` accept declarative `requests:`. So "swap the engine" is free for *Honryu's* code but **not transparent for the user's scenario**. See *Scenario portability* below.
2. **Labels and error messages are not normalised across engines.** The same request appeared as label `http://127.0.0.1:8080/ok` under apiritif/k6 but `ok-endpoint` under JMeter. The same 404 was reported as `"Request to ... didn't succeed (404)"` (apiritif), `"Not Found"` (JMeter), and `"Response code: 404"` (k6). **Response codes were consistent across all three.**
3. **Engine images are per-engine, with their own toolchain pins.** bzt's bundled Gatling 3.9.5 compiles its generated Scala simulation at runtime with Scala 2.13.10, which **cannot read JDK 21 class files** — Gatling failed with `ClassfileParser … errorBadIndex` on this host. A single universal bzt image is not viable; each engine image needs a validated engine+runtime pairing.

### Scenario portability (new model, from finding 1)

A Scenario is one of:

- **Portable** — declarative Taurus `requests:`. Runs on any declarative-capable engine; the engine is genuinely a per-execution choice.
- **Native** — an engine-specific artefact (`.jmx`, k6 JS, Gatling Scala). Pinned to that engine.

Honryu must **surface which engines a scenario can run on** rather than implying universal swapping. Imported Shibuya `.jmx` files are **native/JMeter-pinned** by default; converting them to portable scenarios is a separate, best-effort step.

> **Known limitation (2026-07-31, deferred).** Only the *native* branch is reachable today. A scenario is pinned by the script uploaded to it (`.jmx` → JMeter, `.js` → k6), and there is **no way through the API to create a portable scenario**, because nothing supplies the declarative requests one needs. So "the engine is a per-execution choice" is currently **false for every scenario Honryu can create** — each is pinned to the engine its artefact belongs to.
>
> This does not block the Shibuya migration, whose scenarios are all JMX. It is deferred as **task 23b**, whose likely answer is to accept a **Taurus `scenarios:` fragment** directly rather than invent a request-definition API — that is already the compiler's input shape, and inventing a parallel vocabulary is the mistake this whole rebuild exists to undo.

## Open questions

*(none blocking)*

1. **Gatling engine+JDK pairing** — which JDK does bzt's bundled Gatling actually require (≤17? ≤19?), or should Honryu pin a newer Gatling than bzt's default? Deferrable to whenever Gatling support is actually built; JMeter and k6 are unblocked. Still open as of Phase 15 (2026-08-17) — Phase 11 added k6 but deliberately left Gatling out, restating this same JDK/Scala pairing reason.
2. **Operator-SPA tenancy stance** — added 2026-08-17 (Phase 15), carried forward from Phase 13, which surfaced the SPA without deciding it. This constraint (`:50`, `:214`) says no design may assume a single tenant; the SPA is embedded in the API binary and calls the same API under the same auth, so it enforces nothing of its own and cannot leak what the API will not serve — but that only holds if every endpoint the SPA consumes is actually scoped correctly, which has not been verified. To be decided at whichever future UI phase next extends the SPA meaningfully, informed by the endpoint-scoping audit Phase 15 ran as a precondition for Phase 16's exposure of the platform at a hostname (`.cortex/2026-08-17-phase15-governance-closeout/spec.md`, `.cortex/2026-08-17-phase16-homelab-deployment/spec.md`).
3. **Dependabot security updates bypass the develop lane** — found 2026-08-19 while draining the queue. Phase 14 set `target-branch: develop` on all four ecosystems, and it demonstrably works: every *version* update raised since (#200–#204) targets develop. But GitHub exempts **security** updates from `target-branch` by design — they always open against the default branch — so #205 (`moby/go-archive`, CVE-2026-17106) arrived on `main`, outside the `feat/* → develop → main` flow the rest of the scheme depends on. Not a misconfiguration, and not fixable in `dependabot.yml`. The options are to accept it and hand-port such PRs onto a feat branch (what was done for #205), or to make `develop` the default branch so both kinds land there and `main` becomes purely the promoted branch. Worth deciding before a security PR is ever merged directly to main, which would leave a commit on main that develop does not have and quietly break the promote flow.

---

## Suggested phasing

Each phase independently shippable. Ordering principle: **prove the risky unknown first, then rebuild the execution plumbing, then layer the product on top.**

| Phase | Delivers | Why here |
|---|---|---|
| **0** ✅ | **Spike (done 2026-07-30):** unified KPI stream confirmed, histogram merge validated, three engines run, verdict mechanism confirmed. See *Phase 0 spike results*. | Timeboxed, throwaway. The sidecar design and the engine-agnosticism claim hung on this. Also surfaced scenario-portability tiers and per-engine image pinning, both now folded into the spec. |
| **1** | Vocabulary & domain realignment: `plan`→`scenario`, `collection`→`execution`, across domain, API, schema, docs | Pure refactor, no new behaviour, no new dependencies. Cheapest to do while the surface is still small; everything after it is written in the right language. |
| **2** | Taurus executor + `ConfigCompiler` (domain → Taurus YAML) + JMX import; delete `executor/jmeter` and `executor/k6` and the v3 agent protocol | The architectural pivot. Single-pod runs, any Taurus engine, and Shibuya migration all land together — this is the phase that deletes the most code. |
| **3** | Sharding (N pods, 1/N profile, coordinated start) + sidecar push + histogram aggregation | Restores real load capability on the new engine, and makes cross-pod percentiles correct. Consumes Phase 0's answer. |
| **4** | Result persistence + per-execution report + engine-side log visibility with engine-vs-target attribution | Everything downstream reads from this. Log visibility belongs **with** reports rather than at the end — see note below. |
| **5** | Scheduling + quota + guardrails + kill-switch, on a **cluster-aware scheduler seam** | The customer's #1 missing feature. Built after Phase 4 so an unattended 2am failure is diagnosable. The scheduler is made cluster-addressable here so Phase 8 isn't a retrofit. |
| **6** | Campaign + freeze enforcement + verdict rollup + campaign report | The go/no-go product — the reason the customer pays. |
| **7** | Engine calibration + `CapacityProfile` + fan-out calculator | Reuses Phase 4's attribution logic (`saturated_by` is the same engine/target axis) and Phase 3's sharding math. Turns engine sizing from guesswork into arithmetic. |
| **8** | Multi-cluster operation: cluster registry, per-cluster credentials and quotas, cross-cluster campaigns | Capability lands here, but its **seam is designed in from Phase 5**. Building full multi-cluster earlier would add cost before any customer value. |
| **9** | Service & campaign analytics: trends, regression, error-signature history | Needs accumulated history from Phases 4–6 before trends mean anything. Telemetry correlation was scoped out of this phase and landed as Phase 10 instead. |
| **10** | Telemetry correlation: W3C `traceparent`/`baggage` minted fresh per `Deploy`, surfaced on the report as `correlation_id` | Phase 9's own hand-off named this the telemetry half it deliberately deferred. See *Resolved decisions* #10. |
| **11** | k6 engine image + lifecycle hardening: bounded trigger-readiness wait, orphaned-run reconciliation | Both come straight out of Phase 10's live verification: the engine gap it named (k6 untested) and the operational gaps it hit (trigger readiness, stranded runs). |
| **12** | BYOC data plane: per-cluster ingest token, hashed at rest, batch→cluster scoping | Turns the seam Phases 5–8 designed in into the customer-facing capability the parent spec always pointed at. See *Resolved decisions* #7. |
| **13** | Operator UI: Reports/Campaigns/Clusters/LiveStatus surface Phases 8–12 (correlation ids, trends, comparison, cluster registry, k6 and hardened lifecycle states) | Phases 8–12 landed with no UI; an operator read JSON with curl. Read-only by design — no new API endpoints. |
| **14** | CI green: Go 1.26.6 (6 stdlib CVEs), coverage gate restored to ≥90%, Node-24 action runtimes, dependabot drained | Phases 10–12 merged below the 90% coverage constraint (`:48`, `:213`) because a local `feat→develop` merge runs no CI — the gate's first check was the develop→main promote PR, after the code was already on develop. |

| **15** | Governance closeout: merge-time coverage gate (`make phase-merge`), parent-spec artifact truth, BYOC credential-Secret teardown on cluster delete | Phases 10–12 each breached the ≥90% constraint and the parent spec had stopped describing the project it governs; the credential leak had been deferred twice and owned by nobody. |
| **16** | Homelab deployment: Helm chart for the whole platform (incl. the never-deployed `honryu-scheduler`), durable storage, Prometheus + Grafana, reachable at a hostname | The platform ran on talos-homelab for a week declared nowhere in the repo, on an `emptyDir` MySQL, with scheduling — the headline feature — not running at all. |
| **17** *(optional, stub)* | Rust sidecar behind a versioned, language-agnostic wire contract; control plane and API stay Go | Recorded intent only, not an agreed spec — see `.cortex/2026-08-18-phase17-rust-sidecar/spec.md`. Justified on measurement fidelity (a GC competing with the generator in the same pod biases `saturated_by`), explicitly **not** on throughput: the sidecar moves ~1 message/sec/pod. |
| **18** *(drafted)* | Gap closure: engine pod placement + real node-pool reporting, API path for an ordinary execution's CPU/memory, schedule firing-failure policy, and a decision on the auth posture | Collects the seven app-level defects phases 15–16 found live and deliberately deferred under their "no application code changes" non-goals. Two of them make advertised capabilities untrue: Karpenter cannot be used (engine pods carry no placement fields and request nothing), and `NodePools()` reports a hardcoded GKE label. See `.cortex/2026-08-19-phase18-gap-closure/spec.md`. |

**Note on Phase 4 (log visibility).** Placing engine-side logs with reports rather than in the final analytics phase is a deliberate change from the first draft: Phase 5 introduces **unattended** execution, and a scheduled run that fails at 2am is undebuggable without logs. Shipping scheduling before log visibility means every early failure requires cluster access to diagnose.

**Ordering decided (2026-07-30):** reporting and engine-side logs ship **before** scheduling, as tabled above. The rejected alternative — scheduling first, to get the customer's headline feature out sooner — was declined because it puts unattended execution on the least-proven Taurus plumbing at exactly the moment failures are most likely and least diagnosable.
