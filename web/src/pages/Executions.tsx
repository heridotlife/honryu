import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import Card, { CardContent } from '../components/ui/Card';
import Input from '../components/ui/Input';
import { ApiError } from '../api/client';
import { listExecutions, type ExecutionSummary } from '../api/executions';
import { useProjectSelection } from '../components/ProjectSwitcher';
import DigestCard from '../components/DigestCard';
import WebhooksCard from '../components/WebhooksCard';
import { useSession } from '../hooks/useSession';

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

/**
 * /executions -- the caller-scoped execution list (phase 19 R1, over G1's
 * GET /api/executions). Each row links to /executions/:id (the R2 hub).
 * Newest first is the server's contract, not this page's job.
 */
export default function Executions() {
  const { can } = useSession();
  // Phase 32: the global project switcher's selection (localStorage via
  // the shared hook) scopes this list like every other execution view.
  const { selectedId, selectedName, select } = useProjectSelection();
  const [executions, setExecutions] = useState<ExecutionSummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [engineFilter, setEngineFilter] = useState('all');
  const [search, setSearch] = useState('');

  useEffect(() => {
    let alive = true;
    listExecutions()
      .then((rows) => {
        if (alive) setExecutions(rows);
      })
      .catch((err: unknown) => {
        if (alive) setError(err instanceof ApiError ? err.message : 'failed to load executions');
      });
    return () => {
      alive = false;
    };
  }, []);

  // The filter row: engine chips + name/id search (phase 28) under the
  // global project scope (phase 32), purely client-side -- the executions
  // are already in memory.
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

  if (error) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-semibold text-slate-900 dark:text-slate-100">Executions</h1>
        <p className="text-sm text-red-600 dark:text-red-400">{error}</p>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold text-slate-900 dark:text-slate-100">Executions</h1>
        {can('execution', 'create') && (
          <Link
            to="/executions/new"
            className="rounded text-sm font-medium text-sky-600 hover:underline focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:text-sky-400"
          >
            + New test
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
      {/* Phase 40: the project's run-completion webhook registry, directly
          under the filter row -- project-scoped like the list above it, so it
          only exists once a project is selected and only for callers who may
          update the project (the same grant the backend's webhook routes
          demand). */}
      {selectedId !== '' && can('project', 'update') && (
        <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
          <WebhooksCard projectId={Number(selectedId)} />
          {/* Phase 42: the same gate -- administering what a project
              broadcasts (its digest schedule) is a project update, and the
              card delivers through the webhook registry beside it. */}
          <DigestCard projectId={Number(selectedId)} />
        </div>
      )}
      {executions === null ? (
        <p className="text-sm text-slate-500">Loading…</p>
      ) : executions.length === 0 ? (
        <Card>
          <CardContent>
            <p className="text-sm text-slate-500">No executions yet.</p>
          </CardContent>
        </Card>
      ) : filtered.length === 0 ? (
        <p className="text-sm text-slate-500">No executions match the current filters.</p>
      ) : (
        <ul className="divide-y divide-slate-200 dark:divide-slate-700">
          {filtered.map((e) => (
            <li key={e.id}>
              <Link
                to={`/executions/${e.id}`}
                className="flex items-center justify-between rounded py-3 text-sm hover:bg-slate-50 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:hover:bg-slate-800/50 px-2"
              >
                <span className="font-medium text-slate-900 dark:text-slate-100">{e.name}</span>
                <span className="text-slate-500">
                  {e.engine ?? 'default engine'}
                  {e.cluster ? ` · ${e.cluster}` : ''}
                  {' · '}
                  {new Date(e.created_time).toLocaleString()}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
