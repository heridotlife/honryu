import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import Tenants, { nextUpcoming, ownTenantIds } from './Tenants';
import DashboardLayout from '../components/DashboardLayout';
import { SessionProvider } from '../hooks/useSession';
import type { SessionInfo } from '../api/session';
import type { Reservation } from '../api/reservations';

// The pure halves of the page's fallback logic: which tenants a session
// administers, and what "next upcoming" means on the calendar.
describe('ownTenantIds', () => {
  const session = (tenants: SessionInfo['tenants'] | null): SessionInfo | null =>
    tenants === null
      ? null
      : {
          subject: 'demo:bob',
          name: 'Bob',
          email: '',
          global_roles: [],
          tenants,
          permissions: {},
          demo: true,
        };

  it('is empty for no session', () => {
    expect(ownTenantIds(session(null))).toEqual([]);
  });

  it('returns only tenants the session holds tenant_admin in', () => {
    expect(
      ownTenantIds(
        session({
          '1': ['tenant_admin', 'tenant_editor'],
          '2': ['tenant_editor'],
          '3': ['tenant_viewer'],
        })
      )
    ).toEqual([1]);
  });

  it('ignores keys that are not positive integers', () => {
    expect(ownTenantIds(session({ '1': ['tenant_admin'], junk: ['tenant_admin'] }))).toEqual([1]);
  });
});

describe('nextUpcoming', () => {
  const res = (id: number, start: string, end: string): Reservation => ({
    id,
    tenant_id: 5,
    engine_count: 2,
    start,
    end,
    execution_id: 1,
  });
  const now = new Date('2026-09-09T12:00:00Z');

  it('drops reservations that already ended, soonest first', () => {
    const list = [
      res(1, '2026-09-09T09:00:00Z', '2026-09-09T10:00:00Z'), // ended
      res(2, '2026-09-10T09:00:00Z', '2026-09-10T10:00:00Z'),
      res(3, '2026-09-09T13:00:00Z', '2026-09-09T14:00:00Z'),
      res(4, '2026-09-09T12:30:00Z', '2026-09-09T13:00:00Z'),
    ];
    expect(nextUpcoming(list, 3, now).map((r) => r.id)).toEqual([4, 3, 2]);
  });

  it('caps the count', () => {
    const list = [res(2, '2026-09-10T09:00:00Z', '2026-09-10T10:00:00Z'), res(3, '2026-09-11T09:00:00Z', '2026-09-11T10:00:00Z')];
    expect(nextUpcoming(list, 1, now).map((r) => r.id)).toEqual([2]);
  });
});

// The mounted page contract over a stubbed API, in Executions.test.tsx's
// createRoot + act style: list renders, selection loads the detail panels,
// revoke and create hit their endpoints with the backend's shapes, and the
// refreshed state lands.
(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const aliceAdmin: SessionInfo = {
  subject: 'demo:alice',
  name: 'Alice',
  email: '',
  global_roles: ['service_provider_admin'],
  tenants: {},
  permissions: { '*': ['*'] },
  demo: true,
};

interface Call {
  method: string;
  url: string;
  body?: string;
}

const tenantsFixture = [
  {
    id: 5,
    name: 'acme-corp',
    display_name: 'Acme Corp',
    status: 'ACTIVE',
    created_time: '2026-08-01T00:00:00Z',
  },
  {
    id: 6,
    name: 'globex',
    display_name: 'Globex',
    status: 'ACTIVE',
    created_time: '2026-08-02T00:00:00Z',
  },
];

const grantsFixture = [
  {
    subject: 'demo:bob',
    email: 'bob@acme.io',
    role: 'tenant_admin',
    granted_by: 'demo:alice',
    granted_time: '2026-08-03T00:00:00Z',
  },
];

const reservationsFixture = [
  {
    id: 1,
    tenant_id: 5,
    engine_count: 4,
    start: '2026-09-09T09:00:00Z',
    end: '2026-09-09T10:00:00Z',
    execution_id: 10,
  },
  {
    id: 2,
    tenant_id: 5,
    cluster: 'edge',
    engine_count: 2,
    start: '2026-09-10T09:00:00Z',
    end: '2026-09-10T10:00:00Z',
    execution_id: 11,
  },
];

/** The page's whole API surface, mutable where the tests drive changes. */
function stubTenantApi(me: SessionInfo) {
  const calls: Call[] = [];
  const tenants = [...tenantsFixture];
  const grants = [...grantsFixture];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? 'GET';
    calls.push({ method, url, body: typeof init?.body === 'string' ? init.body : undefined });
    if (url === '/api/me') {
      return json(me);
    }
    if (url === '/api/tenants' && method === 'GET') {
      return json(tenants);
    }
    if (url === '/api/tenants' && method === 'POST') {
      const form = new URLSearchParams(String(init?.body));
      tenants.push({
        id: 7,
        name: form.get('name') ?? '',
        display_name: form.get('display_name') ?? '',
        status: 'ACTIVE',
        created_time: '2026-09-09T00:00:00Z',
      });
      return json(tenants[tenants.length - 1], 201);
    }
    if (url.includes('/api/tenants/5/quota')) {
      return json({ cluster: '', ceiling: 3 });
    }
    if (url.includes('/api/tenants/5/roles')) {
      if (method === 'DELETE') {
        grants.length = 0;
        return json({ message: 'role revoked' });
      }
      return json(grants);
    }
    if (url.includes('/api/tenants/5/reservations')) {
      return json(reservationsFixture);
    }
    if (url === '/api/clusters') {
      return json([
        {
          name: 'edge',
          api_url: 'https://edge:6443',
          ingest_url: 'https://edge/api/ingest',
          sidecar_image: 'sidecar:latest',
          namespace: 'honryu',
          secret_ref: '',
          origin: 'operator',
          created_time: '2026-08-01T00:00:00Z',
        },
      ]);
    }
    return json({ message: `no stub for ${method} ${url}` }, 500);
  });
  return { fetchMock, calls };
}

