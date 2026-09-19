// Phase 93: the one-click Start flow, mounted (createRoot + act, the
// page-test house style). Fake timers go live BEFORE mount so the page's
// 10s status poll and the countdown's 1s ticker share one clock; a 500ms
// offset after mount keeps the poll's deadline and the countdown's final
// tick half a second apart, which makes the mid-countdown-flip test
// deterministic (the poll observes the flip before the final tick can
// complete the countdown). The fetch stub reads a mutable object so tests
// can flip the execution's phase mid-flow exactly like the backend would.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import Execution from './Execution';
import { SessionProvider } from '../hooks/useSession';
import type { ExecutionStatus } from '../api/status';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

/** EventSource stand-in: the live stream constructs one on every mount. */
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  url: string;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  closed = false;
  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }
  close() {
    this.closed = true;
  }
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

/** Everything the stubbed endpoints answer with; tests mutate mid-flight. */
const mutable: {
  phase: ExecutionStatus['phase'];
  deployStatus: number;
  deployBody: Record<string, unknown>;
  triggerStatus: number;
  triggerBody: Record<string, unknown>;
  hangDeploy: boolean;
  deployCalls: number;
  triggerCalls: number;
} = {
  phase: 'idle',
  deployStatus: 200,
  deployBody: { message: 'engines deploying' },
  triggerStatus: 200,
  triggerBody: { message: 'run triggered' },
  hangDeploy: false,
  deployCalls: 0,
  triggerCalls: 0,
};

let container: HTMLDivElement | null = null;
let root: Root | null = null;

async function renderStartPage() {
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/api/me')) {
        return json({
          subject: 'demo:a',
          name: 'a',
          email: '',
          global_roles: [],
          tenants: {},
          permissions: { '*': ['*'] },
          demo: true,
        });
      }
      if (url.endsWith('/api/executions/5')) {
        return json({
          id: 5,
          name: 'demo',
          project_id: 1,
          csv_split: false,
          created_time: '2026-09-05T10:00:00Z',
          load_profile: [],
          data: [],
          engine: 'jmeter',
          kind: 'normal',
        });
      }
      if (url.endsWith('/api/executions/5/status')) {
        return json({ phase: mutable.phase, pool_size: 0, status: [] });
      }
      if (url.endsWith('/api/executions/5/deploy')) {
        mutable.deployCalls++;
        if (mutable.hangDeploy) {
          // A never-landing deploy: the deploying wait must be escapable.
          return new Promise<Response>(() => {});
        }
        if (mutable.deployStatus === 200) {
          // Realistic: a successful deploy flips the execution's phase.
          mutable.phase = 'deployed';
        }
        return json(mutable.deployBody, mutable.deployStatus);
      }
      if (url.endsWith('/api/executions/5/trigger')) {
        mutable.triggerCalls++;
        if (mutable.triggerStatus === 200) {
          mutable.phase = 'running';
        }
        return json(mutable.triggerBody, mutable.triggerStatus);
      }
      if (url.endsWith('/api/executions/5/reports')) {
        return json([]);
      }
      if (url.includes('/api/executions/5/trend')) {
        return json({ execution_id: 5, points: [] });
      }
      if (url.includes('/api/executions/5/error-signatures')) {
        return json({ execution_id: 5, grouped_by: 'label', groups: [] });
      }
      return json({ message: `no stub for ${url}` }, 500);
    })
  );
  vi.stubGlobal('EventSource', FakeEventSource);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter initialEntries={['/executions/5']}>
        <SessionProvider>
          <Routes>
            <Route path="/executions/:id" element={<Execution />} />
          </Routes>
        </SessionProvider>
      </MemoryRouter>
    );
  });
  await act(async () => {});
}

