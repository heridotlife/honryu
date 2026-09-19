// Phase 94/95: the scenario page's Run panel — the run-settings half of
// a scenario. A scenario is the script (steps, thresholds); mode / target
// qps / duration are RUN settings that already live in the execution
// config (execution_scenario + mode migration 0074), so this panel
// surfaces the scenario's most recent execution and its config entry —
// it never duplicates run settings into the scenario document. Phase 95
// made the inputs ALWAYS visible: exactly one form shape (prefilled edit
// when the latest has this scenario's mode entry and the session may PUT
// it; create shape otherwise) renders for every session that can run.
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
import StartCountdown from './StartCountdown';
import { getExecutionConfig, putExecutionConfig, type ConfigTest, type ExecutionConfig } from '../api/executionsConfig';
import { getExecutionStatus, type ExecutionStatus, type Phase } from '../api/status';
import { fanOutCapacity, getCapacityProfile } from '../api/calibration';
import { useSession } from '../hooks/useSession';
import { useStartFlow } from '../hooks/useStartFlow';
import { formatRowTime } from '../lib/executionRow';
import {
  buildModeTest,
  initialModeForm,
  modeChipLabel,
  modeFormValid,
  secondsToModeForm,
  type LoadMode,
  type ModeFormValue,
} from '../lib/modeConfig';
import { ApiError, apiClient, errorDetails } from '../api/client';
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
  projectId,
  executions,
  executionsError,
  lastRun,
  onExecutionsChanged,
}: ScenarioRunPanelProps) {
  const { can } = useSession();
  const latest = latestLoadExecution(executions);
  const latestId = latest?.id;
  // The engine every capacity read and a new execution would use: the
  // newest execution's (a calibration row names the engine it calibrated).
  const engine = defaultEngine(executions);
  // The grants: the inline editor costs execution:update (the config PUT's
  // verb); the Start flow costs run:create (the same verb the hub's Start
  // charges, per phase 93's audit mapping); the create path
  // execution:create. Declared before the early returns — every branch
  // below reads some of them.
  const canEdit = can('execution', 'update');
  const canStart = can('run', 'create');
  const canCreate = can('execution', 'create');

  // The latest execution's config: the whole profile round-trips on edit
  // (the PUT replaces it), so the panel keeps it whole and reads its own
  // scenario's entry out of it.
  const [config, setConfig] = useState<ExecutionConfig | null>(null);
  const [configError, setConfigError] = useState<string | null>(null);
  const [configErrorDetail, setConfigErrorDetail] = useState<Record<string, unknown> | null>(null);
  // The lifecycle snapshot, KEYED BY EXECUTION ID: the guarded read
  // below hands null unless the snapshot's id IS the current latest, so
  // a stale phase from a previous latest (an 'deployed' that would skip
  // a brand-new execution's deploy POST) can never speak for the
  // current one — phase 95's create form now renders alongside an
  // existing latest, and the flow it begins on the flip render reads
  // this guard, not a reset effect that has not flushed yet.
  const [statusSnapshot, setStatusSnapshot] = useState<{ id: number; status: ExecutionStatus } | null>(null);
  const status = statusSnapshot !== null && statusSnapshot.id === latestId ? statusSnapshot.status : null;

  // The inline editor: the entry's mode statement, prefilled from the
  // stored config and re-prefilled after every apply (the server's
  // resolved numbers are the truth; the form only restates the ask).
  const [form, setForm] = useState<ModeFormValue | null>(null);
  const [editBusy, setEditBusy] = useState(false);
  const [editError, setEditError] = useState<string | null>(null);
  const [editErrorDetail, setEditErrorDetail] = useState<Record<string, unknown> | null>(null);
  // 409 from a config write: the no-profile refusal whose remediation is
  // the Calibrate action below the run history on this same tab.
  const [needsCalibration, setNeedsCalibration] = useState(false);
  const [appliedAt, setAppliedAt] = useState<string | null>(null);

  // The capacity hint's two halves: the profile's per-pod rate and the
  // fan-out engine count at the form's current rate. Best-effort reads —
  // absent (never calibrated for this engine, or fetch failed) degrades
  // to no hint line, nothing else.
  const [perPodQps, setPerPodQps] = useState<number | null>(null);
  const [fanoutEngines, setFanoutEngines] = useState<number | null>(null);
  // The qps helper line: only when the profile answered for this engine.
  // Computed here (before the early returns) so the empty state shares it.
  const qpsHint =
    perPodQps != null && perPodQps > 0
      ? `profile: ~${perPodQps} qps/pod${fanoutEngines != null ? `, ${fanoutEngines} engines` : ''}`
      : undefined;

  // The empty state's create form (never-run scenario): the same Simple
  // statement, creating a NEW execution instead of editing an entry.
  const [createForm, setCreateForm] = useState<ModeFormValue>(initialModeForm);
  const [createBusy, setCreateBusy] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [createErrorDetail, setCreateErrorDetail] = useState<Record<string, unknown> | null>(null);
  const [createNeedsCalibration, setCreateNeedsCalibration] = useState(false);
  // The freshly created execution that should begin the start flow as
  // soon as the refetched list names it latest (see the effect below).
  const [startAfterCreate, setStartAfterCreate] = useState<number | null>(null);

  // The start flow's failure surface (message + ActionErrorDetails),
  // separate from the editor's: different actions, different surfaces.
  const [flowError, setFlowError] = useState<string | null>(null);
  const [flowErrorDetail, setFlowErrorDetail] = useState<Record<string, unknown> | null>(null);

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

  // The fan-out half of the hint: the engine count at the mounted form's
  // CURRENT rate — restating the rate restates the fan-out. In the empty
  // state the mounted form is the create form.
  const formQps = form !== null ? form.qps : createForm.qps;
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

  // The lifecycle snapshot's feed: one fetch on entry plus the 10s poll
  // (the execution hub's cadence). The start flow's phase-watch reads the
  // guarded `status` above, so the panel — like the hub — has one source
  // of truth; a snapshot for an id that is no longer latest simply stops
  // being read.
  useEffect(() => {
    if (latestId === undefined) {
      return undefined;
    }
    let alive = true;
    const load = () => {
      getExecutionStatus(latestId)
        .then(s => {
          if (alive) setStatusSnapshot({ id: latestId, status: s });
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

  // Phase 93's one-click Start chain, verbatim: deploy → 10s countdown →
  // trigger from idle; straight countdown when deployed; the page's status
  // poll (above) is the phase-watch's single source of truth. The scenario
  // page shares the hub's contract — begin is only ever called while a
  // latest execution exists, so the hook's id is never the ?? 0 placeholder.
  const startFlow = useStartFlow({
    executionId: latestId ?? 0,
    phase: status?.phase ?? null,
    onStatus: s => setStatusSnapshot({ id: latestId ?? 0, status: s }),
    onError: (message, details) => {
      setFlowError(message);
      setFlowErrorDetail(details);
    },
    onReset: () => {
      setFlowError(null);
      setFlowErrorDetail(null);
    },
  });
  const startBusy = startFlow.step !== null;

  // The create path's second half: the refetched list has named the new
  // execution latest — begin the flow now, against the id the hook
  // received on THIS render. Safe by the id-keyed snapshot: the guarded
  // `status` read is null on this render (the new id has no snapshot
  // yet), so the flow takes the deploy-first path a brand-new execution
  // needs — even when the previous latest was 'deployed' (phase 95: the
  // create form also renders alongside an existing latest).
  useEffect(() => {
    if (startAfterCreate !== null && latestId === startAfterCreate) {
      setStartAfterCreate(null);
      startFlow.begin();
    }
    // begin is stable per render by construction (step/phase closures);
    // the watched pair is the trigger.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [startAfterCreate, latestId]);

  // The empty state's create: the NewTest Simple payload for one existing
  // scenario — POST /executions (form-encoded identity), then the config
  // PUT with the single mode entry — and straight into the Start flow
  // once the refetched list names the new execution latest. A 409 on the
  // PUT leaves the execution created but unconfigured (said so below);
  // the remediation is the Calibrate action below the run history, then
  // retry.
  const createAndStart = () => {
    if (!modeFormValid(createForm)) {
      return;
    }
    setCreateBusy(true);
    setCreateError(null);
    setCreateErrorDetail(null);
    setCreateNeedsCalibration(false);
    apiClient
      .post<{ id: number }>('/executions', new URLSearchParams({ project_id: String(projectId), name: scenarioName, engine }))
      .then(execution =>
        putExecutionConfig(execution.id, {
          name: `${scenarioName}-load`,
          project_id: projectId,
          execution_id: execution.id,
          tests: [buildModeTest(scenarioName, scenarioId, createForm)],
        }).then(() => execution)
      )
      .then(execution => {
        setStartAfterCreate(execution.id);
        onExecutionsChanged();
      })
      .catch((e: unknown) => {
        setCreateError(e instanceof ApiError ? e.message : 'failed to create the run');
        setCreateErrorDetail(errorDetails(e));
        if (e instanceof ApiError && e.status === 409) {
          setCreateNeedsCalibration(true);
        }
      })
      .finally(() => setCreateBusy(false));
  };

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

  // The create shape's shared surface (phase 95's unification): the same
  // ModeForm + Create-and-start + failure trio mounts in the empty state
  // (never run) and — when the latest is advanced or un-PUTtable — inside
  // the statement card. Grants on this path: the form needs
  // execution:create (POST /executions + its config PUT); the chained
  // start additionally costs run:create on the wire — the same verb the
  // plain Start charges — and the flow surfaces its refusal honestly if
  // the session holds only the create half.
  const createSurface = (
    <>
      <ModeForm value={createForm} onChange={setCreateForm} disabled={createBusy || startBusy} qpsHint={qpsHint} />
      <Button
        onClick={createAndStart}
        disabled={createBusy || startBusy || !modeFormValid(createForm)}
        className="active:scale-95"
        data-testid="run-create-start"
      >
        {createBusy ? 'Creating…' : 'Create and start'}
      </Button>
      {createError !== null && (
        <div>
          <p className="text-sm text-red-600 dark:text-red-400" role="alert">
            {createError}
          </p>
          <ActionErrorDetails details={createErrorDetail} />
          {createNeedsCalibration && (
            <p
              className="mt-2 text-caption text-slate-600 dark:text-slate-300"
              data-testid="run-calibrate-remediation"
            >
              Calibrate this scenario first, then retry — the run was created but not configured. The Calibrate
              action sits below the run history on this tab.
            </p>
          )}
        </div>
      )}
    </>
  );

  // Never run (or only ever calibrated): the create-and-start surface —
  // the NewTest Simple statement for this one scenario, creating a NEW
  // execution and going straight into the Start flow.
  if (executions !== null && latest === undefined) {
    return (
      <Card data-testid="run-empty">
        <CardHeader>
          <CardTitle>Run this scenario</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <p className="text-caption text-slate-500 dark:text-slate-400">
            No runs yet — this scenario has never been bound to a load execution. State its load; creating the run
            starts it immediately.
          </p>
          {canCreate ? (
            createSurface
          ) : (
            <p className="text-sm text-slate-500 dark:text-slate-400" data-testid="run-no-create-permission">
              Your role cannot create executions.
            </p>
          )}
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

  // The unified inputs (phase 95): exactly ONE form shape renders, picked
  // by what the latest holds and what the session grants.
  // - Edit shape (prefilled, Apply = the config PUT): the latest has THIS
  //   scenario's simplified-mode entry AND the session holds
  //   execution:update — the PUT's verb.
  // - Create shape (Create and start = POST /executions + its config PUT
  //   + the phase-93 flow): every other case where the session holds
  //   execution:create — an advanced latest (mode undefined) or a mode
  //   entry this session cannot PUT. New run = new execution; forking the
  //   history is expected (the advanced note points in-place edits at
  //   the execution page).
  // - No form at all: a session with NEITHER execution:update NOR
  //   execution:create — the statement above and the run history below
  //   stay (RBAC honesty: no dead form).
  const showEdit = entry?.mode !== undefined && form !== null && canEdit;
  const showCreate = !showEdit && canCreate;

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
            {/* The unified inputs (see showEdit/showCreate above). Edit
                shape: mode + rate + duration, the NewTest Simple row's
                exact shape and validation (soak warning included). Only
                the scenario's own entry restates; co-entries ride the PUT
                untouched (see applyEdit). Costs execution:update. */}
            {showEdit && (
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
                <ModeForm value={form} onChange={setForm} disabled={editBusy || startBusy} qpsHint={qpsHint} />
                <div className="flex flex-wrap items-center gap-3">
                  <Button
                    onClick={applyEdit}
                    disabled={editBusy || startBusy || !modeFormValid(form)}
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
                        action sits below the run history on this same tab
                        (phase 95 merged the old Runs tab into Run). */}
                    {needsCalibration && (
                      <p
                        className="mt-2 text-caption text-slate-600 dark:text-slate-300"
                        data-testid="run-calibrate-remediation"
                      >
                        Calibrate this scenario first, then re-apply — the Calibrate action sits below the run history
                        on this tab.
                      </p>
                    )}
                  </div>
                )}
              </div>
            )}
            {/* Create shape: the always-visible inputs when the edit shape
                is not available — advanced latest, or a mode entry this
                session cannot PUT. Costs execution:create (+ run:create
                when the chained start fires). */}
            {showCreate && (
              <div
                className="space-y-3 border-t border-slate-200 pt-4 dark:border-slate-700"
                data-testid="run-create"
              >
                <p className="text-caption font-medium text-slate-600 dark:text-slate-300">New run</p>
                <p className="text-caption text-slate-500 dark:text-slate-400">
                  {entry.mode === undefined
                    ? 'State the load for a fresh run — creating it starts it immediately. The latest run’s advanced config stays as it is (new run = new execution).'
                    : `Your role cannot restate execution #${latestId}’s config — creating a run states its load on a fresh execution instead.`}
                </p>
                {createSurface}
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
        {/* The phase-93 start flow — the hub's Start composite, verbatim
            contract: idle → deploy + countdown + trigger, deployed →
            countdown + trigger, Cancel while in flight; while busy the
            editor above is locked. Running offers no Start here (the hub
            owns Stop); the panel links through instead. */}
        <div
          className="flex flex-wrap items-center gap-2 border-t border-slate-200 pt-4 dark:border-slate-700"
          role="group"
          aria-label="Run controls"
          aria-busy={startBusy}
          data-testid="run-controls"
        >
          {startBusy ? (
            startFlow.step === 'counting' ? (
              <StartCountdown
                seconds={startFlow.seconds}
                onComplete={startFlow.countdownComplete}
                onCancel={startFlow.cancel}
              />
            ) : (
              <span className="inline-flex items-center gap-2" data-testid={`start-flow-${startFlow.step}`}>
                <span className="text-body-sm font-medium text-slate-700 dark:text-slate-200">
                  {startFlow.step === 'deploying' ? 'Deploying engines…' : 'Starting run…'}
                </span>
                {startFlow.step === 'deploying' && (
                  <Button
                    size="sm"
                    variant="outline"
                    className="active:scale-95"
                    data-testid="start-flow-cancel"
                    onClick={startFlow.cancel}
                  >
                    Cancel
                  </Button>
                )}
              </span>
            )
          ) : status?.phase === 'running' ? (
            <p className="text-caption text-slate-500 dark:text-slate-400" data-testid="run-running-note">
              A run is in progress —{' '}
              <Link
                to={`/executions/${latestId}`}
                className="font-medium text-sky-600 underline dark:text-sky-400"
              >
                view execution #{latestId}
              </Link>{' '}
              to watch or stop it.
            </p>
          ) : (
            // While the create shape is the active form, its submit IS the
            // start path (create + start); the plain Start would target the
            // latest as-is, which this path deliberately replaces. Without
            // the create shape the plain Start keeps its phase-93 contract.
            canStart &&
            !showCreate && (
              <Button
                data-testid="run-start"
                variant="accent"
                className="active:scale-95"
                disabled={status === null || editBusy}
                onClick={startFlow.begin}
              >
                Start
              </Button>
            )
          )}
          {flowError !== null && (
            <div className="w-full">
              <p className="text-sm text-red-600 dark:text-red-400" role="alert">
                {flowError}
              </p>
              <ActionErrorDetails details={flowErrorDetail} />
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
