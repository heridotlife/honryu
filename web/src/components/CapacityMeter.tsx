// Plain-SVG horizontal capacity bar (Sparkline house style: no charting
// library, currentColor so Tailwind's text classes drive it) plus the
// honest state for absent numbers. Since phase 25 the numbers are real:
// quota-wired deployments put engines_used/engines_ceiling on every
// /api/clusters row (unwired ones omit the fields entirely), so a row
// lights the bar up or renders the honest one-liner, per row.

/** Bar geometry: width is also the fill's cap, so overflow never spills. */
const BAR_WIDTH = 120;
const BAR_HEIGHT = 6;

export interface CapacityMeterProps {
  /** Unit caption rendered after the numbers, e.g. "engines". */
  label: string;
  /** Count in use; absent (with ceiling) still renders the honest line. */
  used?: number;
  /** Maximum count; absent (with used) still renders the honest line. */
  ceiling?: number;
}

/**
 * Fill fraction used/ceiling. Undefined -- rendered as "no capacity
 * reported" -- unless both numbers are present, finite, and the ceiling is
 * positive: a zero or negative ceiling has no honest bar.
 */
export function capacityFraction(used?: number, ceiling?: number): number | undefined {
  if (
    used === undefined ||
    ceiling === undefined ||
    !Number.isFinite(used) ||
    !Number.isFinite(ceiling) ||
    ceiling <= 0
  ) {
    return undefined;
  }
  return used / ceiling;
}

/** The meter: a bar plus "used / ceiling label" when numbers exist, an
 * honest one-liner when they do not. 100%+ turns red. */
export default function CapacityMeter({ label, used, ceiling }: CapacityMeterProps) {
  const fraction = capacityFraction(used, ceiling);
  if (fraction === undefined || used === undefined || ceiling === undefined) {
    return <span className="text-caption text-slate-500 dark:text-slate-400">no capacity reported</span>;
  }
  const over = fraction >= 1;
  const fillWidth = Math.max(0, Math.min(fraction, 1)) * BAR_WIDTH;
  return (
    <span
      role="img"
      aria-label={`${used} of ${ceiling} ${label} in use${over ? ' — at or over capacity' : ''}`}
      className={`inline-flex items-center gap-2 ${
        over ? 'text-red-600 dark:text-red-400' : 'text-slate-700 dark:text-slate-300'
      }`}
    >
      <svg
        width={BAR_WIDTH}
        height={BAR_HEIGHT}
        viewBox={`0 0 ${BAR_WIDTH} ${BAR_HEIGHT}`}
        className="shrink-0"
        aria-hidden="true"
      >
        <rect x={0} y={0} width={BAR_WIDTH} height={BAR_HEIGHT} rx={3} fill="currentColor" opacity={0.15} />
        <rect x={0} y={0} width={fillWidth} height={BAR_HEIGHT} rx={3} fill="currentColor" />
      </svg>
      <span className="text-caption whitespace-nowrap">
        {used} / {ceiling} {label}
      </span>
    </span>
  );
}
