import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import Scenarios from './Scenarios';
import { PROJECT_STORAGE_KEY } from '../components/ProjectSwitcher';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

// The scenario-first list (phase 67b), mounted with stubbed fetch per URL
// (the house createRoot + act pattern). The page also pulls the project
// list through the shared useProjectSelection hook, so every stub answers
// /api/projects too.
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const projectsFixture = [
  { id: 1, name: 'checkout', owner: 'honryu', tenant_id: 1, created_time: '2026-08-01T00:00:00Z' },
  { id: 2, name: 'search', owner: 'heri', tenant_id: 1, created_time: '2026-08-02T00:00:00Z' },
];

const scenariosFixture = [
  { id: 11, name: 'checkout-baseline', project_id: 1, kind: 'portable', created_time: '2026-09-17T00:00:00Z' },
  // is_template never rides GET /api/scenarios today (the endpoint excludes
  // templates); this row pins the "if included" badge branch anyway.
  { id: 12, name: 'search-smoke', project_id: 2, kind: 'native', is_template: true, template_name: 'search-baseline', created_time: '2026-09-17T01:00:00Z' },
];

let container: HTMLDivElement | null = null;
let root: Root | null = null;
// Every /api/scenarios URL the page asked for, in order.
let scenarioFetches: string[] = [];

function stubFetch(scenarios: unknown[] | (() => unknown[]), status = 200) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/api/projects')) {
        return json(projectsFixture);
      }
      if (url.endsWith('/api/scenarios') || url.includes('/api/scenarios?')) {
        scenarioFetches.push(url);
        // The endpoint filters server-side, so the stub honours the query
        // the same way: project 2 contributes only its own row.
        const rows = typeof scenarios === 'function' ? scenarios() : scenarios;
        const body = url.includes('project_id=2')
          ? (rows as Array<{ project_id?: number }>).filter((s) => s.project_id === 2)
          : rows;
        return json(body, status);
      }
      return json({ message: `no stub for ${url}` }, 500);
    }),
  );
}

async function renderScenarios() {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter>
        <Scenarios />
      </MemoryRouter>,
    );
  });
  await act(async () => {});
}

beforeEach(() => {
  // The shared project selection lives in localStorage; a stale value from
  // another test would turn every fetch into ?project_id=N.
  localStorage.removeItem(PROJECT_STORAGE_KEY);
});

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
  scenarioFetches = [];
});

describe('Scenarios page (phase 67b)', () => {
  it('renders rows with project names, kind, and links into the detail page', async () => {
    stubFetch(scenariosFixture);
    await renderScenarios();

    const links = Array.from(container!.querySelectorAll<HTMLAnchorElement>('a[data-testid^="scenario-link-"]'));
    expect(links.map((a) => a.getAttribute('href'))).toEqual(['/scenarios/11', '/scenarios/12']);
    expect(container!.textContent).toContain('checkout-baseline');
    expect(container!.textContent).toContain('search-smoke');
    // Project names resolved from the projects fetcher, not raw ids.
    expect(container!.textContent).toContain('checkout');
    expect(container!.textContent).toContain('search');
    // Kind column.
    expect(container!.textContent).toContain('portable');
    expect(container!.textContent).toContain('native');
  });

  it('badges a template only when a row carries is_template, and blanks last-run honestly', async () => {
    stubFetch(scenariosFixture);
    await renderScenarios();

    // The badge branch: present on the row that has is_template, absent otherwise.
    expect(container!.querySelectorAll('[data-testid="template-badge"]').length).toBe(1);
    expect(container!.querySelector('[data-testid="template-badge"]')?.textContent).toContain('search-baseline');

    // Last run: GET /api/scenarios carries no last-run status and deriving
    // it client-side is a per-scenario fan-out, so the cell renders an
    // honest unknown dash -- one per row, never a fabricated status.
    expect(container!.querySelectorAll('[data-testid="last-run-cell"]').length).toBe(2);
    expect(container!.querySelector('[data-testid="last-run-cell"]')?.textContent).toBe('—');
  });

  it('refetches with ?project_id= when the project filter changes', async () => {
    stubFetch(scenariosFixture);
    await renderScenarios();
    expect(scenarioFetches).toEqual(['/api/scenarios']);

    const select = container!.querySelector<HTMLSelectElement>('[data-testid="project-filter"]')!;
    select.value = '2';
    select.dispatchEvent(new Event('change', { bubbles: true }));
    await act(async () => {});

    // The dropdown drives a server-side refetch (the endpoint's own
    // filter), and the selection syncs through the shared hook.
    expect(scenarioFetches).toEqual(['/api/scenarios', '/api/scenarios?project_id=2']);
    expect(container!.querySelectorAll('a[data-testid^="scenario-link-"]').length).toBe(1);
    expect(container!.textContent).toContain('search-smoke');
  });

  it('shows the empty state when the caller has no scenarios', async () => {
    stubFetch([]);
    await renderScenarios();

    expect(container!.querySelector('[data-testid="scenarios-empty"]')?.textContent).toContain('No scenarios yet');
    expect(container!.querySelector('[data-testid="scenarios-table"]')).toBeNull();
  });

  it('shows a loading skeleton while the fetch is in flight', async () => {
    let release!: (value: Response) => void;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.endsWith('/api/projects')) {
          return json(projectsFixture);
        }
        if (url.endsWith('/api/scenarios')) {
          return new Promise<Response>((resolve) => {
            release = resolve;
          });
        }
        return json({ message: `no stub for ${url}` }, 500);
      }),
    );
    await renderScenarios();

    expect(container!.querySelector('[data-testid="scenarios-loading"]')).not.toBeNull();
    await act(async () => {
      release(json(scenariosFixture));
    });
    expect(container!.querySelector('[data-testid="scenarios-loading"]')).toBeNull();
    expect(container!.querySelector('[data-testid="scenarios-table"]')).not.toBeNull();
  });
});
