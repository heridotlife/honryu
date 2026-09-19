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
import { buildModeTest } from '../lib/modeConfig';

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
      // The capacity routes carry query params (?engine=…&cpu=…); match on
      // the path so the stub is query-shape agnostic.
      const path = url.split('?')[0];
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
      if (path === '/api/scenarios/42/capacity-profile/fanout') {
        return json({ status: 'ok', engines: 3 });
      }
      if (path === '/api/scenarios/42/capacity-profile') {
        return json({
          scenario_id: 42,
          engine: 'gatling',
          cpu: '500m',
          memory: '512Mi',
          per_pod_qps: 90,
          saturated_by: 'neither',
          scenario_fingerprint: 'abc',
          calibrated_at: '2026-09-18T08:00:00Z',
          job_id: 5,
        });
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

/** Native value setter + input event (React's tracker ignores plain writes). */
async function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
}

/** The select flavour of the same (change event). */
async function choose(el: HTMLSelectElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event('change', { bubbles: true }));
  });
}

async function click(el: Element) {
  await act(async () => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

const byId = <T extends HTMLElement>(id: string) => container!.querySelector(`[data-testid="${id}"]`) as T;

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

describe('ScenarioRunPanel — inline mode/qps/duration edit', () => {
  it('prefills the form from the stored entry and shows the capacity hint under the qps input', async () => {
    stubFetch();
    await renderPanel();

    expect(byId('run-edit')).not.toBeNull();
    expect((byId('mode-select') as HTMLSelectElement).value).toBe('burst');
    expect((byId('mode-qps') as HTMLInputElement).value).toBe('200');
    expect((byId('mode-duration') as HTMLInputElement).value).toBe('10');
    expect((byId('mode-duration-unit') as HTMLSelectElement).value).toBe('m');

    // The stubs' profile (90 qps/pod) and fan-out (3 engines at 200 qps).
    expect(byId('mode-qps-hint')?.textContent).toBe('profile: ~90 qps/pod, 3 engines');
  });

  it('PUTs the restated entry byte-identical to NewTest Simple, co-entries untouched', async () => {
    stubFetch();
    await renderPanel();

    // Restate: ramp, 500 qps, 1 hour.
    await choose(byId('mode-select') as HTMLSelectElement, 'ramp');
    await type(byId('mode-qps') as HTMLInputElement, '500');
    await type(byId('mode-duration') as HTMLInputElement, '1');
    await choose(byId('mode-duration-unit') as HTMLSelectElement, 'h');
    await click(byId('run-apply'));

    const put = calls.find(c => c.method === 'PUT' && c.url.endsWith('/api/executions/22/config'));
    expect(put).toBeDefined();
    const body = JSON.parse(put!.body as string);

    // The edited entry is byte-identical to what NewTest's Simple submit
    // builds for the same statement — same keys, same order, zeros for the
    // eagerly re-resolved fields.
    const edited = body.tests.find((t: { scenario_id: number }) => t.scenario_id === 42);
    expect(JSON.stringify(edited)).toBe(
      JSON.stringify(buildModeTest('from-baseline', 42, { mode: 'ramp', qps: 500, duration: 1, unit: 'h' }))
    );

    // The co-execution scenario's entry round-trips untouched.
    const sibling = body.tests.find((t: { scenario_id: number }) => t.scenario_id === 7);
    expect(sibling).toEqual(configFixture.tests[0]);

    // The envelope keeps the execution's identity.
    expect(body.name).toBe('checkout-load-2-load');
    expect(body.project_id).toBe(1);
    expect(body.execution_id).toBe(22);
  });

  it('carries NewTest validation: the soak warning appears for short soaks', async () => {
    stubFetch();
    await renderPanel();

    await choose(byId('mode-select') as HTMLSelectElement, 'soak');
    // 10 minutes: under the 30-minute soak guidance.
    expect(byId('soak-warning')).not.toBeNull();
    // Apply stays clickable (soft guidance, not a block) but a zero qps
    // does block it.
    expect((byId('run-apply') as HTMLButtonElement).disabled).toBe(false);
    await type(byId('mode-qps') as HTMLInputElement, '0');
    expect((byId('run-apply') as HTMLButtonElement).disabled).toBe(true);
    expect(byId('mode-qps-error')).not.toBeNull();
  });

  it('surfaces a 409 no-profile refusal with the remediation loop to the Runs tab', async () => {
    stubFetch();
    overrides.push((method, url) => {
      if (method === 'PUT' && url.endsWith('/api/executions/22/config')) {
        return json(
          {
            message: 'executionapp: mode config refused: capacity profile status "no_profile" for scenario 42 on jmeter (500m CPU / 512Mi memory)',
            details: {
              fanout_status: 'no_profile',
              hint: 'calibrate this scenario first (Runs tab → Calibrate), then re-apply',
            },
          },
          409
        );
      }
      return undefined;
    });
    await renderPanel();

    await click(byId('run-apply'));

    // The existing structured copy (message + hint via ActionErrorDetails)…
    expect(container!.querySelector('[role="alert"]')?.textContent).toContain('no_profile');
    expect(container!.querySelector('[data-testid="action-error-details"] code')?.textContent).toContain(
      'calibrate this scenario first'
    );
    // …plus the loop-closer: a jump to this page's Calibrate action.
    const remediation = byId('run-calibrate-remediation');
    expect(remediation?.textContent).toContain('Calibrate this scenario first');
    await click(byId('run-calibrate-link'));
    expect(onOpenCalibration).toHaveBeenCalledTimes(1);
  });

  it('hides the editor without the execution:update grant', async () => {
    stubFetch();
    overrides.push((_method, url) => {
      if (url.endsWith('/api/me')) {
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
    await renderPanel();

    // The statement stays; the editor is absent — no dead form.
    expect(byId('run-resolved')).not.toBeNull();
    expect(byId('run-edit')).toBeNull();
  });

  it('drops the hint when no profile exists for the engine', async () => {
    stubFetch();
    overrides.push((_method, url) => {
      if (url.split('?')[0].endsWith('/api/scenarios/42/capacity-profile')) {
        return json({ message: 'no profile' }, 404);
      }
      return undefined;
    });
    await renderPanel();

    expect(byId('mode-qps-hint')).toBeNull();
  });
});
