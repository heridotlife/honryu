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
import { COUNTDOWN_STORAGE_KEY } from '../lib/countdownPref';
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
    {
      name: 'from-baseline',
      scenario_id: 42,
      concurrency: 48,
      rampup: 60,
      engines: 3,
      duration: 600,
      throughput: 200,
      mode: 'burst',
    },
  ],
};

const mutable = {
  phase: 'idle' as 'idle' | 'deployed' | 'running',
  config: configFixture as unknown as Record<string, unknown>,
  deployStatus: 200,
  hangDeploy: false,
  deployCalls: 0,
  triggerCalls: 0,
  putConfigStatus: 200,
  // Phase 97: the scenario's stored threshold set (GET) and the PUT's
  // adopted answer — the SLO suggestion's zero-thresholds premise.
  thresholds: [] as Array<{ metric: string; comparison: string; value: number }>,
};

let container: HTMLDivElement | null = null;
let root: Root | null = null;
let calls: Array<{ method: string; url: string; body?: string }> = [];
let overrides: Array<(method: string, url: string, body?: string) => Response | undefined> = [];
const onExecutionsChanged = vi.fn();
let lastOpts: RenderOpts = {};

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
      if (path === '/api/executions/22/deploy') {
        mutable.deployCalls++;
        if (mutable.hangDeploy) {
          return new Promise<Response>(() => {});
        }
        if (mutable.deployStatus === 200) {
          mutable.phase = 'deployed';
        }
        return json({ message: 'engines deploying' }, mutable.deployStatus);
      }
      if (path === '/api/executions/22/trigger') {
        mutable.triggerCalls++;
        mutable.phase = 'running';
        return json({ message: 'run triggered' });
      }
      // The empty-state create path: POST /executions mints id 99, its
      // config PUT is captured by the test, its lifecycle mirrors 22's.
      if (path === '/api/executions' && method === 'POST') {
        return json({ id: 99 }, 201);
      }
      if (path === '/api/executions/99/config' && method === 'PUT') {
        if (mutable.putConfigStatus === 200) {
          mutable.config = {
            name: 'from-baseline-load',
            project_id: 1,
            execution_id: 99,
            tests: [
              {
                name: 'from-baseline',
                scenario_id: 42,
                concurrency: 12,
                rampup: 60,
                engines: 3,
                duration: 600,
                throughput: 100,
                mode: 'burst',
              },
            ],
          };
        }
        return json({ message: 'mode config refused: no profile' }, mutable.putConfigStatus);
      }
      if (path === '/api/executions/99/config' && method === 'GET') {
        return json({ 'multi-test': mutable.config });
      }
      if (path === '/api/executions/99/deploy') {
        mutable.deployCalls++;
        if (mutable.hangDeploy) {
          return new Promise<Response>(() => {});
        }
        if (mutable.deployStatus === 200) {
          mutable.phase = 'deployed';
        }
        return json({ message: 'engines deploying' }, mutable.deployStatus);
      }
      if (path === '/api/executions/99/trigger') {
        mutable.triggerCalls++;
        mutable.phase = 'running';
        return json({ message: 'run triggered' });
      }
      if (path === '/api/executions/99/status') {
        return json({ phase: mutable.phase, pool_size: 0, status: [] });
      }
      if (path === '/api/executions/22/status') {
        return json({ phase: mutable.phase, pool_size: 0, status: [] });
      }
      if (path === '/api/scenarios/42/thresholds' && method === 'GET') {
        return json(mutable.thresholds);
      }
      if (path === '/api/scenarios/42/thresholds' && method === 'PUT') {
        // The replace-all route's contract: the stored set (ids assigned)
        // comes back and becomes the caller's new state.
        mutable.thresholds = (JSON.parse(body ?? '{}').thresholds ?? []) as Array<{
          metric: string;
          comparison: string;
          value: number;
        }>;
        return json(mutable.thresholds.map((t, i) => ({ id: i + 1, ...t })));
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
    })
  );
}

interface RenderOpts {
  executions?: ExecutionSummary[];
  lastRun?: { outcome: 'passed' | 'failed'; startedAt: string };
}

