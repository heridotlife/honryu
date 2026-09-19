// /scenarios/:id -- the tabbed scenario detail. Phase 95 merged the old
// Runs tab into "Run" (the first, default tab): the run-settings surface
// -- the scenario's latest execution with its inline mode/qps/duration
// editor and the one-click Start flow (phase 94) -- with the run history
// below it, from GET /api/scenarios/{id}/executions (67a, newest first):
// per row the newest report's outcome/start/duration (the Home page's
// bounded per-execution probe pattern -- the execution list payload
// carries identity only, so status is one ?limit=1 reports call per row) --
// and then the Calibrate control posting to the 67a scenario-scoped
// trigger. Stale ?tab=runs links normalize to the no-param default. The
// "Editor" tab is phase 65's TaurusEditor page, moved over unchanged;
// ?tab=editor deep-links to it (the template-instantiation flow lands
// there).
import { useCallback, useEffect, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import { Play } from 'lucide-react';
import Breadcrumbs from '../components/Breadcrumbs';
import Button from '../components/ui/Button';
import Card, { CardContent, CardHeader, CardTitle } from '../components/ui/Card';
import CardTable, { type CardTableColumn } from '../components/CardTable';
import EmptyState from '../components/EmptyState';
import ScenarioRunPanel, { latestLoadExecution } from '../components/ScenarioRunPanel';
import ScenarioVersionHistory from '../components/ScenarioVersionHistory';
import { TabPanel, Tabs } from '../components/ui/Tabs';
import TaurusEditor from '../components/TaurusEditor';
import ThresholdEditor from '../components/ThresholdEditor';
import RunStatusBadge from '../components/RunStatusBadge';
import { ApiError } from '../api/client';
import { listExecutionReports, type Outcome, type Report } from '../api/reports';
import {
  getScenariosByScenarioId,
  getScenariosByScenarioIdExecutions,
  postScenariosByScenarioIdCalibrationTrigger,
  type CalibrationJob,
  type ExecutionSummary,
  type Scenario,
} from '../api/generated';
import { formatRowTime } from '../lib/executionRow';
import { useSession } from '../hooks/useSession';

/**
 * A run row's status/start/duration, derived from the execution's newest
 * report. Absent from the map = fetch in flight; null = the probe failed or
 * the execution has no run yet -- the row says "unknown" honestly.
 */
interface RunInfo {
  outcome: Outcome;
  startedAt: string;
  durationSeconds: number | null;
}

/** The one report a row needs: the execution's newest run (?limit=1). */
function runInfoOf(report: Report): RunInfo {
  const started = Date.parse(report.started_at);
  const ended = Date.parse(report.ended_at);
  const durationSeconds = Number.isNaN(started) || Number.isNaN(ended) ? null : Math.max(0, Math.round((ended - started) / 1000));
  return { outcome: report.outcome, startedAt: report.started_at, durationSeconds };
}

/** "2m 03s" / "37s" / null (junk or missing ended_at) → the cell's dash. */
export function formatDuration(seconds: number | null): string {
  if (seconds === null) {
    return '—';
  }
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  if (minutes === 0) {
    return `${rest}s`;
  }
  return `${minutes}m ${String(rest).padStart(2, '0')}s`;
}

/**
 * The phase 65 landing page for template instantiation, now the detail half
 * of the scenario-first inversion: the name, the template badge when the
 * row was instantiated from one, and the two tabs. The old "deliberately no
 * scenario list page" decision was reversed in phase 67b -- the list lives
 * at /scenarios and is the primary nav entry.
 */
export default function Scenario() {
  const { id } = useParams();
  const scenarioId = Number(id);
  const { can } = useSession();
  const [searchParams, setSearchParams] = useSearchParams();
  // Phase 95: ONE Run tab -- the default. ?tab=editor is the only deep
  // link left (the instantiation flow's landing is the editor); the old
  // ?tab=runs and any other stale id fall back to the default.
  const tabParam = searchParams.get('tab');
  const tab = tabParam === 'editor' ? 'editor' : 'run';

  // A stale ?tab=runs (or any unknown id) must not survive in the URL:
  // replace it with the no-param default so Run is the canonical address
  // and old bookmarks land on it, not on a dead tab id.
  useEffect(() => {
    if (tabParam !== null && tabParam !== 'editor') {
      setSearchParams({}, { replace: true });
    }
  }, [tabParam, setSearchParams]);

  const [scenario, setScenario] = useState<Scenario | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // Bumped when a version restore lands (phase 80) so the scenario refetch
  // effect re-runs without changing the id, and when the Run panel asks
  // for a list refresh (phase 94: it created a new execution).
  const [reloadKey, setReloadKey] = useState(0);

  const [executions, setExecutions] = useState<ExecutionSummary[] | null>(null);
  const [executionsError, setExecutionsError] = useState<string | null>(null);
  const [runsInfo, setRunsInfo] = useState<Record<number, RunInfo | null>>({});

  const [calibrating, setCalibrating] = useState(false);
  const [job, setJob] = useState<CalibrationJob | null>(null);
  const [calibrationError, setCalibrationError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setError(null);
    getScenariosByScenarioId(scenarioId)
      .then((s) => {
        if (alive) setScenario(s);
      })
      .catch((e: unknown) => {
        if (alive) setError(e instanceof Error ? e.message : 'Failed to load scenario.');
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [scenarioId, reloadKey]);

  // The run history (newest first is the 67a endpoint's contract, not this
  // page's job), then the bounded per-row probe: each execution's newest
  // report lands in the map as its own response arrives, so rows fill in
  // progressively instead of waiting on the slowest. Phase 94: lifted into
  // a callback so the Run panel can ask for a refetch after it creates an
  // execution (reloadKey is its trigger).
  const loadExecutions = useCallback(() => {
    let alive = true;
    setExecutions(null);
    setExecutionsError(null);
    setRunsInfo({});
    getScenariosByScenarioIdExecutions(scenarioId)
      .then((rows) => {
        if (alive) setExecutions(rows);
        for (const e of rows) {
          if (e.id === undefined) {
            continue;
          }
          listExecutionReports(e.id, 1)
            .then((reports) => {
              if (alive) setRunsInfo((prev) => ({ ...prev, [e.id as number]: reports[0] ? runInfoOf(reports[0]) : null }));
            })
            .catch(() => {
              if (alive) setRunsInfo((prev) => ({ ...prev, [e.id as number]: null }));
            });
        }
      })
      .catch((err: unknown) => {
        if (alive) setExecutionsError(err instanceof ApiError ? err.message : 'Failed to load runs.');
      });
    return () => {
      alive = false;
    };
  }, [scenarioId]);

  useEffect(() => loadExecutions(), [loadExecutions, reloadKey]);

  const triggerCalibration = async () => {
    setCalibrating(true);
    setCalibrationError(null);
    try {
      // The 67a scenario-scoped trigger: the service picks the scenario's
      // newest calibrate_engine execution, the job records both ids, and
      // the response is exactly the execution-scoped route's.
      const created = await postScenariosByScenarioIdCalibrationTrigger(scenarioId);
      setJob(created);
    } catch (err: unknown) {
      setCalibrationError(err instanceof ApiError ? err.message : 'Failed to trigger calibration.');
    } finally {
      setCalibrating(false);
    }
  };

  if (loading) {
    return <p className="text-body-sm text-slate-500 dark:text-slate-400">Loading scenario…</p>;
  }
  if (error || !scenario) {
    return (
      <p className="text-sm text-red-600 dark:text-red-400" role="alert">
        {error ?? 'Scenario not found.'}
      </p>
    );
  }

  // Triggering calibration creates a run (calibration:create on the wire is
  // scenario:create for the scenario-scoped route -- the audit table's row).
  const mayCalibrate = can('scenario', 'create');

  // The Run panel's execution: the newest non-calibration row, the same
  // pick the panel itself makes (exported helper, one definition).
  const latestExecution = latestLoadExecution(executions);

  // Phase 79: the run history's column defs -- one source for the sm+ table
  // and the below-sm card list. The per-row status probe (runsInfo) closes
  // over the renders: unknown = fetch in flight (…), null = the probe
  // failed or the run never finalised (—), else the badge.
  const runColumns: CardTableColumn<ExecutionSummary>[] = [
    {
      key: 'run',
      header: 'Run',
      primary: true,
      thClassName: 'px-4 py-3',
      tdClassName: 'px-4 py-3',
      render: (e) => (
        <Link
          to={`/executions/${e.id}`}
          data-testid={`run-link-${e.id}`}
          className="font-medium text-sky-600 hover:underline focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:text-sky-400"
        >
          #{e.id} {e.name}
        </Link>
      ),
    },
    {
      key: 'status',
      header: 'Status',
      cellTestId: (e) => (e.id !== undefined ? `run-status-${e.id}` : undefined),
      thClassName: 'px-4 py-3',
      tdClassName: 'px-4 py-3',
      render: (e) => {
        const known = e.id !== undefined && e.id in runsInfo;
        if (!known) return <span className="text-slate-400 dark:text-slate-500">…</span>;
        const info = runsInfo[e.id as number];
        // In flight / unknown are honest placeholders; the badge itself is
        // icon + text (never color-only).
        if (info == null) return <span className="text-slate-400 dark:text-slate-500">—</span>;
        return <RunStatusBadge outcome={info.outcome} />;
      },
    },
    {
      key: 'started',
      header: 'Started',
      thClassName: 'px-4 py-3',
      tdClassName: 'px-4 py-3 text-slate-500 dark:text-slate-400',
      render: (e) => {
        const info = e.id === undefined ? undefined : runsInfo[e.id];
        return info != null ? formatRowTime(info.startedAt) : '—';
      },
    },
    {
      key: 'duration',
      header: 'Duration',
      thClassName: 'px-4 py-3',
      tdClassName: 'px-4 py-3 text-slate-500 dark:text-slate-400',
      render: (e) => {
        const info = e.id === undefined ? undefined : runsInfo[e.id];
        return info != null ? formatDuration(info.durationSeconds) : '—';
      },
    },
  ];

  return (
    <div className="space-y-4">
      {/* Phase 67b: the third level of depth -- Scenarios > {name} (and the
          run hub below keeps its trail, rooted at the list now). */}
      <Breadcrumbs items={[{ label: 'Scenarios', href: '/scenarios' }, { label: scenario.name ?? `#${scenarioId}` }]} />
      <div>
        <h1 className="text-display-sm text-slate-900 dark:text-white" data-testid="scenario-name">
          {scenario.name}
        </h1>
        {scenario.is_template && (
          <span
            className="mt-1 inline-block rounded-full bg-sky-100 px-2 py-0.5 text-caption text-sky-800 dark:bg-sky-900/40 dark:text-sky-200"
            data-testid="template-badge"
          >
            template · {scenario.template_name}
          </span>
        )}
      </div>

      <Tabs
        tabs={[
          { id: 'run', label: 'Run' },
          { id: 'editor', label: 'Editor' },
        ]}
        active={tab}
        onChange={(next) => setSearchParams(next === 'run' ? {} : { tab: next }, { replace: true })}
        className="mt-2"
      />

      {/* Phase 95: ONE Run tab -- the start surface (phase 94's panel:
          the latest execution's statement with the inline editor and the
          one-click Start flow) with the run history below it, then the
          Calibrate action -- the old Runs tab's content, moved in. */}
      <TabPanel id="run" active={tab} className="space-y-4">
        <ScenarioRunPanel
          scenarioId={scenarioId}
          scenarioName={scenario.name ?? `scenario ${scenarioId}`}
          projectId={scenario.project_id ?? 0}
          executions={executions}
          executionsError={executionsError}
          lastRun={
            latestExecution?.id !== undefined && runsInfo[latestExecution.id] !== undefined
              ? runsInfo[latestExecution.id]
              : undefined
          }
          onExecutionsChanged={() => setReloadKey((k) => k + 1)}
        />

        {/* Run history: the 67a newest-first list (phase 79's CardTable --
            the sm+ table keeps the exact pre-phase markup and testids;
            below sm the same columns render as run cards). */}
        <div className="space-y-3">
          <h2 className="text-body-sm font-semibold text-slate-900 dark:text-white">Run history</h2>
          {executionsError !== null ? (
            <p className="text-sm text-red-600 dark:text-red-400" role="alert">
              {executionsError}
            </p>
          ) : executions === null ? (
            <div className="space-y-2" data-testid="runs-loading">
              {[0, 1].map((i) => (
                <div key={i} className="h-10 animate-pulse rounded-lg bg-slate-100 dark:bg-slate-700/50" />
              ))}
            </div>
          ) : executions.length === 0 ? (
            <Card>
              {/* Phase 76: the shared empty state. The one primary action
                  triggers the existing per-scenario run flow (the 67a
                  calibration trigger -- the same CTA as the Calibrate
                  control below): it creates a real run bound to this
                  scenario. It is a Button, deliberately not a Link into
                  /executions/, so the e2e harness's run-row selector can
                  never match it (pinned in Scenario.test). Hidden without
                  the scenario:create grant, and once a job is queued (the
                  pending banner takes over as the action surface). */}
              <EmptyState
                testId="runs-empty"
                icon={<Play className="size-6" />}
                title="No runs yet"
                description="This scenario has not been bound to an execution. Start one and its history appears here."
                action={
                  mayCalibrate && job === null
                    ? { label: calibrating ? 'Starting…' : 'Run this scenario', onClick: () => void triggerCalibration() }
                    : undefined
                }
              />
            </Card>
          ) : (
            <Card padding="none">
              <CardTable
                tableTestId="runs-table"
                columns={runColumns}
                rows={executions}
                rowKey={(e) => (e.id !== undefined ? `run-${e.id}` : `run-${e.name}`)}
                headerRowClassName="border-b border-slate-100 text-left text-caption font-medium text-slate-500 dark:border-slate-800 dark:text-slate-400"
                tableClassName="w-full text-body-sm"
                rowClassName={() => 'border-b border-slate-50 last:border-b-0 hover:bg-slate-50 dark:border-slate-800/50 dark:hover:bg-slate-800/50'}
              />
            </Card>
          )}
        </div>

        {/* Calibrate: the 67a scenario-scoped trigger. On 201 it returns
            the job with both ids; the execution hub is the calibration
            job view the existing flow uses (CapacityPanel mounts there
            for calibrate_engine executions, and CalibrateScenarioModal
            navigates to the same place). */}
        {(mayCalibrate || job !== null || calibrationError !== null) && (
          <div className="space-y-3">
            {mayCalibrate && (
              <Button
                variant="secondary"
                size="sm"
                data-testid="calibrate-button"
                onClick={triggerCalibration}
                disabled={calibrating}
              >
                {calibrating ? 'Calibrating…' : 'Calibrate'}
              </Button>
            )}
            {job !== null && (
              <p
                className="rounded-lg border border-sky-200 bg-sky-50 px-3 py-2 text-body-sm text-sky-800 dark:border-sky-800 dark:bg-sky-950/30 dark:text-sky-200"
                data-testid="calibration-pending"
              >
                Calibration job #{job.id} queued — phase {job.phase ?? 'pending'}.{' '}
                <Link
                  to={`/executions/${job.execution_id}`}
                  data-testid="calibration-job-link"
                  className="font-medium text-sky-600 underline focus:outline-none focus:ring-2 focus:ring-sky-500 dark:text-sky-400"
                >
                  View calibration job →
                </Link>
              </p>
            )}
            {calibrationError !== null && (
              <p className="text-sm text-red-600 dark:text-red-400" role="alert">
                {calibrationError}
              </p>
            )}
          </div>
        )}
      </TabPanel>

      {/* The editor tab: phase 65's requests editor, plus the phase 72
          threshold rows -- the scenario's k6-style pass/fail bounds, saved
          as a whole via PUT replace-all. */}
      <TabPanel id="editor" active={tab} className="space-y-4">
        <Card>
          <CardHeader>
            <CardTitle>Requests</CardTitle>
          </CardHeader>
          <CardContent>
            <TaurusEditor scenarioId={scenarioId} />
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>Thresholds</CardTitle>
          </CardHeader>
          <CardContent>
            <ThresholdEditor scenarioId={scenarioId} />
          </CardContent>
        </Card>
        {/* Phase 80: the edit audit trail -- collapsed by default; restoring
            rewinds the scenario, so the page refetches it on callback. */}
        <Card>
          <CardHeader>
            <CardTitle>History</CardTitle>
          </CardHeader>
          <CardContent>
            <ScenarioVersionHistory scenarioId={scenarioId} onRestored={() => setReloadKey((k) => k + 1)} />
          </CardContent>
        </Card>
      </TabPanel>
    </div>
  );
}
