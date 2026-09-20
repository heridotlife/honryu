// Phase 97: the modeDefaults table — the web mirror of internal/domain/
// loadmode's SuggestedThresholds. These numbers are a cross-layer
// contract: every case here has a twin in loadmode_test.go, and a change
// on one side without the other is a break.
import { describe, expect, it } from 'vitest';
import { suggestedThresholds } from './modeDefaults';

describe('suggestedThresholds — the per-mode table', () => {
  it('burst: cold-start ceiling, ordinary error budget', () => {
    expect(suggestedThresholds('burst', 20)).toEqual([
      { metric: 'error_rate', comparison: 'lt', value: 0.01 },
      { metric: 'http_p95_ms', comparison: 'lt', value: 500 },
    ]);
  });

  it('ramp: progressive load, looser latency ceiling', () => {
    expect(suggestedThresholds('ramp', 500)).toEqual([
      { metric: 'error_rate', comparison: 'lt', value: 0.01 },
      { metric: 'http_p95_ms', comparison: 'lt', value: 800 },
    ]);
  });

  it('soak: tighter error budget plus the throughput floor at 90% of target', () => {
    expect(suggestedThresholds('soak', 200)).toEqual([
      { metric: 'error_rate', comparison: 'lt', value: 0.005 },
      { metric: 'http_p95_ms', comparison: 'lt', value: 600 },
      { metric: 'throughput_qps', comparison: 'gt', value: 180 },
    ]);
  });

  it('soak floor scales with targetQps', () => {
    expect(suggestedThresholds('soak', 100)[2]).toEqual({ metric: 'throughput_qps', comparison: 'gt', value: 90 });
  });

  it('soak without a rate states no throughput floor', () => {
    expect(suggestedThresholds('soak', 0)).toHaveLength(2);
  });

  it('burst and ramp are rate-independent (the argument is the soak floor’s alone)', () => {
    for (const mode of ['burst', 'ramp'] as const) {
      expect(suggestedThresholds(mode, 10)).toEqual(suggestedThresholds(mode, 10000));
    }
  });
});

// Phase 98: the staircase row, pinned number-for-number against the Go
// table (loadmode.SuggestedThresholds) -- widest latency ceiling in the
// table, ordinary error budget, rate-independent like burst and ramp.
describe('suggestedThresholds staircase (phase 98)', () => {
  it('offers the widest defaults: error_rate < 1%, p95 < 1000ms', () => {
    expect(suggestedThresholds('staircase', 500)).toEqual([
      { metric: 'error_rate', comparison: 'lt', value: 0.01 },
      { metric: 'http_p95_ms', comparison: 'lt', value: 1000 },
    ]);
  });

  it('is rate-independent (no throughput floor for a staircase)', () => {
    expect(suggestedThresholds('staircase', 10)).toEqual(suggestedThresholds('staircase', 10000));
  });
});
