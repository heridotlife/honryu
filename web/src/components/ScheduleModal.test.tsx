// The schedule drawer's mounted interaction (phase 108),
// CalibrateScenarioModal.test.tsx's style: createRoot + act with fetch
// stubbed per-URL. Under test: the list render (kind badge, cron/fire-at,
// next fire, per-row delete), the empty state, the create guards (cron
// required + 5 fields, fire-at required + future -- the server does NOT
// enforce future, so the client guard is the real gate), the two-step
// delete, inline rendering of API errors (plain message and the structured
// 429 envelope ActionErrorDetails exists for), and the success path
// (exact form body, list refetch, form cleared, onMutated fired).
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import ScheduleModal from './ScheduleModal';
import type { Schedule } from '../api/schedules';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const project = { id: 3, name: 'phase108', owner: 'heri', tenant_id: 7, created_time: '2026-08-01T00:00:00Z' };

const oneShot: Schedule = {
  id: 9,
  execution_id: 12,
  tenant_id: 7,
  kind: 'one_shot',
  fire_at: '2030-01-01T00:00:00Z',
  active: true,
  occurrences: [{ id: 1, fire_time: '2030-01-01T00:00:00Z', status: 'reserved' }],
};

const recurring: Schedule = {
  id: 10,
  execution_id: 12,
  tenant_id: 7,
  kind: 'recurring',
  recurrence: '0 6 * * *',
  active: true,
  occurrences: [
    { id: 2, fire_time: '2030-01-01T06:00:00Z', status: 'reserved' },
    { id: 3, fire_time: '2030-01-02T06:00:00Z', status: 'rejected' },
  ],
};

interface ApiState {
  schedules: Schedule[];
  /** Response for the create POST: [status, body]. */
  create: [number, unknown];
  /** Response for the project GET. */
  project: unknown;
  /** DELETE /schedules/{id} -> ok unless listed here (status, body). */
  deleteFails: Record<number, [number, unknown]>;
}

interface Seen {
  gets: string[];
  posts: Array<{ url: string; body: string }>;
  deletes: string[];
}

let container: HTMLDivElement | null = null;
let root: Root | null = null;
let seen: Seen = { gets: [], posts: [], deletes: [] };
const mutated = vi.fn();
const closed = vi.fn();

const stubApi = (state: Partial<ApiState> = {}): void => {
  const s: ApiState = { schedules: [oneShot, recurring], create: [201, oneShot], project, deleteFails: {}, ...state };
  // The served list is stateful: a DELETE (unless stubbed to fail) removes
  // the schedule, so the modal's post-mutation refetch sees the new world.
  const live = [...s.schedules];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method as string) ?? 'GET';
      if (url === '/api/executions/12/schedules' && method === 'GET') {
        seen.gets.push(url);
        return json(live);
      }
      if (url === '/api/executions/12/schedules' && method === 'POST') {
        seen.posts.push({ url, body: String(init?.body ?? '') });
        const [status, body] = s.create;
        return json(body, status);
      }
      if (url.startsWith('/api/executions/12/schedules/') && method === 'DELETE') {
        const id = Number(url.split('/').pop());
        seen.deletes.push(url);
        const fail = s.deleteFails[id];
        if (fail) {
          return json(fail[1], fail[0]);
        }
        const idx = live.findIndex((sc) => sc.id === id);
        if (idx >= 0) live.splice(idx, 1);
        return json({ message: 'schedule deleted' });
      }
      if (url === '/api/projects/3') {
        return json(s.project);
      }
      return json({ message: `no stub for ${method} ${url}` }, 500);
    })
  );
};

async function renderModal(): Promise<void> {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <ScheduleModal executionId={12} executionName="checkout smoke" projectId={3} onClose={closed} onMutated={mutated} />
    );
  });
}

const q = (testid: string): HTMLElement => container!.querySelector(`[data-testid="${testid}"]`) as HTMLElement;

const qAll = (testid: string): HTMLElement[] => Array.from(container!.querySelectorAll(`[data-testid="${testid}"]`));

