import { afterEach, describe, expect, it, vi } from 'vitest';
import { apiClient } from './client';
import { createSchedule, deleteSchedule, listSchedules } from './schedules';

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('listSchedules', () => {
  it('GETs the execution-scoped schedules path via the generated client', async () => {
    const rows = [
      { id: 3, execution_id: 42, kind: 'recurring' as const, recurrence: '0 6 * * *', active: true, occurrences: [] },
    ];
    const spy = vi.spyOn(apiClient, 'get').mockResolvedValueOnce(rows);
    await expect(listSchedules(42)).resolves.toEqual(rows);
    expect(spy).toHaveBeenCalledWith('/executions/42/schedules');
  });

  it('normalizes a null list to [] (Go marshals a nil slice as null)', async () => {
    vi.spyOn(apiClient, 'get').mockResolvedValueOnce(null);
    await expect(listSchedules(42)).resolves.toEqual([]);
  });
});

describe('createSchedule', () => {
  it('POSTs exactly the form fields the handler reads (one-shot)', async () => {
    const created = {
      id: 9,
      execution_id: 42,
      tenant_id: 7,
      kind: 'one_shot' as const,
      fire_at: '2026-10-20T06:00:00Z',
      active: true,
      occurrences: [{ id: 1, fire_time: '2026-10-20T06:00:00Z', status: 'reserved' as const }],
    };
    const spy = vi.spyOn(apiClient, 'post').mockResolvedValueOnce(created);
    await expect(createSchedule(42, { tenantId: 7, kind: 'one_shot', fireAt: '2026-10-20T06:00:00Z' })).resolves.toEqual(
      created
    );
    expect(spy).toHaveBeenCalledTimes(1);
    const [path, form] = spy.mock.calls[0];
    expect(path).toBe('/executions/42/schedules');
    expect(form.toString()).toBe('tenant_id=7&kind=one_shot&fire_at=2026-10-20T06%3A00%3A00Z');
  });

  it('sends recurrence (not fire_at) for a recurring schedule, with no "undefined" fields', async () => {
    const spy = vi.spyOn(apiClient, 'post').mockResolvedValueOnce({ id: 10, kind: 'recurring' });
    await createSchedule(42, { tenantId: 7, kind: 'recurring', recurrence: '0 * * * *' });
    const body = spy.mock.calls[0][1].toString();
    expect(body).toBe('tenant_id=7&kind=recurring&recurrence=0+*+*+*+*');
    expect(body).not.toContain('undefined');
  });
});

describe('deleteSchedule', () => {
  it('DELETEs the schedule-scoped path', async () => {
    const spy = vi.spyOn(apiClient, 'request').mockResolvedValueOnce({ message: 'schedule deleted' });
    await expect(deleteSchedule(42, 9)).resolves.toEqual({ message: 'schedule deleted' });
    expect(spy).toHaveBeenCalledWith('/executions/42/schedules/9', { method: 'DELETE' });
  });
});

// Wire-level: the handler parses the request as a form (r.ParseForm), so
// the POST must actually leave the browser form-encoded -- the same check
// reports.test.ts makes for its form routes.
describe('createSchedule (wire)', () => {
  it('sends a form-encoded POST the handler can parse', async () => {
    const calls: Array<{ url: string; init: RequestInit }> = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        calls.push({ url: String(input), init: init ?? {} });
        return new Response(JSON.stringify({ id: 11, kind: 'one_shot', active: true }), {
          status: 201,
          headers: { 'Content-Type': 'application/json' },
        });
      })
    );

    const got = await createSchedule(1, { tenantId: 2, kind: 'one_shot', fireAt: '2026-10-21T00:00:00Z' });

    expect(got.id).toBe(11);
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe('/api/executions/1/schedules');
    expect(calls[0].init.method).toBe('POST');
    const headers = new Headers(calls[0].init.headers);
    expect(headers.get('Content-Type')).toBe('application/x-www-form-urlencoded');
    expect(calls[0].init.body).toBe('tenant_id=2&kind=one_shot&fire_at=2026-10-21T00%3A00%3A00Z');
  });
});
