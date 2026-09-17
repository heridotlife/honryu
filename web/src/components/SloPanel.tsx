// The project's SLO panel (phase 68): the objectives an operator defined
// for this project, each graded over a selectable window by
// GET /api/projects/{id}/slos/{slo_id}/budget through the generated client.
// List + create + delete live here; the budget math lives on the backend,
// so this panel renders one fetch's verdicts and never recomputes them.
//
// UX laws honoured: compliance is icon+text, never colour alone; delete is
// a two-step inline confirm (no browser dialogs, WebhooksCard's pattern);
// loading and empty states are explicit; the create form's at-least-one-
// target rule fails at the field, with the reason next to it.
import { useCallback, useEffect, useRef, useState } from 'react';
import Button from './ui/Button';
import Card, { CardContent, CardHeader, CardTitle } from './ui/Card';
import ErrorSummary, { type ErrorSummaryEntry } from './ui/ErrorSummary';
import Input from './ui/Input';
import { useFieldValidation } from '../hooks/useFieldValidation';
import { ApiError } from '../api/client';
import {
  deleteProjectsByProjectIdSlosBySloId,
  getProjectsByProjectIdSlos,
  getProjectsByProjectIdSlosBySloIdBudget,
  postProjectsByProjectIdSlos,
  type Slo,
  type SloBudget,
  type SloMetric,
} from '../api/generated';

export interface SloPanelProps {
  /** The project whose objectives this panel administers. */
  projectId: number;
}

/** The windows the API serves, in display order. 7d is the default: long
 * enough to smooth one bad run, short enough to still be news. */
const WINDOWS = ['1d', '7d', '30d'] as const;
type Window = (typeof WINDOWS)[number];

/** Human labels for the wire's metric names. */
const METRIC_LABELS: Record<SloMetric['metric'], string> = {
  p95_ms: 'p95 latency',
  error_rate: 'error rate',
  success_ratio: 'success ratio',
};

/** Renders a remaining budget percentage the way the formula says it:
 * positive = margin left, 0 = on target, negative = burned past it. One
 * decimal -- the formula's fractions of a percent are precision the
 * operator cannot act on, and the % sign is the unit, always visible. */
function formatRemaining(pct: number): string {
  const rounded = Math.round(pct * 10) / 10;
  const sign = rounded > 0 ? '+' : '';
  return `${sign}${rounded}%`;
}

/** The target fields' validators (phase 77 blur + submit), stating the
 * same rules the API enforces (the domain's Validate): p95 must be ABOVE
 * zero -- 0 ms is not a latency target, it is a typo -- while error rate
 * and success ratio may legitimately be 0 (a zero error-rate target is
 * "no errors allowed"). An empty field stays "not tracked". */
function ratioError(value: string, label: string): string | null {
  if (value.trim() === '') return null;
  const n = Number(value);
  return Number.isNaN(n) || n < 0 || n > 1 ? `${label} target must be between 0 and 1` : null;
}

const TARGET_VALIDATORS = {
  p95: (v: string): string | null => (v.trim() === '' || Number(v) > 0 ? null : 'p95 target must be a number above 0'),
  errorRate: (v: string): string | null => ratioError(v, 'error rate'),
  successRatio: (v: string): string | null => ratioError(v, 'success ratio'),
};

/** The target inputs' element ids -- the error summary links to them. */
const TARGET_IDS = {
  p95: 'slo-p95-input',
  errorRate: 'slo-error-input',
  successRatio: 'slo-ratio-input',
} as const;

