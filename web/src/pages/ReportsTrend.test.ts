import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { regressionDetail, sortSignatureGroups, TrendSection } from './ReportsTrend';
import type { SignatureBreakdown } from '../api/trends';
import type { TrendPoint } from '../api/trends';

/** Live roots, for afterEach unmount (newest last). */
const roots: Root[] = [];

function makeGroup(overrides: Partial<SignatureBreakdown> = {}): SignatureBreakdown {
  return { key: 'GET /', total_count: 10, rows: [], ...overrides };
}

describe('sortSignatureGroups', () => {
  it('orders groups biggest-total-first', () => {
    const got = sortSignatureGroups([
      makeGroup({ key: 'GET /orders', total_count: 5 }),
      makeGroup({ key: 'GET /', total_count: 40 }),
      makeGroup({ key: 'POST /cart', total_count: 12 }),
    ]);

    expect(got.map((g) => g.key)).toEqual(['GET /', 'POST /cart', 'GET /orders']);
  });

  it('breaks total ties by key alphabetically so rendering is deterministic', () => {
    const got = sortSignatureGroups([
      makeGroup({ key: 'b', total_count: 10 }),
      makeGroup({ key: 'a', total_count: 10 }),
    ]);

    expect(got.map((g) => g.key)).toEqual(['a', 'b']);
  });

  it('orders rows within a group biggest-first', () => {
    const got = sortSignatureGroups([
      makeGroup({
        rows: [
          { label: 'GET /', response_code: '500', side: 'target', total_count: 3, run_count: 2 },
          { label: 'GET /', response_code: '503', side: 'target', total_count: 9, run_count: 4 },
        ],
      }),
    ]);

    expect(got[0].rows.map((r) => r.response_code)).toEqual(['503', '500']);
  });

  it('does not mutate the input', () => {
    const input = [
      makeGroup({ key: 'b', total_count: 10, rows: [{ label: 'x', side: 'target', total_count: 1, run_count: 1 }] }),
      makeGroup({ key: 'a', total_count: 20 }),
    ];
    const snapshot = JSON.parse(JSON.stringify(input)) as SignatureBreakdown[];

    sortSignatureGroups(input);

    expect(input).toEqual(snapshot);
  });
});

