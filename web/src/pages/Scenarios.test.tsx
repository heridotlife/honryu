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
  {
    id: 11,
    name: 'checkout-baseline',
    project_id: 1,
    kind: 'portable',
    created_time: '2026-09-17T00:00:00Z',
    // Phase 70: the list endpoint batches each scenario's last-run verdict
    // into the row -- the page never probes per row.
    last_run: { execution_id: 77, outcome: 'passed', started_at: '2026-09-17T14:05:00Z' },
  },
  // is_template never rides GET /api/scenarios today (the endpoint excludes
  // templates); this row pins the "if included" badge branch anyway. Its
  // last_run is null: no run has finalised a report, so the cell says so.
  {
    id: 12,
    name: 'search-smoke',
    project_id: 2,
    kind: 'native',
    is_template: true,
    template_name: 'search-baseline',
    created_time: '2026-09-17T01:00:00Z',
    last_run: null,
  },
];

// The same rows with the last_run key absent entirely (an older spec or a
// proxy that stripped it): the page must read that exactly like null.
const scenariosWithoutLastRun = [
  { id: 11, name: 'checkout-baseline', project_id: 1, kind: 'portable', created_time: '2026-09-17T00:00:00Z' },
  { id: 12, name: 'search-smoke', project_id: 2, kind: 'native', created_time: '2026-09-17T01:00:00Z' },
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
  vi.unstubAllEnvs();
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

  it('badges a template only when a row carries is_template', async () => {
    stubFetch(scenariosFixture);
    await renderScenarios();

    // The badge branch: present on the row that has is_template, absent otherwise.
    expect(container!.querySelectorAll('[data-testid="template-badge"]').length).toBe(1);
    expect(container!.querySelector('[data-testid="template-badge"]')?.textContent).toContain('search-baseline');
  });

  it('shows the batched last-run verdict as badge + time, and a dash when there is none', async () => {
    vi.stubEnv('TZ', 'UTC');
    stubFetch(scenariosFixture);
    await renderScenarios();

    const cells = Array.from(container!.querySelectorAll<HTMLTableCellElement>('[data-testid="last-run-cell"]'));
    expect(cells.length).toBe(2);

    // Row with a verdict: RunStatusBadge (icon + word, never color-only)
    // beside the run's start in the one-short-timestamp house style.
    expect(cells[0].textContent).toContain('passed');
    expect(cells[0].querySelector('svg')).not.toBeNull();
    expect(cells[0].textContent).toContain('Sep 17, 14:05');

    // Row with last_run null: the honest dash -- "no verdict yet".
    expect(cells[1].textContent).toBe('—');
  });

  it('reads a missing last_run key exactly like null', async () => {
    stubFetch(scenariosWithoutLastRun);
    await renderScenarios();

    const cells = Array.from(container!.querySelectorAll<HTMLTableCellElement>('[data-testid="last-run-cell"]'));
    expect(cells.length).toBe(2);
    for (const cell of cells) {
      expect(cell.textContent).toBe('—');
      expect(cell.querySelector('svg')).toBeNull();
    }
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

    // Phase 76: the shared EmptyState -- title + description + the ONE
    // action (the template picker, i.e. NewTest).
    const empty = container!.querySelector('[data-testid="scenarios-empty"]')!;
    expect(empty).not.toBeNull();
    expect(empty.querySelector('[data-testid="scenarios-empty-title"]')?.textContent).toContain('No scenarios yet');
    const action = empty.querySelector<HTMLAnchorElement>('[data-testid="scenarios-empty-action"]')!;
    expect(action?.getAttribute('href')).toBe('/executions/new');
    expect(action?.textContent).toBe('Create from template');
    expect(container!.querySelector('[data-testid="scenarios-table"]')).toBeNull();
  });

  it('keeps the project-scoped empty wording distinct from the all-projects one', async () => {
    localStorage.setItem(PROJECT_STORAGE_KEY, '2');
    stubFetch([]);
    await renderScenarios();

    const empty = container!.querySelector('[data-testid="scenarios-empty"]')!;
    expect(empty.querySelector('[data-testid="scenarios-empty-description"]')?.textContent).toContain(
      'No scenarios in this project yet',
    );
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
