# Phase 19 — Operator UI: the full user flow

Status: agreed, ready to plan
Date: 2026-09-01

## Problem

Honryu can run a load test. A person cannot.

The SPA calls **~13 of the API's ~75 operations** (`api/openapi.yaml` documents 54
paths), and exactly **one** of them writes: `POST /tenants/{id}/campaigns`. The
observability half is real and well built — `web/src/api/status.ts:47` opens an
`EventSource` against the SSE metric stream and `LiveStatus` keeps a 1-second
sliding window over it while polling the lifecycle snapshot every 10s. The
control half does not exist at all.

The evidence is not a coverage percentage, it is what a run costs:

- On 2026-08-19, verifying the engine roll meant hand-crafting a multipart YAML
  upload with `curl` from inside the Grafana pod, because BusyBox `wget` cannot
  PUT and nothing else could reach the endpoint. That is the product's primary
  workflow, performed entirely by hand.
- `web/src/pages/LiveStatus.tsx:77` is `useState('')` — a text field you **paste
  an execution ID into**. An ID obtainable only from a terminal. The UI can
  watch a run it has no way to start, and no way to find.

The two steps a human most wants a form for are file uploads of YAML:
`PUT /api/scenarios/{id}/requests` (a Taurus `scenarios:` fragment) and
`PUT /api/executions/{id}/config` (the load profile). Both multipart, neither
JSON.

## Goal

A single operator can complete the entire journey in a browser, never touching
`curl`:

> *"Load test `https://httpbin.pve.heri.life` with a session cookie at 2000 QPS
> for 5 minutes, tonight."*

## Non-goals

- **No authentication UI.** The deployment runs `HONRYU_AUTH_MODE=none`
  (verified live). `web/src/api/client.ts:30` already reads `honryu_token` from
  localStorage and nothing sets it; that seam stays dormant and gets a comment
  saying so, rather than being finished.
- **No tenancy surface.** No screens for tenants, quota, or role grants. The
  audience is one operator who is implicitly the platform admin.
- **No new campaign or reservation features.** Those pages exist; they are left
  as they are, not extended.
- **No UI for machine-facing endpoints.** `POST /api/ingest`, `/metrics` and
  `/healthz` are engine and infrastructure surfaces and must never get a screen.
  `/metrics` in particular is deliberately 503'd at the ingress
  (`deploy/chart/honryu/templates/ingress.yaml`) and stays that way.
- **Not "100% API coverage."** Coverage is a checklist, not a product. The
  journey below is the scope; an endpoint outside it does not earn a screen.

## Constraints

- **The SPA is embedded in the Go binary.** `web/embed.go` does
  `//go:embed all:dist`, and the built `honryu-api` is 42.5MB with a 340KB dist
  (288KB JS / 36KB CSS). Bundle growth ships in every image, so Monaco (~5MB) is
  rejected in favour of CodeMirror 6 (~250–400KB).
- **Lean dependency stack, deliberately.** `web/package.json` runtime deps are
  `react`, `react-dom`, `react-router-dom`, `lucide-react`. New runtime deps in
  this phase are limited to a YAML library and the editor. No state-management
  or data-fetching library: polling plus the existing SSE already work.
- **The route table and OpenAPI must stay in lockstep.** `router.go` declares the
  `Route` table as the single source of truth and a test asserts
  `api/openapi.yaml` documents exactly those routes. Every added endpoint
  updates both.
- **Coverage gate.** The repo sits below its 90% target (a pre-existing gap, not
  introduced here). New Go code must not widen it.
- **Server-side validation already exists and must not be reimplemented.**
  `compile.Taurus(in Input) (taurus.Config, error)`
  (`internal/domain/compile/compile.go:70`) is the same function the deploy path
  uses.

## Approach

Extend the existing SPA and open the API where the flow demands it. Three
decisions carry the design.

### 1. YAML is canonical; the form is a lens that patches it

The Taurus editor shows a form and the YAML side by side. **The YAML document is
the truth.** Form edits patch the document's AST (via the `yaml` package's
`parseDocument`, which preserves comments and key order) rather than
regenerating it, so comments, key ordering, and any key the form does not model
survive untouched.

**Critical corollary — the backend must store the YAML as text.**
`internal/domain/taurus/taurus.go` models Taurus as typed structs with **no
catch-all field**: no overflow map, no `yaml.Node` passthrough. It covers a good
subset (`default-address`, `headers`, `requests`, `data-sources`, `assert`,
`throughput`, `hold-for`, `ramp-up`) but Taurus is wider — `think-time`,
`variables`, `follow-redirects`, `body-file`, JSR223. If the server unmarshals
into `taurus.Config` and re-marshals for storage, every unmodelled key is
silently deleted, and the "nothing is destroyed" guarantee dies at the API
boundary while the frontend lens appears to work perfectly.

