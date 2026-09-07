# Phase 9 — Analytics and telemetry correlation

Agreed via brainstorm 2026-08-12, continuing the brainstorm → write-plan →
execute-plan cadence of Phases 5/6/7/8. Roadmap one-liner
(`.cortex/2026-07-30-honryu/tasks.md` line 342-343): "Per-service trends across
runs (achieved QPS, p95/p99, error rate, pass/fail history) with regression
flagged against the previous comparable run; campaign-over-campaign comparison
marking improved/regressed/newly-at-risk services; error-signature history; W3C
traceparent plus Honryu correlation headers propagated and surfaced for
deep-linking into the customer's APM, with no spans emitted by Honryu. Depends
on: Phases 4, 6 — trends need accumulated history."

**Scope decision (brainstorm):** the roadmap line bundles two unrelated kinds
of work. **(A) read-side analytics** over accumulated report history, and
**(B) telemetry correlation** — injecting W3C `traceparent` + Honryu
correlation headers into the *generated load* so a run deep-links into the
customer's APM. They share a roadmap line but barely share code. **Phase 9 is
scoped to (A) only.** (B) is deferred to its own future phase (telemetry
correlation): it is engine-config injection at compile/deploy time, per-engine,
with its own granularity design (per-run vs per-request `traceparent`) and
deserves a dedicated pass rather than half-baking the APM story here.

## Problem

Honryu already persists a report per run (`execution_report` +
`report_error_signature`, keyed by run; `report.Meta` now also carries
`Cluster`), and Phase 6 rolls a campaign into a go/no-go verdict. Two gaps:

- **The verdict gates only on the Taurus outcome** (`campaignapp/verdict.go:87`:
  `Go` requires `Outcome == OutcomePassed`). A service that "passed" its criteria
  under, say, 60% of its intended load reads as a clean **go**, even though its
  latency/error numbers were measured under the wrong load.
  `report.ShortOfRequest()` already detects this per-report, but nothing acts on
  it — so a campaign can go green on results that do not reflect real target
  load.
- **The stored history is never surfaced.** All run history lives in the report
  tables, but a PM/SRE deciding go/no-go sees only one run's snapshot — no
  per-service trend, no comparison to the last campaign, no error-signature
  history.

## Goal

Turn the accumulated report history into decision-support — all read-side over
existing data, API-first:

1. Extend the campaign go/no-go so a service is a real **go** only if it *passed
   its criteria AND achieved its target QPS*.
2. Compare a campaign to its predecessor, labeling each service improved /
   regressed / newly-at-risk.
3. Surface per-service run trends and error-signature history as advisory views.

## Non-goals

- **No telemetry correlation** (traceparent / APM) — deferred to its own future
  phase. Honryu still emits no spans of its own; that whole half is out of scope
  here.
- **No new analytics store or materialized aggregates** — everything computed on
  read from the existing tables. No new persistence table.
- **No auto-flagging of noisy metrics** (p95/p99/error-rate) as regressions —
  those are advisory series; the *only* flagged signal is the binary "hit target
  QPS," which is clean enough to gate/flag honestly.
- **No UI** this phase — the Phase-5 read-only SPA is untouched; API-first,
  consistent with Phases 7/8.
- **No per-service QPS-tolerance override** — reuse the 95% default; a per-service
  override is a later refinement.
- **No cross-tenant analytics.**
- Calibration is untouched: `CalibrateEngine` is already excluded from the
  campaign rollup (Phase 7), so the QPS gate never applies to a calibration run.

## Constraints

### Technical / structural
- **Backward compatible.** Existing verdicts keep working; the QPS gate only
  *tightens* go, and only when a target QPS was actually requested
  (`Requested.Throughput > 0`). A run requesting unlimited throughput
  (soak / max-find) has no target to fall short of and is unaffected — and
  "no target requested" is treated as "not gated on QPS," never as a shortfall.
- **"Comparable run"** = *same execution + same requested load
  (concurrency / throughput / duration) + same engine + same cluster*. A run
  differing in any one has **no comparable predecessor** → the state is
  **"no baseline"**, a first-class result, never a false regression.
- **Target-QPS-achieved** reuses `report.ShortOfRequest` semantics: achieved
  ≥ **95%** of requested throughput. Applies only when a target was requested.
- **Campaign comparison** matches services by `ProjectID` (a project appears at
  most once per campaign — `campaign.go:82`). Baseline defaults to the tenant's
  **most recent prior *ended* campaign**, with an explicit baseline-campaign-id
  override. A project present in one campaign but not the other is **new** /
  **dropped**, not a regression.
- **Hexagonal, matching Phases 5–8.** New read queries go through ports (with a
  repository conformance contract and MySQL conformance). The gate, comparison,
  and trend logic is **pure domain** — no I/O. Domain imports no ports.
- **No new persistence.** All analytics are computed on read from
  `execution_report` / `report_error_signature`. Any new repository method is a
  read query over those tables.

### Safety / correctness
- The QPS gate must not turn a currently-passing campaign red on a run that had
  no target QPS; the unlimited-throughput guard is load-bearing.
