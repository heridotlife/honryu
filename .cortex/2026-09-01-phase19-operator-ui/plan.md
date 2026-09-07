# Phase 19 — Operator UI: implementation plan

Spec: `.cortex/2026-09-01-phase19-operator-ui/spec.md`
Date: 2026-09-01

## Context

The SPA calls ~13 of ~75 API operations and writes exactly once
(`POST /tenants/{id}/campaigns`). What exists and what is missing, from the code:

- **Routes.** `App.tsx` mounts five sibling pages: `/reports`, `/reports/:runId`,
  `/reservations`, `/status`, `/campaigns`, `/clusters`. Nothing links `/status`
  to `/reports/:runId`, which is why `LiveStatus.tsx:77` is a text field you
  paste an execution ID into.
- **Live metrics already work.** `web/src/api/status.ts:47` opens an
  `EventSource` on the SSE stream; `LiveStatus` keeps a 1s sliding window and
  polls `GET /status` every 10s. This is reused, not rebuilt.
- **Scenario requests are already stored verbatim.**
  `mysql/scenario_repository.go:157` writes to `scenario_requests.raw`;
  `scenarioapp/service.go:265-276` unmarshals into `taurus.Scenario` only to
  validate, then persists the original `raw`. The spec's "store as text, never
  re-emit" constraint therefore already holds — no storage change is needed for
  the YAML-canonical editor.
- **Execution config is not a document.** `executionapp/service.go:201-213`
  rebuilds `loadprofile.Wrapper` from `LoadProfileFor()` and `CriteriaFor()`.
  The config is relational rows, fully modelled by `loadprofile.Entry`
  (`loadprofile.go:25-37`, eight scalars) and `Profile.Criteria`. There are no
  unknown keys to preserve.
- **Two hard blockers.** `PUT /api/scenarios/{scenario_id}/requests`
  (`router.go:142`) has **no GET**, so the editor cannot load the fragment it
  edits — `ports/scenario_repository.go:46` already exposes
  `GetScenarioRequests`, it simply has no route. And there is no
  `GET /api/executions`: `adminExecutions` (`admin_handlers.go:13`) calls
  `RunningExecutions`, returning only what is currently running.
- **The stored fragment is mostly not compiled.** `lifecycleapp/service.go:795-796`
  lifts only `frag.Requests` and `frag.DefaultAddress` out of the stored
  document, and `compileScenario` (`compile.go:122-153`) builds a *fresh*
  `taurus.Scenario` from modelled fields. A scenario-level `headers:` is
  overwritten by `Input.Headers` — telemetry's `traceparent`/`baggage`
  (`telemetry.go:59-71`) — and `timeout`/`keepalive` are modelled in
  `taurus.Scenario` but never read from `frag` at all. `compile.ScenarioInput`
  has no field to carry them, so this is structural. Per-request headers
  (`taurus.Request.Headers`) *do* survive, because `si.Requests = frag.Requests`
  carries the full structs.
- **Errors carry no position.** `response.go:22` emits `{"message": "..."}`,
  which cannot drive an editor squiggle.
- **Lifecycle states are already enumerated.** `ExecutionStatus.phase` is
  `idle | deployed | running`, and `errors.go:86-107` lists every illegal
  transition the server already refuses.

## Approach

Two tracks. **Go first**, because the React work consumes endpoints that do not
exist yet, and because two of them are hard blockers.

The tracks meet at a deliberate seam: task **G1** (`GET /api/executions`) plus
**R1** (list + route restructure) together deliver spec acceptance criterion 10
— running a test without pasting an identifier — and prove the whole Go→React
path before any editor work begins. If that seam is wrong, it is wrong cheaply.

Three design commitments carry the rest:

1. **YAML is canonical for scenario requests only.** The Taurus fragment is
   open-ended and `taurus.Scenario` models a subset, so the editor treats the
   document as truth and patches its AST. The execution config gets a plain
   form: it is eight numbers, fully modelled, and a YAML pane over a struct
   would be ceremony.
