// The share dialog's mounted interaction (phase 34), Reports.test.tsx's
// style: createRoot + act with fetch stubbed per-URL, driven through the
// run detail page so the open (the Share button) is under test too, not
// just the dialog in isolation.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import Reports from '../pages/Reports';
import type { Report } from '../api/reports';
import type { SeriesPoint } from '../api/series';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const reportFixture: Report = {
  execution_id: 1,
  scenario_id: 2,
  run_id: 9,
  started_at: '2026-09-04T10:00:00Z',
  ended_at: '2026-09-04T10:01:00Z',
  outcome: 'passed',
  requested: { concurrency: 10, throughput: 100 },
  achieved: { concurrency: 10, throughput: 95, samples: 6000, failed: 3 },
  error_rate: 0.0005,
  latency: { '50': 0.05, '95': 0.2 },
  attribution: { target: 2, engine: 1, unknown: 0 },
};

const seriesFixture: SeriesPoint[] = [];

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

// One existing link so the list renders alongside the minted one; the
// minted token is deterministic so the copy assertion can pin the URL.
const existingToken = 'e'.repeat(64);
const mintedToken = '7'.repeat(64);

interface ShareCalls {
  urls: string[];
  methods: string[];
  bodies: string[];
}

let container: HTMLDivElement | null = null;
let root: Root | null = null;
let calls: ShareCalls = { urls: [], methods: [], bodies: [] };

async function renderShareDialog(): Promise<void> {
  container = document.createElement('div');
  document.body.appendChild(container);
  calls = { urls: [], methods: [], bodies: [] };
  let existingLinkStillLive = true;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.urls.push(url);
      calls.methods.push((init?.method as string) ?? 'GET');
      calls.bodies.push(String(init?.body ?? ''));
      if (url.endsWith('/api/runs/9/report')) {
        return json(reportFixture);
      }
      if (url.endsWith('/api/runs/9/series')) {
        return json({ points: seriesFixture });
      }
      if (url.endsWith('/api/executions/1/reports')) {
        return json([]); // no siblings: the compare card stays hidden
      }
      if (url === '/api/runs/9/share' && (init?.method ?? 'GET') === 'GET') {
        return json(existingLinkStillLive ? [{ token: existingToken, created_by: 'dave', created_time: '2026-09-04T09:00:00Z', expires_at: null }] : []);
      }
      if (url === '/api/runs/9/share' && init?.method === 'POST') {
        return json({ token: mintedToken, url: `/share/${mintedToken}`, expires_at: '2026-10-04T10:00:00Z' }, 201);
      }
      if (url === `/api/runs/9/share/${existingToken}` && init?.method === 'DELETE') {
        existingLinkStillLive = false;
        return new Response(null, { status: 204 });
      }
      return json({ message: `no stub for ${url}` }, 500);
    })
  );
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter initialEntries={['/reports/9']}>
        <Routes>
          <Route path="/reports/:runId" element={<Reports />} />
        </Routes>
      </MemoryRouter>
    );
  });
  await act(async () => {});
}

function click(testId: string): void {
  const el = container!.querySelector(`[data-testid="${testId}"]`);
  if (el === null) {
    throw new Error(`missing ${testId}`);
  }
  el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
}

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

describe('ShareRunModal (mounted via the run detail page)', () => {
  it('opens from the Share button, mints a link with the chosen expiry, and copies the absolute URL', async () => {
    await renderShareDialog();

    // The dialog is absent until the header's Share button opens it.
    expect(container!.querySelector('[data-testid="share-modal"]')).toBeNull();
    await act(async () => {
      click('share-run-btn');
    });
    expect(container!.querySelector('[data-testid="share-modal"]')).not.toBeNull();
    // The existing link arrived with the dialog's own list fetch.
    expect(container!.textContent).toContain('never expires');
    expect(container!.querySelector(`[data-testid="share-link-${existingToken}"]`)).not.toBeNull();

    // 7-day lifetime, then mint.
    const select = container!.querySelector<HTMLSelectElement>('[data-testid="share-expiry-select"]')!;
    await act(async () => {
      select.value = '168';
      select.dispatchEvent(new Event('change', { bubbles: true }));
    });
    await act(async () => {
      click('generate-share-btn');
    });

    const minted = container!.querySelector('[data-testid="share-generated"]');
    expect(minted?.textContent).toContain(`${window.location.origin}/share/${mintedToken}`);
    expect(minted?.textContent).toContain('Expires');
    const issueCall = calls.urls.findIndex((u, i) => u === '/api/runs/9/share' && calls.methods[i] === 'POST');
    expect(issueCall).toBeGreaterThanOrEqual(0);
    expect(calls.bodies[issueCall]).toBe(JSON.stringify({ expires_in_hours: 168 }));

    // Copy puts the hand-out URL (origin + path) on the clipboard.
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    await act(async () => {
      click('copy-share-url');
    });
    expect(writeText).toHaveBeenCalledWith(`${window.location.origin}/share/${mintedToken}`);
    expect(container!.querySelector('[data-testid="copy-share-url"]')?.textContent).toContain('Copied');
  });

  it('revokes an existing link: the row disappears and the list refetches', async () => {
    await renderShareDialog();
    await act(async () => {
      click('share-run-btn');
    });
    expect(container!.querySelector(`[data-testid="share-link-${existingToken}"]`)).not.toBeNull();

    await act(async () => {
      click(`revoke-share-${existingToken}`);
    });
    await act(async () => {});

    expect(container!.querySelector(`[data-testid="share-link-${existingToken}"]`)).toBeNull();
    expect(container!.textContent).toContain('No links yet');
  });

  it('closes on Escape without minting anything', async () => {
    await renderShareDialog();
    await act(async () => {
      click('share-run-btn');
    });
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    });
    expect(container!.querySelector('[data-testid="share-modal"]')).toBeNull();
  });
});
