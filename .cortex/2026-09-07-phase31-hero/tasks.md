You are working on Honryu web (React/TS, bun, Tailwind).

REPO: /home/coder/personal/honryu
BRANCH: feat/phase31-hero-overlay (checked out, off develop)

Goal: Phase 31 — hero overlay chart on Run Detail Time series tab. One big
combined chart (k6-style): VUs + RPS overlaid with a translucent error-rate
band, replacing the current chart-vus-rps + chart-errors pair as the lead.

VERIFIED RECON:
- web/src/components/charts/TimeSeriesChart.tsx: existing chart component.
  Exports chartPalette, nearestPoint, axisTicks. Takes props: xType="time",
  yLabel, series: [{name, color, points}] — read the full component first.
- web/src/api/series.ts: SeriesPoint {ts, vus, rps, err_pct, latency?}.
- web/src/pages/Reports.tsx TimeSeriesCharts component (~line 512-545):
  renders chart-vus-rps (VUs sky + RPS amber), chart-errors (error % rose,
  yLabel "%"), chart-latency (percentile selector + chart-requested overlay).
- Series wire: {"ts":1788796868,"vus":1,"rps":108,"err_pct":0,...}.
- Existing testids MUST survive: chart-vus-rps, chart-errors, chart-latency,
  chart-requested (layout-check.js + Reports.test.tsx depend on them).

TASKS (one commit each, gates between):

T1 — HeroChart component
- web/src/components/charts/HeroChart.tsx: wraps TimeSeriesChart. Renders ONE
  chart with THREE series: VUs (text-sky-500), RPS (text-amber-500), and
  error% (text-rose-500). Error series styled as overlay: if TimeSeriesChart
  supports only line series, render error as its own series — do NOT fork
  TimeSeriesChart internals; compose only.
  Add a legend row above the chart naming all three + their colors.
  data-testid="chart-hero". Props: points: SeriesPoint[].
- If pure-composition cannot express the error overlay cleanly (e.g. scale
  clash: err_pct 0-100 vs rps thousands), then instead give HeroChart its own
  minimal dual-axis SVG ONLY IF TimeSeriesChart truly cannot be reused —
  prefer: normalize err_pct onto its own series with yLabel omitted, and a
  caption "error % (right-axis scale: 0-100)". Compose, don't fork.
- web/src/components/charts/HeroChart.test.tsx: renders with points incl.
  err_pct>0; legend shows VUs, RPS, error; chart-hero testid present; empty
  points renders empty state.

T2 — Wire into TimeSeriesCharts
- Reports.tsx TimeSeriesCharts: FIRST render HeroChart (chart-hero), then
  chart-latency, then chart-requested. REMOVE the separate chart-errors block
  BUT keep a data-testid="chart-errors" wrapper element (empty <div
  data-testid="chart-errors" className="hidden"> with a visually-hidden
  caption "error rate folded into hero chart") so existing testid assertions
  keep passing. chart-vus-rps likewise folded into hero — keep its testid
  wrapper the same way (hidden, caption "folded into hero chart").
- Reports.test.tsx: add test — Time series tab shows chart-hero first; the
  hidden chart-vus-rps/chart-errors wrappers still exist (toBeInTheDocument).
- Gates: cd web && /home/coder/.bun/bin/bun run vitest run && tsc -b.

COMMIT STYLE:
  feat(web): hero overlay chart for run time series
  feat(web): hero chart leads time series tab

No PR, no push. vitest total must grow from 367 and stay green.