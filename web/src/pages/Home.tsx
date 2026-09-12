// /home -- the dashboard an authenticated session lands on (phase 52).
// First paint used to be the bare run list (the picker redirected to
// /reports); the audit called that out as "no answer to 'what is happening
// right now'". Home answers it from EXISTING endpoints only -- no new
// backend: the executions list (GET /api/executions), per-execution status
// snapshots (GET /api/executions/{id}/status) for a bounded probe, the
// newest execution's reports (GET /api/executions/{id}/reports?limit=1),
// and the run series (GET /api/runs/{id}/series). Every fetcher is the
// one the surface it deep-links to already uses; nothing is re-implemented
// here.
import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import Card from '../components/ui/Card';
import OutcomeBadge from '../components/ui/OutcomeBadge';
import Sparkline from '../components/Sparkline';
import { listExecutions, type ExecutionSummary } from '../api/executions';
import { getExecutionStatus } from '../api/status';
import { listExecutionReports, type Report } from '../api/reports';
import { fetchSeries, type SeriesPoint } from '../api/series';
import { useProjectSelection } from '../components/ProjectSwitcher';
import { executionDisplayName, formatRowTime } from '../lib/executionRow';

/** How many of the newest executions the active-runs probe asks statuses
 * for. The status endpoint is per-execution -- there is no batch endpoint
 * to reuse -- so the probe is bounded to the head of the newest-first list
 * rather than fanning out over every execution the caller can see. */
const ACTIVE_PROBE_LIMIT = 8;

/** Rows in the recent-executions mini-list. */
const RECENT_LIMIT = 5;

/** One KPI card: label, value, optional caption, and the deep page the
 * number is an entry point into. Every card links; a dashboard number
 * without its drill-down is a dead end. */
function KpiCard({
  label,
  value,
  caption,
  href,
  testid,
}: {
  label: string;
  value: string;
  caption?: string;
  href: string;
  testid: string;
}) {
  return (
    <Link
      to={href}
      data-testid={testid}
      className="flex min-h-[44px] flex-col rounded-2xl border border-slate-200 bg-white p-4 shadow-sm transition-colors hover:border-sky-400 hover:bg-sky-50/50 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:border-slate-700 dark:bg-slate-800 dark:hover:border-sky-500 dark:hover:bg-sky-950/20"
    >
      <span className="text-caption font-medium text-slate-500 dark:text-slate-400">{label}</span>
      <span className="text-heading-md mt-1 text-slate-900 dark:text-white">{value}</span>
      {caption !== undefined && <span className="text-caption mt-1 text-slate-500 dark:text-slate-400">{caption}</span>}
    </Link>
  );
}

/** Skeleton block matching the KPI/list geometry while the first fetches
 * are in flight. animate-pulse is covered by globals.css's global
 * prefers-reduced-motion clamp, like every other animated surface. */
function HomeSkeleton() {
  return (
    <div data-testid="home-loading" className="space-y-6">
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="h-24 animate-pulse rounded-2xl bg-slate-100 dark:bg-slate-700/50" />
        ))}
      </div>
      <div className="h-64 animate-pulse rounded-2xl bg-slate-100 dark:bg-slate-700/50" />
    </div>
  );
}

