import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import Button from '../components/ui/Button';
import Card, { CardContent, CardHeader, CardTitle } from '../components/ui/Card';
import StageEditor, { type StageEditorState } from '../components/StageEditor';
import { apiClient, ApiError, errorDetails } from '../api/client';
import { setScenarioRequests } from '../api/scenarios';
import { useSession } from '../hooks/useSession';
import {
  buildFragment,
  concurrencyEnginesWarning,
  stepError,
  flowSteps,
  type NewTestForm,
} from '../lib/newTestFlow';
import { stagesToConfig, type StagesConfigJSON } from '../lib/stagesConfig';
import ActionErrorDetails from '../components/ActionErrorDetails';

interface ProjectRef {
  id: number;
  name: string;
}

const inputCls =
  'rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm text-slate-900 dark:border-slate-600 dark:bg-slate-800 dark:text-slate-100';

/** The page's initial form. The load-shaping fields (concurrency, engines,
 *  rampup, duration) seed the stage editor's first row; there is no
 *  throughput field on this form, so the seeded stage is unlimited --
 *  exactly what buildConfig emitted (the key was never written). */
const initialForm: NewTestForm = {
  name: '',
  targetUrl: '',
  method: 'GET',
  headers: [],
  concurrency: 50,
  engines: 2,
  // R9: ramp-up defaults NON-ZERO -- starting at full concurrency
  // measures connection-pool cold start, not steady state.
  rampup: 30,
  duration: 300,
  engine: 'jmeter',
};

/** The editor's first stage, derived once from the form defaults. */
function seedStage(): StageEditorState {
  return {
    mode: 'table',
    rows: [
      {
        name: '', // assigned at submit from the typed test name
        scenarioId: 0, // assigned at submit from the created scenario
        concurrency: initialForm.concurrency,
        engines: initialForm.engines,
        rampup: initialForm.rampup,
        duration: initialForm.duration,
      },
    ],
    rawJson: '',
  };
}

/**
 * R9: one form, zero identifiers. The flow creates everything in order
 * (project-if-absent -> scenario -> execution -> fragment -> config) and
 * navigates to the execution hub. A failure names the STEP that failed.
 * The load config (step 5) is shaped by the visual StageEditor; its
 * single-stage output is byte-identical to the old form-built JSON.
 */
