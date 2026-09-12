// Run comparison: two runs of one execution side by side, a signed-percent
// delta table (improvement green, regression red), and an overlaid p95
// chart. Route /executions/:id/compare?runs=a,b; the run list comes from
// the same reports endpoint the Reports page uses (most recent first), and
// each run's per-second shape from the series endpoint (task 5).
//
// Delta semantics: A is the baseline, B the candidate, and every delta
// reads "B against A" -- negative latency is an improvement, positive
// error rate a regression, positive RPS an improvement.
import { useEffect, useId, useState } from 'react';
import { useParams, useSearchParams } from 'react-router-dom';
import Card, { CardContent, CardHeader, CardTitle } from '../components/ui/Card';
import TimeSeriesChart from '../components/charts/TimeSeriesChart';
import { ApiError } from '../api/client';
import { listExecutionReports } from '../api/reports';
import type { Report } from '../api/reports';
import { fetchSeries } from '../api/series';
import type { SeriesPoint } from '../api/series';

/**
 * Signed percent delta from A to B: (B - A) / A * 100. Null when A is 0 or
 * either side is not a finite number -- percent change from nothing is
 * undefined, not infinite, so the caller renders an em-dash instead.
 */
export function pctDelta(a: number, b: number): number | null {
  if (!Number.isFinite(a) || !Number.isFinite(b) || a === 0) {
    return null;
  }
  return ((b - a) / a) * 100;
}

export type DeltaKind = 'improvement' | 'regression' | 'neutral' | 'none';

/**
 * Which way a delta reads: `lower` metrics (latency, error rate) improve
 * when the delta is negative, `higher` metrics (RPS) when it is positive.
 * A null delta (unmeasured on one side, or divide-by-zero) is 'none'.
 */
export function deltaKind(better: 'lower' | 'higher', delta: number | null): DeltaKind {
  if (delta === null) {
    return 'none';
  }
  if (delta === 0) {
    return 'neutral';
  }
  const bIsBetter = better === 'lower' ? delta < 0 : delta > 0;
  return bIsBetter ? 'improvement' : 'regression';
}

/** Tone classes for a delta cell; neutral/none stay muted slate. */
export function deltaClass(kind: DeltaKind): string {
  switch (kind) {
    case 'improvement':
      return 'text-emerald-600 dark:text-emerald-400';
    case 'regression':
      return 'text-red-600 dark:text-red-400';
    default:
      return 'text-slate-500 dark:text-slate-400';
  }
}

/** Signed one-decimal percent, e.g. +15.8% / -20.0%; em-dash for null. */
export function formatDelta(delta: number | null): string {
  if (delta === null) {
    return '—';
  }
  return `${delta > 0 ? '+' : ''}${delta.toFixed(1)}%`;
}

function formatTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

/** Fraction (the wire's convention) -> percent; latency seconds -> ms. */
function formatErrorRate(fraction: number): string {
  return `${(fraction * 100).toFixed(2)}%`;
}

function formatMs(seconds: number): string {
  return `${(seconds * 1000).toFixed(1)} ms`;
}

interface MetricSpec {
  key: string;
  label: string;
  better: 'lower' | 'higher';
  /** The metric's value off a report; undefined = that run did not measure it. */
  value: (r: Report) => number | undefined;
  format: (v: number) => string;
}

/** The delta table's rows, in display order. RPS is the achieved figure. */
const METRICS: MetricSpec[] = [
  { key: 'p50', label: 'p50', better: 'lower', value: (r) => r.latency?.['50'], format: formatMs },
  { key: 'p95', label: 'p95', better: 'lower', value: (r) => r.latency?.['95'], format: formatMs },
  { key: 'p99', label: 'p99', better: 'lower', value: (r) => r.latency?.['99'], format: formatMs },
  {
    key: 'rps',
    label: 'RPS',
    better: 'higher',
    value: (r) => r.achieved?.throughput,
    format: (v) => `${v.toFixed(1)} req/s`,
  },
  { key: 'errorRate', label: 'Error rate', better: 'lower', value: (r) => r.error_rate, format: formatErrorRate },
];

