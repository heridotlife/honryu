// Wire-shape pins for the capacity-profile API client (phase 54). The
// fan-out fetcher predates this phase but had no tests; everything
// downstream -- the calibration panel, the phase-54 planner, the editor's
// save-guard -- rides on this exact URL contract, so it is pinned here:
// path, query order and encoding, the number-to-string target encoding,
// and the "engines only ever accompanies status ok" response shape.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fanOutCapacity, getCapacityProfile } from './calibration';
import { ApiError } from './client';

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

describe('fanOutCapacity wire shape', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('GETs the scenario-scoped fanout route with the full capacity key and target', async () => {
    let seenUrl = '';
    let seenMethod = '';
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        seenUrl = String(input);
        seenMethod = init?.method ?? 'GET';
        return jsonResponse({ status: 'ok', engines: 2 });
      }),
    );

    const got = await fanOutCapacity(7, { engine: 'jmeter', cpu: '500m', memory: '512Mi' }, 250);

    expect(seenMethod).toBe('GET');
    expect(seenUrl).toBe('/api/scenarios/7/capacity-profile/fanout?engine=jmeter&cpu=500m&memory=512Mi&target_qps=250');
    expect(got).toEqual({ status: 'ok', engines: 2 });
  });

  it('string-encodes a fractional target without rounding', async () => {
    let seenUrl = '';
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        seenUrl = String(input);
        return jsonResponse({ status: 'ok', engines: 5 });
      }),
    );

    await fanOutCapacity(7, { engine: 'jmeter', cpu: '500m', memory: '512Mi' }, 250.5);

    expect(seenUrl.endsWith('target_qps=250.5')).toBe(true);
  });

  it('non-ok verdicts carry no engines field, and the client passes that absence through', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => jsonResponse({ status: 'engine_floor' })),
    );

    const got = await fanOutCapacity(7, { engine: 'jmeter', cpu: '500m', memory: '512Mi' }, 100);

    expect(got.status).toBe('engine_floor');
    expect(got.engines).toBeUndefined();
  });

  it('error responses surface as typed ApiErrors, not thrown strings', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => jsonResponse({ message: 'invalid target_qps' }, 400)),
    );

    await expect(fanOutCapacity(7, { engine: 'jmeter', cpu: '500m', memory: '512Mi' }, 0)).rejects.toMatchObject({
      status: 400,
    });
  });
});

describe('getCapacityProfile wire shape', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('GETs the scenario-scoped profile route keyed by engine/cpu/memory only', async () => {
    let seenUrl = '';
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        seenUrl = String(input);
        return jsonResponse({
          scenario_id: 7,
          engine: 'jmeter',
          cpu: '500m',
          memory: '512Mi',
          per_pod_qps: 608.5,
          saturated_by: 'engine',
          scenario_fingerprint: 'fp1',
          calibrated_at: '2026-09-10T16:47:22Z',
          job_id: 3,
        });
      }),
    );

    const got = await getCapacityProfile(7, { engine: 'jmeter', cpu: '500m', memory: '512Mi' });

    expect(seenUrl).toBe('/api/scenarios/7/capacity-profile?engine=jmeter&cpu=500m&memory=512Mi');
    expect(got.per_pod_qps).toBe(608.5);
    expect(got.calibrated_at).toBe('2026-09-10T16:47:22Z');
  });

  it('a missing profile is a typed 404 -- the planner and save-guard branch on exactly this', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => jsonResponse({ message: 'no profile' }, 404)),
    );

    await expect(getCapacityProfile(7, { engine: 'jmeter', cpu: '500m', memory: '512Mi' })).rejects.toBeInstanceOf(
      ApiError,
    );
  });
});