// Phase 49, mounted: the trend table's Requested (req/s) column. The wire
// field (requested_throughput) has always been present and correctly
// mapped -- the audit's "always 0.0" is the honest zero of runs whose load
// profile left stage throughput unset, i.e. unlimited. Targetless runs now
// render "unlimited"; pinned targets still render their number.
(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

describe('TrendSection requested-throughput column (mounted, phase 49)', () => {
  let container: HTMLDivElement | null = null;

  afterEach(() => {
    vi.unstubAllGlobals();
    const root = roots.pop();
    if (root !== undefined && container !== null) {
      act(() => {
        root.unmount();
      });
    }
    container?.remove();
    container = null;
  });

  const point = (over: Partial<TrendPoint>): TrendPoint => ({
    run_id: 1,
    outcome: 'passed',
    achieved_throughput: 95.4,
    requested_throughput: 0,
    error_rate: 0.01,
    p50: 0.05,
    p90: 0.1,
    p95: 0.2,
    p99: 0.4,
    hit_target_qps: true,
    has_comparable_predecessor: false,
    ...over,
  });

  const renderTrend = async (points: TrendPoint[]) => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        if (String(input).endsWith('/api/executions/5/trend')) {
          return new Response(JSON.stringify({ execution_id: 5, points }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          });
        }
        return new Response(JSON.stringify({ message: 'no stub' }), { status: 500 });
      }),
    );
    const root = createRoot(container);
    roots.push(root);
    await act(async () => {
      root.render(createElement(TrendSection, { executionId: 5 }));
    });
    await act(async () => {});
  };

  it('renders unlimited for targetless runs instead of a lying 0.0, and real numbers for pinned targets', async () => {
    await renderTrend([
      point({ run_id: 2, requested_throughput: 0 }),
      point({ run_id: 1, requested_throughput: 250 }),
    ]);

    const rows = Array.from(container!.querySelectorAll('tbody tr'));
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain('unlimited');
    expect(rows[0].textContent).not.toContain('0.0');
    expect(rows[1].textContent).toContain('250.0');
    // Achieved is untouched by this fix.
    expect(rows[0].textContent).toContain('95.4');
  });

  // Phase 59: the regressed chip (a phase-42 verdict rendered here since the
  // trend table landed) is pinned at last -- present on a run that missed
  // its target QPS while its comparable baseline met it, absent from a
  // comparable run that did not. The chip is real text, not a color-only cue.
  it('chips a regressed run and leaves a comparable stable run unchipped (phase 59)', async () => {
    await renderTrend([
      point({ run_id: 2, hit_target_qps: false, has_comparable_predecessor: true, regressed: true }),
      point({ run_id: 1, hit_target_qps: true, has_comparable_predecessor: true, regressed: false }),
    ]);

    const rows = Array.from(container!.querySelectorAll('tbody tr'));
    expect(rows[0].querySelector('[data-testid="trend-regression-chip"]')).not.toBeNull();
    expect(rows[0].textContent).toContain('regressed');
    expect(rows[1].querySelector('[data-testid="trend-regression-chip"]')).toBeNull();
  });

  // The chip's title says WHAT regressed, from fields the trend endpoint
  // already carries: this run's own achieved/requested figures against the
  // 95% request tolerance, plus the baseline fact the verdict itself
  // encodes. No new endpoint data -- see regressionDetail above.
  it('titles the regression chip with the numbers behind the miss (phase 59)', async () => {
    await renderTrend([
      point({
        run_id: 2,
        achieved_throughput: 380,
        requested_throughput: 500,
        hit_target_qps: false,
        has_comparable_predecessor: true,
        regressed: true,
      }),
    ]);

    const chip = container!.querySelector('[data-testid="trend-regression-chip"]') as HTMLElement;
    expect(chip.title).toContain('achieved 380.0 of 500.0 req/s');
    expect(chip.title).toContain('95%');
    expect(chip.title).toContain('baseline run met its target');
  });

  // Landmark: a fully stable trend renders ZERO regression chips anywhere
  // in the table. The row-level absence test above pins one row; this pins
  // the whole table -- guards against a chip-everything regression (e.g. a
  // future edit rendering a pill on every row). Points omit `regressed`
  // entirely, matching the wire's omitempty shape for stable rows.
  it('renders zero regression chips on a fully stable trend (phase 59)', async () => {
    await renderTrend([
      point({ run_id: 3, hit_target_qps: true, has_comparable_predecessor: true }),
      point({ run_id: 2, hit_target_qps: true, has_comparable_predecessor: true }),
      point({ run_id: 1, hit_target_qps: true, has_comparable_predecessor: false }),
    ]);

    expect(container!.querySelectorAll('[data-testid="trend-regression-chip"]')).toHaveLength(0);
    // The table itself still renders all three rows.
    expect(container!.querySelectorAll('tbody tr')).toHaveLength(3);
  });
});

describe('regressionDetail (pure, phase 59)', () => {
  const base: TrendPoint = {
    run_id: 7,
    outcome: 'failed',
    achieved_throughput: 95.4,
    requested_throughput: 100,
    error_rate: 0.02,
    p50: 0.05,
    p90: 0.1,
    p95: 0.2,
    p99: 0.4,
    hit_target_qps: false,
    has_comparable_predecessor: true,
    regressed: true,
  };

  it('explains the miss with the run\'s own figures', () => {
    expect(regressionDetail(base)).toBe(
      'Fell short of target: achieved 95.4 of 100.0 req/s (needs ≥95%); nearest comparable baseline run met its target.'
    );
  });

  it('falls back to the qualitative line when the numbers would not make sense', () => {
    // Not regressed: no verdict to explain.
    expect(regressionDetail({ ...base, regressed: false })).not.toContain('95.4');
    // A regressed point with no requested throughput is unreachable on the
    // current wire (unlimited runs cannot regress); the fallback must not
    // print "of 0.0 req/s" if a future change breaks that invariant.
    expect(regressionDetail({ ...base, requested_throughput: 0 })).toBe(
      'This run fell short of its requested throughput while the nearest comparable baseline run met its own.'
    );
  });
});
