import { useEffect, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { ExternalLink, Download, Check, ChevronDown, Share2 } from 'lucide-react';
import Button from '../components/ui/Button';
import Card, { CardContent, CardHeader, CardTitle } from '../components/ui/Card';
import ClusterBadge from '../components/ui/ClusterBadge';
import CopyButton from '../components/ui/CopyButton';
import CopyLink from '../components/CopyLink';
import EngineBadge from '../components/ui/EngineBadge';
import Input from '../components/ui/Input';
import OutcomeBadge from '../components/ui/OutcomeBadge';
import { TabPanel, Tabs } from '../components/ui/Tabs';
import LabelsTable from '../components/LabelsTable';
import ShareRunModal from '../components/ShareRunModal';
import { useProjectSelection } from '../components/ProjectSwitcher';
import { ApiError } from '../api/client';
import { apiClient } from '../api/client';
import { getRunReport, getShardConfig, getShardLog, listExecutionReports } from '../api/reports';
import type { Load, Report } from '../api/reports';
import { pctDelta, formatDelta } from './RunCompare';
import { listExecutions, type ExecutionSummary } from '../api/executions';
import { fetchSeries } from '../api/series';
import type { SeriesPoint } from '../api/series';
import HeroChart from '../components/charts/HeroChart';
import TimeSeriesChart from '../components/charts/TimeSeriesChart';
import { useSession } from '../hooks/useSession';
import { formatApmLink, loadApmTemplate, saveApmTemplate } from '../api/apm';
import { SignatureSection, TrendSection } from './ReportsTrend';

/** Validates the shard viewer's input: shards are 0-indexed non-negative integers; anything else is null. */
export function parseShard(raw: string): number | null {
  if (raw.trim() === '') {
    return null;
  }
  const n = Number(raw);
  return Number.isInteger(n) && n >= 0 ? n : null;
}

function formatTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

/** The run export download URL: the API base (same origin as the SPA) plus
 * the run's export endpoint with the requested format. Anchors, not fetch:
 * the browser performs the download natively. */
function exportRunHref(runId: number, format: 'csv' | 'json' | 'pdf'): string {
  return `${apiClient.baseUrl}/runs/${runId}/export?format=${format}`;
}

/** Small-button styling for the run header's action group -- CopyButton's
 * visual language, anchor-flavoured, so the export links and the copy-link
 * read as one group. */
const runActionClass =
  'inline-flex min-h-[32px] items-center gap-1 rounded-md border border-slate-300 px-2 py-1 text-caption font-medium text-slate-600 transition-colors hover:bg-slate-100 focus:outline-none focus:ring-2 focus:ring-sky-500 dark:border-slate-600 dark:text-slate-300 dark:hover:bg-slate-800';

/** Shared pill styling for the list pages' filter chips (phase 28) -- the
 * latency percentile selector's visual language, so every toggleable pill
 * in the SPA reads the same. */
function chipClass(selected: boolean): string {
  return `${
    selected
      ? 'bg-sky-600 text-white'
      : 'bg-slate-100 text-slate-600 hover:bg-slate-200 dark:bg-slate-700/50 dark:text-slate-300 dark:hover:bg-slate-700'
  } focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500`;
}

/** The engine a row groups under: absent engine means the deployment default. */
function engineOf(e: ExecutionSummary): string {
  return e.engine ?? 'default';
}

function sortedPercentiles(latency: Record<string, number>): [string, number][] {
  return Object.entries(latency).sort(([a], [b]) => Number(a) - Number(b));
}

/** The list view: an execution's reports, most recent first. */
export default function Reports() {
  const params = useParams();
  if (params.runId) {
    return <ReportDetail runId={params.runId} />;
  }
  return <ReportsList />;
}

function ReportsList() {
  const { can } = useSession();
  // Phase 32: the global project switcher's selection (localStorage via
  // the shared hook) scopes this list like every other execution view.
  const { selectedId, selectedName, select } = useProjectSelection();
  const [executionId, setExecutionId] = useState('');
  const [executions, setExecutions] = useState<ExecutionSummary[] | null>(null);
  const [showManual, setShowManual] = useState(false);
  const [engineFilter, setEngineFilter] = useState('all');
  const [search, setSearch] = useState('');
  const [reports, setReports] = useState<Report[] | null>(null);
  const [loadedExecutionId, setLoadedExecutionId] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  // List-first (phase 27): the executions a caller can see, so picking one
  // needs no guessed id. Falls back silently -- the manual input below still
  // works when the list endpoint errs or is empty.
  useEffect(() => {
    let alive = true;
    listExecutions()
      .then((rows) => {
        if (alive) setExecutions(rows);
      })
      .catch(() => {
        if (alive) setExecutions([]);
      });
    return () => {
      alive = false;
    };
  }, []);

  const load = async (id: string) => {
    const executionIdNum = Number(id);
    if (!id || !Number.isInteger(executionIdNum) || executionIdNum <= 0) {
      setError('Enter a valid execution id.');
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const got = await listExecutionReports(executionIdNum);
      setReports(got);
      setLoadedExecutionId(executionIdNum);
    } catch (err) {
      setReports(null);
      setLoadedExecutionId(null);
      setError(err instanceof ApiError ? err.message : 'Failed to load reports.');
    } finally {
      setLoading(false);
    }
  };

  // The filter row: engine chips + name/id search (phase 28) under the
  // global project scope (phase 32), purely client-side -- the executions
  // are already in memory. Hidden while the list loads or is empty: there
  // is nothing to filter yet.
  const engines = executions === null ? [] : Array.from(new Set(executions.map(engineOf)));
  const filtered = (executions ?? []).filter((e) => {
    if (selectedId !== '' && String(e.project_id) !== selectedId) {
      return false;
    }
    if (engineFilter !== 'all' && engineOf(e) !== engineFilter) {
      return false;
    }
    const q = search.trim().toLowerCase();
    return q === '' || `${e.id} ${e.name}`.toLowerCase().includes(q);
  });

  // The compare deep-link: only meaningful once an execution with at
  // least two runs is on screen, and only for callers who may read
  // reports -- the same grant that shows the Reports nav item (task 10).
  const compareHref =
    loadedExecutionId !== null && reports !== null && reports.length >= 2 && can('report', 'read')
      ? `/executions/${loadedExecutionId}/compare`
      : null;

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-display-sm text-slate-900 dark:text-white">Reports</h1>
          <p className="text-body-sm mt-1 text-slate-500 dark:text-slate-400">
            An execution&apos;s run history, most recent first.
          </p>
        </div>
        {compareHref && (
          <Link
            to={compareHref}
            data-testid="compare-runs-link"
            className="rounded text-sm font-medium text-sky-600 hover:underline focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:text-sky-400"
          >
            Compare runs →
          </Link>
        )}
      </div>

      {/* The filter row: engine chips + name/id search (phase 28) under the
          global project scope (phase 32), purely client-side -- the
          executions are already in memory. Hidden while the list loads or
          is empty: there is nothing to filter yet. */}
      {executions !== null && executions.length > 0 && (
        <div className="flex flex-wrap items-center gap-2" data-testid="filter-chips">
          {/* The active project scope (phase 32) leads the row so the list's
              narrowing reads top-down: project first, then engine, then text. */}
          {selectedId !== '' && (
            <span
              data-testid="filter-project"
              className="inline-flex items-center gap-1 rounded-full bg-sky-100 px-3 py-1 text-caption font-medium text-sky-700 dark:bg-sky-900/40 dark:text-sky-300"
            >
              project: {selectedName !== '' ? selectedName : selectedId}
              <button
                type="button"
                aria-label="Clear project filter"
                onClick={() => select('')}
                className="rounded-full px-1 transition-colors hover:bg-sky-200 focus:outline-none focus:ring-2 focus:ring-sky-500 dark:hover:bg-sky-800"
              >
                ✕
              </button>
            </span>
          )}
          <button
            type="button"
            data-testid="filter-engine-all"
            aria-pressed={engineFilter === 'all'}
            onClick={() => setEngineFilter('all')}
            className={`rounded-full px-3 py-1 text-caption font-medium transition-colors ${chipClass(engineFilter === 'all')}`}
          >
            All
          </button>
          {engines.map((engine) => (
            <button
              key={engine}
              type="button"
              data-testid={`filter-engine-${engine}`}
              aria-pressed={engineFilter === engine}
              onClick={() => setEngineFilter(engine)}
              className={`rounded-full px-3 py-1 text-caption font-medium transition-colors ${chipClass(engineFilter === engine)}`}
            >
              {engine}
            </button>
          ))}
          <div className="w-full sm:w-56">
            <Input
              data-testid="filter-search"
              type="search"
              placeholder="Filter name/id…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </div>
        </div>
      )}

      <Card>
        <ul className="divide-y divide-slate-200 dark:divide-slate-700" data-testid="execution-list">
          {executions === null ? (
            <li className="py-3 text-body-sm text-slate-500 dark:text-slate-400">Loading executions…</li>
          ) : executions.length === 0 ? (
            <li className="py-3 text-body-sm text-slate-500 dark:text-slate-400">
              No executions visible to you yet.
            </li>
          ) : filtered.length === 0 ? (
            <li className="py-3 text-body-sm text-slate-500 dark:text-slate-400">
              No executions match the current filters.
            </li>
          ) : (
            filtered.map((e) => {
              const active = loadedExecutionId === e.id;
              return (
                <li key={e.id}>
                  <button
                    type="button"
                    onClick={() => {
                      setExecutionId(String(e.id));
                      void load(String(e.id));
                    }}
                    className={`flex w-full items-center justify-between rounded py-3 px-2 text-left text-sm transition-colors hover:bg-slate-50 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:hover:bg-slate-800/50 ${
                      active ? 'bg-slate-50 dark:bg-slate-800/50' : ''
                    }`}
                    data-testid={`execution-${e.id}`}
                  >
                    <span className="font-medium text-slate-900 dark:text-slate-100">
                      #{e.id} {e.name}
                    </span>
                    <span className="text-slate-500 dark:text-slate-400">
                      {e.engine ?? 'default'}
                      {' · '}
                      {new Date(e.created_time).toLocaleString()}
                      {active ? ' · loaded' : ''}
                    </span>
                  </button>
                </li>
              );
            })
          )}
        </ul>
        <div className="mt-4 border-t border-slate-100 pt-3 dark:border-slate-800">
          {showManual ? (
            <form
              className="flex flex-col gap-4 sm:flex-row sm:items-end"
              onSubmit={(e) => {
                e.preventDefault();
                void load(executionId);
              }}
            >
              <Input
                label="Execution ID"
                type="number"
                min={1}
                value={executionId}
                onChange={(e) => setExecutionId(e.target.value)}
                placeholder="e.g. 42"
                fullWidth
              />
              <Button type="submit" disabled={loading}>
                {loading ? 'Loading…' : 'Load reports'}
              </Button>
            </form>
          ) : (
            <button
              type="button"
              onClick={() => setShowManual(true)}
              className="text-caption rounded text-slate-500 underline-offset-2 hover:underline focus:outline-none focus:ring-2 focus:ring-sky-500 dark:text-slate-400"
              data-testid="manual-id-toggle"
            >
              Know the id? Enter it manually →
            </button>
          )}
        </div>
        {error && (
          <p className="mt-4 text-sm text-red-600 dark:text-red-400" role="alert">
            {error}
          </p>
        )}
      </Card>

      <ApmTemplateSettings />

      {reports && loadedExecutionId !== null && (
        <>
          <Card padding="none">
            {reports.length === 0 ? (
              <p className="text-body-sm p-6 text-slate-500 dark:text-slate-400">No reports for this execution yet.</p>
            ) : (
              <ul className="divide-y divide-slate-200 dark:divide-slate-700">
                {reports.map((r) => (
                  <li key={r.run_id}>
                    <Link
                      to={`/reports/${r.run_id}`}
                      className="flex min-h-[44px] flex-col gap-2 rounded p-4 transition-colors hover:bg-slate-50 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:hover:bg-slate-700/50 sm:flex-row sm:items-center sm:justify-between"
                    >
                      <div className="flex flex-wrap items-center gap-3">
                        <OutcomeBadge outcome={r.outcome} />
                        <span className="text-body-sm font-medium text-slate-900 dark:text-white">Run #{r.run_id}</span>
                        <span className="text-caption text-slate-500 dark:text-slate-400">scenario {r.scenario_id}</span>
                        {r.engine && <EngineBadge engine={r.engine} />}
                        {r.cluster && <ClusterBadge cluster={r.cluster} />}
                      </div>
                      <div className="text-caption text-slate-500 dark:text-slate-400">
                        {formatTime(r.started_at)} · {r.achieved.samples ?? 0} samples ·{' '}
                        {(r.error_rate * 100).toFixed(1)}% errors
                      </div>
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <TrendSection executionId={loadedExecutionId} />
          <SignatureSection executionId={loadedExecutionId} />
        </>
      )}
    </div>
  );
}

/**
 * The APM deep-link template, stored per browser (localStorage): when set,
 * each run's correlation id links into the operator's own APM. Client-side
 * only -- the server never sees it (same precedent as the theme toggle).
 */
function ApmTemplateSettings() {
  const [template, setTemplate] = useState<string>(() => loadApmTemplate());
  const [saved, setSaved] = useState(false);

  return (
    <Card>
      <CardHeader>
        <CardTitle>APM deep-link template</CardTitle>
      </CardHeader>
      <CardContent>
        <form
          className="flex flex-col gap-4 sm:flex-row sm:items-end"
          onSubmit={(e) => {
            e.preventDefault();
            saveApmTemplate(template);
            setSaved(true);
          }}
        >
          {/* text, not url: the {correlation_id} placeholder is not a valid URL character, so url validation would reject it. */}
          <Input
            label="URL template"
            type="text"
            value={template}
            onChange={(e) => {
              setTemplate(e.target.value);
              setSaved(false);
            }}
            placeholder="https://apm.example.com/trace/{correlation_id}"
            helperText="Optional. Put {correlation_id} where the trace id goes; run pages then link each correlation id into your APM. Saved in this browser only."
            fullWidth
          />
          <Button type="submit" variant="secondary">
            Save template
          </Button>
        </form>
        {saved && (
          <p className="text-caption mt-3 text-emerald-600 dark:text-emerald-400" role="status">
            Template saved.
          </p>
        )}
      </CardContent>
    </Card>
  );
}

function LoadStat({ label, load }: { label: string; load: Report['requested'] }) {
  return (
    <div>
      <p className="text-caption font-medium text-slate-500 dark:text-slate-400">{label}</p>
      <p className="text-heading-md text-slate-900 dark:text-white">{load.concurrency} VU</p>
      <p className="text-caption text-slate-500 dark:text-slate-400">
        {load.throughput > 0 ? `${load.throughput.toFixed(1)} req/s target` : 'unlimited req/s'}
        {load.duration_seconds ? ` · ${load.duration_seconds}s` : ''}
      </p>
      {(load.samples !== undefined || load.failed !== undefined) && (
        <p className="text-caption text-slate-500 dark:text-slate-400">
          {load.samples ?? 0} samples, {load.failed ?? 0} failed
        </p>
      )}
    </div>
  );
}

/**
 * One engine shard's durable objects: the compiled config exactly as the
 * run used it, and the captured log that outlives the pod. Scenario comes
 * from the report (prefilled); the operator only picks the shard number.
 */
function ShardObjects({ report }: { report: Report }) {
  const [shard, setShard] = useState('0');
  const [config, setConfig] = useState<string | null>(null);
  const [log, setLog] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const load = async (raw: string) => {
    const parsed = parseShard(raw);
    if (parsed === null) {
      setError('Shard must be a non-negative integer.');
      setConfig(null);
      setLog(null);
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const [cfg, shardLog] = await Promise.all([
        getShardConfig(report.run_id, report.scenario_id, parsed),
        getShardLog(report.run_id, report.scenario_id, parsed),
      ]);
      setConfig(cfg);
      setLog(shardLog);
    } catch (err) {
      setConfig(null);
      setLog(null);
      setError(err instanceof ApiError ? err.message : 'Failed to load shard objects.');
    } finally {
      setLoading(false);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>Shard objects</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-caption text-slate-500 dark:text-slate-400">
          Run #{report.run_id} · scenario {report.scenario_id} — pick a shard to view the config it ran with and the log it captured.
        </p>
        <form
          className="flex items-end gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            void load(shard);
          }}
        >
          <div className="w-32">
            <Input
              label="Shard"
              type="number"
              min={0}
              value={shard}
              onChange={(e) => setShard(e.target.value)}
            />
          </div>
          <Button type="submit" variant="secondary" disabled={loading}>
            {loading ? 'Loading…' : 'Load shard'}
          </Button>
        </form>
        {error && (
          <p className="text-sm text-red-600 dark:text-red-400" role="alert">
            {error}
          </p>
        )}
        {config !== null && (
          <div className="space-y-2">
            <p className="text-caption font-medium text-slate-500 dark:text-slate-400">Config (as the run used it)</p>
            <pre className="max-h-96 overflow-auto rounded-lg bg-slate-900 p-4 font-mono text-xs leading-relaxed text-slate-100 dark:bg-slate-950">
              {config}
            </pre>
          </div>
        )}
        {log !== null && (
          <div className="space-y-2">
            <p className="text-caption font-medium text-slate-500 dark:text-slate-400">Log (captured engine output)</p>
            <pre className="max-h-96 overflow-auto rounded-lg bg-slate-900 p-4 font-mono text-xs leading-relaxed text-slate-100 dark:bg-slate-950">
              {log}
            </pre>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

/** The percentiles the series endpoint serves, in display order. */
const LATENCY_PERCENTILES = ['50', '90', '95', '99'];

/** Shared pill styling for the latency percentile selector. */
function pctPill(selected: boolean): string {
  return `${
    selected
      ? 'bg-sky-600 text-white'
      : 'bg-slate-100 text-slate-600 hover:bg-slate-200 dark:bg-slate-700/50 dark:text-slate-300 dark:hover:bg-slate-700'
  } focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500`;
}

/**
 * The run's per-second shape once the series is in memory: the hero chart
 * leads -- VUs, RPS, and the error rate folded into one picture -- with
 * latency below it, its percentile selector switching the plotted series
 * client-side (every percentile arrived in the same fetch, so switching
 * costs nothing). The former chart-vus-rps and chart-errors blocks live
 * on as hidden wrappers so the testids layout-check and the tests key on
 * stay stable.
 */
function TimeSeriesCharts({ points, pct, onPct }: { points: SeriesPoint[]; pct: string; onPct: (p: string) => void }) {
  const available = LATENCY_PERCENTILES.filter((p) => points.some((pt) => pt.latency?.[p] !== undefined));
  const effective = available.includes(pct) ? pct : available[available.length - 1] ?? pct;
  const latencyPoints = points
    .filter((p) => p.latency?.[effective] !== undefined)
    .map((p) => ({ x: p.ts, y: (p.latency as Record<string, number>)[effective] * 1000 }));

  return (
    <div className="space-y-6">
      <HeroChart points={points} />
      {/* Folded into the hero above; the testid wrappers stay for layout-check. */}
      <div data-testid="chart-vus-rps" className="hidden">
        <p className="sr-only">folded into hero chart</p>
      </div>
      <div data-testid="chart-errors" className="hidden">
        <p className="sr-only">error rate folded into hero chart</p>
      </div>
      <div data-testid="chart-latency">
        <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
          <p className="text-caption font-medium text-slate-500 dark:text-slate-400">Response time</p>
          {available.length > 0 && (
            <div className="flex gap-1" role="group" aria-label="Latency percentile">
              {available.map((p) => (
                <button
                  key={p}
                  type="button"
                  data-testid={`pct-${p}`}
                  aria-pressed={p === effective}
                  onClick={() => onPct(p)}
                  className={`rounded-full px-3 py-1 text-caption font-medium transition-colors ${pctPill(p === effective)}`}
                >
                  p{p}
                </button>
              ))}
            </div>
          )}
        </div>
        {/* The wire carries seconds; the chart reads milliseconds. */}
        <TimeSeriesChart
          xType="time"
          yLabel="ms"
          series={[{ name: effective ? `p${effective}` : 'latency', color: 'text-emerald-500', points: latencyPoints }]}
        />
      </div>
    </div>
  );
}

/**
 * Expands the report's requested load into the constant line the overlay
 * charts. Load carries no stages -- just concurrency and an optional
 * duration_seconds -- so the requested profile is flat: concurrency held
 * from the run's first measured second to last+duration when the duration
 * is known (an early exit shows requested running past achieved, an overrun
 * the reverse), and to the last sample when it is not. No series or no
 * requested concurrency yields nothing, which is the card's hide rule.
 */
export function requestedLine(requested: Load, points: { x: number }[]): { x: number; y: number }[] {
  if (points.length === 0 || !Number.isFinite(requested.concurrency) || requested.concurrency <= 0) {
    return [];
  }
  const firstTs = points[0].x;
  const lastTs = points[points.length - 1].x;
  const end =
    requested.duration_seconds && requested.duration_seconds > 0 ? firstTs + requested.duration_seconds : lastTs;
  return [
    { x: firstTs, y: requested.concurrency },
    { x: Math.max(end, firstTs), y: requested.concurrency },
  ];
}

/**
 * The "Time series" card: loads the run's per-second series alongside the
 * report, with loading, error (retry), and empty states -- runs finalised
 * before the series store existed have a report but no series. When the
 * series and a requested load both exist, the requested-vs-achieved overlay
 * follows as its own card (hidden otherwise, per its own hide rule).
 */
function TimeSeriesSection({ runId, requested }: { runId: number; requested: Load }) {
  const [state, setState] = useState<
    { kind: 'loading' } | { kind: 'error'; message: string } | { kind: 'empty' } | { kind: 'ready'; points: SeriesPoint[] }
  >({ kind: 'loading' });
  const [retry, setRetry] = useState(0);
  const [pct, setPct] = useState('95');

  useEffect(() => {
    let cancelled = false;
    setState({ kind: 'loading' });
    fetchSeries(runId)
      .then((got) => {
        if (cancelled) {
          return;
        }
        setState(got.points.length === 0 ? { kind: 'empty' } : { kind: 'ready', points: got.points });
      })
      .catch((err: unknown) => {
        if (cancelled) {
          return;
        }
        setState({ kind: 'error', message: err instanceof ApiError ? err.message : 'Failed to load time series.' });
      });
    return () => {
      cancelled = true;
    };
  }, [runId, retry]);

  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle>Time series</CardTitle>
        </CardHeader>
      <CardContent>
        {state.kind === 'loading' && (
          <div className="space-y-3" data-testid="series-loading">
            {[0, 1, 2].map((i) => (
              <div key={i} className="h-44 animate-pulse rounded-lg bg-slate-100 dark:bg-slate-700/50" />
            ))}
          </div>
        )}
        {state.kind === 'error' && (
          <div className="flex flex-wrap items-center gap-3">
            <p className="text-sm text-red-600 dark:text-red-400" role="alert">
              {state.message}
            </p>
            <Button variant="secondary" size="sm" onClick={() => setRetry((n) => n + 1)} data-testid="series-retry">
              Retry
            </Button>
          </div>
        )}
        {state.kind === 'empty' && (
          <p className="text-body-sm text-slate-500 dark:text-slate-400" data-testid="series-empty">
            No per-second data recorded for this run.
          </p>
        )}
        {state.kind === 'ready' && <TimeSeriesCharts points={state.points} pct={pct} onPct={setPct} />}
      </CardContent>
    </Card>
      {state.kind === 'ready' && (
        <RequestedVsAchieved requested={requested} points={state.points} />
      )}
    </>
  );
}

/** The requested-vs-achieved overlay: what was asked for (muted, constant) against what the engine actually held (strong). */
function RequestedVsAchieved({ requested, points }: { requested: Load; points: SeriesPoint[] }) {
  const line = requestedLine(
    requested,
    points.map((p) => ({ x: p.ts }))
  );
  if (line.length === 0) {
    return null;
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle>Requested vs achieved</CardTitle>
      </CardHeader>
      <CardContent>
        <div data-testid="chart-requested">
          <TimeSeriesChart
            xType="time"
            yLabel="VUs"
            height={180}
            series={[
              { name: 'requested', color: 'text-slate-400', points: line },
              { name: 'achieved', color: 'text-sky-600', points: points.map((p) => ({ x: p.ts, y: p.vus })) },
            ]}
          />
        </div>
        <p className="text-caption mt-2 text-slate-500 dark:text-slate-400">
          The concurrency the deployment asked for against what the engine actually held.
        </p>
      </CardContent>
    </Card>
  );
}

/** The headline metrics the compare card diffs, in display order. RPS is
 * the achieved figure; latency fields are seconds on the wire and format
 * as ms, like every latency read in the SPA. */
const COMPARE_METRICS: Array<{
  name: string;
  label: string;
  /** Which direction reads as an improvement for this metric. */
  better: 'lower' | 'higher';
  value: (r: Report) => number | undefined;
  format: (v: number) => string;
}> = [
  {
    name: 'samples',
    label: 'Samples',
    better: 'higher',
    value: (r) => r.achieved?.samples,
    format: (v) => v.toLocaleString(),
  },
  {
    name: 'error_rate',
    label: 'Error rate',
    better: 'lower',
    value: (r) => r.error_rate,
    format: (v) => `${(v * 100).toFixed(2)}%`,
  },
  {
    name: 'p95',
    label: 'p95',
    better: 'lower',
    value: (r) => r.latency?.['95'],
    format: (v) => `${(v * 1000).toFixed(1)} ms`,
  },
  {
    name: 'p99',
    label: 'p99',
    better: 'lower',
    value: (r) => r.latency?.['99'],
    format: (v) => `${(v * 1000).toFixed(1)} ms`,
  },
  {
    name: 'rps',
    label: 'RPS',
    better: 'higher',
    value: (r) => r.achieved?.throughput,
    format: (v) => `${v.toFixed(1)} req/s`,
  },
];

/** ±10% is the card's significance band (phase 33): a delta beyond it in
 * the bad direction reads rose, beyond it in the good direction emerald,
 * within it (or unmeasured — pctDelta's null) slate. "Same-ish" must not
 * shout. */
export const COMPARE_BAND_PCT = 10;

/** Which tone a delta renders with; exported for the band-edge unit tests. */
export type CompareTone = 'worse' | 'better' | 'neutral';

/** Tone of a signed percent delta under the ±10% band, given which
 * direction is better for the metric. */
export function deltaTone(better: 'lower' | 'higher', delta: number | null): CompareTone {
  if (delta === null) {
    return 'neutral';
  }
  const up = delta > COMPARE_BAND_PCT;
  const down = delta < -COMPARE_BAND_PCT;
  if (better === 'lower') {
    return up ? 'worse' : down ? 'better' : 'neutral';
  }
  return down ? 'worse' : up ? 'better' : 'neutral';
}

/** Tone → text colour classes (rose/emerald/slate, phase 33's palette). */
function toneClass(tone: CompareTone): string {
  switch (tone) {
    case 'worse':
      return 'text-rose-600 dark:text-rose-400';
    case 'better':
      return 'text-emerald-600 dark:text-emerald-400';
    default:
      return 'text-slate-500 dark:text-slate-400';
  }
}

/** The default baseline: the newest OTHER passed run — the last honest
 * green the execution posted — falling back to the newest other run when
 * nothing passed yet (something to diff beats nothing). Null when there
 * is no other run at all: the card's hide rule. */
export function defaultBaselineRun(others: Report[]): number | null {
  if (others.length === 0) {
    return null;
  }
  const passed = others.find((r) => r.outcome === 'passed');
  return (passed ?? others[0]).run_id;
}

/** The baseline picker's option styling, ProjectSwitcher's visual language. */
function baselineOptionClass(selected: boolean): string {
  return `flex w-full items-center justify-between gap-2 rounded px-3 py-2 text-left text-sm transition-colors ${
    selected
      ? 'bg-sky-50 font-medium text-sky-700 dark:bg-sky-900/30 dark:text-sky-300'
      : 'text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'
  } focus:outline-none focus:ring-2 focus:ring-inset focus:ring-sky-500`;
}

/** One row of the per-label p95 diff (task 3): 'both' carries the two
 * p95s, 'new'/'dropped' carry the one side that measured anything
 * (seconds off the wire, rendered ms). */
export interface LabelDiffRow {
  label: string;
  kind: 'both' | 'new' | 'dropped';
  current?: number;
  baseline?: number;
}

/** The per-label p95 diff's rows: every label either run exercised,
 * matched by name, sorted alphabetically so the table is deterministic
 * regardless of each report's own label order. */
export function compareLabelRows(current: Report, baseline: Report): LabelDiffRow[] {
  const cur = new Map((current.labels ?? []).map((l) => [l.label, l]));
  const base = new Map((baseline.labels ?? []).map((l) => [l.label, l]));
  const names = Array.from(new Set([...cur.keys(), ...base.keys()])).sort((a, b) => a.localeCompare(b));
  return names.map((name) => {
    const c = cur.get(name);
    const b = base.get(name);
    if (c !== undefined && b !== undefined) {
      return { label: name, kind: 'both' as const, current: c.latency?.['95'], baseline: b.latency?.['95'] };
    }
    if (c !== undefined) {
      return { label: name, kind: 'new' as const, current: c.latency?.['95'] };
    }
    return { label: name, kind: 'dropped' as const, baseline: base.get(name)?.latency?.['95'] };
  });
}

/** The Overview tab's Compare card (phase 33): the run on screen against
 * a picked baseline from the same execution — headline metric rows with
 * signed percent deltas under the ±10% band, plus (task 3) a per-label
 * p95 diff. The baseline is another run's full report, fetched on pick;
 * the current one is already in memory. Hidden entirely when the
 * execution offers no other run to compare against. */
function CompareCard({ current, siblings }: { current: Report; siblings: Report[] }) {
  // Others = this execution's runs minus the one on screen. Siblings load
  // once per execution and the card (re)mounts on every run navigation,
  // so the initial pick is simply the default baseline at mount.
  const others = siblings.filter((r) => r.run_id !== current.run_id);
  const [baselineId, setBaselineId] = useState<number | null>(() => defaultBaselineRun(others));
  const [baseline, setBaseline] = useState<Report | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [open, setOpen] = useState(false);

  // Tap-away and Escape close, ProjectSwitcher's pattern: listeners exist
  // only while open, and the root's marker class keeps the toggle button
  // itself outside the outside-click check.
  useEffect(() => {
    if (!open) {
      return;
    }
    const handleClickOutside = (event: MouseEvent) => {
      const target = event.target as Element | null;
      if (!target?.closest('.compare-picker')) {
        setOpen(false);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('mousedown', handleClickOutside);
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [open]);

  useEffect(() => {
    if (baselineId === null) {
      return;
    }
    let cancelled = false;
    setBaseline(null);
    setError(null);
    getRunReport(baselineId)
      .then((rep) => {
        if (!cancelled) setBaseline(rep);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof ApiError ? err.message : 'Failed to load baseline.');
      });
    return () => {
      cancelled = true;
    };
  }, [baselineId]);

  if (baselineId === null) {
    return null;
  }
  const selected = others.find((r) => r.run_id === baselineId) ?? null;

  return (
    <Card data-testid="compare-card">
      <CardHeader className="flex flex-row items-center justify-between gap-4">
        <CardTitle>Compare</CardTitle>
        <div className="compare-picker relative">
          <button
            type="button"
            data-testid="compare-baseline-toggle"
            aria-haspopup="listbox"
            aria-expanded={open}
            onClick={() => setOpen((o) => !o)}
            className="flex min-h-[36px] max-w-72 items-center gap-1 rounded-md border border-slate-300 px-2 py-1 text-caption font-medium text-slate-600 transition-colors hover:bg-slate-100 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:border-slate-600 dark:text-slate-300 dark:hover:bg-slate-800"
          >
            <span className="truncate">
              Baseline: {selected ? `#${selected.run_id} · ${selected.outcome}` : '—'}
            </span>
            <ChevronDown aria-hidden className={`h-4 w-4 shrink-0 transition-transform ${open ? 'rotate-180' : ''}`} />
          </button>
          {open && (
            <div
              role="listbox"
              aria-label="Baseline run"
              className="absolute right-0 top-full z-50 mt-2 w-72 rounded-lg border border-slate-200 bg-white py-1 shadow-lg dark:border-slate-800 dark:bg-slate-950"
            >
              <div className="max-h-72 overflow-y-auto px-2 pb-1">
                {others.map((r) => (
                  <button
                    key={r.run_id}
                    type="button"
                    role="option"
                    aria-selected={r.run_id === baselineId}
                    data-testid={`compare-baseline-${r.run_id}`}
                    onClick={() => {
                      setBaselineId(r.run_id);
                      setOpen(false);
                    }}
                    className={baselineOptionClass(r.run_id === baselineId)}
                  >
                    <span className="truncate">#{r.run_id} · {r.outcome} · {formatTime(r.started_at)}</span>
                    {r.run_id === baselineId && <Check aria-hidden className="h-4 w-4 shrink-0 text-sky-600 dark:text-sky-400" />}
                  </button>
                ))}
              </div>
            </div>
          )}
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        {error && (
          <p className="text-sm text-red-600 dark:text-red-400" role="alert">
            {error}
          </p>
        )}
        {!error && baseline === null && (
          <p className="text-body-sm text-slate-500 dark:text-slate-400" data-testid="compare-loading">
            Loading baseline…
          </p>
        )}
        {baseline !== null && (
          <div className="overflow-x-auto" data-testid="compare-metrics">
            <table className="w-full text-left text-body-sm">
              <thead>
                <tr className="text-caption border-b border-slate-200 text-slate-500 dark:border-slate-700 dark:text-slate-400">
                  <th scope="col" className="px-3 py-2 font-medium">Metric</th>
                  <th scope="col" className="px-3 py-2 font-medium">This run (#{current.run_id})</th>
                  <th scope="col" className="px-3 py-2 font-medium">Baseline (#{baseline.run_id})</th>
                  <th scope="col" className="px-3 py-2 font-medium">Delta</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {COMPARE_METRICS.map((m) => {
                  const base = m.value(baseline);
                  const cur = m.value(current);
                  const delta = base !== undefined && cur !== undefined ? pctDelta(base, cur) : null;
                  const tone = deltaTone(m.better, delta);
                  return (
                    <tr key={m.name} data-testid={`compare-metric-${m.name}`}>
                      <td className="px-3 py-2 font-medium whitespace-nowrap text-slate-900 dark:text-white">{m.label}</td>
                      <td className="px-3 py-2 whitespace-nowrap">{cur !== undefined ? m.format(cur) : '—'}</td>
                      <td className="px-3 py-2 whitespace-nowrap">{base !== undefined ? m.format(base) : '—'}</td>
                      <td className={`px-3 py-2 font-medium whitespace-nowrap ${toneClass(tone)}`}>{formatDelta(delta)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        {baseline !== null && (() => {
          // Task 3: the per-label p95 diff under the headline rows. Hidden
          // when neither run recorded labels — an empty table is noise.
          const rows = compareLabelRows(current, baseline);
          if (rows.length === 0) {
            return null;
          }
          return (
            <div className="overflow-x-auto" data-testid="compare-labels">
              <p className="text-caption mb-2 font-medium text-slate-500 dark:text-slate-400">Per-label p95 diff</p>
              <table className="w-full text-left text-body-sm">
                <thead>
                  <tr className="text-caption border-b border-slate-200 text-slate-500 dark:border-slate-700 dark:text-slate-400">
                    <th scope="col" className="px-3 py-2 font-medium">Label</th>
                    <th scope="col" className="px-3 py-2 font-medium">This run (#{current.run_id})</th>
                    <th scope="col" className="px-3 py-2 font-medium">Baseline (#{baseline.run_id})</th>
                    <th scope="col" className="px-3 py-2 font-medium">Delta</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {rows.map((row) => {
                    const delta =
                      row.current !== undefined && row.baseline !== undefined
                        ? pctDelta(row.baseline, row.current)
                        : null;
                    return (
                    <tr key={row.label} data-testid={`compare-label-${row.label}`}>
                      <td className="px-3 py-2 font-medium whitespace-nowrap text-slate-900 dark:text-white">{row.label}</td>
                      <td className="px-3 py-2 whitespace-nowrap">
                        {row.current !== undefined ? `${(row.current * 1000).toFixed(1)} ms` : '—'}
                      </td>
                      <td className="px-3 py-2 whitespace-nowrap">
                        {row.baseline !== undefined ? `${(row.baseline * 1000).toFixed(1)} ms` : '—'}
                      </td>
                      <td className="px-3 py-2 font-medium whitespace-nowrap">
                        {row.kind === 'new' && (
                          <span className="inline-flex items-center rounded-full bg-sky-100 px-2.5 py-0.5 text-xs font-medium text-sky-700 dark:bg-sky-900/30 dark:text-sky-300">
                            new
                          </span>
                        )}
                        {row.kind === 'dropped' && (
                          <span className="inline-flex items-center rounded-full bg-slate-200 px-2.5 py-0.5 text-xs font-medium text-slate-600 dark:bg-slate-700 dark:text-slate-400">
                            dropped
                          </span>
                        )}
                        {row.kind === 'both' && (
                          <span className={toneClass(deltaTone('lower', delta))}>{formatDelta(delta)}</span>
                        )}
                      </td>
                    </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          );
        })()}
        <p className="text-caption text-slate-500 dark:text-slate-400">
          Deltas read this run against the baseline; ±10% is the significance band. Latencies in ms.
        </p>
      </CardContent>
    </Card>
  );
}

/** The run workspace's tabs, in strip order; each id names its panel and its ?tab= URL value. */
const RUN_TABS = [
  { id: 'overview', label: 'Overview' },
  { id: 'timeseries', label: 'Time series' },
  { id: 'labels', label: 'Labels' },
  { id: 'errors', label: 'Errors' },
  { id: 'config', label: 'Config' },
  { id: 'objects', label: 'Objects' },
];

/**
 * /reports/:runId as a dense tabbed workspace (phase 28): one header band
 * -- identity, verdict, the get-data-out group, and jumps to this
 * execution's neighbouring runs -- then six panels behind a tab strip.
 * Every panel renders on first paint, inactive ones behind the hidden
 * attribute, so charts, labels, and shard objects are addressable without
 * clicking first (the mounted suite queries hidden subtrees; deep links
 * land on the URL's tab) and switching costs no refetch. The active tab
 * mirrors into ?tab= so a copied link reopens the exact view.
 */
function ReportDetail({ runId }: { runId: string }) {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const [report, setReport] = useState<Report | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [apmTemplate] = useState<string>(() => loadApmTemplate());
  // This execution's runs, newest first, for the prev/next jumps. A null
  // value also covers "fetch failed" -- the jumps simply disable; they
  // never block the page (this is an affordance, not a dependency).
  const [siblings, setSiblings] = useState<Report[] | null>(null);
  // The share dialog's open state (phase 34); the dialog itself mints,
  // lists, and revokes the run's public links.
  const [shareOpen, setShareOpen] = useState(false);

  const urlTab = searchParams.get('tab');
  const tab = urlTab !== null && RUN_TABS.some((t) => t.id === urlTab) ? urlTab : 'overview';
  const setTab = (id: string) => {
    // View state, not navigation: replace keeps tab clicks out of history.
    setSearchParams({ tab: id }, { replace: true });
  };

  useEffect(() => {
    const id = Number(runId);
    if (!Number.isInteger(id) || id <= 0) {
      setError('Invalid run id.');
      return;
    }
    setError(null);
    setReport(null);
    setSiblings(null);
    getRunReport(id)
      .then(setReport)
      .catch((err: unknown) => setError(err instanceof ApiError ? err.message : 'Failed to load report.'));
  }, [runId]);

  // Neighbouring runs load once per execution, not per run: the fetch is
  // keyed on execution_id, and failures land back on null (disabled jumps).
  const executionId = report?.execution_id;
  useEffect(() => {
    if (executionId === undefined) {
      return;
    }
    let alive = true;
    listExecutionReports(executionId)
      .then((rows) => {
        if (alive) setSiblings(rows);
      })
      .catch(() => {
        if (alive) setSiblings(null);
      });
    return () => {
      alive = false;
    };
  }, [executionId]);

  // Newest first is the list endpoint's contract: "next" is the run above
  // ours (newer), "prev" the one below (older); the list's edges disable.
  const runIndex =
    siblings !== null && report !== null ? siblings.findIndex((r) => r.run_id === report.run_id) : -1;
  const prevRun = siblings !== null && runIndex >= 0 && runIndex + 1 < siblings.length ? siblings[runIndex + 1] : null;
  const nextRun = runIndex > 0 && siblings !== null ? siblings[runIndex - 1] : null;

  return (
    <div className="space-y-6">
      <Button variant="ghost" size="sm" onClick={() => navigate('/reports')}>
        ← Back to reports
      </Button>

      {error && (
        <Card>
          <p className="text-sm text-red-600 dark:text-red-400" role="alert">
            {error}
          </p>
        </Card>
      )}

      {report && (
        <>
          {/* The header band: the run's identity and verdict at a glance, plus
              the actions that should never need a scroll to reach. */}
          <Card>
            <CardHeader className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
              <div className="flex flex-wrap items-center gap-2">
                <CardTitle>Run #{report.run_id}</CardTitle>
                <OutcomeBadge outcome={report.outcome} />
                {report.engine && <EngineBadge engine={report.engine} />}
                {report.cluster && <ClusterBadge cluster={report.cluster} />}
              </div>
              <div className="flex flex-wrap items-center gap-2">
                {/* The get-data-out group: both export formats the endpoint
                    serves, plus the deep-link to this very page. */}
                <a
                  href={exportRunHref(report.run_id, 'csv')}
                  download
                  data-testid="export-csv"
                  className={runActionClass}
                >
                  <Download aria-hidden className="h-3.5 w-3.5" />
                  Export CSV
                </a>
                <a
                  href={exportRunHref(report.run_id, 'json')}
                  download
                  data-testid="export-json"
                  className={runActionClass}
                >
                  <Download aria-hidden className="h-3.5 w-3.5" />
                  Export JSON
                </a>
                <a
                  href={exportRunHref(report.run_id, 'pdf')}
                  download
                  data-testid="export-pdf"
                  className={runActionClass}
                >
                  <Download aria-hidden className="h-3.5 w-3.5" />
                  Export PDF
                </a>
                {/* Phase 34: mint a token-gated public link to exactly this
                    report — the customer-facing share-out. */}
                <button
                  type="button"
                  data-testid="share-run-btn"
                  onClick={() => setShareOpen(true)}
                  className={runActionClass}
                >
                  <Share2 aria-hidden className="h-3.5 w-3.5" />
                  Share
                </button>
                <CopyLink />
                <div className="flex items-center gap-1" role="group" aria-label="Neighbouring runs">
                  <Button
                    variant="secondary"
                    size="sm"
                    data-testid="run-prev"
                    aria-label="Previous run in this execution"
                    disabled={prevRun === null}
                    onClick={() => prevRun && navigate(`/reports/${prevRun.run_id}`)}
                  >
                    ← Prev
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    data-testid="run-next"
                    aria-label="Next run in this execution"
                    disabled={nextRun === null}
                    onClick={() => nextRun && navigate(`/reports/${nextRun.run_id}`)}
                  >
                    Next →
                  </Button>
                </div>
              </div>
            </CardHeader>
            <CardContent className="grid grid-cols-2 gap-4 sm:grid-cols-4">
              <div>
                <p className="text-caption font-medium text-slate-500 dark:text-slate-400">Execution</p>
                <p className="text-body-sm text-slate-900 dark:text-white">{report.execution_id}</p>
              </div>
              <div>
                <p className="text-caption font-medium text-slate-500 dark:text-slate-400">Scenario</p>
                <p className="text-body-sm text-slate-900 dark:text-white">{report.scenario_id}</p>
              </div>
              <div>
                <p className="text-caption font-medium text-slate-500 dark:text-slate-400">Started</p>
                <p className="text-body-sm text-slate-900 dark:text-white">{formatTime(report.started_at)}</p>
              </div>
              <div>
                <p className="text-caption font-medium text-slate-500 dark:text-slate-400">Ended</p>
                <p className="text-body-sm text-slate-900 dark:text-white">{formatTime(report.ended_at)}</p>
              </div>
            </CardContent>
          </Card>

          <Tabs tabs={RUN_TABS} active={tab} onChange={setTab} data-testid="run-tabs" />

          <div className="mt-6">
            {/* Overview: the verdict-level numbers -- asked vs held load, the
                report's latency percentiles, where failures were attributed,
                and the trace id this run's load carried. */}
            <TabPanel id="overview" active={tab} className="space-y-6">
              {/* Thresholds first: the verdict at a glance — did this run meet
                  the bar its execution set, criterion by criterion, before
                  any measurement detail. */}
              <Card data-testid="thresholds-card">
                <CardHeader>
                  <CardTitle>Thresholds</CardTitle>
                </CardHeader>
                <CardContent>
                  {(report.criteria ?? []).length === 0 ? (
                    <p className="text-body-sm text-slate-500 dark:text-slate-400">
                      No criteria configured. Add pass/fail criteria in the execution&apos;s Configuration card.
                    </p>
                  ) : (
                    <ul className="space-y-1">
                      {(report.criteria ?? []).map((c, i) => {
                        // Configured but not named in failing_criteria = passed.
                        const fc = (report.failing_criteria ?? []).find((f) => f.criterion === c);
                        return (
                          <li key={i} className="flex items-center gap-2" data-testid={`threshold-row-${i}`}>
                            {fc ? (
                              fc.unparsed ? (
                                <span role="img" aria-label="could not be evaluated" data-testid={`threshold-unparsed-${i}`}>
                                  ❓
                                </span>
                              ) : (
                                <span role="img" aria-label="failed" data-testid={`threshold-fail-${i}`}>
                                  ❌
                                </span>
                              )
                            ) : (
                              <span role="img" aria-label="passed" data-testid={`threshold-pass-${i}`}>
                                ✅
                              </span>
                            )}
                            <code className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-body-sm text-slate-800 dark:bg-slate-800 dark:text-slate-200">
                              {c}
                            </code>
                            {fc?.unparsed && (
                              <span className="text-caption text-slate-500 dark:text-slate-400">
                                could not be evaluated
                              </span>
                            )}
                          </li>
                        );
                      })}
                    </ul>
                  )}
                </CardContent>
              </Card>

              {/* Phase 33: the run against a picked baseline from this
                  execution, straight under the verdict — regression context
                  before measurement detail. Hidden when no other run
                  exists to compare against. */}
              {siblings !== null && <CompareCard current={report} siblings={siblings} />}

              <Card>
                <CardHeader>
                  <CardTitle>Load</CardTitle>
                </CardHeader>
                <CardContent className="grid grid-cols-1 gap-6 sm:grid-cols-2">
                  <LoadStat label="Requested" load={report.requested} />
                  <LoadStat label="Achieved" load={report.achieved} />
                </CardContent>
              </Card>

              <Card>
                <CardHeader>
                  <CardTitle>Latency percentiles</CardTitle>
                </CardHeader>
                <CardContent>
                  {Object.keys(report.latency).length === 0 ? (
                    <p className="text-body-sm">No latency data.</p>
                  ) : (
                    <div className="flex flex-wrap gap-4">
                      {sortedPercentiles(report.latency).map(([p, seconds]) => (
                        <div key={p} className="rounded-lg bg-slate-100 px-3 py-2 dark:bg-slate-700/50">
                          <p className="text-caption text-slate-500 dark:text-slate-400">p{p}</p>
                          <p className="text-body-sm font-semibold text-slate-900 dark:text-white">{seconds.toFixed(3)}s</p>
                        </div>
                      ))}
                    </div>
                  )}
                </CardContent>
              </Card>

              <Card>
                <CardHeader>
                  <CardTitle>Attribution</CardTitle>
                </CardHeader>
                <CardContent className="grid grid-cols-3 gap-4">
                  <div>
                    <p className="text-caption font-medium text-slate-500 dark:text-slate-400">Target</p>
                    <p className="text-heading-md text-slate-900 dark:text-white">{report.attribution.target}</p>
                  </div>
                  <div>
                    <p className="text-caption font-medium text-slate-500 dark:text-slate-400">Engine</p>
                    <p className="text-heading-md text-slate-900 dark:text-white">{report.attribution.engine}</p>
                  </div>
                  <div>
                    <p className="text-caption font-medium text-slate-500 dark:text-slate-400">Unknown</p>
                    <p className="text-heading-md text-slate-900 dark:text-white">{report.attribution.unknown}</p>
                  </div>
                </CardContent>
              </Card>

              {report.correlation_id && (
                <Card>
                  <CardHeader>
                    <CardTitle>Correlation id</CardTitle>
                  </CardHeader>
                  <CardContent>
                    <div className="flex flex-wrap items-center gap-3">
                      <code className="rounded-md bg-slate-100 px-2 py-1 font-mono text-body-sm break-all text-slate-900 dark:bg-slate-900 dark:text-slate-100">
                        {report.correlation_id}
                      </code>
                      <CopyButton value={report.correlation_id} label="Copy correlation id" />
                      {formatApmLink(apmTemplate, report.correlation_id) && (
                        <a
                          href={formatApmLink(apmTemplate, report.correlation_id) as string}
                          target="_blank"
                          rel="noreferrer"
                          className="inline-flex items-center gap-1 rounded text-sm font-medium text-sky-600 hover:text-sky-700 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:text-sky-400 dark:hover:text-sky-300"
                        >
                          Open in APM <ExternalLink aria-hidden className="h-3.5 w-3.5" />
                        </a>
                      )}
                    </div>
                    <p className="text-caption mt-2 text-slate-500 dark:text-slate-400">
                      The trace id this run&apos;s load carried (traceparent/baggage); paste it into your APM to see exactly this run&apos;s traffic.
                    </p>
                  </CardContent>
                </Card>
              )}
            </TabPanel>

            {/* Time series: the per-second shape, re-used exactly -- the
                section owns its loading/error/empty states and its own
                requested-vs-achieved overlay hide rule. */}
            <TabPanel id="timeseries" active={tab}>
              <TimeSeriesSection runId={report.run_id} requested={report.requested} />
            </TabPanel>

            {/* Hides itself when the report carries no labels (task 8's hide
                rule), leaving an honest empty panel in its place. */}
            <TabPanel id="labels" active={tab}>
              <LabelsTable labels={report.labels} />
            </TabPanel>

            <TabPanel id="errors" active={tab}>
              <Card>
                <CardHeader>
                  <CardTitle>Error signatures</CardTitle>
                </CardHeader>
                <CardContent>
                  {!report.errors || report.errors.length === 0 ? (
                    <p className="text-body-sm">No failures recorded.</p>
                  ) : (
                    <ul className="space-y-3">
                      {report.errors.map((e, i) => (
                        <li key={i} className="rounded-lg border border-slate-200 p-3 dark:border-slate-700">
                          <div className="flex items-center justify-between gap-2">
                            <span className="text-body-sm font-medium text-slate-900 dark:text-white">{e.label}</span>
                            <span className="text-caption text-slate-500 dark:text-slate-400">
                              {e.side} · {e.count} {e.count === 1 ? 'occurrence' : 'occurrences'}
                              {e.response_code ? ` · ${e.response_code}` : ''}
                            </span>
                          </div>
                          {e.exemplars && e.exemplars.length > 0 && (
                            <p className="text-caption mt-1 text-slate-500 dark:text-slate-400">{e.exemplars[0]}</p>
                          )}
                        </li>
                      ))}
                    </ul>
                  )}
                </CardContent>
              </Card>
            </TabPanel>

            {/* Config: what the deployment asked for, as the run saw it. */}
            <TabPanel id="config" active={tab}>
              <Card>
                <CardHeader>
                  <CardTitle>Requested configuration</CardTitle>
                </CardHeader>
                <CardContent className="grid grid-cols-1 gap-6 sm:grid-cols-2">
                  <LoadStat label="Requested load" load={report.requested} />
                  <div className="space-y-3">
                    <div>
                      <p className="text-caption font-medium text-slate-500 dark:text-slate-400">Engine</p>
                      <p className="text-body-sm text-slate-900 dark:text-white">{report.engine ?? 'default'}</p>
                    </div>
                    <div>
                      <p className="text-caption font-medium text-slate-500 dark:text-slate-400">Cluster</p>
                      <p className="text-body-sm text-slate-900 dark:text-white">{report.cluster ?? 'default'}</p>
                    </div>
                    <div>
                      <p className="text-caption font-medium text-slate-500 dark:text-slate-400">Duration</p>
                      <p className="text-body-sm text-slate-900 dark:text-white">
                        {report.requested.duration_seconds ? `${report.requested.duration_seconds}s` : 'until stopped'}
                      </p>
                    </div>
                  </div>
                </CardContent>
              </Card>
            </TabPanel>

            <TabPanel id="objects" active={tab}>
              <ShardObjects report={report} />
            </TabPanel>
          </div>
        </>
      )}
      {shareOpen && report && <ShareRunModal runId={report.run_id} onClose={() => setShareOpen(false)} />}
    </div>
  );
}
