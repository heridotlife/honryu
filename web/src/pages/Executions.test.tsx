import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import Executions from './Executions';
import { PROJECT_STORAGE_KEY } from '../components/ProjectSwitcher';
import { SessionProvider } from '../hooks/useSession';

// R1's invariants that survive jsdom's no-geometry rendering: the page links
// rows into the hub (/executions/:id) and renders an empty state, not a 404.
// Route-level behaviour (the /status redirect) is pinned in App.test.ts.
describe('Executions page contract', () => {
  it('links rows to /executions/:id', () => {
    // The link target format is the seam with the R2 hub.
    const hrefFor = (id: number) => `/executions/${id}`;
    expect(hrefFor(7)).toBe('/executions/7');
  });
});

// Phase 20 wiring gate (?raw, App.test.ts's pattern): the "+ New test"
// entry point hides for callers without execution:create (AC14 -- the
// viewer sees no way to start a deploy anywhere, list page included).
describe('Executions gating (phase 20)', () => {
  it('gates the + New test link on the session permission map', async () => {
    const executionsSource = (await import('./Executions.tsx?raw')).default;
    expect(executionsSource).toContain("can('execution', 'create')");
  });
});

// Phase 32, mounted: the global project switcher's stored selection scopes
// the list client-side, with a chip that clears back to all projects.
// Reports.test.tsx's createRoot + act style; fetch stubbed per-URL.
(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const projectsFixture = [
  { id: 1, name: 'phase16-live', owner: 'honryu', tenant_id: 1, created_time: '2026-08-01T00:00:00Z' },
  { id: 2, name: 'phase16-sched', owner: 'heri', tenant_id: 1, created_time: '2026-08-02T00:00:00Z' },
];

const executionsFixture = [
  { id: 7, name: 'alpha-exec', project_id: 1, engine: 'jmeter', created_time: '2026-09-01T00:00:00Z' },
  { id: 3, name: 'beta-exec', project_id: 2, engine: 'jmeter', created_time: '2026-09-02T00:00:00Z' },
  // Phase 49: the calibrate flow mints "calibrate <scenario> <ISO>" names;
  // the row must not leak the raw ISO next to the formatted timestamp.
  { id: 16, name: 'calibrate checkout 2026-09-10T16:47:22.442Z', project_id: 1, engine: 'k6', created_time: '2026-09-10T16:47:22.442Z' },
];

// Phase 40: the Webhooks card mounts whenever a project is selected, so
// the page-level stub must answer its registry route too.
const webhooksFixture = [
  { id: 11, url: 'https://hooks.example.com/runs', has_secret: true, enabled: true, created_by: 'op', created_time: '2026-09-03T00:00:00Z' },
];

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const alicePerms = { '*': ['*'] };

let container: HTMLDivElement | null = null;
let root: ReturnType<typeof createRoot> | null = null;

async function renderExecutionsList(storedProject: string | null = null) {
  if (storedProject !== null) {
    localStorage.setItem(PROJECT_STORAGE_KEY, storedProject);
  }
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/api/me')) {
        return json({ subject: 'demo:alice', name: 'Alice', email: '', global_roles: [], tenants: {}, permissions: alicePerms, demo: true });
      }
      if (url === '/api/executions' || url.endsWith('/api/executions')) {
        return json(executionsFixture);
      }
      if (url.endsWith('/api/projects')) {
        return json(projectsFixture);
      }
      if (url.endsWith('/api/projects/1/webhooks')) {
        return json(webhooksFixture);
      }
      if (url.endsWith('/webhooks')) {
        return json([]);
      }
      return json({ message: `no stub for ${url}` }, 500);
    })
  );
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter initialEntries={['/executions']}>
        <SessionProvider>
          <Executions />
        </SessionProvider>
      </MemoryRouter>
    );
  });
  // Flush the me/executions/projects fetches' promise chains.
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
  localStorage.removeItem(PROJECT_STORAGE_KEY);
});

describe('Executions project filter (phase 32)', () => {
  it('scopes rows to the stored project and shows the project chip', async () => {
    await renderExecutionsList('1');

    const row = container!.querySelector('a[href="/executions/7"]');
    expect(row).not.toBeNull();
    expect(row?.textContent).toContain('alpha-exec');
    expect(container!.querySelector('a[href="/executions/3"]')).toBeNull();
    const chip = container!.querySelector('[data-testid="filter-project"]');
    expect(chip?.textContent).toContain('project: phase16-live');
  });

  it('clearing the chip returns every execution and unscopes the stored selection', async () => {
    await renderExecutionsList('2');

    expect(container!.querySelector('a[href="/executions/7"]')).toBeNull();
    await act(async () => {
      container!
        .querySelector('[data-testid="filter-project"] button')!
        .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await act(async () => {});

    expect(container!.querySelector('a[href="/executions/7"]')).not.toBeNull();
    expect(container!.querySelector('a[href="/executions/3"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="filter-project"]')).toBeNull();
    expect(localStorage.getItem(PROJECT_STORAGE_KEY)).toBe('');
  });

  it('shows every execution when no project is stored', async () => {
    await renderExecutionsList();

    expect(container!.querySelector('a[href="/executions/7"]')).not.toBeNull();
    expect(container!.querySelector('a[href="/executions/3"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="filter-project"]')).toBeNull();
  });

  it('renders one formatted timestamp and no raw ISO in row labels (phase 49)', async () => {
    await renderExecutionsList('1');

    const row = container!.querySelector('a[href="/executions/16"]');
    expect(row).not.toBeNull();
    const text = row!.textContent ?? '';
    // The name shows without its minted ISO suffix...
    expect(text).toContain('calibrate checkout');
    expect(text).not.toContain('2026-09-10T16:47:22.442Z');
    expect(text).not.toMatch(/\d{4}-\d{2}-\d{2}T/);
    // ...and exactly one timestamp, already formatted.
    expect(text).toMatch(/· [A-Z][a-z]{2} \d{1,2}, \d{2}:\d{2}$/);
  });
});

// Phase 40: the Webhooks card is the project-scoped registry surface on
// this page -- present only once a project is selected AND the caller may
// update projects, the same grant the backend's webhook routes demand.
describe('Executions webhooks card mount (phase 40)', () => {
  it('gates the card on the project-update permission', async () => {
    const executionsSource = (await import('./Executions.tsx?raw')).default;
    expect(executionsSource).toContain("can('project', 'update')");
    expect(executionsSource).toContain('<WebhooksCard');
    expect(executionsSource).toContain('<DigestCard');
  });

  it('renders the registry when a project is selected', async () => {
    await renderExecutionsList('1');

    const row = container!.querySelector('[data-testid="webhook-row-11"]');
    expect(row?.textContent).toContain('hooks.example.com/runs');
  });

  it('renders no card without a project selection', async () => {
    await renderExecutionsList();

    expect(container!.querySelector('[data-testid="webhook-row-11"]')).toBeNull();
    expect(container!.querySelector('[data-testid="add-webhook-btn"]')).toBeNull();
  });
});
