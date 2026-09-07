# Task 23b option 2 — declarative scenario requests — Tasks

**Plan:** `.cortex/2026-08-05-23b-declarative-requests/plan.md`

Executes before Phase 5 resumes. Every task must leave `go test ./...`, `golangci-lint`, and `scripts/coverage.sh` (≥90%) green.

**Status (2026-08-05): all 5 tasks done.** `77b10c5`, `c69bc96`, `02c505c`, `945d6da`, `1f74051`. Task 53's e2e test surfaced a second wiring gap beyond what the plan anticipated — `ensureTestFiles` required a script for every scenario regardless of `Kind`, so `Trigger` still rejected a portable scenario even after task 52's compile-time fix — fixed as part of closing task 53 (see that commit). `compile.ErrRequestsRequired` was also unmapped in `httpapi`'s error table (500 instead of 400); fixed alongside.

**Self-review (2026-08-05) found one gap, fixed in `f62c32a`:** `SetRequests` didn't check a scenario's `Kind`, so uploading requests to a scenario already pinned native by an uploaded script would succeed (200) and silently store data `compileShards` never reads for a native scenario — a success response indistinguishable from a meaningful one. Now rejected with a new `ErrScenarioNotPortable` (409), mirroring how `ErrScenarioInUse` is already a state conflict rather than a bad-upload error.

Task 23b (both options) is now fully closed. Ready to resume Phase 5 at task 32.

---

### 49. Persist a scenario's declarative requests fragment — **done, `77b10c5`**
- **Files:** `migrations/0026_scenario_requests.sql` (new `scenario_requests(scenario_id PK, raw MEDIUMTEXT, updated_time)`, mirroring `0006_scenario_test_file.sql`'s one-row-per-scenario shape), `internal/ports/scenario_repository.go` (`SetScenarioRequests(ctx, scenarioID, raw []byte) error`, `GetScenarioRequests(ctx, scenarioID) ([]byte, error)` — raw bytes, not a parsed struct), MySQL + fake + conformance
- **Criteria:** conformance suite passes fake+MySQL; `GetScenarioRequests` on a scenario with nothing uploaded returns `ports.ErrNotFound`
- **Depends on:** —

### 50. Add `scenarioapp.SetRequests` — **done, `c69bc96`**
- **Files:** `internal/app/scenarioapp/service.go` (+ tests)
- **Criteria:** `SetRequests(ctx, scenarioID, raw []byte) error` parses `raw` as YAML into `taurus.Scenario`, validates (non-empty `Requests`, each `Request.URL` non-empty), persists the original raw bytes (not re-marshaled) via the new repo method
- **Depends on:** 49

### 51. Add `PUT /api/scenarios/{scenario_id}/requests` — **done, `02c505c`**
- **Files:** `internal/adapters/httpapi/scenario_handlers.go`, `router.go`, `api/openapi.yaml`, `errors.go` (`badRequestErrors` additions)
- **Criteria:** parses an uploaded YAML fragment exactly as `uploadExecutionConfig` does (`execution_handlers.go:154-185`), 400 with the underlying error message on parse/validation failure, calls `scenarioapp.SetRequests`, 200 on success
- **Depends on:** 50

### 52. Wire portable requests into `compileShards` — **done, `945d6da`**
- **Files:** `internal/app/lifecycleapp/service.go` (`compileShards`, `service.go:411-417`)
- **Criteria:** a new `sc.Kind == scenario.KindPortable` branch loads the stored fragment via the new repo method, unmarshals into `taurus.Scenario`, sets `si.Requests`/`si.DefaultAddress`; a not-found (nothing uploaded) leaves `si.Requests` nil, letting `compile.Taurus`'s existing `ErrRequestsRequired` check surface the "no requests" error at deploy time — no new, duplicate error path
- **Satisfies:** the second half of closing `ErrRequestsRequired` — what actually makes a portable scenario runnable
- **Depends on:** 49

### 53. End-to-end: create a portable scenario, upload requests, deploy and trigger it — **done, `1f74051`**
- **Files:** `test/e2e/` (extend an existing phase's suite or add a small new test)
- **Criteria:** a scenario created via `POST /api/scenarios` (no file upload) stays `KindPortable`; `PUT .../requests` with a valid fragment succeeds; `Deploy`+`Trigger` on that scenario succeeds and produces real load; the same scenario with nothing uploaded fails `Trigger`/deploy with `ErrRequestsRequired` surfaced clearly
- **Depends on:** 51, 52
