// The create-calibration dialog (phase 39): closes the dead-end where the
// Capacity card's Calibrate button existed but NOTHING in the SPA could
// create the calibrate_engine execution it needs -- POST /api/calibrations
// had zero frontend consumers. Per scenario row on the execution page it
// collects a search spec (pod size, target-health criterion, optional
// bounds) and mints a fresh CalibrateEngine execution bound to that
// scenario (phase 41: the POST carries scenario_id + source_execution_id,
// so the created execution is runnable the moment it exists); the parent
// then navigates to it. Modal chrome follows ShareRunModal's overlay/
// tap-away/Escape conventions (the SPA still has no generic Modal).
import { useEffect, useState } from 'react';
import { X } from 'lucide-react';
import Button from './ui/Button';
import Input from './ui/Input';
import { ApiError } from '../api/client';
import { createCalibration } from '../api/calibration';

/**
 * One Taurus pass/fail expression, in the subject set Taurus engines
 * themselves support -- the same contract the backend's
 * calibration.ValidateCriterion enforces (phase 42's hotfix: prose like
 * "error_rate < 0.01 AND p95 < 500ms" used to sail through here and die at
 * run time with bzt's "Unsupported fail criteria subject: error_rate").
 */
const criterionExpression = /^(failures|p95|p50|avg-rt|concurrency)\s*(>|<|>=|<=)\s*[0-9.]+(ms|s|%|)?$/;

/** Splits the criterion field into expressions: comma or newline separated. */
function splitCriterion(raw: string): string[] {
  return raw
    .split(/[,\n]/)
    .map(part => part.trim())
    .filter(part => part !== '');
}

/** Text inputs for every field: numbers stay strings until submit, so an
 * empty optional field reads as "" (not "0") and is simply not sent. */
interface CalibrateForm {
  targetQps: string;
  cpu: string;
  memory: string;
  criterion: string;
  seedQps: string;
  maxQps: string;
  maxSteps: string;
  holdSeconds: string;
}

const DEFAULT_FORM: CalibrateForm = {
  targetQps: '',
  cpu: '500m',
  memory: '512Mi',
  // Taurus expressions, comma or newline separated -- the exact grammar
  // the backend validates (calibration.ValidateCriterion) and the config
  // card already teaches. Prose here is a run-time Config Error from bzt,
  // so the default teaches the valid shape (phase 42's hotfix).
  criterion: 'failures>10%, p95>500ms',
  seedQps: '',
  maxQps: '',
  maxSteps: '',
  holdSeconds: '',
};

export interface CalibrateScenarioModalProps {
  /** The scenario the calibration will measure; names the generated execution. */
  scenarioId: number;
  /** Display name for that scenario (the page derives it from the config). */
  scenarioName: string;
  projectId: number;
  /** The execution the dialog was launched from -- the source whose
   * load-profile entry for scenarioId the backend copies as the
   * calibration's starting entry (phase 41's binding). */
  sourceExecutionId: number;
  /** The execution's engine -- a calibration must name one (the backend
   * rejects an engineless creation), so the page only offers the action on
   * engine'd executions. */
  engine: string;
  /** Closes the dialog; the parent owns the open state. */
  onClose: () => void;
  /** Called with the created execution's id -- the parent navigates to it. */
  onCreated: (executionId: number) => void;
}

