import { Fragment, useEffect, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import Button from '../components/ui/Button';
import Card, { CardContent, CardHeader, CardTitle } from '../components/ui/Card';
import { ApiError, errorDetails } from '../api/client';
import { getExecutionInfo, getExecutionStatus, getScenarioPodLog } from '../api/status';
import { listExecutionReports, type Report } from '../api/reports';
import { getExecutionTrend, getErrorSignatures } from '../api/trends';
import type { ErrorSignatureHistory, SignatureGroupBy, TrendPoint } from '../api/trends';
import { sortSignatureGroups } from './ReportsTrend';
import TaurusEditor from '../components/TaurusEditor';
import CapacityPanel, { isCalibrationExecution } from '../components/CapacityPanel';
import type { ExecutionInfo, ExecutionStatus, Phase, ScenarioStatus } from '../api/status';
import type { LiveSeriesPoint } from '../lib/liveSeries';
import { deployExecution, purgeExecution, stopExecution, triggerExecution } from '../api/lifecycle';
import { useSession } from '../hooks/useSession';
import ExecutionConfigCard from '../components/ExecutionConfigCard';
import { useLiveSeries } from '../hooks/useLiveSeries';
import TimeSeriesChart from '../components/charts/TimeSeriesChart';
import ClusterBadge from '../components/ui/ClusterBadge';
import EngineBadge from '../components/ui/EngineBadge';
import CopyLink from '../components/CopyLink';
import ActionErrorDetails from '../components/ActionErrorDetails';
import CalibrateScenarioModal from '../components/CalibrateScenarioModal';

const phaseClasses: Record<Phase, string> = {
  idle: 'bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-300',
  deployed: 'bg-sky-100 text-sky-800 dark:bg-sky-900/30 dark:text-sky-300',
  running: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300',
};

function PhaseBadge({ phase }: { phase: Phase }) {
  return (
    <span className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${phaseClasses[phase]}`}>
      {phase}
    </span>
  );
}

/**
 * Engines still missing for a scenario: wanted minus deployed, floored at
 * zero (a terminating engine can briefly report one extra). Drives the
 * pending-engines mark on the scenario rows.
 */
export function engineShortfall(s: Pick<ScenarioStatus, 'engines' | 'engines_deployed'>): number {
  return Math.max(0, s.engines - s.engines_deployed);
}

function StatCard({ label, value, caption }: { label: string; value: string; caption: string }) {
  return (
    <div>
      <p className="text-caption font-medium text-slate-500 dark:text-slate-400">{label}</p>
      <p className="text-heading-md text-slate-900 dark:text-white">{value}</p>
      <p className="text-caption text-slate-500 dark:text-slate-400">{caption}</p>
    </div>
  );
}

/**
 * Which lifecycle actions the hub offers, per phase and per engine
 * reachability. The matrix is the R2 contract: deploy when idle, trigger
 * when deployed (and engines reachable), stop while running, purge whenever
 * something is deployed or running. Disabled buttons say WHY.
 */
export function phaseControls(phase: Phase | null, enginesReachable: boolean): Array<{ action: 'deploy' | 'trigger' | 'stop' | 'purge'; enabled: boolean }> {
  switch (phase) {
    case 'idle':
      return [
        { action: 'deploy', enabled: true },
        { action: 'trigger', enabled: false },
        { action: 'stop', enabled: false },
        { action: 'purge', enabled: false },
      ];
    case 'deployed':
      return [
        { action: 'deploy', enabled: false },
        { action: 'trigger', enabled: enginesReachable },
        { action: 'stop', enabled: false },
        { action: 'purge', enabled: true },
      ];
    case 'running':
      return [
        { action: 'deploy', enabled: false },
        { action: 'trigger', enabled: false },
        { action: 'stop', enabled: true },
        { action: 'purge', enabled: true },
      ];
    default:
      // Status not loaded yet: nothing to click.
      return [
        { action: 'deploy', enabled: false },
        { action: 'trigger', enabled: false },
        { action: 'stop', enabled: false },
        { action: 'purge', enabled: false },
      ];
  }
}

/** What each lifecycle action costs in RBAC terms — the audit table's row. */
const controlPermission = {
  deploy: { resource: 'run', action: 'create' },
  trigger: { resource: 'run', action: 'create' },
  stop: { resource: 'run', action: 'update' },
  purge: { resource: 'run', action: 'delete' },
} as const;

/**
 * Phase says WHEN a control is offered; the session says WHETHER this
 * caller may have it at all (phase 20, AC14). A tenant_viewer holds
 * run:read/list only, so every control drops out — the UI must not render
 * a Deploy/Trigger/Stop/Delete button the server would 403.
 */
export function gateControls(
  controls: Array<{ action: 'deploy' | 'trigger' | 'stop' | 'purge'; enabled: boolean }>,
  can: (resource: string, action: string) => boolean
): Array<{ action: 'deploy' | 'trigger' | 'stop' | 'purge'; enabled: boolean }> {
  return controls.filter(({ action }) => {
    const perm = controlPermission[action];
    return can(perm.resource, perm.action);
  });
}

/** Outcome-to-color mapping for the report rows. */
export function outcomeBadge(outcome: Report['outcome']): string {
  switch (outcome) {
    case 'passed':
      return 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300';
    case 'failed':
      return 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300';
    case 'aborted':
      return 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300';
    default:
      return 'bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-300';
  }
}

/** UTC timestamp shortened to a locale-less "MM-DD HH:mm" for dense rows. */
export function shortTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return iso;
  }
  const p = (n: number) => String(n).padStart(2, '0');
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** Outcome→colour for the recent-runs strip's pills (phase 33): passed
 * green, failed rose, everything else (aborted included) neutral slate —
 * deliberately not OutcomeBadge's amber: the strip is a scan affordance
 * where failed must read as red at arm's length. */
function trendPillClass(outcome: string): string {
  switch (outcome) {
    case 'passed':
      return 'bg-emerald-100 text-emerald-800 hover:bg-emerald-200 dark:bg-emerald-900/30 dark:text-emerald-300 dark:hover:bg-emerald-900/50';
    case 'failed':
      return 'bg-rose-100 text-rose-800 hover:bg-rose-200 dark:bg-rose-900/30 dark:text-rose-300 dark:hover:bg-rose-900/50';
    default:
      return 'bg-slate-200 text-slate-700 hover:bg-slate-300 dark:bg-slate-700 dark:text-slate-300 dark:hover:bg-slate-600';
  }
}

/** Styling for the strip's REGRESSED mark: it must read on top of any
 * outcome pill, so it carries its own solid rose fill. */
const regressedBadgeClass =
  'inline-flex items-center rounded-full bg-rose-600 px-1.5 py-px text-[10px] font-semibold tracking-wide text-white';

/** The recent-runs strip under the execution header (phase 33): the last
 * ten trend points as pills — run id on the outcome colour, plus a
 * REGRESSED mark when the backend's regression rule tripped — so the
 * execution's recent health reads in one glance, before any scrolling.
 * Points arrive newest-first and render that way; each pill deep-links
 * the run's report. Hidden when the trend is empty or the fetch fails:
 * a strip with nothing to say is noise, and the Past runs card below
 * still lists everything. */
function TrendStrip({ executionId }: { executionId: number }) {
  const [points, setPoints] = useState<TrendPoint[] | null>(null);

  useEffect(() => {
    let cancelled = false;
    setPoints(null);
    getExecutionTrend(executionId, 10)
      .then((t) => {
        // regressed is omitempty on the wire: absent simply means false.
        if (!cancelled && t.points.length > 0) {
          setPoints(t.points);
        }
      })
      .catch(() => {
        if (!cancelled) setPoints(null);
      });
    return () => {
      cancelled = true;
    };
  }, [executionId]);

  if (points === null) {
    return null;
  }
  return (
    <div className="flex flex-wrap items-center gap-2" data-testid="trend-strip">
      <span className="text-caption font-medium text-slate-500 dark:text-slate-400">Recent runs</span>
      {points.map((p) => (
        <Link
          key={p.run_id}
          to={`/reports/${p.run_id}`}
          data-testid={`trend-pill-${p.run_id}`}
          className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium transition-colors focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 ${trendPillClass(p.outcome)}`}
        >
          #{p.run_id}
          {p.regressed && (
            <span
              data-testid={`trend-regressed-${p.run_id}`}
              title="missed target QPS vs previous hit"
              className={regressedBadgeClass}
            >
              REGRESSED
            </span>
          )}
        </Link>
      ))}
    </div>
  );
}

