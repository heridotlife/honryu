// Phase 94: the scenario Run panel, mounted (createRoot + act, the house
// pattern). The fetch stub serves the panel's own reads — the execution
// config (multi-test, so the co-entry preservation in the edit path is
// pinned from commit one), the lifecycle status, and (task 2) the
// capacity profile + fan-out. Fake timers go live before mount so the
// status poll and (task 3) the countdown's ticker share one clock.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ScenarioRunPanel, { defaultEngine, latestLoadExecution } from './ScenarioRunPanel';
import { SessionProvider } from '../hooks/useSession';
import type { ExecutionSummary } from '../api/generated';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

// The 67a list, newest first: a calibration row (30) NEWER than the
// newest load row (22) — the panel must pick 22. The gatling engine on
// the calibration row is also the default-engine pin (task 3's create
// path reuses it).
const executionsFixture: ExecutionSummary[] = [
  {
    id: 30,
    name: 'calibrate from-baseline 2026-09-18T09:00:00Z',
    project_id: 1,
    engine: 'gatling',
    kind: 'calibrate_engine',
    created_time: '2026-09-18T09:00:00Z',
  },
  {
    id: 22,
    name: 'checkout-load-2',
    project_id: 1,
    engine: 'jmeter',
    kind: 'load',
    created_time: '2026-09-17T12:29:00Z',
  },
  {
    id: 7,
    name: 'checkout-load-1',
    project_id: 1,
    engine: 'jmeter',
    kind: 'load',
    created_time: '2026-09-16T12:29:00Z',
  },
];

// The latest execution's profile: TWO entries — another scenario's (7)
// and this scenario's (42, mode provenance). The edit path must preserve
// entry 7 byte-for-byte while rewriting entry 42.
const configFixture = {
  name: 'checkout-load-2-load',
  project_id: 1,
  execution_id: 22,
  tests: [
    { name: 'other', scenario_id: 7, concurrency: 5, rampup: 30, engines: 1, duration: 300 },
    { name: 'from-baseline', scenario_id: 42, concurrency: 48, rampup: 60, engines: 3, duration: 600, throughput: 200, mode: 'burst' },
  ],
};

const mutable = {
  phase: 'idle' as 'idle' | 'deployed' | 'running',
  config: configFixture as unknown as Record<string, unknown>,
  deployCalls: 0,
  triggerCalls: 0,
};

let container: HTMLDivElement | null = null;
let root: Root | null = null;
let calls: Array<{ method: string; url: string; body?: string }> = [];
let overrides: Array<(method: string, url: string, body?: string) => Response | undefined> = [];
const onExecutionsChanged = vi.fn();
const onOpenCalibration = vi.fn();

function stubFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const method = init?.method ?? 'GET';
      const url = String(input);
      const body = typeof init?.body === 'string' ? init.body : undefined;
      calls.push({ method, url, body });
      for (const override of overrides) {
        const got = override(method, url, body);
        if (got !== undefined) {
          return got;
        }
      }
      if (url.endsWith('/api/me')) {
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
      if (url.endsWith('/api/executions/22/config') && method === 'GET') {
        return json({ 'multi-test': mutable.config });
      }
      if (url.endsWith('/api/executions/22/status')) {
        return json({ phase: mutable.phase, pool_size: 0, status: [] });
      }
      if (url.endsWith('/api/scenarios/42/capacity-profile')) {
        return json({
          scenario_id: 42,
          engine: 'jmeter',
          cpu: '500m',
          memory: '512Mi',
          per_pod_qps: 90,
          saturated_by: 'neither',
          scenario_fingerprint: 'abc',
          calibrated_at: '2026-09-18T08:00:00Z',
          job_id: 5,
        });
      }
      if (url.endsWith('/api/scenarios/42/capacity-profile/fanout')) {
        return json({ status: 'ok', engines: 3 });
      }
      return json({ message: `no stub for ${method} ${url}` }, 500);
    }),
  );
}

interface RenderOpts {
  executions?: ExecutionSummary[];
  lastRun?: { outcome: 'passed' | 'failed'; startedAt: string };
}

async function renderPanel(opts: RenderOpts = {}) {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter>
        <SessionProvider>
          <ScenarioRunPanel
            scenarioId={42}
            scenarioName="from-baseline"
            projectId={1}
            executions={opts.executions ?? executionsFixture}
            executionsError={null}
            lastRun={opts.lastRun ?? { outcome: 'passed', startedAt: '2026-09-17T12:30:00Z' }}
            onExecutionsChanged={onExecutionsChanged}
            onOpenCalibration={onOpenCalibration}
          />
        </SessionProvider>
      </MemoryRouter>,
    );
  });
  await act(async () => {});
}

beforeEach(() => {
  vi.useFakeTimers();
  Object.assign(mutable, { phase: 'idle', config: configFixture, deployCalls: 0, triggerCalls: 0 });
});

