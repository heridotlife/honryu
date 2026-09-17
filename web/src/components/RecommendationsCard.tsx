// The run workspace's Recommendations card (phase 78): the k6-style
// advisories the backend's rule engine reads off the run's report, fetched
// per run like the time series. Four states, never blurred: loading
// (skeleton), error (retry), empty (the shared EmptyState -- a clean run is
// a result, not an absence), and the list itself. Severity renders icon +
// visible text together, never colour alone.
import { useEffect, useState } from 'react';
import { AlertTriangle, Info, Lightbulb, RefreshCw } from 'lucide-react';
import Card, { CardContent, CardHeader, CardTitle } from './ui/Card';
import Button from './ui/Button';
import EmptyState from './EmptyState';
import { ApiError } from '../api/client';
import { getRunRecommendations, type Recommendation } from '../api/reports';

/** One severity's fixed rendering: icon + text label travel together so the
 * verdict never rides on colour alone. */
const SEVERITIES: Record<Recommendation['severity'], { label: string; icon: typeof Info; class: string }> = {
  warning: { label: 'Warning', icon: AlertTriangle, class: 'text-amber-600 dark:text-amber-400' },
  info: { label: 'Info', icon: Info, class: 'text-sky-600 dark:text-sky-400' },
};

/** The card's own fetch-state machine -- the Time series section's shape. */
type RecsState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; recommendations: Recommendation[] };

export default function RecommendationsCard({ runId }: { runId: number }) {
  const [state, setState] = useState<RecsState>({ kind: 'loading' });
  const [retry, setRetry] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setState({ kind: 'loading' });
    getRunRecommendations(runId)
      .then((recommendations) => {
        if (cancelled) {
          return;
        }
        setState({ kind: 'ready', recommendations });
      })
      .catch((err: unknown) => {
        if (cancelled) {
          return;
        }
        setState({ kind: 'error', message: err instanceof ApiError ? err.message : 'Failed to load recommendations.' });
      });
    return () => {
      cancelled = true;
    };
  }, [runId, retry]);

  return (
    <Card data-testid="recs-card">
      <CardHeader>
        <CardTitle>Recommendations</CardTitle>
      </CardHeader>
      <CardContent>
        {state.kind === 'loading' && (
          <div className="space-y-2" data-testid="recs-loading">
            {[0, 1].map((i) => (
              <div key={i} className="h-12 animate-pulse rounded-lg bg-slate-100 dark:bg-slate-700/50" />
            ))}
          </div>
        )}
        {state.kind === 'error' && (
          <div className="flex flex-wrap items-center gap-3">
            <p className="text-sm text-red-600 dark:text-red-400" role="alert">
              {state.message}
            </p>
            <Button variant="secondary" size="sm" data-testid="recs-retry" onClick={() => setRetry((n) => n + 1)}>
              <RefreshCw aria-hidden className="mr-1 h-3.5 w-3.5" />
              Retry
            </Button>
          </div>
        )}
        {state.kind === 'ready' && state.recommendations.length === 0 && (
          <EmptyState
            testId="recs-empty"
            icon={<Lightbulb className="size-6" />}
            title="No recommendations — clean run"
            description="Nothing in this run's telemetry called for attention. Thresholds, error rate, latency spread, and throughput all read healthy."
          />
        )}
        {state.kind === 'ready' && state.recommendations.length > 0 && (
          <ul className="space-y-3" data-testid="recs-list">
            {state.recommendations.map((rec, i) => {
              const sev = SEVERITIES[rec.severity] ?? SEVERITIES.info;
              const Icon = sev.icon;
              return (
                <li
                  key={rec.id}
                  data-testid={`rec-row-${i}`}
                  className="flex gap-3 rounded-lg border border-slate-200 p-3 dark:border-slate-700"
                >
                  <div className={`flex shrink-0 items-start gap-1.5 font-medium ${sev.class}`}>
                    <Icon aria-hidden className="mt-0.5 h-4 w-4" />
                    <span className="text-caption" data-testid={`rec-severity-${i}`}>
                      {sev.label}
                    </span>
                  </div>
                  <div className="min-w-0">
                    <p className="text-body-sm font-medium text-slate-900 dark:text-white" data-testid={`rec-title-${i}`}>
                      {rec.title}
                    </p>
                    <p className="text-body-sm text-slate-600 dark:text-slate-300">{rec.detail}</p>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}
