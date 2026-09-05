import { describe, expect, it } from 'vitest';
import {
  buildConfig,
  buildFragment,
  concurrencyEnginesWarning,
  flowSteps,
  stepError,
  type NewTestForm,
} from './newTestFlow';

const form: NewTestForm = {
  name: 'checkout-smoke',
  targetUrl: 'http://checkout.svc/',
  method: 'GET',
  headers: [
    { name: 'X-Auth', value: 'tok' },
    { name: 'Cookie', value: 'session=abc' },
  ],
  concurrency: 50,
  engines: 2,
  rampup: 30,
  duration: 300,
  engine: 'jmeter',
};

describe('concurrencyEnginesWarning (shard.Plan clamp guard)', () => {
  it('warns when engines exceed concurrency, naming both numbers', () => {
    const w = concurrencyEnginesWarning(10, 20);
    expect(w).not.toBeNull();
    expect(w).toContain('20');
    expect(w).toContain('10');
    expect(w).toContain('clamp');
  });

  it('no warning when concurrency >= engines', () => {
    expect(concurrencyEnginesWarning(50, 2)).toBeNull();
    expect(concurrencyEnginesWarning(2, 2)).toBeNull();
  });

  it('no warning at zero concurrency (create validation owns that)', () => {
    expect(concurrencyEnginesWarning(0, 2)).toBeNull();
  });
});

describe('buildFragment (G3 body)', () => {
  it('emits a bare taurus.Scenario — no scenarios: wrapper, no name line', () => {
    const y = buildFragment(form);
    // G3 unmarshals a bare taurus.Scenario; the wrapped shape 400s with
    // "at least one request is required" (verified live, phase 22 finding 1).
    expect(y).not.toContain('scenarios:');
    expect(y).not.toContain('checkout-smoke');
    expect(y.startsWith('default-address: http://checkout.svc')).toBe(true); // root-level, trailing slash stripped
    expect(y).toContain('X-Auth: tok');
    expect(y).toContain('Cookie: session=abc'); // no dedicated cookie field
    expect(y).toContain('- method: GET');
  });

  it('omits the headers block when there are none', () => {
    const y = buildFragment({ ...form, headers: [] });
    expect(y).not.toContain('headers:');
  });
});

describe('buildConfig (G7 JSON body)', () => {
  it('carries a non-zero rampup and the scenario binding', () => {
    const cfg = buildConfig(form, 42);
    expect(cfg.tests[0].scenario_id).toBe(42);
    expect(cfg.tests[0].rampup).toBe(30); // non-zero default, per spec
    expect(cfg.tests[0].concurrency).toBe(50);
    expect(cfg.tests[0].engines).toBe(2);
    expect(cfg.tests[0].duration).toBe(300);
  });
});

describe('flowSteps + stepError', () => {
  it('covers all five steps in order', () => {
    expect(flowSteps).toEqual([
      'resolve project',
      'create scenario',
      'create execution',
      'save requests fragment',
      'save load config',
    ]);
  });

  it('stepError names the step, not a bare error', () => {
    const e = stepError('create scenario', new Error('duplicate name'));
    expect(e.message).toMatch(/^Step "create scenario" failed: duplicate name$/);
  });
});