const click = async (el: HTMLElement): Promise<void> => {
  await act(async () => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
};

const fill = async (testid: string, value: string): Promise<void> => {
  const el = q(testid) as HTMLInputElement;
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
};

const submit = (): Promise<void> => click(q('schedule-create'));

afterEach(() => {
  vi.unstubAllGlobals();
  const r = root;
  if (r !== null && container !== null) {
    act(() => {
      r.unmount();
    });
  }
  container?.remove();
  container = null;
  root = null;
  seen = { gets: [], posts: [], deletes: [] };
  mutated.mockClear();
  closed.mockClear();
});

describe('ScheduleModal list', () => {
  it('loads the schedules and the project tenant on mount, rendering one row per schedule', async () => {
    stubApi();
    await renderModal();

    expect(seen.gets).toContain('/api/executions/12/schedules');
    expect(qAll('schedule-row')).toHaveLength(2);

    const badges = qAll('schedule-kind').map((b) => b.textContent);
    expect(badges).toEqual(['One-shot', 'Recurring']);
    // The trigger itself: the fire instant for one-shot, the cron verbatim
    // for recurring.
    expect(container!.textContent).toContain('0 6 * * *');
    expect(container!.textContent).toContain('2030');
    // Occurrence arithmetic + next still-reserved fire, per row.
    expect(container!.textContent).toContain('1 occurrence · 1 reserved');
    expect(container!.textContent).toContain('2 occurrences · 1 reserved');
    expect(container!.textContent).toContain('next fire');
    expect(q('schedule-delete-9')).toBeTruthy();
    expect(q('schedule-delete-10')).toBeTruthy();
    expect(mutated).not.toHaveBeenCalled();
  });

  it('renders the manual-trigger empty state when there are no schedules', async () => {
    stubApi({ schedules: [] });
    await renderModal();

    const empty = q('schedule-empty');
    expect(empty).toBeTruthy();
    expect(q('schedule-empty-description')?.textContent).toBe(
      'No schedules — this execution fires only when triggered manually.'
    );
    expect(qAll('schedule-row')).toHaveLength(0);
  });
});

describe('ScheduleModal create guards (client-side)', () => {
  it('refuses a recurring schedule with an empty cron', async () => {
    stubApi();
    await renderModal();
    await submit();

    expect(q('schedule-form-error').textContent).toContain('cron expression is required');
    expect(seen.posts).toHaveLength(0);
  });

  it('refuses a cron without exactly 5 fields', async () => {
    stubApi();
    await renderModal();
    await fill('schedule-cron-input', '0 6 * * * *'); // 6-field (seconds) cron
    await submit();

    expect(q('schedule-form-error').textContent).toContain('5 fields');
    expect(seen.posts).toHaveLength(0);
  });

  it('refuses a one-shot without a fire-at time, and one in the past', async () => {
    stubApi();
    await renderModal();
    await click(q('schedule-kind-one_shot'));

    await submit();
    expect(q('schedule-form-error').textContent).toContain('fire-at time is required');
    expect(seen.posts).toHaveLength(0);

    await fill('schedule-fireat-input', '2020-01-01T00:00');
    await submit();
    // The server does NOT reject a past fire_at (it would create a schedule
    // with zero occurrences that never fires) -- this guard is the gate.
    expect(q('schedule-form-error').textContent).toContain('future');
    expect(seen.posts).toHaveLength(0);
  });

  it('refuses creation when the project has no tenant to reserve against', async () => {
    stubApi({ project: { ...project, tenant_id: null } });
    await renderModal();
    await click(q('schedule-preset-hourly'));
    await submit();

    expect(q('schedule-form-error').textContent).toContain('no tenant');
    expect(seen.posts).toHaveLength(0);
  });
});

describe('ScheduleModal create success', () => {
  it('POSTs the handler\u2019s exact form fields (preset-filled cron), refetches, clears the form, fires onMutated', async () => {
    stubApi({ create: [201, recurring] });
    await renderModal();

    await click(q('schedule-preset-hourly'));
    expect((q('schedule-cron-input') as HTMLInputElement).value).toBe('0 * * * *');
    await submit();

    expect(seen.posts).toHaveLength(1);
    expect(seen.posts[0].body).toBe('tenant_id=7&kind=recurring&recurrence=0+*+*+*+*');
    // The list refetched to show the new schedule...
    expect(seen.gets).toHaveLength(2);
    // ...and the form cleared for the next one.
    expect((q('schedule-cron-input') as HTMLInputElement).value).toBe('');
    expect(mutated).toHaveBeenCalledTimes(1);
  });

  it('converts the datetime-local one-shot instant to an RFC3339 UTC fire_at', async () => {
    stubApi({ create: [201, oneShot] });
    await renderModal();
    await click(q('schedule-kind-one_shot'));
    await fill('schedule-fireat-input', '2030-06-15T08:30');
    await submit();

    expect(seen.posts).toHaveLength(1);
    // Same conversion the component performs -- datetime-local has no
    // timezone, so the instant is what the local clock means, in UTC.
    const expected = encodeURIComponent(new Date('2030-06-15T08:30').toISOString());
    expect(seen.posts[0].body).toBe(`tenant_id=7&kind=one_shot&fire_at=${expected}`);
    expect(mutated).toHaveBeenCalledTimes(1);
  });
});

describe('ScheduleModal create errors (server)', () => {
  it('renders a plain API error message inline', async () => {
    stubApi({ create: [400, { message: 'schedule: recurrence is not a valid cron expression: expected exactly 5 fields' }] });
    await renderModal();
    await click(q('schedule-preset-hourly'));
    await submit();

    expect(container!.textContent).toContain('not a valid cron expression');
    // No structured details on this envelope: the details renderer stays
    // out of the way.
    expect(q('action-error-details')).toBeNull();
    expect(mutated).not.toHaveBeenCalled();
  });

  it('renders the structured details of a 429 quota envelope (ActionErrorDetails)', async () => {
    stubApi({
      create: [
        429,
        {
          message: 'quota: cannot reserve occurrence',
          details: { tenant_id: 7, ceiling: 4, used: 4, requested: 2, hint: 'PUT /api/tenants/7/quota ceiling=8' },
        },
      ],
    });
    await renderModal();
    await click(q('schedule-preset-hourly'));
    await submit();

    expect(container!.textContent).toContain('quota: cannot reserve occurrence');
    expect(q('action-error-details')).toBeTruthy();
    expect(container!.textContent).toContain('used 4 / ceiling 4 — requested 2');
  });
});

describe('ScheduleModal delete', () => {
  it('deletes only after a two-step confirm, then refetches and fires onMutated', async () => {
    stubApi({ schedules: [oneShot] });
    await renderModal();

    // Step one arms; nothing leaves the browser yet.
    await click(q('schedule-delete-9'));
    expect(seen.deletes).toHaveLength(0);
    expect(q('schedule-confirm-delete-9')).toBeTruthy();
    expect(q('schedule-cancel-delete-9')).toBeTruthy();

    // Cancel disarms without a request.
    await click(q('schedule-cancel-delete-9'));
    expect(seen.deletes).toHaveLength(0);
    expect(q('schedule-delete-9')).toBeTruthy();

    // Armed again, the confirm deletes and the refreshed list shows the
    // empty state.
    await click(q('schedule-delete-9'));
    await click(q('schedule-confirm-delete-9'));
    expect(seen.deletes).toEqual(['/api/executions/12/schedules/9']);
    expect(q('schedule-empty')).toBeTruthy();
    expect(mutated).toHaveBeenCalledTimes(1);
  });

  it('renders a failed delete inline and keeps the row', async () => {
    stubApi({ schedules: [oneShot], deleteFails: { 9: [500, { message: 'boom' }] } });
    await renderModal();

    await click(q('schedule-delete-9'));
    await click(q('schedule-confirm-delete-9'));
    expect(seen.deletes).toHaveLength(1);
    expect(container!.textContent).toContain('boom');
    expect(qAll('schedule-row')).toHaveLength(1);
    expect(mutated).not.toHaveBeenCalled();
  });
});
