import { afterEach, describe, expect, it, vi } from 'vitest';
import { getScenarioRequests, setScenarioRequests, validateScenarioRequests } from './scenarios';
import { apiClient } from './client';

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('scenario fragment api', () => {
  it('get returns the body text verbatim', async () => {
    const yaml = '# keep me\\nmulti-test:\\n  scenario: false\\n';
    vi.spyOn(apiClient, 'text').mockResolvedValue(yaml);
    await expect(getScenarioRequests(5)).resolves.toBe(yaml);
    expect(apiClient.text).toHaveBeenCalledWith('/scenarios/5/requests');
  });

  // Phase 64 migration: the PUT delegates to the generated client, which
  // sends the fragment as a raw text/yaml body (the spec's own media type --
  // the G3 handler accepts any of the text/yaml media types) with the bytes
  // untouched. Pinned at the wire, not against an internal client method.
  it('put sends text/yaml with the body untouched', async () => {
    const requests: { url: string; init?: RequestInit }[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({ url: String(input), init });
        return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } });
      })
    );
    const body = 'execution:\\n  ramp-up: 30s # hand-written\\n';
    await setScenarioRequests(5, body);
    expect(requests[0].url).toBe('/api/scenarios/5/requests');
    expect(requests[0].init?.method).toBe('PUT');
    expect(new Headers(requests[0].init?.headers).get('Content-Type')).toBe('text/yaml');
    expect(requests[0].init?.body).toBe(body);
  });

  it('validate reports a 200 as valid with the diagnostics passed through', async () => {
    const requests: { url: string; init?: RequestInit }[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({ url: String(input), init });
        return new Response(
          JSON.stringify({ valid: true, diagnostics: [{ severity: 'info', message: 'unmodelled key', line: 4 }] }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        );
      })
    );

    const got = await validateScenarioRequests(7, 'requests:\\n  - /x\\n');

    expect(requests[0].url).toBe('/api/scenarios/7/requests/validate');
    expect(requests[0].init?.method).toBe('POST');
    expect(new Headers(requests[0].init?.headers).get('Content-Type')).toBe('text/yaml');
    expect(got).toEqual({
      valid: true,
      diagnostics: [{ severity: 'info', message: 'unmodelled key', line: 4 }],
    });
  });

  it('validate unwraps a 400 DiagnosticsError into an invalid result', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        new Response(
          JSON.stringify({ message: 'invalid fragment', diagnostics: [{ severity: 'error', message: 'no url', line: 2 }] }),
          { status: 400, headers: { 'Content-Type': 'application/json' } }
        )
      )
    );

    await expect(validateScenarioRequests(7, 'requests:\\n  - {}\\n')).resolves.toEqual({
      valid: false,
      diagnostics: [{ severity: 'error', message: 'no url', line: 2 }],
    });
  });
});
