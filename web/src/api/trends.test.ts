import { afterEach, describe, expect, it, vi } from 'vitest';
import { getErrorSignatures, getExecutionTrend } from './trends';

function jsonResponse(body: string): Response {
  return new Response(body, { status: 200, headers: { 'Content-Type': 'application/json' } });
}

describe('trends api', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe('getExecutionTrend', () => {
    it('GETs the trend route without a query when no limit is given', async () => {
      let seenUrl = '';
      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        seenUrl = String(input);
        return jsonResponse(JSON.stringify({ execution_id: 3, points: [] }));
      });
      vi.stubGlobal('fetch', fetchMock);

      const got = await getExecutionTrend(3);

      expect(seenUrl).toBe('/api/executions/3/trend');
      expect(got.execution_id).toBe(3);
      expect(got.points).toEqual([]);
    });

    it('appends limit as a query parameter and passes points through unchanged', async () => {
      let seenUrl = '';
      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        seenUrl = String(input);
        return jsonResponse(
          JSON.stringify({
            execution_id: 3,
            points: [
              {
                run_id: 9,
                outcome: 'passed',
                achieved_throughput: 100.5,
                requested_throughput: 120,
                error_rate: 0.01,
                p50: 0.05,
                p90: 0.1,
                p95: 0.2,
                p99: 0.4,
                hit_target_qps: false,
                has_comparable_predecessor: true,
                regressed: true,
              },
            ],
          })
        );
      });
      vi.stubGlobal('fetch', fetchMock);

      const got = await getExecutionTrend(3, 10);

      expect(seenUrl).toBe('/api/executions/3/trend?limit=10');
      expect(got.points).toHaveLength(1);
      expect(got.points[0]).toEqual({
        run_id: 9,
        outcome: 'passed',
        achieved_throughput: 100.5,
        requested_throughput: 120,
        error_rate: 0.01,
        p50: 0.05,
        p90: 0.1,
        p95: 0.2,
        p99: 0.4,
        hit_target_qps: false,
        has_comparable_predecessor: true,
        regressed: true,
      });
    });
    it('passes points through unchanged when the regressed key is absent (omitempty drops false on stable rows, phase 59)', async () => {
      const fetchMock = vi.fn(async () =>
        jsonResponse(
          JSON.stringify({
            execution_id: 3,
            points: [
              {
                run_id: 4,
                outcome: 'passed',
                achieved_throughput: 100,
                requested_throughput: 100,
                error_rate: 0,
                p50: 0.05,
                p90: 0.1,
                p95: 0.2,
                p99: 0.4,
                hit_target_qps: true,
                has_comparable_predecessor: true,
              },
            ],
          })
        )
      );
      vi.stubGlobal('fetch', fetchMock);

      const got = await getExecutionTrend(3);

      expect(got.points).toHaveLength(1);
      // The client type keeps regressed optional: absent stays absent.
      expect(got.points[0].regressed).toBeUndefined();
    });
  });

  describe('getErrorSignatures', () => {
    it('GETs the route bare (server-side default grouping is label)', async () => {
      let seenUrl = '';
      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        seenUrl = String(input);
        return jsonResponse(JSON.stringify({ execution_id: 3, grouped_by: 'label', groups: [] }));
      });
      vi.stubGlobal('fetch', fetchMock);

      await getErrorSignatures(3);

      expect(seenUrl).toBe('/api/executions/3/error-signatures');
    });

    it('passes by=code only for the code axis and passes groups through unchanged', async () => {
      let seenUrl = '';
      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        seenUrl = String(input);
        return jsonResponse(
          JSON.stringify({
            execution_id: 3,
            grouped_by: 'response_code',
            groups: [
              {
                key: '500',
                total_count: 40,
                rows: [
                  { label: 'GET /orders', response_code: '500', side: 'target', total_count: 30, run_count: 3 },
                  { label: 'GET /cart', response_code: '500', side: 'engine', total_count: 10, run_count: 1 },
                ],
              },
            ],
          })
        );
      });
      vi.stubGlobal('fetch', fetchMock);

      const got = await getErrorSignatures(3, 'code');

      // ?by=code on the wire, but grouped_by echoes the Go constant
      // ("response_code") -- the handler maps the short query value.
      expect(seenUrl).toBe('/api/executions/3/error-signatures?by=code');
      expect(got.grouped_by).toBe('response_code');
      expect(got.groups[0].total_count).toBe(40);
      expect(got.groups[0].rows).toHaveLength(2);
      expect(got.groups[0].rows[0].run_count).toBe(3);
    });
  });
});
