# Phase 23 — task batches

## Block A — backend export (pi batch 1)
T1: `GET /api/runs/{run_id}/export` handler — `format=json` returns `{report, series}` (same shapes as existing endpoints); `format=csv` returns combined CSV (labels block, series block, `#` comment headers). Content-Disposition attachment filenames: `run-{id}.json` / `run-{id}.csv`. Reuse authorizeReport. Unknown format → 400. Handler tests: happy path both formats, auth 401/403, unknown format, series-nil 404.
T2: router registration + OpenAPI doc entry (follow existing pattern in router.go + wherever OpenAPI spec lives — find it). Contract test for route presence.

## Block B — export + copy-link UI (pi batch 1)
T3: Reports run page — "Export CSV" + "Export JSON" buttons (anchor download to export endpoint, house button style); "Copy link" button (navigator.clipboard.writeText(location.href), fallback: select-text input; test with mocked clipboard). Next to existing heading area.
T4: Execution page — "Copy link" button near the h1. Tests for both pages (rendering + click behavior with mocks).

## Block C — keyboard nav (pi batch 2)
T5: DashboardLayout — skip-to-content anchor (`href="#main"`, visible on focus, first in DOM), Escape closes mobile menu, focus-visible ring classes on nav links + theme toggle. DashboardLayout.test.tsx extended.
T6: App-wide button/link audit — grep for className without focus-visible on interactive elements in components/ + pages/; add `focus-visible:outline ...` ring utility consistent with house style. Chart percentile pills + stage editor controls included.

## Block D — layout-check + close (pi batch 2)
T7: layout-check — export + copy-link + skip-link assertions for alice; persona-scope honest (carol can view reports? check RBAC map in test file). Focus check: tab once, assert document.activeElement is skip link.
T8: phase close — full gates (make test, coverage >= 90, lint 0, vitest, tsc, build, layout-check vs local seeded stack as phase-22 precedent) + PROGRESS.md.

## Operator (Ryo, not pi)
- Cut branch, verify batches, quota gate, push/PR/merge/deploy per house pattern. Bump appVersion phase20 → phase23 + homelab values tags at deploy time.
