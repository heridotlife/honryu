// Phase 94: the scenario page's Run panel — the run-settings half of a
// scenario. A scenario is the script (steps, thresholds); mode / target
// qps / duration are RUN settings that already live in the execution
// config (execution_scenario + mode migration 0074), so this panel
// surfaces the scenario's most recent execution and its config entry —
// it never duplicates run settings into the scenario document.
//
// The executions list is the phase-67a scenario-scoped route the page
// already fetched (newest first, identity + kind on every row): the
// panel takes the newest row that is NOT a calibrate_engine execution —
// a calibration job is bound to the scenario too, but it is not a run
// config. Config, status, and capacity reads are the panel's own; the
// page hands over the shared list plus its newest-report probe.
import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import Button from './ui/Button';
import Card, { CardContent, CardHeader, CardTitle } from './ui/Card';
import ActionErrorDetails from './ActionErrorDetails';
import ModeForm from './ModeForm';
import RunStatusBadge from './RunStatusBadge';
import { getExecutionConfig, putExecutionConfig, type ConfigTest, type ExecutionConfig } from '../api/executionsConfig';
import { getExecutionStatus, type ExecutionStatus, type Phase } from '../api/status';
import { fanOutCapacity, getCapacityProfile } from '../api/calibration';
import { useSession } from '../hooks/useSession';
import { formatRowTime } from '../lib/executionRow';
import {
  buildModeTest,
  modeChipLabel,
  modeFormValid,
  secondsToModeForm,
  type LoadMode,
  type ModeFormValue,
} from '../lib/modeConfig';
import { ApiError, errorDetails } from '../api/client';
import type { ExecutionSummary } from '../api/generated';
import type { Outcome } from '../api/reports';

/** The page's newest-report probe, handed over for the latest execution. */
export interface LastRunInfo {
  outcome: Outcome;
  startedAt: string;
}

export interface ScenarioRunPanelProps {
  scenarioId: number;
  scenarioName: string;
  projectId: number;
  /** The 67a list the page already fetched: newest first, every kind. */
  executions: ExecutionSummary[] | null;
  executionsError: string | null;
  /** The latest execution's newest report, when the page's probe landed. */
  lastRun?: LastRunInfo | null;
  /** The panel created (or re-configured) an execution — refetch the list. */
  onExecutionsChanged: () => void;
  /** Jump to the tab that hosts the Calibrate action (the Runs tab). */
  onOpenCalibration: () => void;
}

/** Phase palette mirroring the execution hub's badge (idle grey, deployed
 *  sky, running emerald — the word always rides along, never colour only). */
const PHASE_CLASSES: Record<Phase, string> = {
  idle: 'bg-slate-100 text-slate-700 dark:bg-slate-700/50 dark:text-slate-200',
  deployed: 'bg-sky-100 text-sky-800 dark:bg-sky-900/40 dark:text-sky-200',
  running: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-200',
};

/** The scenario's run-settings statement: its newest non-calibration
 *  execution from the 67a newest-first list. */
export function latestLoadExecution(executions: ExecutionSummary[] | null): ExecutionSummary | undefined {
  return (executions ?? []).find(e => e.kind !== 'calibrate_engine');
}

/** The engine a NEW execution for this scenario would use: the newest
 *  execution's (a calibration row names the engine it calibrated), else
 *  jmeter — the NewTest flow's default. */
export function defaultEngine(executions: ExecutionSummary[] | null): string {
  return executions?.[0]?.engine ?? 'jmeter';
}

/** The house pod size every calibration surface assumes (Execution.tsx's
 *  capacityKey and CalibrateScenarioModal's prefill). */
const HOUSE_POD = { cpu: '500m', memory: '512Mi' } as const;

