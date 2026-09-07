# Phase 23 — Tier 3 UX: export/share, report deep-links, keyboard nav
Date: 2026-09-05 (late). Branch: feat/phase23-tier3-ux off develop 2722fc7.

## Goal
Close the k6-parity UX gap that remains after phase 22: get run data OUT of honryu (CSV/JSON export, shareable deep-links) and make the web app keyboard-navigable. Dark mode already exists (DashboardLayout theme toggle + Tailwind dark: classes) — NOT in scope beyond verifying chart colors work in dark mode.

## In scope
1. **CSV export** — per run: series points + per-label table as CSV download. Server-side: `GET /api/runs/{run_id}/export?format=csv` (Content-Disposition attachment; reuses GetReport + Series auth path). Two files or one combined? ONE combined: labels section then per-second section, comment-header each (`# labels`, `# series`).
2. **JSON export** — same endpoint `format=json`: the exact series+report JSON the pages consume (stable shape, documented).
3. **Copy-link / share** — Reports page per-run: "Copy link" button (navigator.clipboard, house button pattern) producing `https://host/reports/{run_id}`; Execution page: same for `/executions/{id}`. NO public/unauthenticated share tokens (RBAC stays).
4. **Keyboard nav** — DashboardLayout: skip-to-content link (first Tab), focus-visible rings via Tailwind `focus-visible:` utilities on nav links + all buttons app-wide audit, Escape closes mobile menu. Run selectors on Compare page: arrow-key navigation within existing select elements is native — verify only.
5. **layout-check** — new assertions: export button present on report page, copy-link present, skip-link present, one focus-visible check (tab to first element, assert ring class or focus).

## Non-goals
- Public share links without auth (RBAC relaxation) — explicitly deferred.
- PDF export, scheduled email reports — not requested.
- New chart types.
- i18n.
- The quota-0 NewTest 429 copy improvement (needs error-shape contract; phase-24 candidate with backend error codes).

## Evidence base
- runSeries handler: internal/adapters/httpapi/report_handlers.go:82 — auth via authorizeReport, deps.Series nil → 404.
- Router: internal/adapters/httpapi/router.go:214 GET /api/runs/{run_id}/series.
- No export/Content-Disposition anywhere in httpapi (grep verified).
- Keyboard nav: zero onKeyDown handlers in web/src/pages (grep verified).
- Dark mode present: DashboardLayout.tsx:41-86 (localStorage honryu-theme, prefers-color-scheme, classList dark).
- Reports page: web/src/pages/Reports.tsx (per-label table + series sections exist, labels at bottom).

## Tasks → see tasks.md