> **Store the YAML as text. Parse it to validate. Never re-emit it as the
> storage format.**

**Verified during planning — this already holds, and applies only here.**
`scenarioapp/service.go:265-276` unmarshals into `taurus.Scenario` purely to
validate and then persists the original `raw`, which
`mysql/scenario_repository.go:157` writes to `scenario_requests.raw`. No storage
change is required.

The execution config is a different animal and is **not** YAML-canonical:
`executionapp/service.go:201-213` rebuilds the wrapper from `LoadProfileFor()`
and `CriteriaFor()`, so the config is relational rows, not a document. It is
fully modelled by `loadprofile.Entry` (`loadprofile.go:25-37`, eight scalars),
so there are no unknown keys to preserve and no text to protect. The config pane
is therefore a plain form, and acceptance criteria 3 and 4 below scope to the
scenario requests editor only.

`yaml.Decoder.KnownFields(true)` additionally lets the server *report* keys it
does not model — not as errors, but as notes.

**The note must say the opposite of what an earlier draft of this spec claimed.**
Unmodelled keys are **not** passed through to the engine. `compileScenario`
(`internal/domain/compile/compile.go:122-153`) builds a *fresh*
`taurus.Scenario` from modelled fields only, and `lifecycleapp/service.go:795-796`
lifts exactly two things out of the stored document:

```go
si.Requests = frag.Requests
si.DefaultAddress = frag.DefaultAddress
```

So what a stored fragment actually delivers is:

| Key | Stored verbatim | Reaches the engine |
|---|---|---|
| `requests:` (incl. per-request headers, method, body, assert) | yes | **yes** |
| `default-address:` | yes | **yes** |
| `headers:` | yes | no — overwritten by `Input.Headers` (telemetry trace context) |
| `data-sources:` | yes | no — by design; paths are resolved from uploaded files |
| `timeout:`, `keepalive:` | yes | no — modelled in `taurus.Scenario`, never read from `frag` |
| `think-time:`, `variables:`, anything else | yes | no — not modelled at all |

`compile.ScenarioInput` has no field to carry the middle rows, so this is
structural rather than a single missing line.

Two consequences, both in scope:

- **Close the gap where the fields already exist.** `headers`, `timeout` and
  `keepalive` are modelled in `taurus.Scenario`; `ScenarioInput` simply needs to
  carry them, with a scenario's own headers merged *beneath* telemetry's so
  `traceparent` and `baggage` always win.
- **Tell the truth about the rest.** The editor marks unmodelled keys
  stored-but-not-compiled — "`think-time` is preserved in your document but is
  not compiled into the run" — rather than implying they take effect. Without
  this, a user writes `think-time`, watches it survive a save and reload, and
  reasonably concludes it works.

### 2. Two-layer validation, split categorically so it cannot drift

| | Layer 1 — frontend | Layer 2 — backend |
|---|---|---|
| Owns | syntax and shape | all semantics |
| Checks | parse errors with line:col, `execution` is a list, `scenarios` is a map, form field types | `compile.Taurus()`, `ErrRequestsRequired`, `ErrEngineNeedsScript`, `ErrEnginePinned`, `loadprofile.Err*`, `ErrEngineUnknown`, engine availability, ownership |
| Cost | free from `parseDocument` | one debounced call |
| Authority | advisory | **truth — nothing deploys without it** |

The rule: **the frontend validates only what it can know without the domain.** No
semantic rule is written twice, so there is nothing to drift. The validate
endpoint calls the same `compile.Taurus` the deploy path calls, making it
authoritative by construction rather than a parallel implementation that agrees
today and diverges later.

`response.go:22` emits `{"message": "..."}` — a flat string with no position,
which cannot drive an editor squiggle. The validate endpoint therefore returns a
structured body (`[{severity, message, line, col, path}]`). This is a deliberate
departure from the shared error envelope, not an oversight; yaml.v3's
`TypeError` already carries the line numbers.

### 3. One execution page, reached automatically

`/status` and `/reports/:runId` are sibling pages with no link between them,
which is *why* the paste field exists. They collapse into one hub at
`/executions/:id` that changes with `ExecutionStatus.phase`
(`idle | deployed | running`) and holds config summary, controls, live metrics,
engine logs, and past reports together.

