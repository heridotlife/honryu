// The scenario-threshold editor (phase 72), mounted with the house
// createRoot + act pattern and fetch stubbed per-URL: add/remove rows,
// client-side range validation, and the replace-all save that adopts the
// stored ids back.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ThresholdEditor from './ThresholdEditor';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

interface Call {
  method: string;
  url: string;
  body: string;
}

let container: HTMLDivElement | null = null;
let root: Root | null = null;
let calls: Call[] = [];
// What GET /api/scenarios/9/thresholds answers; reset per test (module
// state would otherwise leak the previous test's list into the next).
let storedFixture: unknown = [];

function stubFetch() {
  calls = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const method = (init?.method as string) ?? 'GET';
      const url = String(input);
      calls.push({ method, url, body: String(init?.body ?? '') });
      if (url.endsWith('/api/scenarios/9/thresholds') && method === 'GET') {
        return json(storedFixture);
      }
      if (url.endsWith('/api/scenarios/9/thresholds') && method === 'PUT') {
        const sent = JSON.parse(String(init?.body)) as {
          thresholds: { metric: string; comparison: string; value: number }[];
        };
        return json(
          sent.thresholds.map((t, i) => ({ id: 100 + i, scenario_id: 9, ...t, created_time: '2026-09-19T00:00:00Z' })),
        );
      }
      return json({ message: `no stub for ${url}` }, 500);
    }),
  );
}

async function renderEditor(): Promise<void> {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(<ThresholdEditor scenarioId={9} />);
  });
  await act(async () => {});
}

function click(testid: string): void {
  const el = container!.querySelector(`[data-testid="${testid}"]`) as HTMLButtonElement | null;
  if (el === null) {
    throw new Error(`no element ${testid}`);
  }
  act(() => {
    el.click();
  });
}

function setInput(testid: string, value: string): void {
  const el = container!.querySelector(`[data-testid="${testid}"]`) as HTMLInputElement | null;
  if (el === null) {
    throw new Error(`no element ${testid}`);
  }
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!;
  act(() => {
    setter.call(el, value);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
}

async function setSelect(testid: string, value: string): Promise<void> {
  const el = container!.querySelector(`[data-testid="${testid}"]`) as HTMLSelectElement | null;
  if (el === null) {
    throw new Error(`no element ${testid}`);
  }
  const setter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, 'value')!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event('change', { bubbles: true }));
  });
}

beforeEach(() => {
  storedFixture = [];
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
});

describe('ThresholdEditor', () => {
  it('shows the stored rows after the load resolves', async () => {
    stubFetch();
    storedFixture = [{ id: 1, scenario_id: 9, metric: 'http_p95_ms', comparison: 'lt', value: 250 }];
    await renderEditor();
    expect(container!.querySelector('[data-testid="threshold-row-0"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="threshold-row-0"]')).not.toBeNull();
    const metric = container!.querySelector('[data-testid="threshold-metric-0"]') as HTMLSelectElement;
    expect(metric.value).toBe('http_p95_ms');
    const value = container!.querySelector('[data-testid="threshold-value-0"]') as HTMLInputElement;
    expect(value.value).toBe('250');
  });

  it('shows the empty state (with the what-thresholds-are note) when none are defined', async () => {
    stubFetch();
    await renderEditor();
    await act(async () => {});
    expect(container!.querySelector('[data-testid="thresholds-empty"]')?.textContent).toContain('No thresholds defined');
    // And Save is disabled with nothing to save.
    const save = container!.querySelector('[data-testid="threshold-save"]') as HTMLButtonElement;
    expect(save.disabled).toBe(true);
  });

  it('add row starts a p95 ceiling with an empty value flagged invalid', async () => {
    stubFetch();
    await renderEditor();
    await act(async () => {});
    click('threshold-add');
    expect(container!.querySelector('[data-testid="threshold-row-0"]')).not.toBeNull();
    const err = container!.querySelector('[data-testid="threshold-error-0"]');
    expect(err?.textContent).toBe('Enter a value');
    const save = container!.querySelector('[data-testid="threshold-save"]') as HTMLButtonElement;
    expect(save.disabled).toBe(true);
  });

  it('flags an out-of-range value per metric and blocks save, then allows a legal one', async () => {
    stubFetch();
    await renderEditor();
    await act(async () => {});
    click('threshold-add');
    // error_rate selected, value 2: outside 0..1.
    await setSelect('threshold-metric-0', 'error_rate');
    setInput('threshold-value-0', '2');
    expect(container!.querySelector('[data-testid="threshold-error-0"]')?.textContent).toContain('Between 0 and 1');
    let save = container!.querySelector('[data-testid="threshold-save"]') as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    // Zero latency would be just as refused, with the positive hint.
    await setSelect('threshold-metric-0', 'throughput_qps');
    setInput('threshold-value-0', '0');
    expect(container!.querySelector('[data-testid="threshold-error-0"]')?.textContent).toContain('greater than 0');
    // A legal value clears the error and enables Save.
    setInput('threshold-value-0', '50');
    expect(container!.querySelector('[data-testid="threshold-error-0"]')).toBeNull();
    save = container!.querySelector('[data-testid="threshold-save"]') as HTMLButtonElement;
    expect(save.disabled).toBe(false);
  });

  it('saves the whole list replace-all and adopts the stored ids', async () => {
    stubFetch();
    storedFixture = [{ id: 1, scenario_id: 9, metric: 'http_p95_ms', comparison: 'lt', value: 250 }];
    await renderEditor();
    await act(async () => {});
    click('threshold-add');
    await setSelect('threshold-metric-1', 'throughput_qps');
    await setSelect('threshold-comparison-1', 'gt');
    setInput('threshold-value-1', '50');
    click('threshold-save');
    await act(async () => {});
    const put = calls.find((c) => c.method === 'PUT');
    expect(put).toBeDefined();
    expect(JSON.parse(put!.body)).toEqual({
      thresholds: [
        { metric: 'http_p95_ms', comparison: 'lt', value: 250 },
        { metric: 'throughput_qps', comparison: 'gt', value: 50 },
      ],
    });
    // The response's ids become the editor's state, and the saved note shows.
    expect(container!.querySelector('[data-testid="thresholds-saved"]')).not.toBeNull();
  });

  it('saving an emptied list clears (the replace-all contract)', async () => {
    stubFetch();
    storedFixture = [{ id: 1, scenario_id: 9, metric: 'error_rate', comparison: 'lt', value: 0.01 }];
    await renderEditor();
    await act(async () => {});
    click('threshold-remove-0');
    click('threshold-save');
    await act(async () => {});
    const put = calls.find((c) => c.method === 'PUT');
    expect(put).toBeDefined();
    expect(JSON.parse(put!.body)).toEqual({ thresholds: [] });
    expect(container!.querySelector('[data-testid="thresholds-empty"]')).not.toBeNull();
  });
});
