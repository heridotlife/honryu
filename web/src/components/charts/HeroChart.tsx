// Phase 31's lead chart: the run's per-second shape as one k6-style
// picture -- concurrency, throughput, and error rate together -- replacing
// the former chart-vus-rps + chart-errors pair atop the time-series tab.
//
// Composition, not a fork: TimeSeriesChart draws line/dot series on a
// single shared y-axis, and err_pct's 0-100 domain would flatten into the
// baseline under RPS in the thousands. heroSeries() therefore linearly
// rescales the error points onto the VUs/RPS data band (err_pct 100
// touches the band's top, 0 its baseline), so the shape survives -- a line
// hugging zero that jumps when errors spike -- and the caption below
// states the 0-100 contract. TimeSeriesChart's own legend (drawn above the
// chart, naming every series with its color swatch) serves as the hero's
// legend row; series names match the old charts so data-series assertions
// keep holding.
import type { SeriesPoint } from '../../api/series';
import TimeSeriesChart, { type SeriesSpec } from './TimeSeriesChart';

export interface HeroChartProps {
  /** The run's per-second samples; an empty array renders the empty state. */
  points: SeriesPoint[];
}

/**
 * Maps the run's samples onto the hero's three series: VUs and RPS raw,
 * error % rescaled from its 0-100 domain onto [0, refMax], where refMax is
 * the largest VUs/RPS sample. A pure transform, exported for unit tests
 * the way nearestPoint/axisTicks are. Scaled error never exceeds refMax
 * (err_pct tops out at 100), so the y axis stays the VUs/RPS axis.
 */
export function heroSeries(points: SeriesPoint[]): SeriesSpec[] {
  const refMax = Math.max(0, ...points.map((p) => Math.max(p.vus, p.rps)));
  // A run with no traffic at all (refMax 0) has no band to scale onto:
  // keep err_pct raw so a nonzero error rate still draws.
  const scale = refMax > 0 ? refMax / 100 : 1;
  return [
    { name: 'VUs', color: 'text-sky-500', points: points.map((p) => ({ x: p.ts, y: p.vus })) },
    { name: 'RPS', color: 'text-amber-500', points: points.map((p) => ({ x: p.ts, y: p.rps })) },
    { name: 'error %', color: 'text-rose-500', points: points.map((p) => ({ x: p.ts, y: p.err_pct * scale })) },
  ];
}

/** The hero: one chart leading the time-series tab, VUs + RPS with the error rate folded in. */
export default function HeroChart({ points }: HeroChartProps) {
  return (
    <div data-testid="chart-hero">
      <p className="text-caption mb-2 font-medium text-slate-500 dark:text-slate-400">Concurrency, throughput, and error rate</p>
      <TimeSeriesChart xType="time" height={240} series={heroSeries(points)} />
      <p className="text-caption mt-2 text-slate-500 dark:text-slate-400">error % (right-axis scale: 0-100)</p>
    </div>
  );
}
