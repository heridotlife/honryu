# Phase 6 — Campaign, freeze, verdict rollup

## Problem

Honryu can run and report on individual executions (Phase 4) and gate/schedule them against tenant quota (Phase 5), but has no way to answer the question a PM actually asks before a launch: "is the whole platform ready?" That requires coordinating multiple services' tests into one readiness event, guaranteeing nothing else contaminates the result, and rolling many pass/fail verdicts into one go/no-go — none of which exists today.

## Goal

A PM can define a campaign (window + participating services, each bound to one designated readiness execution), have Honryu freeze everything else in scope for the window's duration, and pull one rolled-up verdict afterward — per-service status, overall go/no-go, failing criteria named.

## Non-goals

- Calibration-aware rollups. `CalibrateEngine` exclusion is a forward-compatible label only — that execution kind doesn't exist until Phase 7.
- Full elimination of the shared-dependency contamination risk (parent spec's residual risk on decision #12). Phase 6 ships the minimum mitigation (report annotation of other load active), not cluster-level freezing or declared dependencies.
- A general-purpose bzt criteria-grammar parser. Honryu evaluates a defined practical subset of criteria expressions against report data; anything outside that subset falls back to reporting only the coarse outcome for that service, not a wrong criterion name.
- Multi-cluster campaigns. The parent spec allows for it structurally, but Phase 8 hasn't landed the cluster registry yet — today's "clusters" dimension is the single implicit cluster, same as Phase 5.

## Constraints

- No new `Service` aggregate — a campaign's "participating service" is an existing `Project` (`internal/domain/project`).
- A campaign binds each participating service to exactly one designated `Execution` — the readiness test for that service. No new threshold data on Campaign; the bound execution's own Taurus criteria (`taurus.Reporter.Criteria`) are what gets evaluated.
- Freeze enforcement lives inside `lifecycleapp.Trigger`, the same funnel Phase 5's quota check already uses, so manual triggers and scheduled fires are covered by one check.
- Draining in-flight non-campaign runs is a periodic sweep, not synchronous — bounded lag (one scheduler tick) is an accepted tradeoff, same shape as quota's overrun-reclaim.
- Campaign creation and service registration require a new tenant-scoped RBAC role (`RoleCampaignManager`), not merely project-edit rights on each participating project.
- Kill-switch's existing `ScopeCampaign` stub (`internal/app/adminapp/service.go`) gets real wiring: abort tears down in-scope executions *and* closes the campaign (freeze lifts immediately).

## Approach

- **Domain**: new `campaign` package — `Campaign{ID, Name, TenantID, Window{Start,End}, Services []Service{ProjectID, ExecutionID}, AbortedAt *time.Time}`. "Active" is derived (`now` within window and not aborted), not a stored status enum — matching how `run.DerivePhase` already avoids redundant state.
- **Persistence**: `campaign` + `campaign_service` tables, mirroring Phase 5's `schedule`/`schedule_occurrence` two-table shape.
- **campaignapp**: `Create`, `Get`, `List`, `Abort`, plus the freeze predicate `lifecycleapp.Trigger` calls (a new optional `Freeze` interface on `lifecycleapp.Service`, opt-in like `Quota`, checked *before* the quota check since a categorical block shouldn't need a reservation-capacity read at all).
- **cmd/scheduler**: a third tick loop resolving each active campaign's in-scope non-compliant running executions and stopping them. The same "resolve in-scope executions" function is reused by the kill-switch's `ScopeCampaign` case.
- **Verdict**: a new pure evaluator matches each configured criterion string against the bound execution's own `report.Report` fields (error rate, achieved load, latency percentiles) for a defined practical subset of bzt's criteria syntax; unparseable criteria degrade to "outcome known, criterion text not shown" rather than a wrong label.
- **Campaign report annotation**: "other load active" reuses Phase 5's `ReservationRepository.ReservationsInWindow` and `usageapp` history for the campaign's cluster(s), filtered to exclude the campaign's own participating executions.
- **RBAC**: new `ResourceCampaign`, new tenant-scoped `RoleCampaignManager` role with campaign create/read/update/admin permissions.
- **UI**: a Campaigns page — create (name, window, add service+execution rows) and a detail view (per-service status, overall go/no-go, failing criteria, other-load annotation).

### Alternatives considered

- **Freeze check at the HTTP/scheduler call sites instead of inside `lifecycleapp.Trigger`.** Rejected: two call sites to keep in sync instead of one, for no decoupling benefit `Trigger` doesn't already provide for quota.
- **Synchronous drain at campaign creation only.** Rejected: doesn't handle a future-dated window arriving later while `cmd/scheduler` is already running — would need the periodic sweep anyway, so building only the synchronous path is a partial solution for the same cost.
- **Cluster-level freeze as the contamination mitigation.** Rejected for now: closes the residual risk completely but reintroduces the tenant-wide blast radius decision #12 explicitly rejected ("unrelated teams keep working"). No evidence yet that the cheaper report-annotation mitigation is insufficient.
- **Campaign creation open to any tenant editor.** Rejected: a campaign freezes *other teams'* work, not just the creator's own — the same cross-team authority concern that made kill-switch and quota management admin-gated in Phase 5.

## Acceptance criteria

- A `RoleCampaignManager`-authorized caller (tenant-scoped or global) can create a campaign: name, window, and one or more (Project, Execution) service bindings. A caller without that role is rejected, even if they can edit the individual projects.
- While a campaign's window is open, `lifecycleapp.Trigger` rejects any execution that (a) belongs to one of the campaign's participating projects and (b) is not that service's designated execution — with a stated reason identifying the blocking campaign. Executions outside the campaign's participating projects are unaffected.
- The designated execution for each participating service can still be triggered normally during the window (freeze exempts it).
- An in-flight non-campaign execution within scope at window-open is stopped within one scheduler tick after the window opens.
- After the window closes (naturally or via abort), the campaign's verdict is retrievable: per-service outcome (from that service's designated execution's report), the specific failing criteria named for any failed service (within the supported criteria-expression subset), and one overall go/no-go (go only if every service passed).
- The campaign's report records every other reservation/execution active in its cluster(s) during the window, excluding its own participating executions.
- Aborting a campaign via the kill-switch (`ScopeCampaign`) tears down every currently-deployed in-scope execution and marks the campaign closed; freeze lifts immediately.
- The SPA has a Campaigns page: create a campaign, and view a campaign's rolled-up verdict.
- ≥90% coverage gate holds; `Campaign`/`campaign_service` persistence has a fake + MySQL adapter proven by one shared conformance suite, matching every prior phase's pattern.

## Open questions

None outstanding — all decisions from this brainstorm are reflected above. The parent spec's residual risk (shared-dependency contamination beyond what the report annotation surfaces) remains explicitly deferred, per the "minimum mitigation" decision above.
