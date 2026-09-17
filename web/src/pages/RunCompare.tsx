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
// an improvement. Phase 75: a movement within ±5% reads neutral (the same
// band colors the cells and badges the chips), each candidate carries an
// absolute AND a percent delta cell, both marked with a direction arrow
// (▲ value rose, ▼ it fell — direction, never judgment; color and sign
// carry better/worse), and the "vs baseline" mode swaps the A/B/C slot
// pickers for an explicit run list: one radio'd baseline, checkboxes for
// every run compared against it. Deltas are computed HERE, client-side, in
// both modes — the optional baseline_run_id param on the batch endpoint
// only orders the payload baseline-first.
import { Fragment, useEffect, useId, useState } from 'react';
import { useParams, useSearchParams } from 'react-router-dom';
import { GitCompare } from 'lucide-react';
import Breadcrumbs from '../components/Breadcrumbs';
import Card, { CardContent, CardHeader, CardTitle } from '../components/ui/Card';
import EmptyState from '../components/EmptyState';
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
 * The neutral band, in percent (phase 75): a delta this size or smaller is
 * "no real movement" — neither an improvement nor a regression. One band
 * for the whole page, so cell coloring and the regression chips can never
 * disagree about whether a ±4% wobble matters.
 */
export const NEUTRAL_BAND_PCT = 5;

/**
 * Which way a delta reads: `lower` metrics (latency, error rate) improve
 * when the delta is negative, `higher` metrics (RPS) when it is positive.
 * Within ±NEUTRAL_BAND_PCT the delta is 'neutral'; a null delta (unmeasured
 * on one side, or divide-by-zero) is 'none'.
 */
