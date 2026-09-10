import { afterEach, describe, expect, it, vi } from 'vitest';
import { createWebhook, deleteWebhook, listWebhooks, setWebhookEnabled } from './webhooks';

describe('webhooks api', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('listWebhooks GETs the project-scoped route', async () => {
    let seenUrl = '';
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      seenUrl = String(input);
      return new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } });
    });
    vi.stubGlobal('fetch', fetchMock);

    await listWebhooks(7);

    expect(seenUrl).toBe('/api/projects/7/webhooks');
  });

  it('createWebhook posts a form-encoded body with the optional secret', async () => {
    let seenUrl = '';
    let seenMethod = '';
    let seenContentType: string | null = null;
    let seenBody = '';
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      seenUrl = String(input);
      seenMethod = init?.method ?? '';
      seenContentType = new Headers(init?.headers).get('Content-Type');
      seenBody = String(init?.body);
      return new Response(
        JSON.stringify({
          id: 3,
          url: 'https://hooks.example.com/runs',
          has_secret: true,
          enabled: true,
          created_by: 'op',
          created_time: '2026-08-07T00:00:00Z',
        }),
        { status: 201, headers: { 'Content-Type': 'application/json' } }
      );
    });
    vi.stubGlobal('fetch', fetchMock);

    const got = await createWebhook(7, 'https://hooks.example.com/runs', 's3cr3t');

    expect(seenUrl).toBe('/api/projects/7/webhooks');
    expect(seenMethod).toBe('POST');
    expect(seenContentType).toBe('application/x-www-form-urlencoded');
    const body = new URLSearchParams(seenBody);
    expect(body.get('url')).toBe('https://hooks.example.com/runs');
    expect(body.get('secret')).toBe('s3cr3t');
    expect(got.id).toBe(3);
    expect(got.has_secret).toBe(true);
  });

  it('createWebhook omits the secret field when none is given', async () => {
    let seenBody = '';
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      seenBody = String(init?.body);
      return new Response('{}', { status: 201, headers: { 'Content-Type': 'application/json' } });
    });
    vi.stubGlobal('fetch', fetchMock);

    await createWebhook(7, 'https://hooks.example.com/runs');

    const body = new URLSearchParams(seenBody);
    expect(body.get('url')).toBe('https://hooks.example.com/runs');
    expect(body.has('secret')).toBe(false);
  });

  it('deleteWebhook DELETEs the project-scoped webhook and tolerates the empty 204', async () => {
    let seenUrl = '';
    let seenMethod = '';
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      seenUrl = String(input);
      seenMethod = init?.method ?? '';
      return new Response(null, { status: 204 });
    });
    vi.stubGlobal('fetch', fetchMock);

    const got = await deleteWebhook(7, 3);

    expect(seenUrl).toBe('/api/projects/7/webhooks/3');
    expect(seenMethod).toBe('DELETE');
    expect(got).toBeUndefined();
  });

  it('setWebhookEnabled PUTs a form-encoded boolean', async () => {
    let seenUrl = '';
    let seenMethod = '';
    let seenContentType: string | null = null;
    let seenBody = '';
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      seenUrl = String(input);
      seenMethod = init?.method ?? '';
      seenContentType = new Headers(init?.headers).get('Content-Type');
      seenBody = String(init?.body);
      return new Response(JSON.stringify({ message: 'updated' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    });
    vi.stubGlobal('fetch', fetchMock);

    await setWebhookEnabled(7, 3, false);

    expect(seenUrl).toBe('/api/projects/7/webhooks/3/enabled');
    expect(seenMethod).toBe('PUT');
    expect(seenContentType).toBe('application/x-www-form-urlencoded');
    expect(new URLSearchParams(seenBody).get('enabled')).toBe('false');
  });

  it('surfaces the backend rejection message (https-only) as an ApiError', async () => {
    const fetchMock = vi.fn(
      async () =>
        new Response(JSON.stringify({ message: 'url must be https' }), {
          status: 400,
          headers: { 'Content-Type': 'application/json' },
        })
    );
    vi.stubGlobal('fetch', fetchMock);

    await expect(createWebhook(7, 'http://cleartext.example.com/hook')).rejects.toThrow('url must be https');
  });
});
