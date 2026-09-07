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
    expect(sent['multi-test'].tests[0].throughput).toBe(250);
  });

  it('read-only mode renders text, not inputs', async () => {
    await render({ executionId: 7, canUpdate: false });
    expect(container!.querySelector('[data-testid="config-throughput-0"]')).toBeNull();
    expect(container!.textContent).toContain('unlimited');
    expect(container!.textContent).toContain('Read-only');
  });
});
