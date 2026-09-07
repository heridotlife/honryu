# Phase 19 — tasks

17 tasks across two tracks — 8 Go, 9 React. The Go track lands first:
**G1 + R1 form the seam** that proves the whole path and delivers "no pasting an
ID" on its own. G2 and G3 are hard blockers for the editor — without them it
cannot load or save. **G8 is a correctness blocker for R5**: without it the
headers table writes a key the deploy path discards.

Every Go task updates `api/openapi.yaml` in the same commit; the route-table
test enforces it. G8 is the exception — it changes compilation, not routes.

---

## Track A — Go / API

### G1. Add `GET /api/executions` returning the caller's executions
- **Files:** `internal/adapters/httpapi/router.go`,
  `internal/adapters/httpapi/execution_handlers.go`,
  `internal/app/executionapp/service.go`, `internal/ports/execution_repository.go`,
  `internal/adapters/repo/mysql/execution_repository.go`, `api/openapi.yaml`,
  plus tests alongside each
- **Criteria:** returns executions scoped to the caller's owners (all of them,
  not only running ones — `admin_handlers.go:13` calls `RunningExecutions` and
  is not a substitute); each item carries id, name, project_id and engine;
  ordered newest first; the OpenAPI route test passes
- **Satisfies:** spec "API changes in scope"; enables criterion 10
- **Depends on:** —

### G2. Add `GET /api/scenarios/{scenario_id}/requests`
- **Files:** `internal/adapters/httpapi/router.go`,
  `internal/adapters/httpapi/scenario_handlers.go`,
  `internal/app/scenarioapp/service.go`, `api/openapi.yaml`, tests
- **Criteria:** returns the stored fragment **byte-for-byte** as `text/yaml`
  (`ports/scenario_repository.go:46` already exposes `GetScenarioRequests`);
  404 when nothing has been uploaded; 409 for a non-portable scenario, matching
  `SetRequests`' own stance (`scenarioapp/service.go:261`)
- **Satisfies:** spec criterion 3 — the editor cannot round-trip what it cannot load
- **Depends on:** —

### G3. Accept a raw `text/yaml` body on `PUT /scenarios/{id}/requests`
- **Files:** `internal/adapters/httpapi/scenario_handlers.go`, `api/openapi.yaml`, tests
- **Criteria:** a `text/yaml` (or `application/x-yaml`) request body is stored
  verbatim; existing multipart uploads keep working unchanged; a byte-identical
  round trip through G2 → G3 → G2 returns the original bytes including comments
  and key order
- **Satisfies:** spec criteria 3 and 4; plan approach commitment 2
- **Depends on:** G2

### G4. Introduce a line-anchored diagnostic type
- **Files:** `internal/adapters/httpapi/response.go` (or a new `diagnostics.go`),
  `internal/app/scenarioapp/service.go`, tests
- **Criteria:** a `Diagnostic{Severity, Message, Line, Col, Path}` type
  serialises as a list; YAML type errors map to real line numbers extracted
  from yaml.v3's `TypeError`; the existing `writeError` envelope
  (`response.go:22`) is untouched for every other endpoint
- **Satisfies:** spec "The error shape needs to change"
- **Depends on:** —

### G5. Add `POST /scenarios/{id}/requests/validate`
- **Files:** `internal/adapters/httpapi/router.go`,
  `internal/adapters/httpapi/scenario_handlers.go`,
  `internal/app/scenarioapp/service.go`, `api/openapi.yaml`, tests
- **Criteria:** validates a submitted fragment **without storing it**, returning
  G4 diagnostics; shares one code path with `SetRequests` so the two cannot
  diverge; a test asserts that any body this endpoint accepts is also accepted
  by the store path, and any body it rejects is rejected there too
- **Satisfies:** spec criterion 6 — validate and deploy never disagree
- **Depends on:** G3, G4

### G6. Report uncompiled Taurus keys as informational diagnostics
- **Files:** `internal/app/scenarioapp/service.go`, tests
- **Criteria:** decoding with `KnownFields(true)` surfaces keys `taurus.Scenario`
  does not model (e.g. `think-time`) as `severity: info`, never as errors; such
  a document still validates and still stores; **the message states the key is
  stored but not compiled and will not affect the run** — not that it passes
  through. A test asserts the wording, because the earlier claim was the exact
  inverse of what `compileScenario` does (`compile.go:122-153` builds a fresh
  `taurus.Scenario` from modelled fields only)
