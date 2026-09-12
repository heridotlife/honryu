// Home (phase 52): the authenticated landing page. Mounted in
// Executions.test.tsx's createRoot + act style -- fetch stubbed per-URL --
// asserting the contract the audit asked for: KPI labels, the five-row
// recent list, and the sparkline container, all from the SAME fetchers the
// deep pages use (no duplicated API client code to test).
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import Home from './Home';
import { PROJECT_STORAGE_KEY } from '../components/ProjectSwitcher';
import { SessionProvider } from '../hooks/useSession';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

// Seven executions: proves the mini-list caps at five rows while the
// "Total executions" KPI counts all of them. Newest first, per the list
// endpoint's contract -- execution 12 leads.
const executionsFixture = [
  { id: 12, name: 'gamma-exec', project_id: 1, engine: 'k6', created_time: '2026-09-12T00:00:00Z' },
  { id: 11, name: 'calibrate checkout 2026-09-11T10:00:00.000Z', project_id: 1, engine: 'k6', kind: 'calibrate_engine', created_time: '2026-09-11T10:00:00Z' },
  { id: 9, name: 'alpha-exec', project_id: 1, engine: 'jmeter', created_time: '2026-09-10T00:00:00Z' },
  { id: 8, name: 'delta-exec', project_id: 2, engine: 'jmeter', created_time: '2026-09-09T00:00:00Z' },
  { id: 7, name: 'epsilon-exec', project_id: 1, engine: 'k6', created_time: '2026-09-08T00:00:00Z' },
  { id: 6, name: 'zeta-exec', project_id: 1, engine: 'k6', created_time: '2026-09-07T00:00:00Z' },
  { id: 5, name: 'eta-exec', project_id: 1, engine: 'k6', created_time: '2026-09-06T00:00:00Z' },
];

// The newest execution's newest report: the "Last run" / p95 KPIs and the
// sparkline all key off run 21.
const latestReport = {
  execution_id: 12,
  scenario_id: 1,
  run_id: 21,
  started_at: '2026-09-12T09:00:00Z',
  ended_at: '2026-09-12T09:01:00Z',
  outcome: 'passed',
  requested: { concurrency: 10, throughput: 100 },
  achieved: { concurrency: 10, throughput: 110, samples: 6600, failed: 0 },
  error_rate: 0,
  latency: { '50': 0.04, '95': 0.18, '99': 0.35 },
  attribution: { target: 2, engine: 0, unknown: 0 },
};

const seriesFixture = {
  points: [
    { ts: 1, vus: 10, rps: 95, err_pct: 0, latency: { '95': 0.18 } },
    { ts: 2, vus: 10, rps: 110, err_pct: 0, latency: { '95': 0.17 } },
    { ts: 3, vus: 10, rps: 120, err_pct: 0, latency: { '95': 0.16 } },
  ],
};

const projectsFixture = [
  { id: 1, name: 'phase16-live', owner: 'honryu', tenant_id: 1, created_time: '2026-08-01T00:00:00Z' },
  { id: 2, name: 'phase16-sched', owner: 'heri', tenant_id: 1, created_time: '2026-08-02T00:00:00Z' },
];

const alice = { subject: 'demo:alice', name: 'Alice', email: '', global_roles: [], tenants: {}, permissions: { '*': ['*'] }, demo: true };

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

let container: HTMLDivElement | null = null;
let root: Root | null = null;

async function renderHome(storedProject: string | null = null) {
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
        return json(alice);
      }
      if (url === '/api/executions' || url.endsWith('/api/executions')) {
        return json(executionsFixture);
      }
      if (url.endsWith('/api/projects')) {
        return json(projectsFixture);
      }
      // The active-runs probe asks each of the newest executions; only 9
      // is running, so the KPI must read exactly 1.
      if (/\/api\/executions\/\d+\/status$/.test(url)) {
        const id = Number(url.match(/\/api\/executions\/(\d+)\/status$/)![1]);
        return json({
          phase: id === 9 ? 'running' : 'idle',
          pool_size: 0,
          status: [{ scenario_id: 1, engines: 1, engines_deployed: id === 9 ? 1 : 0, engines_reachable: true, in_progress: false }],
        });
      }
      if (/\/api\/executions\/\d+\/reports/.test(url)) {
        return json([latestReport]);
      }
      if (/\/api\/runs\/\d+\/series$/.test(url)) {
        return json(seriesFixture);
      }
      return json({ message: `no stub for ${url}` }, 500);
    })
  );
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter initialEntries={['/home']}>
        <SessionProvider>
          <Home />
        </SessionProvider>
      </MemoryRouter>
    );
  });
  // Flush me/executions, then reports/series/statuses.
  await act(async () => {});
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
  localStorage.removeItem(PROJECT_STORAGE_KEY);
});

describe('Home (phase 52)', () => {
  it('renders the four KPI labels with computed values', async () => {
    await renderHome();

    const kpis = container!.querySelector('[data-testid="home-kpis"]')!;
    for (const label of ['Active runs', 'Last run', 'p95', 'Total executions']) {
      expect(kpis.textContent, `missing KPI ${label}`).toContain(label);
    }
    // Active runs: only execution 9's status says "running".
    expect(container!.querySelector('[data-testid="kpi-active-runs"]')!.textContent).toContain('1');
    // Total: all seven executions, not the five-row cap.
    expect(container!.querySelector('[data-testid="kpi-total-executions"]')!.textContent).toContain('7');
    // Last run + p95 come off run 21's report.
    const lastRun = container!.querySelector('[data-testid="kpi-last-run"]')!.textContent ?? '';
    expect(lastRun).toContain('passed');
    const p95 = container!.querySelector('[data-testid="kpi-p95"]')!.textContent ?? '';
    // 0.18s on the wire renders as 180.0 ms.
    expect(p95).toContain('180.0 ms');
    expect(container!.querySelector('[data-testid="kpi-last-run"]')!.getAttribute('href')).toBe('/reports/21');
  });

  it('caps the recent-executions list at five rows, newest first', async () => {
    await renderHome();

    const list = container!.querySelector('[data-testid="home-recent"]')!;
    const rows = Array.from(list.querySelectorAll('a[href^="/executions/"]:not([href="/executions"])'));
    expect(rows.length).toBe(5);
    expect(rows[0].getAttribute('href')).toBe('/executions/12');
    // The calibrate row shows its kind and hides the minted ISO suffix.
    expect(list.textContent).toContain('calibrate_engine');
    expect(list.textContent).not.toContain('2026-09-11T10:00:00.000Z');
  });

  it('renders the throughput sparkline container for the latest run', async () => {
    await renderHome();

    const spark = container!.querySelector('[data-testid="home-sparkline"]');
    expect(spark).not.toBeNull();
    // Sparkline renders a labelled role="img" svg.
    const svg = spark!.querySelector('svg[role="img"]');
    expect(svg).not.toBeNull();
    expect(svg!.getAttribute('aria-label')).toContain('run #21');
  });

  it('scopes KPIs and rows to the stored project selection', async () => {
    await renderHome('2');

    // Project 2 has exactly one execution (id 8), and it is not running.
    expect(container!.querySelector('[data-testid="kpi-total-executions"]')!.textContent).toContain('1');
    const rows = Array.from(container!.querySelectorAll('[data-testid="home-recent"] a[href^="/executions/"]'));
    expect(rows.map((r) => r.getAttribute('href'))).toEqual(['/executions/8']);
  });
});