export default function CalibrateScenarioModal({
  scenarioId,
  scenarioName,
  projectId,
  sourceExecutionId,
  engine,
  onClose,
  onCreated,
}: CalibrateScenarioModalProps) {
  const [form, setForm] = useState<CalibrateForm>(DEFAULT_FORM);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const set =
    (field: keyof CalibrateForm) =>
    (e: React.ChangeEvent<HTMLInputElement>): void => {
      setForm(f => ({ ...f, [field]: e.target.value }));
    };

  // Escape closes, the share dialog's convention.
  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [onClose]);

  const submit = () => {
    // Target QPS is the operator's stated aggregate goal -- the number a
    // calibrated profile later turns into an engine count via the fan-out
    // lookup. It is required so the intent is explicit; the creation POST
    // itself carries only the search spec (criterion, pod size, bounds) --
    // the backend has no target_qps field to receive it.
    const target = Number(form.targetQps);
    if (form.targetQps.trim() === '' || !Number.isFinite(target) || target <= 0) {
      setError('Target QPS is required (a positive number).');
      return;
    }
    // The criterion reaches Taurus's passfail module verbatim: validate
    // here, before the POST, so the operator sees the invalid expression
    // immediately instead of a Config Error after the first trigger. The
    // server rejects it too -- this check is convenience, not the gate.
    const expressions = splitCriterion(form.criterion);
    const invalid = expressions.find(expr => !criterionExpression.test(expr));
    if (expressions.length === 0 || invalid !== undefined) {
      setError(
        invalid !== undefined
          ? `Criterion "${invalid}" is not a Taurus expression. Use e.g. failures>10%, p95>500ms (comma or newline separated).`
          : 'Criterion is required: use Taurus expressions like failures>10%, p95>500ms.'
      );
      return;
    }
    const optional = (v: string): number | undefined => {
      const t = v.trim();
      if (t === '') return undefined;
      const n = Number(t);
      return Number.isFinite(n) ? n : undefined;
    };
    const seedQps = optional(form.seedQps);
    const maxQps = optional(form.maxQps);
    const maxSteps = optional(form.maxSteps);
    const holdSeconds = optional(form.holdSeconds);
    if (
      (form.seedQps.trim() !== '' && seedQps === undefined) ||
      (form.maxQps.trim() !== '' && maxQps === undefined) ||
      (form.maxSteps.trim() !== '' && maxSteps === undefined) ||
      (form.holdSeconds.trim() !== '' && holdSeconds === undefined)
    ) {
      setError('Optional bounds must be numbers when filled in.');
      return;
    }

    setBusy(true);
    setError(null);
    createCalibration({
      projectId,
      scenarioId,
      sourceExecutionId,
      // Auto-generated per the phase 39 spec: "calibrate <scenario name>
      // <timestamp>" -- unique per attempt, legible in the executions list.
      name: `calibrate ${scenarioName} ${new Date().toISOString()}`,
      engine,
      criterion: form.criterion,
      cpu: form.cpu,
      memory: form.memory,
      seedQps,
      maxQps,
      maxSteps,
      holdSeconds,
    })
      .then(created => onCreated(created.execution_id))
      .catch((err: unknown) => {
        setError(err instanceof ApiError ? err.message : 'Failed to create calibration execution.');
      })
      .finally(() => setBusy(false));
  };

  return (
    // The overlay is the tap-away target; only a tap on the backdrop itself
    // (not the dialog) closes, matching the share dialog.
    <div
      data-testid="calibrate-overlay"
      className="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4"
      onMouseDown={e => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={`Calibrate scenario ${scenarioId}`}
        data-testid="calibrate-modal"
        className="max-h-[85vh] w-full max-w-xl overflow-y-auto rounded-xl bg-white p-6 shadow-2xl dark:bg-slate-900"
      >
        <div className="mb-4 flex items-start justify-between gap-4">
          <div>
            <h2 className="text-heading-md text-slate-900 dark:text-white">Calibrate scenario {scenarioId}</h2>
            <p className="text-caption mt-1 text-slate-500 dark:text-slate-400">
              Creates a fresh calibration execution bound to this scenario ({engine} engine) that searches its per-pod
              capacity.
            </p>
          </div>
          <button
            type="button"
            aria-label="Close calibrate dialog"
            onClick={onClose}
            className="rounded p-1 text-slate-500 transition-colors hover:bg-slate-100 focus:outline-none focus:ring-2 focus:ring-sky-500 dark:text-slate-400 dark:hover:bg-slate-800"
          >
            <X aria-hidden className="h-5 w-5" />
          </button>
        </div>

        {/* noValidate: the modal's own guard validates (target QPS is the
            one field without a sensible default), so the refusal is testable
            and renders inline rather than as a browser bubble. */}
        <form
          className="space-y-4"
          noValidate
          onSubmit={e => {
            e.preventDefault();
            submit();
          }}
        >
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Input
              label="Target QPS (req/s)"
              type="number"
              min={1}
              required
              placeholder="500"
              data-testid="calibrate-target-qps"
              value={form.targetQps}
              onChange={set('targetQps')}
            />
            <Input
              label="Criterion"
              type="text"
              placeholder="failures>10%, p95>500ms"
              data-testid="calibrate-criterion"
              value={form.criterion}
              onChange={set('criterion')}
            />
            <Input label="CPU" type="text" data-testid="calibrate-cpu" value={form.cpu} onChange={set('cpu')} />
            <Input
              label="Memory"
              type="text"
              data-testid="calibrate-memory"
              value={form.memory}
              onChange={set('memory')}
            />
          </div>

          <fieldset className="rounded-lg border border-slate-200 p-3 dark:border-slate-700">
            <legend className="text-caption px-1 font-medium text-slate-500 dark:text-slate-400">
              Search bounds (optional -- the backend defaults apply when left empty)
            </legend>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Input
                label="Seed QPS"
                type="number"
                min={0}
                data-testid="calibrate-seed-qps"
                value={form.seedQps}
                onChange={set('seedQps')}
              />
              <Input
                label="Max QPS"
                type="number"
                min={0}
                data-testid="calibrate-max-qps"
                value={form.maxQps}
                onChange={set('maxQps')}
              />
              <Input
                label="Max steps"
                type="number"
                min={0}
                data-testid="calibrate-max-steps"
                value={form.maxSteps}
                onChange={set('maxSteps')}
              />
              <Input
                label="Hold seconds"
                type="number"
                min={0}
                data-testid="calibrate-hold-seconds"
                value={form.holdSeconds}
                onChange={set('holdSeconds')}
              />
            </div>
          </fieldset>

          {error && (
            <p className="text-sm text-red-600 dark:text-red-400" role="alert">
              {error}
            </p>
          )}

          <div className="flex items-center justify-end gap-2">
            <Button type="button" variant="outline" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy} data-testid="calibrate-submit">
              {busy ? 'Creating…' : 'Create calibration'}
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
}
