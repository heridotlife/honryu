import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiClient, ApiError, errorDetails } from './client';

describe('ApiClient', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('GETs against baseUrl and returns the decoded JSON body', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      expect(String(input)).toBe('/mock-api/scenarios/1');
      return new Response(JSON.stringify({ id: 1, name: 'checkout' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    });
    vi.stubGlobal('fetch', fetchMock);

    const client = new ApiClient({ baseUrl: '/mock-api', getToken: () => null });
    const got = await client.get<{ id: number; name: string }>('/scenarios/1');

    expect(got).toEqual({ id: 1, name: 'checkout' });
    expect(fetchMock).toHaveBeenCalledOnce();
  });

  it('attaches a bearer token when one is available', async () => {
    let seenAuth: string | null = null;
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      seenAuth = new Headers(init?.headers).get('Authorization');
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } });
    });
    vi.stubGlobal('fetch', fetchMock);

    const client = new ApiClient({ baseUrl: '/mock-api', getToken: () => 'tok123' });
    await client.get('/whoami');

    expect(seenAuth).toBe('Bearer tok123');
  });

  it('omits the Authorization header when there is no token', async () => {
    let seenAuth: string | null = 'unset';
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      seenAuth = new Headers(init?.headers).get('Authorization');
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } });
    });
    vi.stubGlobal('fetch', fetchMock);

    const client = new ApiClient({ baseUrl: '/mock-api', getToken: () => null });
    await client.get('/whoami');

    expect(seenAuth).toBeNull();
  });

  it('surfaces the backend error envelope as a typed ApiError', async () => {
    const fetchMock = vi.fn(async () => {
      return new Response(JSON.stringify({ message: 'execution not found' }), {
        status: 404,
        headers: { 'Content-Type': 'application/json' },
      });
    });
    vi.stubGlobal('fetch', fetchMock);

    const client = new ApiClient({ baseUrl: '/mock-api', getToken: () => null });
    await expect(client.get('/executions/999')).rejects.toMatchObject({
      name: 'ApiError',
      status: 404,
      message: 'execution not found',
    });
  });

  it('falls back to statusText when the error body is not the expected shape', async () => {
    const fetchMock = vi.fn(async () => new Response('not json', { status: 500, statusText: 'Internal Server Error' }));
    vi.stubGlobal('fetch', fetchMock);

    const client = new ApiClient({ baseUrl: '/mock-api', getToken: () => null });
    let caught: unknown;
    try {
      await client.get('/boom');
    } catch (err) {
      caught = err;
    }
    expect(caught).toBeInstanceOf(ApiError);
    expect((caught as ApiError).status).toBe(500);
    expect((caught as ApiError).message).toBe('Internal Server Error');
  });

  it('returns undefined for a 204 No Content response', async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }));
    vi.stubGlobal('fetch', fetchMock);

    const client = new ApiClient({ baseUrl: '/mock-api', getToken: () => null });
    const got = await client.get('/nothing');
    expect(got).toBeUndefined();
  });

  it('POSTs a form-urlencoded body', async () => {
    let seenMethod = '';
    let seenContentType: string | null = null;
    let seenBody = '';
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      seenMethod = init?.method ?? '';
      seenContentType = new Headers(init?.headers).get('Content-Type');
      seenBody = String(init?.body);
      return new Response(JSON.stringify({ ok: true }), { status: 201, headers: { 'Content-Type': 'application/json' } });
    });
    vi.stubGlobal('fetch', fetchMock);

    const client = new ApiClient({ baseUrl: '/mock-api', getToken: () => null });
    const form = new URLSearchParams();
    form.set('name', 'checkout');
    const got = await client.post<{ ok: boolean }>('/scenarios', form);

    expect(seenMethod).toBe('POST');
    expect(seenContentType).toBe('application/x-www-form-urlencoded');
    expect(seenBody).toBe('name=checkout');
    expect(got).toEqual({ ok: true });
  });

  it('text() GETs and returns the raw text/plain body', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      expect(String(input)).toBe('/mock-api/runs/1/scenarios/2/shards/0/config');
      return new Response('concurrency: 10', { status: 200, headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
    });
    vi.stubGlobal('fetch', fetchMock);

    const client = new ApiClient({ baseUrl: '/mock-api', getToken: () => null });
    const got = await client.text('/runs/1/scenarios/2/shards/0/config');

    expect(got).toBe('concurrency: 10');
  });

  it('text() surfaces the error envelope like JSON requests do', async () => {
    const fetchMock = vi.fn(
      async () => new Response(JSON.stringify({ message: 'shard not found' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
    );
    vi.stubGlobal('fetch', fetchMock);

    const client = new ApiClient({ baseUrl: '/mock-api', getToken: () => null });
    await expect(client.text('/runs/1/scenarios/2/shards/9/config')).rejects.toMatchObject({
      name: 'ApiError',
      status: 404,
      message: 'shard not found',
    });
  });
});

describe('putRaw', () => {
  it('PUTs the raw body with the given content type and bearer token', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    const client = new ApiClient({ baseUrl: 'http://x', getToken: () => 'tok' });
    await client.putRaw('/scenarios/1/requests', 'text/yaml', 'a: 1\n');
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('http://x/scenarios/1/requests');
    expect(init.method).toBe('PUT');
    expect(init.headers.get('Content-Type')).toBe('text/yaml');
    expect(init.headers.get('Authorization')).toBe('Bearer tok');
    expect(init.body).toBe('a: 1\n');
  });

  it('throws ApiError with the server message on failure', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: { message: 'invalid fragment' } }), { status: 422 }),
      ),
    );
    const client = new ApiClient({ baseUrl: 'http://x', getToken: () => null });
    const err = await client.putRaw('/scenarios/1/requests', 'text/yaml', 'a: 1\n').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(422);
  });
});