- Advisory views must never influence the go/no-go; the verdict is the absolute
  per-campaign signal (passed + hit target QPS). The comparison only *labels*
  services relative to a baseline.

### Verification bar (as Phases 5–8)
`go build` / `vet` / `gofmt` / `golangci-lint`, unit tests, MySQL conformance for
any new read query, e2e where relevant, `scripts/coverage.sh` as a check. Live
verification is optional (analytics is read-side; it could be sanity-checked
against the accumulated reports in the kept `honryu` namespace, but is not a gate).

## Approach

### The one domain change — target-QPS in the verdict
Extend the campaign verdict: a service's `Go` requires `Outcome == Passed`
**AND** (no target QPS requested **OR** achieved ≥ 95% of target). A no-go
carries a per-service reason naming the shortfall ("short of target QPS: achieved
X of Y"). This is the only domain mutation; everything else is read-side.

### Campaign-over-campaign comparison (advisory)
A pure domain classifier takes this campaign's per-service go/no-go signals and
the baseline campaign's, matches by `ProjectID`, and emits one of **improved /
regressed / newly-at-risk / still-at-risk / new / dropped** per service. An app
method resolves the baseline (the tenant's most recent prior ended campaign, or
an explicit id) and both campaigns' verdicts, then calls the classifier. No new
storage — it diffs two computed verdicts.

### Per-service trends (advisory)
Read an execution's run history via the existing `ListReports(execution, limit)`.
A pure domain computation builds the series — achieved QPS, p50/95/99, error
rate, outcome, hit-target-QPS per run, most-recent-first — and attaches the
run-over-run **regression flag = the binary hit-target-QPS signal flipping**
between comparable runs; a run with no comparable predecessor is **no baseline**.
p95/p99/error-rate are surfaced as raw series, never auto-flagged.

### Error-signature history (advisory)
A new read query aggregates `report_error_signature` across an execution's runs,
groupable **by label** and **by response code** independently (the Phase 4
annotation, tasks.md:224, anticipated exactly this — the three fields are indexed
columns, not a composite string). A pure domain aggregation shapes it for the API.

### HTTP
New read endpoints — per-service trend, campaign comparison, error-signature
history — documented in `openapi.yaml` (the parity test enforces it), scoped to
the caller's authorization exactly like the existing report / campaign routes.

### Rejected alternative
A precomputed analytics/trends table or a rollup job. Rejected: the source rows
already exist and per-execution report volumes are modest, so query-time
computation is simpler, always fresh, and adds no write path or migration.
Reconsider only if a trend query over a very long history becomes a measured
hotspot.

## Acceptance criteria

1. A campaign whose service *passed its criteria* but achieved **< 95% of its
   target QPS** is **no-go**, with a per-service reason naming the shortfall
   (achieved X of Y).
2. A service that requested **no target QPS** (unlimited) is unaffected by the
   QPS gate; a campaign still goes **go** when every service passed AND (hit
   target QPS or had none). Pre-Phase-9 campaigns behave identically.
3. Campaign comparison classifies each project-service as **improved / regressed
   / newly-at-risk / still-at-risk / new / dropped** against the baseline
   (default = most-recent-prior *ended* campaign for the tenant; explicit
   override), matched by `ProjectID`.
4. A project present in one campaign but not the baseline is **new** / **dropped**
   — never a false regression.
5. Per-service trend returns the run series (achieved QPS, p50/95/99, error rate,
   outcome, hit-target-QPS) most-recent-first; a run is flagged **regressed** iff
   its hit-target-QPS signal flipped from a *comparable* predecessor; a run with
   no comparable predecessor is **no baseline**.
6. "Comparable" = same execution + same requested load + same engine + same
   cluster; differ in any → **no baseline**, not a regression.
7. Error-signature history aggregates an execution's signatures across its runs,
   groupable **by label** and **by response code** independently.
8. All new endpoints are documented (openapi parity test passes) and scoped to
   the caller's authorization like existing report / campaign endpoints.
9. **No new persistence table**; all analytics computed on read from
   `execution_report` / `report_error_signature`.
10. Verdict, comparison, and trend logic are **pure domain with table tests**;
    new repo queries have **MySQL conformance**; an **e2e** covers a campaign
    flipped to no-go on QPS *and* a campaign comparison.

## Open questions

Deferred to write-plan; none block the design.

- Trend look-back default (e.g. last N runs) and whether the API exposes a
  `limit` — lean: reuse the existing `limit` convention.
- Whether the campaign-comparison endpoint returns full per-service detail or a
  summary + drill-down — lean: one response with per-service rows.
- Exact new endpoint shapes / paths — deferred, as in prior phases.
- Whether a large p95 jump with QPS held should ever surface in the *campaign*
  comparison — leaning no (strictly advisory, QPS-signal only); revisit if it
  feels thin in practice.

## Hand-off note

**(B) telemetry correlation** is explicitly carried forward as a future phase:
propagate a W3C `traceparent` + a Honryu correlation header through the generated
load (engine-config injection, per-engine), surface the run's correlation id on
the report for APM deep-linking, and emit no spans of Honryu's own. Its
granularity (per-run vs per-request `traceparent`) and multi-engine support are
its design surface, untouched here.
