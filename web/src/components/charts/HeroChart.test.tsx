import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it } from 'vitest';
import HeroChart, { heroSeries } from './HeroChart';
import type { SeriesPoint } from '../../api/series';

// Points with a live error rate and an RPS band far above 100 -- the very
// scale clash heroSeries exists to smooth over (refMax 600, so ×6).
const fixture: SeriesPoint[] = [
  { ts: 100, vus: 2, rps: 200, err_pct: 0 },
  { ts: 101, vus: 4, rps: 400, err_pct: 5 },
  { ts: 102, vus: 6, rps: 600, err_pct: 50 },
];

describe('heroSeries', () => {
  it('carries VUs and RPS through raw', () => {
    const [vus, rps] = heroSeries(fixture);
    expect(vus.name).toBe('VUs');
    expect(vus.points).toEqual([
      { x: 100, y: 2 },
      { x: 101, y: 4 },
      { x: 102, y: 6 },
    ]);
    expect(rps.name).toBe('RPS');
    expect(rps.points).toEqual([
      { x: 100, y: 200 },
      { x: 101, y: 400 },
      { x: 102, y: 600 },
    ]);
  });

  it('rescales error % from its 0-100 domain onto the VUs/RPS band (refMax 600 → ×6)', () => {
    const err = heroSeries(fixture)[2];
    expect(err.name).toBe('error %');
    expect(err.points.map((p) => p.y)).toEqual([0, 30, 300]);
  });

  // A no-traffic run has no band to scale onto; raw keeps errors visible.
  it('keeps err_pct raw when every VUs/RPS sample is zero', () => {
    const err = heroSeries([{ ts: 1, vus: 0, rps: 0, err_pct: 40 }])[2];
    expect(err.points).toEqual([{ x: 1, y: 40 }]);
  });
});

// The mounted half, TimeSeriesChart.test.tsx's style: createRoot + act.
(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | null = null;
let root: Root | null = null;

async function renderHero(props: Parameters<typeof HeroChart>[0]) {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(<HeroChart {...props} />);
  });
}

afterEach(() => {
  const r = root;
  if (r !== null && container !== null) {
    act(() => {
      r.unmount();
    });
  }
  container?.remove();
  container = null;
  root = null;
});

describe('HeroChart (mounted)', () => {
  it('draws all three series under the chart-hero testid', async () => {
    await renderHero({ points: fixture });

    expect(container!.querySelector('[data-testid="chart-hero"]')).not.toBeNull();
    for (const name of ['VUs', 'RPS', 'error %']) {
      expect(container!.querySelector(`[data-series="${name}"]`)).not.toBeNull();
    }
    // The error series keeps the old charts' rose, scaled not restyled.
    expect(container!.querySelector('[data-series="error %"]')!.getAttribute('class')).toContain('text-rose-500');
    // One chart, naming all three series.
    expect(container!.querySelector('svg[role="img"]')!.getAttribute('aria-label')).toBe('Time series: VUs, RPS, error %');
    // The scale contract is stated where the operator reads it.
    expect(container!.textContent).toContain('error % (right-axis scale: 0-100)');
  });

  it('names VUs, RPS, and error in the legend above the chart', async () => {
    await renderHero({ points: fixture });

    const legend = container!.querySelector('[data-testid="chart-legend"]')!;
    expect(legend).not.toBeNull();
    const labels = Array.from(legend.querySelectorAll('span')).map((s) => s.textContent);
    for (const name of ['VUs', 'RPS', 'error %']) {
      expect(labels).toContain(name);
    }
  });

  it('renders the empty state for no points', async () => {
    await renderHero({ points: [] });

    expect(container!.querySelector('[data-testid="chart-hero"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="chart-empty"]')).not.toBeNull();
    expect(container!.querySelector('svg[role="img"]')).toBeNull();
  });
});
