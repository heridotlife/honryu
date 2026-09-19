import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import Scenario from './Scenario';
import { SessionProvider } from '../hooks/useSession';
import { PROJECT_STORAGE_KEY } from '../components/ProjectSwitcher';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

// The tabbed scenario detail (phase 95): ONE Run tab — the latest
// execution's run settings (phase 94) with the 67a newest-first history
// and the scenario-scoped calibrate trigger below it — plus Editor
// (phase 65's TaurusEditor page, unchanged). Stale ?tab=runs URLs
// normalize to the default.
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

// The Run panel's own reads (phase 94): the latest execution's (22) load
// config — this scenario's entry carries mode provenance — and its
// lifecycle snapshot. Mounted even when the Run tab is inactive.
const config22 = {
  'multi-test': {
    name: 'checkout-load-2-load',
    project_id: 1,
    execution_id: 22,
    tests: [
      { name: 'from-baseline', scenario_id: 42, concurrency: 48, rampup: 60, engines: 3, duration: 600, throughput: 200, mode: 'burst' },
    ],
  },
};

// The scenario's stored thresholds (phase 72): one p95 ceiling. Overridable
// per test via `overrides`.
let thresholdsFixture: unknown = [
  { id: 3, scenario_id: 42, metric: 'http_p95_ms', comparison: 'lt', value: 300, created_time: '2026-09-17T00:00:00Z' },
];

