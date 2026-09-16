// RunCompare: the delta math as pure exports (sign, color semantics,
// divide-by-zero), and the mounted page (selector wiring, ?runs=a,b
// preselect, same-run hint, chart overlay and hide rule), in
// DashboardLayout.test.tsx's createRoot + act style.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import RunCompare, { deltaArrow, deltaClass, deltaKind, formatDelta, NEUTRAL_BAND_PCT, pctDelta, signedAbs } from './RunCompare';
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

  it('classifies improvement vs regression by metric direction (outside the band)', () => {
    // lower-is-better: latency, error rate.
    expect(deltaKind('lower', -10)).toBe('improvement');
    expect(deltaKind('lower', 100)).toBe('regression');
    // higher-is-better: RPS.
    expect(deltaKind('higher', 15.8)).toBe('improvement');
    expect(deltaKind('higher', -20)).toBe('regression');
    // unmeasurable on one side, nothing to say.
    expect(deltaKind('lower', null)).toBe('none');
    expect(deltaKind('higher', null)).toBe('none');
  });

  // Phase 75: within ±5% a movement is "no real movement" -- neutral in
  // both directions, boundary inclusive; a hair past the band classifies.
  it('reads a delta inside the 5% band as neutral, each direction', () => {
    expect(deltaKind('lower', -3)).toBe('neutral');
    expect(deltaKind('higher', 3)).toBe('neutral');
    expect(deltaKind('lower', 4.9)).toBe('neutral');
    expect(deltaKind('higher', -4.9)).toBe('neutral');
    expect(deltaKind('lower', 0)).toBe('neutral');
    expect(deltaKind('lower', -NEUTRAL_BAND_PCT)).toBe('neutral');
    expect(deltaKind('higher', NEUTRAL_BAND_PCT)).toBe('neutral');
    expect(deltaKind('lower', -5.1)).toBe('improvement');
    expect(deltaKind('higher', 5.1)).toBe('improvement');
    expect(deltaKind('lower', 5.1)).toBe('regression');
    expect(deltaKind('higher', -5.1)).toBe('regression');
  });

  it('arrows the raw direction: up when the value rose, down when it fell', () => {
    expect(deltaArrow(20)).toBe('▲');
    expect(deltaArrow(-0.5)).toBe('▼');
    expect(deltaArrow(0)).toBe('');
    expect(deltaArrow(null)).toBe('');
  });

  it('formats the absolute delta in the metric unit', () => {
    expect(signedAbs(0.04 - 0.05, (v) => `${(v * 1000).toFixed(1)} ms`)).toBe('-10.0 ms');
    expect(signedAbs(15, (v) => `${v.toFixed(1)} req/s`)).toBe('+15.0 req/s');
    expect(signedAbs(0, (v) => `${(v * 1000).toFixed(1)} ms`)).toBe('0.0 ms');
    expect(signedAbs(null, (v) => `${v}`)).toBe('—');
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
  /** GET /api/runs/compare's reply; absent = the endpoint is down (500),
   * which is the fallback path the older tests ride by default. */
  compare?: () => Response;
  /** When provided, every stubbed request's URL is recorded into it. */
  record?: string[];
}

async function renderCompare(opts: MountOptions = {}) {
  const { url = '/executions/5/compare', reports = [runNewer, runOlder], series = {}, reportsStatus = 200, compare, record } = opts;
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url_ = String(input);
      record?.push(url_);
      if (url_.endsWith('/api/executions/5/reports')) {
        return reportsStatus === 200 ? json(reports) : json({ message: 'reports backend down' }, reportsStatus);
      }
      if (url_.includes('/api/runs/compare')) {
        return compare ? compare() : json({ message: 'compare backend down' }, 500);
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

  it('renders the delta table with colored abs+percent deltas, arrows and signs', async () => {
    await renderCompare();

    const row = (key: string) => container!.querySelector(`tr[data-metric="${key}"]`)!;
    const absCell = (key: string) => row(key).querySelector('td[data-delta="abs"]')!;
    const pctCell = (key: string) => row(key).querySelector('td[data-delta="pct"]')!;

    // p50 0.05s -> 0.04s: -20%, an improvement (lower latency, green).
    // The arrow reads the raw direction (the value fell); the sign and the
    // color carry better/worse, so the cell is never color-only.
    expect(row('p50').textContent).toContain('50.0 ms');
    expect(row('p50').textContent).toContain('40.0 ms');
    expect(absCell('p50').textContent).toBe('▼ -10.0 ms');
    expect(pctCell('p50').textContent).toBe('▼ -20.0%');
    expect(pctCell('p50').className).toContain('text-emerald-600');

    // Error rate 1% -> 2%: +100%, a regression (red).
    expect(row('errorRate').textContent).toContain('1.00%');
    expect(row('errorRate').textContent).toContain('2.00%');
    expect(absCell('errorRate').textContent).toBe('▲ +1.00%');
    expect(pctCell('errorRate').textContent).toBe('▲ +100.0%');
    expect(pctCell('errorRate').className).toContain('text-red-600');

    // RPS 95 -> 110: ~+15.8%, an improvement (higher is better).
    expect(row('rps').textContent).toContain('95.0 req/s');
    expect(absCell('rps').textContent).toBe('▲ +15.0 req/s');
    expect(pctCell('rps').textContent).toBe('▲ +15.8%');
    expect(pctCell('rps').className).toContain('text-emerald-600');

    // p99 missing on the older run: values and both deltas are em-dashes,
    // uncolored and arrowless.
    expect(absCell('p99').textContent).toBe('—');
    expect(pctCell('p99').textContent).toBe('—');
    expect(pctCell('p99').className).not.toContain('text-red-600');
    expect(pctCell('p99').className).not.toContain('text-emerald-600');
  });

  // Phase 59: the delta card carries a summary verdict chip only when at
  // least one metric regressed -- the absence is the "stable" signal, and
  // the row cells already color the individual movements.
  it('chips the delta card when a metric regressed, and only then (phase 59)', async () => {
    await renderCompare();
    // Default fixture's only regression is error rate (1% -> 2%, +100%);
    // the title names it against the baseline run, no recompute beyond the
    // deltas the table itself already renders.
    const chip = container!.querySelector('[data-testid="compare-regression-chip"]') as HTMLElement;
    expect(chip).not.toBeNull();
    expect(chip.textContent).toBe('regressed');
    expect(chip.title).toBe('Error rate +100.0% vs run #8');

    // B strictly better-or-equal on every metric against A: no chip, table stays.
    const betterRun: Report = {
      ...runNewer,
      run_id: 10,
      started_at: '2026-09-05T10:00:00Z',
      error_rate: 0.005,
      latency: { '50': 0.035, '95': 0.15, '99': 0.3 },
      achieved: { concurrency: 10, throughput: 120, samples: 7200, failed: 36 },
    };
    await renderCompare({ reports: [betterRun, runNewer] });
    expect(container!.querySelector('[data-testid="compare-regression-chip"]')).toBeNull();
    expect(container!.querySelector('[data-testid="delta-table"]')).not.toBeNull();
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
    // p95 0.2 -> 0.18 is the improvement again, arrow down (the value fell).
    const p95 = container!.querySelector('tr[data-metric="p95"] td[data-delta="pct"]')!;
    expect(p95.textContent).toBe('▼ -10.0%');
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

    // Phase 76: the shared EmptyState -- first-run guidance, the one
    // action routed to /scenarios (never into this same runless
    // execution), and no pickers at all.
    const empty = container!.querySelector('[data-testid="compare-no-runs"]')!;
    expect(empty).not.toBeNull();
    expect(empty.querySelector('[data-testid="compare-no-runs-title"]')?.textContent).toBe('No runs yet');
    const action = empty.querySelector<HTMLAnchorElement>('[data-testid="compare-no-runs-action"]')!;
    expect(action.getAttribute('href')).toBe('/scenarios');
    expect(action.textContent).toBe('Run a scenario first');
    expect(container!.querySelector('[data-testid="select-run-a"]')).toBeNull();
  });

  // e2e selector safety, route shape: the empty state itself must not
  // introduce any /executions/ link -- with no runs, such a link would be
  // a dead loop (this very execution has nothing to show). The one action
  // points back at /scenarios instead. (The page's breadcrumb trail back
  // to the execution hub is navigation, not empty-state guidance, and
  // stays.)
  it('renders no ^/executions/ anchor inside the no-runs empty state (dead-loop safety)', async () => {
    await renderCompare({ reports: [] });

    const empty = container!.querySelector('[data-testid="compare-no-runs"]')!;
    expect(empty).not.toBeNull();
    expect(empty.querySelectorAll('a[href^="/executions/"]').length).toBe(0);
  });

  it('fills the results area with a pick hint while the selection is incomplete', async () => {
    await renderCompare(); // A=8, B=9 preselected -- table up, no hint.
    expect(container!.querySelector('[data-testid="compare-pick-hint"]')).toBeNull();

    // Collapse to the same run twice: the pick hint appears -- the results
    // area is never blank -- but carries no action button (the pickers
    // above ARE the action).
    await setSelect('select-run-b', '8');
    const hint = container!.querySelector('[data-testid="compare-pick-hint"]')!;
    expect(hint).not.toBeNull();
    expect(hint.querySelector('[data-testid="compare-pick-hint-title"]')?.textContent).toBe('Pick runs to compare');
    expect(hint.querySelector('button, a')).toBeNull();

    // A real pair restores the table and retires the hint.
    await setSelect('select-run-b', '9');
    expect(container!.querySelector('[data-testid="compare-pick-hint"]')).toBeNull();
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

// Phase 61: three (or more) runs against one baseline. The selection rides
// the same ?runs= spelling it always has, now with N ids; the summaries come
// off GET /api/runs/compare; the verdict chips (phase 59) sit per candidate
// column, exactly where the numbers they summarize sit.
describe('RunCompare multi-run (phase 61)', () => {
  // Run 8 is strictly better than baseline 9 on every metric (no chip);
  // run 10 is worse across the board (chips).
  const runBetter: Report = {
    ...runOlder,
    run_id: 8,
    started_at: '2026-09-03T10:00:00Z',
    error_rate: 0.015,
    latency: { '50': 0.035, '95': 0.15, '99': 0.3 },
    achieved: { concurrency: 10, throughput: 120, samples: 7200, failed: 36 },
  };
  const runWorse: Report = {
    ...runNewer,
    run_id: 10,
    started_at: '2026-09-05T10:00:00Z',
    error_rate: 0.03,
    latency: { '50': 0.06, '95': 0.25, '99': 0.4 },
    achieved: { concurrency: 10, throughput: 90, samples: 5400, failed: 162 },
  };

  it('renders three run columns from ?runs=a,b,c off the compare endpoint', async () => {
    await renderCompare({
      url: '/executions/5/compare?runs=9,8,10',
      reports: [runNewer, runBetter, runWorse],
      compare: () => json([runNewer, runBetter, runWorse]),
    });

    // Three slots, in query order.
    expect(selectValue('select-run-a')).toBe('9');
    expect(selectValue('select-run-b')).toBe('8');
    expect(selectValue('select-run-c')).toBe('10');

    // The table carries all three run columns -- a baseline plus one
    // value/delta pair per candidate.
    const head = container!.querySelector('[data-testid="delta-table"] thead')!;
    for (const runId of [9, 8, 10]) {
      expect(head.textContent).toContain(`Run #${runId}`);
    }
    const ths = Array.from(head.querySelectorAll('th'));
    // The regressed chip is part of run 10's header text ("Run #10" plus
    // the chip's own word) -- that is the per-column design, not noise.
    // Phase 75: each candidate carries an abs and a pct delta column.
    expect(ths.map((th) => th.textContent!.trim())).toEqual([
      'Metric', 'Run #9', 'Run #8', 'Δ abs vs #9', 'Δ % vs #9', 'Run #10regressed', 'Δ abs vs #9', 'Δ % vs #9',
    ]);
    // Eight data cells per row: the label, the baseline value, then a
    // value/abs/pct trio per candidate.
    const p50cells = Array.from(container!.querySelectorAll('tr[data-metric="p50"] td'));
    expect(p50cells).toHaveLength(8);
    // Values read candidate-against-baseline off the compare payload's
    // request order: 8 is better (green, 0.035 vs 0.04 = -12.5%), 10 worse
    // (red, 0.06 vs 0.04 = +50%).
    expect(p50cells[2].textContent).toBe('35.0 ms');
    expect(p50cells[3].textContent).toBe('▼ -5.0 ms');
    expect(p50cells[4].textContent).toBe('▼ -12.5%');
    expect(p50cells[4].className).toContain('text-emerald-600');
    expect(p50cells[5].textContent).toBe('60.0 ms');
    expect(p50cells[6].textContent).toBe('▲ +20.0 ms');
    expect(p50cells[7].textContent).toBe('▲ +50.0%');
    expect(p50cells[7].className).toContain('text-red-600');
  });

  it('chips only the candidate columns that regressed against the baseline', async () => {
    await renderCompare({
      url: '/executions/5/compare?runs=9,8,10',
      reports: [runNewer, runBetter, runWorse],
      compare: () => json([runNewer, runBetter, runWorse]),
    });

    // Exactly one chip, on run 10's column, naming its baseline.
    const chips = Array.from(container!.querySelectorAll('[data-testid="compare-regression-chip"]'));
    expect(chips).toHaveLength(1);
    expect(chips[0].getAttribute('data-run-id')).toBe('10');
    expect(chips[0].textContent).toBe('regressed');
    expect(chips[0].getAttribute('title')).toContain('vs run #9');
    // The healthy candidate's column header carries no chip.
    expect(container!.querySelector('th[data-run-id="8"] [data-testid="compare-regression-chip"]')).toBeNull();
  });

  it('falls back to the reports list when the compare endpoint is down', async () => {
    await renderCompare({ url: '/executions/5/compare?runs=9,8,10', reports: [runNewer, runBetter, runWorse] });

    // The batch endpoint's failure costs the page nothing: same three
    // columns, same deltas, computed from the already-loaded list.
    const p50cells = Array.from(container!.querySelectorAll('tr[data-metric="p50"] td'));
    expect(p50cells).toHaveLength(8);
    expect(p50cells[7].textContent).toBe('▲ +50.0%');
    expect(container!.querySelector('[data-testid="compare-regression-chip"]')).not.toBeNull();
  });

  it('extends the selection via Add run, newest unselected run first', async () => {
    // Most-recent-first, matching the wire: 10 (09-05), 9 (09-04), 8 (09-03).
    await renderCompare({ reports: [runWorse, runNewer, runBetter] });

    // Default selection: oldest baseline, newest candidate.
    expect(selectValue('select-run-a')).toBe('8');
    expect(selectValue('select-run-b')).toBe('10');
    expect(container!.querySelector('[data-testid="select-run-c"]')).toBeNull();

    const add = container!.querySelector('[data-testid="add-run"]') as HTMLButtonElement;
    expect(add).not.toBeNull();
    await act(async () => {
      add.click();
    });
    // Run 9 is the only unselected run; the third slot appears carrying it.
    expect(selectValue('select-run-c')).toBe('9');
    expect(container!.querySelector('[data-testid="remove-run-2"]')).not.toBeNull();

    const remove = container!.querySelector('[data-testid="remove-run-2"]') as HTMLButtonElement;
    await act(async () => {
      remove.click();
    });
    expect(container!.querySelector('[data-testid="select-run-c"]')).toBeNull();
  });

  // The two-run spelling the page has taken since it existed keeps working
  // against the batch endpoint: ?runs=a,b still preselects exactly two
  // slots, baseline first.
  it('keeps the two-run ?runs=a,b URL working against the compare endpoint', async () => {
    await renderCompare({
      url: '/executions/5/compare?runs=9,8',
      compare: () => json([runNewer, runOlder]),
    });

    expect(selectValue('select-run-a')).toBe('9');
    expect(selectValue('select-run-b')).toBe('8');
    expect(container!.querySelector('[data-testid="select-run-c"]')).toBeNull();
    const head = container!.querySelector('[data-testid="delta-table"] thead')!;
    expect(head.textContent).toContain('Run #9');
    expect(head.textContent).toContain('Run #8');
    // One value/abs/pct trio -- five cells for the two-run comparison.
    expect(container!.querySelectorAll('tr[data-metric="p50"] td')).toHaveLength(5);
  });
});

// Phase 75: "vs baseline" mode -- an explicit radio'd baseline plus
// checkbox'd comparison runs. The deltas stay client-side (same math as
// slots mode); the batch fetch is told which run is the baseline via
// baseline_run_id, which only orders the payload.
describe('RunCompare vs-baseline mode (phase 75)', () => {
  it('toggles to the run list and radios the baseline, carrying the selection over', async () => {
    await renderCompare();

    // Slots by default; the toggle swaps the pickers for the run list.
    expect(container!.querySelector('[data-testid="select-run-a"]')).not.toBeNull();
    const toggle = container!.querySelector('[data-testid="mode-vs-baseline"]') as HTMLButtonElement;
    expect(toggle.getAttribute('aria-pressed')).toBe('false');
    await act(async () => {
      toggle.click();
    });
    expect(container!.querySelector('[data-testid="select-run-a"]')).toBeNull();
    expect(toggle.getAttribute('aria-pressed')).toBe('true');
    const list = container!.querySelector('[data-testid="baseline-run-list"]');
    expect(list).not.toBeNull();

    // The loaded state carried over: oldest run 8 radio'd, newest run 9
    // checked -- mirroring the slots' A=8, B=9 default.
    expect((list!.querySelector('[data-testid="baseline-radio-8"]') as HTMLInputElement).checked).toBe(true);
    expect((list!.querySelector('[data-testid="compare-check-9"]') as HTMLInputElement).checked).toBe(true);
    // The table stands on the carried-over selection.
    expect(container!.querySelector('[data-testid="delta-table"]')).not.toBeNull();

    // Re-point the baseline at run 9: run 9's own checkbox goes disabled
    // (and unchecked -- it cannot compare against itself), and after run 8
    // is checked as the comparison the table re-reads 8 against 9.
    const radio9 = list!.querySelector('[data-testid="baseline-radio-9"]') as HTMLInputElement;
    await act(async () => {
      radio9.click();
    });
    await act(async () => {});
    expect((list!.querySelector('[data-testid="compare-check-9"]') as HTMLInputElement).disabled).toBe(true);
    expect((list!.querySelector('[data-testid="compare-check-9"]') as HTMLInputElement).checked).toBe(false);
    expect((list!.querySelector('[data-testid="compare-check-8"]') as HTMLInputElement).disabled).toBe(false);
    // No comparison checked: the table stands down until one is.
    expect(container!.querySelector('[data-testid="delta-table"]')).toBeNull();
    await act(async () => {
      (list!.querySelector('[data-testid="compare-check-8"]') as HTMLInputElement).click();
    });
    await act(async () => {});
    const head = container!.querySelector('[data-testid="delta-table"] thead')!;
    expect(head.textContent).toContain('Run #9');
    expect(head.textContent).toContain('Run #8');
  });

  it('badges a regressed comparison and passes baseline_run_id to the batch fetch', async () => {
    const runWorse: Report = {
      ...runNewer,
      run_id: 10,
      started_at: '2026-09-05T10:00:00Z',
      error_rate: 0.03,
      latency: { '50': 0.06, '95': 0.25, '99': 0.4 },
      achieved: { concurrency: 10, throughput: 90, samples: 5400, failed: 162 },
    };
    const seen: string[] = [];
    // Most-recent-first: 10 (09-05), 9 (09-04), 8 (09-03) -- so the default
    // selection is baseline 8 (oldest) with run 10 (newest) checked.
    await renderCompare({
      reports: [runWorse, runNewer, runOlder],
      compare: () => json([runOlder, runNewer, runWorse]),
      record: seen,
    });

    // Toggle to baseline mode: the carried selection stands (baseline 8,
    // comparison 10) and the batch fetch re-fires naming the baseline.
    await act(async () => {
      (container!.querySelector('[data-testid="mode-vs-baseline"]') as HTMLButtonElement).click();
    });
    await act(async () => {});

    // The batch fetch named the baseline: ordering only, ids unchanged.
    const compareUrls = seen.filter((u) => u.includes('/api/runs/compare'));
    expect(compareUrls[compareUrls.length - 1]).toBe(
      '/api/runs/compare?run_ids[]=8&run_ids[]=10&baseline_run_id=8'
    );

    // The regressed candidate's column carries the phase-59 badge, named
    // against the radio'd baseline.
    const chip = container!.querySelector('[data-testid="compare-regression-chip"]') as HTMLElement;
    expect(chip.getAttribute('data-run-id')).toBe('10');
    expect(chip.getAttribute('title')).toContain('vs run #8');
  });

  it('lands in baseline mode from ?mode=vs-baseline and honors ?runs= baseline-first', async () => {
    await renderCompare({ url: '/executions/5/compare?mode=vs-baseline' });
    expect(container!.querySelector('[data-testid="baseline-run-list"]')).not.toBeNull();
    // Default carry: oldest run 8 is the baseline, newest 9 the comparison.
    expect((container!.querySelector('[data-testid="baseline-radio-8"]') as HTMLInputElement).checked).toBe(true);
    expect((container!.querySelector('[data-testid="compare-check-9"]') as HTMLInputElement).checked).toBe(true);
    expect(container!.querySelector('[data-testid="delta-table"]')).not.toBeNull();

    // ?runs=a,b keeps its long-standing meaning -- first id is the baseline
    // -- in this mode too: 9 radios the baseline, 8 the comparison.
    await renderCompare({
      url: '/executions/5/compare?mode=vs-baseline&runs=9,8',
      compare: () => json([runNewer, runOlder]),
    });
    expect((container!.querySelector('[data-testid="baseline-radio-9"]') as HTMLInputElement).checked).toBe(true);
    expect((container!.querySelector('[data-testid="compare-check-8"]') as HTMLInputElement).checked).toBe(true);
  });

  it('toggling back to slots restores the baseline-first selection', async () => {
    await renderCompare();
    await act(async () => {
      (container!.querySelector('[data-testid="mode-vs-baseline"]') as HTMLButtonElement).click();
    });
    await act(async () => {
      (container!.querySelector('[data-testid="mode-slots"]') as HTMLButtonElement).click();
    });
    expect(selectValue('select-run-a')).toBe('8');
    expect(selectValue('select-run-b')).toBe('9');
    expect(container!.querySelector('[data-testid="delta-table"]')).not.toBeNull();
  });
});

// Phase 52: the breadcrumb trail -- "Scenarios / #5 / Compare" (the list
// root moved to /scenarios in phase 67b). Two ancestor links, the current
// page as aria-current text, chevrons between.
describe('RunCompare breadcrumbs (phase 52)', () => {
  it('renders the trail with two links and aria-current on the last item', async () => {
    await renderCompare();

    const nav = container!.querySelector('nav[aria-label="breadcrumb"]');
    expect(nav).not.toBeNull();
    const items = Array.from(nav!.querySelectorAll('li'));
    expect(items.map((li) => li.textContent?.trim())).toEqual(['Scenarios', '#5', 'Compare']);
    // Ancestors are links; the current page is not.
    const links = Array.from(nav!.querySelectorAll('a')).map((a) => a.getAttribute('href'));
    expect(links).toEqual(['/scenarios', '/executions/5']);
    const current = nav!.querySelector('[aria-current="page"]');
    expect(current).not.toBeNull();
    expect(current!.textContent).toBe('Compare');
    expect(current!.tagName).toBe('SPAN');
    // A chevron separator between each pair, hidden from AT.
    expect(nav!.querySelectorAll('svg[aria-hidden="true"]').length).toBe(2);
  });
});
