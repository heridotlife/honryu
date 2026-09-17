// The SLO panel's mounted interactions (phase 68), WebhooksCard.test.tsx's
// createRoot + act style with fetch stubbed per-URL: the list renders
// compliance badges (icon + text, never colour alone), the empty state
// shows before any SLO exists, the create form's at-least-one-target rule
// fails at the field without a request, delete takes a second confirming
// click, and the window selector refetches the budgets.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import SloPanel from './SloPanel';
import type { Slo, SloBudget } from '../api/generated';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const checkoutSLO: Slo = { id: 3, project_id: 1, name: 'checkout p95', target_p95_ms: 250, target_error_rate: null, target_success_ratio: 0.99, created_time: '2026-09-01T00:00:00Z' };
const signupSLO: Slo = { id: 5, project_id: 1, name: 'signup errors', target_p95_ms: null, target_error_rate: 0.01, target_success_ratio: null, created_time: '2026-09-02T00:00:00Z' };

const budgetFor = (slo: Slo, overrides: Partial<SloBudget> = {}): SloBudget => ({
  slo_id: slo.id,
  name: slo.name,
  window: '7d',
  window_start: '2026-09-08T00:00:00Z',
  window_end: '2026-09-15T00:00:00Z',
  run_count: 4,
  compliant: true,
  metrics: [
    { metric: 'p95_ms', target: 250, actual: 210, compliant: true, budget_remaining_pct: 16 },
    { metric: 'success_ratio', target: 0.99, actual: 0.995, compliant: true, budget_remaining_pct: 0.5 },
  ],
  ...overrides,
});

interface Calls {
  urls: string[];
  methods: string[];
  bodies: string[];
}

let container: HTMLDivElement | null = null;
let root: Root | null = null;
let calls: Calls = { urls: [], methods: [], bodies: [] };
// Per-test fixtures, read by the fetch stub. Tests set these BEFORE
// renderPanel so the mount-time fetches serve them.
let listSLOs: Slo[] = [checkoutSLO, signupSLO];
let budgetOverrides: Record<number, Partial<SloBudget>> = {};
let listStatus = 200;

async function renderPanel(projectId = 1): Promise<void> {
  container = document.createElement('div');
  document.body.appendChild(container);
  calls = { urls: [], methods: [], bodies: [] };
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.urls.push(url);
      calls.methods.push((init?.method as string) ?? 'GET');
      calls.bodies.push(String(init?.body ?? ''));
      if (url === '/api/projects/1/slos' && (init?.method as string) === 'POST') {
        return json({ id: 9, project_id: 1, name: 'new slo', target_p95_ms: 100, target_error_rate: null, target_success_ratio: null, created_time: '2026-09-03T00:00:00Z' }, 201);
      }
      if (url === '/api/projects/1/slos') {
        return json(listSLOs, listStatus);
      }
      if (url === '/api/projects/1/slos/3') {
        return new Response(null, { status: 204 });
      }
      const match = /\/api\/projects\/1\/slos\/(\d+)\/budget/.exec(url);
      if (match) {
        const slo = listSLOs.find(s => s.id === Number(match[1]));
        if (!slo) return json({ message: 'not found' }, 404);
        return json(budgetFor(slo, budgetOverrides[slo.id] ?? {}));
      }
      return json({ message: 'unexpected ' + url }, 404);
    })
  );
  await act(async () => {
    root = createRoot(container as HTMLDivElement);
    root?.render(<SloPanel projectId={projectId} />);
  });
  // Let the mount-time list + budget fetches land.
  await act(async () => {});
}

const tid = (id: string): HTMLElement => {
  const el = container!.querySelector(`[data-testid="${id}"]`);
  if (!el) {
    throw new Error(`missing ${id}`);
  }
  return el as HTMLElement;
};

/** Native value setter + input event (React's tracker ignores plain writes). */
async function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
}

async function click(el: Element) {
  await act(async () => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  root?.unmount();
  container?.remove();
  container = null;
  root = null;
  listSLOs = [checkoutSLO, signupSLO];
  budgetOverrides = {};
  listStatus = 200;
});

const text = (): string => document.body.textContent ?? '';

