import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import Scenario from './Scenario';
import { SessionProvider } from '../hooks/useSession';
import { PROJECT_STORAGE_KEY } from '../components/ProjectSwitcher';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

// The tabbed scenario detail (phase 67b): Runs (default, the 67a newest-first
// history with per-row newest-report enrichment) and Editor (phase 65's
// TaurusEditor page, unchanged), plus the scenario-scoped calibrate trigger.
// Mounted with stubbed fetch (the house createRoot + act pattern); the
// editor's requests fragment is stubbed because inactive tab panels stay in
// the DOM (Tabs' contract), so TaurusEditor mounts and fetches regardless of
// the active tab.
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const scenarioFixture = {
  id: 42,
  name: 'from-baseline',
  project_id: 1,
  created_time: '2026-09-17T00:00:00Z',
  is_template: true,
  template_name: 'httpbin-baseline',
};

// Newest first is the 67a endpoint's contract; the fixture is already in
// wire order and the test asserts the page preserves it.
const executionsFixture = [
  { id: 22, name: 'checkout-load-2', project_id: 1, engine: 'jmeter', created_time: '2026-09-17T12:29:00Z' },
  { id: 7, name: 'checkout-load-1', project_id: 1, engine: 'jmeter', created_time: '2026-09-16T12:29:00Z' },
];

const report22 = {
  execution_id: 22,
  scenario_id: 42,
  run_id: 30,
  started_at: '2026-09-17T12:30:00Z',
  ended_at: '2026-09-17T12:32:03Z',
  outcome: 'passed',
  requested: { concurrency: 10, throughput: 50 },
  achieved: { samples: 1000, throughput: 49 },
  error_rate: 0,
  latency: {},
  attribution: { target: 1, engine: 0, unknown: 0 },
};

const report7 = {
  ...report22,
  execution_id: 7,
  run_id: 11,
  started_at: '2026-09-16T12:30:00Z',
  ended_at: '2026-09-16T12:30:37Z',
  outcome: 'failed',
};

// The scenario's stored thresholds (phase 72): one p95 ceiling. Overridable
// per test via `overrides`.
let thresholdsFixture: unknown = [
  { id: 3, scenario_id: 42, metric: 'http_p95_ms', comparison: 'lt', value: 300, created_time: '2026-09-17T00:00:00Z' },
];

let container: HTMLDivElement | null = null;
let root: Root | null = null;
// Every (method, url) the page asked for, in order.
let calls: Array<{ method: string; url: string }> = [];
// Overrides applied on top of the default stub, keyed by test.
let overrides: Array<(method: string, url: string) => Response | undefined> = [];

function stubFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const method = init?.method ?? 'GET';
      const url = String(input);
      calls.push({ method, url });
      for (const override of overrides) {
        const got = override(method, url);
        if (got !== undefined) {
          return got;
        }
      }
      if (url.endsWith('/api/me')) {
        // Wildcard permissions: the calibrate button is the scenario:create
        // grant the audit table pins for the 67a trigger route.
        return json({
          subject: 'demo:alice',
          name: 'Alice',
          email: '',
          global_roles: [],
          tenants: {},
          permissions: { '*': ['*'] },
          demo: true,
        });
      }
      if (url.endsWith('/api/scenarios/42')) {
        return json(scenarioFixture);
      }
      if (url.endsWith('/api/scenarios/42/executions')) {
        return json(executionsFixture);
      }
      if (url.startsWith('/api/executions/22/reports')) {
        return json([report22]);
      }
      if (url.startsWith('/api/executions/7/reports')) {
        return json([report7]);
      }
      if (url.endsWith('/api/scenarios/42/requests')) {
        return new Response('default-address: https://httpbin.org\nrequests:\n', {
          status: 200,
          headers: { 'Content-Type': 'text/yaml' },
        });
      }
      // Phase 72: the editor tab's threshold rows (mounted even when the
      // tab is inactive -- Tabs keeps panels in the DOM).
      if (url.endsWith('/api/scenarios/42/thresholds')) {
        return json(thresholdsFixture);
      }
      return json({ message: `no stub for ${url}` }, 500);
    }),
  );
}

async function renderScenario(path = '/scenarios/42') {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter initialEntries={[path]}>
        <SessionProvider>
          <Routes>
            <Route path="/scenarios/:id" element={<Scenario />} />
          </Routes>
        </SessionProvider>
      </MemoryRouter>,
    );
  });
  await act(async () => {});
}

beforeEach(() => {
  // ?tab= rides the URL, but the shared project selection lives in
  // localStorage -- reset it so nothing leaks between tests.
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
  calls = [];
  overrides = [];
});