/** One metric's compliance badge: icon + text, never colour alone. */
function ComplianceBadge({ compliant }: { compliant: boolean }) {
  return compliant ? (
    <span
      className="inline-flex items-center gap-1 rounded-full bg-emerald-100 px-2 py-0.5 text-xs font-medium text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300"
      data-testid="slo-badge-compliant"
    >
      <svg aria-hidden="true" viewBox="0 0 20 20" fill="currentColor" className="h-3 w-3">
        <path fillRule="evenodd" d="M16.7 5.3a1 1 0 010 1.4l-7.5 7.5a1 1 0 01-1.4 0l-3.5-3.5a1 1 0 111.4-1.4l2.8 2.79 6.8-6.8a1 1 0 011.4 0z" clipRule="evenodd" />
      </svg>
      within target
    </span>
  ) : (
    <span
      className="inline-flex items-center gap-1 rounded-full bg-red-100 px-2 py-0.5 text-xs font-medium text-red-800 dark:bg-red-900/30 dark:text-red-300"
      data-testid="slo-badge-violated"
    >
      <svg aria-hidden="true" viewBox="0 0 20 20" fill="currentColor" className="h-3 w-3">
        <path fillRule="evenodd" d="M4.3 4.3a1 1 0 011.4 0L10 8.6l4.3-4.3a1 1 0 111.4 1.4L11.4 10l4.3 4.3a1 1 0 01-1.4 1.4L10 11.4l-4.3 4.3a1 1 0 01-1.4-1.4L8.6 10 4.3 5.7a1 1 0 010-1.4z" clipRule="evenodd" />
      </svg>
      over target
    </span>
  );
}

/** The budget-remaining bar: fill width is the remaining percentage clamped
 * to the 0..100 the bar can show; a burned budget shows an empty track and
 * lets the signed number say how far past. The % value is text beside the
 * bar, so the bar decorates rather than carries the data. */
function BudgetBar({ pct }: { pct: number }) {
  const clamped = Math.max(0, Math.min(pct, 100));
  const burned = pct < 0;
  return (
    <div className="flex items-center gap-2" data-testid="slo-budget-bar">
      <div
        className={`h-1.5 w-28 overflow-hidden rounded-full ${burned ? 'bg-red-200 dark:bg-red-900/40' : 'bg-slate-200 dark:bg-slate-700'}`}
        role="presentation"
      >
        <div
          className={`h-full rounded-full ${burned ? 'bg-red-500' : 'bg-emerald-500'}`}
          style={{ width: `${clamped}%` }}
        />
      </div>
      <span
        className={`text-caption font-medium ${burned ? 'text-red-700 dark:text-red-300' : 'text-slate-600 dark:text-slate-300'}`}
      >
        {formatRemaining(pct)} left
      </span>
    </div>
  );
}

