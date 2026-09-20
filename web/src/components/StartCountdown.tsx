// Phase 93: the one-click Start flow's countdown (see useStartFlow). A
// 10-second wait is NOT a spinner (ui-ux-pro-max: loading feedback must
// match the expected wait) — this component renders an explicit ring plus
// the remaining seconds as text, announces progress to screen readers only
// at the scaled marks, and always offers Cancel: a one-way door needs an
// exit. owns its 1s ticker so it stays independently testable with fake
// timers; the parent (Execution.tsx / ScenarioRunPanel) mounts it for
// exactly the 'counting' step and unmounts on cancel/complete. Phase 96:
// the length is the operator's per-browser preference (0–60s) — the ring
// and the announce cadence scale with whatever seconds it is handed.
import { useEffect, useRef, useState } from 'react';
import Button from './ui/Button';

/** The countdown default (spec: stays 10; the preference overrides it). */
export const START_SECONDS = 10;

/** The announce cadence scaled to the length: the top, the half-way mark
 *  (floored — 3s's half is 1, which the final mark already covers), and
 *  the final second, deduped and clamped into 1..N. 10 → {10,5,1} (the
 *  phase-93 cadence, unchanged); 3 → {3,1}; 1 → {1}; 60 → {60,30,1}.
 *  Every tick would narrate a drum-roll of numbers; the marks say
 *  "halfway gone, one left" and nothing more. */
export function announceMarks(seconds: number): Set<number> {
  const marks = new Set([seconds, Math.floor(seconds / 2), 1]);
  for (const m of marks) {
    if (m < 1 || m > seconds) {
      marks.delete(m);
    }
  }
  return marks;
}

const message = (s: number) => `Load test starts in ${s}s`;

/** The ring's stroke geometry: a full circle of radius 16 in a 36px box. */
const RING_CIRCUMFERENCE = 2 * Math.PI * 16;

export interface StartCountdownProps {
  /** Total seconds; defaults to the spec's fixed 10. */
  seconds?: number;
  /** Fired once when the countdown reaches zero. Never after cancel. */
  onComplete: () => void;
  /** Fired when the operator cancels; the countdown stops dead. */
  onCancel: () => void;
}

export default function StartCountdown({ seconds = START_SECONDS, onComplete, onCancel }: StartCountdownProps) {
  // The cadence is computed from the handed length, not hardcoded —
  // phase 96's scaling requirement. seconds never changes mid-mount (the
  // hook captures it at begin(), before this component exists).
  const marks = announceMarks(seconds);
  const [remaining, setRemaining] = useState(seconds);
  // The live region is rendered with the mount-mark's text so the very
  // first announcement rides the initial paint instead of racing it;
  // later marks update the text in place, and between marks the text
  // simply does not change — which is exactly "announce only at marks".
  // For every N ≥ 1 the top N is itself a mark, so the first paint always
  // carries one.
  const [announced, setAnnounced] = useState(() => (marks.has(seconds) ? message(seconds) : ''));
  // Set once the countdown has ended (complete or cancel): gates the
  // one-shot callbacks and freezes the ticker.
  const finished = useRef(false);
  const timer = useRef<number | null>(null);

  const stopTimer = () => {
    if (timer.current !== null) {
      clearInterval(timer.current);
      timer.current = null;
    }
  };

  useEffect(() => {
    timer.current = window.setInterval(() => setRemaining(r => Math.max(0, r - 1)), 1_000);
    return stopTimer;
  }, []);

  useEffect(() => {
    if (marks.has(remaining)) {
      setAnnounced(message(remaining));
    }
    if (remaining <= 0 && !finished.current) {
      finished.current = true;
      stopTimer();
      onComplete();
    }
  }, [remaining, onComplete]);

  const cancel = () => {
    if (finished.current) {
      return;
    }
    finished.current = true;
    stopTimer();
    onCancel();
  };

  // Reduced motion (the phase-77 clamp already freezes CSS transitions
  // globally; here the choice is structural): no animated ring at all,
  // just the text ticking — which under the clamp is what would render
  // anyway, minus the decorative ring chrome.
  const reduced =
    typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  return (
    <span className="inline-flex items-center gap-3" data-testid="start-countdown">
      {!reduced && (
        <svg
          width="36"
          height="36"
          viewBox="0 0 36 36"
          aria-hidden="true"
          data-testid="start-countdown-ring"
          className="shrink-0 -rotate-90"
        >
          <circle cx="18" cy="18" r="16" fill="none" strokeWidth="4" className="stroke-slate-200 dark:stroke-slate-700" />
          <circle
            cx="18"
            cy="18"
            r="16"
            fill="none"
            strokeWidth="4"
            strokeLinecap="round"
            className="stroke-sky-500 transition-[stroke-dashoffset] duration-1000 ease-linear"
            strokeDasharray={RING_CIRCUMFERENCE}
            strokeDashoffset={RING_CIRCUMFERENCE * (1 - remaining / seconds)}
          />
        </svg>
      )}
      <span
        className="text-body-sm font-medium text-slate-700 dark:text-slate-200"
        data-testid="start-countdown-remaining"
      >
        {/* Zero is a single pre-unmount paint; say what is happening, not "0s". */}
        {remaining > 0 ? message(remaining) : 'Starting…'}
      </span>
      {/* Polite: the run IS starting; nothing here is worth interrupting. */}
      <span aria-live="polite" data-testid="start-countdown-announce" className="sr-only">
        {announced}
      </span>
      <Button
        size="sm"
        variant="outline"
        className="active:scale-95"
        data-testid="start-countdown-cancel"
        onClick={cancel}
      >
        Cancel
      </Button>
    </span>
  );
}
