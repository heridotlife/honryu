// App's route table end to end: pushState sets the real location before
// mount (App mounts a BrowserRouter, so no MemoryRouter indirection), the
// session resolves a persona, and the compare deep-link -- route AND query
// -- must land on a working RunCompare. The four personas' permission maps
// are copied from DashboardLayout.test.tsx; every persona holds
// report:read, so every one of them may use the compare surface.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import App from './App';
import type { SessionInfo } from './api/session';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const personas: Record<string, Record<string, string[]>> = {
  alice: { '*': ['*'] },
  bob: {
    project: ['create', 'delete', 'list', 'read', 'update'],
    execution: ['create', 'delete', 'list', 'read', 'update'],
    scenario: ['create', 'delete', 'list', 'read', 'update'],
    run: ['create', 'delete', 'list', 'read', 'update'],
    schedule: ['create', 'delete', 'list', 'read', 'update'],
    report: ['list', 'read'],
  },
  carol: {
    project: ['list', 'read'],
    execution: ['list', 'read'],
    scenario: ['list', 'read'],
    run: ['list', 'read'],
    schedule: ['list', 'read'],
    report: ['list', 'read'],
  },
  dave: {
    campaign: ['admin', 'create', 'delete', 'list', 'read', 'update'],
    project: ['list', 'read'],
    execution: ['list', 'read'],
    schedule: ['list', 'read'],
    report: ['list', 'read'],
  },
};

const sessionFor = (who: string): SessionInfo => ({
  subject: `demo:${who}`,
  name: who,
  email: '',
  global_roles: [],
  tenants: { '1': [who] },
  permissions: personas[who],
  demo: true,
});

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const reportsBody = [
  {
    execution_id: 5,
    scenario_id: 2,
    run_id: 9,
    started_at: '2026-09-04T10:00:00Z',
    ended_at: '2026-09-04T10:01:00Z',
    outcome: 'passed',
    requested: { concurrency: 10, throughput: 100 },
    achieved: { concurrency: 10, throughput: 110, samples: 6600, failed: 132 },
    error_rate: 0.02,
    latency: { '50': 0.04, '95': 0.18, '99': 0.35 },
    attribution: { target: 2, engine: 0, unknown: 0 },
  },
  {
    execution_id: 5,
    scenario_id: 2,
    run_id: 8,
    started_at: '2026-09-03T10:00:00Z',
    ended_at: '2026-09-03T10:01:00Z',
    outcome: 'passed',
    requested: { concurrency: 10, throughput: 100 },
    achieved: { concurrency: 10, throughput: 95, samples: 6000, failed: 60 },
    error_rate: 0.01,
    latency: { '50': 0.05, '95': 0.2 },
    attribution: { target: 1, engine: 0, unknown: 0 },
  },
];

let container: HTMLDivElement | null = null;
let root: Root | null = null;

async function renderAppAt(url: string, me: () => Response) {
  window.history.pushState({}, '', url);
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url_ = String(input);
    if (url_.endsWith('/api/me')) {
      return me();
    }
    if (url_.endsWith('/api/executions/5/reports')) {
      return json(reportsBody);
    }
    if (url_.endsWith('/api/runs/8/series') || url_.endsWith('/api/runs/9/series')) {
      return json({ points: [] });
    }
    return json({ message: `no stub for ${url_}` }, 500);
  }));
  // DashboardLayout's theme effect asks matchMedia, which jsdom lacks.
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: false }));
  root = createRoot(container);
  await act(async () => {
    root!.render(<App />);
  });
  // Flush /api/me, then the compare page's reports fetch.
  await act(async () => {});
  await act(async () => {});
}

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

describe('App route table: /executions/:id/compare', () => {
  it('renders RunCompare for every persona that can read reports', async () => {
    for (const who of ['alice', 'bob', 'carol', 'dave']) {
      await renderAppAt('/executions/5/compare', () => json(sessionFor(who)));

      expect(container!.textContent, `persona ${who}`).toContain('Compare runs');
      expect(container!.querySelector('[data-testid="select-run-a"]'), `persona ${who}`).not.toBeNull();
      expect(container!.querySelector('[data-testid="select-run-b"]'), `persona ${who}`).not.toBeNull();
    }
  });

  it('deep-links ?runs=a,b through the real router into the selectors', async () => {
    await renderAppAt('/executions/5/compare?runs=9,8', () => json(sessionFor('carol')));

    expect((container!.querySelector('[data-testid="select-run-a"]') as HTMLSelectElement).value).toBe('9');
    expect((container!.querySelector('[data-testid="select-run-b"]') as HTMLSelectElement).value).toBe('8');
    expect(container!.querySelector('[data-testid="delta-table"]')).not.toBeNull();
  });
});

// Phase 67b: the old flat list URL redirects to /scenarios for real --
// pushState + BrowserRouter, the way a bookmark arrives. The list page's own
// data fetches 404 into its error state here (the harness stubs only what
// this test needs); the assertion is the landing, not the list.
describe('App route table: /executions redirects (phase 67b)', () => {
  it('lands an old /executions bookmark on the scenarios list', async () => {
    await renderAppAt('/executions', () => json(sessionFor('carol')));

    expect(window.location.pathname).toBe('/scenarios');
    expect(container!.querySelector('h1')?.textContent).toBe('Scenarios');
  });
});
