// The mounted half of CapacityPanel (phase 44): the editable Target QPS
// input that replaced the hardcoded 100. Execution.live.test.tsx's
// createRoot + act style; fetch is stubbed per-URL with a fan-out whose
// engine count derives from the request's target_qps (ceil(target/50)), so
// a re-query is observable both in the fetched URL and in the rendered
// "N engines for X qps" line. Persistence is asserted against the real
// jsdom localStorage, keyed per scenario.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import CapacityPanel from './CapacityPanel';
import { formatDay } from '../lib/executionRow';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

/** Every fan-out URL the stub saw, in order. */
let fanOuts: string[] = [];
/** Every URL the planner's stub saw, in order (all endpoints). */
let calls: string[] = [];
let container: HTMLDivElement | null = null;
let root: Root | null = null;

/** Stubs GET /api/scenarios/7/capacity-profile/fanout: ok with
 * ceil(target_qps/50) engines, recording the URL. */
async function renderPanel(): Promise<void> {
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/api/scenarios/7/capacity-profile/fanout')) {
        fanOuts.push(url);
        // The URL is relative (/api/...); a base makes it parseable.
        const target = Number(new URL(url, 'http://localhost').searchParams.get('target_qps'));
        return json({ status: 'ok', engines: Math.ceil(target / 50) });
      }
      return json({ message: `no stub for ${url}` }, 500);
    }),
  );
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <CapacityPanel
        scenarioId={7}
        executionId={5}
        keyInfo={{ engine: 'jmeter', cpu: '500m', memory: '512Mi' }}
      />,
    );
  });
  // Flush the initial fetches.
  await act(async () => {});
}

const targetInput = (): HTMLInputElement =>
  container!.querySelector('[data-testid="capacity-target-qps"]') as HTMLInputElement;

const enginesLine = (): string | null =>
  container!.querySelector('[data-testid="capacity-engines"]')?.textContent ?? null;

const setTarget = async (value: string): Promise<void> => {
  const el = targetInput();
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
};

beforeEach(() => {
  localStorage.clear();
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
  fanOuts = [];
  calls = [];
});

describe('CapacityPanel target QPS (mounted)', () => {
  it('defaults to 100 and renders the engines line with its target', async () => {
    await renderPanel();

    expect(fanOuts).toHaveLength(1);
    expect(fanOuts[0]).toContain('target_qps=100');
    expect(targetInput().value).toBe('100');
    expect(enginesLine()).toBe('2 engines for 100 qps');
  });

  it('changing the target re-queries fan-out, updates the line, and persists', async () => {
    await renderPanel();
    await setTarget('250');

    expect(fanOuts).toHaveLength(2);
    expect(fanOuts[1]).toContain('target_qps=250');
    expect(enginesLine()).toBe('5 engines for 250 qps');
    expect(localStorage.getItem('honryu.capacity-target-qps.7')).toBe('250');
  });

  it('a stored target for the scenario is the initial query', async () => {
    localStorage.setItem('honryu.capacity-target-qps.7', '80');
    await renderPanel();

    expect(fanOuts[0]).toContain('target_qps=80');
    expect(enginesLine()).toBe('2 engines for 80 qps');
  });

  it('a value below the minimum never re-queries', async () => {
    await renderPanel();
    await setTarget('0');
    await setTarget('');

    expect(fanOuts).toHaveLength(1);
    expect(enginesLine()).toBe('2 engines for 100 qps');
  });

  it('renders one engine with no plural s', async () => {
    await renderPanel();
    await setTarget('50');

    expect(enginesLine()).toBe('1 engine for 50 qps');
  });
});

// ---- Phase 54: the planner (normal executions ask the same fan-out
// question, with pod size/engine selectable and an explicit Compute). ----

/** The stored profile the planner's best-effort lookup sees: the calibrated
 * scenario-6 shape from the phase context (jmeter, 500m/512Mi, 608.5 qps/pod). */
const plannerProfile = (calibratedAt = new Date().toISOString()): Record<string, unknown> => ({
  scenario_id: 7,
  engine: 'jmeter',
  cpu: '500m',
  memory: '512Mi',
  per_pod_qps: 608.5,
  saturated_by: 'engine',
  scenario_fingerprint: 'fp1',
  calibrated_at: calibratedAt,
  job_id: 3,
});

/** Renders the planner variant with every fetch routed through `respond`. */
async function renderPlanner(respond: (url: string) => Promise<Response>): Promise<void> {
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      calls.push(url);
      return respond(url);
    }),
  );
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <CapacityPanel
        planner
        scenarioId={7}
        executionId={5}
        keyInfo={{ engine: 'jmeter', cpu: '500m', memory: '512Mi' }}
      />,
    );
  });
  // Flush any mount-time fetches (the planner must make none).
  await act(async () => {});
}

/** Standard stub pair: a fan-out body plus an optional profile (absent → 404). */
function plannerStubs(
  fanOut: Record<string, unknown>,
  profile: Record<string, unknown> | null = plannerProfile(),
) {
  return async (url: string): Promise<Response> => {
    if (url.includes('/api/scenarios/7/capacity-profile/fanout')) {
      fanOuts.push(url);
      return json(fanOut);
    }
    if (url.includes('/api/scenarios/7/capacity-profile?')) {
      return profile === null ? json({ message: 'no profile' }, 404) : json(profile);
    }
    return json({ message: `no stub for ${url}` }, 500);
  };
}

