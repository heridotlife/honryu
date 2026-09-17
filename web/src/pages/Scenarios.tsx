// /scenarios -- the scenario-first list (phase 67b): the inversion of the
// phase 65 decision that bounded scenarios to the NewTest picker. Each row
// answers "what can I run and how did its latest runs go" and links into the
// tabbed scenario detail (/scenarios/:id). Scopes through the global project
// selection (phase 32) like every other list page -- here as a server-side
// ?project_id=, which the 67a list endpoint natively supports. Since phase
// 70 the rows also carry last_run straight from the list endpoint -- one
// batched read server-side, never a per-row probe fan-out.
import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { FlaskConical } from 'lucide-react';
import Card from '../components/ui/Card';
import CardTable, { type CardTableColumn } from '../components/CardTable';
import EmptyState from '../components/EmptyState';
import RunStatusBadge from '../components/RunStatusBadge';
import { formatRowTime } from '../lib/executionRow';
import { getScenarios, type Scenario, type ScenarioLastRun } from '../api/generated';
import { useProjectSelection } from '../components/ProjectSwitcher';
import { ApiError } from '../api/client';

/**
 * The template badge's classes, reused from the Scenario detail page's
 * phase 65 badge: rendered only when a row actually carries is_template.
 * GET /api/scenarios never returns templates today (the service's
 * ListByProject excludes them for every consumer), so this stays dormant
 * by contract -- it exists so the list cannot mis-render if the wire ever
 * includes one, and the test pins that branch.
 */
function TemplateBadge({ name }: { name: string }) {
  return (
    <span
      className="ml-2 inline-block rounded-full bg-sky-100 px-2 py-0.5 text-caption text-sky-800 dark:bg-sky-900/40 dark:text-sky-200"
      data-testid="template-badge"
    >
      template · {name}
    </span>
  );
}

/**
 * Skeleton matching the table's geometry while the first fetch is in
 * flight (ProjectDashboard's DashboardSkeleton pattern; animate-pulse is
 * clamped by globals.css under prefers-reduced-motion).
 */
function ScenariosSkeleton() {
  return (
    <div data-testid="scenarios-loading" className="space-y-2">
      {[0, 1, 2].map((i) => (
        <div key={i} className="h-10 animate-pulse rounded-lg bg-slate-100 dark:bg-slate-700/50" />
      ))}
    </div>
  );
}

/**
 * The last-run cell (phase 70): the verdict the list endpoint batched for
 * the whole page -- RunStatusBadge (icon + word, never color-only) beside
 * the run's start time in the one-short-timestamp house style. A null
 * last_run renders the honest dash: the scenario has no runs, or its newest
 * run has not finalised a report yet -- "no verdict yet", never "failed".
 *
 * Phase 79: the CONTENT only -- CardTable owns the td (sm+) and the card
 * pair's value slot (below sm), both carrying the last-run-cell testid, so
 * this render serves both branches unchanged.
 */
function LastRunContent({ lastRun }: { lastRun: ScenarioLastRun | null }) {
  if (lastRun === null) {
    return <span className="text-slate-400 dark:text-slate-500" title="No run with a report yet">—</span>;
  }
  return (
    <div className="flex flex-wrap items-center gap-2">
      <RunStatusBadge outcome={lastRun.outcome} />
      <span className="text-slate-500 dark:text-slate-400">{formatRowTime(lastRun.started_at)}</span>
    </div>
  );
}

/** The phase 79 column defs -- one source for the sm+ table and the
 * below-sm card list (primary column = card title, the rest label:value
 * pairs). Testids ride exactly where the pre-CardTable markup had them. */
function scenarioColumns(projects: { id: number; name: string }[]): CardTableColumn<Scenario & { last_run: ScenarioLastRun | null }>[] {
  const projectName = (projectId: number | undefined): string => {
    if (projectId === undefined) return '—';
    const name = projects.find((p) => p.id === projectId)?.name;
    return name ?? `project ${projectId}`;
  };
  return [
    {
      key: 'name',
      header: 'Name',
      primary: true,
      thClassName: 'px-4 py-3',
      tdClassName: 'px-4 py-3',
      render: (s) => (
        <>
          <Link
            to={`/scenarios/${s.id}`}
            data-testid={`scenario-link-${s.id}`}
            className="font-medium text-sky-600 hover:underline focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:text-sky-400"
          >
            {s.name}
          </Link>
          {s.is_template && <TemplateBadge name={s.template_name ?? ''} />}
        </>
      ),
    },
    {
      key: 'project',
      header: 'Project',
      thClassName: 'px-4 py-3',
      tdClassName: 'px-4 py-3 text-slate-500 dark:text-slate-400',
      render: (s) => projectName(s.project_id),
    },
    {
      key: 'kind',
      header: 'Kind',
      thClassName: 'px-4 py-3',
      tdClassName: 'px-4 py-3 text-slate-500 dark:text-slate-400',
      render: (s) => s.kind ?? 'portable',
    },
    {
      key: 'last_run',
      header: 'Last run',
      cellTestId: () => 'last-run-cell',
      thClassName: 'px-4 py-3',
      tdClassName: 'px-4 py-3',
      render: (s) => <LastRunContent lastRun={s.last_run ?? null} />,
    },
  ];
}

