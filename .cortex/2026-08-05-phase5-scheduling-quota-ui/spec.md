# Phase 5 — Scheduling, quota, guardrails, and a first UI

**Parent plan:** `.cortex/2026-07-30-honryu/plan.md` · **Parent spec:** `.cortex/2026-07-30-honryu/spec.md`

This is a sub-spec decomposing Phase 5 of the parent plan (currently a one-paragraph outline in `tasks.md`) into an agreed design, and folding in a UI dimension the parent plan had previously excluded.

---

## Problem

Every execution today is triggered manually by a human watching it. Phase 4 shipped diagnosability (reports, logs, config all retrievable after the fact), but there is still no way to run a test unattended at a scheduled time, no way to bound how many engines a tenant can consume concurrently, and no way to abort a batch of runs at once. Nothing prevents an unattended scheduled run from self-inflicting a fleet-wide traffic storm, and nothing lets an operator stop one short of killing executions one at a time.

## Goal

Ship scheduling (one-shot and recurring), quota enforcement with a real guarantee (not best-effort), and a scoped kill-switch — with a first, read-only UI surface, since this is unattended operation's first real user-facing exposure and the parent plan's prior "no UI" decision is being reversed here.

## Non-goals (this phase)

- **Multi-cluster registry / credentials** (Phase 8). Phase 5 uses the `ClusterRef` concept Phase 3 already put on the `Scheduler` port; one implicit default cluster exists in practice until Phase 8's registry lands. Quota is keyed by `(tenant, ClusterRef)` from the start specifically so Phase 8 isn't a retrofit — this was already decided in the parent plan's risk table.
- **Campaign freeze interaction** (Phase 6). Campaigns don't exist yet; this phase's guardrails operate per-execution/per-tenant only. The kill-switch's scope enum includes `campaign` as a value now so its endpoint doesn't need touching again once Phase 6 lands, but campaign-scoped invocation isn't reachable until then.
- **Calibration execution kind** (Phase 7). Doesn't exist yet, but the quota/guardrail mechanism must be generic enough that a future `CalibrateEngine` kind is governed by it automatically (per parent spec: "Calibration executions are subject to the same quotas, guardrails, kill-switch, and freeze rules as any other execution") — not something Phase 7 has to bolt on separately.
- **UI write actions.** Creating/editing a schedule or invoking the kill-switch from the browser is a later task. This phase's UI is read-only.
- **Task 23b** (declarative scenario requests) is separate, already scoped from prior research, and executed independently of this spec.

## Constraints

- Must reuse the existing `Scheduler` port's `ClusterRef` cluster-addressing rather than introducing a parallel concept.
- Must follow the codebase's existing lock-then-act idiom (e.g. `lockShard` in `internal/adapters/repo/mysql/report_progress.go`) for any new concurrent-claim logic, rather than introducing leader election or another new coordination mechanism.
- Every new port needs an in-memory fake plus a conformance suite real adapters also pass (existing project-wide non-functional bar); `go test ./...`, `golangci-lint`, and `scripts/coverage.sh` at ≥90% stay green.
- No design may assume a single tenant or a single cluster (existing non-functional constraint from the parent spec).
- The UI is React + Tailwind v4, built as static assets served by the existing `cmd/api` Go binary — one deployable, same Kubernetes rollout — not a separately deployed frontend service. This is a durable decision for all future UI work (Phases 6-9 too), not scoped to Phase 5 alone.
- UI styling follows the design *language* of `~/personal/heridotlife`'s existing admin dashboard (`DashboardLayout`, `StatsCard`, `Button`/`Card`/`Input` components, sky/blue accent palette, light/dark via a `.dark` class variant) — not its Astro/Cloudflare Workers/D1 deployment stack, which doesn't fit a Go service on Kubernetes.

## Approach

### Quota & reservation ledger

A new time-bounded `Reservation` concept: `(tenant, cluster ref, engine count, start time, end time, owning execution/schedule)`. Every accepted run — a manual `Trigger` call or a scheduled occurrence — gets a reservation for `[start, start+duration)`, using the `Duration` already present on `loadprofile.Entry`. A quota check is "does adding this reservation cause any overlapping time window to exceed the tenant+cluster's ceiling" — an interval-overlap check against the ledger, not a simple running counter.

- **Manual `Trigger`:** reservation starts now, checked synchronously in the same request; reject immediately with a stated reason if it doesn't fit. No queueing — a human is present and can retry.
- **One-shot schedule:** reservation created and checked at schedule-creation time for its single future fire time; re-checked again when that time actually arrives, catching drift from other activity in between.
- **Recurring schedule:** rolling 7-day lookahead. At creation, every occurrence in the next 7 days is computed and reserved independently. Some may fit and some may not — **partial success**: the ones that fit are reserved, the ones that don't are marked rejected up front, both visible to the schedule's owner. A background job extends the horizon forward over time as old occurrences complete and new ones roll into range; its last-successful-run must be observable (a stalled extension job must not silently leave future occurrences unguarded).
- **Stop early** releases the remaining reserved window immediately.
- **Overrun** past the declared duration is tolerated if capacity allows. If the *same tenant's* own new reservation needs that capacity, the overrunning execution is force-stopped to free it. Preemption is self-contained per tenant — since quota is tenant-scoped, a reservation can only ever preempt that same tenant's own overrunning run, never another tenant's.

