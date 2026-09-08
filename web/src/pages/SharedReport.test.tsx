// The public share page (phase 34), Reports.test.tsx's mounted style. The
// two contracts under test: a live token renders the run workspace
// read-only (no session affordances), and a dead link is one friendly
// message whatever killed it — unknown, revoked, or expired.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import SharedReport from './SharedReport';
import type { Report } from '../api/reports';
import type { SeriesPoint } from '../api/series';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const reportFixture: Report = {
  execution_id: 1,
  scenario_id: 2,
  run_id: 29,
  started_at: '2026-09-08T09:58:42Z',
  ended_at: '2026-09-08T09:59:44Z',
  outcome: 'passed',
  requested: { concurrency: 4, throughput: 0, duration_seconds: 60 },
  achieved: { concurrency: 4, throughput: 1116.6, duration_seconds: 66, samples: 73695 },
  error_rate: 0,
  latency: { '50': 0.002, '95': 0.007 },
  attribution: { target: 0, engine: 0, unknown: 0 },
  criteria: [],
  failing_criteria: [],
};

const seriesFixture: SeriesPoint[] = [
  { ts: 1_700_000_000, vus: 4, rps: 100, err_pct: 0, latency: { '50': 0.002, '90': 0.005, '95': 0.007, '99': 0.013 } },
];

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

let container: HTMLDivElement | null = null;
let root: Root | null = null;
let requestedUrls: string[] = [];

async function renderShared(sharedStatus: number): Promise<void> {
  container = document.createElement('div');
  document.body.appendChild(container);
  requestedUrls = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      requestedUrls.push(url);
      if (url === '/api/share/goodtoken') {
        return sharedStatus === 200 ? json(reportFixture) : json({ message: 'share link expired' }, sharedStatus);
      }
      // The workspace's own fetches: the series endpoint is sessioned, so on
      // the share page it may well 401 — the section renders its own error
      // state, which is the degradation under test, not a page failure.
      if (url.endsWith('/api/runs/29/series')) {
        return json({ points: seriesFixture });
      }
      return json({ message: `no stub for ${url}` }, 500);
    })
  );
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter initialEntries={['/share/goodtoken']}>
        <Routes>
          <Route path="/share/:token" element={<SharedReport />} />
        </Routes>
      </MemoryRouter>
    );
  });
  // Flush the shared fetch (and whatever the workspace kicked off).
  await act(async () => {});
  await act(async () => {});
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

describe('SharedReport (mounted)', () => {
  it('renders the run workspace from the token alone, without session affordances', async () => {
    await renderShared(200);

    // The workspace is the run detail's own tree: identity header, the tab
    // strip, and the thresholds card all present.
    expect(requestedUrls).toContain('/api/share/goodtoken');
    expect(container!.textContent).toContain('Run #29');
    expect(container!.querySelector('[data-testid="run-tabs"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="thresholds-card"]')).not.toBeNull();
    // Public data, exportable: the export group stays.
    expect(container!.querySelector('[data-testid="export-csv"]')).not.toBeNull();
    // Session-only affordances are absent: no Share button (nothing to mint
    // anonymously) and no prev/next jumps (listing runs needs a session).
    expect(container!.querySelector('[data-testid="share-run-btn"]')).toBeNull();
    expect(container!.querySelector('[data-testid="run-prev"]')).toBeNull();
    expect(container!.querySelector('[data-testid="run-next"]')).toBeNull();
    // No compare card: no siblings exist to compare against.
    expect(container!.querySelector('[data-testid="compare-card"]')).toBeNull();
    // The run's own fetches are the only ones; the page never asks for a
    // session or the execution's runs.
    expect(requestedUrls.some(u => u.includes('/api/me') || u.includes('/api/executions/1/reports'))).toBe(false);
  });

  it('shows one friendly invalid-or-expired message with a way back, for any dead link', async () => {
    for (const status of [404, 401]) {
      await renderShared(status);

      expect(container!.querySelector('[data-testid="shared-invalid"]')).not.toBeNull();
      expect(container!.textContent).toContain('This link is invalid or expired.');
      // No run header leaks for a dead link, and there is a way back.
      expect(container!.textContent).not.toContain('Run #29');
      const home = container!.querySelector('[data-testid="shared-home-btn"]');
      expect(home?.closest('a')?.getAttribute('href')).toBe('/');
    }
  });
});
