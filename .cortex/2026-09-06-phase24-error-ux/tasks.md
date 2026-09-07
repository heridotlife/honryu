# Phase 24 — task batches

## Block A — backend structured errors (pi batch 1)
T1: typed ErrOverQuota — quotaapp: `type OverQuotaError struct { TenantID int64; Cluster string; Requested, Used, Ceiling int; NoQuotaConfigured bool }` with Error() + Is(target) so `errors.Is(err, ErrOverQuota)` still matches. Service returns it with real numbers. Unit tests: both branches carry numbers, sentinel match, message text stable.
T2: writeError details — response.go: `writeErrorDetails(w, status, message string, details any)`. errors.go: ErrOverQuota branch now `errors.As` → builds details JSON (tenant_id, cluster, requested, used, ceiling, hint incl. PUT path; hint differs for NoQuotaConfigured). Tests: 429 body has message+details, old consumers (message-only) unaffected, other errors unchanged shape.
T3: ErrEnginesFinished details — lifecycleapp typed error (Orphaned int) + respondError conflict branch populates details {orphaned_completions, hint: purge+redeploy}. Tests both.

## Block B — UI surfacing (pi batch 1)
T4: client.ts — extractErrorMessage keeps message; add helper `errorDetails(err): {hint?: string, [k: string]: unknown} | null` reading ApiError.data.details. Unit tests.
T5: Execution.tsx actionError render — if details present (esp. hint): alert box gains second line `<code>` with hint + numbers line "used X / ceiling Y — requested Z". Plain string fallback unchanged. Tests: 429-with-details renders hint; message-only unchanged.
T6: NewTest flow same treatment at trigger step. Tests.

## Block C — investigation + close (pi batch 2)
T7: trigger-empty-reply repro attempt (timeboxed 30 min, spec C) — local stack, invalid-scenario deploy → trigger; document result either way in PROGRESS.md. If reproduced: diagnose + fix + test (this task may expand).
T8: layout-check: extend one assertion (quota error detail rendering is unit-tested; layout-check adds the alert block presence check on Execution page). Full gates + PROGRESS + close commit.

## Operator (Ryo)
Branch cut, verification, push/PR/merge/deploy, appVersion+tags bump at deploy.