export function deltaKind(better: 'lower' | 'higher', delta: number | null): DeltaKind {
  if (delta === null) {
    return 'none';
  }
  if (Math.abs(delta) <= NEUTRAL_BAND_PCT) {
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

/**
 * Signed absolute delta in the metric's own unit, e.g. +40.0 ms (phase 75:
 * the abs half of each candidate's delta pair); em-dash for null.
 */
export function signedAbs(delta: number | null, format: (v: number) => string): string {
  if (delta === null) {
    return '—';
  }
  return `${delta > 0 ? '+' : ''}${format(delta)}`;
}

/**
 * Direction glyph for a delta (phase 75): ▲ the raw value rose, ▼ it fell,
 * '' it did not move (or the movement is unmeasurable). Direction, not
 * judgment — the color and the sign carry better/worse, so a cell is never
 * read by color alone.
 */
export function deltaArrow(delta: number | null): string {
  if (delta === null || delta === 0) {
    return '';
  }
  return delta > 0 ? '▲' : '▼';
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
        {/* Phase 79 decision: the delta table stays overflow-x-auto below
            sm -- side-by-side comparison is desktop tooling (the same call
            as Reports' compare tables), and the per-metric baseline/delta
            column pairing is the point; cards would flatten it. */}
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
                {/* One value/abs-delta/pct-delta column trio per candidate,
                    in slot order. The verdict chip rides the candidate's own
                    column header (phase 61): per column, exactly where the
                    numbers it summarizes sit. Only-when-regressed -- a clean
                    comparison gets no chip, the row cells already color the
                    individual movements. Real text, not a color-only cue. */}
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
                      <th scope="col" className="px-3 py-2 font-medium">Δ abs vs #{baseline.run_id}</th>
                      <th scope="col" className="px-3 py-2 font-medium">Δ % vs #{baseline.run_id}</th>
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
                      const pct = vBase !== undefined && vCand !== undefined ? pctDelta(vBase, vCand) : null;
                      const abs = vBase !== undefined && vCand !== undefined ? vCand - vBase : null;
                      // One classification per pair: the percent delta is
                      // the band's unit, so it decides both cells' tone;
                      // the abs cell repeats it only because the two always
                      // share a sign (and when the percent is undefined --
                      // a zero baseline -- the abs still shows, uncolored).
                      const cls = deltaClass(deltaKind(m.better, pct));
                      const pctArrow = deltaArrow(pct);
                      const absArrow = deltaArrow(abs);
                      return (
                        <Fragment key={c.run_id}>
                          <td className="px-3 py-2 whitespace-nowrap">{vCand !== undefined ? m.format(vCand) : '—'}</td>
                          <td data-delta="abs" data-run-id={c.run_id} className={`px-3 py-2 font-medium whitespace-nowrap ${cls}`}>
                            {absArrow && (
                              <span aria-hidden="true" className="mr-1">
                                {absArrow}{' '}
                              </span>
                            )}
                            {signedAbs(abs, m.format)}
                          </td>
                          <td data-delta="pct" data-run-id={c.run_id} className={`px-3 py-2 font-medium whitespace-nowrap ${cls}`}>
                            {pctArrow && (
                              <span aria-hidden="true" className="mr-1">
                                {pctArrow}{' '}
                              </span>
                            )}
                            {formatDelta(pct)}
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
  // Phase 75: two selection modes. "slots" is the long-standing A/B/C pick
  // list (index 0 is the baseline); "baseline" ("vs baseline") is an explicit
  // run list -- one radio'd baseline run plus checkboxes for every run
  // compared against it. The query's ?mode=vs-baseline lands in baseline
  // mode; the toggle is otherwise local state.
  const [mode, setMode] = useState<'slots' | 'baseline'>(() =>
    searchParams.get('mode') === 'vs-baseline' ? 'baseline' : 'slots'
  );
  // The slots-mode selection, in slot order: index 0 is the baseline, the
  // rest are candidates. Two slots by default; "Add run" extends it.
  const [selected, setSelected] = useState<number[]>([]);
  // The baseline-mode selection: one baseline run plus the runs compared
  // against it, in check order. Kept alongside `selected` so toggling modes
  // carries the comparison across instead of losing it.
  const [baselineId, setBaselineId] = useState<number | null>(null);
  const [picked, setPicked] = useState<number[]>([]);
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
        // most-recent-first. Both representations are seeded (slots AND
        // baseline/checkboxes) so the toggle works before any interaction;
        // in every spelling the first id is the baseline.
        const ids = rows.map((r) => r.run_id);
        const wanted = (searchParams.get('runs') ?? '')
          .split(',')
          .map((part) => Number(part.trim()))
          .filter((n) => Number.isInteger(n) && n > 0);
        const fromQuery = wanted.length >= 2 && wanted.every((n) => ids.includes(n));
        const sel = fromQuery ? wanted : ids.length > 0 ? [ids[ids.length - 1], ids[0]] : [];
        setSelected(sel);
        setBaselineId(sel.length > 0 ? sel[0] : null);
        setPicked(sel.slice(1));
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

  // The selection this mode compares, in wire order: baseline first. The
  // batch fetch, the fallback pool, the table and the chart all read it.
  const effective = mode === 'slots' ? selected : baselineId !== null ? [baselineId, ...picked] : [];

  // Fetch the selection's summaries off the batch endpoint. Keyed on the
  // joined selection: the effect re-runs exactly when the runs compared
  // change, never on the array's render-time identity. In baseline mode the
  // radio'd run rides along as baseline_run_id so the server orders the
  // payload baseline-first (ordering only -- the deltas are computed here).
  const selectionKey = effective.join(',');
  useEffect(() => {
    const ids = selectionKey === '' ? [] : selectionKey.split(',').map(Number);
    if (ids.length < 2) {
      setSummaries(null);
      return;
    }
    let cancelled = false;
    const baseline = mode === 'baseline' && baselineId !== null ? baselineId : undefined;
    compareRuns(ids, baseline)
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
  }, [selectionKey, mode, baselineId]);

  const sameRun = new Set(effective).size !== effective.length;
  const pool = summaries ?? reports;
  const byId = new Map((pool ?? []).map((r) => [r.run_id, r] as const));
  const selectedReports = effective.map((sid) => byId.get(sid) ?? null);
  const ready = !sameRun && selectedReports.length >= 2 && selectedReports.every((r) => r !== null);
  const baseline = ready ? selectedReports[0] : null;
  const candidates = ready ? (selectedReports.slice(1) as Report[]) : [];

  /** Mode toggle: carry the comparison across the two representations. */
  const switchMode = (next: 'slots' | 'baseline') => {
    if (next === mode) {
      return;
    }
    setMode(next);
    if (next === 'baseline') {
      setBaselineId(selected[0] ?? null);
      setPicked(selected.slice(1));
    } else {
      setSelected(baselineId !== null ? [baselineId, ...picked] : []);
    }
  };

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
      {/* Phase 52: the trail back up -- execution hub AND the scenario
          list, since compare is two levels deep. Phase 67b: the list root
          is /scenarios (the flat /executions list redirects there). */}
      <Breadcrumbs
        items={[
          { label: 'Scenarios', href: '/scenarios' },
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
        <Card>
          {/* Phase 76: a first-run empty, not a dead end -- the one action
              routes to /scenarios, where a scenario gets run (never into
              /executions/, which has no runs to show here by definition). */}
          <EmptyState
            testId="compare-no-runs"
            icon={<GitCompare className="size-6" />}
            title="No runs yet"
            description="Runs appear here once they finalise."
            action={{ label: 'Run a scenario first', to: '/scenarios' }}
          />
        </Card>
      )}

      {validId && !error && reports !== null && reports.length > 0 && (
        <>
          <Card>
            <CardHeader>
              <CardTitle>Runs</CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              {/* Phase 75: the two selection modes -- the long-standing slot
                  pickers, or "vs baseline": an explicit radio'd baseline plus
                  checkbox'd comparisons. aria-pressed, so the state is text,
                  not color. */}
              <div role="group" aria-label="Comparison mode" data-testid="compare-mode" className="flex flex-wrap items-center gap-2">
                <button
                  type="button"
                  data-testid="mode-slots"
                  aria-pressed={mode === 'slots'}
                  onClick={() => switchMode('slots')}
                  className={`min-h-[44px] rounded-lg px-3 text-sm font-medium transition-colors focus:ring-2 focus:ring-sky-500 focus:outline-none ${
                    mode === 'slots'
                      ? 'bg-sky-600 text-white'
                      : 'border border-slate-300 text-slate-700 hover:bg-slate-100 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800'
                  }`}
                >
                  A/B/C slots
                </button>
                <button
                  type="button"
                  data-testid="mode-vs-baseline"
                  aria-pressed={mode === 'baseline'}
                  onClick={() => switchMode('baseline')}
                  className={`min-h-[44px] rounded-lg px-3 text-sm font-medium transition-colors focus:ring-2 focus:ring-sky-500 focus:outline-none ${
                    mode === 'baseline'
                      ? 'bg-sky-600 text-white'
                      : 'border border-slate-300 text-slate-700 hover:bg-slate-100 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800'
                  }`}
                >
                  vs baseline
                </button>
              </div>
              {mode === 'slots' && (
                <>
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
                </>
              )}
              {mode === 'baseline' && (
                <div role="group" aria-label="Baseline and comparison runs" data-testid="baseline-run-list" className="space-y-2">
                  <p className="text-body-sm text-slate-500 dark:text-slate-400">
                    Pick the baseline with the radio, then tick every run to compare against it.
                  </p>
                  {reports.map((r) => {
                    const isBaseline = r.run_id === baselineId;
                    const isPicked = picked.includes(r.run_id);
                    return (
                      <div
                        key={r.run_id}
                        className="flex items-center gap-3 rounded-lg border border-slate-200 px-3 py-2 dark:border-slate-700"
                      >
                        <input
                          type="radio"
                          name="compare-baseline"
                          data-testid={`baseline-radio-${r.run_id}`}
                          aria-label={`Use run ${r.run_id} as the baseline`}
                          checked={isBaseline}
                          onChange={() => {
                            setBaselineId(r.run_id);
                            // The new baseline leaves the comparison set: its
                            // checkbox disables, it can't compare against itself.
                            setPicked((prev) => prev.filter((id) => id !== r.run_id));
                          }}
                          className="size-4 accent-sky-600"
                        />
                        <input
                          type="checkbox"
                          data-testid={`compare-check-${r.run_id}`}
                          aria-label={`Compare run ${r.run_id} against the baseline`}
                          checked={isPicked}
                          disabled={isBaseline}
                          onChange={() =>
                            setPicked((prev) => (isPicked ? prev.filter((id) => id !== r.run_id) : [...prev, r.run_id]))
                          }
                          className="size-4 accent-sky-600"
                        />
                        <span className="text-body-sm text-slate-900 dark:text-white">
                          Run #{r.run_id} · {formatTime(r.started_at)}
                        </span>
                      </div>
                    );
                  })}
                </div>
              )}
              {mode === 'baseline' && baselineId === null && (
                <p className="text-body-sm text-amber-600 dark:text-amber-400" data-testid="baseline-hint">
                  Pick a baseline run — the others are compared against it.
                </p>
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
              <CompareChart runIds={effective.filter((sid) => byId.has(sid))} />
            </>
          )}
          {/* Phase 76: the results area is never blank while the selection
              is incomplete (fewer than two distinct runs picked). No action
              button here by design: the pickers in the Runs card above ARE
              the action; the hint just points at them. */}
          {!ready && (
            <Card>
              <EmptyState
                testId="compare-pick-hint"
                icon={<GitCompare className="size-6" />}
                title="Pick runs to compare"
                description="Choose a baseline and at least one other run above — the delta table appears once two different runs are picked."
              />
            </Card>
          )}
        </>
      )}
    </div>
  );
}
