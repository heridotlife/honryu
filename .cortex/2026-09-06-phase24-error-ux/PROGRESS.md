# Phase 24 — PROGRESS (batch 1: tasks 1–6)

Branch `feat/phase24-error-ux`. One commit per task; every commit landed with
its tests green. Nothing pushed or merged.

## T1 — typed `OverQuotaError` (commit faa2a5e)

`internal/app/quotaapp/service.go`: both `fmt.Errorf` rejection sites replaced
with `&OverQuotaError{TenantID, Cluster, Requested, Used, Ceiling,
NoQuotaConfigured}`. `Error()` reproduces the old message **byte-identically**
(both branches, including the ceiling-0 "set one via
PUT /api/tenants/{tenant_id}/quota" sentence — `fmt.Sprintf("%s: …", ErrOverQuota,
…)`); `Unwrap()` returns the sentinel, so `errors.Is(err, ErrOverQuota)` still
matches. Evidence (`internal/app/quotaapp/service_test.go`):

- `TestReserve_OverQuotaErrorCarriesNumbers` — normal branch: numbers via
  `errors.As`, exact message pinned, `NoQuotaConfigured=false`.
- `TestReserve_ZeroCeilingErrorIsFlaggedUnconfigured` — ceiling-0 branch:
  numbers + `NoQuotaConfigured=true`, exact message with the PUT hint pinned.
- `TestOverQuotaError_SentinelAndTypeSurviveWrapping` — through
  `fmt.Errorf("trigger: %w", err)`: `errors.Is` sentinel + `errors.As` type
  both survive.

## T2 — details envelope (commit 7139ade)

`internal/adapters/httpapi/response.go`: `writeErrorDetails(w, status, message,
details any)` writes `{"message":…, "details":…}`; `writeError` delegates with
nil, and nil **omits the key** — message-only bodies stay byte-identical
(`map[string]string` encoding preserved). `errors.go` ErrOverQuota branch:
`errors.As` → details `{tenant_id, cluster, requested, used, ceiling, hint}`;
hint = `PUT /api/tenants/{tenant_id}/quota ceiling=<N>`, appending ";
no quota row exists for this tenant+cluster" when `NoQuotaConfigured`. Message
stays the fixed `"reservation would exceed tenant quota"`; a bare sentinel
falls back to message-only. Evidence:

- `internal/adapters/httpapi/tenant_quota_test.go`
  `TestTenantQuota_TriggerOverQuotaCarriesDetails` — full wiring (deploy →
  over-quota trigger): 429 body parsed as the envelope, tenant/ceiling/used
  from the ledger, requested over ceiling, hint names the PUT + ceiling.
- `internal/adapters/httpapi/errors_details_internal_test.go`
  `TestRespondError_OverQuotaDetails` (both hint variants + a re-wrapped
  error), `TestRespondError_OverQuotaSentinelWithoutNumbersIsMessageOnly`,
  `TestWriteErrorDetails_NilOmitsTheKey`.
- Byte-identity regression on an existing 404 shape:
  `report_handlers_test.go` `TestReportHTTP_UnknownRunIs404` now pins the body
  to `{"message":"ports: not found"}` exactly.

## T3 — `EnginesFinishedError` details (commit 9155c1e)

`internal/app/lifecycleapp/service.go`: `type EnginesFinishedError struct{
Orphaned int}` with `Error()` byte-identical to the old wrap
("run: engines already finished, redeploy before triggering: N orphaned shard
completion(s)") and `Unwrap()` → `run.ErrEnginesFinished`. `errors.go`: a
dedicated `errors.As` case ahead of the generic conflict branch emits 409 with
details `{orphaned_completions, hint: "purge the execution and redeploy before
triggering"}` on top of the verbatim message; bare sentinel stays
message-only 409. Evidence:

- `internal/app/lifecycleapp/service_test.go`
  `TestTrigger_EnginesFinishedErrorCarriesOrphanCount` — As + count + exact
  text, and Is/As through a re-wrap.
- `internal/adapters/httpapi/errors_details_internal_test.go`
  `TestRespondError_EnginesFinishedDetails` (409 body shape, wrapped),
  `TestRespondError_EnginesFinishedSentinelIsMessageOnly`.

## T4 — web `errorDetails` helper (commits 14facf7, 30fb143)

`web/src/api/client.ts`: `errorDetails(err): Record<string, unknown> | null` —
reads `ApiError.data.details` when it is an object, else null; walks the
error's `cause` chain (≤5 hops) so wrappers can't hide the envelope.
Evidence (`client.test.ts`): 429 payload end-to-end through `ApiClient.post`
→ details present; message-only → null; no-data / non-ApiError / non-object
details → null; envelope reached through a `stepError`-style cause wrap; cause
chain without an ApiError → null.

## T5 — Execution.tsx surfacing (commit 01c145f)

`runAction`'s catch now keeps the error object (`setActionDetails(errorDetails(err))`
next to the message state) and the render adds the shared
`web/src/components/ActionErrorDetails.tsx` under the existing
`<p role="alert">`: hint as a `<code>` chip (copyable PUT remediation) and,
when `ceiling` is present, "used X / ceiling Y — requested Z" (missing numbers
tolerate as `?`). Plain errors unchanged. Evidence:
`ActionErrorDetails.test.tsx` (5 cases incl. null and junk-typed fields) +
`Execution.live.test.tsx` mounted: 429 renders hint+numbers, 409 renders hint,
message-only 404 keeps the single line with no details node.

