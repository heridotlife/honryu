# Phase 22 Progress — Tier 2 UI

Batch 1 (tasks 1-4, live run chart) executed by pi on 2026-09-05, branch
`feat/phase22-tier2-ui`. One commit per task; `vitest run` + `tsc -b`
green before every commit. Not pushed.

## Task 1 — liveSeries reducer ✅

- Commit `69662b0` — `web/src/lib/liveSeries.ts` + `liveSeries.test.ts` (10 tests).
- Output type named `LiveSeriesPoint` (task text said `SeriesPoint`, which
  is already taken by `api/series.ts` with a different shape — documented
  rename, no collision).
- **Deviation (per the task's own "inspect" instruction):** error rule is
  `status !== '200'`, not "success"/"ok". Ground truth: the producer
  (`internal/app/metricsapp/ingest.go` `record()`) emits exactly `"200"` /
  `"500"` HTTP-code strings, and the existing `summarize()` in
  Execution.tsx already used `!== '200'`. "success"/"ok" never appear on
  the wire.
- Percentiles: **nearest-rank** (`ceil(p/100·N)`, clamped) — chosen over
  linear interpolation because per-second buckets are small and
  nearest-rank never reports a latency no event measured. Documented in
  the module.
- `t` is seconds since the **earliest** `receivedAt` (out-of-order events
  are min-anchored so bucket indices stay non-negative); gaps stay absent;
  last 60 buckets kept without re-basing `t`.
- `rps` = event count per bucket (each event is one measured interval per
  label/shard — same proxy the trailing samples/sec stat always used).
- Hostile fixtures all covered: empty, single, out-of-order, mixed labels
  (aggregated into shared buckets), 61 buckets, exactly 60.
- First vitest run had 2 fixture bugs (my test expectations ignored that
  `t` is anchored on the earliest event); reducer was correct, fixtures
  fixed before commit.

## Task 2 — useLiveSeries hook extraction ✅

- Commit `392ef6e` — `web/src/hooks/useLiveSeries.ts`, plus
  `streamExecutionMetrics` gained optional `{onOpen, onError}` handlers
  (backward compatible; LiveStatus.tsx untouched).
- `summarize`/`LiveStats`/`windowMs`/`ReceivedMetric` moved verbatim into
  `lib/liveSeries.ts`; Execution.tsx's bottom re-exports dropped (grep
  confirmed zero external importers — LiveStatus.tsx has its own copy).
- **Documented signature additions** beyond `{series, connected,
  lastEventAt}`:
  - `stats` — the trailing-10s rolling numbers, so the three existing stat
    cards keep their exact prior values (the task's "keep summarize for
    the numbers" option; the numbers must live with the events, which
    moved into the hook).
  - `reset()` — preserves the inline code's instant purge reset
    (runAction purge now calls it).
- Raw events retained 61s (stats still computed over trailing 10s only),
  so the chart's 60-bucket window can fill without changing any displayed
  number. Existing Execution.test.ts untouched and green.

## Task 3 — Execution page live chart ✅

- Commit `2dd1fb0` — "Live" `<h3>` section rendered when
  `phase === 'running' || series.length > 0`.
- Two-series chart (VUs sky / RPS amber, Reports.tsx pattern) + latency
  chart with p50/p95/p99 pill group (default p95, same `pctPill` styling),
  idle ("Waiting for first events…" when connected && empty) and
  disconnected ("Stream disconnected — reconnecting…" when `!connected`)
  states. Charts stay visible under the disconnected banner when a stream
  drops mid-run (data already received is still informative).
- TimeSeriesChart reused as-is — no new chart code paths.
- **Axis honesty:** x is `t` (run-relative seconds) on a numeric axis, NOT
  `xType="time"` (that would render run-relative offsets as 1970 local
  wall-clock). Latencies charted as ms (wire is seconds, Reports'
  convention).
- layout-check deliberately untouched (task 10 collates).

## Task 4 — Block A tests + gates ✅

