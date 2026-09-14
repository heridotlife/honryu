import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  getScenarioRequests,
  instantiateScenario,
  listTemplates,
  setScenarioRequests,
  validateScenarioRequests,
} from './scenarios';
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

// Phase 65: the template catalog and instantiate. Pinned at the wire like
// the fragment calls above -- the picker renders what GET returned and the
// instantiate POST carries exactly the caller's choices (overrides omitted
// when no target URL was given).
describe('scenario template api', () => {
  it('listTemplates maps the catalog rows to the picker contract', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        new Response(
          JSON.stringify([
            { id: 7, name: 'HTTPbin baseline', project_id: 0, is_template: true, template_name: 'httpbin-baseline' },
            { id: 8, name: 'HTTPbin spike', project_id: 0, is_template: true, template_name: 'httpbin-spike' },
          ]),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        )
      )
    );

    await expect(listTemplates()).resolves.toEqual([
      { id: 7, name: 'HTTPbin baseline', templateName: 'httpbin-baseline' },
      { id: 8, name: 'HTTPbin spike', templateName: 'httpbin-spike' },
    ]);
  });

  it('instantiateScenario POSTs JSON with the override and returns the clone id', async () => {
    const requests: Array<{ url: string; init?: RequestInit }> = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({ url: String(input), init });
        return new Response(JSON.stringify({ id: 42, name: 'checkout-baseline', is_template: false }), {
          status: 201,
          headers: { 'Content-Type': 'application/json' },
        });
      })
    );

    const got = await instantiateScenario(7, { name: 'checkout-baseline', projectId: 3, targetUrl: 'http://checkout.svc' });

    expect(got).toBe(42);
    expect(requests[0].url).toBe('/api/scenarios/7/instantiate');
    expect(requests[0].init?.method).toBe('POST');
    expect(new Headers(requests[0].init?.headers).get('Content-Type')).toBe('application/json');
    expect(JSON.parse(String(requests[0].init?.body))).toEqual({
      name: 'checkout-baseline',
      project_id: 3,
      overrides: { target_url: 'http://checkout.svc' },
    });
  });

  it('instantiateScenario omits overrides entirely when no target URL is given', async () => {
    const requests: Array<{ init?: RequestInit }> = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({ init });
        return new Response(JSON.stringify({ id: 43 }), { status: 201, headers: { 'Content-Type': 'application/json' } });
      })
    );

    await expect(instantiateScenario(7, { name: 'plain', projectId: 3 })).resolves.toBe(43);
    expect(JSON.parse(String(requests[0].init?.body))).toEqual({ name: 'plain', project_id: 3 });
  });
});
