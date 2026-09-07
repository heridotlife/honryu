# Phase 26 — appVersion/tag hygiene + dependabot fold-in + PDF export

Date: 2026-09-07 · Branch: `feat/phase26-hygiene-pdf` off develop `47d726f`
(depends: #248–#251 dependabot merges already folded in)

## Why

Three loose ends, one phase:

1. **appVersion staleness**: `deploy/chart/honryu/Chart.yaml` still says
   `appVersion: "phase20"` while prod runs `:phase25`. The sidecar image in
   `values.yaml` is pinned to `:phase16` — also stale relative to phase 25.
2. **Dependabot backlog**: 4 PRs (#248–#251) were sitting open; all merged
   into this branch's base. Chart must keep linting.
3. **PDF export**: Reports page has CSV/JSON export anchors but no PDF.
   Operators want a printable artifact for run reports.

## What changes

### T1 — Chart hygiene
- `Chart.yaml appVersion: "phase20"` → `"phase26"`.
- `values.yaml` sidecar image tag `phase16` → `phase26` (build + push a
  fresh `honryu-sidecar:phase26` so the pin matches a real digest).
- Chart `version: 0.1.0` → `0.2.0` (first visible chart change since
  initial).

### T2 — sidecar image build
- `p26_build.sh` (p25 pattern): also build/push `honryu-sidecar:phase26`
  from `engine/` sources (verify the sidecar's Dockerfile path — p25 build
  script only built api/calibrator/scheduler).

### T3 — PDF export button
- ReportDetail header action group gains "Export PDF" anchor styled like
  CSV/JSON (`runActionClass`, Download icon, `data-testid="export-pdf"`).
- Approach: **server-side PDF** at `GET /api/runs/{id}/export?format=pdf`.
  Reuses the existing export handler; renders the JSON report structure
  into a minimal PDF (gopdf or similar stdlib-shape lib — pick lightest
  dep that passes gosec/govulncheck). Content: title line (Run #id,
  outcome, started/ended), summary table (percentiles), labels.
- vitest: anchor present + href points at `format=pdf`.
- go tests: export handler `format=pdf` returns `application/pdf`,
  non-empty body, sane prefix `%PDF-`.
- layout-check: extend the export affordances assertion to include the
  PDF anchor.

### T4 — close
- PROGRESS.md, gates table, phase-close commit.

## Non-goals

- No multi-run PDF, no charts-in-PDF (tables + text only, phase 26).
- No chart values schema change beyond tags/appVersion.
