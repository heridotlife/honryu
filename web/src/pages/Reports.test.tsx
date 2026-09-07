import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { parseShard, requestedLine, default as Reports } from './Reports';
import type { Report } from '../api/reports';
import type { SeriesPoint } from '../api/series';
import { SessionProvider } from '../hooks/useSession';
import { can as canOn } from '../api/session';

describe('parseShard', () => {
  it('parses 0-indexed shard numbers', () => {
    expect(parseShard('0')).toBe(0);
    expect(parseShard('3')).toBe(3);
    expect(parseShard(' 2 ')).toBe(2);
  });

  // Number('') is 0 in JS -- the empty-string guard keeps an empty input
  // from silently loading shard 0.
  it('rejects an empty input rather than treating it as shard 0', () => {
    expect(parseShard('')).toBeNull();
    expect(parseShard('  ')).toBeNull();
  });

  it('rejects negatives, fractions, and junk', () => {
    expect(parseShard('-1')).toBeNull();
    expect(parseShard('1.5')).toBeNull();
    expect(parseShard('abc')).toBeNull();
  });
});

// The mounted half, DashboardLayout.test.tsx's style: createRoot + act,
// fetch stubbed per-URL so the report and series endpoints the detail page
// fires side by side are both under test.
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

const seriesFixture: SeriesPoint[] = [
  { ts: 1_700_000_000, vus: 10, rps: 100, err_pct: 0.5, latency: { '50': 0.05, '90': 0.1, '95': 0.2, '99': 0.4 } },
  { ts: 1_700_000_001, vus: 10, rps: 102, err_pct: 0, latency: { '50': 0.055, '90': 0.11, '95': 0.21, '99': 0.41 } },
  { ts: 1_700_000_002, vus: 8, rps: 80, err_pct: 1.5, latency: { '50': 0.06, '90': 0.12, '95': 0.22, '99': 0.42 } },
];

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

let container: HTMLDivElement | null = null;
let root: Root | null = null;

