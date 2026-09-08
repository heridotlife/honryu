import { afterEach, describe, expect, it, vi } from 'vitest';
import { listProjects } from './projects';

describe('projects api', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('listProjects GETs /api/projects', async () => {
    let seenUrl = '';
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      seenUrl = String(input);
      return new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } });
    });
    vi.stubGlobal('fetch', fetchMock);

    await listProjects();

    expect(seenUrl).toBe('/api/projects');
  });

  // Go marshals a nil slice as null: a caller with no projects gets null,
  // not [], on the wire -- normalized here so callers never see null.
  it('normalizes a null body to an empty array', async () => {
    const fetchMock = vi.fn(
      async () => new Response('null', { status: 200, headers: { 'Content-Type': 'application/json' } })
    );
    vi.stubGlobal('fetch', fetchMock);

    const got = await listProjects();

    expect(got).toEqual([]);
  });

  it('passes populated project rows through unchanged', async () => {
    const rows = [
      { id: 1, name: 'phase16-live', owner: 'honryu', tenant_id: 1, created_time: '2026-08-01T00:00:00Z' },
      { id: 2, name: 'phase16-sched', owner: 'heri', tenant_id: 1, created_time: '2026-08-02T00:00:00Z' },
    ];
    const fetchMock = vi.fn(
      async () => new Response(JSON.stringify(rows), { status: 200, headers: { 'Content-Type': 'application/json' } })
    );
    vi.stubGlobal('fetch', fetchMock);

    const got = await listProjects();

    expect(got).toEqual(rows);
  });
});