const startBtn = () => container!.querySelector('[data-testid="lifecycle-start"]') as HTMLButtonElement;
const deployBtn = () => container!.querySelector('[data-testid="lifecycle-deploy"]') as HTMLButtonElement;
const groupEl = () => container!.querySelector('[role="group"][aria-label="Lifecycle controls"]') as HTMLElement;
const countdown = () => container!.querySelector('[data-testid="start-countdown"]');
const remainingText = () =>
  container!.querySelector('[data-testid="start-countdown-remaining"]')?.textContent ?? '';

const click = async (el: Element) => {
  await act(async () => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
};

const advance = async (ms: number) => {
  await act(async () => {
    vi.advanceTimersByTime(ms);
  });
};

/** The common prefix: mount at clock 0, offset 500ms (poll vs final-tick
 *  separation), click Start, let the deploy POST and its immediate status
 *  refresh land → the countdown is up at remaining=10. */
async function reachCountdown() {
  await renderStartPage();
  await advance(500);
  await click(startBtn());
  await act(async () => {});
  expect(countdown()).not.toBeNull();
  expect(remainingText()).toBe('Load test starts in 10s');
}

beforeEach(() => {
  vi.useFakeTimers();
  Object.assign(mutable, {
    phase: 'idle',
    deployStatus: 200,
    deployBody: { message: 'engines deploying' },
    triggerStatus: 200,
    triggerBody: { message: 'run triggered' },
    hangDeploy: false,
    deployCalls: 0,
    triggerCalls: 0,
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
  FakeEventSource.instances = [];
  container?.remove();
  container = null;
  root = null;
});

describe('Execution one-click Start (mounted)', () => {
  it('idle offers the Start composite beside the plain deploy path', async () => {
    await renderStartPage();
    expect(startBtn().textContent).toBe('Start');
    expect(deployBtn().textContent).toBe('Deploy');
    expect(groupEl().getAttribute('aria-busy')).toBe('false');
  });

  it('chains deploy → visible countdown → trigger, then hands control back', async () => {
    await reachCountdown();

    // Mid-flow contract: the countdown reads as seconds (not a spinner),
    // the group is busy, and every other lifecycle control is locked.
    expect(mutable.deployCalls).toBe(1);
    expect(mutable.triggerCalls).toBe(0);
    expect(deployBtn().disabled).toBe(true);
    expect(groupEl().getAttribute('aria-busy')).toBe('true');

    // 9.5s: one second left, the 10s poll has come and gone harmlessly.
    await advance(9_500);
    expect(remainingText()).toBe('Load test starts in 1s');
    expect(mutable.triggerCalls).toBe(0);

    // The final tick fires the trigger; success resets to the running row.
    await advance(1_000);
    expect(mutable.triggerCalls).toBe(1);
    expect(countdown()).toBeNull();
    expect(startBtn()).toBeNull();
    expect(groupEl().getAttribute('aria-busy')).toBe('false');
    const stop = container!.querySelector('[data-testid="lifecycle-stop"]') as HTMLButtonElement;
    expect(stop.disabled).toBe(false);
  });

  it('cancel during the countdown returns control without triggering', async () => {
    await reachCountdown();

    const cancel = container!.querySelector('[data-testid="start-countdown-cancel"]') as HTMLButtonElement;
    await click(cancel);

    // The execution IS deployed by now, so the honest control set is the
    // deployed row: Trigger available, Start gone, nothing fired.
    expect(countdown()).toBeNull();
    expect(mutable.triggerCalls).toBe(0);
    const trigger = container!.querySelector('[data-testid="lifecycle-trigger"]') as HTMLButtonElement;
    expect(trigger.disabled).toBe(false);
    expect(groupEl().getAttribute('aria-busy')).toBe('false');

    // The countdown's timer is dead: running out the original 10s (and
    // then some) still fires nothing.
    await advance(30_000);
    expect(mutable.triggerCalls).toBe(0);
    expect(countdown()).toBeNull();
  });

  it('cancel during the deploying wait returns the idle controls (deploy already sent)', async () => {
    mutable.hangDeploy = true;
    await renderStartPage();
    await advance(500);
    await click(startBtn());

    const status = container!.querySelector('[data-testid="start-flow-deploying"]');
    expect(status?.textContent).toContain('Deploying engines…');
    expect(status?.querySelector('[data-testid="start-flow-cancel"]')).not.toBeNull();
    expect(deployBtn().disabled).toBe(true);
    expect(mutable.deployCalls).toBe(1);

    await click(status!.querySelector('[data-testid="start-flow-cancel"]')!);
    expect(container!.querySelector('[data-testid="start-flow-deploying"]')).toBeNull();
    expect(startBtn()).not.toBeNull();
    expect(deployBtn().disabled).toBe(false);
    expect(groupEl().getAttribute('aria-busy')).toBe('false');

    // The hung deploy response can never land a late error: the flow was
    // abandoned, so it may not still speak. (The config card's own load
    // error is unrelated and may legitimately be present.)
    await advance(30_000);
    const alerts = Array.from(container!.querySelectorAll('[role="alert"]')).map(a => a.textContent ?? '');
    expect(alerts.some(t => t.includes('start failed'))).toBe(false);
  });

  it('a failed deploy surfaces the server message and unlocks the controls', async () => {
    mutable.deployStatus = 500;
    mutable.deployBody = { message: 'engine quota exceeded' };
    await renderStartPage();
    await advance(500);
    await click(startBtn());
    await act(async () => {});

    expect(container!.querySelector('[role="alert"]')?.textContent).toContain('engine quota exceeded');
    expect(container!.querySelector('[data-testid="action-error-details"]')).toBeNull();
    expect(startBtn().disabled).toBe(false);
    expect(deployBtn().disabled).toBe(false);
    expect(groupEl().getAttribute('aria-busy')).toBe('false');
  });

  it('a mid-countdown phase flip aborts the countdown without triggering', async () => {
    await reachCountdown();
    await advance(4_500);
    expect(remainingText()).toBe('Load test starts in 6s');

    // The idle reaper (or a colleague's purge) tears the engines down
    // mid-countdown; the page's 10s poll is what observes it.
    mutable.phase = 'idle';
    await advance(5_000); // clock 10s: the poll reads idle → abort

    expect(container!.querySelector('[role="alert"]')?.textContent).toContain(
      'start aborted: the execution is no longer deployed'
    );
    expect(countdown()).toBeNull();
    expect(mutable.triggerCalls).toBe(0);
    // Idle again: the Start composite is back and enabled.
    expect(startBtn().disabled).toBe(false);

    // The countdown died before its final tick: nothing fires after.
    await advance(10_000);
    expect(mutable.triggerCalls).toBe(0);
  });

  it('a failed trigger surfaces message plus details (429 quota refusal)', async () => {
    mutable.triggerStatus = 429;
    mutable.triggerBody = {
      message: 'reservation would exceed tenant quota',
      details: {
        tenant_id: 1,
        cluster: '',
        requested: 2,
        used: 0,
        ceiling: 1,
        hint: 'PUT /api/tenants/{tenant_id}/quota ceiling=1',
      },
    };
    await reachCountdown();
    await advance(10_000); // countdown runs out → trigger POST → 429
    await act(async () => {});

    expect(container!.querySelector('[role="alert"]')?.textContent).toContain(
      'reservation would exceed tenant quota'
    );
    const details = container!.querySelector('[data-testid="action-error-details"]');
    expect(details?.querySelector('code')?.textContent).toBe('PUT /api/tenants/{tenant_id}/quota ceiling=1');
    expect(details?.textContent).toContain('used 0 / ceiling 1 — requested 2');
    expect(mutable.triggerCalls).toBe(1);
    // Still deployed: manual Trigger is the operator's retry path.
    const trigger = container!.querySelector('[data-testid="lifecycle-trigger"]') as HTMLButtonElement;
    expect(trigger.disabled).toBe(false);
    expect(groupEl().getAttribute('aria-busy')).toBe('false');
  });
});
