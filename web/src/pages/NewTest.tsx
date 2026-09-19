import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import Button from '../components/ui/Button';
import Card, { CardContent, CardHeader, CardTitle } from '../components/ui/Card';
import ErrorSummary, { type ErrorSummaryEntry } from '../components/ui/ErrorSummary';
import FieldError from '../components/ui/FieldError';
import StageEditor, { type StageEditorState } from '../components/StageEditor';
import { useFieldValidation } from '../hooks/useFieldValidation';
import { apiClient, ApiError, errorDetails } from '../api/client';
import { instantiateScenario, listTemplates, setScenarioRequests, type Template } from '../api/scenarios';
import { listClusters, type Cluster } from '../api/clusters';
import { useSession } from '../hooks/useSession';
import { buildFragment, concurrencyEnginesWarning, stepError, flowSteps, type NewTestForm } from '../lib/newTestFlow';
import { stagesToConfig, type StagesConfigJSON } from '../lib/stagesConfig';
import ActionErrorDetails from '../components/ActionErrorDetails';
import ModeRowsForm from '../components/ModeRowsForm';
import { buildModeTests, initialModeRows, modeRowsValid, scenarioName, type ModeRowValue } from '../lib/modeConfig';

interface ProjectRef {
  id: number;
  name: string;
}

const inputCls =
  'rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm text-slate-900 dark:border-slate-600 dark:bg-slate-800 dark:text-slate-100';

/** The Load card's segmented toggle styling (StageEditor's tab convention). */
const loadTabCls = (active: boolean) =>
  [
    'px-3 py-1.5 text-sm font-medium transition-colors',
    'focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500',
    active
      ? 'bg-slate-200 text-slate-900 dark:bg-slate-700 dark:text-white'
      : 'text-slate-600 hover:bg-slate-50 dark:text-slate-300 dark:hover:bg-slate-800',
  ].join(' ');

/** The identity fields' element ids -- the error summary links to them. */
const FIELD_IDS = { name: 'newtest-name', targetUrl: 'newtest-target-url' } as const;

/** The identity fields' blur/submit validators: the same two rules the
 * submit guard always enforced, now stated per field. */
