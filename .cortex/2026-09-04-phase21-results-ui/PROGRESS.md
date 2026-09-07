# Phase 21 — PROGRESS

Branch: `feat/phase21-results-ui` (12/12 tasks committed; not merged, not pushed).
Per-task deviations are in the commit bodies and the execution logs at the
bottom of `tasks.md`; this file is the close-out ledger.

## Per-task table

| # | Task | Commit | State |
|---|------|--------|-------|
| 1 | Pure series builder (`reportapp/series.go`) | `efe8da6` | done — see batch-1 deviation 2/3 |
| 2 | `ListIntervalsByRun` port + MySQL + fake + contract | `7a6afd5` | done — grew a write side (deviation 1) |
| 3 | `GET /api/runs/{run_id}/series` | `4373d34` | done — OpenAPI + handler tests |
| 4 | `TimeSeriesChart` plain-SVG primitive | `7176ddd` | done — no new deps (AC8) |
| 5 | Series API client | `f879c53` | done |
| 6 | Run report time-series section | `3a858bb` | done — `Reports.test.ts` → `.tsx` (batch-2 deviation 1) |
| 7 | Requested-vs-achieved overlay | `117d9cd` | done — constant line, `Load` has no stages |
| 8 | `LabelsTable` + integration | `2c5fef7` | done |
| 9 | RunCompare page + delta math | `9ecd2d1` | done — `comparison.ts` untouched (batch-3 deviation 1) |
| 10 | Route + nav registration | `e392983` | done — DashboardLayout unchanged (batch-3 deviation 2) |
| 11 | layout-check extension + local demo verification | `783d4df` | done — see batch-4 notes below |
| 12 | Phase close: gates + docs | this commit | done — gate table below |

## Gate results (2026-09-04, all green)

| Gate | Command | Result |
|------|---------|--------|
| gofmt | `gofmt -l` | clean |
| go vet | `go vet ./...` | pass |
| Unit (race) | `make test` | 56 pkgs ok, 0 fail |
| Integration (Docker) | `make integration` | pass (mysql 773s; note: suite legitimately exceeds go test's 10m default — the Makefile's 30m `-timeout` is required, a first attempt capped at 9m failed on its own cap, not on code) |
| Coverage gate (Docker) | `./scripts/coverage.sh` | **91.8% ≥ 90% PASS** |
| Lint | `golangci-lint run` (v2.12.2, CI pin) | 0 issues |
| Web types | `tsc -b` | pass |
| Web unit | `bun run test` (vitest) | 256/256, 37 files |
| Web build | `bun run build` | pass |
| layout-check | `LAYOUT_CHECK_URL=http://localhost:8080 bun run layout-check` | 198 ok / 0 fail, exit 0 |

## Batch 4 (tasks 11–12) notes and deviations

1. **layout-check discovers data; it does not seed it.** The task allowed
   either asserting against seeded demo data or honestly limiting to
   route+nav. Chosen: the script *discovers* an execution with ≥2 finalised
   runs and a run with series+labels via read-only API walks (the same
   endpoints the pages use), then asserts the delta table, the p95 overlay,
   and the Reports sections. Seeding stays out of the script by design — it
   reads deployments, it does not build them. A target without such data
   (verified: preview with the API stopped) skips the data-dependent
   assertions and still passes exit 0.
2. **The demo stack's runs were seeded out-of-band** (not committed; chore
   script in the task-11 session): `cmd/api` with `HONRYU_AUTH_MODE=demo`,
   fake DB, homelab personas, then curl as alice — tenants 1+2, project,
   scenario, execution, and two runs driven over the public API
   (deploy/trigger → `POST /api/ingest` batches with two labels and bucket
   histograms → Final → Stop to close the run row; the fake scheduler's
   pods never exit on their own, so teardown is what makes the execution
   re-triggerable). Batches push `run_id: 0`, which the ingest path
   attributes to the active run — no id guessing.
3. **Compare route asserted for all four personas** (alice/bob/carol/dave),
   not just a viewer: every persona's map grants `report:read` (the Reports
   nav item's grant), which is the set the task named. Report-detail
   sections asserted once, for carol (`tenant_viewer`, least-privileged
   reader), at every viewport.
4. **Latent crash fixed in layout-check** (in scope of task 11): the
   drawer/persona-section `page.goto`s were not failure-wrapped, so an
   unreachable target crashed the script with an uncaught exception instead
   of failing the run — first exposed when pointing the script at a stopped
   preview. Now wrapped like the ROUTES loop already was.
5. `vite preview` inherits `server.proxy`, so the default layout-check
   target also exercises the full data assertions when cmd/api is up — the
   honest-skip path only fires with the API down (that is how it was
   verified).

## Honest skips / pending

- **AC2 "against the live site": not run against honryu.pve.heri.life.** The
  homelab still runs phase20 tags (chart pinned in `84485dd`); the compare
  route and series endpoint do not exist there yet. Stood in: the local demo
  stack (real cmd/api + real Chromium over the real API), zero console
  errors asserted on both new surfaces at all three viewports. The live run
  pends the phase-21 deploy — same ledger pattern as phase 20's AC17, which
  was closed post-deploy.
- **`make engine` not run in this close** (not in task 12's gate list; needs
  `bzt` on PATH). No engine-side code changed in phase 21 — the series write
  path rides `Absorb`, covered by unit + integration + e2e lanes above.
- Migration count check: phase 21 added `0053_execution_report_series` only;
  no migration was edited.

## Phase-wide deviations (summary; details in tasks.md execution logs + commit bodies)

- **Batch 1:** intervals proved transient (discarded after report), so task 2
  added the write side the plan assumed existed — permanent per-second
  `execution_report_series` rows written inside Absorb's transaction
  (migration 0053), not just a read port. BuildSeries dedups exact
  `(ts, label, seq)` repeats (the plan's `(shard, seq)` key is not
  computable from `[]metrics.Interval`); the domain merge rule lives in
  `report.MergeSecond` so series and report cannot disagree.
- **Batch 2:** `Reports.test.ts` → `Reports.test.tsx` (JSX in tests); the
  hover-leave test dispatches `pointerout` (React derives leave from
  out/over); VUs+RPS share one axis on the dual chart by task spec;
  requested load renders as a constant line (`Load` has no stages).
- **Batch 3:** `web/src/api/comparison.ts` untouched (campaign domain);
  `DashboardLayout.tsx` unchanged — "Compare runs" is a Reports-page header
  link gated by `can('report','read')` and ≥2 runs; compare default
  preselect is A=oldest, B=newest; the p95 overlay hides on series fetch
  failure.
