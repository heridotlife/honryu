// Phase 90's simple-mode pure model: duration math, the validation
// mirror, soak's soft guidance, the wire test's shape, and the display
// strings (chip + derivation note). No derivation of resolved numbers
// ever happens client-side -- these pins keep it that way.
import { describe, expect, it } from 'vitest';
import {
  buildModeTest,
  durationSeconds,
  formatModeDuration,
  initialModeForm,
  modeChipLabel,
  modeDerivationLines,
  modeFormValid,
  soakTooShortWarning,
  validateModeForm,
} from './modeConfig';

describe('durationSeconds', () => {
  it('converts minutes and hours to the wire’s seconds', () => {
    expect(durationSeconds({ ...initialModeForm, duration: 10, unit: 'm' })).toBe(600);
    expect(durationSeconds({ ...initialModeForm, duration: 2, unit: 'h' })).toBe(7200);
  });
});

describe('validateModeForm', () => {
  it('accepts the defaults', () => {
    expect(validateModeForm(initialModeForm)).toEqual({});
    expect(modeFormValid(initialModeForm)).toBe(true);
  });

  it('refuses a non-positive rate and a non-positive duration, naming both', () => {
    const errs = validateModeForm({ ...initialModeForm, qps: 0, duration: 0 });
    expect(errs.qps).toContain('positive');
    expect(errs.duration).toContain('positive');
    expect(modeFormValid({ ...initialModeForm, qps: 0 })).toBe(false);
  });
});

describe('soakTooShortWarning', () => {
  it('warns under 30 minutes, silent at or above, silent for other modes', () => {
    expect(soakTooShortWarning({ ...initialModeForm, mode: 'soak', duration: 20, unit: 'm' })).toContain('soak');
    expect(soakTooShortWarning({ ...initialModeForm, mode: 'soak', duration: 30, unit: 'm' })).toBeNull();
    expect(soakTooShortWarning({ ...initialModeForm, mode: 'soak', duration: 1, unit: 'h' })).toBeNull();
    expect(soakTooShortWarning({ ...initialModeForm, mode: 'burst', duration: 5, unit: 'm' })).toBeNull();
  });
});

describe('buildModeTest', () => {
  it('states only mode, rate, and duration — the resolved fields ride as zeros for the server to overwrite', () => {
    const t = buildModeTest('checkout', 42, { mode: 'soak', qps: 250, duration: 2, unit: 'h' });
    expect(t).toEqual({
      name: 'checkout',
      scenario_id: 42,
      concurrency: 0,
      rampup: 0,
      engines: 0,
      throughput: 250,
      duration: 7200,
      mode: 'soak',
    });
    // mode is the LAST key: Go's marshal order (after csv_split), the
    // stagesConfig contract.
    expect(Object.keys(t).pop()).toBe('mode');
  });
});

describe('formatModeDuration', () => {
  it('renders seconds, minutes, and hour+minute mixes compactly', () => {
    expect(formatModeDuration(45)).toBe('45s');
    expect(formatModeDuration(600)).toBe('10m');
    expect(formatModeDuration(3600)).toBe('1h');
    expect(formatModeDuration(5400)).toBe('1h 30m');
  });
});

describe('modeChipLabel', () => {
  it('states mode · rate · duration (the spec’s worked example)', () => {
    expect(
      modeChipLabel({ mode: 'burst', throughput: 500, duration: 600, concurrency: 0, engines: 0, rampup: 0 })
    ).toBe('burst · 500 rps · 10m');
    expect(
      modeChipLabel({ mode: 'soak', throughput: undefined, duration: 3600, concurrency: 0, engines: 0, rampup: 0 })
    ).toBe('soak · unlimited · 1h');
  });
});