const FIELD_VALIDATORS: Record<keyof typeof FIELD_IDS, (value: string) => string | null> = {
  name: v => (v.trim() === '' ? 'Test name is required.' : null),
  targetUrl: v => (v.trim() === '' ? 'Target URL is required.' : null),
};

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
  // Phase 90: the Load card's Simple | Advanced toggle -- Simple (the
  // default) states mode + rate + duration per scenario and lets the
  // server resolve everything else; Advanced is the pre-phase-90 stage
  // editor, verbatim. Phase 91: Simple is multi-scenario -- one row per
  // scenario, each its own mode entry resolved independently.
  const [loadTab, setLoadTab] = useState<'simple' | 'advanced'>('simple');
  const [modeRows, setModeRows] = useState<ModeRowValue[]>(initialModeRows);
  const [stages, setStages] = useState<StageEditorState>(seedStage);
  const [stagesValid, setStagesValid] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Blur + submit validation for the identity fields (phase 77): each
  // field's error shows once it is blurred (earlier feedback than the
  // submit guard, which stays as the backstop), and a failed submit moves
  // focus to the summary at the card's top.
  const fields = useFieldValidation();
  // The structured half of the last failure (phase 24): remediation hint /
  // numbers when the server's envelope carried them.
  const [errorDetail, setErrorDetail] = useState<Record<string, unknown> | null>(null);
  const [step, setStep] = useState<string | null>(null);

  // --- From template (phase 65) -------------------------------------------
  // The catalog is server-driven: the picker renders GET /api/templates
  // verbatim and instantiate clones server-side; nothing is copied in the
  // client. A catalog failure must never touch the from-scratch flow, so it
  // degrades to a note inside this card only.
  const canCreateScenario = can('scenario', 'create');
  const [templates, setTemplates] = useState<Template[] | null>(null);
  const [templatesUnavailable, setTemplatesUnavailable] = useState(false);
  const [templateId, setTemplateId] = useState('');
  const [templateName, setTemplateName] = useState('');
  const [templateTarget, setTemplateTarget] = useState('');
  const [templateBusy, setTemplateBusy] = useState(false);
  const [templateStep, setTemplateStep] = useState<string | null>(null);
  const [templateError, setTemplateError] = useState<string | null>(null);
  const [templateErrorDetail, setTemplateErrorDetail] = useState<Record<string, unknown> | null>(null);

  // --- Fan-out targets (phase 88) ------------------------------------------
  // The registered BYOC clusters, offered as fan-out targets: checking any
  // makes the execution run its full load profile on every checked cluster
  // simultaneously. A registry read failure degrades to the disabled note,
  // never to a failed form. Disabled when no BYOC cluster is registered --
  // there is nothing to fan out to (the deployment default needs no target).
  const [clusters, setClusters] = useState<Cluster[] | null>(null);
  const [clustersUnavailable, setClustersUnavailable] = useState(false);
  const [fanOutTargets, setFanOutTargets] = useState<Set<string>>(new Set());

  useEffect(() => {
    let alive = true;
    listClusters()
      .then(cs => {
        if (alive) setClusters(cs);
      })
      .catch(() => {
        if (alive) setClustersUnavailable(true);
      });
    return () => {
      alive = false;
    };
  }, []);

  const byocClusters = (clusters ?? []).filter(c => c.origin === 'byoc');
  const toggleFanOut = (name: string) => {
    setFanOutTargets(prev => {
      const next = new Set(prev);
      if (next.has(name)) {
        next.delete(name);
      } else {
        next.add(name);
      }
      return next;
    });
  };

  useEffect(() => {
    let alive = true;
    listTemplates()
      .then(t => {
        if (alive) setTemplates(t);
      })
      .catch(() => {
        if (alive) setTemplatesUnavailable(true);
      });
    return () => {
      alive = false;
    };
  }, []);

  const createFromTemplate = () => {
    if (!templateId || !templateName.trim()) {
      setTemplateError('Pick a template and name the new test.');
      return;
    }
    setTemplateBusy(true);
    setTemplateError(null);
    setTemplateErrorDetail(null);

    const run = async () => {
      // Same project-if-absent convention as the from-scratch flow: the
      // operator names a test; the project is derived from it. The step
      // labels are the flow's own (resolve project / create scenario) --
      // the template path is the same two steps with a shorter second one.
      setTemplateStep(flowSteps[0]);
      let project: ProjectRef;
      const projects = await apiClient.get<ProjectRef[] | null>('/projects').catch((e: unknown) => {
        throw stepError(flowSteps[0], e);
      });
      const wanted = `tests-${templateName.trim().toLowerCase().replace(/\s+/g, '-')}`;
      const existing = (projects ?? []).find(p => p.name === wanted);
      if (existing) {
        project = existing;
      } else {
        project = await apiClient
          .post<ProjectRef>('/projects', new URLSearchParams({ name: wanted, owner: 'honryu' }))
          .catch((e: unknown) => {
            throw stepError(flowSteps[0], e);
          });
      }

      // One server call does the cloning: the new scenario is ordinary
      // (is_template false), carries the template's requests fragment with
      // at most the target URL replaced, and the template itself is
      // untouched. Overrides left empty cross the wire absent.
      setTemplateStep(flowSteps[1]);
      const scenarioId = await instantiateScenario(Number(templateId), {
        name: templateName.trim(),
        projectId: project.id,
        targetUrl: templateTarget.trim() || undefined,
      }).catch((e: unknown) => {
        throw stepError(flowSteps[1], e);
      });

      // Phase 67b: the detail page's default tab is the run history;
      // instantiation still lands where the flow always ended -- the
      // editor for the fresh fragment.
      navigate(`/scenarios/${scenarioId}?tab=editor`);
    };

    run()
      .catch((e: unknown) => {
        setTemplateError(e instanceof ApiError ? e.message : e instanceof Error ? e.message : 'Something failed.');
        setTemplateErrorDetail(errorDetails(e));
      })
      .finally(() => setTemplateBusy(false));
  };

  // R9's clamp guard, per stage row. Raw JSON mode skips it: there the
  // operator owns the config verbatim and the backend's own Validate is
  // the authority.
  const warning =
    stages.mode === 'table'
      ? (stages.rows.map(r => concurrencyEnginesWarning(r.concurrency, r.engines)).find(w => w !== null) ?? null)
      : null;
  const set = (patch: Partial<NewTestForm>) => setForm(f => ({ ...f, ...patch }));
  const setHeader = (i: number, hpatch: Partial<{ name: string; value: string }>) =>
    set({ headers: form.headers.map((h, j) => (j === i ? { ...h, ...hpatch } : h)) });

  /** Every identity field's current verdict (null while legal). */
  const fieldErrors = {
    name: FIELD_VALIDATORS.name(form.name),
    targetUrl: FIELD_VALIDATORS.targetUrl(form.targetUrl),
  };
  /** The submit-failure summary's entries, in field order. */
  const summaryEntries: ErrorSummaryEntry[] = (Object.keys(FIELD_IDS) as Array<keyof typeof FIELD_IDS>)
    .filter(f => fieldErrors[f] !== null)
    .map(f => ({ fieldId: FIELD_IDS[f], message: fieldErrors[f]! }));

  const submit = () => {
    fields.markSubmitted();
    // The per-field guard: the summary renders (and takes focus) instead
    // of a single combined message; the old guard's rule is unchanged.
    if (summaryEntries.length > 0) {
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
      const existing = (projects ?? []).find(p => p.name === wanted);
      if (existing) {
        project = existing;
      } else {
        project = await apiClient
          .post<ProjectRef>('/projects', new URLSearchParams({ name: wanted, owner: 'honryu' }))
          .catch((e: unknown) => {
            throw stepError('resolve project', e);
          });
      }

      // Step 2: scenarios (portable; no kind needed -- default). Simple
      // mode creates one per Load-card row -- each row is its own scenario
      // and its own mode entry; Advanced creates the one the stage rows
      // share, exactly as before.
      setStep(flowSteps[1]);
      const createScenario = async (name: string): Promise<number> => {
        const scenario = (await apiClient
          .post<{ id: number }>('/scenarios', new URLSearchParams({ project_id: String(project.id), name }))
          .catch((e: unknown) => {
            throw stepError('create scenario', e);
          })) as { id: number; scenario?: { id: number } };
        return scenario.id ?? scenario.scenario?.id;
      };
      let scenarioIds: number[];
      if (loadTab === 'simple') {
        scenarioIds = [];
        for (let i = 0; i < modeRows.length; i++) {
          scenarioIds.push(await createScenario(scenarioName(modeRows[i], i, form.name)));
        }
      } else {
        scenarioIds = [await createScenario(form.name)];
      }
      const scenarioId = scenarioIds[0];

      // Step 3: execution. fanout_targets rides the form only when the
      // operator checked targets -- the request every pre-fan-out client
      // sends stays byte-identical otherwise.
      setStep(flowSteps[2]);
      const executionForm = new URLSearchParams({
        project_id: String(project.id),
        name: form.name,
        engine: form.engine,
      });
      if (fanOutTargets.size > 0) {
        executionForm.set('fanout_targets', JSON.stringify([...fanOutTargets].sort()));
      }
      const execution = await apiClient.post<{ id: number }>('/executions', executionForm).catch((e: unknown) => {
        throw stepError('create execution', e);
      });

      // Step 4: the requests fragment (G3, text/yaml verbatim). Every
      // scenario of the test carries the same request shape -- the Test
      // definition card's method, target URL, and headers.
      setStep(flowSteps[3]);
      const fragment = buildFragment(form);
      for (const sid of scenarioIds) {
        await setScenarioRequests(sid, fragment).catch((e: unknown) => {
          throw stepError('save requests fragment', e);
        });
      }

      // Step 5: the load config (G7's JSON body). Simple mode PUTs one
      // mode-shaped statement per Load-card row (mode + rate + duration,
      // each row bound to its own scenario) and the server resolves each
      // independently; Advanced is the stage editor, unchanged -- table
      // mode byte-identical to the pre-editor flow, raw mode the verbatim
      // escape hatch.
      setStep(flowSteps[4]);
      let cfg: StagesConfigJSON;
      try {
        if (loadTab === 'simple') {
          cfg = {
            name: `${form.name}-load`,
            project_id: project.id,
            execution_id: execution.id,
            tests: buildModeTests(form.name, scenarioIds, modeRows),
          };
        } else if (stages.mode === 'table') {
          cfg = stagesToConfig(
            stages.rows.map(r => ({ ...r, name: form.name, scenarioId })),
            `${form.name}-load`,
            project.id,
            execution.id
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
      // A mode config's 409 (no usable capacity profile) must not strand
      // the operator on this page: the execution exists and its page is
      // where the Calibrate action lives. The error rides along as router
      // state and the destination renders it.
      const configErr = await apiClient
        .putRaw(`/executions/${execution.id}/config`, 'application/json', JSON.stringify(cfg))
        .then(() => null)
        .catch((e: unknown) => stepError(flowSteps[4], e));

      // Done: land on the deep-linkable hub -- carrying the config error
      // (if any) for the banner there. Extracted here, while the ApiError
      // (and its structured envelope) is still in hand; the banner renders
      // plain strings.
      navigate(`/executions/${execution.id}`, {
        state: configErr
          ? {
              configError: configErr instanceof Error ? configErr.message : String(configErr),
              configErrorDetail: errorDetails(configErr),
            }
          : undefined,
      });
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
          {/* Submit-failure summary: focusable, links to each field. */}
          <ErrorSummary entries={summaryEntries} testId="newtest-error-summary" />
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <label className="text-caption text-slate-600 dark:text-slate-300">
              Test name
              <input
                id={FIELD_IDS.name}
                className={`${inputCls} mt-1 w-full`}
                value={form.name}
                onChange={e => set({ name: e.target.value })}
                onBlur={() => fields.blur('name')}
                aria-invalid={fields.visibleError('name', fieldErrors.name) !== null}
                placeholder="checkout-smoke"
              />
              {fields.visibleError('name', fieldErrors.name) !== null && (
                <FieldError message={fieldErrors.name!} testId="newtest-name-error" />
              )}
            </label>
            <label className="text-caption text-slate-600 dark:text-slate-300">
              Engine
              <select
                className={`${inputCls} mt-1 w-full`}
                value={form.engine}
                onChange={e => set({ engine: e.target.value })}
              >
                <option value="jmeter">jmeter</option>
                <option value="gatling">gatling</option>
                <option value="k6">k6</option>
              </select>
            </label>
          </div>
          <label className="block text-caption text-slate-600 dark:text-slate-300">
            Target URL
            <input
              id={FIELD_IDS.targetUrl}
              className={`${inputCls} mt-1 w-full`}
              value={form.targetUrl}
              onChange={e => set({ targetUrl: e.target.value })}
              onBlur={() => fields.blur('targetUrl')}
              aria-invalid={fields.visibleError('targetUrl', fieldErrors.targetUrl) !== null}
              placeholder="http://checkout.svc"
            />
            {fields.visibleError('targetUrl', fieldErrors.targetUrl) !== null && (
              <FieldError message={fieldErrors.targetUrl!} testId="newtest-target-url-error" />
            )}
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
                  <input
                    className={`${inputCls} flex-1`}
                    value={h.name}
                    onChange={e => setHeader(i, { name: e.target.value })}
                    placeholder="X-Auth"
                    aria-label="header name"
                  />
                  <input
                    className={`${inputCls} flex-1`}
                    value={h.value}
                    onChange={e => setHeader(i, { value: e.target.value })}
                    placeholder="token"
                    aria-label="header value"
                  />
                  <Button variant="ghost" onClick={() => set({ headers: form.headers.filter((_, j) => j !== i) })}>
                    ✕
                  </Button>
                </div>
              ))}
            </div>
          </div>
          {warning && (
            <p
              className="rounded-md bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/30 dark:text-amber-200"
              role="alert"
            >
              {warning}
            </p>
          )}
          {/* Phase 88: fan-out targets. A checkbox per registered BYOC
              cluster; any checked makes the execution fan-out (the full
              shard set runs on every checked cluster). Disabled -- with the
              reason stated -- when no BYOC cluster is registered. */}
          <fieldset
            className="rounded-md border border-slate-200 p-3 dark:border-slate-700"
            data-testid="fanout-targets"
          >
            <legend className="text-caption px-1 text-slate-600 dark:text-slate-300">Run everywhere (fan-out)</legend>
            {clustersUnavailable ? (
              <p className="text-caption text-slate-400">
                Cluster registry unavailable — the test will run on the deployment&apos;s default cluster.
              </p>
            ) : clusters === null ? (
              <p className="text-caption text-slate-400">Loading clusters…</p>
            ) : byocClusters.length === 0 ? (
              <p className="text-caption text-slate-400" data-testid="fanout-targets-empty">
                No BYOC clusters registered — the test will run on the deployment&apos;s default cluster.
              </p>
            ) : (
              <>
                <p className="text-caption mb-2 text-slate-500 dark:text-slate-400">
                  Check any cluster to run this test&apos;s full load there simultaneously.
                </p>
                <div className="flex flex-wrap gap-3">
                  {byocClusters.map(c => (
                    <label
                      key={c.name}
                      className="flex items-center gap-1.5 text-sm text-slate-700 dark:text-slate-300"
                    >
                      <input
                        type="checkbox"
                        data-testid={`fanout-target-${c.name}`}
                        checked={fanOutTargets.has(c.name)}
                        onChange={() => toggleFanOut(c.name)}
                      />
                      {c.name}
                    </label>
                  ))}
                </div>
                {fanOutTargets.size > 0 && (
                  <p
                    className="text-caption mt-2 text-slate-500 dark:text-slate-400"
                    data-testid="fanout-targets-selected"
                  >
                    {fanOutTargets.size} target{fanOutTargets.size === 1 ? '' : 's'} selected — engines multiply per
                    target.
                  </p>
                )}
              </>
            )}
          </fieldset>
        </CardContent>
      </Card>
      <Card>
        <CardHeader className="flex flex-row items-center justify-between">
          <CardTitle>Load</CardTitle>
          <div
            role="group"
            aria-label="load configuration mode"
            className="inline-flex overflow-hidden rounded-lg border border-slate-300 dark:border-slate-700"
            data-testid="load-tab"
          >
            <button
              type="button"
              aria-pressed={loadTab === 'simple'}
              aria-label="switch to simple load"
              data-testid="load-tab-simple"
              onClick={() => setLoadTab('simple')}
              className={loadTabCls(loadTab === 'simple')}
            >
              Simple
            </button>
            <button
              type="button"
              aria-pressed={loadTab === 'advanced'}
              aria-label="switch to advanced load"
              data-testid="load-tab-advanced"
              onClick={() => setLoadTab('advanced')}
              className={loadTabCls(loadTab === 'advanced')}
            >
              Advanced
            </button>
          </div>
        </CardHeader>
        <CardContent>
          {loadTab === 'simple' ? (
            <ModeRowsForm value={modeRows} onChange={setModeRows} />
          ) : (
            <StageEditor
              state={stages}
              onStateChange={setStages}
              configName={`${form.name}-load`}
              onValidityChange={setStagesValid}
            />
          )}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>From template</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {templatesUnavailable ? (
            <p className="text-caption text-slate-400">Template catalog unavailable.</p>
          ) : templates === null ? (
            <p className="text-caption text-slate-400">Loading templates…</p>
          ) : templates.length === 0 ? (
            <p className="text-caption text-slate-400">No templates yet.</p>
          ) : (
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <label className="text-caption text-slate-600 dark:text-slate-300">
                Template
                <select
                  className={`${inputCls} mt-1 w-full`}
                  value={templateId}
                  onChange={e => setTemplateId(e.target.value)}
                  aria-label="template picker"
                  data-testid="template-picker"
                >
                  <option value="">Choose a template…</option>
                  {templates.map(t => (
                    <option key={t.id} value={t.id}>
                      {t.name}
                    </option>
                  ))}
                </select>
              </label>
              <label className="text-caption text-slate-600 dark:text-slate-300">
                New test name
                <input
                  className={`${inputCls} mt-1 w-full`}
                  value={templateName}
                  onChange={e => setTemplateName(e.target.value)}
                  placeholder="checkout-baseline"
                  aria-label="template test name"
                />
              </label>
              <label className="text-caption text-slate-600 dark:text-slate-300 sm:col-span-2">
                Target URL override (optional)
                <input
                  className={`${inputCls} mt-1 w-full`}
                  value={templateTarget}
                  onChange={e => setTemplateTarget(e.target.value)}
                  placeholder="https://httpbin.org (optional)"
                  aria-label="template target URL override"
                />
              </label>
            </div>
          )}
          <div className="flex items-center gap-3">
            {canCreateScenario ? (
              <Button
                onClick={createFromTemplate}
                disabled={templateBusy || !templateId}
                data-testid="create-from-template"
              >
                {templateBusy ? `Working — ${templateStep ?? '…'}` : 'Create from template'}
              </Button>
            ) : (
              <p className="text-sm text-slate-500 dark:text-slate-400" data-testid="no-template-permission">
                Your role cannot create scenarios.
              </p>
            )}
            {templateBusy && templateStep && (
              <span className="text-caption text-slate-500 dark:text-slate-400">step: {templateStep}</span>
            )}
          </div>
          {templateError && (
            <div>
              <p className="text-sm text-red-600 dark:text-red-400" role="alert">
                {templateError}
              </p>
              <ActionErrorDetails details={templateErrorDetail} />
            </div>
          )}
        </CardContent>
      </Card>
      <div className="flex items-center gap-3">
        {canCreate ? (
          <Button
            onClick={submit}
            disabled={busy || (loadTab === 'simple' ? !modeRowsValid(modeRows) : !stagesValid)}
            data-testid="create-test"
          >
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
