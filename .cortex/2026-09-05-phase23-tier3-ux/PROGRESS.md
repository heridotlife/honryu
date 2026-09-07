# Phase 23 — PROGRESS

## Batch 1 (tasks 1-4) — 2026-09-06, pi

All four tasks landed; one commit each. Gates at each commit: `go test
./internal/adapters/httpapi/` green (task 1-2), `bun run vitest run` +
`bun run tsc -b` green (task 3-4); before the batch's final commit the full
`make test` passed (exit 0, no FAILs), all 329 web tests passed, tsc clean.

### T1 — export handler (`b2cf7f8`)

- `runExport` in `internal/adapters/httpapi/report_handlers.go`: auth and
  data path identical to `runSeries` (authorizeReport against the report's
  execution; `deps.Series == nil` → 404 "series not configured").
- json = `{"report": <GET /report shape>, "series": {"points": [...]}}`.
- csv = two sections under `# run {id} — labels` / `# run {id} — per-second
  series` comment headers, written with `encoding/csv` into a
  `bytes.Buffer`. **Documented conventions:** LF endings (`UseCRLF=false`;
  csv.Writer defaults to CRLF), latency in seconds, `error_rate` a fraction,
  `err_pct` already percent (each exactly as the JSON wire carries it —
  the export formats nothing), empty cell where a percentile was
  unmeasured (the table renders an em-dash for the same condition).
- Deviation (also in the commit message): the router line, authz-audit
  entry, and `api/openapi.yaml` path landed in T1's commit rather than
  T2's — handler tests route through `NewRouter`, and
  `TestAuthzAuditCoversRoutes` / `TestOpenAPIMatchesRoutes` fail on any
  registered-but-undocumented route, so a green T1 commit necessarily
  carried them.
- Check-order note: `format` is validated (400) *before* the series-nil
  check, so a bad format is the caller's error on any deployment; the
  series-nil 404 still precedes any store read.

### T2 — route + OpenAPI + RBAC coverage (`a9018cf`)

- Route + OpenAPI + audit entry rode T1's commit (above). T2's commit
  extends `TestRBAC_ReportRoutesRequireReportRead`: the export route
  (`?format=json`) now read as tenant_viewer/campaign_manager (200) and
  denied 403 to an outsider — the route-level contract test per the
  existing bucket pattern (the OpenAPI sync itself is enforced by the
  pre-existing `TestOpenAPIMatchesRoutes`/`TestOpenAPITagsMatchRouteGroups`).

### T3 — Reports export + copy-link (`302e39f`)

