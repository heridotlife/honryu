// Run comparison: one execution's runs side by side — two by default, N on
// request — a signed-percent delta table (improvement green, regression
// red) with a chip per candidate column that regressed, and an overlaid p95
// chart. Route /executions/:id/compare?runs=a,b[,c,…]; the run menu comes
// from the same reports endpoint the Reports page uses (most recent first).
//
// Data source (phase 61): the selected runs' summaries come from
// GET /api/runs/compare (run_ids[] in request order, first id = baseline),
// each element exactly the single-run report shape. While that fetch is in
// flight — or if it fails — the table renders from the already-loaded
// reports list instead, which carries the same fields: the comparison must
// not blink just because the batch endpoint did.
//
// Delta semantics: the first slot is the baseline, every later slot a
// candidate, and every delta reads "candidate against baseline" — negative
// latency is an improvement, positive error rate a regression, positive RPS
// an improvement.
import { Fragment, useEffect, useId, useState } from 'react';
import { useParams, useSearchParams } from 'react-router-dom';
import Breadcrumbs from '../components/Breadcrumbs';
import Card, { CardContent, CardHeader, CardTitle } from '../components/ui/Card';
import TimeSeriesChart from '../components/charts/TimeSeriesChart';
import { ApiError } from '../api/client';
import { compareRuns, listExecutionReports } from '../api/reports';
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

/**
 * The delta table's rows, in display order. RPS is the achieved figure.
 */
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

/**
 * Metrics whose candidate-vs-baseline delta reads as a regression, in table
 * order. Phase 59: feeds the per-column verdict chips — the per-metric cells
 * already say which direction each number moved; the chip is the one-line
 * verdict over all of them, from the same deltas the table itself renders.
 */
export function summarizeRegressions(a: Report, b: Report): { metric: string; delta: number }[] {
  return METRICS.flatMap((m) => {
    const va = m.value(a);
    const vb = m.value(b);
    const delta = va !== undefined && vb !== undefined ? pctDelta(va, vb) : null;
    return deltaKind(m.better, delta) === 'regression' && delta !== null ? [{ metric: m.label, delta }] : [];
  });
}

/**
 * The selection slot's letter: A is the baseline, B the first candidate, C…
 * onward. Past Z the labels keep counting (AA, AB) — an honest label beats
 * a hard cap the server side already enforces.
 */
export function slotLetter(index: number): string {
  let n = index;
  let out = '';
  do {
    out = String.fromCharCode(65 + (n % 26)) + out;
    n = Math.floor(n / 26) - 1;
  } while (n >= 0);
  return out;
}

/** Slot label: the two leading roles keep their long-standing names. */
function slotLabel(index: number): string {
  if (index === 0) {
    return 'Run A (baseline)';
  }
  if (index === 1) {
    return 'Run B (candidate)';
  }
  return `Run ${slotLetter(index)}`;
}

/** Selector test ids: select-run-a/b are the long-standing two; C onward
 * extends the same lowercase pattern. */
function slotTestId(index: number): string {
  return `select-run-${slotLetter(index).toLowerCase()}`;
}

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

