// Phase 90's simple-mode pure model: the ModeForm's value shape, its
// validation, the wire test it produces, and the display strings the
// Execution page's mode summary renders. No client-side derivation of
// concurrency/engines/ramp-up ever happens here -- the server is the only
// resolver; these helpers only shape what the operator stated and explain
// what came back.

/** The four simplified modes, mirroring internal/domain/loadmode. */
export type LoadMode = 'burst' | 'ramp' | 'soak' | 'staircase';

export const LOAD_MODES: LoadMode[] = ['burst', 'ramp', 'soak', 'staircase'];

/** Each mode's one-line statement of what it is for (select helper copy). */
export const MODE_PURPOSE: Record<LoadMode, string> = {
  burst: 'full rate from the first second — cold start is the subject',
  ramp: 'rise over a fifth of the window (60–600s) and observe',
  soak: 'a steady rate for a long window — leak and degradation hunting',
  staircase: 'rise in plateaus to the ceiling — find-the-ceiling capacity probe',
};

/** Duration units the form offers; seconds is the wire's currency. */
export type DurationUnit = 'm' | 'h';

/** The Simple form's whole value. */
export interface ModeFormValue {
  mode: LoadMode;
  /** Target req/s. Must be > 0: a mode entry is rate-defined. */
  qps: number;
  /** The number in the duration box, in unit. */
  duration: number;
  unit: DurationUnit;
  /** Staircase only (phase 98): the step count, 2–10. Ignored by the
   * other modes (never emitted on their wire entries); the server
   * defaults an unstated 0 to 5, but the form always states one. */
  steps: number;
}

export const initialModeForm: ModeFormValue = {
  mode: 'burst',
  qps: 100,
  duration: 10,
  unit: 'm',
  steps: 5,
};

/** The form's duration in seconds (the wire's unit). */
export function durationSeconds(v: ModeFormValue): number {
  return v.unit === 'h' ? v.duration * 3600 : v.duration * 60;
}

/** Seconds -> {duration, unit} for prefilling a form from a stored
 *  entry: whole hours render as hours, everything else minutes. The
 *  inverse of durationSeconds, for display round-trips only. */
export function secondsToModeForm(seconds: number): Pick<ModeFormValue, 'duration' | 'unit'> {
  if (seconds >= 3600 && seconds % 3600 === 0) {
    return { duration: seconds / 3600, unit: 'h' };
  }
  return { duration: Math.round(seconds / 60), unit: 'm' };
}

/**
 * Client-side validation mirroring the server's input rules (qps > 0,
 * duration > 0; staircase: steps 2–10 and a per-step hold of at least
 * 60s); the resolved fields' rules (engines, concurrency) are the
 * server's to enforce, never guessed here. Collects all offenders for
 * inline display.
 */
export interface ModeFormErrors {
  qps?: string;
  duration?: string;
  steps?: string;
}

export function validateModeForm(v: ModeFormValue): ModeFormErrors {
  const e: ModeFormErrors = {};
  if (!(v.qps > 0)) {
    e.qps = 'target req/s must be positive';
  }
  if (!(v.duration > 0)) {
    e.duration = 'duration must be positive';
  }
  if (v.mode === 'staircase') {
    // The server's own bounds (loadmode 2–10); the duration is the
    // PER-STEP hold, so the 60s floor reads as minutes here: a 5×60s
    // staircase is the honest minimum shape.
    if (!Number.isInteger(v.steps) || v.steps < 2 || v.steps > 10) {
      e.steps = 'steps must be between 2 and 10';
    }
    if (v.duration > 0 && durationSeconds(v) < 60) {
      e.duration = 'each step needs a hold of at least 1m';
    }
  }
  return e;
}

export function modeFormValid(v: ModeFormValue): boolean {
  return Object.keys(validateModeForm(v)).length === 0;
}