export default function ScenarioRunPanel({
  scenarioId,
  scenarioName,
  executions,
  executionsError,
  lastRun,
  onOpenCalibration,
}: ScenarioRunPanelProps) {
  const { can } = useSession();
  const latest = latestLoadExecution(executions);
  const latestId = latest?.id;
  // The engine every capacity read and a new execution would use: the
  // newest execution's (a calibration row names the engine it calibrated).
  const engine = defaultEngine(executions);

  // The latest execution's config: the whole profile round-trips on edit
  // (the PUT replaces it), so the panel keeps it whole and reads its own
  // scenario's entry out of it.
  const [config, setConfig] = useState<ExecutionConfig | null>(null);
  const [configError, setConfigError] = useState<string | null>(null);
  const [configErrorDetail, setConfigErrorDetail] = useState<Record<string, unknown> | null>(null);
  const [status, setStatus] = useState<ExecutionStatus | null>(null);

  // The inline editor: the entry's mode statement, prefilled from the
  // stored config and re-prefilled after every apply (the server's
  // resolved numbers are the truth; the form only restates the ask).
  const [form, setForm] = useState<ModeFormValue | null>(null);
  const [editBusy, setEditBusy] = useState(false);
  const [editError, setEditError] = useState<string | null>(null);
  const [editErrorDetail, setEditErrorDetail] = useState<Record<string, unknown> | null>(null);
  // 409 from a config write: the no-profile refusal whose remediation is
  // the Calibrate action on this same page's Runs tab.
  const [needsCalibration, setNeedsCalibration] = useState(false);
  const [appliedAt, setAppliedAt] = useState<string | null>(null);

  // The capacity hint's two halves: the profile's per-pod rate and the
  // fan-out engine count at the form's current rate. Best-effort reads —
  // absent (never calibrated for this engine, or fetch failed) degrades
  // to no hint line, nothing else.
  const [perPodQps, setPerPodQps] = useState<number | null>(null);
  const [fanoutEngines, setFanoutEngines] = useState<number | null>(null);

  /** Prefill the form from a config's entry for this scenario. */
  const prefill = (cfg: ExecutionConfig) => {
    const e = cfg.tests.find(t => t.scenario_id === scenarioId);
    if (e?.mode !== undefined) {
      setForm({ mode: e.mode as LoadMode, qps: e.throughput ?? 0, ...secondsToModeForm(e.duration) });
    } else {
      setForm(null);
    }
  };

  useEffect(() => {
    let alive = true;
    setConfig(null);
    setConfigError(null);
    setConfigErrorDetail(null);
    setForm(null);
    setEditError(null);
    setEditErrorDetail(null);
    setNeedsCalibration(false);
    setAppliedAt(null);
    if (latestId === undefined) {
      return undefined;
    }
    getExecutionConfig(latestId)
      .then(cfg => {
        if (!alive) return;
        setConfig(cfg);
        prefill(cfg);
      })
      .catch((e: unknown) => {
        if (alive) {
          setConfigError(e instanceof ApiError ? e.message : 'failed to load run config');
          setConfigErrorDetail(errorDetails(e));
        }
      });
    return () => {
      alive = false;
    };
    // prefill is a pure closure over scenarioId; re-running on latestId is
    // the point.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [latestId, scenarioId]);

  // The profile read behind the hint line (and the no-profile refusal's
  // remediation loop): one fetch per (scenario, engine).
  useEffect(() => {
    let alive = true;
    setPerPodQps(null);
    setFanoutEngines(null);
    getCapacityProfile(scenarioId, { engine, ...HOUSE_POD })
      .then(p => {
        if (alive) setPerPodQps(p.per_pod_qps);
      })
      .catch(() => {
        /* no profile for this key: no hint line */
      });
    return () => {
      alive = false;
    };
  }, [scenarioId, engine]);

  // The fan-out half of the hint: the engine count at the form's CURRENT
  // rate — restating the rate restates the fan-out.
  const formQps = form?.qps ?? 0;
  useEffect(() => {
    if (perPodQps == null || !(formQps > 0)) {
      setFanoutEngines(null);
      return undefined;
    }
    let alive = true;
    fanOutCapacity(scenarioId, { engine, ...HOUSE_POD }, formQps)
      .then(fan => {
        if (alive) setFanoutEngines(fan.status === 'ok' ? (fan.engines ?? null) : null);
      })
      .catch(() => {
        if (alive) setFanoutEngines(null);
      });
    return () => {
      alive = false;
    };
  }, [scenarioId, engine, perPodQps, formQps]);

  // The lifecycle snapshot: one fetch on entry plus the 10s poll (the
  // execution hub's cadence). The start flow's phase-watch reads this
  // same state, so the panel — like the hub — has one source of truth.
  useEffect(() => {
    if (latestId === undefined) {
      setStatus(null);
      return undefined;
    }
    let alive = true;
    // A different execution is now the latest: stale phase state must not
    // survive the switch (an 'deployed' from the old id would skip the
    // new id's deploy).
    setStatus(null);
    const load = () => {
      getExecutionStatus(latestId)
        .then(s => {
          if (alive) setStatus(s);
        })
        .catch(() => {
          /* phase stays unknown; Start renders disabled */
        });
    };
    load();
    const poll = setInterval(load, 10_000);
    return () => {
      alive = false;
      clearInterval(poll);
    };
  }, [latestId]);

  // Apply the restated run settings: the ONLY entry that changes is this
  // scenario's — it becomes a fresh mode entry (the exact shape NewTest's
  // Simple submit builds, zeros for the server to re-resolve eagerly)
  // while every co-execution scenario's entry round-trips untouched. The
  // PUT replaces the whole profile; dropping siblings would be data loss.
  const applyEdit = () => {
    const entry = config?.tests.find(t => t.scenario_id === scenarioId);
    if (form === null || config === null || entry === undefined || latestId === undefined || !modeFormValid(form)) {
      return;
    }
    setEditBusy(true);
    setEditError(null);
    setEditErrorDetail(null);
    setNeedsCalibration(false);
    const tests = config.tests.map(t =>
      t.scenario_id === scenarioId
        ? buildModeTest(t.name || scenarioName || `scenario ${scenarioId}`, scenarioId, form)
        : t
    );
    putExecutionConfig(latestId, { ...config, tests })
      .then(() => getExecutionConfig(latestId))
      .then(fresh => {
        setConfig(fresh);
        prefill(fresh);
        setAppliedAt(new Date().toLocaleTimeString());
      })
      .catch((e: unknown) => {
        setEditError(e instanceof ApiError ? e.message : 'failed to apply run settings');
        setEditErrorDetail(errorDetails(e));
        if (e instanceof ApiError && e.status === 409) {
          setNeedsCalibration(true);
        }
      })
      .finally(() => setEditBusy(false));
  };

  if (executionsError !== null) {
    return (
      <p className="text-sm text-red-600 dark:text-red-400" role="alert">
        {executionsError}
      </p>
    );
  }

  // Never run (or only ever calibrated): the create-and-start surface.
  if (executions !== null && latest === undefined) {
    return (
      <Card data-testid="run-empty">
        <CardContent>
          <p className="text-body-sm font-medium text-slate-900 dark:text-white">No runs yet</p>
          <p className="text-caption mt-1 text-slate-500 dark:text-slate-400">
            This scenario has no load execution. State its load below; saving creates one and starts it.
          </p>
        </CardContent>
      </Card>
    );
  }

  if (latest === undefined || latestId === undefined) {
    return <div data-testid="run-loading" className="h-24 animate-pulse rounded-lg bg-slate-100 dark:bg-slate-700/50" />;
  }

  // This scenario's entry in the latest execution's profile (the 67a list
  // only contains executions whose profile binds the scenario, so a found
  // entry is the normal case; a missing one means the config never saved).
  const entry: ConfigTest | undefined = config?.tests.find(t => t.scenario_id === scenarioId);
  // The inline editor costs execution:update (the config PUT's verb); the
  // Start flow's own grant is checked where the flow mounts (task 3).
  const canEdit = can('execution', 'update');
  // The qps helper line: only when the profile answered for this engine.
  const qpsHint =
    perPodQps != null && perPodQps > 0
      ? `profile: ~${perPodQps} qps/pod${fanoutEngines != null ? `, ${fanoutEngines} engines` : ''}`
      : undefined;

  return (
    <Card data-testid="run-panel">
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
        <CardTitle className="flex flex-wrap items-center gap-2">
          Latest run
          {status?.phase !== undefined && (
            <span
              className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${PHASE_CLASSES[status.phase]}`}
              data-testid="run-phase"
            >
              {status.phase}
            </span>
          )}
          {entry?.mode !== undefined && (
            <span
              className="inline-flex items-center rounded-full bg-sky-100 px-2.5 py-0.5 text-xs font-medium text-sky-800 dark:bg-sky-900/30 dark:text-sky-300"
              data-testid="run-mode-chip"
              title="simplified mode: the server resolved concurrency, engines, and ramp-up at save time"
            >
              {modeChipLabel(entry)}
            </span>
          )}
        </CardTitle>
        <Link
          to={`/executions/${latestId}`}
          data-testid="run-execution-link"
          className="text-body-sm font-medium text-sky-600 hover:underline focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:text-sky-400"
        >
          #{latestId} {latest.name} →
        </Link>
      </CardHeader>
      <CardContent className="space-y-4">
        {configError !== null ? (
          <div>
            <p className="text-sm text-red-600 dark:text-red-400" role="alert">
              {configError}
            </p>
            <ActionErrorDetails details={configErrorDetail} />
          </div>
        ) : config === null ? (
          <p className="text-caption text-slate-500 dark:text-slate-400" data-testid="run-config-loading">
            Loading run config…
          </p>
        ) : entry === undefined ? (
          <p className="text-caption text-slate-500 dark:text-slate-400" data-testid="run-config-missing">
            The latest execution has no saved load config for this scenario yet — create one from its page.
          </p>
        ) : (
          <>
            <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4" data-testid="run-resolved">
              <div>
                <dt className="text-caption font-medium text-slate-500 dark:text-slate-400">Target rate</dt>
                <dd className="text-body-sm text-slate-900 dark:text-white">
                  {entry.throughput != null ? `${entry.throughput} req/s` : 'unlimited'}
                </dd>
              </div>
              <div>
                <dt className="text-caption font-medium text-slate-500 dark:text-slate-400">Duration</dt>
                <dd className="text-body-sm text-slate-900 dark:text-white">{entry.duration}s</dd>
              </div>
              <div>
                <dt className="text-caption font-medium text-slate-500 dark:text-slate-400">Engines</dt>
                <dd className="text-body-sm text-slate-900 dark:text-white">{entry.engines}</dd>
              </div>
              <div>
                <dt className="text-caption font-medium text-slate-500 dark:text-slate-400">Concurrency</dt>
                <dd className="text-body-sm text-slate-900 dark:text-white">{entry.concurrency}</dd>
              </div>
            </dl>
            {entry.mode === undefined && (
              <p className="text-caption text-slate-500 dark:text-slate-400" data-testid="run-advanced-entry">
                Advanced config (no simplified mode) — edit it on the{' '}
                <Link to={`/executions/${latestId}`} className="text-sky-600 underline dark:text-sky-400">
                  execution page
                </Link>
                .
              </p>
            )}
            {/* The inline editor: mode + rate + duration, the NewTest Simple
                row's exact shape and validation (soak warning included).
                Only the scenario's own entry restates; co-entries ride the
                PUT untouched (see applyEdit). Hidden without the
                execution:update grant. */}
            {entry.mode !== undefined && form !== null && canEdit && (
              <div
                className="space-y-3 border-t border-slate-200 pt-4 dark:border-slate-700"
                data-testid="run-edit"
              >
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <p className="text-caption font-medium text-slate-600 dark:text-slate-300">Run settings</p>
                  {appliedAt && (
                    <span className="text-caption text-slate-500 dark:text-slate-400" data-testid="run-applied-at">
                      re-applied {appliedAt}
                    </span>
                  )}
                </div>
                <ModeForm value={form} onChange={setForm} disabled={editBusy} qpsHint={qpsHint} />
                <div className="flex flex-wrap items-center gap-3">
                  <Button
                    onClick={applyEdit}
                    disabled={editBusy || !modeFormValid(form)}
                    className="active:scale-95"
                    data-testid="run-apply"
                  >
                    {editBusy ? 'Applying…' : 'Apply run settings'}
                  </Button>
                  <span className="text-caption text-slate-500 dark:text-slate-400">
                    Saving re-resolves concurrency, engines, and ramp-up against the current calibration.
                  </span>
                </div>
                {editError !== null && (
                  <div>
                    <p className="text-sm text-red-600 dark:text-red-400" role="alert">
                      {editError}
                    </p>
                    <ActionErrorDetails details={editErrorDetail} />
                    {/* The no-profile refusal's loop-closer: the Calibrate
                        action lives on this same page's Runs tab. */}
                    {needsCalibration && (
                      <p
                        className="mt-2 text-caption text-slate-600 dark:text-slate-300"
                        data-testid="run-calibrate-remediation"
                      >
                        Calibrate this scenario first, then re-apply.{' '}
                        <button
                          type="button"
                          className="font-medium text-sky-600 underline focus:outline-none focus:ring-2 focus:ring-sky-500 dark:text-sky-400"
                          data-testid="run-calibrate-link"
                          onClick={onOpenCalibration}
                        >
                          Go to the Runs tab’s Calibrate →
                        </button>
                      </p>
                    )}
                  </div>
                )}
              </div>
            )}
          </>
        )}
        {lastRun != null && (
          <p
            className="flex flex-wrap items-center gap-2 text-caption text-slate-500 dark:text-slate-400"
            data-testid="run-last-result"
          >
            <span>Last result:</span>
            <RunStatusBadge outcome={lastRun.outcome} />
            <span data-testid="run-last-started">started {formatRowTime(lastRun.startedAt)}</span>
          </p>
        )}
        {/* The phase-93 start flow mounts here (task 3). */}
      </CardContent>
    </Card>
  );
}
