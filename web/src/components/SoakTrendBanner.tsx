import type { SoakTrend } from '../api/reports';

/**
 * The phase-99 degradation banner: a run whose response time trended
 * upward across its soak window reads as a suspected resource leak at
 * steady load, and the row that carries the run says so where a reader
 * cannot miss it. Amber, the calibration-pending banner's geometry
 * (Scenario's pending notice): a caution, not a failure -- the run's own
 * outcome is unchanged, and the finding is advisory until a human reads
 * it. Rendered only when leak_suspected fired; the halves and slope ride
 * along so the claim is checkable at a glance.
 */
export default function SoakTrendBanner({ trend }: { trend: SoakTrend }) {
  return (
    <p
      className="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-body-sm text-amber-800 dark:border-amber-800 dark:bg-amber-950/30 dark:text-amber-200"
      data-testid="soak-leak-banner"
      role="status"
    >
      Response time degraded across the soak window ({Math.round(trend.first_half_ms)}ms →{' '}
      {Math.round(trend.second_half_ms)}ms, +{Math.round(trend.slope_ms_per_min)}ms/min) — consistent with a
      resource leak at steady load.
    </p>
  );
}
