// RunCompare: the delta math as pure exports (sign, color semantics,
// divide-by-zero), and the mounted page (selector wiring, ?runs=a,b
// preselect, same-run hint, chart overlay and hide rule), in
// DashboardLayout.test.tsx's createRoot + act style.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import RunCompare, { deltaClass, deltaKind, formatDelta, pctDelta } from './RunCompare';
import type { Report } from '../api/reports';

describe('delta math (pure)', () => {
  it('computes the signed percent from A to B', () => {
    expect(pctDelta(100, 110)).toBe(10);
    expect(pctDelta(0.2, 0.18)).toBeCloseTo(-10, 10);
    expect(pctDelta(95, 110)).toBeCloseTo(15.789, 3);
  });

  it('returns null on a zero baseline instead of Infinity (divide-by-zero guard)', () => {
    expect(pctDelta(0, 5)).toBeNull();
    expect(pctDelta(0, 0)).toBeNull();
    expect(pctDelta(Number.NaN, 5)).toBeNull();
  });

  it('classifies improvement vs regression by metric direction', () => {
    // lower-is-better: latency, error rate.
    expect(deltaKind('lower', -10)).toBe('improvement');
    expect(deltaKind('lower', 100)).toBe('regression');
    // higher-is-better: RPS.
    expect(deltaKind('higher', 15.8)).toBe('improvement');
    expect(deltaKind('higher', -3)).toBe('regression');
    // no change, nothing to say.
    expect(deltaKind('lower', 0)).toBe('neutral');
    expect(deltaKind('higher', null)).toBe('none');
  });

  it('colors improvement green and regression red, nothing else', () => {
    expect(deltaClass('improvement')).toContain('text-emerald-600');
    expect(deltaClass('regression')).toContain('text-red-600');
    expect(deltaClass('neutral')).not.toContain('red');
    expect(deltaClass('none')).not.toContain('emerald');
  });

  it('formats signed percents and em-dashes the null guard produces', () => {
    expect(formatDelta(10.25)).toBe('+10.3%');
    expect(formatDelta(-9.96)).toBe('-10.0%');
    expect(formatDelta(0)).toBe('0.0%');
    expect(formatDelta(null)).toBe('—');
  });
});

// The mounted half.
(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

// Two runs of execution 5: the list arrives most-recent-first, so run 9
// (newer, faster latency, higher RPS, but worse errors) leads run 8.
const runNewer: Report = {
  execution_id: 5,
  scenario_id: 2,
  run_id: 9,
  started_at: '2026-09-04T10:00:00Z',
  ended_at: '2026-09-04T10:01:00Z',
  outcome: 'passed',
  requested: { concurrency: 10, throughput: 100 },
  achieved: { concurrency: 10, throughput: 110, samples: 6600, failed: 132 },
  error_rate: 0.02,
  latency: { '50': 0.04, '95': 0.18, '99': 0.35 },
  attribution: { target: 2, engine: 0, unknown: 0 },
};

const runOlder: Report = {
  execution_id: 5,
  scenario_id: 2,
  run_id: 8,
  started_at: '2026-09-03T10:00:00Z',
  ended_at: '2026-09-03T10:01:00Z',
  outcome: 'passed',
  requested: { concurrency: 10, throughput: 100 },
  achieved: { concurrency: 10, throughput: 95, samples: 6000, failed: 60 },
  error_rate: 0.01,
  // No p99 on the older run: exercises the em-dash row.
  latency: { '50': 0.05, '95': 0.2 },
  attribution: { target: 1, engine: 0, unknown: 0 },
};

const seriesFixture = (base: number) => ({
  points: [
    { ts: 1_700_000_000 + base, vus: 10, rps: 100, err_pct: 0, latency: { '50': 0.05, '90': 0.1, '95': 0.2, '99': 0.4 } },
    { ts: 1_700_000_001 + base, vus: 10, rps: 102, err_pct: 1, latency: { '50': 0.055, '90': 0.11, '95': 0.21, '99': 0.41 } },
  ],
});

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

let container: HTMLDivElement | null = null;
let root: Root | null = null;

interface MountOptions {
  url?: string;
  reports?: Report[];
  series?: Record<number, () => Response>;
  reportsStatus?: number;
}

async function renderCompare(opts: MountOptions = {}) {
  const { url = '/executions/5/compare', reports = [runNewer, runOlder], series = {}, reportsStatus = 200 } = opts;
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url_ = String(input);
      if (url_.endsWith('/api/executions/5/reports')) {
        return reportsStatus === 200 ? json(reports) : json({ message: 'reports backend down' }, reportsStatus);
      }
      for (const [runId, respond] of Object.entries(series)) {
        if (url_.endsWith(`/api/runs/${runId}/series`)) {
          return respond();
        }
      }
      return json({ message: `no stub for ${url_}` }, 500);
    })
  );
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter initialEntries={[url]}>
        <Routes>
          <Route path="/executions/:id/compare" element={<RunCompare />} />
        </Routes>
      </MemoryRouter>
    );
  });
  // Flush the reports fetch, then the series fetch it triggers.
  await act(async () => {});
  await act(async () => {});
}

