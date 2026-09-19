// The Simple-mode load form (phase 90): mode, target req/s, duration +
// unit -- nothing else. The server resolves concurrency/engines/ramp-up;
// this component never derives them, it only validates the three stated
// inputs (mirroring the server's own input rules) and surfaces soak's
// soft duration guidance. Fully controlled: the host owns the value the
// way it owns any other form field.
import { useEffect } from 'react';
import FieldError from './ui/FieldError';
import { LOAD_MODES, MODE_PURPOSE, soakTooShortWarning, validateModeForm, type ModeFormValue } from '../lib/modeConfig';

const inputCls =
  'rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm text-slate-900 dark:border-slate-600 dark:bg-slate-800 dark:text-slate-100';

export interface ModeFormProps {
  value: ModeFormValue;
  onChange: (next: ModeFormValue) => void;
  /** Fires whenever submittability changes (the StageEditor convention). */
  onValidityChange?: (valid: boolean) => void;
  /** Phase 94: locks every input (the Run panel does while a flow is busy). */
  disabled?: boolean;
  /** Phase 94: capacity hint line under the qps input ("profile: ~N qps/pod,
   *  M engines"), shown only when the profile is available. */
  qpsHint?: string;
}

export default function ModeForm({ value, onChange, onValidityChange, disabled = false, qpsHint }: ModeFormProps) {
  const errors = validateModeForm(value);
  const valid = Object.keys(errors).length === 0;
  const soakWarning = soakTooShortWarning(value);

  // Deps are [valid] on purpose: the host only cares about submittability
  // transitions, not every keystroke.
  useEffect(() => {
    onValidityChange?.(valid);
  }, [valid]);

  const set = (patch: Partial<ModeFormValue>) => onChange({ ...value, ...patch });

  return (
    <div className="space-y-4" data-testid="mode-form">
      <p className="text-caption text-slate-500 dark:text-slate-400">
        State the load; the server derives concurrency, engines, and ramp-up from this scenario&rsquo;s calibration.
        Saving needs a capacity profile — if it refuses with a 409, calibrate the scenario first.
      </p>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <label className="text-caption text-slate-600 dark:text-slate-300">
          Mode
          <select
            className={`${inputCls} mt-1 w-full`}
            aria-label="load mode"
            data-testid="mode-select"
            value={value.mode}
            disabled={disabled}
            onChange={e => set({ mode: e.target.value as ModeFormValue['mode'] })}
          >
            {LOAD_MODES.map(m => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
          <span className="mt-1 block text-slate-400">{MODE_PURPOSE[value.mode]}</span>
        </label>
        <label className="text-caption text-slate-600 dark:text-slate-300">
          Target rate (req/s)
          <input
            type="number"
            min={1}
            className={`${inputCls} mt-1 w-full`}
            aria-label="target requests per second"
            data-testid="mode-qps"
            value={Number.isFinite(value.qps) ? value.qps : ''}
            disabled={disabled}
            onChange={e => set({ qps: e.target.value === '' ? 0 : Number(e.target.value) })}
          />
          {errors.qps && <FieldError message={errors.qps} testId="mode-qps-error" />}
          {qpsHint && (
            <span className="mt-1 block text-slate-400" data-testid="mode-qps-hint">
              {qpsHint}
            </span>
          )}
        </label>
        <label className="text-caption text-slate-600 dark:text-slate-300">
          Duration
          <span className="mt-1 flex">
            <input
              type="number"
              min={1}
              className={`${inputCls} w-full rounded-r-none`}
              aria-label="duration"
              data-testid="mode-duration"
              value={Number.isFinite(value.duration) ? value.duration : ''}
              disabled={disabled}
              onChange={e => set({ duration: e.target.value === '' ? 0 : Number(e.target.value) })}
            />
            <select
              className={`${inputCls} rounded-l-none border-l-0`}
              aria-label="duration unit"
              data-testid="mode-duration-unit"
              value={value.unit}
              disabled={disabled}
              onChange={e => set({ unit: e.target.value as ModeFormValue['unit'] })}
            >
              <option value="m">minutes</option>
              <option value="h">hours</option>
            </select>
          </span>
          {errors.duration && <FieldError message={errors.duration} testId="mode-duration-error" />}
        </label>
      </div>
      {soakWarning && (
        <p
          className="rounded-md bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/30 dark:text-amber-200"
          role="status"
          data-testid="soak-warning"
        >
          {soakWarning}
        </p>
      )}
    </div>
  );
}
