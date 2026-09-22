import type { SoakTrend } from '../api/reports';

/**
 * The phase-103 per-label leak banner: the run's aggregate trend is diluted
 * by healthy labels -- a leaking /checkout beside a flat /browse can hold
 * the aggregate under its threshold -- so each label whose OWN trend crossed
 * the leak thresholds gets its own amber row, next to (or instead of) the
 * aggregate banner. Same geometry as SoakTrendBanner: a caution, not a
 * failure. Renders nothing unless at least one label is flagged -- a
 * healthy-only label list is data, not a finding.
 */
export default function PerLabelSoakBanner({ trend }: { trend: SoakTrend }) {
  const leaking = (trend.labels ?? []).filter((l) => l.leak_suspected);
  if (leaking.length === 0) {
    return null;
  }
  return (
    <div className="space-y-2" data-testid="soak-label-leak-banner" role="status">
      {leaking.map((l) => (
        <p
          key={l.label}
          className="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-body-sm text-amber-800 dark:border-amber-800 dark:bg-amber-950/30 dark:text-amber-200"
          data-testid="soak-label-leak-row"
        >
          ⚠ Soak leak suspected — {l.label}: first {Math.round(l.first_half_ms)}ms → second{' '}
          {Math.round(l.second_half_ms)}ms (slope +{Math.round(l.slope_ms_per_min)}ms/min)
        </p>
      ))}
    </div>
  );
}
