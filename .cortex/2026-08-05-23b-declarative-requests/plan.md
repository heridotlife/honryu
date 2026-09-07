# Task 23b option 2 — declarative scenario requests — Plan

**Parent task:** `.cortex/2026-07-30-honryu/tasks.md` lines ~274-283 · **Parent spec:** `.cortex/2026-07-30-honryu/spec.md` lines 281-283

No separate spec.md — the design was already resolved by the parent tasks.md's own "Likely shape" note (accept a Taurus `scenarios:` fragment, not a new request-definition API) and confirmed by code research; brainstorming was skipped as unnecessary.

## Context

- `taurus.Config`/`taurus.Scenario`/`taurus.Request` (`internal/domain/taurus/taurus.go`) already have full yaml+json tags matching bzt 1.16's wire format — no new Go type needed for the uploaded fragment.
- `compile.ScenarioInput` (`internal/domain/compile/compile.go:35-45`) already has `Requests []taurus.Request` and `DefaultAddress string`; `compileScenario` (`compile.go:129`) already raises `ErrRequestsRequired` when they're empty for a portable scenario — the compiler's portable branch is fully built and simply never fed.
- `scenario.Scenario` needs no new field — it stays pure portability metadata.
- `lifecycleapp.compileShards` (`service.go:411-417`) builds `ScenarioInput{Scenario: sc}` and only branches on `sc.Kind == scenario.KindNative` (sets `ScriptPath`). This is the critical second half — without a parallel `KindPortable` branch here, storing requests via a new API still wouldn't make anything runnable.
- `SetScenarioKind` exists in three places mirrored here: `internal/ports/scenario_repository.go:19-34` (interface), `internal/adapters/repo/mysql/scenario_repository.go:138-153` (`UPDATE ... RowsAffected()==0 → ErrNotFound`), `internal/ports/fake/repository.go:282-293` (map mutation). `scenarioapp.Repo` (`service.go:28-38`) duck-types a second, narrower copy of the same interface — both need the new method.
- `uploadExecutionConfig` (`internal/adapters/httpapi/execution_handlers.go:154-185`) is the exact template for "parse multipart YAML into a domain type, 400 on failure, hand the parsed struct to the service."

## Approach

Accept a Taurus `scenarios:` fragment directly (already decided), stored as its own table rather than a column on `scenario` — `scenario_test_file` (migration `0006`) already establishes "one blob-ish thing per scenario, its own table" as this codebase's pattern for exactly this shape. Wire it through in data-flow order: persist first, then read at compile time, so the compile-time wiring is verifiable the moment persistence lands rather than left dangling until the HTTP layer exists.

## Risks

| Risk | Mitigation |
|---|---|
| An uploaded fragment parses as valid YAML but is semantically empty/useless (no requests, or requests with no URL). | Scenario-scoped validation at upload time, backstopped by `taurus.Config.Validate()`'s existing whole-config check either way. |
| The compile-time wiring gets built but nothing ever exercises it end to end, so a regression goes unnoticed. | An e2e test that uploads a fragment then actually deploys+triggers a portable scenario, not just a unit test on the HTTP handler. |

## Out of scope

Editing/replacing an already-stored fragment beyond a full overwrite (PUT semantics only, no partial patch); any UI for this.

## Verification

Per-task: `go build ./...`, `go test ./... -count=1`, `golangci-lint run`, `scripts/coverage.sh` (≥90%). The MySQL-touching task additionally against real MySQL via testcontainers. Full e2e suite before considering the feature done.

## Numbering note

Tasks below continue forward from 48 (Phase 5's last task) rather than renumbering Phase 5's already-committed task 31. These execute *before* Phase 5 resumes, regardless of number. Task 49's migration (`0026_scenario_requests.sql`) claims the real next migration slot; Phase 5's own task 32 (`0026_reservation.sql`) will need its migration number bumped when Phase 5 execution picks back up.