async function renderReportDetail(
  seriesBody: () => Response = () => json({ points: seriesFixture }),
  calls: string[] = [],
  report: Report = reportFixture
) {
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      calls.push(url);
      if (url.endsWith('/api/runs/9/report')) {
        return json(report);
      }
      if (url.endsWith('/api/runs/9/series')) {
        return seriesBody();
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
  // Flush the report + series fetches' promise chains.
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

describe('ReportDetail time series (mounted)', () => {
  it('renders three charts from the run\'s series points', async () => {
    await renderReportDetail();

    expect(container!.textContent).toContain('Time series');
    // The time-series card's three charts (the overlay card adds its own).
    for (const id of ['chart-vus-rps', 'chart-errors', 'chart-latency']) {
      const wrap = container!.querySelector(`[data-testid="${id}"]`);
      expect(wrap?.querySelector('svg[role="img"]')).not.toBeNull();
    }
    expect(container!.querySelector('[data-series="VUs"]')).not.toBeNull();
    expect(container!.querySelector('[data-series="RPS"]')).not.toBeNull();
    expect(container!.querySelector('[data-series="error %"]')).not.toBeNull();
    // p95 is the default percentile selection.
    expect(container!.querySelector('[data-series="p95"]')).not.toBeNull();
    // The time axis formats ticks as wall-clock times.
    const xLabels = Array.from(container!.querySelectorAll('svg text')).map((t) => t.textContent);
    expect(xLabels.some((l) => /\d{2}:\d{2}:\d{2}/.test(l ?? ''))).toBe(true);
  });

  it('switches the latency percentile client-side: no refetch, new series', async () => {
    const calls: string[] = [];
    await renderReportDetail(() => json({ points: seriesFixture }), calls);
    const seriesCallsBefore = calls.filter((u) => u.endsWith('/api/runs/9/series')).length;
    expect(seriesCallsBefore).toBe(1);

    const pill = container!.querySelector('[data-testid="pct-50"]') as HTMLButtonElement;
    expect(pill).not.toBeNull();
    await act(async () => {
      pill.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });

    expect(calls.filter((u) => u.endsWith('/api/runs/9/series')).length).toBe(seriesCallsBefore);
    expect(container!.querySelector('[data-series="p50"]')).not.toBeNull();
    expect(container!.querySelector('[data-series="p95"]')).toBeNull();
    // The pill reflects the selection.
    expect(pill.getAttribute('aria-pressed')).toBe('true');
  });

  it('charts nothing but says so when the run has no series (pre-series-store runs)', async () => {
    await renderReportDetail(() => json({ points: [] }));

    expect(container!.querySelector('[data-testid="series-empty"]')).not.toBeNull();
    expect(container!.querySelectorAll('svg[role="img"]').length).toBe(0);
  });

  it('shows the error with a retry that refetches', async () => {
    const calls: string[] = [];
    await renderReportDetail(() => json({ message: 'series backend down' }, 500), calls);

    const alert = container!.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain('series backend down');
    expect(container!.querySelector('[data-testid="series-retry"]')).not.toBeNull();

    await act(async () => {
      container!
        .querySelector('[data-testid="series-retry"]')!
        .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(calls.filter((u) => u.endsWith('/api/runs/9/series')).length).toBe(2);
  });
});

describe('ReportDetail export + copy-link (mounted)', () => {
  it('renders export anchors and copy-link next to the run heading', async () => {
    await renderReportDetail();

    const csv = container!.querySelector('[data-testid="export-csv"]');
    expect(csv?.getAttribute('href')).toBe('/api/runs/9/export?format=csv');
    expect(csv?.hasAttribute('download')).toBe(true);
    expect(csv?.textContent).toContain('Export CSV');
    const json = container!.querySelector('[data-testid="export-json"]');
    expect(json?.getAttribute('href')).toBe('/api/runs/9/export?format=json');
    expect(json?.hasAttribute('download')).toBe(true);
    expect(json?.textContent).toContain('Export JSON');
    const pdf = container!.querySelector('[data-testid="export-pdf"]');
    expect(pdf?.getAttribute('href')).toBe('/api/runs/9/export?format=pdf');
    expect(pdf?.hasAttribute('download')).toBe(true);
    expect(pdf?.textContent).toContain('Export PDF');
    expect(container!.querySelector('[data-testid="copy-link"]')).not.toBeNull();
  });

  it('copy-link puts the page URL on the clipboard and confirms', async () => {
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    await renderReportDetail();

    await act(async () => {
      container!
        .querySelector('[data-testid="copy-link"]')!
        .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });

    expect(writeText).toHaveBeenCalledWith(window.location.href);
    expect(container!.querySelector('[data-testid="copy-link"]')?.textContent).toContain('Copied');
  });
});

describe('requestedLine', () => {
  const xs = [
    { x: 100 },
    { x: 110 },
    { x: 120 },
  ];

  it('returns nothing without points or without a requested concurrency', () => {
    expect(requestedLine({ concurrency: 10, throughput: 100 }, [])).toEqual([]);
    expect(requestedLine({ concurrency: 0, throughput: 100 }, xs)).toEqual([]);
  });

  it('holds the requested concurrency from the first sample to the last when no duration is known', () => {
    expect(requestedLine({ concurrency: 10, throughput: 100 }, xs)).toEqual([
      { x: 100, y: 10 },
      { x: 120, y: 10 },
    ]);
  });

  it('runs to first + duration_seconds when the load names one', () => {
    // The run ended early: requested outlives the achieved samples.
    expect(requestedLine({ concurrency: 10, throughput: 100, duration_seconds: 100 }, xs)).toEqual([
      { x: 100, y: 10 },
      { x: 200, y: 10 },
    ]);
    // The run overran its duration: requested stops at 105 while achieved
    // continues to 120.
    expect(requestedLine({ concurrency: 10, throughput: 100, duration_seconds: 5 }, xs)).toEqual([
      { x: 100, y: 10 },
      { x: 105, y: 10 },
    ]);
  });
});

describe('ReportDetail requested-vs-achieved overlay (mounted)', () => {
  it('renders both series with the requested/achieved legend, as distinct paths', async () => {
    await renderReportDetail();

    const overlay = container!.querySelector('[data-testid="chart-requested"]');
    expect(overlay).not.toBeNull();
    expect(overlay?.textContent).toContain('requested');
    expect(overlay?.textContent).toContain('achieved');

    const requestedPath = overlay?.querySelector('[data-series="requested"]');
    const achievedPath = overlay?.querySelector('[data-series="achieved"]');
    expect(requestedPath).not.toBeNull();
    expect(achievedPath).not.toBeNull();
    // Divergence (achieved 8-10 vs requested constant 10) must show as
    // distinct lines, not two coincident flats.
    expect(requestedPath?.getAttribute('d')).not.toBe(achievedPath?.getAttribute('d'));
  });

  it('hides the overlay card when the run has no series points', async () => {
    await renderReportDetail(() => json({ points: [] }));

    expect(container!.querySelector('[data-testid="series-empty"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="chart-requested"]')).toBeNull();
    expect(container!.textContent).not.toContain('Requested vs achieved');
  });

  it('hides the overlay card when the report carries no requested load', async () => {
    await renderReportDetail(
      () => json({ points: seriesFixture }),
      [],
      { ...reportFixture, requested: { concurrency: 0, throughput: 0 } }
    );

    expect(container!.querySelector('[data-testid="chart-requested"]')).toBeNull();
  });
});

// Task 10: the "Compare runs" nav surface. It lives on the Reports page
// header (an execution-scoped route cannot be a top-nav item), gated by
// the same report:read grant that shows the Reports nav item. Persona maps
// copied from DashboardLayout.test.tsx -- permissions exactly as authapp
// emits them; the nav is a pure function of this map.
const navPersonas: Record<string, Record<string, string[]>> = {
  alice: { '*': ['*'] },
  bob: {
    project: ['create', 'delete', 'list', 'read', 'update'],
    execution: ['create', 'delete', 'list', 'read', 'update'],
    scenario: ['create', 'delete', 'list', 'read', 'update'],
    run: ['create', 'delete', 'list', 'read', 'update'],
    schedule: ['create', 'delete', 'list', 'read', 'update'],
    report: ['list', 'read'],
  },
  carol: {
    project: ['list', 'read'],
    execution: ['list', 'read'],
    scenario: ['list', 'read'],
    run: ['list', 'read'],
    schedule: ['list', 'read'],
    report: ['list', 'read'],
  },
  dave: {
    campaign: ['admin', 'create', 'delete', 'list', 'read', 'update'],
    project: ['list', 'read'],
    execution: ['list', 'read'],
    schedule: ['list', 'read'],
    report: ['list', 'read'],
  },
};

/** Two reports so the compare link's >= 2 runs condition holds. */
const listFixture: Report[] = [
  reportFixture,
  { ...reportFixture, run_id: 7, started_at: '2026-09-03T10:00:00Z' },
];

async function renderReportsList(permissions: Record<string, string[]> | null, reports: Report[] = listFixture) {
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/api/me')) {
        return permissions
          ? json({ subject: 'demo:x', name: 'x', email: '', global_roles: [], tenants: {}, permissions, demo: true })
          : json({ message: 'unauthenticated' }, 401);
      }
      if (url === '/api/executions' || url.endsWith('/api/executions')) {
        return json([
          { id: 1, name: 'alpha-exec', project_id: 1, engine: 'jmeter', created_time: '2026-09-01T00:00:00Z' },
        ]);
      }
      if (url.endsWith('/api/executions/1/reports')) {
        return json(reports);
      }
      return json({ message: `no stub for ${url}` }, 500);
    })
  );
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter initialEntries={['/reports']}>
        <SessionProvider>
          <Routes>
            <Route path="/reports" element={<Reports />} />
          </Routes>
        </SessionProvider>
      </MemoryRouter>
    );
  });
  await act(async () => {});
}

