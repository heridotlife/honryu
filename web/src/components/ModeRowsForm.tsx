// The multi-scenario Simple load form (phase 91): one row per scenario,
// each stating its own mode / target rate / duration with per-row
// validation and per-row soak guidance. The server resolves each row's
// concurrency/engines/ramp-up independently — nothing is shared or derived
// across rows, and no client-side derivation happens here. Rows carry an
// optional scenario name (blank derives one at submit); every row of the
// NewTest flow shares the Test definition card's request shape. Fully
// controlled: the host owns the value, the StageEditor convention.
import { useEffect } from 'react';
import Button from './ui/Button';
import FieldError from './ui/FieldError';
import {
  MODE_PURPOSE,
  LOAD_MODES,
  modeRowsValid,
  soakTooShortWarning,
  validateModeRows,
  type ModeRowValue,
} from '../lib/modeConfig';

const inputCls =
  'rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm text-slate-900 dark:border-slate-600 dark:bg-slate-800 dark:text-slate-100';

export interface ModeRowsFormProps {
  value: ModeRowValue[];
  onChange: (next: ModeRowValue[]) => void;
  /** Fires whenever submittability changes (the StageEditor convention). */
  onValidityChange?: (valid: boolean) => void;
}

export default function ModeRowsForm({ value, onChange, onValidityChange }: ModeRowsFormProps) {
  const errors = validateModeRows(value);
  const valid = modeRowsValid(value);

  // Deps are [valid] on purpose: the host only cares about submittability
  // transitions, not every keystroke.
  useEffect(() => {
    onValidityChange?.(valid);
  }, [valid]);

  const setRow = (i: number, patch: Partial<ModeRowValue>) =>
    onChange(value.map((row, j) => (j === i ? { ...row, ...patch } : row)));
  const addRow = () => onChange([...value, { mode: 'burst', qps: 100, duration: 10, unit: 'm', name: '' }]);
  const dropRow = (i: number) => onChange(value.filter((_, j) => j !== i));

  return (
    <div className="space-y-4" data-testid="mode-form">
      <p className="text-caption text-slate-500 dark:text-slate-400">
        One row per scenario: state the load for each; the server derives that row&rsquo;s concurrency, engines, and
        ramp-up from its calibration. A blank name derives one at submit. Saving needs a capacity profile per scenario —
        if it refuses with a 409, calibrate that scenario first.
      </p>
      {value.map((row, i) => {
        const rowErrors = errors[i];
        const soakWarning = soakTooShortWarning(row);
        return (
          <fieldset
            key={i}
            className="rounded-md border border-slate-200 p-3 dark:border-slate-700"
            data-testid={`mode-row-${i}`}
          >
            <legend className="text-caption px-1 text-slate-600 dark:text-slate-300">Scenario {i + 1}</legend>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-4">
              <label className="text-caption text-slate-600 dark:text-slate-300">
                Name (optional)
                <input
                  type="text"
                  className={`${inputCls} mt-1 w-full`}
                  aria-label={`scenario ${i + 1} name`}
                  data-testid={`mode-row-${i}-name`}
                  value={row.name}
                  placeholder={i === 0 ? 'defaults to the test name' : `defaults to …-${i + 1}`}
                  onChange={e => setRow(i, { name: e.target.value })}
                />
                {rowErrors.name && <FieldError message={rowErrors.name} testId={`mode-row-${i}-name-error`} />}
              </label>
              <label className="text-caption text-slate-600 dark:text-slate-300">
                Mode
                <select
                  className={`${inputCls} mt-1 w-full`}
                  aria-label={`scenario ${i + 1} load mode`}
                  data-testid={`mode-row-${i}-mode`}
                  value={row.mode}
                  onChange={e => setRow(i, { mode: e.target.value as ModeRowValue['mode'] })}
                >
                  {LOAD_MODES.map(m => (
                    <option key={m} value={m}>
                      {m}
                    </option>
                  ))}
                </select>
                <span className="mt-1 block text-slate-400">{MODE_PURPOSE[row.mode]}</span>
              </label>
              <label className="text-caption text-slate-600 dark:text-slate-300">
                Target rate (req/s)
                <input
                  type="number"
                  min={1}
                  className={`${inputCls} mt-1 w-full`}
                  aria-label={`scenario ${i + 1} target requests per second`}
                  data-testid={`mode-row-${i}-qps`}
                  value={Number.isFinite(row.qps) ? row.qps : ''}
                  onChange={e => setRow(i, { qps: e.target.value === '' ? 0 : Number(e.target.value) })}
                />
                {rowErrors.qps && <FieldError message={rowErrors.qps} testId={`mode-row-${i}-qps-error`} />}
              </label>
              <label className="text-caption text-slate-600 dark:text-slate-300">
                Duration
                <span className="mt-1 flex">
                  <input
                    type="number"
                    min={1}
                    className={`${inputCls} w-full rounded-r-none`}
                    aria-label={`scenario ${i + 1} duration`}
                    data-testid={`mode-row-${i}-duration`}
                    value={Number.isFinite(row.duration) ? row.duration : ''}
                    onChange={e => setRow(i, { duration: e.target.value === '' ? 0 : Number(e.target.value) })}
                  />
                  <select
                    className={`${inputCls} rounded-l-none border-l-0`}
                    aria-label={`scenario ${i + 1} duration unit`}
                    data-testid={`mode-row-${i}-duration-unit`}
                    value={row.unit}
                    onChange={e => setRow(i, { unit: e.target.value as ModeRowValue['unit'] })}
                  >
                    <option value="m">minutes</option>
                    <option value="h">hours</option>
                  </select>
                </span>
                {rowErrors.duration && (
                  <FieldError message={rowErrors.duration} testId={`mode-row-${i}-duration-error`} />
                )}
              </label>
            </div>
            <div className="mt-2 flex items-center justify-between">
              <Button
                variant="ghost"
                onClick={() => dropRow(i)}
                disabled={value.length === 1}
                aria-label={`remove scenario ${i + 1}`}
              >
                ✕ Remove
              </Button>
            </div>
            {soakWarning && (
              <p
                className="mt-2 rounded-md bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/30 dark:text-amber-200"
                role="status"
                data-testid={`soak-warning-${i}`}
              >
                {soakWarning}
              </p>
            )}
          </fieldset>
        );
      })}
      <Button variant="ghost" onClick={addRow} data-testid="mode-add-row" aria-label="add scenario">
        + Add scenario
      </Button>
    </div>
  );
}