function DeltaTable({ baseline, candidates }: { baseline: Report; candidates: Report[] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          Delta — {candidates.length === 1 ? `run #${candidates[0].run_id}` : `${candidates.length} runs`} against run #
          {baseline.run_id}
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
                  Run #{baseline.run_id}
                </th>
                {/* One value/delta column pair per candidate, in slot order.
                    The verdict chip rides the candidate's own column header
                    (phase 61): per column, exactly where the numbers it
                    summarizes sit. Only-when-regressed -- a clean comparison
                    gets no chip, the row cells already color the individual
                    movements. Real text, not a color-only cue. */}
                {candidates.map((c) => {
                  const regressions = summarizeRegressions(baseline, c);
                  return (
                    <Fragment key={c.run_id}>
                      <th scope="col" data-run-id={c.run_id} className="px-3 py-2 font-medium">
                        <span className="inline-flex items-center gap-2">
                          Run #{c.run_id}
                          {regressions.length > 0 && (
                            <span
                              data-testid="compare-regression-chip"
                              data-run-id={c.run_id}
                              title={`${regressions.map((r) => `${r.metric} ${formatDelta(r.delta)}`).join(' · ')} vs run #${baseline.run_id}`}
                              className="inline-flex items-center rounded-full bg-red-100 px-2.5 py-0.5 text-xs font-medium text-red-800 dark:bg-red-900/30 dark:text-red-300"
                            >
                              regressed
                            </span>
                          )}
                        </span>
                      </th>
                      <th scope="col" className="px-3 py-2 font-medium">
                        Δ vs #{baseline.run_id}
                      </th>
                    </Fragment>
                  );
                })}
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {METRICS.map((m) => {
                const vBase = m.value(baseline);
                return (
                  <tr key={m.key} data-metric={m.key}>
                    <td className="px-3 py-2 font-medium whitespace-nowrap text-slate-900 dark:text-white">{m.label}</td>
                    <td className="px-3 py-2 whitespace-nowrap">{vBase !== undefined ? m.format(vBase) : '—'}</td>
                    {candidates.map((c) => {
                      const vCand = m.value(c);
                      const delta = vBase !== undefined && vCand !== undefined ? pctDelta(vBase, vCand) : null;
                      const kind = deltaKind(m.better, delta);
                      return (
                        <Fragment key={c.run_id}>
                          <td className="px-3 py-2 whitespace-nowrap">{vCand !== undefined ? m.format(vCand) : '—'}</td>
                          <td className={`px-3 py-2 font-medium whitespace-nowrap ${deltaClass(kind)}`}>
                            {formatDelta(delta)}
                          </td>
                        </Fragment>
                      );
                    })}
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

/** Chart series colors, cycled per candidate slot; the first two keep the
 * long-standing sky/amber pair. */
const CHART_COLORS = [
  'text-sky-500',
  'text-amber-500',
  'text-emerald-500',
  'text-violet-500',
  'text-rose-500',
  'text-cyan-500',
];

/**
 * The overlaid p95 chart: one series per selected run, fetched together
 * once the selection stands. Hidden entirely when any run has no series
 * (runs finalised before the series store existed) or a fetch fails -- the
 * delta table stays the page's primary source either way.
 */
function CompareChart({ runIds }: { runIds: number[] }) {
  const [state, setState] = useState<
    { kind: 'loading' } | { kind: 'ready'; series: SeriesPoint[][] } | { kind: 'hidden' }
  >({ kind: 'loading' });
  const selectionKey = runIds.join(',');

  useEffect(() => {
    // Re-read through the key: the effect re-runs exactly when the joined
    // selection changes, and runIds is captured fresh on each such run.
    const ids = selectionKey.split(',').map(Number);
    let cancelled = false;
    setState({ kind: 'loading' });
    Promise.all(ids.map((rid) => fetchSeries(rid)))
      .then((all) => {
        if (cancelled) {
          return;
        }
        if (all.some((s) => s.points.length === 0)) {
          setState({ kind: 'hidden' });
        } else {
          setState({ kind: 'ready', series: all.map((s) => s.points) });
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
  }, [selectionKey]);

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
            series={runIds.map((rid, i) => ({
              name: `run #${rid} p95`,
              color: CHART_COLORS[i % CHART_COLORS.length],
              points: p95(state.series[i]),
            }))}
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
  // The selection, in slot order: index 0 is the baseline, the rest are
  // candidates. Two slots by default; "Add run" extends it.
  const [selected, setSelected] = useState<number[]>([]);
  // The selected runs' summaries off GET /api/runs/compare, request order.
  // Null while the fetch is in flight or after it failed -- the render then
  // falls back to the already-loaded reports list, same fields.
  const [summaries, setSummaries] = useState<Report[] | null>(null);

  useEffect(() => {
    if (!validId) {
      setError('Invalid execution id.');
      setReports(null);
      setSelected([]);
      return;
    }
    let cancelled = false;
    setReports(null);
    setError(null);
    setSelected([]);
    listExecutionReports(executionId)
      .then((rows) => {
        if (cancelled) {
          return;
        }
        setReports(rows);
        // Preselect: ?runs=a,b,c… wins when EVERY id belongs to this
        // execution (two is the smallest ask -- the long-standing
        // ?runs=a,b spelling); otherwise slot A = the oldest run (baseline)
        // and slot B = the newest (candidate) -- the list arrives
        // most-recent-first.
        const ids = rows.map((r) => r.run_id);
        const wanted = (searchParams.get('runs') ?? '')
          .split(',')
          .map((part) => Number(part.trim()))
          .filter((n) => Number.isInteger(n) && n > 0);
        const fromQuery = wanted.length >= 2 && wanted.every((n) => ids.includes(n));
        setSelected(fromQuery ? wanted : ids.length > 0 ? [ids[ids.length - 1], ids[0]] : []);
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

  // Fetch the selection's summaries off the batch endpoint. Keyed on the
  // joined selection: the effect re-runs exactly when the runs compared
  // change, never on the array's render-time identity.
  const selectionKey = selected.join(',');
  useEffect(() => {
    const ids = selectionKey === '' ? [] : selectionKey.split(',').map(Number);
    if (ids.length < 2) {
      setSummaries(null);
      return;
    }
    let cancelled = false;
    compareRuns(ids)
      .then((rows) => {
        if (!cancelled) {
          setSummaries(rows);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setSummaries(null); // fall back to the reports list below
        }
      });
    return () => {
      cancelled = true;
    };
  }, [selectionKey]);

  const sameRun = new Set(selected).size !== selected.length;
  const pool = summaries ?? reports;
  const byId = new Map((pool ?? []).map((r) => [r.run_id, r] as const));
  const selectedReports = selected.map((sid) => byId.get(sid) ?? null);
  const ready = !sameRun && selectedReports.length >= 2 && selectedReports.every((r) => r !== null);
  const baseline = ready ? selectedReports[0] : null;
  const candidates = ready ? (selectedReports.slice(1) as Report[]) : [];

  const addRun = () => {
    if (reports === null) {
      return;
    }
    // Newest run not already in a slot (the list is most-recent-first).
    const next = reports.find((r) => !selected.includes(r.run_id));
    if (next !== undefined) {
      setSelected((prev) => [...prev, next.run_id]);
    }
  };
  const removeRun = (index: number) => setSelected((prev) => prev.filter((_, i) => i !== index));

  return (
    <div className="space-y-6" role="region" aria-label="Run comparison panel">
      {/* Phase 52: the trail back up -- execution hub AND the executions
          list, since compare is two levels deep. */}
      <Breadcrumbs
        items={[
          { label: 'Executions', href: '/executions' },
          { label: `#${executionId}`, href: `/executions/${executionId}` },
          { label: 'Compare' },
        ]}
      />
      <div>
        <h1 className="text-display-sm text-slate-900 dark:text-white">Compare runs</h1>
        <p className="text-body-sm mt-1 text-slate-500 dark:text-slate-400">
          Execution #{executionId} — two or more runs against one baseline, metric by metric.
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
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
                {selected.map((runId, i) => (
                  <div key={i} className="flex items-end gap-2">
                    <div className="min-w-0 grow">
                      <RunSelect
                        label={slotLabel(i)}
                        testId={slotTestId(i)}
                        value={runId}
                        reports={reports}
                        onChange={(rid) => setSelected((prev) => prev.map((v, j) => (j === i ? (rid ?? v) : v)))}
                      />
                    </div>
                    {selected.length > 2 && (
                      <button
                        type="button"
                        data-testid={`remove-run-${i}`}
                        aria-label={`Remove slot ${slotLetter(i)}`}
                        onClick={() => removeRun(i)}
                        className="min-h-[44px] shrink-0 rounded-lg border border-slate-300 px-3 text-slate-500 transition-colors hover:bg-slate-100 focus:ring-2 focus:ring-sky-500 focus:outline-none dark:border-slate-700 dark:text-slate-400 dark:hover:bg-slate-800"
                      >
                        ✕
                      </button>
                    )}
                  </div>
                ))}
              </div>
              {reports.length > selected.length && (
                <button
                  type="button"
                  data-testid="add-run"
                  onClick={addRun}
                  className="min-h-[44px] rounded-lg border border-slate-300 px-3 text-sm font-medium text-slate-700 transition-colors hover:bg-slate-100 focus:ring-2 focus:ring-sky-500 focus:outline-none dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
                >
                  + Add run
                </button>
              )}
              {sameRun && (
                <p className="text-body-sm text-amber-600 dark:text-amber-400" data-testid="compare-same-run">
                  Two slots point at the same run — pick a different run for each slot.
                </p>
              )}
            </CardContent>
          </Card>
          {baseline !== null && (
            <>
              <DeltaTable baseline={baseline} candidates={candidates} />
              <CompareChart runIds={selected.filter((sid) => byId.has(sid))} />
            </>
          )}
        </>
      )}
    </div>
  );
}