/** Fills the execution-id form and submits it, flushing the load. */
async function loadExecution() {
  // List-first (phase 27): the manual form sits behind a toggle; open it
  // before the id input can be driven.
  await act(async () => {
    container!.querySelector('[data-testid="manual-id-toggle"]')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
  const input = container!.querySelector('input[type="number"]') as HTMLInputElement;
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  await act(async () => {
    setter.call(input, '1');
    input.dispatchEvent(new Event('input', { bubbles: true }));
  });
  await act(async () => {
    container!.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
  });
  await act(async () => {});
}

describe('ReportsList execution list (phase 27)', () => {
  it('renders executions as clickable rows that load their reports', async () => {
    await renderReportsList(navPersonas.alice);
    const row = container!.querySelector('[data-testid="execution-1"]') as HTMLButtonElement;
    expect(row).not.toBeNull();
    expect(row.textContent).toContain('alpha-exec');
    await act(async () => {
      row.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await act(async () => {});
    // Loading execution 1 puts its runs on screen (the fetch stub serves them).
    expect(container!.querySelector('[data-testid="compare-runs-link"]')).not.toBeNull();
  });
  it('hides the manual id input until asked for', async () => {
    await renderReportsList(navPersonas.alice);
    expect(container!.querySelector('input[type="number"]')).toBeNull();
    await act(async () => {
      container!.querySelector('[data-testid="manual-id-toggle"]')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(container!.querySelector('input[type="number"]')).not.toBeNull();
  });
});

describe('ReportsList compare link (mounted)', () => {
  it('appears for every persona that can read reports, deep-linking the loaded execution', async () => {
    for (const who of ['alice', 'bob', 'carol', 'dave']) {
      expect(canOn(navPersonas[who], 'report', 'read'), `persona ${who} precondition`).toBe(true);
      await renderReportsList(navPersonas[who]);
      await loadExecution();

      const link = container!.querySelector('[data-testid="compare-runs-link"]') as HTMLAnchorElement;
      expect(link, `persona ${who}`).not.toBeNull();
      expect(link.getAttribute('href'), `persona ${who}`).toBe('/executions/1/compare');
    }
  });

  it('stays hidden from a caller without report:read -- same grant as the Reports nav item', async () => {
    // A synthetic map that can see executions but not reports: the Reports
    // NAV item would drop out for this caller too.
    await renderReportsList({ execution: ['list', 'read'] });
    await loadExecution();

    expect(container!.querySelector('[data-testid="compare-runs-link"]')).toBeNull();
  });

  it('stays hidden until an execution with at least two runs is loaded', async () => {
    await renderReportsList(navPersonas.carol);

    // Nothing loaded yet: no execution to compare.
    expect(container!.querySelector('[data-testid="compare-runs-link"]')).toBeNull();

    // Once two runs are on screen the deep-link appears.
    await loadExecution();
    expect(container!.querySelector('[data-testid="compare-runs-link"]')).not.toBeNull();
  });

  it('keeps the link hidden for a single-run execution', async () => {
    await renderReportsList(navPersonas.carol, [reportFixture]);
    await loadExecution();

    expect(container!.querySelector('[data-testid="compare-runs-link"]')).toBeNull();
  });
});