/**
 * Soak's soft guidance (non-blocking): the mode means hours-long steady
 * state; under half an hour is probably not what a soak is for. Returns
 * null for other modes or long-enough soaks.
 */
export function soakTooShortWarning(v: ModeFormValue): string | null {
  if (v.mode !== 'soak') {
    return null;
  }
  if (durationSeconds(v) < 30 * 60) {
    return 'soaks usually run an hour or more — under 30 minutes reads as a short steady test, not a soak';
  }
  return null;
}

/**
 * The tests[] entry a Simple submit PUTs: mode + the target rate + the
 * duration, nothing else -- concurrency/engines/ramp-up are the server's
 * to derive. The shape is StageTestJSON so the existing config plumbing
 * carries it; mode sits last, matching Go's marshal order, and staircase
 * appends its step count after that (the same last-position contract).
 * Non-staircase entries never carry steps: the server refuses the field
 * anywhere else.
 */
export function buildModeTest(
  name: string,
  scenarioId: number,
  v: ModeFormValue
): {
  name: string;
  scenario_id: number;
  concurrency: number;
  rampup: number;
  engines: number;
  throughput: number;
  duration: number;
  mode: LoadMode;
  steps?: number;
} {
  const t: {
    name: string;
    scenario_id: number;
    concurrency: number;
    rampup: number;
    engines: number;
    throughput: number;
    duration: number;
    mode: LoadMode;
    steps?: number;
  } = {
    name,
    scenario_id: scenarioId,
    concurrency: 0,
    rampup: 0,
    engines: 0,
    throughput: v.qps,
    duration: durationSeconds(v),
    mode: v.mode,
  };
  if (v.mode === 'staircase') {
    t.steps = v.steps;
  }
  return t;
}

// --- Multi-scenario Simple rows (phase 91) -------------------------------
// A Simple Load card states one row per scenario: each row is its own
// mode entry with its own derivation -- no cross-scenario sharing, ever.

/** One Simple Load-card row: a scenario's name plus its mode statement. */
export interface ModeRowValue extends ModeFormValue {
  /** The row's scenario name. Blank is legal: the flow derives one at
   * submit (the test name for the first row, name-2, name-3, ...). */
  name: string;
}

/** The Load card's initial shape: exactly the phase-90 single row. */
export const initialModeRows: ModeRowValue[] = [{ ...initialModeForm, name: '' }];

/** A row's errors: the mode statement's own two rules plus the row's
 * name (duplicates within the card only -- the server owns the rest). */
export interface ModeRowErrors extends ModeFormErrors {
  name?: string;
}

/**
 * One row's validation: qps > 0 and duration > 0 (the server's own input
 * rules, stated per row), plus a duplicate-name check against the card's
 * other rows -- two rows naming the same scenario would create two
 * scenarios the operator cannot tell apart. Blank names are legal and
 * derived at submit, so they never error here.
 */
export function validateModeRow(row: ModeRowValue, allRows: ModeRowValue[]): ModeRowErrors {
  const e: ModeRowErrors = validateModeForm(row);
  const trimmed = row.name.trim();
  if (trimmed !== '' && allRows.some(other => other !== row && other.name.trim() === trimmed)) {
    e.name = `another row already names this scenario`;
  }
  return e;
}

/** Every row's errors, in row order. */
export function validateModeRows(rows: ModeRowValue[]): ModeRowErrors[] {
  return rows.map(r => validateModeRow(r, rows));
}

/** The submit guard: valid when every row is. */
export function modeRowsValid(rows: ModeRowValue[]): boolean {
  return validateModeRows(rows).every(e => Object.keys(e).length === 0);
}

/**
 * The scenario name a row submits under: its own trimmed name, else a
 * derived one -- the test name for the first row (exactly what the
 * single-scenario flow always created), name-2, name-3, ... for the
 * rest, so a never-named multi-row card still yields distinct scenarios.
 */
