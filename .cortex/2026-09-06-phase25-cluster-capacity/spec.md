# Phase 25 — Cluster capacity backend: light up the meters
Date: 2026-09-06. Branch: feat/phase25-cluster-capacity off develop bef7835.

## Problem
Phase 22 shipped CapacityMeter UI with honest "no capacity reported" fallbacks because GET /api/clusters has NO capacity fields (registration fields only). Phase-22 PROGRESS.md flagged this as the backend candidate: "engine-count/capacity fields on GET /api/clusters would light every meter up; Clusters.clusterCapacity is the single mapping point."

## Capacity model (from recon)
- Quota ledger: tenant_quota (tenant_id, cluster, ceiling) + reservation table. usedCapacity = overlapping windows + overrunning reservations (quotaapp/service.go:204).
- Engine counts are PER (tenant, cluster). Clusters page needs AGGREGATES per cluster.
- What "used" means per cluster: sum over tenants of usedCapacity(tenant, cluster) at now. Ceiling: sum over tenants of GetCeiling(tenant, cluster). A cluster with no quota rows: ceiling 0, used 0 → meter shows honest "no capacity reported" still (by design — no invented data).

## In scope
### A. Backend
- quotaapp: new read method `ClusterCapacity(ctx, cluster) (used, ceiling int, err error)` — sums across tenants. Reuses the same repo queries pattern (usedCapacity needs a tenant-agnostic variant: repo method or loop over tenants-with-quota-rows — investigate which is cleaner against sqlclient; prefer ONE SQL query joining tenant_quota × reservation if the repo layer allows, else iterate).
- clusterapp Service.List: enrich each cluster with capacity via quotaapp (constructor injection optional dep — nil = no enrichment, keeps tests/fakes simple).
- cluster_handlers toClusterResponse: new optional fields `engines_used`, `engines_ceiling` (omitempty — absent when not enriched; wire format stays backward compatible).
- OpenAPI entries. Tests: enriched + non-enriched (nil dep) + multi-tenant aggregation math (overrun counted).

### B. Frontend
- api/clusters.ts: Cluster type + engines_used?/engines_ceiling?.
- Clusters.clusterCapacity: flip the pinned test — map {used, ceiling} when present, {} when absent (fallback unchanged).
- Layout-check: on alice, cluster row capacity meter shows numbers when prod stack has quota rows (it does: tenants 1+4). Honest note if empty.

### C. Bonus (small): Usage summary parity
- If GET /api/usage/summary already computes VUh per cluster, reuse its aggregation approach for consistency (read it first; don't duplicate logic if a shared helper is trivial).

## Non-goals
- Per-scenario capacity (phase-7 surface stays).
- Historic capacity/time series.
- Reserve-flow changes.

## Evidence
- cluster_handlers.go:42 toClusterResponse (registration fields only).
- quotaapp/service.go:138 Reserve, :204 usedCapacity, :226 overrunReservations, ports/reservation_repository.go:30 GetCeiling.
- Prod: cluster "honryu" registered; tenant_quota rows: tenant 1 ceiling 4, tenant 4 ceiling 8.
- Clusters.clusterCapacity currently returns {} and its test PINS that (the test to flip).
