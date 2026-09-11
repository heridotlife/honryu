import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { sortSignatureGroups, TrendSection } from './ReportsTrend';
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
});