- **Satisfies:** spec criterion 15
- **Depends on:** G5, G8

### G7. Accept a JSON body on `PUT /executions/{id}/config`
- **Files:** `internal/adapters/httpapi/execution_handlers.go`, `api/openapi.yaml`, tests
- **Criteria:** a JSON body matching `loadprofile.Profile`'s json tags
  (`loadprofile.go:57-69`) is accepted alongside the existing multipart wrapper;
  both paths reach the same `StoreConfig` and enforce the same validation
  (`executionapp/service.go:168-188`); the `multi-test` file format is
  **unchanged** — its migration is explicitly out of scope
- **Satisfies:** spec "API changes in scope"; enables R9
- **Depends on:** —

### G8. Carry a fragment's headers, timeout and keepalive through to the engine
- **Files:** `internal/domain/compile/compile.go`,
  `internal/app/lifecycleapp/service.go`, `internal/domain/compile/testdata/*`,
  tests
- **Criteria:** `compile.ScenarioInput` gains `Headers`, `Timeout` and
  `KeepAlive`; `lifecycleapp/service.go:795-796` populates them from the stored
  fragment alongside `Requests` and `DefaultAddress`; `compileScenario` merges a
  scenario's headers **beneath** `Input.Headers` so telemetry's `traceparent`
  and `baggage` win on collision; a golden test covers a fragment carrying all
  three plus a colliding `traceparent`, asserting telemetry wins and the rest
  survive; native scenarios keep today's behaviour
- **Satisfies:** spec criterion 14; removes the trap under R5's headers field
- **Depends on:** —

---

## Track B — React / SPA

### R1. Restructure routes and add the execution list
- **Files:** `web/src/App.tsx`, `web/src/pages/Executions.tsx` (new),
  `web/src/api/executions.ts` (new), `web/src/components/DashboardLayout.tsx`, tests
- **Criteria:** `/executions` lists executions from G1 and links each to
  `/executions/:id`; the nav gains Executions; `/status` redirects to
  `/executions` rather than 404ing an existing bookmark
- **Satisfies:** spec criterion 10 (first half)
- **Depends on:** G1

### R2. Build the execution hub with phase-driven controls
- **Files:** `web/src/pages/Execution.tsx` (new, absorbing `LiveStatus.tsx`),
  `web/src/api/lifecycle.ts` (new), `web/src/App.tsx`, tests
- **Criteria:** `/executions/:id` shows config summary, phase, and live metrics
  by **reusing** the existing `EventSource` wiring (`api/status.ts:47`) with no
  pasted ID; Deploy/Trigger/Stop/Purge render per `phase`
  (`idle | deployed | running`) and per `engines_reachable`; a 409 surfaces the
  server's message verbatim, not a generic failure; the page is deep-linkable
  and survives refresh
- **Satisfies:** spec criteria 10 and 11
- **Depends on:** R1

### R3. Link reports and engine logs into the hub
- **Files:** `web/src/pages/Execution.tsx`, `web/src/api/reports.ts`, tests
- **Criteria:** past reports for the execution list on the hub and link to
  `/reports/:runId`; engine logs render as a polled tail; a finished run reaches
  its report without leaving the hub
- **Satisfies:** spec journey step 6
- **Depends on:** R2

### R4. Add the Taurus editor shell
- **Files:** `web/src/components/TaurusEditor.tsx` (new),
  `web/src/api/scenarios.ts` (new), `web/package.json`, tests
- **Criteria:** loads a fragment via G2 and saves via G3; CodeMirror 6 with YAML
  highlighting (**not** Monaco); `web/dist` stays under 1MB with the editor
  included; a hand-written comment survives load → save → reload
- **Satisfies:** spec criteria 3 and 12
- **Depends on:** G2, G3

### R5. Add the form lens that patches the YAML AST
- **Files:** `web/src/components/TaurusEditor.tsx`,
  `web/src/lib/taurusDoc.ts` (new), tests
