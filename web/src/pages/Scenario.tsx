// /scenarios/:id -- the tabbed scenario detail (phase 67b). The "Runs" tab
// (default) is the scenario's run history from GET
// /api/scenarios/{id}/executions (67a, newest first): per row the newest
// report's outcome/start/duration (the Home page's bounded per-execution
// probe pattern -- the execution list payload carries identity only, so
// status is one ?limit=1 reports call per row), and the Calibrate control
// posting to the 67a scenario-scoped trigger. The "Editor" tab is phase
// 65's TaurusEditor page, moved over unchanged; ?tab=editor deep-links to
// it (the template-instantiation flow lands there).
import { useEffect, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import Breadcrumbs from '../components/Breadcrumbs';
import Button from '../components/ui/Button';
import Card, { CardContent, CardHeader, CardTitle } from '../components/ui/Card';
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
  // Runs is the default tab; ?tab=editor is the deep link (and the
  // instantiation flow's landing).
  const tab = searchParams.get('tab') === 'editor' ? 'editor' : 'runs';

  const [scenario, setScenario] = useState<Scenario | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

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
  }, [scenarioId]);

  // The run history (newest first is the 67a endpoint's contract, not this
  // page's job), then the bounded per-row probe: each execution's newest
  // report lands in the map as its own response arrives, so rows fill in
  // progressively instead of waiting on the slowest.
  useEffect(() => {
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
          { id: 'runs', label: 'Runs' },
          { id: 'editor', label: 'Editor' },
        ]}
        active={tab}
        onChange={(next) => setSearchParams(next === 'runs' ? {} : { tab: next }, { replace: true })}
        className="mt-2"
      />

      <TabPanel id="runs" active={tab}>
        <div className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="text-body-sm font-semibold text-slate-900 dark:text-white">Run history</h2>
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
          </div>

          {/* On 201 the 67a trigger returns the job with both ids; the
              execution hub is the calibration job view the existing flow
              uses (CapacityPanel mounts there for calibrate_engine
              executions, and CalibrateScenarioModal navigates to the same
              place). */}
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
              <CardContent>
                <p className="text-sm text-slate-500 dark:text-slate-400" data-testid="runs-empty">
                  No runs yet — this scenario has not been bound to an execution.
                </p>
              </CardContent>
            </Card>
          ) : (
            <Card padding="none">
              <table className="w-full text-body-sm" data-testid="runs-table">
                <thead>
                  <tr className="border-b border-slate-100 text-left text-caption font-medium text-slate-500 dark:border-slate-800 dark:text-slate-400">
                    <th scope="col" className="px-4 py-3">
                      Run
                    </th>
                    <th scope="col" className="px-4 py-3">
                      Status
                    </th>
                    <th scope="col" className="px-4 py-3">
                      Started
                    </th>
                    <th scope="col" className="px-4 py-3">
                      Duration
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {executions.map((e) => {
                    const info = e.id === undefined ? null : runsInfo[e.id];
                    const known = e.id !== undefined && e.id in runsInfo;
                    return (
                      <tr
                        key={e.id !== undefined ? `run-${e.id}` : `run-${e.name}`}
                        className="border-b border-slate-50 last:border-b-0 hover:bg-slate-50 dark:border-slate-800/50 dark:hover:bg-slate-800/50"
                      >
                        <td className="px-4 py-3">
                          <Link
                            to={`/executions/${e.id}`}
                            data-testid={`run-link-${e.id}`}
                            className="font-medium text-sky-600 hover:underline focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:text-sky-400"
                          >
                            #{e.id} {e.name}
                          </Link>
                        </td>
                        <td className="px-4 py-3" data-testid={`run-status-${e.id}`}>
                          {/* In flight / unknown are honest placeholders;
                              the badge itself is icon + text (never
                              color-only). */}
                          {!known ? <span className="text-slate-400 dark:text-slate-500">…</span> : info == null ? <span className="text-slate-400 dark:text-slate-500">—</span> : <RunStatusBadge outcome={info.outcome} />}
                        </td>
                        <td className="px-4 py-3 text-slate-500 dark:text-slate-400">
                          {info != null ? formatRowTime(info.startedAt) : '—'}
                        </td>
                        <td className="px-4 py-3 text-slate-500 dark:text-slate-400">
                          {info != null ? formatDuration(info.durationSeconds) : '—'}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </Card>
          )}
        </div>
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
      </TabPanel>
    </div>
  );
}