- Running a test **navigates there automatically**. The primary flow never
  touches a list.
- A real execution list exists for returning later.
- The route is deep-linkable and survives a refresh.

Controls map onto the phase enum — `idle`→Deploy, `deployed`+reachable→Trigger,
`running`→Stop, always→Purge — and the server already refuses every illegal
transition (`errors.go:86-107`: `ErrNotDeployed`, `ErrEnginesNotReady`,
`ErrAlreadyRunning`, `ErrNotRunning`, `ErrEnginesFinished`,
`ErrEnginesUnreachable`, `ErrRunActive`, `ErrCampaignFrozen`). The UI disables
the obvious and surfaces the 409 text for the rest; it does not duplicate the
state machine.

### Alternative rejected

**UI-only, generating YAML client-side.** Zero server change and fastest to
ship, but it moves Taurus schema knowledge into React, defers every semantic
error to deploy time, and reproduces exactly the failure that cost three
attempts on 2026-08-19 — a malformed `multi-test` wrapper that looked correct
until it wasn't. Rejected because this repo decides correctness in the domain,
and a form that silently emits invalid YAML is the same bug with a nicer button.

**Doing nothing** was considered: a CLI would be scriptable and cheaper. Rejected
because the watching half is inherently visual and already built, and because
runs here are driven by hand rather than from CI.

## The journey

| # | Step | Today | After |
|---|---|---|---|
| 1 | **New test** — name, target URL, method, headers, cookies, load, duration, engine | 5 API calls, 4 IDs tracked by hand | one form; the UI orchestrates the calls and defaults the project |
| 2 | **Tune** — Taurus editor, YAML canonical, form patches the AST | hand-author YAML, upload multipart | side-by-side editor with two-layer validation |
| 3 | **Capacity** — target QPS → engine count | `fanout` via curl | guided: offers calibration when no fresh profile exists |
| 4 | **Schedule or run now** | `POST /schedules` (form-urlencoded), or `deploy`→`trigger` | buttons |
| 5 | **Watch** — live metrics, engine logs | exists, but only by pasting an ID | reached automatically from the run |
| 6 | **Report** — outcome, trend, error signatures | exists, unlinked | linked from the execution hub |
| 7 | **Purge** | curl | button |

### Step 3 in detail: teaching capacity, not hiding it

`fanout` returns an engine count **only** for a fresh, engine-limited profile;
otherwise a named reason, and the API is explicit that it is *"never a number a
reader could mistake for one."* `FanOutResult.status` is
`ok | target_limited | inconclusive | stale | no_profile`, and `engines` is
present only on `ok`. Each non-`ok` status needs its own copy and its own call to
action — `no_profile` offers a first calibration, `stale` offers a
recalibration, `target_limited` means the search never saturated the engine.

This is the product's differentiator, so the UI teaches it rather than hiding it
behind a concurrency box. The calibration itself is watchable, not dead time:
`CalibrationJob` carries `phase` (`pending → bracketing → bisecting → done`),
`step_count` and `next_requested_qps`, so the search can be rendered converging.

**The editor and the QPS field interact.** `CapacityProfile.scenario_fingerprint`
is the scenario's content hash at calibration time, and fanout treats any
mismatch as staleness. Editing the Taurus YAML therefore invalidates the
capacity profile. The editor must warn **before saving**, naming the profile it
will invalidate, rather than letting the QPS field go `stale` mysteriously.

**"Target QPS" is two concepts and the form sets both.**
`taurus.Execution.Throughput` is a per-engine rate cap the engine enforces;
fanout's target QPS is an aggregate goal. They compose:
`engines = ceil(target / per_pod_qps)`, then `throughput = target / engines` so
the run lands on the target instead of overshooting.

## Acceptance criteria

1. A test can be created, configured, run, watched, reported on, and purged
   entirely in the browser, with no `curl` at any step.
2. Creating a test is **one form submission** from the operator's side, whatever
   the underlying call count.
3. Editing the Taurus YAML by hand, saving, and reloading preserves comments,
   key order, and every key Honryu does not model — asserted by a test using a
   document containing `think-time` and a comment.
4. Touching a form field in the editor changes only the keys that field owns;
   an unrelated hand-written key is byte-identical afterwards.
5. Invalid YAML shows a line-anchored error without a network round trip.
6. Semantically invalid YAML that is syntactically fine is rejected by the
   server with a line-anchored message, and the same input is rejected by
   `POST /deploy` — i.e. validate and deploy never disagree.