describe('Scenario detail (phase 67b)', () => {
  it('shows the header, breadcrumbs, and the Runs tab default with history newest-first', async () => {
    stubFetch();
    await renderScenario();

    expect(container!.querySelector('[data-testid="scenario-name"]')?.textContent).toBe('from-baseline');
    const badge = container!.querySelector('[data-testid="template-badge"]');
    expect(badge?.textContent).toContain('httpbin-baseline');

    // Breadcrumbs: Scenarios > {name}, the new third level of depth.
    const crumbs = Array.from(container!.querySelectorAll('[data-testid="breadcrumbs"] li')).map((li) => li.textContent?.trim());
    expect(crumbs).toEqual(['Scenarios', 'from-baseline']);
    const crumbLinks = Array.from(container!.querySelectorAll('[data-testid="breadcrumbs"] a')).map((a) =>
      a.getAttribute('href'),
    );
    expect(crumbLinks).toEqual(['/scenarios']);

    // Tabs render, Runs selected by default, Editor present but hidden
    // (TabPanel keeps inactive panels in the DOM).
    expect(container!.querySelector('#tab-runs')?.getAttribute('aria-selected')).toBe('true');
    expect(container!.querySelector('#tab-editor')?.getAttribute('aria-selected')).toBe('false');
    expect((container!.querySelector('#panel-editor') as HTMLElement).hidden).toBe(true);

    // Runs table: wire order preserved (newest first), each row linking the
    // existing run hub.
    const runLinks = Array.from(container!.querySelectorAll<HTMLAnchorElement>('a[data-testid^="run-link-"]'));
    expect(runLinks.map((a) => a.getAttribute('href'))).toEqual(['/executions/22', '/executions/7']);

    // Status chips (icon + text) from each execution's newest report.
    expect(container!.querySelector('[data-testid="run-status-22"]')?.textContent).toContain('passed');
    expect(container!.querySelector('[data-testid="run-status-22"]')?.querySelector('svg')).not.toBeNull();
    expect(container!.querySelector('[data-testid="run-status-7"]')?.textContent).toContain('failed');

    // Started (midday UTC so the local rendering keeps the date in every
    // realistic timezone) and duration (ended - started).
    expect(container!.textContent).toMatch(/Sep 17, 12:30/);
    expect(container!.textContent).toMatch(/Sep 16, 12:30/);
    expect(container!.textContent).toContain('2m 03s');
    expect(container!.textContent).toContain('37s');
  });

  it('lands on the editor with ?tab=editor and the editor is unchanged', async () => {
    stubFetch();
    await renderScenario('/scenarios/42?tab=editor');

    expect(container!.querySelector('#tab-editor')?.getAttribute('aria-selected')).toBe('true');
    const panel = container!.querySelector('#panel-editor') as HTMLElement;
    expect(panel.hidden).toBe(false);
    // Phase 65's editor, byte-for-byte the same surface: the fragment
    // loaded ("Up to date") and the CodeMirror view mounted.
    expect(panel.textContent).toContain('Requests');
    expect(panel.textContent).toContain('Up to date');
    expect(panel.querySelector('.cm-editor')).not.toBeNull();
  });

  it('calibrate posts to the scenario-scoped trigger and links the job view on 201', async () => {
    stubFetch();
    overrides.push((method, url) => {
      if (method === 'POST' && url.endsWith('/api/scenarios/42/calibration/trigger')) {
        return json({ id: 5, execution_id: 22, phase: 'pending', created_time: '2026-09-17T13:00:00Z' }, 201);
      }
      return undefined;
    });
    await renderScenario();

    container!.querySelector('[data-testid="calibrate-button"]')!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    );
    await act(async () => {});

    // The 67a route, via the generated client.
    const post = calls.find((c) => c.method === 'POST');
    expect(post?.url).toBe('/api/scenarios/42/calibration/trigger');

    // Pending state names the job and links the existing calibration job
    // view: the execution hub, where CapacityPanel mounts for
    // calibrate_engine executions.
    const pending = container!.querySelector('[data-testid="calibration-pending"]');
    expect(pending?.textContent).toContain('job #5');
    expect(pending?.textContent).toContain('pending');
    expect(container!.querySelector<HTMLAnchorElement>('[data-testid="calibration-job-link"]')?.getAttribute('href')).toBe(
      '/executions/22',
    );
  });

  it('surfaces a calibrate failure instead of a pending state', async () => {
    stubFetch();
    overrides.push((method, url) => {
      if (method === 'POST' && url.endsWith('/api/scenarios/42/calibration/trigger')) {
        return json({ message: 'no calibrate_engine execution' }, 400);
      }
      return undefined;
    });
    await renderScenario();

    container!.querySelector('[data-testid="calibrate-button"]')!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    );
    await act(async () => {});

    expect(container!.querySelector('[role="alert"]')?.textContent).toContain('no calibrate_engine execution');
    expect(container!.querySelector('[data-testid="calibration-pending"]')).toBeNull();
  });

  it('shows the runs empty state when the scenario was never executed', async () => {
    stubFetch();
    overrides.push((_method, url) => {
      if (url.endsWith('/api/scenarios/42/executions')) {
        return json([]);
      }
      return undefined;
    });
    await renderScenario();

    expect(container!.querySelector('[data-testid="runs-empty"]')?.textContent).toContain('No runs yet');
    expect(container!.querySelector('[data-testid="runs-table"]')).toBeNull();
  });

  it('surfaces the error when the scenario does not exist', async () => {
    stubFetch();
    overrides.push((_method, url) => {
      if (url.endsWith('/api/scenarios/42')) {
        return json({ message: 'ports: not found' }, 404);
      }
      return undefined;
    });
    await renderScenario();
    expect(container!.querySelector('[role="alert"]')?.textContent).toContain('ports: not found');
    // No editor was wired up for a scenario that never loaded.
    expect(container!.querySelector('.cm-editor')).toBeNull();
  });
});