export default function SloPanel({ projectId }: SloPanelProps) {
  const [slos, setSlos] = useState<Slo[] | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  const [budgets, setBudgets] = useState<Record<number, SloBudget>>({});
  // The budget fetch's shared phase: loading shows a skeleton row per SLO,
  // error shows an explicit failed row -- a failed fetch must never render
  // as eternal "loading", and a window switch must never leave the previous
  // window's numbers standing in for the new one's.
  const [budgetPhase, setBudgetPhase] = useState<'loading' | 'ready' | 'error'>('loading');
  const [window, setWindow] = useState<Window>('7d');
  // Last-write-wins guard: only the newest in-flight budget fetch may
  // settle state, so a fast window switch cannot let a slow stale response
  // overwrite the newer window's numbers.
  const budgetReq = useRef(0);

  // Create-form state. The three targets are optional individually; the
  // at-least-one rule is validated here so the error lands at the field.
  const [name, setName] = useState('');
  const [p95, setP95] = useState('');
  const [errorRate, setErrorRate] = useState('');
  const [successRatio, setSuccessRatio] = useState('');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmId, setConfirmId] = useState<number | null>(null);
  // Blur + submit validation (phase 77): each target's error shows once
  // the field is blurred; a failed submit moves focus to the summary.
  const fields = useFieldValidation();

  /** Every target field's current verdict (null while legal). */
  const targetErrors = {
    p95: TARGET_VALIDATORS.p95(p95),
    errorRate: TARGET_VALIDATORS.errorRate(errorRate),
    successRatio: TARGET_VALIDATORS.successRatio(successRatio),
  };
  /** The validation failures, in field order, plus the at-least-one rule
   * (linked to the first target field) when every field is legal but
   * empty. Pure over the current values; visibility is decided by the
   * hook's submitted flag at render time. */
  const validationEntries = (): ErrorSummaryEntry[] => {
    const fieldEntries = (Object.keys(TARGET_IDS) as Array<keyof typeof TARGET_IDS>)
      .filter((f) => targetErrors[f] !== null)
      .map((f) => ({ fieldId: TARGET_IDS[f], message: targetErrors[f]! }));
    if (fieldEntries.length === 0 && p95.trim() === '' && errorRate.trim() === '' && successRatio.trim() === '') {
      return [{ fieldId: TARGET_IDS.p95, message: 'set at least one target' }];
    }
    return fieldEntries;
  };

  // The armed delete confirm disarms on Escape or on a press outside its
  // row (the shared Modal's tap-away convention, inline): a half-armed
  // destructive action must not linger waiting for a stray second click.
  useEffect(() => {
    if (confirmId === null) return;
    const disarm = (): void => setConfirmId(null);
    const handleKeyDown = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') disarm();
    };
    const handleMouseDown = (event: MouseEvent): void => {
      const row = document.querySelector(`[data-testid="slo-row-${confirmId}"]`);
      if (row !== null && event.target instanceof Node && !row.contains(event.target)) disarm();
    };
    document.addEventListener('keydown', handleKeyDown);
    document.addEventListener('mousedown', handleMouseDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
      document.removeEventListener('mousedown', handleMouseDown);
    };
  }, [confirmId]);

  const loadBudgets = useCallback(
    (ids: number[], win: Window): void => {
      const seq = ++budgetReq.current;
      setBudgetPhase('loading');
      setBudgets({});
      void Promise.all(
        ids.map(id =>
          getProjectsByProjectIdSlosBySloIdBudget(projectId, id, { query: { window: win } })
            .then(b => [id, b] as const)
        )
      )
        .then(pairs => {
          if (seq !== budgetReq.current) return; // a newer fetch superseded this one
          setBudgets(Object.fromEntries(pairs));
          setBudgetPhase('ready');
        })
        .catch(() => {
          if (seq !== budgetReq.current) return;
          // A failed budget read renders an explicit failed row, never a
          // faked verdict and never an eternal spinner; the list itself
          // still shows.
          setBudgets({});
          setBudgetPhase('error');
        });
    },
    [projectId]
  );

  useEffect(() => {
    let alive = true;
    setSlos(null);
    setBudgets({});
    setListError(null);
    getProjectsByProjectIdSlos(projectId)
      .then(rows => {
        if (!alive) return;
        setSlos(rows);
        loadBudgets(
          rows.map(r => r.id),
          '7d'
        );
        setWindow('7d');
      })
      .catch((err: unknown) => {
        if (alive) setListError(err instanceof ApiError ? err.message : 'failed to load SLOs');
      });
    return () => {
      alive = false;
    };
  }, [projectId, loadBudgets]);

  function pickWindow(win: Window): void {
    if (win === window && budgetPhase !== 'error') {
      // Same window re-picked while its numbers stand: they are that
      // window's numbers already; refetching would only flicker the
      // skeletons. After a failed fetch, though, the re-pick IS the retry.
      return;
    }
    setWindow(win);
    if (slos !== null) {
      loadBudgets(
        slos.map(s => s.id),
        win
      );
    }
  }

  async function create(e: React.FormEvent): Promise<void> {
    e.preventDefault();
    fields.markSubmitted();
    // The per-field guards fail at the field (blur already showed them);
    // the summary renders at the form's top and takes focus instead of a
    // single combined message. The rules themselves are unchanged.
    if (validationEntries().length > 0) {
      return;
    }
    setFormError(null);
    const targets: Record<string, number> = {};
    // Parsed-when-present: an empty field stays "not tracked".
    if (p95.trim() !== '') targets.target_p95_ms = Number(p95);
    if (errorRate.trim() !== '') targets.target_error_rate = Number(errorRate);
    if (successRatio.trim() !== '') targets.target_success_ratio = Number(successRatio);
    setBusy(true);
    try {
      const created = await postProjectsByProjectIdSlos(projectId, { name: name.trim(), ...targets });
      setSlos(prev => [...(prev ?? []), created]);
      setName('');
      setP95('');
      setErrorRate('');
      setSuccessRatio('');
      fields.reset();
      loadBudgets([created.id], window);
    } catch (err: unknown) {
      setFormError(err instanceof ApiError ? err.message : 'failed to create SLO');
    } finally {
      setBusy(false);
    }
  }

  async function remove(id: number): Promise<void> {
    setListError(null);
    try {
      await deleteProjectsByProjectIdSlosBySloId(projectId, id);
      setSlos(prev => (prev ?? []).filter(s => s.id !== id));
      setBudgets(prev => {
        const next = { ...prev };
        delete next[id];
        return next;
      });
    } catch (err: unknown) {
      setListError(err instanceof ApiError ? err.message : 'failed to delete SLO');
    } finally {
      setConfirmId(null);
    }
  }

  return (
    <Card padding="none" data-testid="slo-panel">
      <CardHeader className="px-4 pt-4 sm:px-6 sm:pt-6">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle>Service-level objectives</CardTitle>
          {/* Window selector: the three spans the API serves. The active
              window is marked by pressed state and text weight, not colour
              alone. */}
          <div role="group" aria-label="Budget window" className="flex items-center gap-1" data-testid="slo-window-selector">
            {WINDOWS.map(w => (
              <button
                key={w}
                type="button"
                aria-pressed={window === w}
                data-testid={`slo-window-${w}`}
                onClick={() => pickWindow(w)}
                className={`rounded-full px-2.5 py-0.5 text-xs font-medium ${
                  window === w
                    ? 'bg-sky-600 text-white'
                    : 'bg-slate-100 text-slate-600 hover:bg-slate-200 dark:bg-slate-700 dark:text-slate-300 dark:hover:bg-slate-600'
                }`}
              >
                {w}
              </button>
            ))}
          </div>
        </div>
        <p className="text-caption mt-1 text-slate-500 dark:text-slate-400">
          Targets the recent runs are graded against. Budget remaining: positive = margin, negative = burned.
        </p>
      </CardHeader>
      <CardContent>
        {listError && (
          <p className="text-body-sm px-4 pb-2 text-red-600 dark:text-red-400 sm:px-6" role="alert">
            {listError}
          </p>
        )}
        {slos === null ? (
          <p className="text-body-sm p-4 text-slate-500 dark:text-slate-400 sm:px-6" data-testid="slo-loading">
            Loading SLOs…
          </p>
        ) : slos.length === 0 ? (
          <p className="text-body-sm p-4 text-slate-500 dark:text-slate-400 sm:px-6" data-testid="slo-empty">
            No SLOs defined. Add one below to start grading this project&rsquo;s runs.
          </p>
        ) : (
          <ul className="divide-y divide-slate-200 dark:divide-slate-700">
            {slos.map(slo => {
              const budget = budgets[slo.id];
              return (
                <li key={slo.id} data-testid={`slo-row-${slo.id}`} className="p-4 sm:px-6">
                  <div className="flex items-center justify-between gap-3">
                    <p className="text-body-sm font-medium text-slate-900 dark:text-slate-100">{slo.name}</p>
                    <Button
                      size="sm"
                      variant={confirmId === slo.id ? 'primary' : 'outline'}
                      data-testid={`slo-delete-${slo.id}`}
                      onClick={() => {
                        if (confirmId === slo.id) {
                          void remove(slo.id);
                        } else {
                          setConfirmId(slo.id);
                        }
                      }}
                    >
                      {confirmId === slo.id ? `Delete “${slo.name}”?` : 'Delete'}
                    </Button>
                  </div>
                  {budget === undefined ? (
                    budgetPhase === 'error' ? (
                      <p
                        className="text-caption mt-1 text-red-600 dark:text-red-400"
                        data-testid={`slo-budget-error-${slo.id}`}
                      >
                        Budget failed to load. Pick a window to retry.
                      </p>
                    ) : (
                      <div className="mt-2 space-y-1.5" data-testid={`slo-budget-loading-${slo.id}`}>
                        <div className="h-2.5 w-40 animate-pulse rounded bg-slate-200 dark:bg-slate-700" />
                        <div className="h-2.5 w-64 animate-pulse rounded bg-slate-200 dark:bg-slate-700" />
                      </div>
                    )
                  ) : (
                    <div className="mt-2 space-y-2">
                      <p className="text-caption text-slate-500 dark:text-slate-400">
                        {budget.run_count === 0
                          ? `no eligible runs in the last ${budget.window}`
                          : `${budget.run_count} run${budget.run_count === 1 ? '' : 's'} in the last ${budget.window}`}
                      </p>
                      <div className="flex flex-wrap items-center gap-2" data-testid={`slo-metrics-${slo.id}`}>
                        {budget.metrics.map(m => (
                          <span key={m.metric} className="inline-flex items-center gap-1.5">
                            <ComplianceBadge compliant={m.compliant} />
                            <span className="text-caption text-slate-500 dark:text-slate-400">
                              {METRIC_LABELS[m.metric]}
                            </span>
                          </span>
                        ))}
                      </div>
                      <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
                        {budget.metrics
                          .filter(m => m.budget_remaining_pct !== null && m.budget_remaining_pct !== undefined)
                          .map(m => (
                            <div key={m.metric} className="flex items-center gap-2">
                              <span className="text-caption text-slate-500 dark:text-slate-400">{METRIC_LABELS[m.metric]}:</span>
                              <BudgetBar pct={m.budget_remaining_pct as number} />
                            </div>
                          ))}
                      </div>
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
        )}
        <form
          onSubmit={e => void create(e)}
          className="flex flex-col gap-2 border-t border-slate-200 p-4 dark:border-slate-700 sm:px-6"
          data-testid="slo-create-form"
        >
          {/* Submit-failure summary (the old slo-form-error line, now
              focusable and field-linked); also carries API failures. */}
          <ErrorSummary
            entries={[
              ...(fields.submitted ? validationEntries() : []),
              ...(formError !== null ? [{ message: formError }] : []),
            ]}
            testId="slo-form-error"
          />
          <div className="flex flex-col gap-2 sm:flex-row sm:items-start">
            <div className="flex-1">
              <Input
                id="slo-name-input"
                data-testid="slo-name-input"
                type="text"
                placeholder="Name (e.g. checkout p95)"
                value={name}
                onChange={e => setName(e.target.value)}
                aria-label="SLO name"
              />
            </div>
            <div className="flex-1">
              <Input
                id={TARGET_IDS.p95}
                data-testid="slo-p95-input"
                type="number"
                step="any"
                min="0"
                placeholder="p95 target (ms)"
                value={p95}
                onChange={e => setP95(e.target.value)}
                onBlur={() => fields.blur('p95')}
                error={fields.visibleError('p95', targetErrors.p95) ?? undefined}
                aria-invalid={fields.visibleError('p95', targetErrors.p95) !== null}
                aria-label="p95 target in milliseconds"
              />
            </div>
            <div className="flex-1">
              <Input
                id={TARGET_IDS.errorRate}
                data-testid="slo-error-input"
                type="number"
                step="any"
                min="0"
                max="1"
                placeholder="Error rate target (0-1)"
                value={errorRate}
                onChange={e => setErrorRate(e.target.value)}
                onBlur={() => fields.blur('errorRate')}
                error={fields.visibleError('errorRate', targetErrors.errorRate) ?? undefined}
                aria-invalid={fields.visibleError('errorRate', targetErrors.errorRate) !== null}
                aria-label="Error rate target"
              />
            </div>
            <div className="flex-1">
              <Input
                id={TARGET_IDS.successRatio}
                data-testid="slo-ratio-input"
                type="number"
                step="any"
                min="0"
                max="1"
                placeholder="Success ratio target (0-1)"
                value={successRatio}
                onChange={e => setSuccessRatio(e.target.value)}
                onBlur={() => fields.blur('successRatio')}
                error={fields.visibleError('successRatio', targetErrors.successRatio) ?? undefined}
                aria-invalid={fields.visibleError('successRatio', targetErrors.successRatio) !== null}
                aria-label="Success ratio target"
              />
            </div>
            <Button type="submit" data-testid="slo-add-btn" disabled={busy || name.trim() === ''}>
              Add
            </Button>
          </div>
          {/* The targets' zero rules, stated as the API enforces them (the
              domain's Validate): 0 ms is not a p95 target, but a zero error
              rate is a legal one -- "no errors allowed". */}
          <p className="text-caption text-slate-500 dark:text-slate-400" data-testid="slo-targets-note">
            p95 target must be above 0&thinsp;ms. Error rate and success ratio may be 0–1; 0 is a legal
            target (no errors allowed). Leave a field empty to not track it.
          </p>
        </form>
      </CardContent>
    </Card>
  );
}