- Run detail header: `Export CSV` / `Export JSON` anchors (`download`,
  href via `apiClient.baseUrl` — exposed read-only for exactly this
  raw-URL need) + `CopyLink`, styled as one small-button group
  (CopyButton's visual language).
- Deviation: task 4's extraction was pulled forward — `CopyLink` was
  *born* in `web/src/components/CopyLink.tsx` (reusing `copyText` from
  `ui/CopyButton`, which already implements the clipboard +
  select/execCommand fallback) instead of being inlined in Reports and
  extracted one task later.
- **Known limit (spec-level, flagged for phase 24):** anchor downloads
  carry no `Authorization` header. Works in no-auth and session-cookie
  deployments; a bearer-token deployment answers 401 to the anchor. A
  fetch+blob export with the client's auth header is the fix if it bites.
- jsdom note: the fallback test stubs `document.execCommand` (jsdom has
  none), same as `CopyButton.test.ts`.

### T4 — Execution copy-link (this commit)

- `CopyLink` next to the Execution h1 (right side of the header row).
- Deviation: the test lives in `Execution.live.test.tsx`, not
  `Execution.test.tsx` — the latter is the pure-function `.ts` file; JSX
  mounted tests for this page are the `.live.test.tsx` file per house
  pattern.

## Batch 1 leftover for Ryo / batch 2

- None blocking. Layout-check assertions for the new buttons are T7
  (batch 2), keyboard nav T5-T6.

## Batch 2 (tasks 5-8) — 2026-09-06, pi

All four tasks landed; one commit each. Gates at each commit: task 5-6
`vitest run` + `tsc -b` green (16/16 then 332/332), task 7 layout-check
green against the local seeded stack (422 ok / 0 fail).

### T5 — DashboardLayout keyboard (`ca605f0`)

- Skip-to-content anchor: first element in the shell DOM (before `<nav>`),
  `href="#main"` + `id="main"` on `<main>`. `sr-only focus:not-sr-only`
  per Tailwind's documented skip-link pattern, plus
  `focus:absolute focus:left-4 focus:top-2 focus:z-50` + a solid chip
  look while focused (z-50 beats the nav's sticky z-40).
- **CSS-order note (verified, load-bearing):** `focus:absolute` and
  `focus:not-sr-only` tie at (0,2,0), so Tailwind v4's deterministic
  utility sort decides — `.focus\:absolute:focus` lands AFTER
  `.focus\:not-sr-only:focus` in the built CSS (byte offsets 26497 vs
  26362 in the current build), so absolute wins. If a Tailwind upgrade
  ever flips that order, the focused link falls back to in-flow static
  (still visible, pushes nav down) — and task 7's layout-check assertion
  "skip link becomes visible while focused" is the live guard in a real
  engine, since jsdom computes no geometry.
- Escape closes the open drawer: separate keydown effect, same
  closed-state guard + cleanup pattern as the click-outside effect.
- Focus ring: house pattern reused verbatim from ui/Button
  (`focus:outline-none focus:ring-2 focus:ring-offset-2
  focus:ring-sky-500`) on the logo, inline nav links, and drawer links.
  **The theme toggle needed no change** — it is a ui/Button and inherits
  the ring; that IS the reuse the task asked for.
- Test deviation (documented in-file): no `@testing-library/user-event`
  dep exists in web/, so "first Tab focuses it" is modeled the way
  browsers compute forward-from-body — first DOM-order tabbable element
  via the standard selector, then `.focus()` — rather than
  `userEvent.tab()`. Adding the dep for one assertion was not worth the
  package.json change.
- TDD: 3 new tests written red first (all failed for the right reasons),
  then implementation, then 16/16 green.

### T6 — app-wide focus audit (`549c892`)

- Audit method: a brace/quote-aware tag scanner (naive regex ends a tag
  at the first `=>` inside a JSX attribute — found the hard way) over
  `src/components{,/charts,/ui}` + `src/pages`, flagging every literal
  `<button|<a|<Link>` whose tag carries no `focus:` class; helpers
  resolved by hand. Full grep list is in the commit message body.
- 12 sites gained the house ring (LabelsTable sort buttons, StageEditor
  tabCls, pctPill in BOTH Execution and Reports — duplicated local
  helpers, both fixed, "Report →" link, compare link, run-card links,
  APM external link, Campaigns select cards, ProfilePicker persona
  cards, ReportsTrend axis toggles, "+ New test" + execution row links).
  `rounded` added where the element had no corner radius for the ring.
- Verified already covered, untouched: ui/Button + ui/Input primitives,
  `runActionClass` (export anchors, ring since batch 1), CopyLink (ring
  since batch 1), DashboardLayout (T5), the skip link (its focus style IS
  its sr-only → visible reveal).

### T7 — layout-check extensions (`aa69810`)

- Three families: skip link present on every shell route (6 routes × 3
  viewports, unauthenticated pass — the link is session-independent);
  once-per-run focus check (first route, first viewport, documented —
  `keyboard.press("Tab")` must land on the skip anchor AND make it
  visible, the latter being the real-engine guard for the CSS-order note
  above); alice-only export-csv/export-json/copy-link on the report page
  + copy-link inside the existing live-route block on the Execution page.
- **Stack bring-up (phase-22 recipe, re-derived — phase-22 PROGRESS
  described it but stored no runnable script):** `go run ./cmd/api` with
  `HONRYU_AUTH_MODE=demo DEMO_ENABLED=true ENABLE_RBAC=true
  DEMO_SIGNING_KEY=… DEMO_PROFILES=<homelab personas JSON>
  INGEST_TOKEN=dev-engine-token` (fake DB + fake scheduler are defaults,
  port 8080), embedding the fresh dist. Seeded over the public API as
  alice (session cookie extracted from Set-Cookie and passed manually —
  the cookie is Secure, which curl will not replay over plain http):
  tenants 1+2 (quota raised 0→8 — `display_name` turned out to be
  required), tenant-1 project (`tenant_id` form field), **jmx scenario
  file upload** (a portable scenario without requests fails deploy's
  compile with ErrRequestsRequired — the e2e's native-scenario path is
  the proven one), execution A two finalised runs (ingest batches with 2
  labels + histograms + 5xx sprinkle, Final exit 0, Stop to close the
  run row — fake pods never exit alone), execution B left RUNNING under
  a 2s-interval ingest loop (`run_id: 0` attributes to the active run).
  Script kept out-of-band at /tmp/p23/seed.sh, same as phase 21/22.
- Verified: `LAYOUT_CHECK_URL=http://localhost:8080 bun run
  layout-check` → **422 ok / 0 fail, exit 0** (was 378 in phase 22; the
  +44 are the new assertions firing at every applicable viewport), all
  phase-22 assertions still green, live-section firing for real on
  running execution 2.

### T8 — phase close

| Gate | Command | Result |
|------|---------|--------|
| gofmt | `gofmt -l .` | 0 files |
| go vet | `go vet ./...` | pass |
| Unit (race) | `make test` | 56 pkgs ok, 0 fail |
| Coverage (Docker) | `./scripts/coverage.sh` | **91.8% ≥ 90% PASS** |
| Lint | `make lint` (golangci v2.12.2 pin) | 0 issues |
| Web unit | `bun run vitest run` | 332/332, 44 files |
| Web types | `tsc -b` | pass |
| Web build | `bun run build` | pass |
| layout-check | `LAYOUT_CHECK_URL=http://localhost:8080 bun run layout-check` | 422 ok / 0 fail, exit 0 |

- `web/dist/.gitkeep` deletion left uncommitted (local build artifact,
  per AGENTS.md). Close commit is the phase-22-style marker: PROGRESS.md
  stays a gitignored disk artifact, the gate table rides the message.
  **Not pushed** (operator instruction).

### Findings surfaced, not fixed (out of plan scope)

1. **`POST /api/executions/{id}/trigger` against an execution whose
   deploy failed (e.g. compile error) returns an EMPTY reply** — curl
   exit 52 (empty reply from server), no response body, and nothing in
   the server log (no panic trace, no audit line). Observed while
   seeding (deploy 400 → trigger hung → connection cut). Not reproduced
   in isolation or root-caused — worth a `systematic-debugging` pass; a
   4xx/409 answer is what the API contract implies.
2. Stale `go run ./cmd/api` processes orphan their compiled child binary
   (`/tmp/go-build…/exe/api`, comm `api`), which survives `pkill -f
   "go run ./cmd/api"` and keeps :8080 — bit twice during stack bring-up.
   Operational footnote only; kill the listening pid from `ss -tlnp`.
