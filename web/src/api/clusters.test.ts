import { afterEach, describe, expect, it, vi } from 'vitest';
import * as clusters from './clusters';
import { listClusters } from './clusters';
import { getClusters } from './generated';

describe('listClusters', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  // Phase 64 migration pin: the module must keep re-exporting the generated
  // client's fetcher, not grow a parallel hand-rolled copy.
  it('re-exports the generated client cluster list fetcher', () => {
    expect(clusters.listClusters).toBe(getClusters);
  });

  it('GETs the registry list and passes entries through unchanged', async () => {
    let seenUrl = '';
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      seenUrl = String(input);
      return new Response(
        JSON.stringify([
          {
            name: 'dogfood-byoc',
            api_url: 'https://k8s.example.com',
            ingest_url: 'https://honryu.example.com/api/ingest',
            sidecar_image: 'registry.example.com/honryu/sidecar:latest',
            namespace: 'honryu-engines',
            secret_ref: 'honryu-cluster-dogfood-byoc',
            origin: 'byoc',
            created_by: 'admin@example.com',
            created_time: '2026-08-17T00:00:00Z',
          },
          {
            name: 'ops-home',
            api_url: '',
            ingest_url: '',
            sidecar_image: '',
            namespace: 'default',
            secret_ref: '',
            origin: 'operator',
            created_time: '2026-08-16T00:00:00Z',
          },
        ]),
        { status: 200, headers: { 'Content-Type': 'application/json' } }
      );
    });
    vi.stubGlobal('fetch', fetchMock);

    const got = await listClusters();

    expect(seenUrl).toBe('/api/clusters');
    expect(got).toHaveLength(2);
    expect(got[0]).toEqual({
      name: 'dogfood-byoc',
      api_url: 'https://k8s.example.com',
      ingest_url: 'https://honryu.example.com/api/ingest',
      sidecar_image: 'registry.example.com/honryu/sidecar:latest',
      namespace: 'honryu-engines',
      secret_ref: 'honryu-cluster-dogfood-byoc',
      origin: 'byoc',
      created_by: 'admin@example.com',
      created_time: '2026-08-17T00:00:00Z',
    });
    // created_by is omitempty on the wire -- absent for operator rows, not
    // an empty string.
    expect(got[1].created_by).toBeUndefined();
    expect(got[1].origin).toBe('operator');
  });

  it('returns the empty array for an empty registry', async () => {
    const fetchMock = vi.fn(
      async () => new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } })
    );
    vi.stubGlobal('fetch', fetchMock);

    const got = await listClusters();

    expect(got).toEqual([]);
  });
});
