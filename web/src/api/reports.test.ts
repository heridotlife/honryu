import { afterEach, describe, expect, it, vi } from 'vitest';
import { fetchShared, getShardConfig, listExecutionReports, listShares, revokeShare, shareLinkUrl, shareRun, shardObjectUrl } from './reports';

describe('listExecutionReports', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  // The backend encodes ListReports' nil slice as JSON null, not [], for an
  // execution with no reports yet (confirmed against a real cmd/api: `GET
  // /api/executions/1/reports` on a fresh fake store returns "null"). A
  // caller treating the result as an array (e.g. `.length`) would otherwise
  // throw at runtime.
  it('normalizes a null response body to an empty array', async () => {
    const fetchMock = vi.fn(async () => new Response('null', { status: 200, headers: { 'Content-Type': 'application/json' } }));
    vi.stubGlobal('fetch', fetchMock);

    const got = await listExecutionReports(1);

    expect(got).toEqual([]);
  });

  it('passes through a real report array unchanged', async () => {
    const fetchMock = vi.fn(
      async () =>
        new Response(JSON.stringify([{ run_id: 1, outcome: 'passed' }]), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
    );
    vi.stubGlobal('fetch', fetchMock);

    const got = await listExecutionReports(1);

    expect(got).toHaveLength(1);
    expect(got[0].run_id).toBe(1);
  });

  it('appends limit as a query parameter when given', async () => {
    let seenUrl = '';
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      seenUrl = String(input);
      return new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } });
    });
    vi.stubGlobal('fetch', fetchMock);

    await listExecutionReports(1, 10);

    expect(seenUrl).toBe('/api/executions/1/reports?limit=10');
  });
});

describe('shard objects', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('builds config and log URLs on the run/scenario/shard path', () => {
    expect(shardObjectUrl(9, 2, 0, 'config')).toBe('/runs/9/scenarios/2/shards/0/config');
    expect(shardObjectUrl(9, 2, 3, 'log')).toBe('/runs/9/scenarios/2/shards/3/log');
  });

  it('getShardConfig returns the raw text/plain body', async () => {
    let seenUrl = '';
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      seenUrl = String(input);
      return new Response('execution:\n  concurrency: 10', { status: 200, headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
    });
    vi.stubGlobal('fetch', fetchMock);

    const got = await getShardConfig(9, 2, 0);

    expect(seenUrl).toBe('/api/runs/9/scenarios/2/shards/0/config');
    expect(got).toBe('execution:\n  concurrency: 10');
  });
});

describe('share links', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('shareRun POSTs an empty body when no expiry is given, JSON when one is', async () => {
    const calls: Array<{ url: string; init: RequestInit }> = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        calls.push({ url: String(input), init: init ?? {} });
        return new Response(JSON.stringify({ token: 't'.repeat(64), url: `/share/${'t'.repeat(64)}`, expires_at: null }), {
          status: 201,
          headers: { 'Content-Type': 'application/json' },
        });
      })
    );

    await shareRun(9);
    await shareRun(9, 168);

    expect(calls[0].url).toBe('/api/runs/9/share');
    expect(calls[0].init.body).toBeUndefined();
    expect(calls[1].init.body).toBe(JSON.stringify({ expires_in_hours: 168 }));
    expect(new Headers(calls[1].init.headers).get('Content-Type')).toBe('application/json');
  });

  it('listShares normalizes null to [] and revokeShare DELETEs the token path', async () => {
    const urls: string[] = [];
    const methods: string[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        urls.push(String(input));
        methods.push((init?.method as string) ?? 'GET');
        return new Response('null', { status: 200, headers: { 'Content-Type': 'application/json' } });
      })
    );

    await listShares(9);
    expect(await listShares(9)).toEqual([]);
    await revokeShare(9, 'abc');

    expect(urls).toEqual(['/api/runs/9/share', '/api/runs/9/share', '/api/runs/9/share/abc']);
    expect(methods).toEqual(['GET', 'GET', 'DELETE']);
  });

  it('fetchShared reads the public path and normalizes null verdict arrays', async () => {
    let seenUrl = '';
    vi.stubGlobal(
      'fetch',
      async (input: RequestInfo | URL) => {
        seenUrl = String(input);
        return new Response(JSON.stringify({ run_id: 9, started_at: 'x', ended_at: 'y', outcome: 'passed' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }
    );

    const got = await fetchShared('tok');

    expect(seenUrl).toBe('/api/share/tok');
    expect(got.criteria).toEqual([]);
    expect(got.failing_criteria).toEqual([]);
  });

  it('shareLinkUrl builds the absolute hand-out URL', () => {
    expect(shareLinkUrl('abc')).toBe(`${window.location.origin}/share/abc`);
  });
});
