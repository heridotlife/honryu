// Phase 96: the launch countdown's control surface. Two exports, both
// mounted wherever a launch affordance lives (the Execution hub's
// lifecycle controls and the scenario Run panel — the same tree the
// StartCountdown mounts in):
//
// - <CountdownSettings />: the gear button plus its popover. Present BOTH
//   pre-start (next to Start, so the value is discoverable before the
//   first launch) and during the countdown (the operator can retune for
//   the NEXT launch while the current one ticks). The input is saved on
//   change — valid values persist per keystroke; an invalid draft never
//   reaches localStorage and reverts on blur.
// - <CountdownChip />: the persisted value echoed as "5s" next to Start
//   when it differs from the default, so the active setting is visible
//   without opening the popover. Hidden at the default (10s): a control
//   that always shows says nothing.
//
// Both are self-sufficient (each mounts useCountdownPref; localStorage
// plus the sync events keep them one source of truth) — the host pages
// add them beside their buttons and wire nothing.
import { useEffect, useState } from 'react';
import { Settings } from 'lucide-react';
import { DEFAULT_COUNTDOWN_SECONDS, isValidCountdown } from '../lib/countdownPref';
import { useCountdownPref } from '../hooks/useCountdownPref';

/** The persisted countdown echoed beside Start — only when ≠ default. */
export function CountdownChip() {
  const [seconds] = useCountdownPref();
  if (seconds === DEFAULT_COUNTDOWN_SECONDS) {
    return null;
  }
  return (
    <span
      data-testid="countdown-chip"
      title="Launch countdown before start (0 = immediate)"
      className="inline-flex items-center rounded-full bg-slate-100 px-2 py-0.5 text-xs font-medium text-slate-600 dark:bg-slate-700/50 dark:text-slate-300"
    >
      {seconds}s
    </span>
  );
}

/**
 * The gear + popover. Open/close is ProjectSwitcher's pattern: toggle on
 * click, tap-away and Escape close, listeners alive only while open, and
 * the root's marker class exempts the toggle from its own outside-click
 * check.
 */
export default function CountdownSettings() {
  const [open, setOpen] = useState(false);
  const [seconds, setSeconds] = useCountdownPref();
  // null = mirror the stored preference (external changes included, via
  // the hook's sync); a string = the operator is mid-keystroke and the
  // field shows their draft instead.
  const [draft, setDraft] = useState<string | null>(null);

  useEffect(() => {
    if (!open) {
      return;
    }
    const handleClickOutside = (event: MouseEvent) => {
      const target = event.target as Element | null;
      if (!target?.closest('.countdown-settings')) {
        setOpen(false);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('mousedown', handleClickOutside);
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [open]);

  const shown = draft ?? String(seconds);

  /** Saved on change: a parseable, in-range value persists immediately;
   *  anything else only updates the visible draft (localStorage — and
   *  therefore every consumer and every tab — is never touched by an
   *  invalid draft). */
  const onChange = (raw: string) => {
    setDraft(raw);
    const n = Number(raw);
    if (raw.trim() !== '' && isValidCountdown(n)) {
      setSeconds(n);
    }
  };

  /** Blur and Enter both end the edit: the field goes back to mirroring
   *  the persisted value — which is exactly revert-on-invalid. */
  const commit = () => setDraft(null);

  return (
    <div className="countdown-settings relative inline-flex" data-testid="countdown-settings">
      <button
        type="button"
        onClick={() => {
          setOpen(o => !o);
          setDraft(null);
        }}
        aria-label="Countdown settings"
        aria-haspopup="dialog"
        aria-expanded={open}
        title="Countdown settings"
        data-testid="countdown-settings-button"
        className="inline-flex min-h-[32px] min-w-[32px] items-center justify-center rounded-md text-slate-500 transition-colors hover:bg-slate-100 hover:text-sky-600 focus:outline-none focus:ring-2 focus:ring-sky-500 active:scale-95 dark:text-slate-400 dark:hover:bg-slate-800 dark:hover:text-sky-400"
      >
        <Settings aria-hidden className="h-4 w-4" />
      </button>
      {open && (
        <div
          role="dialog"
          aria-label="Countdown settings"
          data-testid="countdown-settings-popover"
          className="absolute left-0 top-full z-50 mt-2 w-56 rounded-lg border border-slate-200 bg-white p-3 shadow-lg dark:border-slate-800 dark:bg-slate-950"
        >
          <label
            htmlFor="countdown-seconds-input"
            className="text-caption block font-medium text-slate-600 dark:text-slate-300"
          >
            Launch countdown (s)
          </label>
          <input
            id="countdown-seconds-input"
            type="number"
            inputMode="numeric"
            min={0}
            max={60}
            step={1}
            value={shown}
            data-testid="countdown-seconds-input"
            aria-describedby="countdown-seconds-help"
            onChange={e => onChange(e.target.value)}
            onBlur={commit}
            onKeyDown={e => {
              if (e.key === 'Enter') {
                commit();
              }
            }}
            className="mt-1 w-full rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm text-slate-900 focus:outline-none focus:ring-2 focus:ring-sky-500 dark:border-slate-600 dark:bg-slate-800 dark:text-slate-100"
          />
          <p
            id="countdown-seconds-help"
            className="text-caption mt-1 text-slate-500 dark:text-slate-400"
          >
            0 starts immediately
          </p>
        </div>
      )}
    </div>
  );
}
