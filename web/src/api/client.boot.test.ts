// Vitest regression: unauthenticated boot must NOT redirect.
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { ApiClient } from './client';

function stubFetch(statuses: number[]) {
  const calls: string[] = [];
  let i = 0;
  globalThis.fetch = vi.fn(async (url: any) => {
    calls.push(String(url));
    const status = statuses[Math.min(i++, statuses.length - 1)];
    return new Response(JSON.stringify({ message: 'x' }), { status, headers: { 'Content-Type': 'application/json' } });
  });
  return calls;
}

describe('unauthenticated boot does not redirect (phase49 loop fix)', () => {
  beforeEach(() => {
    localStorage.clear();
    vi.restoreAllMocks();
  });

  it('never calls onUnauthorized when the visitor was never signed in', async () => {
    const onUnauthorized = vi.fn();
    const client = new ApiClient({ baseUrl: '/api', onUnauthorized });
    stubFetch([401, 401, 401, 401]);
    // Simulate the picker boot: /me (exempt), /projects (401, layout), /projects again
    await client.get('/projects').catch(() => {});
    await client.get('/executions').catch(() => {});
    await client.get('/projects').catch(() => {});
    expect(onUnauthorized).not.toHaveBeenCalled();
  });

  it('redirects exactly once when a previously-authenticated session dies', async () => {
    const onUnauthorized = vi.fn();
    const client = new ApiClient({ baseUrl: '/api', onUnauthorized });
    stubFetch([200, 401, 401]);
    await client.get('/projects').catch(() => {}); // 200: marks everAuthenticated
    await client.get('/executions').catch(() => {}); // 401: session died -> redirect once
    await client.get('/executions').catch(() => {}); // burst: still once
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });
});