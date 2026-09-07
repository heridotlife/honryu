import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import ExecutionConfigCard from './ExecutionConfigCard';

let container: HTMLDivElement | null = null;
let root: ReturnType<typeof createRoot> | null = null;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });

const cfg = {
  'multi-test': {
    name: 'p22-data-1',
    project_id: 1,
    execution_id: 7,
    tests: [
      { name: '', scenario_id: 5, concurrency: 2, rampup: 5, engines: 1, duration: 120, csv_split: false },
    ],
    csv_split: false,
  },
};

async function render(props: { executionId: number; canUpdate?: boolean }) {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter>
        <ExecutionConfigCard executionId={props.executionId} canUpdate={props.canUpdate ?? true} />
      </MemoryRouter>,
    );
  });
  await act(async () => {});
}

beforeEach(() => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/api/executions/7/config') && url.includes('api')) {
        return json(cfg);
      }
      return json({ message: `no stub for ${url}` }, 500);
    }),
  );
});
afterEach(() => {
  act(() => root?.unmount());
  container?.removeAttribute('data-testid');
  container?.remove();
  vi.unstubAllGlobals();
});

describe('ExecutionConfigCard', () => {

  it('shows the config with unlimited throughput when the key is omitted', async () => {
    await render({ executionId: 7 });
    const input = container!.querySelector('[data-testid="config-throughput-0"]') as HTMLInputElement;
    expect(input).not.toBeNull();
    expect(input.value).toBe('');
    expect(input.placeholder).toBe('unlimited');
    expect(container!.textContent).toContain('Target QPS');
  });

  it('save round-trips a set throughput as a number and omits it when cleared', async () => {
    const calls: Array<{ url: string; body: string }> = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (init?.method === 'PUT') {
          calls.push({ url, body: String(init.body) });
          return json({});
        }
        return json(cfg);
      }),
    );
    await render({ executionId: 7 });
    const input = container!.querySelector('[data-testid="config-throughput-0"]') as HTMLInputElement;
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
    await act(async () => {
      setter.call(input, '250');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
    const save = container!.querySelector('[data-testid="config-save"]') as HTMLButtonElement;
    await act(async () => {
      save.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await act(async () => {});
    expect(calls.length).toBe(1);
    const sent = JSON.parse(calls[0].body);
    // Bare profile on the wire (PUT contract), not the GET wrapper.
    expect(sent['multi-test']).toBeUndefined();
    expect(sent.tests[0].throughput).toBe(250);
    expect(sent.execution_id).toBe(7);
  });

  it('adds a criterion and the save round-trip carries it in the PUT body', async () => {
    const calls: Array<{ url: string; body: string }> = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (init?.method === 'PUT') {
          calls.push({ url, body: String(init.body) });
          return json({});
        }
        return json(cfg);
      }),
    );
    await render({ executionId: 7 });
    const input = container!.querySelector('[data-testid="criteria-input"]') as HTMLInputElement;
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
    await act(async () => {
      setter.call(input, 'p95>500ms');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
    const add = container!.querySelector('[data-testid="criteria-add"]') as HTMLButtonElement;
    await act(async () => {
      add.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    // The row appears immediately, from draft state.
    const row = container!.querySelector('[data-testid="criteria-row-0"]');
    expect(row?.textContent).toContain('p95>500ms');
    const save = container!.querySelector('[data-testid="config-save"]') as HTMLButtonElement;
    await act(async () => {
      save.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await act(async () => {});
    expect(calls.length).toBe(1);
    const sent = JSON.parse(calls[0].body);
    expect(sent.criteria).toEqual(['p95>500ms']);
  });

  it('removing the last criterion saves criteria: [] so the removal persists', async () => {
    const calls: Array<{ url: string; body: string }> = [];
    const withCriteria = {
      'multi-test': { ...cfg['multi-test'], criteria: ['failures>10%'] },
    };
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (init?.method === 'PUT') {
          calls.push({ url, body: String(init.body) });
          return json({});
        }
        return json(withCriteria);
      }),
    );
    await render({ executionId: 7 });
    expect(container!.querySelector('[data-testid="criteria-row-0"]')?.textContent).toContain('failures>10%');
    const remove = container!.querySelector('[data-testid="criteria-remove-0"]') as HTMLButtonElement;
    await act(async () => {
      remove.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(container!.querySelector('[data-testid="criteria-row-0"]')).toBeNull();
    const save = container!.querySelector('[data-testid="config-save"]') as HTMLButtonElement;
    await act(async () => {
      save.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await act(async () => {});
    expect(calls.length).toBe(1);
    const sent = JSON.parse(calls[0].body);
    // An empty array, not an omitted key: an omitted key would leave the
    // stored criteria untouched and silently undo the removal.
    expect(Array.isArray(sent.criteria)).toBe(true);
    expect(sent.criteria).toEqual([]);
  });

  it('read-only mode renders text, not inputs', async () => {
    await render({ executionId: 7, canUpdate: false });
    expect(container!.querySelector('[data-testid="config-throughput-0"]')).toBeNull();
    expect(container!.textContent).toContain('unlimited');
    expect(container!.textContent).toContain('Read-only');
  });
});
