// Phase 96: the countdown preference's validation matrix, pinned from
// commit one. jsdom's localStorage is per-file, but each case sets its
// own starting value so the matrix reads as a table.
import { afterEach, describe, expect, it } from 'vitest';
import {
  COUNTDOWN_STORAGE_KEY,
  DEFAULT_COUNTDOWN_SECONDS,
  getCountdownSeconds,
  isValidCountdown,
  setCountdownSeconds,
} from './countdownPref';

afterEach(() => {
  localStorage.clear();
});

describe('isValidCountdown', () => {
  it('accepts bare integers 0 through 60', () => {
    for (const n of [0, 1, 3, 10, 30, 60]) {
      expect(isValidCountdown(n)).toBe(true);
    }
  });

  it('rejects out-of-range, fractional, and non-number values', () => {
    for (const n of [-1, 61, 2.5, Number.NaN, Number.POSITIVE_INFINITY]) {
      expect(isValidCountdown(n)).toBe(false);
    }
  });
});

describe('getCountdownSeconds', () => {
  it('returns the default when the key is absent', () => {
    expect(getCountdownSeconds()).toBe(DEFAULT_COUNTDOWN_SECONDS);
    expect(DEFAULT_COUNTDOWN_SECONDS).toBe(10);
  });

  it('reads stored 0 and 60 verbatim (0 = launch immediately)', () => {
    localStorage.setItem(COUNTDOWN_STORAGE_KEY, '0');
    expect(getCountdownSeconds()).toBe(0);
    localStorage.setItem(COUNTDOWN_STORAGE_KEY, '60');
    expect(getCountdownSeconds()).toBe(60);
  });

  it('falls back to the default on invalid stored text', () => {
    for (const bad of ['61', '-1', '2.5', 'abc', '', '  ', '1e2', '0x5', '10s', '5px', '③']) {
      localStorage.setItem(COUNTDOWN_STORAGE_KEY, bad);
      expect(getCountdownSeconds(), `stored ${JSON.stringify(bad)}`).toBe(DEFAULT_COUNTDOWN_SECONDS);
    }
  });

  it('trims surrounding whitespace off an otherwise valid value', () => {
    localStorage.setItem(COUNTDOWN_STORAGE_KEY, ' 7 ');
    expect(getCountdownSeconds()).toBe(7);
  });
});

describe('setCountdownSeconds', () => {
  it('persists valid values as bare integers', () => {
    setCountdownSeconds(0);
    expect(localStorage.getItem(COUNTDOWN_STORAGE_KEY)).toBe('0');
    setCountdownSeconds(5);
    expect(localStorage.getItem(COUNTDOWN_STORAGE_KEY)).toBe('5');
    setCountdownSeconds(60);
    expect(localStorage.getItem(COUNTDOWN_STORAGE_KEY)).toBe('60');
  });

  it('refuses invalid values with no write at all', () => {
    setCountdownSeconds(5);
    setCountdownSeconds(61);
    expect(localStorage.getItem(COUNTDOWN_STORAGE_KEY)).toBe('5');
    setCountdownSeconds(-1);
    expect(localStorage.getItem(COUNTDOWN_STORAGE_KEY)).toBe('5');
    setCountdownSeconds(2.5);
    expect(localStorage.getItem(COUNTDOWN_STORAGE_KEY)).toBe('5');
  });
});