*Rejected alternative:* a simple "count current live usage" check with no ledger. Rejected because it can only be best-effort at the moment a run fires — it cannot guarantee a future one-shot's capacity ahead of time, which is the actual requirement (spec: "queued or rejected... never silently degraded").

### Scheduler seam

A new `cmd/scheduler` binary/deployment, decoupled from `cmd/api` so a scheduler stall doesn't ride along with API restarts and its health is independently observable. Responsibilities:

1. Fire due one-shot/recurring occurrences — deploy and trigger the execution when its reserved fire time arrives.
2. Roll the 7-day reservation horizon forward for active recurring schedules.
3. Reclaim capacity from overrunning executions when a new reservation needs it (per-tenant, as above).

Runs on an interval tick. To allow more than one replica safely (avoiding double-firing the same due occurrence) without needing leader election, due occurrences are claimed via row-locking — the same lock-then-act pattern already used throughout this codebase's MySQL adapters, applied to schedule/occurrence rows instead of shard rows.

### Kill-switch

A caller-scoped abort: `tenant`, `cluster`, `campaign` (unreachable until Phase 6, but the enum value exists now), or a specific execution list. Tears down every matching in-flight execution within a bounded time (exact duration an open question below). Shares the same underlying "stop N executions" primitive the scheduler's own overrun-preemption uses internally, and the existing per-execution `Stop`/`Purge`.

### UI

Read-only this phase:

- Report/run history viewer.
- A reservation calendar — what's reserved, when, per tenant/cluster.
- Live status for in-flight executions (the existing Prometheus-backed live metrics, surfaced in a page rather than only Grafana).

React + Tailwind v4 SPA, static assets served by `cmd/api`, styled after heridotlife's admin dashboard design language (component patterns, palette, light/dark support) — a fresh build, not a port of heridotlife's code.

## Acceptance criteria

- A service owner can create a one-shot schedule (fire at time T) or a recurring schedule (recurrence rule); both go through the same reservation check at creation time.
- A manual `Trigger` call is quota-checked the same way a scheduled occurrence is — same reservation mechanism, `start=now`.
- Creating a schedule whose reservation(s) don't fit is rejected with a stated reason; for a recurring schedule, occurrences that fit are reserved and occurrences that don't are marked rejected up front, both visible to the owner (partial success, not all-or-nothing).
- A recurring schedule's reservation horizon is always at least 7 days out while the schedule is active, maintained by a background job whose last-successful-run is observable.
- `Stop`ping a run releases its remaining reserved window immediately.
- A run that continues past its declared duration keeps running if capacity allows; if the same tenant's own new reservation needs that capacity, the overrunning run is force-stopped to free it.
- The scheduler runs as its own deployment (`cmd/scheduler`), independent of `cmd/api`; more than one replica can run concurrently without double-firing the same due occurrence.
- An operator can invoke a kill-switch scoped to a tenant, a cluster, a campaign, or a specific execution list, tearing down every matching in-flight execution within a bounded time.
- A read-only UI (React + Tailwind v4 SPA, served as static assets by `cmd/api`, styled after heridotlife's admin dashboard language) shows: report/run history, a reservation calendar per tenant/cluster, and live status for in-flight executions.
- Every new port (reservation store, schedule store) ships an in-memory fake plus a conformance suite real adapters also pass; `go test ./...`, `golangci-lint`, and `scripts/coverage.sh` at ≥90% stay green throughout.

## Open questions

- **How is a tenant's quota ceiling actually configured?** Not discussed yet — likely a new field on the existing `tenant` domain, settable via the existing admin surface, but worth confirming at write-plan rather than assuming.
- **Recurrence expression format.** Cron syntax is the obvious default (industry-standard, well-covered by existing Go libraries) but not explicitly confirmed; flagging rather than silently deciding.
- **Kill-switch's bounded time.** The parent spec says "within a bounded time" but doesn't say how long; likely inherits whatever grace period `TerminationGracePeriodSeconds` already uses, but worth an explicit decision at write-plan.

## Note on scope of this brainstorm

This spec covers **Phase 5 only**, in the depth the user asked for before starting it. Phases 6-9 remain phase-level outlines in the parent `tasks.md`, per that document's own stated approach ("Phases 5–9 are phase-level goals to be decomposed when reached") — except that the UI technology/deployment decision above (React + Tailwind v4 SPA, static assets via `cmd/api`, heridotlife-derived styling) is a durable, plan-level decision that applies whenever each later phase adds its own UI surfaces, not something to re-decide per phase.