// Phase 24's structured error envelope: {"message": ..., "details": {...}}
// rides on select errors (429 quota, 409 engines-finished). errorDetails is
// the single reader pages use -- details when present, null otherwise.
describe('errorDetails', () => {
  it('returns the details object of a 429 quota envelope, end to end through the client', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            message: 'reservation would exceed tenant quota',
            details: { tenant_id: 1, ceiling: 1, used: 0, requested: 2, hint: 'PUT /api/tenants/{tenant_id}/quota ceiling=1' },
          }),
          { status: 429, headers: { 'Content-Type': 'application/json' } },
        ),
      ),
    );
    const client = new ApiClient({ baseUrl: '/mock-api', getToken: () => null });
    const err = await client.post('/executions/9/trigger', new URLSearchParams()).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(errorDetails(err)).toEqual({
      tenant_id: 1,
      ceiling: 1,
      used: 0,
      requested: 2,
      hint: 'PUT /api/tenants/{tenant_id}/quota ceiling=1',
    });
  });

  it('returns null for a message-only envelope', () => {
    expect(errorDetails(new ApiError(404, 'not found', { message: 'not found' }))).toBeNull();
  });

  it('returns null when the body never parsed (no data) and for non-ApiError errors', () => {
    expect(errorDetails(new ApiError(500, 'Internal Server Error'))).toBeNull();
    expect(errorDetails(new Error('boom'))).toBeNull();
    expect(errorDetails('reservation would exceed tenant quota')).toBeNull();
    expect(errorDetails(null)).toBeNull();
  });

  it('returns null when details is present but not an object', () => {
    expect(errorDetails(new ApiError(400, 'bad', { message: 'bad', details: 'oops' }))).toBeNull();
  });
});

