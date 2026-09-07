# Phase 25 — task batches

## Block A — backend (pi batch 1)
T1: quotaapp ClusterCapacity read method — used/ceiling summed across tenants for one cluster. Read ports.ReservationRepository + sqlclient implementation FIRST; if a single-query approach fits the existing repo pattern add ONE new repo method (ListClusterQuotas / ClusterUsage); else iterate tenants from GetCeiling-shaped query. Tests: multi-tenant sum, overrun counted, empty cluster = 0/0.
T2: clusterapp enrichment — optional quota dep injected in NewService (nil-safe), List returns clusters + capacity. Handlers: engines_used/engines_ceiling omitempty on clusterResponse. Route/OpenAPI unchanged path shape. Tests: nil dep = fields absent (byte-compat), with dep = numbers present, correct math.

## Block B — frontend (pi batch 1)
T3: api/clusters.ts types + Clusters.clusterCapacity flip — return {used, ceiling} from new fields, {} fallback. Flip the pinned honesty test to the new contract (absent fields still {}). CapacityMeter unchanged (already handles numbers). Tests.
T4: Clusters page — verify meter renders numbers end-to-end vs local stack seeded with quota rows (stack recipe: bash /tmp/p23_stack.sh; seed quota via API or fake-repo test harness — read how existing quota tests seed). Layout-check: capacity meter with numbers assertion for alice (honest skip if stack lacks rows).

## Block C — close (pi batch 2)
T5: usage-summary consistency read — if /api/usage/summary duplicates cluster aggregation, extract/reuse shared helper; else document why not (one paragraph in PROGRESS.md). No behavior change.
T6: full gates + PROGRESS + close commit. DO NOT PUSH.

## Operator (Ryo)
Branch, verify, push/PR/merge/deploy, tags phase25.
