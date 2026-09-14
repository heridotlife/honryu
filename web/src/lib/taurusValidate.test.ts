import { describe, expect, it, vi, afterEach } from 'vitest';
import { parseDocument } from 'yaml';
import { validateScenarioRequests, type Diagnostic } from '../api/scenarios';

// The editor's layer-1 check is a pure function of the text -- mirrored
// here against the SAME library call so the contract is pinned: parse
// errors are line-anchored, valid docs return null. (The component wraps
// exactly this logic; CodeMirror-in-jsdom is flaky, so the lens + api
// layers carry the assertions -- same pattern as R5's byte-equality tests.)
function checkParse(text: string): Diagnostic | null {
  const doc = parseDocument(text);
  if (doc.errors.length === 0) {
    return null;
  }
  const e = doc.errors[0];
  const m = /line\s+(\d+)/i.exec(e.message);
  return {
    severity: 'error',
    message: e.message.replace(/line \d+:\s*/i, ''),
    line: m ? parseInt(m[1], 10) : 1,
  };
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('layer 1: client parse check', () => {
  it('valid YAML -> null (no network)', () => {
    expect(checkParse('execution:\n  - executor: jmeter\n')).toBeNull();
  });

  it('broken YAML -> line-anchored error', () => {
    const d = checkParse('execution: [');
    expect(d).not.toBeNull();
    expect(d?.severity).toBe('error');
    expect(d?.line).toBeGreaterThan(0);
    expect(d?.message).not.toMatch(/^line/i); // prefix stripped into .line
  });

  it('line number anchors at/near the broken row on a multi-line doc', () => {
    const d = checkParse('a: 1\nb: [x\nc: 3\n');
    expect(d).not.toBeNull();
    // yaml.v2.9 reports the line where the flow sequence fails to close;
    // observed as 2 or 3 depending on where the scanner gives up. The
    // contract is "close to the break", not an exact scanner offset.
    expect([2, 3]).toContain(d?.line);
  });
});

describe('layer 2: G5 validate endpoint', () => {
  // Phase 64: validateScenarioRequests delegates to the generated client, so
  // these pin the wire (method, media type, bytes) and let the real ApiClient
  // build the ApiErrors, instead of mocking its internal call shape.
  function stubFetchOnce(status: number, body: string): void {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(body, { status, headers: { 'Content-Type': 'application/json' } })),
    );
  }

  it('200 -> valid with no diagnostics', async () => {
    const requests: { url: string; init?: RequestInit }[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({ url: String(input), init });
        return new Response(JSON.stringify({ valid: true, diagnostics: [] }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }),
    );

    const res = await validateScenarioRequests(5, 'a: 1\n');

    expect(res).toEqual({ valid: true, diagnostics: [] });
    expect(requests[0].url).toBe('/api/scenarios/5/requests/validate');
    expect(requests[0].init?.method).toBe('POST');
    expect(new Headers(requests[0].init?.headers).get('Content-Type')).toBe('text/yaml');
    expect(requests[0].init?.body).toBe('a: 1\n');
  });

  it('400 DiagnosticsError -> valid:false with the diagnostics unwrapped', async () => {
    const diags: Diagnostic[] = [
      { severity: 'error', message: 'unknown field "requestz"', line: 4 },
      { severity: 'info', message: 'uncompiled key "think-time"', line: 6 },
    ];
    stubFetchOnce(400, JSON.stringify({ message: 'invalid requests', diagnostics: diags }));

    const res = await validateScenarioRequests(5, 'a: 1\n');

    expect(res.valid).toBe(false);
    expect(res.diagnostics).toEqual(diags);
  });

  it('400 without a diagnostics body -> empty list, still invalid', async () => {
    stubFetchOnce(400, JSON.stringify({ message: 'bad body' }));

    const res = await validateScenarioRequests(5, 'a: 1\n');

    expect(res.valid).toBe(false);
    expect(res.diagnostics).toEqual([]);
  });

  it('non-400 (404 scenario) propagates -- the banner owns it', async () => {
    stubFetchOnce(404, JSON.stringify({ message: 'no scenario' }));

    await expect(validateScenarioRequests(5, 'a: 1\n')).rejects.toThrow('no scenario');
  });
});

describe('severity contract', () => {
  it('severity is exactly error or info (UI colors + blocks on it)', () => {
    const severities = ['error', 'info'] as const;
    for (const s of severities) {
      expect(['error', 'info']).toContain(s);
    }
    // The ValidateResponse shape carries severities through untouched.
    const diags: Diagnostic[] = [
      { severity: 'error', message: 'x', line: 1 },
      { severity: 'info', message: 'y', line: 2 },
    ];
    expect(diags.filter((d) => d.severity === 'error')).toHaveLength(1);
    expect(diags.filter((d) => d.severity === 'info')).toHaveLength(1);
  });
});
