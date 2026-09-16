import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import Reservations, { groupByDay, reservationStatus } from './Reservations';
import type { Reservation } from '../api/reservations';

function makeReservation(overrides: Partial<Reservation> = {}): Reservation {
  return {
    id: 1,
    tenant_id: 1,
    cluster: 'default',
    engine_count: 2,
    start: '2026-08-06T10:00:00Z',
    end: '2026-08-06T11:00:00Z',
    execution_id: 42,
    ...overrides,
  };
}

describe('reservationStatus', () => {
  it('is upcoming when now is before start', () => {
    const r = makeReservation();
    const now = new Date('2026-08-06T09:00:00Z');
    expect(reservationStatus(r, now)).toBe('upcoming');
  });

  it('is active when now is within [start, end)', () => {
    const r = makeReservation();
    const now = new Date('2026-08-06T10:30:00Z');
    expect(reservationStatus(r, now)).toBe('active');
  });

  it('is past once now reaches end', () => {
    const r = makeReservation();
    const now = new Date('2026-08-06T11:00:00Z');
    expect(reservationStatus(r, now)).toBe('past');
  });
});

describe('groupByDay', () => {
  it('groups reservations that share a calendar day and preserves the rest as separate groups', () => {
    const sameDay1 = makeReservation({ id: 1, start: '2026-08-06T12:00:00Z' });
    const sameDay2 = makeReservation({ id: 2, start: '2026-08-06T15:00:00Z' });
    const otherDay = makeReservation({ id: 3, start: '2026-08-10T12:00:00Z' });

    const groups = groupByDay([sameDay1, sameDay2, otherDay]);

    expect(groups).toHaveLength(2);
    const [firstDay, firstItems] = groups[0];
    const [secondDay, secondItems] = groups[1];
    expect(firstItems.map((r) => r.id)).toEqual([1, 2]);
    expect(secondItems.map((r) => r.id)).toEqual([3]);
    expect(firstDay).not.toBe(secondDay);
  });

  it('returns no groups for an empty list', () => {
    expect(groupByDay([])).toEqual([]);
  });
});

// Phase 51 landmark gate (mounted; createElement so this stays a .ts file):
// the page's outermost element is a labelled region a screen reader can jump to.
// Reservations fetches nothing on mount (the query form drives the load), so
// the mount needs no fetch stubs.
(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | null = null;
let root: Root | null = null;

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
});

describe('Reservations landmarks (phase 51)', () => {
  it('renders the page as a labelled region', async () => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => {
      root!.render(createElement(Reservations));
    });

    const region = container!.querySelector('[role="region"]');
    expect(region).not.toBeNull();
    expect(region!.getAttribute('aria-label')).toBe('Reservations panel');
  });
});

// Phase 76: an empty query result renders the shared EmptyState,
// message-only -- reservations are created by the scheduler when runs
// reserve engines; the SPA has no create flow, so a button here would be
// dead. Loading (the submit button's disabled "Loading…") never blurs
// into the empty copy: the empty state only mounts after the response.
describe('Reservations empty state (phase 76)', () => {
  it('renders message-only after a query that returns nothing', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } })),
    );
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => {
      root!.render(createElement(Reservations));
    });

    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
    const tenant = container!.querySelector('input[type="number"]') as HTMLInputElement;
    await act(async () => {
      setter.call(tenant, '7');
      tenant.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await act(async () => {
      container!.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    });
    await act(async () => {});

    const empty = container!.querySelector('[data-testid="reservations-empty"]')!;
    expect(empty).not.toBeNull();
    expect(empty.querySelector('[data-testid="reservations-empty-title"]')?.textContent).toBe(
      'No reservations for this tenant',
    );
    expect(empty.querySelector('button, a')).toBeNull();
  });
});