export default function Home() {
  // The same project scope the Executions and Reports lists honour (phase
  // 32): a stored selection narrows the mini-list and the KPIs alike.
  const { selectedId } = useProjectSelection();
  const [executions, setExecutions] = useState<ExecutionSummary[] | null>(null);
  // The newest run we know of: the newest execution's newest report. Drives
  // the "Last run" and p95 KPIs and the throughput sparkline.
  const [latest, setLatest] = useState<Report | null>(null);
  const [activeRuns, setActiveRuns] = useState<number | null>(null);
  const [series, setSeries] = useState<SeriesPoint[] | null>(null);

  useEffect(() => {
    let alive = true;
    listExecutions()
      .then(async (rows) => {
        if (!alive) {
          return;
        }
        setExecutions(rows);
        if (rows.length === 0) {
          setLatest(null);
          setActiveRuns(0);
          return;
        }
        // The newest execution's newest report ("most recent first" is the
        // endpoint's contract, so limit=1 is the latest run).
        listExecutionReports(rows[0].id, 1)
          .then((reports) => {
            if (alive) {
              setLatest(reports[0] ?? null);
            }
          })
          .catch(() => {
            if (alive) {
              setLatest(null);
            }
          });
        // Bounded live probe: a run is active while its execution's phase
        // is "running". A failed snapshot counts as inactive -- the KPI
        // degrades honestly instead of taking the page down.
        const probe = rows.slice(0, ACTIVE_PROBE_LIMIT);
        Promise.all(
          probe.map((e) =>
            getExecutionStatus(e.id)
              .then((s) => s.phase === 'running')
              .catch(() => false)
          )
        ).then((flags) => {
          if (alive) {
            setActiveRuns(flags.filter(Boolean).length);
          }
        });
      })
      .catch(() => {
        // List failed: everything numeric degrades to "—" below.
        if (alive) {
          setExecutions([]);
        }
      });
    return () => {
      alive = false;
    };
  }, []);

  // The sparkline follows the latest run's id; a run without series data
  // (finalised before the series store existed) degrades to a note.
  useEffect(() => {
    if (latest === null) {
      setSeries(null);
      return;
    }
    let alive = true;
    fetchSeries(latest.run_id)
      .then((s) => {
        if (alive) {
          setSeries(s.points);
        }
      })
      .catch(() => {
        if (alive) {
          setSeries([]);
        }
      });
    return () => {
      alive = false;
    };
  }, [latest]);

  const scoped =
    executions === null
      ? null
      : selectedId !== ''
        ? executions.filter((e) => String(e.project_id) === selectedId)
        : executions;
  const recent = scoped === null ? [] : scoped.slice(0, RECENT_LIMIT);
  const latestP95 = latest?.latency?.['95'];
  const sparkValues = (series ?? []).map((p) => p.rps);

  if (executions === null) {
    return (
      <div className="space-y-6">
        <h1 className="text-display-sm text-slate-900 dark:text-white">Home</h1>
        <HomeSkeleton />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-display-sm text-slate-900 dark:text-white">Home</h1>
        <p className="text-body-sm mt-1 text-slate-500 dark:text-slate-400">
          What is happening right now, across the executions you can see.
        </p>
      </div>

      {/* KPI row: each number is a doorway, not a destination. */}
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4" data-testid="home-kpis">
        <KpiCard
          testid="kpi-active-runs"
          label="Active runs"
          value={activeRuns === null ? '—' : String(activeRuns)}
          caption={`across your ${Math.min(ACTIVE_PROBE_LIMIT, scoped?.length ?? 0)} most recent executions`}
          href="/executions"
        />
        <KpiCard
          testid="kpi-last-run"
          label="Last run"
          value={latest ? latest.outcome : '—'}
          caption={latest ? formatRowTime(latest.started_at) : 'no runs yet'}
          href={latest ? `/reports/${latest.run_id}` : '/reports'}
        />
        <KpiCard
          testid="kpi-p95"
          label="p95"
          value={latestP95 !== undefined ? `${(latestP95 * 1000).toFixed(1)} ms` : '—'}
          caption={latest ? `run #${latest.run_id}` : 'no runs yet'}
          href={latest ? `/reports/${latest.run_id}` : '/reports'}
        />
        <KpiCard
          testid="kpi-total-executions"
          label="Total executions"
          value={String(scoped?.length ?? 0)}
          caption="visible to you"
          href="/executions"
        />
      </div>

      {/* Recent executions: the five rows an operator is most likely to be
          looking for, one tap from here to the hub. */}
      <Card padding="none" data-testid="home-recent">
        <div className="flex items-center justify-between border-b border-slate-100 px-4 py-3 dark:border-slate-800">
          <h2 className="text-body-sm font-semibold text-slate-900 dark:text-white">Recent executions</h2>
          <Link
            to="/executions"
            className="rounded text-caption font-medium text-sky-600 hover:underline focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:text-sky-400"
          >
            View all →
          </Link>
        </div>
        {recent.length === 0 ? (
          <p className="text-body-sm p-6 text-slate-500 dark:text-slate-400">No executions visible to you yet.</p>
        ) : (
          <ul className="divide-y divide-slate-200 dark:divide-slate-700">
            {recent.map((e) => (
              <li key={e.id}>
                <Link
                  to={`/executions/${e.id}`}
                  className="flex min-h-[44px] flex-wrap items-center justify-between gap-2 px-4 py-3 text-sm transition-colors hover:bg-slate-50 focus:outline-none focus:ring-2 focus:ring-inset focus:ring-sky-500 dark:hover:bg-slate-800/50"
                >
                  <span className="flex items-center gap-2">
                    <span className="font-medium text-slate-900 dark:text-slate-100">
                      #{e.id} {executionDisplayName(e.name)}
                    </span>
                    {/* Kind badge (phase 39's field): calibrate_engine
                        executions read differently from normal ones. */}
                    <span
                      className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${
                        e.kind === 'calibrate_engine'
                          ? 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300'
                          : 'bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-300'
                      }`}
                    >
                      {e.kind ?? 'normal'}
                    </span>
                  </span>
                  <span className="text-caption text-slate-500 dark:text-slate-400">
                    {e.engine ?? 'default'} · {formatRowTime(e.created_time)}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </Card>

      {/* Throughput sparkline of the latest run: the shape of the only run
          that matters right now, from the series endpoint the report page
          charts. */}
      <Card data-testid="home-sparkline-card">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-body-sm font-semibold text-slate-900 dark:text-white">
            Latest run throughput
          </h2>
          {latest && (
            <Link
              to={`/reports/${latest.run_id}`}
              className="rounded text-caption font-medium text-sky-600 hover:underline focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:text-sky-400"
            >
              Run #{latest.run_id} report →
            </Link>
          )}
        </div>
        <div className="mt-3" data-testid="home-sparkline">
          {latest === null ? (
            <p className="text-body-sm text-slate-500 dark:text-slate-400">No runs yet.</p>
          ) : series === null ? (
            <div className="h-8 animate-pulse rounded bg-slate-100 dark:bg-slate-700/50" />
          ) : sparkValues.length === 0 ? (
            <p className="text-body-sm text-slate-500 dark:text-slate-400">
              No per-second data recorded for run #{latest.run_id}.
            </p>
          ) : (
            <div className="flex items-center gap-3">
              <Sparkline
                values={sparkValues}
                ariaLabel={`Achieved requests per second across run #${latest.run_id}, oldest to newest`}
                width={240}
                height={40}
                className="text-sky-600 dark:text-sky-400"
              />
              <OutcomeBadge outcome={latest.outcome} />
              <span className="text-caption text-slate-500 dark:text-slate-400">
                {sparkValues.length} seconds measured
              </span>
            </div>
          )}
        </div>
      </Card>
    </div>
  );
}
