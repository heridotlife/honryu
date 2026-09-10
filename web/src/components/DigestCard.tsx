import { useEffect, useState } from 'react';
import Card, { CardContent, CardHeader, CardTitle } from './ui/Card';
import { ApiError } from '../api/client';
import {
  deleteDigestConfig,
  getDigestConfig,
  listDigests,
  setDigestConfig,
  type DigestConfig,
  type DigestPeriod,
  type DigestRow,
} from '../api/digests';

export interface DigestCardProps {
  /** The project whose periodic report digest this card administers. */
  projectId: number;
}

/** Outcome chips are coloured by what a reader acts on: failures loudest. */
const outcomeChip = (label: string, count: number): React.JSX.Element | null =>
  count > 0 ? (
    <span
      key={label}
      data-testid={`digest-chip-${label}`}
      className={`rounded-full px-2 py-0.5 text-caption font-medium ${
        label === 'failed'
          ? 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300'
          : label === 'aborted'
            ? 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
            : 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300'
      }`}
    >
      {label} {count}
    </span>
  ) : null;

/**
 * The project's periodic report digest (phase 42): a period toggle
 * (daily/weekly/off), the last-fired caption, and the recent digests feed.
 * "Off" is a DELETE, not a disabled row -- the backend keeps no schedule
 * row to re-examine, and this card reads its absence (404) as the off
 * state. Mounted on the Executions page next to WebhooksCard, behind the
 * same project-update gate the backend's PUT/DELETE demand.
 */
export default function DigestCard({ projectId }: DigestCardProps) {
  const [config, setConfig] = useState<DigestConfig | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [rows, setRows] = useState<DigestRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let alive = true;
    setConfig(null);
    setLoaded(false);
    setRows(null);
    setError(null);
    Promise.all([getDigestConfig(projectId), listDigests(projectId)])
      .then(([cfg, feed]) => {
        if (!alive) return;
        setConfig(cfg);
        setRows(feed);
      })
      .catch((err: unknown) => {
        if (alive) setError(err instanceof ApiError ? err.message : 'failed to load digests');
      })
      .finally(() => {
        if (alive) setLoaded(true);
      });
    return () => {
      alive = false;
    };
  }, [projectId]);

  const msg = (err: unknown, fallback: string): string => (err instanceof ApiError ? err.message : fallback);

  async function choosePeriod(period: DigestPeriod | 'off'): Promise<void> {
    if (busy) return;
    setError(null);
    setBusy(true);
    try {
      if (period === 'off') {
        await deleteDigestConfig(projectId);
        setConfig(null);
      } else {
        setConfig(await setDigestConfig(projectId, period));
      }
    } catch (err: unknown) {
      setError(msg(err, 'failed to update digest schedule'));
    } finally {
      setBusy(false);
    }
  }

  const active = config !== null && config.enabled ? config.period : 'off';
  const lastFired =
    config?.last_fired !== undefined ? ` · last fired ${new Date(config.last_fired).toLocaleString()}` : '';

  return (
    <Card padding="none" data-testid="digest-card">
      <CardHeader className="px-4 pt-4 sm:px-6 sm:pt-6">
        <CardTitle>Report digests</CardTitle>
        <p className="text-caption mt-1 text-slate-500 dark:text-slate-400">
          Aggregates every completed run in a window and delivers a report.digest event to the project&apos;s
          webhooks.{lastFired}
        </p>
      </CardHeader>
      <CardContent>
        {error && (
          <p className="text-body-sm px-4 pb-2 text-red-600 dark:text-red-400 sm:px-6" role="alert">
            {error}
          </p>
        )}
        {/* The period toggle: three states, one of which is off. Radio-like
            buttons rather than a switch, because off is a third state, not
            the opposite of either period. */}
        <div
          className="flex flex-wrap items-center gap-2 px-4 py-3 sm:px-6"
          role="group"
          aria-label="Digest period"
          data-testid="digest-period-toggle"
        >
          {(['daily', 'weekly'] as const).map(period => (
            <button
              key={period}
              type="button"
              data-testid={`digest-period-toggle-${period}`}
              aria-pressed={active === period}
              disabled={busy}
              onClick={() => void choosePeriod(period)}
              className={`rounded-full px-3 py-1 text-caption font-medium transition-colors ${
                active === period
                  ? 'bg-sky-600 text-white'
                  : 'bg-slate-100 text-slate-700 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700'
              }`}
            >
              {period}
            </button>
          ))}
          <button
            type="button"
            data-testid="digest-toggle-off"
            aria-pressed={active === 'off'}
            disabled={busy}
            onClick={() => void choosePeriod('off')}
            className={`rounded-full px-3 py-1 text-caption font-medium transition-colors ${
              active === 'off'
                ? 'bg-slate-700 text-white dark:bg-slate-600'
                : 'bg-slate-100 text-slate-700 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700'
            }`}
          >
            off
          </button>
        </div>
        {!loaded ? (
          <p className="text-body-sm p-4 text-slate-500 dark:text-slate-400 sm:px-6">Loading digests…</p>
        ) : rows === null || rows.length === 0 ? (
          <p className="text-body-sm p-4 text-slate-500 dark:text-slate-400 sm:px-6">
            No digests fired yet{active === 'off' ? '' : ' — the first arrives after the next window closes'}.
          </p>
        ) : (
          <ul className="divide-y divide-slate-200 dark:divide-slate-700">
            {rows.map((row, i) => (
              <li key={row.id} data-testid={`digest-row-${i}`} className="p-4 sm:px-6">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <p className="text-body-sm font-medium text-slate-900 dark:text-slate-100">
                    {new Date(row.window_start).toLocaleDateString()} →{' '}
                    {new Date(row.window_end).toLocaleDateString()}
                  </p>
                  <p className="text-caption text-slate-500 dark:text-slate-400">
                    {row.runs_total} run{row.runs_total === 1 ? '' : 's'}
                    {row.threshold_failures > 0
                      ? ` · ${row.threshold_failures} threshold failure${row.threshold_failures === 1 ? '' : 's'}`
                      : ''}
                  </p>
                </div>
                <div className="mt-1 flex flex-wrap items-center gap-1.5">
                  {outcomeChip('passed', row.by_outcome.passed)}
                  {outcomeChip('failed', row.by_outcome.failed)}
                  {outcomeChip('aborted', row.by_outcome.aborted)}
                  {row.by_outcome.passed + row.by_outcome.failed + row.by_outcome.aborted === 0 &&
                    row.runs_total > 0 && (
                      <span className="text-caption text-slate-500 dark:text-slate-400">
                        {row.runs_total} engine-error run{row.runs_total === 1 ? '' : 's'}
                      </span>
                    )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}