describe('SloPanel', () => {
  it('renders loading first, then compliance badges with icon and text', async () => {
    let resolveList: ((r: Response) => void) | undefined;
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.stubGlobal(
      'fetch',
      vi.fn(
        (input: RequestInfo | URL) =>
          new Promise<Response>(resolve => {
            if (String(input) === '/api/projects/1/slos') resolveList = resolve;
            else resolve(json(budgetFor(checkoutSLO)));
          })
      )
    );
    await act(async () => {
      root = createRoot(container as HTMLDivElement);
      root?.render(<SloPanel projectId={1} />);
    });
    expect(tid('slo-loading')).toBeTruthy();
    await act(async () => {
      resolveList?.(json([checkoutSLO]));
    });
    expect(tid('slo-row-3')).toBeTruthy();
    // The badge carries its text beside the icon -- colour is never the
    // only signal.
    expect(tid('slo-metrics-3').textContent).toContain('within target');
    expect(tid('slo-metrics-3').textContent).toContain('p95 latency');
  });

  it('renders violated budgets with the over-target badge and burned bar', async () => {
    listSLOs = [checkoutSLO];
    budgetOverrides = {
      3: {
        compliant: false,
        metrics: [
          { metric: 'p95_ms', target: 250, actual: 400, compliant: false, budget_remaining_pct: -60 },
          { metric: 'success_ratio', target: 0.99, actual: 0.995, compliant: true, budget_remaining_pct: 0.5 },
        ],
      },
    };
    await renderPanel();
    expect(tid('slo-badge-violated')).toBeTruthy();
    expect(text()).toContain('over target');
    expect(text()).toContain('-60% left');
    expect(text()).toContain('4 runs in the last 7d');
  });

  it('shows the empty state when the project has no SLOs', async () => {
    listSLOs = [];
    await renderPanel();
    expect(tid('slo-empty')).toBeTruthy();
    expect(text()).toContain('No SLOs defined');
  });

  it('shows the no-runs wording for a vacuous budget (run_count 0)', async () => {
    listSLOs = [checkoutSLO];
    budgetOverrides = { 3: { run_count: 0, metrics: [{ metric: 'p95_ms', target: 250, actual: null, compliant: true, budget_remaining_pct: null }] } };
    await renderPanel();
    expect(text()).toContain('no eligible runs in the last 7d');
    expect(tid('slo-badge-compliant')).toBeTruthy();
  });

  it('rejects a create with no targets at the field, without a request', async () => {
    listSLOs = [];
    await renderPanel();
    await type(tid('slo-name-input') as HTMLInputElement, 'no targets');
    await click(tid('slo-add-btn'));
    expect(tid('slo-form-error').textContent).toContain('at least one target');
    // Only the list fetch happened; no POST left the browser.
    expect(calls.methods).toEqual(['GET']);
  });

  it('creates an SLO with targets, refreshing the list', async () => {
    listSLOs = [];
    await renderPanel();
    await type(tid('slo-name-input') as HTMLInputElement, 'new slo');
    await type(tid('slo-p95-input') as HTMLInputElement, '100');
    await click(tid('slo-add-btn'));
    expect(calls.methods).toContain('POST');
    const postIdx = calls.methods.indexOf('POST');
    expect(calls.bodies[postIdx]).toContain('name=new+slo');
    expect(calls.bodies[postIdx]).toContain('target_p95_ms=100');
    expect(tid('slo-row-9')).toBeTruthy();
  });

  it('deletes only on the second, confirming click, naming the SLO', async () => {
    listSLOs = [checkoutSLO];
    await renderPanel();
    await click(tid('slo-delete-3'));
    // First click arms only: the confirm names WHAT is being deleted (the
    // blast radius), no DELETE sent.
    expect(tid('slo-delete-3').textContent).toContain('Delete “checkout p95”?');
    expect(calls.methods).not.toContain('DELETE');
    await click(tid('slo-delete-3'));
    expect(calls.methods).toContain('DELETE');
    expect(calls.urls.some(u => u === '/api/projects/1/slos/3')).toBe(true);
    expect(container!.querySelector('[data-testid="slo-row-3"]')).toBeNull();
  });

  // Phase 77: the armed confirm is dismissible without a second click --
  // Escape or a press outside the armed row disarms it, no DELETE leaves.
  // Phase 77: targets validate on blur -- earlier feedback than the
  // submit guard, which stays as the backstop. The summary carries the
  // old slo-form-error testid, now focusable and field-linked.
  it('shows a target error on blur, before any submit', async () => {
    listSLOs = [];
    await renderPanel();
    await type(tid('slo-name-input') as HTMLInputElement, 'blur check');
    await type(tid('slo-p95-input') as HTMLInputElement, '0');
    await act(async () => {
      tid('slo-p95-input').dispatchEvent(new FocusEvent('focusout', { bubbles: true }));
    });
    // Inline, at the field, before any submit: no summary yet.
    expect(container!.querySelector('[data-testid="slo-form-error"]')).toBeNull();
    expect(container!.textContent).toContain('p95 target must be a number above 0');
    expect(calls.methods).toEqual(['GET']);
  });

  it('on a failed submit the summary takes focus and its links focus their field', async () => {
    listSLOs = [];
    await renderPanel();
    await type(tid('slo-name-input') as HTMLInputElement, 'summary check');
    await type(tid('slo-p95-input') as HTMLInputElement, '0');
    await click(tid('slo-add-btn'));
    const summary = tid('slo-form-error');
    expect(summary.getAttribute('role')).toBe('alert');
    expect(document.activeElement).toBe(summary);
    expect(summary.textContent).toContain('p95 target must be a number above 0');
    expect(calls.methods).toEqual(['GET']);
    // The entry links to its field: activating it focuses the p95 input.
    const link = summary.querySelector('a');
    expect(link).not.toBeNull();
    await act(async () => {
      link!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    });
    expect(document.activeElement).toBe(tid('slo-p95-input'));
  });

  it('disarms the armed delete confirm on Escape and on a press outside the row', async () => {
    listSLOs = [checkoutSLO];
    await renderPanel();
    await click(tid('slo-delete-3'));
    expect(tid('slo-delete-3').textContent).toContain('Delete “checkout p95”?');
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    });
    expect(tid('slo-delete-3').textContent).toBe('Delete');
    expect(calls.methods).not.toContain('DELETE');

    // Re-arm, then a press outside the armed row (the backdrop, another
    // card, anywhere) disarms it too.
    await click(tid('slo-delete-3'));
    expect(tid('slo-delete-3').textContent).toContain('“checkout p95”?');
    await act(async () => {
      document.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    });
    expect(tid('slo-delete-3').textContent).toBe('Delete');
    expect(calls.methods).not.toContain('DELETE');
  });

  it('refetches budgets when the window changes', async () => {
    listSLOs = [checkoutSLO];
    await renderPanel();
    const budgetCallsBefore = calls.urls.filter(u => u.includes('/budget')).length;
    expect(budgetCallsBefore).toBeGreaterThan(0);
    await click(tid('slo-window-30d'));
    const budgetCallsAfter = calls.urls.filter(u => u.includes('/budget'));
    expect(budgetCallsAfter.length).toBeGreaterThan(budgetCallsBefore);
    expect(budgetCallsAfter.some(u => u.includes('window=30d'))).toBe(true);
    // The pressed state moved to the new window (marked by state, not
    // colour alone: aria-pressed).
    expect(tid('slo-window-30d').getAttribute('aria-pressed')).toBe('true');
    expect(tid('slo-window-7d').getAttribute('aria-pressed')).toBe('false');
  });

  it('does not refetch when the same window is re-picked', async () => {
    listSLOs = [checkoutSLO];
    await renderPanel();
    const budgetCallsBefore = calls.urls.filter(u => u.includes('/budget')).length;
    expect(budgetCallsBefore).toBeGreaterThan(0);
    await click(tid('slo-window-7d'));
    expect(calls.urls.filter(u => u.includes('/budget')).length).toBe(budgetCallsBefore);
  });

  it('rounds the budget remaining to one decimal with its unit', async () => {
    listSLOs = [checkoutSLO];
    budgetOverrides = {
      3: {
        metrics: [
          { metric: 'p95_ms', target: 250, actual: 187.5, compliant: true, budget_remaining_pct: 33.3333333 },
          { metric: 'success_ratio', target: 0.99, actual: 0.99, compliant: true, budget_remaining_pct: -12.344 },
        ],
      },
    };
    await renderPanel();
    expect(text()).toContain('+33.3% left');
    expect(text()).toContain('-12.3% left');
    // The formula's raw fractions never leak through.
    expect(text()).not.toContain('33.333');
  });

  it('shows a skeleton while budgets fetch and numbers once they land', async () => {
    listSLOs = [checkoutSLO];
    let releaseBudgets: ((r: Response) => void) | undefined;
    container = document.createElement('div');
    document.body.appendChild(container);
    calls = { urls: [], methods: [], bodies: [] };
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url === '/api/projects/1/slos') return json(listSLOs);
        if (url.includes('/budget')) {
          return new Promise<Response>(resolve => {
            releaseBudgets = resolve;
          });
        }
        return json({ message: 'unexpected ' + url }, 404);
      })
    );
    await act(async () => {
      root = createRoot(container as HTMLDivElement);
      root?.render(<SloPanel projectId={1} />);
    });
    await act(async () => {});
    // In flight: a skeleton row, distinct from both the empty list state
    // and the no-eligible-runs verdict.
    expect(tid('slo-budget-loading-3')).toBeTruthy();
    expect(tid('slo-budget-loading-3').textContent).not.toContain('no eligible runs');
    expect(document.querySelector('[data-testid="slo-budget-error-3"]')).toBeNull();
    await act(async () => {
      releaseBudgets?.(json(budgetFor(checkoutSLO)));
    });
    expect(document.querySelector('[data-testid="slo-budget-loading-3"]')).toBeNull();
    expect(tid('slo-metrics-3')).toBeTruthy();
  });

  it('renders a failed budget fetch as an explicit error, never eternal loading', async () => {
    listSLOs = [checkoutSLO];
    await renderPanel();
    // First load succeeds, so the baselines exist.
    expect(tid('slo-metrics-3')).toBeTruthy();
    budgetOverrides = { 3: { run_count: 1 } }; // unused; the stub now fails
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        calls.urls.push(url);
        if (url === '/api/projects/1/slos') return json(listSLOs);
        if (url.includes('/budget')) return json({ message: 'storage down' }, 500);
        return json({ message: 'unexpected ' + url }, 404);
      })
    );
    await click(tid('slo-window-30d'));
    // The window switch cleared the stale numbers and the failure rendered
    // as the failed row -- not a spinner that would spin forever.
    expect(tid('slo-budget-error-3')).toBeTruthy();
    expect(tid('slo-budget-error-3').textContent).toContain('Budget failed to load');
    expect(document.querySelector('[data-testid="slo-budget-loading-3"]')).toBeNull();
    // Re-picking the same window is the retry the row promises.
    const budgetCallsBefore = calls.urls.filter(u => u.includes('/budget')).length;
    await click(tid('slo-window-30d'));
    expect(calls.urls.filter(u => u.includes('/budget')).length).toBe(budgetCallsBefore + 1);
  });

  it('refuses a p95 target of 0 at the field (the API requires above 0), but sends error rate 0', async () => {
    listSLOs = [];
    await renderPanel();
    await type(tid('slo-name-input') as HTMLInputElement, 'zero p95');
    await type(tid('slo-p95-input') as HTMLInputElement, '0');
    await click(tid('slo-add-btn'));
    expect(tid('slo-form-error').textContent).toContain('p95 target must be a number above 0');
    expect(calls.methods).toEqual(['GET']); // no doomed POST left the browser

    // The zero-semantics twin: 0 IS a legal error-rate target ("no errors
    // allowed") and must reach the API, not be swallowed as "unset".
    await type(tid('slo-p95-input') as HTMLInputElement, '100');
    await type(tid('slo-error-input') as HTMLInputElement, '0');
    await click(tid('slo-add-btn'));
    expect(calls.methods).toContain('POST');
    const postIdx = calls.methods.indexOf('POST');
    expect(calls.bodies[postIdx]).toContain('target_error_rate=0');
    // The helper note states the rules as the API enforces them.
    expect(tid('slo-targets-note').textContent).toContain('above 0');
    expect(tid('slo-targets-note').textContent).toContain('0 is a legal');
  });
});
