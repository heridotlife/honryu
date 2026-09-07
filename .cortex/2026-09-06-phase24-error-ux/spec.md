# Phase 24 — UX: structured error payloads + actionable UI
Date: 2026-09-06. Branch: feat/phase24-error-ux off develop 2b9bcf0.

## Problem
Two UX debt items from phase 22/23 PROGRESS.md:
1. **429 quota error is deliberately flattened.** respondError maps ErrOverQuota to fixed "reservation would exceed tenant quota" — discarding the phase-22 remediation hint (PUT /api/tenants/{id}/quota, ceiling, used numbers). A tenant editor hitting the ceiling gets zero actionable info; the NewTest flow just shows the generic string.
2. **Trigger against failed deploy = empty reply.** curl exit 52, no body, nothing in logs (phase-23 finding #1, never reproduced/root-caused).

## In scope
### A. Structured error envelope (opt-in extra field, backward compatible)
- `writeError` gains optional details: `{"message": "...", "details": {...}}` for select errors. `message` stays the stable contract (existing tests/consumers unaffected).
- ErrOverQuota 429: details = `{tenant_id, cluster, requested, used, ceiling, hint: "PUT /api/tenants/{tenant_id}/quota"}` (ceiling=0 case includes "no quota configured" variant). Requires quotaapp to return structured data — refactor ErrOverQuota to a typed error struct (errors.As) carrying the numbers, keep `errors.Is` sentinel working via Is() method.
- Conflict errors (ErrEnginesFinished 409): details = `{orphaned_completions: N, hint: "purge and redeploy before triggering"}` — same typed-error treatment.
- ApiError already carries `.data` (parsed body) — extend extractErrorMessage to keep details accessible.

### B. UI surfacing
- Execution page actionError block: when ApiError.data has details (429 quota case), render the hint as a second line with a `<code>` remediation command + the numbers (used/ceiling/requested). Keep plain string fallback.
- NewTest flow: same treatment on trigger failure.
- No new endpoints, no error-code enum registry (YAGNI — details struct only where surfaced).

### C. Trigger empty-reply reproduction (investigation, timeboxed)
- Attempt repro: local stack, deploy with invalid scenario (compile error), then trigger. If reproduced: root-cause (likely handler panic with no recover, or HAProxy cutting on server crash) and fix. If NOT reproduced in 30min of effort: document in PROGRESS.md as unresolved, move on. pi must log exactly what was tried.

## Non-goals
- Error-code registry/enum for all errors.
- Changing status codes.
- OIDC error paths.

## Evidence
- errors.go respondError ErrOverQuota branch (fixed message, deliberate).
- quotaapp/service.go:135-143 — ceiling-0 hint text exists in Go error string only.
- lifecycleapp/service.go:449-456 — ErrEnginesFinished wraps orphan count.
- client.ts ApiError.data already parsed.
- Execution.tsx:317 — actionError = err.message only.
