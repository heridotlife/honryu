import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { executionDisplayName, formatRowTime } from './executionRow';

describe('executionDisplayName', () => {
  it('strips the calibrate flow\'s trailing ISO timestamp', () => {
    expect(executionDisplayName('calibrate checkout 2026-09-10T16:47:22.442Z')).toBe('calibrate checkout');
  });

  it('handles ISO stamps without fractional seconds and with numeric offsets', () => {
    expect(executionDisplayName('calibrate search 2026-09-10T16:47:22Z')).toBe('calibrate search');
    expect(executionDisplayName('calibrate search 2026-09-10T16:47:22+02:00')).toBe('calibrate search');
  });

  it('leaves ordinary names untouched', () => {
    expect(executionDisplayName('checkout-smoke')).toBe('checkout-smoke');
    expect(executionDisplayName('calibrate checkout')).toBe('calibrate checkout');
  });

  it('keeps mid-name ISO-looking text (only a trailing suffix is a timestamp)', () => {
    expect(executionDisplayName('2026-09-10T16:47:22.442Z run')).toBe('2026-09-10T16:47:22.442Z run');
  });
});

describe('formatRowTime', () => {
  // Pin the zone so the short-form assertions hold on any machine.
  beforeEach(() => {
    vi.stubEnv('TZ', 'UTC');
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it('formats the short "Sep 10, 16:47" form', () => {
    expect(formatRowTime('2026-09-10T16:47:22.442Z')).toBe('Sep 10, 16:47');
  });

  it('zero-pads the clock', () => {
    expect(formatRowTime('2026-01-05T03:05:00Z')).toBe('Jan 5, 03:05');
  });

  it('returns junk input verbatim rather than "Invalid Date"', () => {
    expect(formatRowTime('not-a-date')).toBe('not-a-date');
  });
});