export function scenarioName(row: ModeRowValue, index: number, testName: string): string {
  const trimmed = row.name.trim();
  if (trimmed !== '') {
    return trimmed;
  }
  return index === 0 ? testName : `${testName}-${index + 1}`;
}

/**
 * The tests[] array a multi-scenario Simple submit PUTs: one mode-shaped
 * entry per row, each bound to its own scenario, in row order. Each row
 * resolves independently server-side; nothing is shared or derived
 * across rows.
 */
export function buildModeTests(
  testName: string,
  scenarioIds: number[],
  rows: ModeRowValue[]
): Array<ReturnType<typeof buildModeTest>> {
  return rows.map((row, i) => buildModeTest(scenarioName(row, i, testName), scenarioIds[i], row));
}

/** Whole-second compact duration: 600 -> "10m", 5400 -> "1h 30m", 45 -> "45s". */
export function formatModeDuration(seconds: number): string {
  if (seconds <= 0) {
    return `${seconds}s`;
  }
  const h = Math.floor(seconds / 3600);
  const m = Math.round((seconds % 3600) / 60);
  if (h === 0 && seconds % 60 !== 0 && seconds < 60) {
    return `${seconds}s`;
  }
  if (h === 0) {
    return `${m}m`;
  }
  if (m === 0) {
    return `${h}h`;
  }
  return `${h}h ${m}m`;
}

/** The entry fields the chip and derivation note read -- structural so the
 * wire types (StageTestJSON, ConfigTest, LoadProfileEntry) all satisfy it. */
export interface ModeEntryView {
  mode?: string;
  throughput?: number;
  duration: number;
  concurrency: number;
  engines: number;
  rampup: number;
  /** Staircase entries only (phase 98): the step count. */
  steps?: number;
}

/** The mode chip's compact statement: "burst · 500 rps · 10m". */
export function modeChipLabel(t: ModeEntryView): string {
  const mode = t.mode ?? '';
  const rate = t.throughput != null ? `${t.throughput} rps` : 'unlimited';
  const shape = mode === 'staircase' && t.steps ? ` · ${t.steps} steps` : '';
  return `${mode} · ${rate} · ${formatModeDuration(t.duration)}${shape}`;
}

/**
 * The derivation note under a mode entry's resolved numbers (Q8): one line
 * per derived field, phrased from what the page can actually know. The
 * implied p95 is concurrency / (3 x rate) -- the Little's-Law inverse --
 * rendered approximate because the engine-count floor can raise
 * concurrency above the law's own count. perPodQps, when the capacity
 * profile fetch succeeded, names the per-pod rate the engines came from.
 */
export function modeDerivationLines(t: ModeEntryView, perPodQps?: number): string[] {
  const lines: string[] = [];
  const qps = t.throughput ?? 0;
  if (qps > 0) {
    const p95ms = Math.round((t.concurrency / (3 * qps)) * 1000);
    lines.push(`concurrency ${t.concurrency} ← Little's Law (${qps} rps × p95 ≈ ${p95ms}ms × 3.0 headroom)`);
  } else {
    lines.push(`concurrency ${t.concurrency} ← resolved by the server`);
  }
  if (perPodQps != null && perPodQps > 0) {
    lines.push(`engines ${t.engines} ← capacity profile ${perPodQps} rps/pod at ${qps} rps`);
  } else {
    lines.push(`engines ${t.engines} ← capacity profile fan-out`);
  }
  const why: Record<LoadMode, string> = {
    burst: 'cold start is the subject',
    ramp: `ramp policy: duration/5, clamped to 60–600s (${t.duration}s → ${t.rampup}s)`,
    soak: 'soak policy: fixed 60s warmup before the hold',
    staircase: `staircase policy: ${t.steps ?? 5} steps to the ceiling, step edges are the shape`,
  };
  lines.push(`ramp-up ${t.rampup}s ← ${why[(t.mode ?? '') as LoadMode] ?? 'mode policy'}`);
  return lines;
}