let container: HTMLDivElement | null = null;
let root: Root | null = null;

async function renderTenants(me: SessionInfo = aliceAdmin) {
  const api = stubTenantApi(me);
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal('fetch', api.fetchMock);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter initialEntries={['/tenants']}>
        <SessionProvider>
          <Tenants />
        </SessionProvider>
      </MemoryRouter>
    );
  });
  // Flush the me/tenants/clusters fetch chains.
  await act(async () => {});
  return api;
}

/** Native value setter + input event (React's tracker ignores plain writes). */
async function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
}

async function click(el: Element) {
  await act(async () => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

beforeEach(() => {
  // The page renders Next upcoming against the real clock; pin it so the
  // seeded 2026-09-10 reservations stay upcoming forever (the test would
  // otherwise expire as real time passes the seeded windows).
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-09-09T12:00:00Z"));
});

afterEach(() => {
  vi.useRealTimers();
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

describe('Tenants page (mounted)', () => {
  it('renders the tenant list from the API', async () => {
    await renderTenants();

    expect(container!.querySelector('[data-testid="tenants-page"]')).not.toBeNull();
    const row5 = container!.querySelector('[data-testid="tenant-row-5"]');
    const row6 = container!.querySelector('[data-testid="tenant-row-6"]');
    expect(row5?.textContent).toContain('acme-corp');
    expect(row5?.textContent).toContain('Acme Corp');
    expect(row6?.textContent).toContain('globex');
  });

  it('selecting a tenant loads quota, members, and the reservations summary', async () => {
    await renderTenants();

    await click(container!.querySelector('[data-testid="tenant-row-5"]')!);
    await act(async () => {});

    const ceiling = container!.querySelector('input[aria-label="quota ceiling"]') as HTMLInputElement;
    expect(ceiling.value).toBe('3');

    const table = container!.querySelector('[data-testid="members-table"]');
    expect(table?.textContent).toContain('demo:bob');
    expect(table?.textContent).toContain('tenant_admin');

    // The ended reservation counts but only the future one is upcoming.
    const summary = container!.textContent ?? '';
    expect(summary).toContain('2 reservations');
    expect(summary).toContain('2 engines on edge');
  });

  it('revoke issues the DELETE with subject/role and refreshes the roster', async () => {
    const api = await renderTenants();
    await click(container!.querySelector('[data-testid="tenant-row-5"]')!);
    await act(async () => {});

    await click(container!.querySelector('[data-testid="revoke-btn-demo:bob"]')!);
    await act(async () => {});

    const revoke = api.calls.find((c) => c.method === 'DELETE');
    expect(revoke?.url).toContain('/api/tenants/5/roles?');
    expect(revoke?.url).toContain('subject=demo%3Abob');
    expect(revoke?.url).toContain('role=tenant_admin');
    // The refreshed roster dropped the row.
    expect(container!.querySelector('[data-testid="members-table"]')?.textContent).not.toContain('demo:bob');
  });

  it('create POSTs form-encoded and refreshes the list', async () => {
    const api = await renderTenants();

    await type(container!.querySelector('input[aria-label="tenant name"]') as HTMLInputElement, 'initech');
    await type(container!.querySelector('input[aria-label="tenant display name"]') as HTMLInputElement, 'Initech');
    await click(container!.querySelector('[data-testid="create-tenant-btn"]')!);
    await act(async () => {});

    const post = api.calls.find((c) => c.method === 'POST' && c.url === '/api/tenants');
    expect(post?.body).toBe('name=initech&display_name=Initech');
    expect(container!.querySelector('[data-testid="tenant-row-7"]')?.textContent).toContain('initech');
  });
});

// The nav's tenant:admin gate, mounted with a mocked session: the link is
// the route's promise, so it must not appear for a persona the routes
// would refuse (DashboardLayout's mounted-test style).
describe('Tenants nav gating (phase 35)', () => {
  async function renderNav(me: SessionInfo) {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url === '/api/me') {
        return json(me);
      }
      if (url === '/api/projects') {
        return json([]);
      }
      return json({ message: `no stub for ${url}` }, 500);
    });
    // DashboardLayout's theme effect asks matchMedia, which jsdom lacks.
    vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: false }));
    root = createRoot(container);
    await act(async () => {
      root!.render(
        <MemoryRouter initialEntries={['/reports']}>
          <SessionProvider>
            <DashboardLayout>
              <p>page</p>
            </DashboardLayout>
          </SessionProvider>
        </MemoryRouter>
      );
    });
    await act(async () => {});
  }

  it('shows the Tenants link to a tenant admin', async () => {
    await renderNav({
      subject: 'demo:bob',
      name: 'Bob',
      email: '',
      global_roles: [],
      tenants: { '1': ['tenant_admin'] },
      permissions: { tenant: ['admin'] },
      demo: true,
    });
    const link = container!.querySelector('a[href="/tenants"]');
    expect(link?.textContent).toContain('Tenants');
  });

  it('hides the Tenants link from a plain viewer', async () => {
    await renderNav({
      subject: 'demo:carol',
      name: 'Carol',
      email: '',
      global_roles: [],
      tenants: { '1': ['tenant_viewer'] },
      permissions: {
        project: ['list', 'read'],
        execution: ['list', 'read'],
        schedule: ['list', 'read'],
        report: ['list', 'read'],
      },
      demo: true,
    });
    expect(container!.querySelector('a[href="/tenants"]')).toBeNull();
  });
});
