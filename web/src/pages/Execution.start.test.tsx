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
import { COUNTDOWN_STORAGE_KEY } from '../lib/countdownPref';
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
  /** When true the deploy POST stays pending until the test calls releaseDeploy. */
  gateDeploy: boolean;
  releaseDeploy: (() => void) | null;
  deployCalls: number;
  triggerCalls: number;
} = {
  phase: 'idle',
  deployStatus: 200,
  deployBody: { message: 'engines deploying' },
  triggerStatus: 200,
  triggerBody: { message: 'run triggered' },
  hangDeploy: false,
  gateDeploy: false,
  releaseDeploy: null,
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
        if (mutable.gateDeploy) {
          // A test-gated deploy: pending until the test releases it, so the
          // mid-deploy state is observable (phase 96's 0-skip test).
          return new Promise<Response>(resolve => {
            mutable.releaseDeploy = () => {
              if (mutable.deployStatus === 200) {
                mutable.phase = 'deployed';
              }
              resolve(json(mutable.deployBody, mutable.deployStatus));
            };
          });
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
const stopBtn = () => container!.querySelector('[data-testid="lifecycle-stop"]') as HTMLButtonElement;
const lifecycle = (name: string) => container!.querySelector(`[data-testid="lifecycle-${name}"]`);
const groupEl = () => container!.querySelector('[role="group"][aria-label="Lifecycle controls"]') as HTMLElement;
const countdown = () => container!.querySelector('[data-testid="start-countdown"]');
const remainingText = () =>
  container!.querySelector('[data-testid="start-countdown-remaining"]')?.textContent ?? '';
const stepStatus = (step: string) => container!.querySelector(`[data-testid="start-flow-${step}"]`);

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
  // Phase 96: the countdown preference is localStorage-backed and jsdom's
  // storage persists across tests within a file — reset to absent so every
  // test starts from the 10s default unless it sets a value itself.
  localStorage.removeItem(COUNTDOWN_STORAGE_KEY);
  Object.assign(mutable, {
    phase: 'idle',
    deployStatus: 200,
    deployBody: { message: 'engines deploying' },
    triggerStatus: 200,
    triggerBody: { message: 'run triggered' },
    hangDeploy: false,
    gateDeploy: false,
    releaseDeploy: null,
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
  it('idle offers Start only — no deploy/trigger/purge/stop buttons in the DOM', async () => {
    await renderStartPage();
    expect(startBtn().textContent).toBe('Start');
    for (const gone of ['deploy', 'trigger', 'purge', 'stop']) {
      expect(lifecycle(gone)).toBeNull();
    }
    expect(groupEl().getAttribute('aria-busy')).toBe('false');
  });

  it('running offers Stop only — no Start/deploy/trigger/purge buttons', async () => {
    mutable.phase = 'running';
    await renderStartPage();
    expect(stopBtn().textContent).toBe('Stop');
    expect(stopBtn().disabled).toBe(false);
    for (const gone of ['start', 'deploy', 'trigger', 'purge']) {
      expect(lifecycle(gone)).toBeNull();
    }
  });

  it('deployed offers Start; clicking it skips the deploy POST and opens the countdown directly', async () => {
    mutable.phase = 'deployed';
    await renderStartPage();
    await advance(500);
    expect(startBtn().textContent).toBe('Start');
    await click(startBtn());

    // Straight to the countdown — no deploying step, no deploy call.
    expect(countdown()).not.toBeNull();
    expect(remainingText()).toBe('Load test starts in 10s');
    expect(container!.querySelector('[data-testid="start-flow-deploying"]')).toBeNull();
    expect(mutable.deployCalls).toBe(0);

    // The countdown runs out into the trigger; success lands on the
    // running row (Stop), Start gone.
    await advance(10_000);
    expect(mutable.triggerCalls).toBe(1);
    expect(countdown()).toBeNull();
    expect(startBtn()).toBeNull();
    expect(stopBtn().disabled).toBe(false);
  });

  it('chains deploy → visible countdown → trigger, then hands control back', async () => {
    await reachCountdown();

    // Mid-flow contract: the countdown reads as seconds (not a spinner),
    // the group is busy, and no lifecycle button is clickable (Start IS
    // the countdown now; the deployed row offers nothing else).
    expect(mutable.deployCalls).toBe(1);
    expect(mutable.triggerCalls).toBe(0);
    expect(startBtn()).toBeNull();
    expect(stopBtn()).toBeNull();
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

    // The execution IS deployed by now, so the honest control set is
    // Start again (straight countdown this time); nothing fired, and the
    // removed Trigger button stays removed.
    expect(countdown()).toBeNull();
    expect(mutable.triggerCalls).toBe(0);
    expect(startBtn().disabled).toBe(false);
    expect(lifecycle('trigger')).toBeNull();
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
    // Start is the flow status now — no lifecycle button while in flight.
    expect(startBtn()).toBeNull();
    expect(mutable.deployCalls).toBe(1);

    await click(status!.querySelector('[data-testid="start-flow-cancel"]')!);
    expect(container!.querySelector('[data-testid="start-flow-deploying"]')).toBeNull();
    // Phase never left idle (the deploy hung), so Start is back and enabled.
    expect(startBtn().disabled).toBe(false);
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
    // Still deployed: Start (straight countdown) is the operator's retry
    // path — the removed manual Trigger stays removed.
    expect(startBtn().disabled).toBe(false);
    expect(lifecycle('trigger')).toBeNull();
    expect(groupEl().getAttribute('aria-busy')).toBe('false');
  });
});

describe('configurable launch countdown (phase 96)', () => {
  it('begin() reads the preference at click time — a stored 3 counts down from 3', async () => {
    localStorage.setItem(COUNTDOWN_STORAGE_KEY, '3');
    await renderStartPage();
    await advance(500);
    await click(startBtn());
    await act(async () => {});

    expect(countdown()).not.toBeNull();
    expect(remainingText()).toBe('Load test starts in 3s');
    expect(mutable.deployCalls).toBe(1);

    // The scaled length is honest end to end: 3s → trigger, not 10s.
    await advance(2_000);
    expect(mutable.triggerCalls).toBe(0);
    await advance(1_000);
    expect(mutable.triggerCalls).toBe(1);
    expect(countdown()).toBeNull();
  });

  it('a stored 0 skips the counting step entirely from idle: deploy-wait → trigger', async () => {
    localStorage.setItem(COUNTDOWN_STORAGE_KEY, '0');
    mutable.gateDeploy = true;
    await renderStartPage();
    await advance(500);

    // A countdown mounting even for one frame would betray a zero-second
    // countdown UI. The check reads the mutation RECORDS (addedNodes), not
    // the live DOM — a countdown that mounts and unmounts within the same
    // act flush is already gone by the time the observer callback runs, but
    // its insertion is still in the records.
    let sawCountdown = false;
    const wasCountdownNode = (n: Node) =>
      n instanceof Element && (n.matches('[data-testid="start-countdown"]') || n.querySelector('[data-testid="start-countdown"]') !== null);
    const observer = new MutationObserver(records => {
      for (const r of records) {
        if (Array.from(r.addedNodes).some(wasCountdownNode)) {
          sawCountdown = true;
        }
      }
    });
    observer.observe(container!, { childList: true, subtree: true });

    await click(startBtn());
    // The deploy POST is out and gated: the deploying wait is up, and no
    // countdown renders alongside it.
    expect(mutable.deployCalls).toBe(1);
    expect(stepStatus('deploying')).not.toBeNull();
    expect(countdown()).toBeNull();

    // Release the deploy: the phase flip hands STRAIGHT to the trigger —
    // deploy-wait → triggering, no counting step between.
    await act(async () => {
      mutable.releaseDeploy!();
    });
    observer.disconnect();

    expect(sawCountdown).toBe(false);
    expect(countdown()).toBeNull();
    expect(mutable.triggerCalls).toBe(1);
    expect(stopBtn().disabled).toBe(false);
  });

  it('a stored 0 on an already-deployed execution triggers immediately — no deploy, no countdown', async () => {
    localStorage.setItem(COUNTDOWN_STORAGE_KEY, '0');
    mutable.phase = 'deployed';
    await renderStartPage();
    await advance(500);
    await click(startBtn());
    await act(async () => {});

    expect(mutable.deployCalls).toBe(0);
    expect(countdown()).toBeNull();
    expect(mutable.triggerCalls).toBe(1);
    expect(stopBtn().disabled).toBe(false);
  });

  it('a preference change mid-session applies to the NEXT begin, not the live countdown', async () => {
    // First launch at the default: the countdown opens at 10.
    await reachCountdown();
    expect(remainingText()).toBe('Load test starts in 10s');

    // The operator walks away (cancel), changes the preference, starts again.
    await click(container!.querySelector('[data-testid="start-countdown-cancel"]')!);
    localStorage.setItem(COUNTDOWN_STORAGE_KEY, '5');
    await click(startBtn());
    await act(async () => {});

    // Deployed by the first launch's deploy: the retry is a straight
    // countdown — at the NEW value, read at begin() time.
    expect(countdown()).not.toBeNull();
    expect(remainingText()).toBe('Load test starts in 5s');
    expect(mutable.deployCalls).toBe(1); // no second deploy
  });

  it('offers the countdown settings beside Start when idle, and during the countdown', async () => {
    await renderStartPage();
    await advance(500);

    // Idle: the gear sits in the lifecycle group next to Start; the chip
    // stays hidden while the preference equals the default.
    const gearBtn = () => container!.querySelector('[data-testid="countdown-settings-button"]')!;
    expect(gearBtn().getAttribute('aria-label')).toBe('Countdown settings');
    expect(groupEl().contains(gearBtn())).toBe(true);
    expect(container!.querySelector('[data-testid="countdown-chip"]')).toBeNull();

    // The popover opens right from the hub and closes on its toggle.
    await click(gearBtn());
    expect(container!.querySelector('[data-testid="countdown-settings-popover"]')).not.toBeNull();
    await click(gearBtn());
    expect(container!.querySelector('[data-testid="countdown-settings-popover"]')).toBeNull();

    // A non-default value (written by the popover elsewhere in this tab,
    // or another tab) echoes as the chip beside Start.
    await act(async () => {
      localStorage.setItem(COUNTDOWN_STORAGE_KEY, '5');
      window.dispatchEvent(new StorageEvent('storage', { key: COUNTDOWN_STORAGE_KEY, newValue: '5' }));
    });
    expect(container!.querySelector('[data-testid="countdown-chip"]')?.textContent).toBe('5s');

    // During the countdown the gear stays mounted next to it (a retune
    // applies to the next launch); the chip is Start's companion and
    // waits with it.
    await click(startBtn());
    await act(async () => {});
    expect(countdown()).not.toBeNull();
    expect(container!.querySelector('[data-testid="countdown-settings-button"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="countdown-chip"]')).toBeNull();
  });
});