function selectValue(testId: string): string {
  return (container!.querySelector(`[data-testid="${testId}"]`) as HTMLSelectElement).value;
}

async function setSelect(testId: string, value: string) {
  const select = container!.querySelector(`[data-testid="${testId}"]`) as HTMLSelectElement;
  const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')!.set!;
  await act(async () => {
    setter.call(select, value);
    select.dispatchEvent(new Event('change', { bubbles: true }));
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
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

describe('RunCompare (mounted)', () => {
  it('preselects both selectors from ?runs=a,b', async () => {
    await renderCompare({ url: '/executions/5/compare?runs=9,8' });

    expect(selectValue('select-run-a')).toBe('9');
    expect(selectValue('select-run-b')).toBe('8');
    // The table's columns carry the selected runs.
    expect(container!.querySelector('[data-testid="delta-table"] thead')!.textContent).toContain('Run #9');
    expect(container!.querySelector('[data-testid="delta-table"] thead')!.textContent).toContain('Run #8');
  });

  it('defaults to oldest baseline / newest candidate without the query', async () => {
    await renderCompare();

    expect(selectValue('select-run-a')).toBe('8');
    expect(selectValue('select-run-b')).toBe('9');
  });

  it('renders the delta table with colored, signed percents', async () => {
    await renderCompare();

    const row = (key: string) => container!.querySelector(`tr[data-metric="${key}"]`)!;
    const deltaCell = (key: string) => row(key).querySelector('td:last-child')!;

    // p50 0.05s -> 0.04s: -20%, an improvement (lower latency, green).
    expect(row('p50').textContent).toContain('50.0 ms');
    expect(row('p50').textContent).toContain('40.0 ms');
    expect(deltaCell('p50').textContent).toBe('-20.0%');
    expect(deltaCell('p50').className).toContain('text-emerald-600');

    // Error rate 1% -> 2%: +100%, a regression (red).
    expect(row('errorRate').textContent).toContain('1.00%');
    expect(row('errorRate').textContent).toContain('2.00%');
    expect(deltaCell('errorRate').textContent).toBe('+100.0%');
    expect(deltaCell('errorRate').className).toContain('text-red-600');

    // RPS 95 -> 110: ~+15.8%, an improvement (higher is better).
    expect(row('rps').textContent).toContain('95.0 req/s');
    expect(deltaCell('rps').textContent).toBe('+15.8%');
    expect(deltaCell('rps').className).toContain('text-emerald-600');

    // p99 missing on the older run: values and delta are em-dashes, uncolored.
    expect(deltaCell('p99').textContent).toBe('—');
    expect(deltaCell('p99').className).not.toContain('text-red-600');
    expect(deltaCell('p99').className).not.toContain('text-emerald-600');
  });

  it('swaps runs when a selector changes (selector wiring)', async () => {
    await renderCompare(); // A=8, B=9 by default.

    await setSelect('select-run-b', '8');
    expect(container!.querySelector('[data-testid="compare-same-run"]')).not.toBeNull();
    // Same run selected: the delta table and chart stand down.
    expect(container!.querySelector('[data-testid="delta-table"]')).toBeNull();
    expect(container!.querySelector('[data-testid="chart-compare-p95"]')).toBeNull();

    // Back to a different pair: everything returns, reading B vs A.
    await setSelect('select-run-b', '9');
    expect(container!.querySelector('[data-testid="compare-same-run"]')).toBeNull();
    expect(container!.querySelector('[data-testid="delta-table"]')).not.toBeNull();
    // p95 0.2 -> 0.18 is the improvement again.
    const p95 = container!.querySelector('tr[data-metric="p95"] td:last-child')!;
    expect(p95.textContent).toBe('-10.0%');
  });

  it('overlays both runs\' p95 series on one chart', async () => {
    await renderCompare({ series: { 8: () => json(seriesFixture(0)), 9: () => json(seriesFixture(60)) } });

    const chart = container!.querySelector('[data-testid="chart-compare-p95"]');
    expect(chart).not.toBeNull();
    expect(chart!.querySelector('[data-series="run #8 p95"]')).not.toBeNull();
    expect(chart!.querySelector('[data-series="run #9 p95"]')).not.toBeNull();
  });

  it('hides the chart when either run has no series, keeping the delta table', async () => {
    await renderCompare({ series: { 8: () => json({ points: [] }), 9: () => json(seriesFixture(0)) } });

    expect(container!.querySelector('[data-testid="chart-compare-p95"]')).toBeNull();
    expect(container!.textContent).not.toContain('p95 overlay');
    expect(container!.querySelector('[data-testid="delta-table"]')).not.toBeNull();
  });

  it('shows the no-runs state when the execution has no reports', async () => {
    await renderCompare({ reports: [] });

    expect(container!.querySelector('[data-testid="compare-no-runs"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="select-run-a"]')).toBeNull();
  });

  it('surfaces a reports-load failure and an invalid execution id', async () => {
    await renderCompare({ reportsStatus: 500 });
    expect(container!.querySelector('[role="alert"]')!.textContent).toContain('reports backend down');

    await renderCompare({ url: '/executions/abc/compare' });
    expect(container!.textContent).toContain('Invalid execution id.');
    expect(container!.querySelector('[data-testid="select-run-a"]')).toBeNull();
  });

  // Phase 51 landmark gate: the page's outermost element is a labelled
  // region a screen reader can jump to, in both the loaded and the
  // no-runs state (the region div wraps every branch).
  it('renders the page as a labelled region (phase 51)', async () => {
    await renderCompare();
    const region = container!.querySelector('[role="region"]');
    expect(region).not.toBeNull();
    expect(region!.getAttribute('aria-label')).toBe('Run comparison panel');

    await renderCompare({ reports: [] });
    expect(container!.querySelector('[role="region"]')!.getAttribute('aria-label')).toBe('Run comparison panel');
  });
});

// Phase 52: the breadcrumb trail -- "Executions / #5 / Compare". Two
// ancestor links, the current page as aria-current text, chevrons between.
describe('RunCompare breadcrumbs (phase 52)', () => {
  it('renders the trail with two links and aria-current on the last item', async () => {
    await renderCompare();

    const nav = container!.querySelector('nav[aria-label="breadcrumb"]');
    expect(nav).not.toBeNull();
    const items = Array.from(nav!.querySelectorAll('li'));
    expect(items.map((li) => li.textContent?.trim())).toEqual(['Executions', '#5', 'Compare']);
    // Ancestors are links; the current page is not.
    const links = Array.from(nav!.querySelectorAll('a')).map((a) => a.getAttribute('href'));
    expect(links).toEqual(['/executions', '/executions/5']);
    const current = nav!.querySelector('[aria-current="page"]');
    expect(current).not.toBeNull();
    expect(current!.textContent).toBe('Compare');
    expect(current!.tagName).toBe('SPAN');
    // A chevron separator between each pair, hidden from AT.
    expect(nav!.querySelectorAll('svg[aria-hidden="true"]').length).toBe(2);
  });
});