function RunSelect({
  label,
  testId,
  value,
  reports,
  onChange,
}: {
  label: string;
  testId: string;
  value: number | null;
  reports: Report[];
  onChange: (runId: number | null) => void;
}) {
  const selectId = useId();
  return (
    <div>
      <label htmlFor={selectId} className="mb-2 block text-sm font-medium text-slate-700 dark:text-slate-300">
        {label}
      </label>
      <select
        id={selectId}
        data-testid={testId}
        value={value ?? ''}
        onChange={(e) => onChange(e.target.value === '' ? null : Number(e.target.value))}
        className="block w-full min-h-[44px] rounded-lg border border-slate-300 bg-white px-3 py-2 text-base text-slate-900 transition-colors focus:border-sky-500 focus:ring-2 focus:ring-sky-500 focus:outline-none dark:border-slate-700 dark:bg-slate-900 dark:text-white"
      >
        <option value="">Select a run…</option>
        {reports.map((r) => (
          <option key={r.run_id} value={r.run_id}>
            Run #{r.run_id} · {formatTime(r.started_at)}
          </option>
        ))}
      </select>
    </div>
  );
}

function DeltaTable({ a, b }: { a: Report; b: Report }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          Delta — run #{b.run_id} against run #{a.run_id}
        </CardTitle>
      </CardHeader>
      <CardContent>
        <div className="overflow-x-auto" data-testid="delta-table">
          <table className="w-full text-left text-body-sm">
            <thead>
              <tr className="text-caption border-b border-slate-200 text-slate-500 dark:border-slate-700 dark:text-slate-400">
                <th scope="col" className="px-3 py-2 font-medium">
                  Metric
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  Run #{a.run_id}
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  Run #{b.run_id}
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  Delta
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {METRICS.map((m) => {
                const va = m.value(a);
                const vb = m.value(b);
                const delta = va !== undefined && vb !== undefined ? pctDelta(va, vb) : null;
                const kind = deltaKind(m.better, delta);
                return (
                  <tr key={m.key} data-metric={m.key}>
                    <td className="px-3 py-2 font-medium whitespace-nowrap text-slate-900 dark:text-white">{m.label}</td>
                    <td className="px-3 py-2 whitespace-nowrap">{va !== undefined ? m.format(va) : '—'}</td>
                    <td className="px-3 py-2 whitespace-nowrap">{vb !== undefined ? m.format(vb) : '—'}</td>
                    <td className={`px-3 py-2 font-medium whitespace-nowrap ${deltaClass(kind)}`}>{formatDelta(delta)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </CardContent>
    </Card>
  );
}

/**
 * The overlaid p95 chart: one series per run, fetched together once both
 * selections exist. Hidden entirely when either run has no series (runs
 * finalised before the series store existed) or a fetch fails -- the delta
 * table stays the page's primary source either way.
 */
function CompareChart({ runIdA, runIdB }: { runIdA: number; runIdB: number }) {
  const [state, setState] = useState<
    { kind: 'loading' } | { kind: 'ready'; a: SeriesPoint[]; b: SeriesPoint[] } | { kind: 'hidden' }
  >({ kind: 'loading' });

  useEffect(() => {
    let cancelled = false;
    setState({ kind: 'loading' });
    Promise.all([fetchSeries(runIdA), fetchSeries(runIdB)])
      .then(([sa, sb]) => {
        if (cancelled) {
          return;
        }
        if (sa.points.length === 0 || sb.points.length === 0) {
          setState({ kind: 'hidden' });
        } else {
          setState({ kind: 'ready', a: sa.points, b: sb.points });
        }
      })
      .catch(() => {
        if (!cancelled) {
          setState({ kind: 'hidden' });
        }
      });
    return () => {
      cancelled = true;
    };
  }, [runIdA, runIdB]);

  if (state.kind !== 'ready') {
    return null;
  }
  // The wire carries seconds (keyed '95'); the chart reads milliseconds.
  const p95 = (points: SeriesPoint[]) =>
    points
      .filter((p) => p.latency?.['95'] !== undefined)
      .map((p) => ({ x: p.ts, y: (p.latency as Record<string, number>)['95'] * 1000 }));

  return (
    <Card>
      <CardHeader>
        <CardTitle>p95 overlay</CardTitle>
      </CardHeader>
      <CardContent>
        <div data-testid="chart-compare-p95">
          <TimeSeriesChart
            xType="time"
            yLabel="ms"
            series={[
              { name: `run #${runIdA} p95`, color: 'text-sky-500', points: p95(state.a) },
              { name: `run #${runIdB} p95`, color: 'text-amber-500', points: p95(state.b) },
            ]}
          />
        </div>
      </CardContent>
    </Card>
  );
}

export default function RunCompare() {
  const { id } = useParams<{ id: string }>();
  const [searchParams] = useSearchParams();
  const executionId = Number(id);
  const validId = Number.isInteger(executionId) && executionId > 0;

  const [reports, setReports] = useState<Report[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [runA, setRunA] = useState<number | null>(null);
  const [runB, setRunB] = useState<number | null>(null);

  useEffect(() => {
    if (!validId) {
      setError('Invalid execution id.');
      setReports(null);
      return;
    }
    let cancelled = false;
    setReports(null);
    setError(null);
    setRunA(null);
    setRunB(null);
    listExecutionReports(executionId)
      .then((rows) => {
        if (cancelled) {
          return;
        }
        setReports(rows);
        // Preselect: ?runs=a,b wins when BOTH ids belong to this execution;
        // otherwise A = the oldest run (baseline) and B = the newest
        // (candidate) -- the list arrives most-recent-first.
        const ids = rows.map((r) => r.run_id);
        const wanted = (searchParams.get('runs') ?? '')
          .split(',')
          .map((part) => Number(part.trim()))
          .filter((n) => Number.isInteger(n) && n > 0);
        const fromQuery = wanted.length >= 2 && ids.includes(wanted[0]) && ids.includes(wanted[1]);
        setRunA(fromQuery ? wanted[0] : ids[ids.length - 1] ?? null);
        setRunB(fromQuery ? wanted[1] : ids[0] ?? null);
      })
      .catch((err: unknown) => {
        if (cancelled) {
          return;
        }
        setError(err instanceof ApiError ? err.message : 'Failed to load runs.');
      });
    return () => {
      cancelled = true;
    };
  }, [executionId, validId, searchParams]);

  const reportA = reports?.find((r) => r.run_id === runA) ?? null;
  const reportB = reports?.find((r) => r.run_id === runB) ?? null;
  const sameRun = runA !== null && runA === runB;

  return (
    <div className="space-y-6" role="region" aria-label="Run comparison panel">
      <div>
        <h1 className="text-display-sm text-slate-900 dark:text-white">Compare runs</h1>
        <p className="text-body-sm mt-1 text-slate-500 dark:text-slate-400">
          Execution #{executionId} — one run against another, metric by metric.
        </p>
      </div>

      {error && (
        <p className="text-sm text-red-600 dark:text-red-400" role="alert">
          {error}
        </p>
      )}

      {validId && !error && reports === null && (
        <p className="text-body-sm text-slate-500 dark:text-slate-400" data-testid="compare-loading">
          Loading runs…
        </p>
      )}

      {validId && !error && reports !== null && reports.length === 0 && (
        <p className="text-body-sm text-slate-500 dark:text-slate-400" data-testid="compare-no-runs">
          No reports for this execution yet — runs appear here once they finalise.
        </p>
      )}

      {validId && !error && reports !== null && reports.length > 0 && (
        <>
          <Card>
            <CardHeader>
              <CardTitle>Runs</CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                <RunSelect label="Run A (baseline)" testId="select-run-a" value={runA} reports={reports} onChange={setRunA} />
                <RunSelect label="Run B (candidate)" testId="select-run-b" value={runB} reports={reports} onChange={setRunB} />
              </div>
              {sameRun && (
                <p className="text-body-sm text-amber-600 dark:text-amber-400" data-testid="compare-same-run">
                  Both selectors point at the same run — pick two different runs to compare.
                </p>
              )}
            </CardContent>
          </Card>
          {reportA !== null && reportB !== null && !sameRun && (
            <>
              <DeltaTable a={reportA} b={reportB} />
              <CompareChart runIdA={reportA.run_id} runIdB={reportB.run_id} />
            </>
          )}
        </>
      )}
    </div>
  );
}