const computeButton = (): HTMLButtonElement =>
  container!.querySelector('[data-testid="planner-compute"]') as HTMLButtonElement;

const plannerPods = (): string | null =>
  container!.querySelector('[data-testid="capacity-planner-pods"]')?.textContent ?? null;

const plannerBasis = (): string | null =>
  container!.querySelector('[data-testid="planner-basis"]')?.textContent ?? null;

const plannerResult = (): Element | null => container!.querySelector('[data-testid="planner-result"]');

const click = async (el: HTMLElement): Promise<void> => {
  await act(async () => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
};

const setSelect = async (testId: string, value: string): Promise<void> => {
  const el = container!.querySelector(`[data-testid="${testId}"]`) as HTMLSelectElement;
  const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event('change', { bubbles: true }));
  });
};

describe('CapacityPanel planner (mounted)', () => {
  it('fetches nothing on mount; Compute queries fan-out with the execution key and renders pods plus the basis note', async () => {
    await renderPlanner(plannerStubs({ status: 'ok', engines: 7 }));

    // Collapsed by default and silent: a card the operator never opens
    // must never hit the endpoint.
    expect(calls).toHaveLength(0);
    expect((container!.querySelector('[data-testid="planner-details"]') as HTMLDetailsElement).open).toBe(false);

    await click(computeButton());

    expect(fanOuts).toHaveLength(1);
    expect(fanOuts[0]).toContain('engine=jmeter&cpu=500m&memory=512Mi&target_qps=100');
    expect(plannerPods()).toBe('7 pods for 100 qps');
    expect(plannerBasis()).toBe('based on calibrated 608.5 qps/pod for this scenario');
  });

  it('a no_profile verdict renders the human explanation instead of a pod count', async () => {
    await renderPlanner(plannerStubs({ status: 'no_profile' }));
    await click(computeButton());

    expect(plannerPods()).toBeNull();
    expect(plannerResult()?.textContent).toContain('No capacity profile');
    expect(plannerResult()?.textContent).toContain('never been calibrated');
  });

  it('compute is disabled while the query is in flight', async () => {
    let release!: (response: Response) => void;
    const gate = new Promise<Response>((resolve) => {
      release = resolve;
    });
    await renderPlanner(async (url) => {
      if (url.includes('/api/scenarios/7/capacity-profile/fanout')) {
        fanOuts.push(url);
        return gate;
      }
      return json({ message: 'no profile' }, 404);
    });

    const btn = computeButton();
    expect(btn.disabled).toBe(false);
    await click(btn);
    expect(btn.disabled).toBe(true);
    expect(btn.textContent).toBe('Computing…');

    await act(async () => {
      release(json({ status: 'ok', engines: 2 }));
    });
    expect(btn.disabled).toBe(false);
    expect(plannerPods()).toBe('2 pods for 100 qps');
  });

  it('the engine/cpu/memory selects feed the queried key', async () => {
    await renderPlanner(plannerStubs({ status: 'ok', engines: 12 }));
    await setSelect('planner-engine', 'gatling');
    await setSelect('planner-cpu', '1');
    await setSelect('planner-memory', '1Gi');
    await click(computeButton());

    expect(fanOuts[0]).toContain('engine=gatling&cpu=1&memory=1Gi&target_qps=100');
  });

  it('a missing profile (404) still renders the pod count, without a basis note', async () => {
    await renderPlanner(plannerStubs({ status: 'ok', engines: 3 }, null));
    await click(computeButton());

    expect(plannerPods()).toBe('3 pods for 100 qps');
    expect(plannerBasis()).toBeNull();
  });

  it('a fresh calibration shows the short-form date and no stale hint', async () => {
    const day = new Date(Date.now() - 24 * 60 * 60 * 1000);
    await renderPlanner(plannerStubs({ status: 'ok', engines: 7 }, plannerProfile(day.toISOString())));
    await click(computeButton());

    const line = container!.querySelector('[data-testid="planner-calibrated"]')?.textContent ?? '';
    expect(line).toContain(`calibrated ${formatDay(day.toISOString())}`);
    expect(line).not.toContain('stale');
    expect(container!.querySelector('[data-testid="planner-stale"]')).toBeNull();
  });

  it('a calibration older than 7 days adds the muted stale hint', async () => {
    const old = new Date(Date.now() - 30 * 24 * 60 * 60 * 1000);
    await renderPlanner(plannerStubs({ status: 'ok', engines: 7 }, plannerProfile(old.toISOString())));
    await click(computeButton());

    expect(container!.querySelector('[data-testid="planner-calibrated"]')?.textContent).toContain(
      `calibrated ${formatDay(old.toISOString())}`,
    );
    expect(container!.querySelector('[data-testid="planner-stale"]')?.textContent).toContain(
      'stale calibration — re-run recommended',
    );
  });

  it('an unparseable calibrated_at renders no freshness line at all', async () => {
    await renderPlanner(plannerStubs({ status: 'ok', engines: 7 }, plannerProfile('not-a-date')));
    await click(computeButton());

    expect(container!.querySelector('[data-testid="planner-calibrated"]')).toBeNull();
    expect(plannerPods()).toBe('7 pods for 100 qps');
  });
});
