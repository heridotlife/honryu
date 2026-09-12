import { act } from 'react';
import { createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import LiveStatus, { engineShortfall, summarize } from './LiveStatus';
import type { ReceivedMetric } from './LiveStatus';
import type { EngineMetric } from '../api/status';

function makeMetric(overrides: Partial<EngineMetric> = {}): EngineMetric {
  return {
    threads: 10,
    latency: 0.25,
    label: 'GET /',
    status: '200',
    raw: '',
    execution_id: '1',
    scenario_id: '1',
    engine_id: '0',
    run_id: '1',
    ...overrides,
  };
}

describe('summarize', () => {
  it('reports zeroed stats and null latency for an empty window', () => {
    expect(summarize([])).toEqual({ throughput: 0, errorRate: 0, latencySeconds: null });
  });

  it('derives throughput from event count over the 10s window and uses the latest latency', () => {
    const events: ReceivedMetric[] = [
      { receivedAt: 1000, metric: makeMetric({ latency: 0.1 }) },
      { receivedAt: 2000, metric: makeMetric({ latency: 0.2 }) },
      { receivedAt: 3000, metric: makeMetric({ latency: 0.3 }) },
    ];
    const stats = summarize(events);
    expect(stats.throughput).toBeCloseTo(0.3, 5);
    expect(stats.latencySeconds).toBe(0.3);
    expect(stats.errorRate).toBe(0);
  });

  it('computes error rate as the fraction of non-200 events', () => {
    const events: ReceivedMetric[] = [
      { receivedAt: 1000, metric: makeMetric({ status: '200' }) },
      { receivedAt: 2000, metric: makeMetric({ status: '500' }) },
      { receivedAt: 3000, metric: makeMetric({ status: '500' }) },
      { receivedAt: 4000, metric: makeMetric({ status: '200' }) },
    ];
    expect(summarize(events).errorRate).toBeCloseTo(0.5, 5);
  });
});

describe('engineShortfall', () => {
  it('is zero when every wanted engine is deployed', () => {
    expect(engineShortfall({ engines: 4, engines_deployed: 4 })).toBe(0);
  });

  it('counts engines still missing while a scenario scales up', () => {
    expect(engineShortfall({ engines: 4, engines_deployed: 1 })).toBe(3);
  });

  it('never goes negative when a terminating engine briefly over-reports', () => {
    expect(engineShortfall({ engines: 4, engines_deployed: 5 })).toBe(0);
  });
});

// Phase 51 landmark gate (mounted, Executions.test.tsx's aria-pressed style;
// createElement instead of JSX so the pure suite's .ts file keeps its name):
// the page's outermost element is a labelled region, and the stats grid the
// stream re-renders every second announces itself with aria-live="polite".
(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

// streamExecutionMetrics constructs an EventSource on watch; jsdom has none,
// so a inert double stands in (the test drives no real events through it).
class FakeEventSource {
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((event: MessageEvent<string>) => void) | null = null;
  close(): void {}
}

let container: HTMLDivElement | null = null;
let root: Root | null = null;

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
});

describe('LiveStatus landmarks (phase 51)', () => {
  it('renders a labelled region whose stats grid is a polite live area', async () => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.stubGlobal('EventSource', FakeEventSource);
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.endsWith('/api/executions/42/info')) {
          return json({ id: 42, name: 'alpha-exec', project_id: 1, engine: 'k6', cluster: 'honryu' });
        }
        if (url.endsWith('/api/executions/42/status')) {
          return json({
            phase: 'running',
            pool_size: 2,
            status: [{ scenario_id: 1, engines: 2, engines_deployed: 2, engines_reachable: true, in_progress: false }],
          });
        }
        return json({ message: `no stub for ${url}` }, 500);
      })
    );
    root = createRoot(container);
    await act(async () => {
      root!.render(createElement(LiveStatus));
    });

    // Drive the watch form so the status snapshot (and with it the stats
    // grid) loads.
    const input = container.querySelector('input[type="number"]') as HTMLInputElement;
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
    await act(async () => {
      setter.call(input, '42');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await act(async () => {
      container!.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    });
    await act(async () => {});

    const region = container!.querySelector('[role="region"]');
    expect(region).not.toBeNull();
    expect(region!.getAttribute('aria-label')).toBe('Live status panel');

    // The Throughput/Error rate/Latency grid re-renders on every stream
    // tick; the polite-live attribute is what keeps screen readers sane.
    const live = container!.querySelector('[aria-live="polite"]');
    expect(live).not.toBeNull();
    expect(live!.textContent).toContain('Throughput');
    expect(live!.textContent).toContain('Error rate');
    expect(live!.textContent).toContain('Latency');
  });
});
