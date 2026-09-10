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

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

/** Every fan-out URL the stub saw, in order. */
let fanOuts: string[] = [];
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
