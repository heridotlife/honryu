# Phase 106 — Information-density toggle (S)

## Problem
The k6-parity scorecard (2026-09-07) marked "Density toggle" as the sole
surviving gap: k6 Cloud offers a comfortable/compact row-density switch; Honryu
ships one density only. Rows feel sparse on large tables (Executions, Campaigns,
Clusters registry) and cramped on overview cards.

## Goal
A `comfortable | compact` density mode, user-toggled, persisted per browser,
applied app-wide without any page-level wiring.

## Design (mirrors the proven theme-toggle pattern in DashboardLayout.tsx)

1. **CSS custom properties + Tailwind v4 `@variant`** — `web/src/styles/globals.css`:
   - Define `--row-pad-y`, `--row-gap` custom properties on `:root`, overridden
     under a `.density-compact` class on `<html>` (same mechanism as
     `@variant dark (&:where(.dark, .dark *))`).
   - `@variant compact (&:where(.density-compact, .density-compact *))`.
   - Typography scale STAYS untouched (`.text-body-sm` etc. are the design
     language; compact shrinks whitespace, never font sizes — legibility law).
2. **DensityToggle component** — `web/src/components/DensityToggle.tsx`:
   - `comfortable/compact` segmented control, icon + label, `data-testid`.
   - Persists to `localStorage['honryu-density']`, mirrors `applyTheme`.
3. **DashboardLayout wiring** — toggle sits beside the theme button in the
   top nav (desktop) + inside the mobile menu section. On mount, the same
   `useEffect` that resolves theme resolves density (saved → else default
   `comfortable` — density has NO OS preference fallback; it's a table-density
   preference, not an accessibility medium).
4. **CardTable + Executions/Campaigns/Clusters rows** consume the CSS vars:
   - `CardTable` default `tableClassName` gains `py-[var(--row-pad-y)]` cells.
   - Card-list variant (below sm) gap uses `--row-gap`.
5. **No backend surface.** Pure SPA. No migration, no API change.

## Out of scope
- Font-size scaling (rejected: text scale is the design language).
- Per-page density overrides (one global mode, one localStorage key).

## Acceptance
- Toggle in nav on every page; choice survives reload; `.density-compact`
  class applied on `<html>` when compact.
- Compact mode visibly tightens Executions table row padding (measured: cell
  height delta ≥ 6px on a seeded table).
- vitest: DensityToggle unit tests (initial resolution, toggle persistence,
  html class application) + CardTable renders both densities.
- Existing tests untouched (no API change).