let container: HTMLDivElement | null = null;
let root: Root | null = null;
// The URL under MemoryRouter: the ?tab=runs normalization test needs to
// see the param actually leave the address bar, not just the right tab
// light up.
let lastSearch = 'unset';
function LocationProbe() {
  const location = useLocation();
  lastSearch = location.search;
  return null;
}
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
      if (url.endsWith('/api/executions/22/config')) {
        return json(config22);
      }
      if (url.endsWith('/api/executions/22/status')) {
        return json({ phase: 'idle', pool_size: 0, status: [] });
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
          <LocationProbe />
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
  // localStorage -- reset it so nothing leaks between tests. The location
  // probe re-fires on every render; reset it so a stale URL from the
  // previous test can never satisfy an assertion.
  localStorage.removeItem(PROJECT_STORAGE_KEY);
  lastSearch = 'unset';
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
  it('shows the header, breadcrumbs, and the Run tab default with the history and Calibrate inside it', async () => {
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

    // Phase 95: the final tab list is [Run, Editor] — Run first and
    // default, the old Runs tab gone entirely (no tab id, no panel).
    expect(container!.querySelector('#tab-run')?.getAttribute('aria-selected')).toBe('true');
    expect(container!.querySelector('#tab-runs')).toBeNull();
    expect(container!.querySelector('#panel-runs')).toBeNull();
    expect(container!.querySelector('#tab-editor')?.getAttribute('aria-selected')).toBe('false');
    expect((container!.querySelector('#panel-run') as HTMLElement).hidden).toBe(false);
    expect((container!.querySelector('#panel-editor') as HTMLElement).hidden).toBe(true);
    expect(container!.querySelector('[data-testid="run-mode-chip"]')?.textContent).toBe('burst · 200 rps · 10m');

    // Phase 95: the history table and the Calibrate control live INSIDE
    // the Run panel now, below the start surface.
    expect(container!.querySelector('#panel-run [data-testid="runs-table"]')).not.toBeNull();
    expect(container!.querySelector('#panel-run [data-testid="calibrate-button"]')).not.toBeNull();

    // Runs table (in the Run panel): wire order preserved (newest first),
    // each row linking the existing run hub.
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
    // The deep link keeps its param — Run is the only canonical no-param
    // address (see the normalization test below).
    expect(lastSearch).toBe('?tab=editor');
    const panel = container!.querySelector('#panel-editor') as HTMLElement;
    expect(panel.hidden).toBe(false);
    // Phase 65's editor, byte-for-byte the same surface: the fragment
    // loaded ("Up to date") and the CodeMirror view mounted.
    expect(panel.textContent).toContain('Requests');
    expect(panel.textContent).toContain('Up to date');
    expect(panel.querySelector('.cm-editor')).not.toBeNull();
  });

  it('normalizes a stale ?tab=runs to the default Run tab with a clean URL', async () => {
    stubFetch();
    await renderScenario('/scenarios/42?tab=runs');

    // Phase 95: the stale id lands on Run (the default), the param leaves
    // the address bar entirely — old bookmarks must not sit on a dead tab
    // id — and the Runs content is right there in the Run panel.
    expect(container!.querySelector('#tab-run')?.getAttribute('aria-selected')).toBe('true');
    expect((container!.querySelector('#panel-run') as HTMLElement).hidden).toBe(false);
    expect(container!.querySelector('#panel-run [data-testid="runs-table"]')).not.toBeNull();
    expect(lastSearch).toBe('');
  });

  it('advanced latest config: the create shape renders inside the Run tab', async () => {
    stubFetch();
    overrides.push((_method, url) => {
      if (url.endsWith('/api/executions/22/config')) {
        return json({
          'multi-test': {
            name: 'checkout-load-2-load',
            project_id: 1,
            execution_id: 22,
            tests: [{ name: 'from-baseline', scenario_id: 42, concurrency: 10, rampup: 30, engines: 2, duration: 300 }],
          },
        });
      }
      return undefined;
    });
    await renderScenario();

    // No mode chip (advanced entry), the guidance note stays, and the
    // ALWAYS-visible inputs arrive as the create shape — with the run
    // history right below inside the same tab.
    expect(container!.querySelector('[data-testid="run-mode-chip"]')).toBeNull();
    expect(container!.querySelector('[data-testid="run-advanced-entry"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="run-create"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="run-create-start"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="run-edit"]')).toBeNull();
    expect(container!.querySelector('#panel-run [data-testid="runs-table"]')).not.toBeNull();
  });

  it('grant-less session: no start inputs in the Run tab, the history still there', async () => {
    stubFetch();
    overrides.push((_method, url) => {
      if (url === '/api/me') {
        return json({
          subject: 'demo:carol',
          name: 'Carol',
          email: '',
          global_roles: [],
          tenants: {},
          permissions: { scenario: ['list', 'read'] },
          demo: true,
        });
      }
      return undefined;
    });
    await renderScenario();

    // The statement stays…
    expect(container!.querySelector('[data-testid="run-resolved"]')).not.toBeNull();
    // …but with neither execution:update (the config PUT) nor
    // execution:create, NO form renders — and without run:create no
    // plain Start either. The Run tab is not the form's hostage: the
    // history table stays (phase 95's RBAC honesty).
    expect(container!.querySelector('[data-testid="run-edit"]')).toBeNull();
    expect(container!.querySelector('[data-testid="run-create"]')).toBeNull();
    expect(container!.querySelector('[data-testid="run-create-start"]')).toBeNull();
    expect(container!.querySelector('[data-testid="run-start"]')).toBeNull();
    expect(container!.querySelector('[data-testid="calibrate-button"]')).toBeNull();
    expect(container!.querySelector('#panel-run [data-testid="runs-table"]')).not.toBeNull();
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

    // Phase 76: the shared EmptyState -- title, guidance, and the ONE
    // action: the existing per-scenario run flow (the 67a calibration
    // trigger), as a Button (never a Link into /executions/, which the
    // e2e harness treats as a run row).
    const empty = container!.querySelector('[data-testid="runs-empty"]')!;
    expect(empty).not.toBeNull();
    expect(empty.querySelector('[data-testid="runs-empty-title"]')?.textContent).toContain('No runs yet');
    expect(empty.querySelector('a[href^="/executions/"]')).toBeNull();
    const action = empty.querySelector<HTMLButtonElement>('[data-testid="runs-empty-action"]')!;
    expect(action?.tagName).toBe('BUTTON');
    expect(action?.textContent).toBe('Run this scenario');
    expect(container!.querySelector('[data-testid="runs-table"]')).toBeNull();

    // The action triggers the same flow the Calibrate button drives.
    await act(async () => {
      action.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    const post = calls.find((c) => c.method === 'POST');
    expect(post?.url).toBe('/api/scenarios/42/calibration/trigger');
  });

  it('keeps the runs empty state message-only without the scenario:create grant', async () => {
    stubFetch();
    overrides.push((_method, url) => {
      if (url === '/api/me') {
        return json({
          subject: 'demo:carol',
          name: 'Carol',
          email: '',
          global_roles: [],
          tenants: {},
          permissions: { scenario: ['list', 'read'] },
          demo: true,
        });
      }
      if (url.endsWith('/api/scenarios/42/executions')) {
        return json([]);
      }
      return undefined;
    });
    await renderScenario();

    expect(container!.querySelector('[data-testid="runs-empty"]')).not.toBeNull();
    // No grant, no dead button: the action is absent, the message stands.
    expect(container!.querySelector('[data-testid="runs-empty-action"]')).toBeNull();
  });

  // e2e selector safety (boot.e2e.mjs scenario D): the harness, having
  // opened a scenario, waits for 'main a[href^="/executions/"]' to click
  // the newest RUN row. The runs-empty branch must therefore render no
  // such anchor at rest -- "Run this scenario" is a Button on purpose --
  // or the harness would click the empty state's action and time out
  // waiting for an execution url that never comes.
  it('renders no ^/executions/ anchor while the runs list is empty (e2e run-row safety)', async () => {
    stubFetch();
    overrides.push((_method, url) => {
      if (url.endsWith('/api/scenarios/42/executions')) {
        return json([]);
      }
      return undefined;
    });
    await renderScenario();

    expect(container!.querySelector('[data-testid="runs-empty"]')).not.toBeNull();
    expect(container!.querySelectorAll('a[href^="/executions/"]').length).toBe(0);
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
