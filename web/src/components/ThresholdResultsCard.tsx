// The scenario-threshold results block on a run's report (phase 72): how
// this run graded against its scenario's k6-style bounds. Strictly additive
// evidence — the engine's own outcome badge above is untouched. Met/missed
// is icon + text, never colour alone; a run whose report has not landed yet
// shows a skeleton (the results do not exist until the report does); a run
// whose scenario defines no thresholds hides the section entirely.
import Card, { CardContent, CardHeader, CardTitle } from './ui/Card';
import { describeThreshold } from './ThresholdEditor';
import type { ThresholdResult } from '../api/reports';

/** The observed figure in the metric's own unit, the way the threshold's
 * editor names it: milliseconds, a per-second rate, a 0..1 fraction. */
function formatObserved(result: ThresholdResult): string {
  if (result.observed_value === null) {
    return '—';
  }
  switch (result.metric) {
    case 'http_p95_ms':
    case 'http_p99_ms':
      return `${result.observed_value} ms`;
    case 'error_rate':
      return `${(result.observed_value * 100).toFixed(2)}%`;
    case 'throughput_qps':
      return `${result.observed_value} req/s`;
  }
}

interface ThresholdResultsCardProps {
  results: ThresholdResult[];
  /** True while the run's report has not landed (or the run has none):
   * the block shows a skeleton instead of pretending to know. */
  pending?: boolean;
}

/** The three states a reader can meet, in one component: pending
 * (skeleton), none (hidden), some (one row per threshold, definition
 * order). */
export default function ThresholdResultsCard({ results, pending = false }: ThresholdResultsCardProps) {
  if (pending) {
    return (
      <Card data-testid="scenario-thresholds-pending">
        <CardHeader>
          <CardTitle>Scenario thresholds</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="space-y-2">
            <div className="h-6 animate-pulse rounded bg-slate-100 dark:bg-slate-700/50" />
            <div className="h-6 w-2/3 animate-pulse rounded bg-slate-100 dark:bg-slate-700/50" />
          </div>
          <p className="mt-2 text-caption text-slate-500 dark:text-slate-400">
            Results appear when the run&apos;s report lands.
          </p>
        </CardContent>
      </Card>
    );
  }

  // No thresholds defined (or the run predates the feature): the section
  // does not exist, rather than announcing an empty verdict.
  if (results.length === 0) {
    return null;
  }

  return (
    <Card data-testid="scenario-thresholds-card">
      <CardHeader>
        <CardTitle>Scenario thresholds</CardTitle>
      </CardHeader>
      <CardContent>
        <ul className="space-y-1">
          {results.map((r, i) => (
            <li key={r.threshold_id} className="flex flex-wrap items-center gap-2" data-testid={`scenario-threshold-row-${i}`}>
              {r.satisfied === null ? (
                <>
                  <span role="img" aria-label="could not be evaluated" data-testid={`scenario-threshold-unknown-${i}`}>
                    ❓
                  </span>
                  <code className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-body-sm text-slate-800 dark:bg-slate-800 dark:text-slate-200">
                    {describeThreshold(r)}
                  </code>
                  <span className="text-caption text-slate-500 dark:text-slate-400">
                    unknown{r.reason ? ` — ${r.reason}` : ''}
                  </span>
                </>
              ) : r.satisfied ? (
                <>
                  <span role="img" aria-label="met" data-testid={`scenario-threshold-met-${i}`}>
                    ✅
                  </span>
                  <code className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-body-sm text-slate-800 dark:bg-slate-800 dark:text-slate-200">
                    {describeThreshold(r)}
                  </code>
                  <span className="text-body-sm text-slate-900 dark:text-white">met</span>
                  <span className="text-caption text-slate-500 dark:text-slate-400">
                    observed {formatObserved(r)}
                  </span>
                </>
              ) : (
                <>
                  <span role="img" aria-label="missed" data-testid={`scenario-threshold-missed-${i}`}>
                    ❌
                  </span>
                  <code className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-body-sm text-slate-800 dark:bg-slate-800 dark:text-slate-200">
                    {describeThreshold(r)}
                  </code>
                  <span className="text-body-sm font-medium text-slate-900 dark:text-white">missed</span>
                  <span className="text-caption text-slate-500 dark:text-slate-400">
                    observed {formatObserved(r)}
                  </span>
                </>
              )}
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  );
}