- Commit `c2800e2`:
  - `useLiveSeries.test.tsx` (6 tests): fake-EventSource probe — subscribe
    when enabled / silence when disabled, open/error connection tracking
    (incl. reconnect), series+stats accumulation, close-on-unmount,
    reset() without stream teardown.
  - `Execution.live.test.tsx` (4 mounted tests): Live heading + idle text
    when connected+empty, disconnected banner before first open, VUs/RPS +
    p95 charts from streamed events, percentile toggle switches the
    series, charts persist under the banner, section hidden when idle.
- **Deviation:** new `Execution.live.test.tsx` instead of extending
  `Execution.test.ts` — the mounted tests need JSX (.tsx) and the existing
  file had to stay unchanged; its 5 pure suites pass untouched.
- Gates: `bun run vitest run` → **276/276 across 40 files**;
  `bun run tsc -b` → clean. Run before every commit, not just the last.

## Evidence (batch 1)

```
c2800e2 feat(phase22): tests for the live series hook and live chart section
2dd1fb0 feat(phase22): live run chart section on the Execution page
392ef6e feat(phase22): extract useLiveSeries hook from Execution page
69662b0 feat(phase22): pure liveSeries reducer for the live run chart
```

Not yet done (per plan): live-run eyeball against a real stream (needs
task 0's cluster + execution), layout-check assertions (task 10).

---

Batch 2 (tasks 5-7, visual stage editor) executed by pi on 2026-09-05,
same branch, same rules. TDD throughout: red run before each implementation.

## Task 5 — stages round-trip pair ✅

- Commit `a65262a` — `web/src/lib/stagesConfig.ts` + `stagesConfig.test.ts`
  (11 tests at commit; 20 after task 6's validation helpers).
- Key order mirrors Go's `loadprofile` json marshal order (throughput
  between engines and duration, csv_split last) via conditional spreads,
  so the **single-row case is byte-equal to `buildConfig`** — tested
  explicitly with `JSON.stringify` comparison across three form fixtures.
- Omission rules: throughput 0/undefined → key omitted (unlimited,
  `Throughput` omitempty intent); csvSplit false → omitted. **Honest
  wrinkle:** Go's `Entry.CSVSplit` tag has NO omitempty, so the backend
  may echo `"csv_split": false` — `configToStages` accepts both forms,
  we just never write `false` (buildConfig never did either).
- Round-trip property test without a new dep: seeded LCG generates 200
  valid row sets (throughput domain: positive-or-omitted; csvSplit:
  true-or-omitted — 0/false ARE the omitted defaults),
  `configToStages(stagesToConfig(rows))` toEqual rows (undefined-key
  equivalence = "modulo omitted defaults").
- Process deviation, noted honestly: task-5 commit was made after running
  only the new test file; the FULL suite (287/287) was verified
  immediately after, before any further work.

## Task 6 — StageEditor component ✅

- Commit `d06041a` — `StageEditor.tsx` + `StageEditor.test.tsx` (11
  mounted tests) + validation helpers in `stagesConfig.ts`.
- **Judgment call (documented, per task's "your call"):** single state
  object `StageEditorState {mode, rows, rawJson}` with one
  `onStateChange` — the host owns it like any form field; `rawJson`
  persists inside the state across switches so nothing is ever lost.
- Validation = `validateStageRow` (pure, unit-tested), mirroring
  `Entry.Validate` semantics + the task's message copy; collects all
  fields (inline display) instead of Go's first-error switch. Also mirrors
  `ErrThroughputInvalid` ("throughput cannot be negative") — not in the
  task's four-line list but part of Validate's semantics.
- **Validity split (documented):** table mode counts only the editable
  fields (`editableRowsValid`) because the scenario id has no input there
  — the host flow assigns it at submit (NewTest creates the scenario
  first; rows legitimately carry scenarioId 0). Raw mode owns the full
  check incl. scenario (`stageRowsValid`), surfaced as `stage N:` error
  lines under the textarea. `onValidityChange` fires from an effect keyed
  on the computed boolean.
- Raw toggle: table→raw serializes via `stagesToConfig` pretty 2-space;
  raw→table parses + shape-checks (`tests` array); **a parse error keeps
  raw mode and never replaces `rows`** — tested explicitly
  (garbage → error shown → switch refused → repair → switch succeeds,
  rows untouched throughout).
- jsdom lesson: React's value tracker swallows plain `.value` writes, so
  tests drive inputs via the prototype value setter — same as
  Reports.test.tsx's house pattern.
- Post-commit hardening `f852b9e` (batch-close self-review): a `tests`
  array of primitives (e.g. `{"tests":[5]}`) reported VALID — undefined
  fields make every `<= 0` comparison false — so `parseRawConfig` now
  shape-checks each entry is a non-null object. Regression test included.

## Task 7 — NewTest step-5 integration ✅

- Commit `414f8ee` — NewTest.tsx + NewTest.test.tsx (1 existing gating
  test + 3 new mounted flow tests).
- **Deviation (the headline one):** there was no step-5 "JSON textarea"
  to replace — the spec's "current state" prose was stale: R9's NewTest
  builds the config invisibly from its numeric grid (`buildConfig`). The
  numeric grid WAS the step-5 input surface, so it is what got replaced:
  the Load stages card (StageEditor) is now the load-shaping input,
  seeded once from the form's initial values. `NewTestForm` keeps its
  shape (buildFragment, seed, type stability); `buildConfig` stays
  exported and tested as the byte-equality reference.
- **Pre-fill deviation:** the task says to seed "concurrency/rampup/
  throughput/duration" from the form — `NewTestForm` has no throughput
  field, so the seeded stage is throughput-unlimited (key omitted),
  exactly matching `buildConfig` output which never wrote the key.
- Submit path: table mode maps rows through `stagesToConfig` with
  flow-assigned `name: form.name` + `scenarioId` → **byte-identical PUT
  body to the pre-editor flow** (mounted test pins it against
  `buildConfig` output with patched ids). Raw mode submits the JSON as
  typed, patching wrapper ids always, scenario id + test name only when
  left placeholder-empty (explicit values honored). Create button also
  disabled while the editor reports invalid.
- R9 clamp warning now computed per stage row (same pure
  `concurrencyEnginesWarning`), table mode only — raw mode's config is
  the operator's verbatim; backend Validate is the authority there.
- Requests-fragment step untouched (TaurusEditor was never mounted in
  NewTest; still isn't).

## Evidence (batch 2)

```
f852b9e fix(phase22): reject non-object tests entries in the raw JSON editor
414f8ee feat(phase22): NewTest step 5 uses the visual stage editor
d06041a feat(phase22): StageEditor component with table/raw toggle
a65262a feat(phase22): pure stages round-trip pair for the config JSON
```

Gates at close of batch 2: `bun run vitest run` → **311/311 across 42
files**; `tsc -b` clean. Not pushed. Remaining: tasks 0 (operator), 8-11.

## Batch 3 (tasks 8-11, capacity meters + close) — 2026-09-05

### Task 8 — CapacityMeter + honest-scope check ✅

- Commit `e6a181b` — `web/src/components/CapacityMeter.tsx` + 11 tests
  (house createRoot+act pattern).
- **Honest-scope check executed, fallback taken.** `GET /api/clusters`
  (`cluster_handlers.go` `toClusterResponse`) exposes registration fields
  only — name, api_url, ingest_url, sidecar_image, namespace, secret_ref,
  origin, created_by, created_time. **No engine-count/capacity fields.**
  The phase-7 calibration surface (`GET /api/scenarios/{id}/capacity-profile`)
  is per-scenario per-(engine,cpu,memory) throughput (`per_pod_qps`) with
  **no cluster association** — nothing cluster-level exists anywhere. So:
  - `api/clusters.ts` was NOT extended (tasks.md: extend only if backend
    fields exist — they don't).
  - `CapacityMeter` takes `{label, used?, ceiling?}`; absent numbers
    render the honest "no capacity reported" line. Bar = plain SVG
    (Sparkline house style, currentColor), fill = used/ceiling capped at
    the track, `>=100%` turns `text-red-600 dark:text-red-400`. Pure
    `capacityFraction` helper rejects absent/non-finite inputs and
    non-positive ceilings.
  - **Phase-23 backend candidate:** engine-count/capacity fields on
    `GET /api/clusters` (e.g. engines in use + ceiling per cluster) would
    light every meter up; `Clusters.clusterCapacity` is the single
    mapping point.

### Task 9 — Clusters page meters ✅

- Commit `523ad53` — Capacity column (after Origin) with
  `<CapacityMeter label="engines" {...clusterCapacity(c)} />` per row;
  all prior columns kept.
- `clusterCapacity(c: Cluster)` exported and **tested to return `{}`**
  for a full registered cluster — the test pins the honesty (no invented
  data) and is the one to flip when the backend grows fields.

### Task 10 — layout-check extensions ✅ (deviation: local stack, not prod)

- Commit `f288c7c` — three new assertion families in the per-persona loop:
  - **Execution `/executions/:id`**: discovery now also returns the newest
    execution + its phase; while `phase === 'running'` the run asserts
    the live section, its "Live" h3, and charts-or-idle-placeholder
    (one comma-selector waits for either); non-running → honest skip.
    All four personas (all hold execution:read).
  - **NewTest `/executions/new`**: stage-table keeps its Concurrency and
    Duration columns — the editor renders for every persona (only the
    create button is gated), so asserted for all four.
  - **Clusters `/clusters`**: Capacity column header — alice only (the
    sole persona whose map grants /clusters, via `hrefs.includes`);
    skips honestly when no table renders (empty registry, or registry
    endpoints not deployed — they require the k8s scheduler).
- `settleAt` gained a `waitUntil` option: the Execution page holds an
  open metric stream (SSE) for any valid id, so `networkidle` never
  settles there — that route settles on `domcontentloaded`.
- Console-noise handling: Chromium logs every 4xx fetch as a console
  error, and the Execution page probes optional endpoints **by design**
  (capacity-profile 404 is the documented "nothing calibrated" contract;
  personas without scenario grants see 403s). The two new blocks filter
  `Failed to load resource` notices; uncaught throws (pageerror) and any
  other console error still fail them. Compare/reports checks unchanged
  and strict.
- **Deviation from the task text:** run against
  `http://localhost:8080`, not `https://honryu.pve.heri.life` — prod
  still serves **phase20** tags (chart pinned in `84485dd`), so the
  phase-22 SPA cannot pass there; the phase-21 close hit the same wall
  and used the same stand-in. Local stack: `cmd/api` demo mode (RBAC +
  the four homelab personas, fake DB + fake scheduler, ingest token
  set), embedding the fresh phase-22 `web/dist`, seeded out-of-band over
  the public API exactly like phase 21's chore: tenants 1+2 (quota
  raised from the default **0** — otherwise every trigger 429s with
  "reservation would exceed tenant quota"), tenant-1 project, execution
  A with two finalised runs (2 labels each), execution B left RUNNING
  with a live ingest loop. **378 ok / 0 fail, exit 0**, live-section
  assertions firing for real on a running execution, 4 personas × 3
  viewports.
- **Prod observations (out of scope, for the operator):** pointed at prod
  today, the new checks fail only because the SPA there is phase 20;
  additionally the compare pages log 403s for bob/carol/dave (data drift
  with task-0's executions) and dave's execution-detail fetches 403
  (campaign_manager list-vs-detail tenant scope on prod's cross-tenant
  data). Pre-existing, phase-20-SPA behavior; re-check after deploy.

### Task 11 — phase close ✅ (not pushed, per instruction)

| Gate | Command | Result |
|------|---------|--------|
| gofmt | `gofmt -l .` | 0 files |
| go vet | `go vet ./...` | pass |
| Unit (race) | `make test` | 56 pkgs ok, 0 fail |
| Coverage (Docker) | `./scripts/coverage.sh` | **91.8% ≥ 90% PASS** |
| Lint | `make lint` (golangci v2.12.2 pin) | 0 issues |
| Web unit | `bun run vitest run` | 323/323, 43 files |
| Web types | `tsc -b` | pass |
| Web build | `bun run build` | pass |
| layout-check | `LAYOUT_CHECK_URL=http://localhost:8080 bun run layout-check` | 378 ok / 0 fail, exit 0 |

- `web/dist/.gitkeep` deletion left uncommitted (local build artifact,
  per AGENTS.md).
- tasks.md's "push + PR to develop" step deliberately not run (operator
  instruction: stop after the close commit). Task 0 (operator bootstrap)
  remains the operator's checkbox; its prod data (execution 7
  "p22-data-1", observed running 2026-09-05) is on the target.

### Findings surfaced, not fixed (out of plan scope)

1. **Latent R9 bug — `buildFragment` emits a fragment the server
   rejects.** `web/src/lib/newTestFlow.ts` wraps the YAML in a
   `scenarios:` map, but G3 (`PUT /scenarios/{id}/requests`) unmarshals a
   bare `taurus.Scenario` (root-level `default-address:`/`requests:`) —
   verified live: bare shape → 200, wrapped shape → 400 "at least one
   request is required". Phase 19's e2e stores the bare shape; the SPA's
   NewTest step 4 has therefore never worked against a real backend (its
   tests mock the API, and `newTestFlow.test.ts` even pins the wrapper).
   Recommend a phase-23 fix: emit the bare fragment + flip the pinned
   test.
2. New tenants default to a **0-engine quota** (every trigger 429s until
   an admin raises it). Surprised the seed; fine by design, but the
   NewTest UX around it (generic 429) could carry clearer copy someday.

## Evidence (batch 3)

```
e6a181b feat(phase22): CapacityMeter with honest no-capacity state
523ad53 feat(phase22): cluster rows carry a capacity meter column
f288c7c test(phase22): layout-check covers live section, stage editor, capacity column
<close commit> chore(phase22): phase close — gates, PROGRESS
```

## Followups — surfaced findings fixed, 2026-09-05

Branch `feat/phase22-fixes` (cut from develop by Ryo), one commit per fix.

### Finding 1 — buildFragment emitted YAML the server rejects ✅

`b5bf910 fix(phase22): buildFragment emits bare taurus.Scenario the G3
endpoint accepts`

- `web/src/lib/newTestFlow.ts` `buildFragment()` no longer wraps the
  fragment in a `scenarios:` map + indented scenario-name line; output is
  the flat root-level shape (`default-address:` / `headers:` /
  `requests:`) that G3 (`SetRequests` → `requestDiagnostics`,
  `yaml.Unmarshal` into a bare `taurus.Scenario`) actually accepts.
  Verified server-side before touching the SPA: the wrapped shape leaves
  `Requests` empty → 400 "at least one request is required".
- `newTestFlow.test.ts` flipped: pins absence of `scenarios:` and the
  name line, `default-address` at root, headers/requests assertions kept.
- No other reference to the wrapped shape: grep across `web/` found only
  the call site (`NewTest.tsx`, shape-agnostic) and the test;
  `taurusDoc.test.ts`'s `scenarios:` fixture is the doc *viewer* parsing
  full stored configs — different concern, untouched.
- Gates: `vitest run` 323/323 (43 files), `tsc -b` clean.

### Finding 2 — quota-0 error lacked remediation ✅

`8b680cc fix(phase22): quota-0 rejection names the remediation`

- Behavior unchanged (0-ceiling default deliberate — 0028 comment,
  tenantapp SetQuota doc). `quotaapp/service.go` `Reserve()`: when
  `ceiling == 0` the `ErrOverQuota` wrap appends "— no quota configured
  for this tenant+cluster; set one via PUT /api/tenants/{tenant_id}/quota"
  (route verified against `httpapi/router.go:229`); `ceiling > 0` keeps
  the existing message verbatim.
- Tests: `TestReserve_ZeroCeilingRejectsEverything` pins the remediation
  text present; `TestReserve_RejectsWhenOverCeiling` pins it absent when
  a real ceiling is exhausted. Both follow the existing `errors.Is` +
  `t.Fatalf` pattern.
- Gates: `go test -race -count=1 ./internal/app/quotaapp/` green,
  `make test` (full unit lane) green, gofmt clean (one intermediate
  `gofmt -w` needed — tab depth on the kept-branch continuation lines),
  `go vet` clean.

### Deviations

- None of substance. Minor process note: the gofmt slip above was caught
  by the pre-commit gate and fixed before the commit landed.