async function renderPanel(opts: RenderOpts = {}) {
  stubFetch();
  lastOpts = opts;
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
          />
        </SessionProvider>
      </MemoryRouter>
    );
  });
  await act(async () => {});
}

/** The page's refetch, simulated: re-render with updated props (the page
 *  keeps the panel mounted while its list reloads). */
async function rerenderPanel(patch: Partial<RenderOpts> = {}) {
  lastOpts = { ...lastOpts, ...patch };
  await act(async () => {
    root!.render(
      <MemoryRouter>
        <SessionProvider>
          <ScenarioRunPanel
            scenarioId={42}
            scenarioName="from-baseline"
            projectId={1}
            executions={lastOpts.executions ?? executionsFixture}
            executionsError={null}
            lastRun={lastOpts.lastRun ?? { outcome: 'passed', startedAt: '2026-09-17T12:30:00Z' }}
            onExecutionsChanged={onExecutionsChanged}
          />
        </SessionProvider>
      </MemoryRouter>
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

const advance = async (ms: number) => {
  await act(async () => {
    vi.advanceTimersByTime(ms);
  });
};

const countdown = () => container!.querySelector('[data-testid="start-countdown"]');
const remainingText = () => container!.querySelector('[data-testid="start-countdown-remaining"]')?.textContent ?? '';

const byId = <T extends HTMLElement>(id: string) => container!.querySelector(`[data-testid="${id}"]`) as T;

beforeEach(() => {
  vi.useFakeTimers();
  // Phase 96: the countdown preference persists across tests within this
  // file's jsdom storage — reset to absent (the 10s default) each time.
  localStorage.removeItem(COUNTDOWN_STORAGE_KEY);
  Object.assign(mutable, {
    phase: 'idle',
    config: configFixture,
    deployStatus: 200,
    hangDeploy: false,
    deployCalls: 0,
    triggerCalls: 0,
    putConfigStatus: 200,
    thresholds: [],
  });
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
  lastOpts = {};
  onExecutionsChanged.mockClear();
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

  it('renders an advanced entry: no mode chip, the guidance note, and the CREATE shape below', async () => {
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
    // Phase 95: the inputs are ALWAYS there for a session that can run —
    // an advanced latest gets the CREATE shape (new run = new execution,
    // expected) instead of a dead panel. Its submit is the start path,
    // so the plain Start control is absent.
    expect(byId('run-create')).not.toBeNull();
    expect(byId('run-create-start')).not.toBeNull();
    expect((byId('mode-qps') as HTMLInputElement).value).toBe('100');
    expect(byId('run-edit')).toBeNull();
    expect(byId('run-start')).toBeNull();
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
            />
          </SessionProvider>
        </MemoryRouter>
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
      JSON.stringify(buildModeTest('from-baseline', 42, { mode: 'ramp', qps: 500, duration: 1, unit: 'h', steps: 5 }))
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

  it('surfaces a 409 no-profile refusal with the remediation naming this tab’s Calibrate', async () => {
    stubFetch();
    overrides.push((method, url) => {
      if (method === 'PUT' && url.endsWith('/api/executions/22/config')) {
        return json(
          {
            message:
              'executionapp: mode config refused: capacity profile status "no_profile" for scenario 42 on jmeter (500m CPU / 512Mi memory)',
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
    // …plus the loop-closer: the remediation names the Calibrate action
    // that sits below the run history on this same tab (phase 95 merged
    // the old Runs tab into Run — no tab jump left to make).
    const remediation = byId('run-calibrate-remediation');
    expect(remediation?.textContent).toContain('Calibrate this scenario first');
    expect(remediation?.textContent).toContain('below the run history');
    expect(byId('run-calibrate-link')).toBeNull();
  });

  it('hides every form shape without update-or-create grants — the statement stays', async () => {
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

    // The statement stays; NEITHER form shape mounts — phase 95's hide
    // rule is "cannot PUT the config (execution:update) AND cannot create
    // an execution (execution:create)" — and without run:create there is
    // no plain Start either. No dead form, no dead button.
    expect(byId('run-resolved')).not.toBeNull();
    expect(byId('run-edit')).toBeNull();
    expect(byId('run-create')).toBeNull();
    expect(byId('run-create-start')).toBeNull();
    expect(byId('run-start')).toBeNull();
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

describe('ScenarioRunPanel — always-visible inputs (phase 95)', () => {
  it('create-only session on a mode entry: the create shape, not a dead panel', async () => {
    stubFetch();
    // execution:create + run:create but NOT execution:update: the mode
    // entry cannot be PUT in place, so the unified form falls to the
    // create shape — starting a run forks to a fresh execution (the
    // honest path when restating the entry is not granted).
    overrides.push((_method, url) => {
      if (url.endsWith('/api/me')) {
        return json({
          subject: 'demo:dave',
          name: 'Dave',
          email: '',
          global_roles: [],
          tenants: {},
          permissions: { execution: ['create'], run: ['create'] },
          demo: true,
        });
      }
      return undefined;
    });
    await renderPanel();

    // The mode-entry statement is intact — chip and resolved numbers…
    expect(container!.querySelector('[data-testid="run-mode-chip"]')?.textContent).toBe('burst · 200 rps · 10m');
    expect(byId('run-resolved')).not.toBeNull();
    // …but the edit form needs execution:update: the create shape
    // renders instead, and its submit is the start path (no plain Start).
    expect(byId('run-edit')).toBeNull();
    expect(byId('run-create')).not.toBeNull();
    expect(byId('run-create-start')).not.toBeNull();
    expect((byId('mode-qps') as HTMLInputElement).value).toBe('100');
    expect(byId('run-start')).toBeNull();
  });

  it('advanced latest with the old execution DEPLOYED: create+start deploys the NEW execution', async () => {
    // The stale-phase pin. The create form now renders alongside a
    // latest execution, so the flow that auto-begins when the refetched
    // list names the new run latest must not inherit the OLD execution's
    // 'deployed' — that would skip the new execution's deploy POST and
    // trigger undeployed engines straight from the countdown.
    mutable.config = {
      name: 'checkout-load-2-load',
      project_id: 1,
      execution_id: 22,
      tests: [{ name: 'from-baseline', scenario_id: 42, concurrency: 10, rampup: 30, engines: 2, duration: 300 }],
    };
    mutable.phase = 'deployed';
    await renderPanel();
    await advance(500);

    await click(byId('run-create-start'));

    // Create: POST /executions, then the config PUT on the new id.
    expect(calls.some(c => c.method === 'POST' && c.url.endsWith('/api/executions'))).toBe(true);
    expect(calls.some(c => c.method === 'PUT' && c.url.endsWith('/api/executions/99/config'))).toBe(true);
    expect(onExecutionsChanged).toHaveBeenCalledTimes(1);

    // The refetched list names 99 latest — the flow begins on its own…
    const executions99: ExecutionSummary[] = [
      {
        id: 99,
        name: 'from-baseline',
        project_id: 1,
        engine: 'gatling',
        kind: 'load',
        created_time: '2026-09-19T10:00:00Z',
      },
    ];
    await rerenderPanel({ executions: executions99 });

    // …and takes the deploy-first path: /executions/99/deploy fires even
    // though execution 22 was 'deployed' when Create and start was
    // clicked (the stale snapshot would have opened the countdown with
    // deployCalls === 0).
    expect(mutable.deployCalls).toBe(1);
    expect(calls.some(c => c.method === 'POST' && c.url.endsWith('/api/executions/99/deploy'))).toBe(true);
    expect(countdown()).not.toBeNull();

    await advance(10_000);
    expect(mutable.triggerCalls).toBe(1);
  });
});

describe('ScenarioRunPanel — start flow and empty-state create', () => {
  /** The common prefix: mount at clock 0, offset 500ms (the 10s status
   * poll and the countdown's final tick stay half a second apart), click
   * Start, let the deploy POST and its immediate status refresh land —
   * the countdown is up at remaining=10. */
  async function reachCountdown() {
    await renderPanel();
    await advance(500);
    await click(byId('run-start'));
    await act(async () => {});
    expect(countdown()).not.toBeNull();
    expect(remainingText()).toBe('Load test starts in 10s');
  }

  it('offers Start (idle) and chains deploy → countdown → trigger', async () => {
    await reachCountdown();

    expect(mutable.deployCalls).toBe(1);
    expect(mutable.triggerCalls).toBe(0);
    expect(byId('run-controls').getAttribute('aria-busy')).toBe('true');

    // The countdown runs out into the trigger; the flow hands control back
    // (running note in place of the Start button).
    await advance(10_000);
    expect(mutable.triggerCalls).toBe(1);
    expect(countdown()).toBeNull();
    expect(byId('run-start')).toBeNull();
    expect(byId('run-running-note')?.textContent).toContain('view execution #22');
    expect(byId('run-controls').getAttribute('aria-busy')).toBe('false');
  });

  it('deployed skips the deploy POST and opens the countdown directly', async () => {
    mutable.phase = 'deployed';
    await renderPanel();
    await advance(500);

    await click(byId('run-start'));

    expect(countdown()).not.toBeNull();
    expect(container!.querySelector('[data-testid="start-flow-deploying"]')).toBeNull();
    expect(mutable.deployCalls).toBe(0);

    await advance(10_000);
    expect(mutable.triggerCalls).toBe(1);
  });

  it('cancel during the countdown returns the Start control without triggering', async () => {
    await reachCountdown();

    await click(byId('start-countdown-cancel'));

    expect(countdown()).toBeNull();
    expect(mutable.triggerCalls).toBe(0);
    // Deployed by the earlier deploy: Start is back and enabled (the
    // straight-countdown retry path).
    expect((byId('run-start') as HTMLButtonElement).disabled).toBe(false);
    expect(byId('run-controls').getAttribute('aria-busy')).toBe('false');

    // The countdown's timer is dead: running out the original 10s fires
    // nothing.
    await advance(30_000);
    expect(mutable.triggerCalls).toBe(0);
  });

  it('cancel during the deploying wait escapes, and busy locks the editor', async () => {
    mutable.hangDeploy = true;
    await renderPanel();
    await advance(500);

    // Editor is live before the flow.
    expect((byId('mode-qps') as HTMLInputElement).disabled).toBe(false);
    await click(byId('run-start'));

    const status = byId('start-flow-deploying');
    expect(status?.textContent).toContain('Deploying engines…');
    // Busy disables editing: inputs locked, Apply dead.
    expect((byId('mode-qps') as HTMLInputElement).disabled).toBe(true);
    expect((byId('mode-select') as HTMLSelectElement).disabled).toBe(true);
    expect((byId('run-apply') as HTMLButtonElement).disabled).toBe(true);

    await click(status.querySelector('[data-testid="start-flow-cancel"]')!);
    expect(byId('start-flow-deploying')).toBeNull();
    expect((byId('mode-qps') as HTMLInputElement).disabled).toBe(false);
    expect((byId('run-apply') as HTMLButtonElement).disabled).toBe(false);
  });

  it('never-run: create sends NewTest Simple\u2019s exact payload, then starts the new execution', async () => {
    // Calibration-only history: no LOAD execution, so the create state
    // renders — and the calibration row names the engine a new run uses.
    await renderPanel({ executions: [executionsFixture[0]] });

    // The create form prefills the Simple defaults and shows the hint.
    expect((byId('mode-qps') as HTMLInputElement).value).toBe('100');
    await click(byId('run-create-start'));

    // POST /executions: form-encoded identity, engine from the newest
    // row (the calibration row's gatling).
    const post = calls.find(c => c.method === 'POST' && c.url.endsWith('/api/executions'))!;
    expect(post.body).toBe('project_id=1&name=from-baseline&engine=gatling');

    // PUT config: the single mode entry, byte-identical to NewTest Simple.
    const put = calls.find(c => c.method === 'PUT' && c.url.endsWith('/api/executions/99/config'))!;
    const body = JSON.parse(put.body as string);
    expect(body.name).toBe('from-baseline-load');
    expect(body.project_id).toBe(1);
    expect(body.execution_id).toBe(99);
    expect(JSON.stringify(body.tests[0])).toBe(
      JSON.stringify(buildModeTest('from-baseline', 42, { mode: 'burst', qps: 100, duration: 10, unit: 'm', steps: 5 }))
    );

    // The panel asked the page to refetch; simulate the refetched list
    // naming the new execution latest — the flow begins on its own.
    expect(onExecutionsChanged).toHaveBeenCalledTimes(1);
    const executions99: ExecutionSummary[] = [
      {
        id: 99,
        name: 'from-baseline',
        project_id: 1,
        engine: 'gatling',
        kind: 'load',
        created_time: '2026-09-19T10:00:00Z',
      },
    ];
    await rerenderPanel({ executions: executions99 });

    // Idle chain against the NEW id: deploy → countdown.
    expect(mutable.deployCalls).toBe(1);
    expect(countdown()).not.toBeNull();
    expect(remainingText()).toBe('Load test starts in 10s');

    await advance(10_000);
    expect(mutable.triggerCalls).toBe(1);
  });

  it('never-run create refused with 409: remediation copy and the Calibrate link', async () => {
    mutable.putConfigStatus = 409;
    await renderPanel({ executions: [] });

    await click(byId('run-create-start'));

    expect(container!.querySelector('[role="alert"]')?.textContent).toContain('no profile');
    // The honest half-state is named: the execution exists, unconfigured;
    // the remediation names this tab's Calibrate (no jump link left).
    expect(byId('run-calibrate-remediation')?.textContent).toContain('the run was created but not configured');
    expect(byId('run-calibrate-remediation')?.textContent).toContain('below the run history');
    expect(byId('run-calibrate-link')).toBeNull();
    // No flow began.
    expect(mutable.deployCalls).toBe(0);
    expect(countdown()).toBeNull();
  });

  it('never-run without the execution:create grant renders message-only', async () => {
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
    await renderPanel({ executions: [] });

    expect(byId('run-create-start')).toBeNull();
    expect(byId('run-no-create-permission')?.textContent).toContain('cannot create executions');
  });
});

describe('ScenarioRunPanel — countdown preference affordances (phase 96)', () => {
  /** The other-tab write, simulated: the value lands in localStorage and
   *  the native storage event carries it to the mounted consumers. */
  const otherTabWrites = async (value: string) => {
    await act(async () => {
      localStorage.setItem(COUNTDOWN_STORAGE_KEY, value);
      window.dispatchEvent(new StorageEvent('storage', { key: COUNTDOWN_STORAGE_KEY, newValue: value }));
    });
  };

  it('shows the settings gear beside Start when idle; the chip only when ≠ default', async () => {
    await renderPanel();

    expect(byId('run-start')).not.toBeNull();
    const gear = byId('countdown-settings-button');
    expect(gear.getAttribute('aria-label')).toBe('Countdown settings');
    // The gear sits inside the run-controls group, Start's row.
    expect(container!.querySelector('[data-testid="run-controls"]')!.contains(gear)).toBe(true);
    // Default value: no chip — a control that always shows says nothing.
    expect(byId('countdown-chip')).toBeNull();

    await otherTabWrites('5');
    expect(byId('countdown-chip')?.textContent).toBe('5s');
  });

  it('keeps the settings gear beside the countdown while it ticks', async () => {
    mutable.phase = 'deployed';
    await renderPanel();
    await click(byId('run-start'));

    expect(countdown()).not.toBeNull();
    expect(remainingText()).toBe('Load test starts in 10s');
    expect(byId('countdown-settings-button')).not.toBeNull();
  });

  it('rides the create surface too: gear + chip beside Create and start (empty state)', async () => {
    await renderPanel({ executions: [] });

    expect(byId('run-create-start')).not.toBeNull();
    expect(byId('countdown-settings-button')).not.toBeNull();
    expect(byId('countdown-chip')).toBeNull();

    await otherTabWrites('3');
    expect(byId('countdown-chip')?.textContent).toBe('3s');
  });
});

describe('ScenarioRunPanel — SLO defaults suggestion (phase 97)', () => {
  it('renders the burst suggestion with the mode’s rows when the stored set is empty', async () => {
    await renderPanel();

    const block = byId('run-slo-suggestion');
    expect(block).not.toBeNull();
    expect(block.textContent).toContain('No thresholds set');
    // The rows are the table’s burst contract, wire spelling.
    const rows = Array.from(block.querySelectorAll('[data-testid="run-slo-row"]')).map(r => r.textContent);
    expect(rows).toEqual(['error_rate < 0.01', 'http_p95_ms < 500']);
    expect(byId('run-slo-apply')).not.toBeNull();
  });

  it('derives the soak throughput floor from the entry’s stated rate', async () => {
    mutable.config = {
      name: 'checkout-load-2-load',
      project_id: 1,
      execution_id: 22,
      tests: [
        { name: 'other', scenario_id: 7, concurrency: 5, rampup: 30, engines: 1, duration: 300 },
        {
          name: 'from-baseline',
          scenario_id: 42,
          concurrency: 48,
          rampup: 60,
          engines: 3,
          duration: 3600,
          throughput: 200,
          mode: 'soak',
        },
      ],
    };
    await renderPanel();

    const rows = Array.from(byId('run-slo-suggestion').querySelectorAll('[data-testid="run-slo-row"]')).map(
      r => r.textContent
    );
    expect(rows).toEqual(['error_rate < 0.005', 'http_p95_ms < 600', 'throughput_qps > 180']);
  });

  it('apply PUTs the suggested rows through the thresholds route, then hides the suggestion', async () => {
    await renderPanel();

    await click(byId('run-slo-apply'));

    const put = calls.find(c => c.method === 'PUT' && c.url.endsWith('/api/scenarios/42/thresholds'));
    expect(put).toBeDefined();
    // The payload pin: exactly the burst table rows, the shape
    // saveThresholds sends — ids are the server's business, not ours.
    expect(JSON.parse(put!.body as string)).toEqual({
      thresholds: [
        { metric: 'error_rate', comparison: 'lt', value: 0.01 },
        { metric: 'http_p95_ms', comparison: 'lt', value: 500 },
      ],
    });
    // The stored answer (non-empty now) hides the suggestion — applying
    // twice is not offered; the Editor tab owns edits from here.
    expect(byId('run-slo-suggestion')).toBeNull();
    expect(calls.filter(c => c.method === 'PUT' && c.url.endsWith('/api/scenarios/42/thresholds'))).toHaveLength(1);
  });

  it('non-empty stored thresholds: no suggestion rendered', async () => {
    mutable.thresholds = [{ metric: 'http_p95_ms', comparison: 'lt', value: 300 }];
    await renderPanel();

    expect(byId('run-slo-suggestion')).toBeNull();
    expect(byId('run-slo-apply')).toBeNull();
  });

  it('advanced entry (no mode) or a failed thresholds read: no suggestion', async () => {
    // Advanced latest: a mode entry is not in hand.
    mutable.config = {
      name: 'checkout-load-2-load',
      project_id: 1,
      execution_id: 22,
      tests: [{ name: 'from-baseline', scenario_id: 42, concurrency: 10, rampup: 30, engines: 2, duration: 300 }],
    };
    await renderPanel();
    expect(byId('run-slo-suggestion')).toBeNull();
  });

  it('failed thresholds read: the set is unknown, so no suggestion', async () => {
    overrides.push((_method, url) => {
      if (url.endsWith('/api/scenarios/42/thresholds')) {
        return json({ message: 'boom' }, 500);
      }
      return undefined;
    });
    await renderPanel();

    expect(byId('run-slo-suggestion')).toBeNull();
  });

  it('without the scenario:update grant the affordance never mounts', async () => {
    overrides.push((_method, url) => {
      if (url.endsWith('/api/me')) {
        return json({
          subject: 'demo:erin',
          name: 'Erin',
          email: '',
          global_roles: [],
          tenants: {},
          permissions: { execution: ['update'], run: ['create'], scenario: ['read'] },
          demo: true,
        });
      }
      return undefined;
    });
    await renderPanel();

    // The statement and the edit form stay (execution:update held); only
    // the one-click threshold write is withheld — it costs
    // scenario:update.
    expect(byId('run-edit')).not.toBeNull();
    expect(byId('run-slo-suggestion')).toBeNull();
    expect(byId('run-slo-apply')).toBeNull();
  });
});
