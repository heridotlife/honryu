# Phase 107 — Run starring + run notes (bundled 1+2 from k6 gap analysis)

## Problem
k6 Cloud deprecates baselines in favor of star/unstar; every run carries a
user-defined note. Honryu runs have neither: RunCompare picks its baseline by
slot position only, and there is no way to annotate why a run matters.

## Goal
1. Star runs; starred state visible in Reports list; star filter; RunCompare
   "vs baseline" mode defaults to starred runs (starred = baseline candidates).
2. Free-text note per run, editable from the Reports row + run detail, shown
   in list and RunCompare slots.

## Design

### Backend
- Migration `0087_run_star_note.sql`: `ALTER TABLE execution_report ADD
  COLUMN starred TINYINT(1) NOT NULL DEFAULT 0, ADD COLUMN note TEXT NULL`.
- Domain `report.Report`: `Starred bool json:"starred,omitempty"` + `Note
  string json:"note,omitempty"` (empty = absent, matches house omitempty law).
- `reportColumns` const (mysql report_store.go) gains `starred, note`; scan
  sites updated (single-row Get + listReports shared scan).
- ReportStore port + mysql impl:
  - `SetStarred(ctx, runID, starred bool) error` — UPDATE, ErrNotFound if no row.
  - `SetNote(ctx, runID, note string) error` — note capped at 2000 runes
    (domain-level validation in handler, not DB).
- HTTP (router.go, reports group, same auth as runReport — authorizeRunShard
  pattern reuses report read auth; write needs the reports UPDATE-equivalent:
  use the same authorize the campaign/execution mutations use, ActionUpdate):
  - `PUT /api/runs/{run_id}/star` body `{"starred": bool}` → 200 `{"run_id":N,"starred":bool}`
  - `PUT /api/runs/{run_id}/note` body `{"note": string}` (≤2000 runes) → 200 `{"run_id":N,"note":"…"}`
  - Star/note ride ALONG in existing GETs (ListReports, GetReport, runsCompare
    payload rows) — no new read endpoints.
- Fake store (ports/repositorytest or wherever ReportStore fakes live) gains
    the two methods; contract tests updated.

### Frontend
- `web/src/api/reports.ts` (or wherever run APIs live): `starRun(runID, bool)`,
  `setRunNote(runID, note)` generated/handwritten per house pattern.
- Reports.tsx list rows: star toggle (☆/★ button, aria-pressed), note pencil →
  inline edit (input + save on Enter/blur), note shown truncated under run id.
  "Starred only" filter chip beside existing filters.
- RunCompare: in "vs baseline" mode the run picker lists starred runs first
  (starred badge); if exactly one starred run exists it preselects as baseline.
- No new pages. Empty states: starring zero runs → filter chip disabled.

## Out of scope
- Per-tenant star permissions (any role that can read reports can star).
- Note history/edit log (last write wins).

## Acceptance
- Migration applies clean (chart job); existing rows default unstarred/no note.
- Star a run in prod UI → survives reload; Reports list shows ★; filter works.
- Note persists; truncation at 2000 runes enforced with 400 on exceed.
- RunCompare preselects single starred run as baseline.
- Full gates: go unit+race, vitest, tsc, helm lint, e2e boot harness green.