describe('modeDerivationLines', () => {
  const resolved = { mode: 'ramp', throughput: 500, concurrency: 375, engines: 4, rampup: 120, duration: 600 };

  it('explains each derived field from what the page can know', () => {
    const lines = modeDerivationLines(resolved, 125);
    expect(lines[0]).toBe("concurrency 375 ← Little's Law (500 rps × p95 ≈ 250ms × 3.0 headroom)");
    expect(lines[1]).toBe('engines 4 ← capacity profile 125 rps/pod at 500 rps');
    expect(lines[2]).toBe('ramp-up 120s ← ramp policy: duration/5, clamped to 60–600s (600s → 120s)');
  });

  it('degrades wording without the per-pod rate, and states each mode’s policy', () => {
    const lines = modeDerivationLines(resolved);
    expect(lines[1]).toBe('engines 4 ← capacity profile fan-out');
    expect(modeDerivationLines({ ...resolved, mode: 'burst' })[2]).toContain('cold start is the subject');
    expect(modeDerivationLines({ ...resolved, mode: 'soak' })[2]).toContain('fixed 60s warmup');
  });
});

// Phase 91: the multi-scenario Simple rows -- per-row validation, the
// submit-time name derivation, and the one-entry-per-scenario wire shape.
import {
  buildModeTests,
  initialModeRows,
  modeRowsValid,
  scenarioName,
  validateModeRows,
  type ModeRowValue,
} from './modeConfig';

const row = (patch: Partial<ModeRowValue> = {}): ModeRowValue => ({
  mode: 'burst',
  qps: 100,
  duration: 10,
  unit: 'm',
  name: '',
  ...patch,
});

describe('validateModeRows (phase 91)', () => {
  it('accepts the initial single row and blanks names generally', () => {
    expect(modeRowsValid(initialModeRows)).toBe(true);
    expect(modeRowsValid([row(), row()])).toBe(true); // both blank: derived names differ
  });

  it('flags qps/duration per row, naming the offending row only', () => {
    const errors = validateModeRows([row(), row({ qps: 0, duration: 0 })]);
    expect(Object.keys(errors[0])).toHaveLength(0);
    expect(errors[1].qps).toContain('positive');
    expect(errors[1].duration).toContain('positive');
    expect(modeRowsValid([row(), row({ qps: 0 })])).toBe(false);
  });

  it('flags a scenario name used on two rows, not a single use', () => {
    const errors = validateModeRows([row({ name: 'checkout' }), row({ name: 'checkout' }), row({ name: 'search' })]);
    expect(errors[0].name).toBeDefined();
    expect(errors[1].name).toBeDefined();
    expect(errors[2].name).toBeUndefined();
    expect(modeRowsValid([row({ name: 'checkout' }), row({ name: 'checkout' })])).toBe(false);
  });
});

describe('scenarioName (phase 91)', () => {
  it('uses the row name when stated, trimmed', () => {
    expect(scenarioName(row({ name: '  checkout ' }), 1, 'peak')).toBe('checkout');
  });

  it('derives the test name for row 1 and indexed names after it', () => {
    expect(scenarioName(row(), 0, 'peak')).toBe('peak');
    expect(scenarioName(row(), 1, 'peak')).toBe('peak-2');
    expect(scenarioName(row(), 2, 'peak')).toBe('peak-3');
  });
});

describe('buildModeTests (phase 91)', () => {
  it('emits one mode-shaped entry per row, in row order, each on its own scenario', () => {
    const rows = [
      row({ name: 'checkout', qps: 500, duration: 10, unit: 'm' }),
      row({ mode: 'soak', qps: 50, duration: 1, unit: 'h' }),
    ];
    const tests = buildModeTests('peak', [41, 42], rows);
    expect(tests).toHaveLength(2);
    expect(tests[0]).toEqual(buildModeTest('checkout', 41, rows[0]));
    expect(tests[1]).toEqual(buildModeTest('peak-2', 42, rows[1]));
    expect(tests[1].mode).toBe('soak');
    expect(tests[1].duration).toBe(3600);
    // No cross-row sharing: each entry's numbers come from its own row.
    expect(tests[0].throughput).toBe(500);
    expect(tests[1].throughput).toBe(50);
  });
});
