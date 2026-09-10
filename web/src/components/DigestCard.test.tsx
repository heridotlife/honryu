// The Digest card's mounted interactions (phase 42), WebhooksCard.test's
// createRoot + act style with fetch stubbed per-URL: the config + feed
// pair renders (404 config reading as "off"), choosing a period PUTs it,
// "off" DELETEs, and the feed rows render window, run count, and outcome
// chips.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import DigestCard from './DigestCard';
import type { DigestRow } from '../api/digests';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const feed: DigestRow[] = [
  {
    id: 21,
    period: 'daily',
    window_start: '2026-09-09T00:00:00Z',
    window_end: '2026-09-10T00:00:00Z',
    runs_total: 7,
    by_outcome: { passed: 5, failed: 2, aborted: 0 },
    threshold_failures: 2,
    executions: [
      { execution_id: 3, name: 'checkout', runs: 5, worst_outcome: 'failed' },
      { execution_id: 4, name: 'search', runs: 2, worst_outcome: 'passed' },
    ],
  },
  {
    id: 20,
    period: 'daily',
    window_start: '2026-09-08T00:00:00Z',
    window_end: '2026-09-09T00:00:00Z',
    runs_total: 1,
    by_outcome: { passed: 0, failed: 0, aborted: 0 },
    threshold_failures: 0,
    executions: [{ execution_id: 3, name: 'checkout', runs: 1, worst_outcome: 'error' }],
  },
];

interface Calls {
  urls: string[];
  methods: string[];
  bodies: string[];
}

let container: HTMLDivElement | null = null;
let root: Root | null = null;
let calls: Calls = { urls: [], methods: [], bodies: [] };
// Scripted responses, switchable per test.
let configStatus = 404;
let configBody: unknown = { message: 'not configured' };

async function renderCard(projectId = 1): Promise<void> {
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
      if (url === `/api/projects/${projectId}/digest`) {
        const method = (init?.method as string) ?? 'GET';
        if (method === 'PUT') {
          return json({ project_id: projectId, period: 'daily', enabled: true });
        }
        if (method === 'DELETE') {
          return new Response(null, { status: 204 });
        }
        return json(configBody, configStatus);
      }
      if (url === `/api/projects/${projectId}/digests?limit=10`) {
        return json(feed);
      }
      return json({ message: 'unexpected ' + url }, 500);
    })
  );
  root = createRoot(container);
  await act(async () => {
    root!.render(<DigestCard projectId={projectId} />);
  });
  // Drain the config+feed pair's promise chain.
  await act(async () => {});
}

function teardown(): void {
  if (root) {
    act(() => root!.unmount());
  }
  container?.remove();
  container = null;
  root = null;
  vi.unstubAllGlobals();
}

afterEach(teardown);

describe('DigestCard', () => {
  it('renders the off state when no config exists (404) plus the feed rows', async () => {
    configStatus = 404;
    await renderCard();

    const card = container!.querySelector('[data-testid="digest-card"]');
    expect(card).not.toBeNull();
    // Off is pressed when the config GET 404s.
    const off = card!.querySelector('[data-testid="digest-toggle-off"]') as HTMLButtonElement;
    expect(off.getAttribute('aria-pressed')).toBe('true');

    const row = card!.querySelector('[data-testid="digest-row-0"]');
    expect(row?.textContent).toContain('7');
    expect(row?.textContent).toContain('runs');
    expect(row?.textContent).toContain('threshold failures');
    expect(card!.querySelector('[data-testid="digest-chip-passed"]')?.textContent).toContain('passed 5');
    expect(card!.querySelector('[data-testid="digest-chip-failed"]')?.textContent).toContain('failed 2');
    // No aborted runs: that chip is absent, not zero-rendered.
    expect(card!.querySelector('[data-testid="digest-chip-aborted"]')).toBeNull();
    // The error-outcome window renders the engine-error caption instead of chips.
    const errorRow = card!.querySelector('[data-testid="digest-row-1"]');
    expect(errorRow?.textContent).toContain('engine-error');
  });

  it('shows the configured period and last-fired caption when a schedule exists', async () => {
    configStatus = 200;
    configBody = {
      project_id: 1,
      period: 'weekly',
      enabled: true,
      last_fired: '2026-09-08T08:00:00Z',
    };
    await renderCard();

    const card = container!.querySelector('[data-testid="digest-card"]')!;
    const weekly = card.querySelector('[data-testid="digest-period-toggle-weekly"]') as HTMLButtonElement;
    expect(weekly.getAttribute('aria-pressed')).toBe('true');
    expect(card.textContent).toContain('last fired');
    const off = card.querySelector('[data-testid="digest-toggle-off"]') as HTMLButtonElement;
    expect(off.getAttribute('aria-pressed')).toBe('false');
  });

  it('PUTs the chosen period when a toggle is clicked', async () => {
    configStatus = 404;
    await renderCard();

    const daily = container!.querySelector('[data-testid="digest-period-toggle-daily"]') as HTMLButtonElement;
    await act(async () => {
      daily.click();
    });
    const put = calls.urls.findIndex((u, i) => u === '/api/projects/1/digest' && calls.methods[i] === 'PUT');
    expect(put).toBeGreaterThanOrEqual(0);
    expect(calls.bodies[put]).toContain('period=daily');
    // The pressed state follows the PUT's response, not the click alone.
    const pressed = container!.querySelector(
      '[data-testid="digest-period-toggle-daily"]'
    ) as HTMLButtonElement;
    expect(pressed.getAttribute('aria-pressed')).toBe('true');
  });

  it('DELETEs the schedule when off is chosen', async () => {
    configStatus = 200;
    configBody = { project_id: 1, period: 'daily', enabled: true };
    await renderCard();

    const off = container!.querySelector('[data-testid="digest-toggle-off"]') as HTMLButtonElement;
    expect(off.getAttribute('aria-pressed')).toBe('false');
    await act(async () => {
      off.click();
    });
    const del = calls.urls.findIndex((u, i) => u === '/api/projects/1/digest' && calls.methods[i] === 'DELETE');
    expect(del).toBeGreaterThanOrEqual(0);
    // Off is pressed after the 204.
    const nowOff = container!.querySelector('[data-testid="digest-toggle-off"]') as HTMLButtonElement;
    expect(nowOff.getAttribute('aria-pressed')).toBe('true');
  });

  it('surfaces the backend rejection instead of flipping the toggle', async () => {
    configStatus = 404;
    await renderCard();
    // Make the PUT fail from here on.
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        calls.urls.push(url);
        calls.methods.push((init?.method as string) ?? 'GET');
        if (url === '/api/projects/1/digest' && (init?.method as string) === 'PUT') {
          return json({ message: 'period must be daily or weekly' }, 400);
        }
        if (url === '/api/projects/1/digests?limit=10') {
          return json(feed);
        }
        return json({}, 404);
      })
    );
    const weekly = container!.querySelector('[data-testid="digest-period-toggle-weekly"]') as HTMLButtonElement;
    await act(async () => {
      weekly.click();
    });
    expect(container!.textContent).toContain('period must be daily or weekly');
    const stillOff = container!.querySelector('[data-testid="digest-toggle-off"]') as HTMLButtonElement;
    expect(stillOff.getAttribute('aria-pressed')).toBe('true');
  });
});