/** The live chart's latency percentiles, in display order (same selector concept as Reports). */
const LIVE_PERCENTILES = ['50', '95', '99'] as const;
type LivePercentile = (typeof LIVE_PERCENTILES)[number];

/** Which LiveSeriesPoint field each percentile pill plots. */
const latencyField: Record<LivePercentile, 'p50' | 'p95' | 'p99'> = {
  '50': 'p50',
  '95': 'p95',
  '99': 'p99',
};

/**
 * Failure history across runs (phase 37): the execution's error-signature
 * history from the phase 9 endpoint, surfaced on the execution page beside
 * the per-run record so "this label keeps failing" reads before opening any
 * single report. Group-by mirrors ReportsTrend's SignatureSection (label |
 * response code); per the domain docs a group's run count is NEVER summed
 * from its rows (a run hitting two response codes under one label would
 * double-count) -- the group row shows only the safe total, and the
 * runs-affected column belongs to the leaf rows it expands to.
 */
function FailureHistoryCard({ executionId }: { executionId: number }) {
  const [by, setBy] = useState<SignatureGroupBy>('label');
  const [history, setHistory] = useState<ErrorSignatureHistory | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());

  useEffect(() => {
    let cancelled = false;
    setHistory(null);
    setError(null);
    getErrorSignatures(executionId, by)
      .then((h) => {
        if (!cancelled) setHistory(h);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof ApiError ? err.message : 'Failed to load failure history.');
      });
    return () => {
      cancelled = true;
    };
  }, [executionId, by]);

  // A new grouping axis re-collapses everything: keys from one axis are not
  // keys under the other (a label is not a response code).
  useEffect(() => {
    setExpanded(new Set());
  }, [by]);

  const groups = history ? sortSignatureGroups(history.groups) : [];
  const toggle = (key: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) {
        next.delete(key);
      } else {
        next.add(key);
      }
      return next;
    });
  };

  return (
    <Card padding="none" data-testid="signature-history">
      <CardHeader className="flex flex-row items-center justify-between gap-4">
        <CardTitle>Failure history across runs</CardTitle>
        <div
          role="group"
          aria-label="Group failures by"
          className="flex gap-1 rounded-lg border border-slate-200 p-1 dark:border-slate-700"
        >
          {(
            [
              ['label', 'by label', 'signature-group-by-label'],
              ['code', 'by response code', 'signature-group-by-code'],
            ] as const
          ).map(([axis, label, testid]) => (
            <button
              key={axis}
              type="button"
              data-testid={testid}
              aria-pressed={by === axis}
              onClick={() => setBy(axis)}
              className={`min-h-[32px] rounded-md px-3 py-1 text-caption font-medium transition-colors focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 ${
                by === axis
                  ? 'bg-sky-100 text-sky-800 dark:bg-sky-900/40 dark:text-sky-300'
                  : 'text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'
              }`}
            >
              {label}
            </button>
          ))}
        </div>
      </CardHeader>
      {error ? (
        <p className="text-body-sm p-6 text-red-600 dark:text-red-400" role="alert">
          {error}
        </p>
      ) : history === null ? (
        <p className="text-body-sm p-6 text-slate-500 dark:text-slate-400">Loading failure history…</p>
      ) : groups.length === 0 ? (
        <p className="text-body-sm p-6 text-slate-500 dark:text-slate-400">
          No failures recorded across this execution&apos;s runs.
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-body-sm">
            <thead>
              <tr className="text-caption border-b border-slate-200 text-slate-500 dark:border-slate-700 dark:text-slate-400">
                <th scope="col" className="px-4 py-2 font-medium">Key</th>
                <th scope="col" className="px-3 py-2 font-medium">Total failures</th>
                <th scope="col" className="px-4 py-2 font-medium">Runs affected</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {groups.map((g) => (
                <Fragment key={g.key}>
                  <tr>
                    <td className="px-4 py-2 font-medium text-slate-900 dark:text-white">{g.key}</td>
                    <td className="px-3 py-2 whitespace-nowrap">{g.total_count}</td>
                    <td className="px-4 py-2">
                      <button
                        type="button"
                        aria-expanded={expanded.has(g.key)}
                        onClick={() => toggle(g.key)}
                        className="rounded text-caption font-medium text-sky-600 hover:underline focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:text-sky-400"
                      >
                        {expanded.has(g.key) ? 'Hide' : 'Show'} {g.rows.length === 1 ? 'signature' : 'signatures'} ({g.rows.length})
                      </button>
                    </td>
                  </tr>
                  {expanded.has(g.key) &&
                    g.rows.map((row, i) => (
                      <tr key={i} className="bg-slate-50 dark:bg-slate-800/40">
                        <td className="px-8 py-2 text-caption text-slate-700 dark:text-slate-300">
                          {row.side} | {row.response_code ?? '—'} | {row.label}
                        </td>
                        <td className="px-3 py-2 text-caption whitespace-nowrap">{row.total_count}</td>
                        <td className="px-4 py-2 text-caption whitespace-nowrap">
                          {row.run_count} {row.run_count === 1 ? 'run' : 'runs'}
                        </td>
                      </tr>
                    ))}
                </Fragment>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

/** Shared pill styling for the percentile selector (Reports' pctPill, same house style). */
function pctPill(selected: boolean): string {
  return `${
    selected
      ? 'bg-sky-600 text-white'
      : 'bg-slate-100 text-slate-600 hover:bg-slate-200 dark:bg-slate-700/50 dark:text-slate-300 dark:hover:bg-slate-700'
  } focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500`;
}

/**
 * The live run chart: VUs and RPS over run-relative seconds, plus the
 * chosen latency percentile. Same two-series pattern as Reports' charts
 * (sky/amber/emerald), except the x axis is seconds since the first
 * received event -- a plain number axis, not xType="time", because t is
 * not a Unix timestamp and the live view charts run-relative time. The
 * wire's latencies are seconds; the chart reads ms, like Reports.
 */
function LiveCharts({ series, pct, onPct }: { series: LiveSeriesPoint[]; pct: LivePercentile; onPct: (p: LivePercentile) => void }) {
  return (
    <div className="space-y-6">
      <div data-testid="live-chart-vus-rps">
        <p className="text-caption mb-2 font-medium text-slate-500 dark:text-slate-400">Concurrency and throughput</p>
        <TimeSeriesChart
          series={[
            { name: 'VUs', color: 'text-sky-500', points: series.map((p) => ({ x: p.t, y: p.vus })) },
            { name: 'RPS', color: 'text-amber-500', points: series.map((p) => ({ x: p.t, y: p.rps })) },
          ]}
        />
      </div>
      <div data-testid="live-chart-latency">
        <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
          <p className="text-caption font-medium text-slate-500 dark:text-slate-400">Response time</p>
          <div className="flex gap-1" role="group" aria-label="Latency percentile">
            {LIVE_PERCENTILES.map((p) => (
              <button
                key={p}
                type="button"
                data-testid={`pct-${p}`}
                aria-pressed={p === pct}
                onClick={() => onPct(p)}
                className={`rounded-full px-3 py-1 text-caption font-medium transition-colors ${pctPill(p === pct)}`}
              >
                p{p}
              </button>
            ))}
          </div>
        </div>
        <TimeSeriesChart
          yLabel="ms"
          series={[
            { name: `p${pct}`, color: 'text-emerald-500', points: series.map((p) => ({ x: p.t, y: p[latencyField[pct]] * 1000 })) },
          ]}
        />
      </div>
      <p className="text-caption text-slate-500 dark:text-slate-400">
        Seconds since the first received event; each bucket aggregates the events of one second.
      </p>
    </div>
  );
}

/** A watched execution's live phase, deployment status, rolling metrics, and lifecycle controls. Deep-linkable: the execution id comes from the route. */
export default function Execution() {
  const { id } = useParams<{ id: string }>();
  const { can } = useSession();
  const navigate = useNavigate();
  const executionId = Number(id);
  const validId = Number.isInteger(executionId) && executionId > 0;

  const [info, setInfo] = useState<ExecutionInfo | null>(null);
  const [status, setStatus] = useState<ExecutionStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  // The structured half of the last action error (phase 24): quota
  // numbers / remediation hint when the server's envelope carried them.
  const [actionDetails, setActionDetails] = useState<Record<string, unknown> | null>(null);
  const [busyAction, setBusyAction] = useState<string | null>(null);
  const [reports, setReports] = useState<Report[] | null>(null);
  const [logsScenario, setLogsScenario] = useState<number | null>(null);
  const [logText, setLogText] = useState<string>('');
  // Phase 39: which scenario row has its create-calibration dialog open
  // (null = none). The dialog mints a NEW calibrate_engine execution -- the
  // only UI path that ever creates one.
  const [calibrateFor, setCalibrateFor] = useState<number | null>(null);
  // The live stream (SSE subscribe, event window, per-second recompute)
  // lives in the hook; this page keeps only the rolling numbers it feeds.
  const { series, connected, stats, reset: resetLive } = useLiveSeries(executionId, validId);
  const [pct, setPct] = useState<LivePercentile>('95');

  useEffect(() => {
    if (!validId) {
      return;
    }
    let cancelled = false;
    // The execution's setup (engine kind, cluster) is immutable -- fetched
    // once. The lifecycle snapshot changes as engines deploy, so it polls.
    getExecutionInfo(executionId)
      .then((i) => {
        if (!cancelled) setInfo(i);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof ApiError ? err.message : 'Failed to load execution.');
      });
    const loadStatus = () => {
      getExecutionStatus(executionId)
        .then((s) => {
          if (!cancelled) setStatus(s);
        })
        .catch((err: unknown) => {
          if (!cancelled) setError(err instanceof ApiError ? err.message : 'Failed to load status.');
        });
    };
    loadStatus();
    const poll = setInterval(loadStatus, 10_000);
    // Past runs: fetched once on entry (the list is bounded by the
    // server's default limit; a finished run's report is immutable).
    listExecutionReports(executionId)
      .then((rows) => {
        if (!cancelled) setReports(rows);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof ApiError ? err.message : 'Failed to load reports.');
      });
    return () => {
      cancelled = true;
      clearInterval(poll);
    };
  }, [executionId, validId]);

  // Engine-log tail: re-fetched every 5s while a scenario is selected, and
  // once on selection. The endpoint streams the pod's CURRENT output, so
  // polling (not SSE) is the honest refresh model here.
  useEffect(() => {
    if (logsScenario === null || !validId) {
      return;
    }
    let alive = true;
    const load = () => {
      getScenarioPodLog(executionId, logsScenario)
        .then((text) => {
          if (alive) setLogText(text);
        })
        .catch((err: unknown) => {
          if (alive) setLogText(err instanceof ApiError ? err.message : 'failed to fetch logs');
        });
    };
    load();
    const t = setInterval(load, 5_000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [executionId, logsScenario, validId]);

  const runAction = (action: 'deploy' | 'trigger' | 'stop' | 'purge') => {
    setBusyAction(action);
    setActionError(null);
    setActionDetails(null);
    const fn = { deploy: deployExecution, trigger: triggerExecution, stop: stopExecution, purge: purgeExecution }[action];
    fn(executionId)
      .then((message) => {
        setActionError(null);
        setActionDetails(null);
        // The mutation succeeded; refresh the snapshot immediately rather
        // than waiting for the next poll tick.
        getExecutionStatus(executionId).then((s) => setStatus(s));
        if (action === 'purge') {
          // Purged: the hub's live view resets to the idle snapshot.
          resetLive();
        }
        void message;
      })
      .catch((err: unknown) => {
        // A 409/429 surfaces the server's message verbatim -- that is the
        // whole point of the typed ApiError, not a generic failure string.
        // Phase 24: keep the structured details (quota numbers, hint) for
        // the second render line instead of flattening to message only.
        setActionError(err instanceof ApiError ? err.message : `${action} failed.`);
        setActionDetails(errorDetails(err));
      })
      .finally(() => setBusyAction(null));
  };

  if (!validId) {
    return (
      <div className="space-y-4">
        <h1 className="text-display-sm text-slate-900 dark:text-white">Execution</h1>
        <p className="text-body-sm text-red-600 dark:text-red-400">Invalid execution id.</p>
      </div>
    );
  }

  const enginesReachable = status?.status.every((s) => s.engines_reachable) ?? false;
  const controls = gateControls(phaseControls(status?.phase ?? null, enginesReachable), can);
  // Phase 39's Calibrate action needs an engine to name (the backend rejects
  // an engineless calibration) and the session's execution:create.
  const canCalibrate = !!info?.engine && can('execution', 'create');
  // A scenario's display name: the config's test name doubles as it (the
  // NewTest flow names test and scenario the same); the id is the fallback.
  const scenarioName = (scenarioId: number): string =>
    info?.load_profile.find((t) => t.scenario_id === scenarioId)?.name ?? `scenario ${scenarioId}`;
  // The capacity key every calibration surface on this page assumes: the
  // execution's engine at the house-default pod size (the same defaults
  // CalibrateScenarioModal pre-fills, phase 39). Absent while info loads
  // or on engine-less executions.
  const capacityKey = info?.engine
    ? { engine: info.engine, cpu: '500m', memory: '512Mi' }
    : undefined;

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-display-sm text-slate-900 dark:text-white">Execution #{executionId}</h1>
          <p className="text-body-sm mt-1 text-slate-500 dark:text-slate-400">
            Deployment status, lifecycle controls, and rolling metrics.
          </p>
        </div>
        {/* The page is deep-linkable (/executions/{id}); the copy-link hands
            that URL to a colleague. */}
        <CopyLink />
      </div>
      <TrendStrip executionId={executionId} />
      {error && (
        <p className="text-sm text-red-600 dark:text-red-400" role="alert">
          {error}
        </p>
      )}
      {status && (
        <>
          <Card>
            <CardHeader className="flex flex-row items-center justify-between">
              <div className="flex flex-wrap items-center gap-2">
                <CardTitle>Execution #{executionId}</CardTitle>
                {info?.engine && <EngineBadge engine={info.engine} />}
                {info?.cluster && <ClusterBadge cluster={info.cluster} />}
              </div>
              <PhaseBadge phase={status.phase} />
            </CardHeader>
            <CardContent className="space-y-6">
              <div className="grid grid-cols-1 gap-6 sm:grid-cols-3">
                <StatCard
                  label="Throughput"
                  value={`${stats.throughput.toFixed(1)}/s`}
                  caption="samples/sec, trailing 10s"
                />
                <StatCard
                  label="Error rate"
                  value={`${(stats.errorRate * 100).toFixed(1)}%`}
                  caption="trailing 10s"
                />
                <StatCard
                  label="Latency (p50)"
                  value={stats.latencySeconds !== null ? `${(stats.latencySeconds * 1000).toFixed(0)} ms` : '—'}
                  caption="most recent sample"
                />
              </div>
              <div className="flex flex-wrap items-center gap-2">
                {controls.map(({ action, enabled }) => (
                  <Button
                    key={action}
                    variant={action === 'stop' || action === 'purge' ? 'outline' : 'primary'}
                    disabled={!enabled || busyAction !== null}
                    onClick={() => runAction(action)}
                  >
                    {busyAction === action ? 'Working…' : action.charAt(0).toUpperCase() + action.slice(1)}
                  </Button>
                ))}
                {controls.length === 0 && (
                  <p className="text-sm text-slate-500 dark:text-slate-400">
                    Your role is read-only here: no lifecycle controls.
                  </p>
                )}
              </div>
              {actionError && (
                <div>
                  <p className="text-sm text-red-600 dark:text-red-400" role="alert">
                    {actionError}
                  </p>
                  <ActionErrorDetails details={actionDetails} />
                </div>
              )}
            </CardContent>
          </Card>
          {(status.phase === 'running' || series.length > 0) && (
            <section aria-labelledby="live-heading" data-testid="live-section">
              <h3 id="live-heading" className="text-lg font-semibold text-slate-900 sm:text-xl dark:text-white">
                Live
              </h3>
              <div className="mt-3">
                {!connected && (
                  <p className="text-body-sm text-amber-600 dark:text-amber-400" role="status" data-testid="live-disconnected">
                    Stream disconnected — reconnecting…
                  </p>
                )}
                {series.length === 0 ? (
                  connected && (
                    <p className="text-body-sm text-slate-500 dark:text-slate-400" data-testid="live-idle">
                      Waiting for first events…
                    </p>
                  )
                ) : (
                  <LiveCharts series={series} pct={pct} onPct={setPct} />
                )}
              </div>
            </section>
          )}
          <Card padding="none">
            {status.status.length === 0 ? (
              <p className="text-body-sm p-6 text-slate-500 dark:text-slate-400">No scenarios deployed yet.</p>
            ) : (
              <ul className="divide-y divide-slate-200 dark:divide-slate-700">
                {status.status.map((sc) => (
                  <li
                    key={sc.scenario_id}
                    className="flex min-h-[44px] flex-col gap-2 p-4 sm:flex-row sm:items-center sm:justify-between"
                  >
                    <div className="flex items-center gap-3">
                      <span className="text-body-sm font-medium text-slate-900 dark:text-white">
                        Scenario {sc.scenario_id}
                      </span>
                      {sc.in_progress && (
                        <span className="inline-flex items-center rounded-full bg-emerald-100 px-2.5 py-0.5 text-xs font-medium text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300">
                          in progress
                        </span>
                      )}
                      {!sc.engines_reachable && (
                        <span className="inline-flex items-center rounded-full bg-amber-100 px-2.5 py-0.5 text-xs font-medium text-amber-800 dark:bg-amber-900/30 dark:text-amber-300">
                          unreachable
                        </span>
                      )}
                    </div>
                    <div className="flex items-center gap-3">
                      <div className="text-caption text-slate-500 dark:text-slate-400">
                        {sc.engines_deployed}/{sc.engines} engines deployed
                        {engineShortfall(sc) > 0 && (
                          <span className="text-amber-600 dark:text-amber-400"> · {engineShortfall(sc)} pending</span>
                        )}
                      </div>
                      {canCalibrate && (
                        <Button
                          size="sm"
                          variant="outline"
                          data-testid="calibrate-scenario-btn"
                          onClick={() => setCalibrateFor(sc.scenario_id)}
                        >
                          Calibrate
                        </Button>
                      )}
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </Card>
          {/* Phase 39: the Capacity card mounts ONLY on calibrate_engine
              executions (isCalibrationExecution). Before Kind rode the wire
              this gate was just info?.engine, so a normal execution's
              Calibrate button could only ever earn a 400. */}
          {isCalibrationExecution(info) && capacityKey && (
            <CapacityPanel
              scenarioId={status.status[0]?.scenario_id ?? 0}
              executionId={executionId}
              keyInfo={capacityKey}
              targetQPS={100}
            />
          )}
          {status.phase === 'idle' && (
            <ExecutionConfigCard executionId={executionId} canUpdate={can('execution', 'update')} />
          )}
          <Card padding="none">
            <CardHeader>
              <CardTitle>Past runs</CardTitle>
            </CardHeader>
            {reports === null ? (
              <p className="text-body-sm p-6 text-slate-500 dark:text-slate-400">Loading reports…</p>
            ) : reports.length === 0 ? (
              <p className="text-body-sm p-6 text-slate-500 dark:text-slate-400">No runs yet.</p>
            ) : (
              <ul className="divide-y divide-slate-200 dark:divide-slate-700">
                {reports.map((rep) => (
                  <li key={rep.run_id} className="flex items-center justify-between p-4">
                    <div className="flex items-center gap-3">
                      <span
                        className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${outcomeBadge(rep.outcome)}`}
                      >
                        {rep.outcome}
                      </span>
                      <span className="text-caption text-slate-500 dark:text-slate-400">
                        {shortTime(rep.started_at)}
                      </span>
                    </div>
                    <Link
                      to={`/reports/${rep.run_id}`}
                      className="rounded text-sm font-medium text-sky-600 hover:underline focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:text-sky-400"
                    >
                      Report →
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </Card>
          {/* Cross-run failure history (phase 37): beside the per-run record
              -- a repeatedly failing label reads here without opening any
              single report. */}
          <FailureHistoryCard executionId={executionId} />
          <Card>
            <CardHeader>
              <CardTitle>Scenario editor</CardTitle>
            </CardHeader>
            <CardContent>
              {status.status.length === 0 ? (
                <p className="text-body-sm text-slate-500 dark:text-slate-400">
                  No scenarios deployed yet.
                </p>
              ) : (
                <TaurusEditor
                  key={status.status[0].scenario_id}
                  scenarioId={status.status[0].scenario_id}
                  capacityKey={capacityKey}
                />
              )}
            </CardContent>
          </Card>
          <Card padding="none">
            <CardHeader>
              <CardTitle>Engine logs</CardTitle>
            </CardHeader>
            {status.status.length === 0 ? (
              <p className="text-body-sm p-6 text-slate-500 dark:text-slate-400">
                No scenarios deployed yet.
              </p>
            ) : (
              <>
                <div className="flex flex-wrap gap-2 border-b border-slate-200 p-4 dark:border-slate-700">
                  {status.status.map((sc) => (
                    <Button
                      key={sc.scenario_id}
                      variant={logsScenario === sc.scenario_id ? 'primary' : 'outline'}
                      onClick={() => setLogsScenario(sc.scenario_id)}
                    >
                      Scenario {sc.scenario_id}
                    </Button>
                  ))}
                </div>
                {logsScenario !== null && (
                  <pre className="max-h-80 overflow-auto bg-slate-950 p-4 text-xs leading-5 text-slate-200">
                    {logText || '…'}
                  </pre>
                )}
              </>
            )}
          </Card>
        </>
      )}
      {/* Phase 39: the create-calibration dialog, per scenario row above.
          On success it navigates to the fresh execution's page, where the
          Capacity card (now gated on kind) lives. */}
      {calibrateFor !== null && info?.engine && (
        <CalibrateScenarioModal
          scenarioId={calibrateFor}
          scenarioName={scenarioName(calibrateFor)}
          projectId={info.project_id}
          engine={info.engine}
          onClose={() => setCalibrateFor(null)}
          onCreated={(executionId) => navigate(`/executions/${executionId}`)}
        />
      )}
    </div>
  );
}