7. Saving a scenario that has a capacity profile warns that the profile will be
   invalidated, naming its `per_pod_qps` and `calibrated_at`, before the write.
8. Target QPS renders a number only when `FanOutResult.status` is `ok`; each of
   the other four statuses renders its own explanation and call to action.
9. A calibration can be started from the UI and its search rendered as it
   progresses through `bracketing` and `bisecting`.
10. Running a test navigates to `/executions/:id` without the operator typing or
    pasting an identifier anywhere in the flow.
11. Every lifecycle control is driven by `ExecutionStatus.phase`, and a 409 from
    an illegal transition surfaces the server's message rather than a generic
    failure.
12. `web/dist` stays under 1MB.
13. `api/openapi.yaml` documents exactly the route table, with the existing test
    passing.
14. A scenario-level `headers:` key in a stored fragment **reaches the engine**:
    a compiled shard config contains it, merged beneath telemetry's
    `traceparent` and `baggage`, which win on collision. Same for `timeout:`
    and `keepalive:`.
15. A key Honryu does not compile (`think-time`, `variables`) is reported by the
    editor as **stored but not compiled**, never as passed through. A test
    asserts the note's wording says it will not affect the run.
16. Headers are edited as a headers table, not a dedicated cookie field — a
    cookie is a header, and `Authorization` / `Content-Type` / `X-API-Key` are
    at least as common in practice.

## API changes in scope

- `GET /api/executions` — a properly scoped execution list. Today listing exists
  only as `GET /api/admin/executions`, which returns 200 in this deployment
  *purely because auth is off* (verified live) — a trap, not a foundation.
- `GET /api/scenarios/{id}/requests` — the editor cannot round-trip a fragment
  it cannot load, and `router.go:142` registers only the PUT. The port method
  `GetScenarioRequests` already exists (`ports/scenario_repository.go:46`).
- A raw `text/yaml` body on `PUT /api/scenarios/{id}/requests`, and a JSON body
  on `PUT /api/executions/{id}/config`, alongside the existing multipart. YAML
  is sent raw rather than JSON-wrapped: wrapping it in a JSON string escapes it
  for no gain, where a raw body is byte-preserving by construction.
- A validate endpoint returning structured, line-anchored diagnostics.
- **Compile fidelity for keys already modelled.** `compile.ScenarioInput` gains
  `Headers`, `Timeout` and `KeepAlive` so a fragment's own values reach the
  engine; a scenario's headers merge *beneath* `Input.Headers` so telemetry's
  `traceparent` and `baggage` win on collision. Without this, the editor's
  headers field is a trap — the key exists in `taurus.Scenario`, so people will
  write it, and today it is silently discarded at deploy.

**Deferred, deliberately:** the config-format migration to Taurus YAML that
`api/openapi.yaml` anticipates. It was originally folded in on the argument that
the form would otherwise be built twice; reading `executionapp/service.go:168`
showed that argument to be wrong. The form binds to the domain model through
JSON, not to the file format, so the transport format is invisible to the UI.
Honryu also needs `Engines` and `ScenarioID`, which Taurus has no concept of, so
the domain model — and the JSON contract with it — survives that migration.

## Open questions

1. **Schedule scope.** `POST /schedules` requires `tenant_id` and supports
   recurring cron within a 7-day admission horizon, with per-occurrence quota
   rejection. For a single operator, is one-shot "run at 22:00 tonight" enough
   for this phase, with recurrence deferred?
2. **Engine logs.** `GET /executions/{id}/scenarios/{id}/logs` is a fetch, not a
   stream. Is polled tail sufficient, or should logs stream?
3. **Existing pages.** Campaigns and Reservations have little meaning for a
   single operator. Leave them in the nav, or hide them behind a setting?
4. ~~**Cookies as a first-class field.**~~ **Resolved 2026-09-01: a headers
   table, no dedicated cookie field.** A cookie *is* a header, so a cookie
   widget adds only semicolon-joining, while a headers table also covers
   `Authorization: Bearer`, `Content-Type`, `Accept` and `X-API-Key` — at least
   as common in load tests. The repo agrees by omission: there is not one
   reference to "cookie" in any Go, YAML or TypeScript file. Note also that a
   single static cookie is usually the wrong instrument for an authenticated
   load test — one shared session hits server-side session locking and cache
   affinity, so you measure the session rather than the system. Realistic
   per-user sessions need `data-sources` plus variables, which belongs in the
   YAML pane, not the form.