## T6 — NewTest surfacing (commit 30fb143)

Same treatment at the create-flow failure point. `stepError` now attaches the
original error as `cause` (ES2022) — previously it flattened the ApiError,
which is exactly the "capture the ApiError, not just message" trap — and
`errorDetails` walks causes (T4). Evidence: `NewTest.test.tsx` mounted — a
step failing with the envelope renders the hint `<code>` + numbers line and
stops (no navigation, no config PUT); message-only 409 renders the single
alert line with no details node.

## Gates (run after T6, all green)

- `go test -race -count=1 ./internal/...` — ok (whole tree; gofmt clean,
  `go vet ./...` clean).
- `cd web && bun run vitest run` — 348 passed.
- `cd web && bun run tsc -b` — clean.

## Notes / deviations

- None of substance. T6 required the `stepError` cause fix (and the matching
  `errorDetails` cause-walk) to satisfy "capture the ApiError, not just the
  message" — folded into the T6 commit with its tests.
- `web/dist/.gitkeep` shows deleted locally (real build artifact rule) —
  intentionally not committed.

## Remaining (batch 2)

- T7: trigger-empty-reply repro attempt (timeboxed), T8: layout-check
  assertion + phase close.

---

# Batch 2 (tasks 7-8, pi)

## T7 — trigger-empty-reply: REPRODUCED, root-caused, fixed (commit f533d71)

### Reproduction (exact recipe and observations)

Stack: `bash /tmp/p23_stack.sh` (demo auth, fake DB + fake scheduler,
defaults otherwise). Seeded over the public API as alice: tenant + quota 8,
tenant-1 project, **portable scenario with NO requests and NO script file**
(the deploy-rejecting shape — the jmx/native path compiles fine and deploys
200), execution + config.

- `POST /api/executions/1/deploy` → **400**
  `{"message":"compile: portable scenario needs requests: scenario \"noreq\""}`
  (0.9ms).
- `GET /api/executions/1/status` → `{"phase":"idle","pool_size":0,...}`.
- `POST /api/executions/1/trigger` → **hangs 119.995s, then EMPTY REPLY:
  curl exit 52, zero response bytes, no `< HTTP` line at all** — and
  `/tmp/p23local.log` shows no panic, no trace, no audit line at that
  moment. Exactly the phase-23 finding.

### Root cause (confirmed by two discriminating probes)

The handler's poll loop and the server's write deadline race:

1. `Trigger` on a never-deployed execution returns `run.ErrNotDeployed`
   (PoolSize 0 → PhaseIdle → `CanTrigger`) — a readiness-class error, so
   `triggerExecution` polls until `TriggerReadyTimeout` (default **2m**).
2. `http.Server.WriteTimeout` (default **15s**, `config.go` defaults) sets
   the connection's write deadline the moment the request header is read.
   A TCP deadline does not close anything by itself — it passes silently
   at t=15s while the handler keeps polling.
3. At t≈120s the loop exits and `respondError` → `WriteHeader` → the
   underlying write fails (`i/o timeout`), which net/http swallows; the
   connection closes with **zero bytes written**. Client: empty reply,
   exit 52. Server: nothing logged (no panic ever happened; the failed
   write is discarded). "No audit line" is unrelated pre-existing
   behavior: audit is only wired for tenant handlers.

Probes:
- **B** `HONRYU_HTTP_TRIGGER_READY_TIMEOUT=5s` (5s < 15s WriteTimeout) →
  trigger answers in 5.0005s: **409 `{"message":"run: engines are not
  deployed"}`**. Handler logic itself is correct.
- **A** `HONRYU_HTTP_WRITE_TIMEOUT=300s` (default 120s wait) → trigger
  answers at **119.995s with the 409 body**. Isolates WriteTimeout as the
  sole killer: any wait longer than WriteTimeout makes the final answer
  unwritable.