/** /scenarios -- every runnable scenario the caller may see, id order (the
 * server's contract). The project dropdown drives a ?project_id= refetch,
 * not a client-side filter: the endpoint filters, so the page ships one
 * request's worth of rows, and the selection stays in sync with the nav
 * switcher through the shared hook. */
export default function Scenarios() {
  // The global project scope (localStorage via the shared hook): the
  // dropdown below and the nav's ProjectSwitcher are two views of one
  // selection, so the nav switcher narrows this list and vice versa.
  const { projects, selectedId, select } = useProjectSelection();
  const [scenarios, setScenarios] = useState<(Scenario & { last_run: ScenarioLastRun | null })[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    setScenarios(null);
    setError(null);
    getScenarios(selectedId === '' ? undefined : { query: { project_id: selectedId } })
      .then((rows) => {
        if (alive) setScenarios(rows);
      })
      .catch((err: unknown) => {
        if (alive) setError(err instanceof ApiError ? err.message : 'Failed to load scenarios.');
      });
    return () => {
      alive = false;
    };
  }, [selectedId]);

  // Project name resolution moved into scenarioColumns -- the columns are
  // built per render from the same projects list the shared hook already
  // fetched (no per-row lookup fetch): a scenario whose project fell out of
  // the caller's view falls back to the raw id, never blank.

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-display-sm text-slate-900 dark:text-white">Scenarios</h1>
        {/* The existing projects fetcher (the shared hook's list) feeds the
            dropdown; "" is the shared hook's "all projects" value. */}
        <label className="flex items-center gap-2 text-body-sm text-slate-500 dark:text-slate-400">
          Project
          <select
            data-testid="project-filter"
            value={selectedId}
            onChange={(e) => select(e.target.value)}
            className="rounded-lg border border-slate-300 bg-white px-3 py-2 text-body-sm text-slate-900 focus:outline-none focus:ring-2 focus:ring-sky-500 dark:border-slate-600 dark:bg-slate-800 dark:text-slate-100"
          >
            <option value="">All projects</option>
            {projects.map((p) => (
              <option key={p.id} value={String(p.id)}>
                {p.name}
              </option>
            ))}
          </select>
        </label>
      </div>

      {error !== null ? (
        <p className="text-sm text-red-600 dark:text-red-400" role="alert">
          {error}
        </p>
      ) : scenarios === null ? (
        <ScenariosSkeleton />
      ) : scenarios.length === 0 ? (
        <Card>
          {/* Phase 76: the shared empty state. The one primary action routes
              to the template picker (NewTest) -- the create flow this SPA
              actually has. The action's href deliberately avoids
              ^/scenarios/ so the e2e harness's row selector can never
              mistake it for a scenario row (pinned in Scenarios.test). */}
          <EmptyState
            testId="scenarios-empty"
            icon={<FlaskConical className="size-6" />}
            title="No scenarios yet"
            description={
              selectedId === ''
                ? 'Create one from a template — the catalog ships HTTPbin baselines to start from.'
                : 'No scenarios in this project yet. Create one from a template.'
            }
            action={{ label: 'Create from template', to: '/executions/new' }}
          />
        </Card>
      ) : (
        <Card padding="none">
          {/* Phase 79: shared CardTable -- the sm+ markup is the pre-phase
              table unchanged (same testids on the same nodes); below sm the
              same columns become the card list, so a phone never has to
              scroll sideways to read a row. */}
          <CardTable
            tableTestId="scenarios-table"
            columns={scenarioColumns(projects)}
            rows={scenarios}
            rowKey={(s) => (s.id !== undefined ? `scenario-${s.id}` : `scenario-${s.name}`)}
            headerRowClassName="border-b border-slate-100 text-left text-caption font-medium text-slate-500 dark:border-slate-800 dark:text-slate-400"
            tableClassName="w-full text-body-sm"
            rowClassName={() => 'border-b border-slate-50 last:border-b-0 hover:bg-slate-50 dark:border-slate-800/50 dark:hover:bg-slate-800/50'}
          />
        </Card>
      )}
    </div>
  );
}
