import { describe, expect, it } from 'vitest';
import { fanOutCopy, isCalibrationExecution, jobIsActive, jobProgressLine } from './CapacityPanel';
import type { CalibrationJob } from '../api/calibration';

// R7's contract: the number only exists alongside "ok"; every other status
// explains itself and offers a call to action.
describe('fanOutCopy', () => {
  it('ok states calibrated with no CTA', () => {
    const c = fanOutCopy('ok');
    expect(c.title).toMatch(/[Cc]alibrated/);
    expect(c.cta).toBeNull();
  });

  it('each non-ok status has its own title, detail, and cta', () => {
    for (const status of ['no_profile', 'stale', 'target_limited', 'inconclusive', 'engine_floor'] as const) {
      const c = fanOutCopy(status);
      expect(c.title.length).toBeGreaterThan(0);
      expect(c.detail.length).toBeGreaterThan(0);
      expect(c.cta).not.toBeNull();
    }
    // ...and the explanations are pairwise distinct.
    const titles = ['no_profile', 'stale', 'target_limited', 'inconclusive', 'engine_floor'].map(
      (s) => fanOutCopy(s as never).title,
    );
    expect(new Set(titles).size).toBe(5);
  });

  // Phase 44's split: exec 15 wore inconclusive's "both ends still healthy"
  // copy while its actual job had every step engine_saturated at the lowest
  // rate -- the opposite finding. engine_floor owns that copy now, and
  // inconclusive's copy states its real case precisely.
  it('engine_floor explains that the engine saturated below measurable load', () => {
    const c = fanOutCopy('engine_floor');
    expect(c.title).toBe('Engine saturates below measurable load');
    expect(c.detail).toContain('too light');
    expect(c.cta).toContain('Loosen the criterion');
  });

  it('inconclusive copy is precise: budget exhausted, neither end saturated', () => {
    const c = fanOutCopy('inconclusive');
    expect(c.detail).toContain('neither');
    expect(c.detail).not.toContain('both ends');
  });
});

describe('jobProgressLine', () => {
  const base: CalibrationJob = {
    id: 1, execution_id: 1, phase: 'pending', step_count: 0,
    created_time: '2026-09-02T00:00:00Z', steps: [],
  };

  it('bracketing shows step count and next qps', () => {
    const line = jobProgressLine({ ...base, phase: 'bracketing', step_count: 3, next_requested_qps: 42.4 });
    expect(line).toContain('bracketing');
    expect(line).toContain('step 3');
    expect(line).toContain('42');
  });

  it('bisecting shows its phase name', () => {
    const line = jobProgressLine({ ...base, phase: 'bisecting', step_count: 5, next_requested_qps: 91 });
    expect(line).toContain('bisecting');
    expect(line).toContain('step 5');
  });

  it('done reports per-pod qps and what saturated', () => {
    const line = jobProgressLine({
      ...base, phase: 'done', step_count: 8,
      result: { saturated_by: 'engine', per_pod_qps: 310 },
    });
    expect(line).toContain('310 qps/pod');
    expect(line).toContain('engine');
  });

  it('failed carries the reason', () => {
    const line = jobProgressLine({ ...base, phase: 'failed', failure_reason: 'run errored' });
    expect(line).toContain('failed');
    expect(line).toContain('run errored');
  });
});

describe('jobIsActive', () => {
  it('pending/bracketing/bisecting are active; done/failed are not', () => {
    const mk = (phase: string) =>
      ({ id: 1, execution_id: 1, phase, step_count: 0, created_time: '', steps: [] } as CalibrationJob);
    expect(jobIsActive(mk('pending'))).toBe(true);
    expect(jobIsActive(mk('bracketing'))).toBe(true);
    expect(jobIsActive(mk('bisecting'))).toBe(true);
    expect(jobIsActive(mk('done'))).toBe(false);
    expect(jobIsActive(mk('failed'))).toBe(false);
  });
});

// Phase 39's mount gate: the Capacity card exists only on calibrate_engine
// executions -- the bug was mounting it for ANY engine'd execution, so a
// normal soak's Calibrate button could only ever earn a 400.
describe('isCalibrationExecution', () => {
  it('mounts only for a calibrate_engine execution with an engine', () => {
    expect(isCalibrationExecution({ engine: 'jmeter', kind: 'calibrate_engine' })).toBe(true);
  });

  it('does not mount when kind is normal or absent (pre-phase39 backend)', () => {
    expect(isCalibrationExecution({ engine: 'jmeter', kind: 'normal' })).toBe(false);
    expect(isCalibrationExecution({ engine: 'jmeter' })).toBe(false);
    expect(isCalibrationExecution({ engine: 'jmeter', kind: '' })).toBe(false);
  });

  it('does not mount while info is unloaded, or without an engine', () => {
    expect(isCalibrationExecution(null)).toBe(false);
    expect(isCalibrationExecution(undefined)).toBe(false);
    expect(isCalibrationExecution({ kind: 'calibrate_engine' })).toBe(false);
  });
});