Why the fix is NOT "stop polling ErrNotDeployed": the k8s scheduler's
`ExecutionStatus` counts only *ready* pods (`k8s.go`: "ExecutionStatus
counts ready pods per scenario"), so between `deploy 200` and pods Ready,
`PoolSize==0` → ErrNotDeployed **is** the post-deploy race the wait exists
for (task 121). Removing that retry would regress it.

### Fix + regression test (TDD red→green)

- `lifecycle_handlers.go`: before the poll loop,
  `_ = http.NewResponseController(w).SetWriteDeadline(deadline.Add(poll))`
  — push the connection's write deadline out to cover the whole bounded
  wait plus one poll beat of grace (the per-response deadline control long
  handlers are meant to use; best-effort, falls back to server config
  without deadline control).
- `TestLifecycleHTTP_TriggerWaitOutlivesServerWriteTimeout`: a REAL
  `httptest` server (recorders cannot carry deadlines) with
  `WriteTimeout=10ms` and a 50ms readiness wait. Pre-fix: fails with
  client `EOF` — the in-test empty reply. Post-fix: 409 +
  `run: engines are not deployed`.
- **Live verification** (fixed binary, all-default config, original repro
  recipe): trigger now returns **409 + body at 119.995s**, curl exit 0,
  no panics. Reproduction confirmed gone.

Operational footnotes found on the way (out of scope, documented):
- `/tmp/p23_stack.sh`'s `fuser -k 8080/tcp` is a silent no-op here (`fuser`
  not installed) — stale `api` binaries keep the port; kill the listener
  pid from `ss -tlnp` (phase-23 finding #2 reconfirmed).
- The stack script's `HONRYU_INGEST_TOKEN=localtest` mismatched the seed
  script's `dev-engine-token` (every ingest batch 401'd, empty runs);
  scratch script corrected before the layout-check run.
- TaurusEditor on the Execution page 409s (`scenario is not portable`) for
  native scenarios — pre-existing, handled as the editor's own error
  state; surfaced only because the local stack seeds a native scenario.

## T8 — layout-check assertion + phase close

- New assertion (3 viewports, alice): on `/executions/{newest}` the
  action-error alert block must appear when a lifecycle action fails. The
  failure is manufactured **entirely client-side** — the lifecycle POST
  (`deploy|trigger|stop|purge`) is intercepted via `page.route` and
  answered with a stubbed 409 before it can leave the browser, keeping the
  script read-only against any target (it never mutates a deployment, not
  even with a request that would fail). Whichever lifecycle button the
  phase leaves enabled serves as the click. Waits specifically for the
  alert carrying the stub text (the page co-hosts TaurusEditor's own
  role=alert from load — waiting for "an alert" races the round trip;
  that race bit twice during development and is why the wait is
  text-targeted).
- `web/dist/.gitkeep` deletion left uncommitted (local build artifact,
  per AGENTS.md).

## Final gate table (batch 2 close)

| Gate | Command | Result |
|------|---------|--------|
| gofmt | `gofmt -l .` | 0 files |
| go vet | `go vet ./...` | pass |
| Unit (race) | `make test` | 56 pkgs ok, 0 fail |
| Coverage (Docker) | `make cover-gate` | **91.8% ≥ 90% PASS** |
| Lint | `make lint` (golangci v2.12.2 pin) | 0 issues |
| Web unit | `bun run vitest run` | 348/348 |
| Web types | `bun run tsc -b` | pass |
| Web build | `bun run build` | pass |
| layout-check | `LAYOUT_CHECK_URL=http://localhost:8080 bun run layout-check` | **425 ok / 0 fail, exit 0** |

## Per-task status

- T1 ✅ (batch 1) · T2 ✅ (batch 1) · T3 ✅ (batch 1) · T4 ✅ (batch 1) ·
  T5 ✅ (batch 1) · T6 ✅ (batch 1)
- T7 ✅ REPRODUCED → root-caused (WriteTimeout vs readiness-wait race) →
  fixed (`http.ResponseController` deadline extension) → regression test →
  live repro confirmed gone (commit f533d71).
- T8 ✅ layout-check assertion added; all gates green; this close.

## Deviations

- T7 expanded past investigation into fix+test — explicitly allowed by the
  task ("if reproduced... this task may take the rest of the batch").
- Coverage gate needs >600s wall (first run hit the project's `timeout
  600` guidance and was terminated mid-e2e; rerun with a longer ceiling
  passed cleanly at 91.8%).