afterEach(() => {
  vi.unstubAllGlobals();
  const r = root;
  if (r !== null && container !== null) {
    act(() => {
      r.unmount();
    });
  }
  vi.useRealTimers();
  container?.remove();
  container = null;
  root = null;
  calls = [];
  overrides = [];
  onExecutionsChanged.mockClear();
  onOpenCalibration.mockClear();
});

describe('ScenarioRunPanel — latest execution surfacing', () => {
  it('picks the newest load execution, not a newer calibration row', () => {
    expect(latestLoadExecution(executionsFixture)?.id).toBe(22);
    expect(latestLoadExecution([])?.id).toBeUndefined();
    // The calibration row's engine is the create-path default; jmeter is
    // NewTest's default when no execution names one.
    expect(defaultEngine(executionsFixture)).toBe('gatling');
    expect(defaultEngine([])).toBe('jmeter');
  });

  it('renders the latest execution config: chip, phase, resolved numbers, link, last result', async () => {
    stubFetch();
    await renderPanel();

    // Mode chip: mode · rate · duration (the hub's compact statement).
    expect(container!.querySelector('[data-testid="run-mode-chip"]')?.textContent).toBe('burst · 200 rps · 10m');
    expect(container!.querySelector('[data-testid="run-phase"]')?.textContent).toBe('idle');

    // The resolved statement: target rate, duration, engines, concurrency.
    const resolved = container!.querySelector('[data-testid="run-resolved"]')!;
    const dds = Array.from(resolved.querySelectorAll('dd')).map(d => d.textContent);
    expect(dds).toEqual(['200 req/s', '600s', '3', '48']);

    // The latest execution, linked; the newer calibration row is not.
    const link = container!.querySelector<HTMLAnchorElement>('[data-testid="run-execution-link"]')!;
    expect(link.getAttribute('href')).toBe('/executions/22');
    expect(link.textContent).toContain('checkout-load-2');
    expect(container!.querySelector('a[href="/executions/30"]')).toBeNull();

    // The last run result: the page's probe handed down, badge + start.
    expect(container!.querySelector('[data-testid="run-last-result"]')?.textContent).toContain('passed');
    expect(container!.querySelector('[data-testid="run-last-started"]')?.textContent).toContain('started');

    // The panel fetched exactly the latest execution's config + status.
    expect(calls.some(c => c.url.endsWith('/api/executions/22/config'))).toBe(true);
    expect(calls.some(c => c.url.endsWith('/api/executions/22/status'))).toBe(true);
    expect(calls.some(c => c.url.endsWith('/api/executions/7/config'))).toBe(false);
  });

  it('renders an advanced entry read-only — no mode chip, guidance instead', async () => {
    stubFetch();
    // Serve a config whose entry for scenario 42 has no mode provenance.
    mutable.config = {
      name: 'checkout-load-2-load',
      project_id: 1,
      execution_id: 22,
      tests: [{ name: 'from-baseline', scenario_id: 42, concurrency: 10, rampup: 30, engines: 2, duration: 300 }],
    };
    await renderPanel();

    expect(container!.querySelector('[data-testid="run-mode-chip"]')).toBeNull();
    const note = container!.querySelector('[data-testid="run-advanced-entry"]');
    expect(note?.textContent).toContain('Advanced config');
    expect(note?.querySelector('a')?.getAttribute('href')).toBe('/executions/22');
    // The resolved numbers still show.
    expect(container!.querySelector('[data-testid="run-resolved"]')?.textContent).toContain('unlimited');
  });

  it('shows the empty state when the scenario has no load execution at all', async () => {
    stubFetch();
    await renderPanel({ executions: [] });

    const empty = container!.querySelector('[data-testid="run-empty"]')!;
    expect(empty).not.toBeNull();
    expect(empty.textContent).toContain('No runs yet');
    expect(container!.querySelector('[data-testid="run-panel"]')).toBeNull();
  });

  it('treats a calibration-only history as empty: the create state, not a calibrate row', async () => {
    stubFetch();
    await renderPanel({ executions: [executionsFixture[0]] });

    expect(container!.querySelector('[data-testid="run-empty"]')).not.toBeNull();
    expect(container!.querySelector('a[href="/executions/30"]')).toBeNull();
  });

  it('surfaces the executions error without the panel body', async () => {
    stubFetch();
    await renderPanel();
    // Re-render with the error set (the page keeps the panel mounted).
    await act(async () => {
      root!.render(
        <MemoryRouter>
          <SessionProvider>
            <ScenarioRunPanel
              scenarioId={42}
              scenarioName="from-baseline"
              projectId={1}
              executions={executionsFixture}
              executionsError="Failed to load runs."
              lastRun={undefined}
              onExecutionsChanged={onExecutionsChanged}
              onOpenCalibration={onOpenCalibration}
            />
          </SessionProvider>
        </MemoryRouter>,
      );
    });
    await act(async () => {});

    expect(container!.querySelector('[role="alert"]')?.textContent).toBe('Failed to load runs.');
    expect(container!.querySelector('[data-testid="run-panel"]')).toBeNull();
  });
});
