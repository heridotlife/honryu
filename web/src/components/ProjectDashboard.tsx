// The project dashboard (phase 66): one ProjectSummary fetch powers every
// number here -- KPI cards (executions, scenarios, templates, regressions),
// a throughput sparkline over the recent runs, and the last-run status
// chip. Data comes from GET /api/projects/{id}/summary through the
// generated client, so this component holds no fetch logic of its own and
// cannot drift from the spec. Mounted on the Executions page once a project
// is selected (the project-scoped view), above the webhook/digest admin
// cards.
import { useEffect, useState } from 'react';
import { Rocket } from 'lucide-react';
import Card from './ui/Card';
import OutcomeBadge from './ui/OutcomeBadge';
import EmptyState from './EmptyState';
import SloPanel from './SloPanel';
import Sparkline from './Sparkline';
import { getProjectsByProjectIdSummary, type ProjectSummary } from '../api/generated';
import { formatRowTime } from '../lib/executionRow';
import type { Outcome } from '../api/reports';

export interface ProjectDashboardProps {
  /** The project whose dashboard this renders. */
  projectId: number;
}

/** One KPI card. The regressed card takes danger tokens when its count is
 * positive -- the one number on this dashboard that can be bad news. */
function DashboardKpi({
  label,
  value,
  caption,
  testid,
  danger = false,
}: {
  label: string;
  value: string;
  caption?: string;
  testid: string;
  danger?: boolean;
}) {
  return (
    <div
      data-testid={testid}
      data-danger={danger ? 'true' : 'false'}
      className={`flex min-h-[44px] flex-col rounded-2xl border p-4 shadow-sm ${
        danger
          ? 'border-red-300 bg-red-50 dark:border-red-800 dark:bg-red-950/30'
          : 'border-slate-200 bg-white dark:border-slate-700 dark:bg-slate-800'
      }`}
    >
      <span className={`text-caption font-medium ${danger ? 'text-red-700 dark:text-red-300' : 'text-slate-500 dark:text-slate-400'}`}>
        {label}
      </span>
      <span className={`text-heading-md mt-1 ${danger ? 'text-red-800 dark:text-red-200' : 'text-slate-900 dark:text-white'}`}>
        {value}
      </span>
      {caption !== undefined && (
        <span className={`text-caption mt-1 ${danger ? 'text-red-600 dark:text-red-300' : 'text-slate-500 dark:text-slate-400'}`}>
          {caption}
        </span>
      )}
    </div>
  );
}

/** Skeleton matching the loaded layout while the one fetch is in flight. */
function DashboardSkeleton() {
  return (
    <div data-testid="project-dashboard-loading" className="space-y-4">
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="h-24 animate-pulse rounded-2xl bg-slate-100 dark:bg-slate-700/50" />
        ))}
      </div>
      <div className="h-32 animate-pulse rounded-2xl bg-slate-100 dark:bg-slate-700/50" />
    </div>
  );
}

export default function ProjectDashboard({ projectId }: ProjectDashboardProps) {
  const [summary, setSummary] = useState<ProjectSummary | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let alive = true;
    setSummary(null);
    setFailed(false);
    getProjectsByProjectIdSummary(projectId)
      .then((s) => {
        if (alive) setSummary(s);
      })
      .catch(() => {
        // The dashboard degrades to an honest note -- it never takes the
        // execution list below it down.
        if (alive) setFailed(true);
      });
    return () => {
      alive = false;
    };
  }, [projectId]);

  if (failed) {
    return (
      <p data-testid="project-dashboard-error" className="text-body-sm text-slate-500 dark:text-slate-400">
        Dashboard unavailable right now.
      </p>
    );
  }
  if (summary === null) {
    return <DashboardSkeleton />;
  }

  if (summary.total_executions === 0) {
    return (
      <div data-testid="project-dashboard-empty" className="space-y-4">
        {/* Phase 76: the empty branch, aligned with the shared EmptyState
            (icon + title + one action) -- the action routes to the template
            picker, the create flow this SPA has. SLO management stays
            available with no runs: objectives are defined before the first
            run grades them, and the budget read answers the honest no-data
            shape. */}
        <Card>
          <EmptyState
            icon={<Rocket className="size-6" />}
            title="No executions yet"
            description="Create one to start this project's dashboard."
            action={{ label: 'New test', to: '/executions/new' }}
          />
        </Card>
        <SloPanel projectId={projectId} />
      </div>
    );
  }

  const series = summary.throughput_series;
  const sparkValues = series.map((p) => p.achieved_throughput ?? 0);
  const regressedPoints = series.filter((p) => p.regressed).length;

  return (
    <div data-testid="project-dashboard" className="space-y-4">
      {/* KPI row: the counts a project owner opens the page for. */}
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4" data-testid="project-kpis">
        <DashboardKpi
          testid="project-kpi-executions"
          label="Executions"
          value={String(summary.total_executions)}
          caption={summary.last_execution_time ? `last created ${formatRowTime(summary.last_execution_time)}` : undefined}
        />
        <DashboardKpi testid="project-kpi-scenarios" label="Scenarios" value={String(summary.scenario_count)} />
        <DashboardKpi testid="project-kpi-templates" label="Templates" value={String(summary.template_count)} caption="in the catalog" />
        <DashboardKpi
          testid="project-kpi-regressed"
          label="Regressed runs"
          value={String(summary.regressed_count)}
          caption={summary.regressed_count > 0 ? 'missed target QPS vs baseline' : 'none'}
          danger={summary.regressed_count > 0}
        />
      </div>

      <Card data-testid="project-sparkline-card">
        <div className="flex flex-wrap items-center justify-between gap-2 p-4 pb-0">
          <h3 className="text-body-sm font-semibold text-slate-900 dark:text-white">Recent run throughput</h3>          {summary.last_run !== null && (
            <span className="flex items-center gap-2" data-testid="project-last-run">
              <span className="text-caption text-slate-500 dark:text-slate-400">Last run:</span>
              <OutcomeBadge outcome={(summary.last_run.outcome ?? 'aborted') as Outcome} />
            </span>
          )}
        </div>
        <div className="p-4 pt-3">
          {sparkValues.length === 0 ? (
            <p className="text-body-sm text-slate-500 dark:text-slate-400">
              No runs recorded yet &mdash; the throughput line appears after the first run.
            </p>
          ) : (
            <div className="flex items-center gap-3">
              <Sparkline
                values={sparkValues}
                ariaLabel={`Achieved requests per second across the ${sparkValues.length} most recent runs, oldest to newest`}
                width={240}
                height={40}
                className="text-sky-600 dark:text-sky-400"
              />
              <span className="text-caption text-slate-500 dark:text-slate-400">
                {sparkValues.length} most recent runs{regressedPoints > 0 ? ` · ${regressedPoints} regressed` : ''}
              </span>
            </div>
          )}
        </div>
      </Card>

      {/* The SLO section (phase 68): objectives + their budget grades, with
          the window selector. Below the sparkline, above the webhook and
          digest admin cards the page mounts outside this component. */}
      <SloPanel projectId={projectId} />
    </div>
  );
}