export default function NewTest() {
  const navigate = useNavigate();
  const { can } = useSession();
  const canCreate = can('execution', 'create');
  const [form, setForm] = useState<NewTestForm>(initialForm);
  const [stages, setStages] = useState<StageEditorState>(seedStage);
  const [stagesValid, setStagesValid] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The structured half of the last failure (phase 24): remediation hint /
  // numbers when the server's envelope carried them.
  const [errorDetail, setErrorDetail] = useState<Record<string, unknown> | null>(null);
  const [step, setStep] = useState<string | null>(null);

  // R9's clamp guard, per stage row. Raw JSON mode skips it: there the
  // operator owns the config verbatim and the backend's own Validate is
  // the authority.
  const warning =
    stages.mode === 'table'
      ? (stages.rows.map((r) => concurrencyEnginesWarning(r.concurrency, r.engines)).find((w) => w !== null) ?? null)
      : null;
  const set = (patch: Partial<NewTestForm>) => setForm((f) => ({ ...f, ...patch }));
  const setHeader = (i: number, hpatch: Partial<{ name: string; value: string }>) =>
    set({ headers: form.headers.map((h, j) => (j === i ? { ...h, ...hpatch } : h)) });

  const submit = () => {
    if (!form.name.trim() || !form.targetUrl.trim()) {
      setError('Name and target URL are required.');
      return;
    }
    setBusy(true);
    setError(null);
    setErrorDetail(null);

    const run = async () => {
      // Step 1: resolve project-if-absent. The operator names a project;
      // the flow looks it up and creates it only when missing.
      setStep(flowSteps[0]);
      let project: ProjectRef;
      const projects = await apiClient.get<ProjectRef[] | null>('/projects').catch((e: unknown) => {
        throw stepError('resolve project', e);
      });
      const wanted = `tests-${form.name.trim().toLowerCase().replace(/\s+/g, '-')}`;
      const existing = (projects ?? []).find((p) => p.name === wanted);
      if (existing) {
        project = existing;
      } else {
        project = await apiClient
          .post<ProjectRef>('/projects', new URLSearchParams({ name: wanted, owner: 'honryu' }))
          .catch((e: unknown) => {
            throw stepError('resolve project', e);
          });
      }

      // Step 2: scenario (portable; no kind needed -- default).
      setStep(flowSteps[1]);
      const scenario = (await apiClient
        .post<{ id: number }>('/scenarios', new URLSearchParams({ project_id: String(project.id), name: form.name }))
        .catch((e: unknown) => {
          throw stepError('create scenario', e);
        })) as { id: number; scenario?: { id: number } };
      const scenarioId = scenario.id ?? scenario.scenario?.id;

      // Step 3: execution.
      setStep(flowSteps[2]);
      const execution = await apiClient
        .post<{ id: number }>(
          '/executions',
          new URLSearchParams({ project_id: String(project.id), name: form.name, engine: form.engine }),
        )
        .catch((e: unknown) => {
          throw stepError('create execution', e);
        });

      // Step 4: the requests fragment (G3, text/yaml verbatim).
      setStep(flowSteps[3]);
      await setScenarioRequests(scenarioId, buildFragment(form)).catch((e: unknown) => {
        throw stepError('save requests fragment', e);
      });

      // Step 5: the load config (G7's JSON body), shaped by the stage
      // editor. Table mode is the guided path: the flow owns the test
      // name and scenario binding (buildConfig's exact mapping), so the
      // payload is byte-identical to the pre-editor flow. Raw mode is the
      // escape hatch: submit the JSON as typed, patching only what the
      // flow owns -- the wrapper ids always; scenario id and test name
      // when the operator left them at their placeholder values.
      setStep(flowSteps[4]);
      let cfg: StagesConfigJSON;
      try {
        if (stages.mode === 'table') {
          cfg = stagesToConfig(
            stages.rows.map((r) => ({ ...r, name: form.name, scenarioId })),
            `${form.name}-load`,
            project.id,
            execution.id,
          );
        } else {
          cfg = JSON.parse(stages.rawJson) as StagesConfigJSON;
          cfg.project_id = project.id;
          cfg.execution_id = execution.id;
          for (const t of cfg.tests) {
            if (!t.scenario_id) {
              t.scenario_id = scenarioId;
            }
            if (!t.name) {
              t.name = form.name;
            }
          }
        }
      } catch (e: unknown) {
        throw stepError(flowSteps[4], e);
      }
      await apiClient
        .putRaw(`/executions/${execution.id}/config`, 'application/json', JSON.stringify(cfg))
        .catch((e: unknown) => {
          throw stepError(flowSteps[4], e);
        });

      // Done: land on the deep-linkable hub.
      navigate(`/executions/${execution.id}`);
    };

    run()
      .catch((e: unknown) => {
        setError(e instanceof ApiError ? e.message : e instanceof Error ? e.message : 'Something failed.');
        setErrorDetail(errorDetails(e));
      })
      .finally(() => setBusy(false));
  };

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-display-sm text-slate-900 dark:text-white">New test</h1>
        <p className="text-body-sm mt-1 text-slate-500 dark:text-slate-400">
          Describe the test; the project, scenario, and execution are created for you.
        </p>
      </div>
      <Card>
        <CardHeader>
          <CardTitle>Test definition</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <label className="text-caption text-slate-600 dark:text-slate-300">
              Test name
              <input className={`${inputCls} mt-1 w-full`} value={form.name} onChange={(e) => set({ name: e.target.value })} placeholder="checkout-smoke" />
            </label>
            <label className="text-caption text-slate-600 dark:text-slate-300">
              Engine
              <select className={`${inputCls} mt-1 w-full`} value={form.engine} onChange={(e) => set({ engine: e.target.value })}>
                <option value="jmeter">jmeter</option>
                <option value="gatling">gatling</option>
                <option value="k6">k6</option>
              </select>
            </label>
          </div>
          <label className="block text-caption text-slate-600 dark:text-slate-300">
            Target URL
            <input className={`${inputCls} mt-1 w-full`} value={form.targetUrl} onChange={(e) => set({ targetUrl: e.target.value })} placeholder="http://checkout.svc" />
          </label>
          <div>
            <div className="flex items-center justify-between">
              <p className="text-caption text-slate-600 dark:text-slate-300">Headers (a cookie is a header)</p>
              <Button variant="ghost" onClick={() => set({ headers: [...form.headers, { name: '', value: '' }] })}>
                + Add header
              </Button>
            </div>
            {form.headers.length === 0 && <p className="text-caption text-slate-400">No headers.</p>}
            <div className="mt-2 space-y-2">
              {form.headers.map((h, i) => (
                <div key={i} className="flex gap-2">
                  <input className={`${inputCls} flex-1`} value={h.name} onChange={(e) => setHeader(i, { name: e.target.value })} placeholder="X-Auth" aria-label="header name" />
                  <input className={`${inputCls} flex-1`} value={h.value} onChange={(e) => setHeader(i, { value: e.target.value })} placeholder="token" aria-label="header value" />
                  <Button variant="ghost" onClick={() => set({ headers: form.headers.filter((_, j) => j !== i) })}>
                    ✕
                  </Button>
                </div>
              ))}
            </div>
          </div>
          {warning && (
            <p className="rounded-md bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/30 dark:text-amber-200" role="alert">
              {warning}
            </p>
          )}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>Load stages</CardTitle>
        </CardHeader>
        <CardContent>
          <StageEditor
            state={stages}
            onStateChange={setStages}
            configName={`${form.name}-load`}
            onValidityChange={setStagesValid}
          />
        </CardContent>
      </Card>
      <div className="flex items-center gap-3">
        {canCreate ? (
          <Button onClick={submit} disabled={busy || !stagesValid} data-testid="create-test">
            {busy ? `Working — ${step ?? '…'}` : 'Create test'}
          </Button>
        ) : (
          <p className="text-sm text-slate-500 dark:text-slate-400" data-testid="no-create-permission">
            Your role cannot create executions.
          </p>
        )}
        {busy && step && <span className="text-caption text-slate-500 dark:text-slate-400">step: {step}</span>}
      </div>
      {error && (
        <div>
          <p className="text-sm text-red-600 dark:text-red-400" role="alert">
            {error}
          </p>
          <ActionErrorDetails details={errorDetail} />
        </div>
      )}
    </div>
  );
}
