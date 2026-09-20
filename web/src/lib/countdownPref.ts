// Phase 96: the launch countdown as a per-browser preference, persisted
// in localStorage — deliberately NOT a server setting. The countdown is a
// client-side orchestration nicety (the server's trigger handler waits
// out engine readiness independently); different operators on one install
// may want different values, so the preference travels with the browser,
// the way the project switcher's selection does ("honryu.project").
//
// Validation is total on the read side: anything absent or not a bare
// integer in 0–60 falls back to the default, so a corrupt or hand-edited
// value can never produce a negative or fractional countdown. The write
// side refuses invalid values outright (no write at all) — callers that
// persist every keystroke can never leave garbage behind.
//
// Reactivity does not live here (this module is plain read/write, the
// lib/ convention); hooks/useCountdownPref.ts owns the storage-event and
// same-tab sync on top of these primitives.

/** localStorage key holding the preferred countdown length in seconds. */
export const COUNTDOWN_STORAGE_KEY = 'honryu.startCountdown';

/** The default countdown: 10s, the phase-93 v1 value (spec: stays 10). */
export const DEFAULT_COUNTDOWN_SECONDS = 10;

/** Bounds of the preference. 0 means "no countdown, launch immediately". */
export const COUNTDOWN_MIN_SECONDS = 0;
export const COUNTDOWN_MAX_SECONDS = 60;

/** Whether n is a storable countdown: a bare integer 0–60. */
export function isValidCountdown(n: number): boolean {
  return Number.isInteger(n) && n >= COUNTDOWN_MIN_SECONDS && n <= COUNTDOWN_MAX_SECONDS;
}

/**
 * The stored preference, or the default. Accepts only bare non-negative
 * integer text (so "", "-1", "2.5", "1e2", "abc" all fall back) whose
 * value lies in 0–60.
 */
export function getCountdownSeconds(): number {
  const raw = localStorage.getItem(COUNTDOWN_STORAGE_KEY);
  if (raw === null) {
    return DEFAULT_COUNTDOWN_SECONDS;
  }
  const text = raw.trim();
  if (!/^\d+$/.test(text)) {
    return DEFAULT_COUNTDOWN_SECONDS;
  }
  const n = Number(text);
  return isValidCountdown(n) ? n : DEFAULT_COUNTDOWN_SECONDS;
}

/**
 * Persist a preference. Invalid values are refused with no write — the
 * previous stored value (if any) survives untouched.
 */
export function setCountdownSeconds(n: number): void {
  if (isValidCountdown(n)) {
    localStorage.setItem(COUNTDOWN_STORAGE_KEY, String(n));
  }
}
