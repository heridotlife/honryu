import { useEffect, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { ExternalLink, Download } from 'lucide-react';
import Button from '../components/ui/Button';
import Card, { CardContent, CardHeader, CardTitle } from '../components/ui/Card';
import ClusterBadge from '../components/ui/ClusterBadge';
import CopyButton from '../components/ui/CopyButton';
import CopyLink from '../components/CopyLink';
import EngineBadge from '../components/ui/EngineBadge';
import Input from '../components/ui/Input';
import OutcomeBadge from '../components/ui/OutcomeBadge';
import LabelsTable from '../components/LabelsTable';
import { ApiError } from '../api/client';
import { apiClient } from '../api/client';
import { getRunReport, getShardConfig, getShardLog, listExecutionReports } from '../api/reports';
import type { Load, Report } from '../api/reports';
import { fetchSeries } from '../api/series';
import type { SeriesPoint } from '../api/series';
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
  const [executionId, setExecutionId] = useState('');
  const [reports, setReports] = useState<Report[] | null>(null);
  const [loadedExecutionId, setLoadedExecutionId] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

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

      <Card>
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
 * The run's per-second shape once the series is in memory: VUs and RPS on
 * one axis (the plan's dual-series chart; the legend names each unit),
 * error rate, and latency with a percentile selector that switches the
 * plotted series client-side -- every percentile arrived in the same
 * fetch, so switching costs nothing.
 */
function TimeSeriesCharts({ points, pct, onPct }: { points: SeriesPoint[]; pct: string; onPct: (p: string) => void }) {
  const available = LATENCY_PERCENTILES.filter((p) => points.some((pt) => pt.latency?.[p] !== undefined));
  const effective = available.includes(pct) ? pct : available[available.length - 1] ?? pct;
  const latencyPoints = points
    .filter((p) => p.latency?.[effective] !== undefined)
    .map((p) => ({ x: p.ts, y: (p.latency as Record<string, number>)[effective] * 1000 }));

  return (
    <div className="space-y-6">
      <div data-testid="chart-vus-rps">
        <p className="text-caption mb-2 font-medium text-slate-500 dark:text-slate-400">Concurrency and throughput</p>
        <TimeSeriesChart
          xType="time"
          series={[
            { name: 'VUs', color: 'text-sky-500', points: points.map((p) => ({ x: p.ts, y: p.vus })) },
            { name: 'RPS', color: 'text-amber-500', points: points.map((p) => ({ x: p.ts, y: p.rps })) },
          ]}
        />
      </div>
      <div data-testid="chart-errors">
        <p className="text-caption mb-2 font-medium text-slate-500 dark:text-slate-400">Error rate</p>
        <TimeSeriesChart
          xType="time"
          yLabel="%"
          series={[{ name: 'error %', color: 'text-rose-500', points: points.map((p) => ({ x: p.ts, y: p.err_pct })) }]}
        />
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

function ReportDetail({ runId }: { runId: string }) {
  const navigate = useNavigate();
  const [report, setReport] = useState<Report | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [apmTemplate] = useState<string>(() => loadApmTemplate());

  useEffect(() => {
    const id = Number(runId);
    if (!Number.isInteger(id) || id <= 0) {
      setError('Invalid run id.');
      return;
    }
    setError(null);
    setReport(null);
    getRunReport(id)
      .then(setReport)
      .catch((err: unknown) => setError(err instanceof ApiError ? err.message : 'Failed to load report.'));
  }, [runId]);

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
          <Card>
            <CardHeader className="flex flex-row items-center justify-between">
              <div className="flex flex-wrap items-center gap-2">
                <CardTitle>Run #{report.run_id}</CardTitle>
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
                <CopyLink />
              </div>
              <OutcomeBadge outcome={report.outcome} />
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
              {report.engine && (
                <div>
                  <p className="text-caption font-medium text-slate-500 dark:text-slate-400">Engine</p>
                  <p className="text-body-sm text-slate-900 dark:text-white">{report.engine}</p>
                </div>
              )}
              {report.cluster && (
                <div>
                  <p className="text-caption font-medium text-slate-500 dark:text-slate-400">Cluster</p>
                  <p className="text-body-sm text-slate-900 dark:text-white">{report.cluster}</p>
                </div>
              )}
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

          <Card>
            <CardHeader>
              <CardTitle>Load</CardTitle>
            </CardHeader>
            <CardContent className="grid grid-cols-1 gap-6 sm:grid-cols-2">
              <LoadStat label="Requested" load={report.requested} />
              <LoadStat label="Achieved" load={report.achieved} />
            </CardContent>
          </Card>

          <TimeSeriesSection runId={report.run_id} requested={report.requested} />

          {/* Hides itself when the report carries no labels (task 8's hide rule). */}
          <LabelsTable labels={report.labels} />

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
                  )                  )}
                </ul>
              )}
            </CardContent>
          </Card>

          <ShardObjects report={report} />
        </>
      )}
    </div>
  );
}