describe('errorDetails through wrapper errors', () => {
  it("reaches the envelope through a wrapper's cause chain (NewTest's stepError)", () => {
    const api = new ApiError(429, 'reservation would exceed tenant quota', {
      message: 'reservation would exceed tenant quota',
      details: { ceiling: 1, hint: 'PUT /api/tenants/{tenant_id}/quota ceiling=1' },
    });
    const wrapped = new Error('Step "save load config" failed: reservation would exceed tenant quota', { cause: api });
    expect(errorDetails(wrapped)).toEqual({ ceiling: 1, hint: 'PUT /api/tenants/{tenant_id}/quota ceiling=1' });
  });

  it('stays null for a cause chain with no ApiError in it', () => {
    expect(errorDetails(new Error('outer', { cause: new Error('inner') }))).toBeNull();
  });
});

// Phase 49: a session cookie expiring mid-use used to leave pages rendering
// lying empty states ("No executions visible to you yet."). Now any API 401
// outside the session surface drops local session state and hard-redirects
// to "/" exactly once, so the profile picker boots. The session surface's
// own 401s (/me, /session, /session/profiles) are the picker's normal
// unauthenticated state and must never trigger the redirect -- otherwise
// booting the picker would loop.
describe('ApiClient dead-session redirect (phase 49)', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    localStorage.removeItem('honryu_token');
  });

  /** Swaps window.location for a stub whose assign() is observable. The
   * spread keeps href etc. working for the rest of the worker. */
  const stubLocation = (): ReturnType<typeof vi.fn> => {
    const assign = vi.fn();
    Object.defineProperty(window, 'location', {
      value: { ...window.location, assign },
      writable: true,
      configurable: true,
    });
    return assign;
  };

  const fetch401 = (): void => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify({ message: 'session expired' }), { status: 401 })),
    );
  };

  it('a 401 from a list call clears the local token and hard-redirects to / exactly once', async () => {
    localStorage.setItem('honryu_token', 'stale-token');
    const assign = stubLocation();
    fetch401();

    const client = new ApiClient({ baseUrl: '/api', getToken: () => localStorage.getItem('honryu_token') });
    await expect(client.get('/executions')).rejects.toBeInstanceOf(ApiError);

    expect(assign).toHaveBeenCalledTimes(1);
    expect(assign).toHaveBeenCalledWith('/');
    expect(localStorage.getItem('honryu_token')).toBeNull();

    // The burst case: several in-flight calls 401 together (a page polling
    // status, trend, and reports at once) -- still exactly one redirect.
    await expect(client.get('/executions/5/status')).rejects.toBeInstanceOf(ApiError);
    await expect(client.get('/executions/5/trend')).rejects.toBeInstanceOf(ApiError);
    expect(assign).toHaveBeenCalledTimes(1);
  });

  it('a burst of 401s on an injected spy also fires exactly once', async () => {
    fetch401();
    const onUnauthorized = vi.fn();
    const client = new ApiClient({ baseUrl: '/api', getToken: () => null, onUnauthorized });

    await expect(client.get('/reports')).rejects.toBeInstanceOf(ApiError);
    await expect(client.post('/executions/5/trigger', new URLSearchParams())).rejects.toBeInstanceOf(ApiError);
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });

  it('the session surface never triggers the redirect (the picker must be able to boot)', async () => {
    fetch401();
    const onUnauthorized = vi.fn();
    const client = new ApiClient({ baseUrl: '/api', getToken: () => null, onUnauthorized });

    // GET /me (identity), POST /session (the picker's login -- createSession
    // rides request/send like everything else), DELETE /session (logout),
    // and GET /session/profiles (the persona list).
    await expect(client.get('/me')).rejects.toBeInstanceOf(ApiError);
    await expect(client.post('/session', new URLSearchParams())).rejects.toBeInstanceOf(ApiError);
    await expect(client.request('/session', { method: 'DELETE' })).rejects.toBeInstanceOf(ApiError);
    await expect(client.get('/session/profiles')).rejects.toBeInstanceOf(ApiError);
    expect(onUnauthorized).not.toHaveBeenCalled();
  });
});