- **Criteria:** form fields (target URL → `default-address`, a **headers table**
  — no dedicated cookie field, since a cookie is a header — method, per-request
  URL) read from and write to the parsed document via `yaml.parseDocument`;
  editing one field leaves every other node — comments, key order, and
  uncompiled keys such as `think-time` — **byte-identical**; a unit test asserts
  byte equality, not structural equality
- **Satisfies:** spec criteria 4 and 16
- **Depends on:** R4, G8 *(without G8 the headers field writes a key the deploy
  path discards, which would ship a control that silently does nothing)*

### R6. Wire two-layer validation
- **Files:** `web/src/components/TaurusEditor.tsx`,
  `web/src/api/scenarios.ts`, tests
- **Criteria:** malformed YAML shows a line-anchored error with **no network
  call**; syntactically valid input calls G5 on a debounce and renders returned
  diagnostics at their lines; `severity: info` notes from G6 render distinctly
  from errors and never block saving, and read as **stored but not compiled**
  rather than implying the key takes effect
- **Satisfies:** spec criteria 5, 6 and 15; plan approach commitment 3
- **Depends on:** R4, G5, G6

### R7. Build the capacity panel
- **Files:** `web/src/pages/Execution.tsx`,
  `web/src/components/CapacityPanel.tsx` (new),
  `web/src/api/calibration.ts` (new), tests
- **Criteria:** renders an engine count **only** when `FanOutResult.status` is
  `ok`; each of `no_profile`, `stale`, `target_limited`, `inconclusive` renders
  its own explanation and call to action; a calibration can be started and its
  `CalibrationJob` progress rendered through `bracketing` and `bisecting` with
  `step_count` and `next_requested_qps`
- **Satisfies:** spec criteria 8 and 9
- **Depends on:** R2

### R8. Warn before invalidating a capacity profile
- **Files:** `web/src/components/TaurusEditor.tsx`,
  `web/src/api/calibration.ts`, tests
- **Criteria:** saving an edited fragment for a scenario holding a capacity
  profile warns **before** the write, naming the profile's `per_pod_qps` and
  `calibrated_at`; no warning when the profile is already `stale` or absent;
  the warning explains that Target QPS will need a recalibration
- **Satisfies:** spec criterion 7
- **Depends on:** R5, R7

### R9. Build the new-test flow
- **Files:** `web/src/pages/NewTest.tsx` (new), `web/src/api/*`, `web/src/App.tsx`, tests
- **Criteria:** one form (name, target URL, method, **headers table**, load,
  duration, engine) creates project-if-absent, scenario, execution, requests
  fragment and config, then navigates to `/executions/:id`; a failure names the
  step that failed rather than a generic error; no identifier is typed or
  pasted at any point; ramp-up defaults to a non-zero value rather than 0, since
  starting at full concurrency measures connection-pool cold start rather than
  steady state; the form warns when `concurrency < engines`, because
  `shard.Plan` silently clamps the shard count in that case
  (`internal/domain/shard/shard.go` — "callers that care compare the returned
  length against what they asked for")
- **Satisfies:** spec criteria 1, 2, 10 and 16
- **Depends on:** R2, R5, G7

---

## Sequencing

```
G1 ──► R1 ──► R2 ──► R3
                │     └─► R7 ──────────┐
G2 ─► G3 ─► R4 ─┤                      │
G8 ─────────────┴─► R5 ────────────────┴─► R8
      G4 ─► G5 ─┬─► R6                     │
                └─► G6 ─────────────────────┘
G7 ────────────────────────────────────────► R9
```

**G1 + R1 is the seam** — smallest slice that proves Go→React end to end and
independently delivers the daily win of never pasting an ID. Land it and
verify on the cluster before starting the editor track.

**G8 gates R5.** It has no dependencies and can land any time in the Go track,
but shipping R5's headers table without it would put a control in the UI that
writes a key the deploy path throws away — green everywhere, no effect on the
run. Verify it by reading a compiled shard config back through
`GET /api/runs/{run_id}/scenarios/{id}/shards/{shard}/config`, not by observing
a successful deploy.
