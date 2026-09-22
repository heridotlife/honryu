// Phase 103's render matrix: one amber row per label whose own trend
// crossed the leak thresholds, nothing for healthy-only label lists (a
// flat label is data, not a finding) and nothing when the run carried no
// per-label trends at all.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it } from 'vitest';
import PerLabelSoakBanner from './PerLabelSoakBanner';
import type { SoakTrend } from '../api/reports';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | null = null;
let root: Root | null = null;

async function renderBanner(trend: SoakTrend) {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(<PerLabelSoakBanner trend={trend} />);
  });
}

afterEach(() => {
  act(() => {
    root?.unmount();
  });
  container?.remove();
  container = null;
  root = null;
});

/** The phase's own fixture: a leaking checkout diluted by a flat,
 * higher-volume browse -- the aggregate stays quiet while the label
 * trend fires. */
const diluted: SoakTrend = {
  first_half_ms: 197.7,
  second_half_ms: 211.4,
  slope_ms_per_min: 11,
  leak_suspected: false,
  labels: [
    { label: 'browse', first_half_ms: 200.2, second_half_ms: 199.8, slope_ms_per_min: 0, leak_suspected: false },
    { label: 'checkout', first_half_ms: 174.6, second_half_ms: 325.4, slope_ms_per_min: 120.8, leak_suspected: true },
  ],
};

describe('PerLabelSoakBanner (phase 103)', () => {
  it('renders one row per leaking label, with that label\'s own figures', async () => {
    await renderBanner(diluted);

    const rows = container!.querySelectorAll('[data-testid="soak-label-leak-row"]');
    expect(rows.length).toBe(1);
    expect(rows[0].textContent).toContain('Soak leak suspected — checkout');
    expect(rows[0].textContent).toContain('first 175ms → second 325ms');
    expect(rows[0].textContent).toContain('slope +121ms/min');
  });

  it('renders every leaking label, not just the first', async () => {
    await renderBanner({
      ...diluted,
      labels: [
        ...diluted.labels!,
        { label: 'search', first_half_ms: 90.4, second_half_ms: 180.2, slope_ms_per_min: 35.9, leak_suspected: true },
      ],
    });

    const rows = container!.querySelectorAll('[data-testid="soak-label-leak-row"]');
    expect(rows.length).toBe(2);
    expect(rows[1].textContent).toContain('search');
  });

  it('renders nothing for a healthy-only label list', async () => {
    await renderBanner({
      ...diluted,
      labels: diluted.labels!.filter((l) => !l.leak_suspected),
    });

    expect(container!.querySelector('[data-testid="soak-label-leak-banner"]')).toBeNull();
  });

  it('renders nothing when the run carried no per-label trends', async () => {
    const { labels: _labels, ...withoutLabels } = diluted;
    await renderBanner(withoutLabels);

    expect(container!.querySelector('[data-testid="soak-label-leak-banner"]')).toBeNull();
  });

  it('renders the label banner even while the aggregate stays quiet', async () => {
    // The dilution case is the component's reason to exist: the aggregate
    // never fired, so nothing else on the page would carry the finding.
    expect(diluted.leak_suspected).toBe(false);
    await renderBanner(diluted);

    expect(container!.querySelectorAll('[data-testid="soak-label-leak-row"]').length).toBe(1);
  });
});