2. **The requests endpoint accepts a raw `text/yaml` body**, not JSON-wrapped
   YAML. Wrapping YAML in a JSON string escapes it for no gain; a raw body is
   byte-preserving by construction and keeps the storage guarantee trivially
   true. JSON is used for the config, which really is structured.
3. **Validation splits categorically, never duplicated.** The frontend owns
   syntax and shape (free from `parseDocument`); the backend owns every
   semantic rule and calls the same `compile.Taurus`
   (`internal/domain/compile/compile.go:70`) the deploy path calls, so validate
   and deploy cannot disagree.

## Risks

| Risk | Mitigation |
|---|---|
| Validate and deploy diverge, so the editor lies | The validate endpoint calls the same `compile.Taurus` deploy uses; acceptance criterion 6 asserts agreement with a test that runs both over one input |
| The form lens silently destroys hand-written YAML | Patch the AST via `parseDocument`; criterion 4 asserts an unrelated key is **byte-identical** after a form edit, not merely present |
| A user writes a key that stores and reloads cleanly but never affects the run — the worst kind of failure, since every signal is green | G8 closes the gap for `headers`/`timeout`/`keepalive`, which are already modelled; for everything else the editor states plainly that the key is **stored but not compiled** (criteria 14 and 15). An earlier draft of the spec claimed the opposite; it was wrong and is corrected |
| Bundle growth ships in every image (`//go:embed all:dist`, 340KB today in a 42.5MB binary) | CodeMirror 6 over Monaco; criterion 12 caps `web/dist` at 1MB and is checked in CI |
| Editing a scenario silently invalidates its capacity profile | `ScenarioFingerprint` (`scenarioapp/service.go:279+`) hashes files plus the requests fragment; R8 warns before the write, naming `per_pod_qps` and `calibrated_at` |
| Route table and OpenAPI drift | A test already asserts they match; every Go task updates `api/openapi.yaml` in the same commit |
| Coverage gate (repo is below its 90% target, pre-existing) | Each Go task lands with its tests; no task defers them |
| The new-test wizard orchestrates 5 calls and can half-fail | R9 lands last, on proven endpoints, and surfaces which step failed rather than a generic error |

## Out of scope

- The execution config → Taurus YAML file-format migration. It was considered
  and **deliberately deferred**: the form binds to the domain model through
  JSON, not to the file format, so there is no second build to avoid. Honryu
  needs `Engines` and `ScenarioID`, which Taurus has no concept of, so the
  domain model survives that migration and the JSON contract with it.
- Authentication UI, tenancy screens, campaign/reservation features, and any UI
  for `POST /api/ingest`, `/metrics` or `/healthz` (spec Non-goals).
- Streaming engine logs. The endpoint is a fetch; polled tail is accepted for
  this phase (spec Open question 2).

## Verification

End to end, on the homelab cluster, driving the product through its own UI:

1. From an empty state, create a test against `httpbin.pve.heri.life` with a
   cookie header — one form, no identifier typed anywhere.
2. Open the Taurus editor, hand-add a comment and a `think-time` key, save,
   reload: both survive. Change a form field; the comment and `think-time` are
   byte-identical afterwards. The editor states that `think-time` is stored but
   not compiled.
2b. Add a `Cookie` header in the headers table, deploy, and read the compiled
   shard config back via
   `GET /api/runs/{run_id}/scenarios/{id}/shards/{shard}/config`: the header is
   present, alongside — not instead of — `traceparent` and `baggage`. This is
   the only step that proves G8 end to end; a green deploy does not.
3. Enter a target QPS with no capacity profile: the UI offers calibration,
   runs it, and renders the search through `bracketing` and `bisecting`.
4. Run the test; the browser lands on `/executions/:id` unaided; live metrics
   stream; the report links from the same page when it finishes.
5. Purge from the UI; the report survives the purge.

Static gates: `make test`, `make lint`, `make helm-lint`, the OpenAPI route
test, and the `web/dist` size check.
