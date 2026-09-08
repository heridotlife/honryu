import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { parseShard, requestedLine, defaultBaselineRun, deltaTone, COMPARE_BAND_PCT, default as Reports } from './Reports';
import type { Report } from '../api/reports';
import type { SeriesPoint } from '../api/series';
import { SessionProvider } from '../hooks/useSession';
import { can as canOn } from '../api/session';
import { PROJECT_STORAGE_KEY } from '../components/ProjectSwitcher';

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
  report: Report = reportFixture,
  siblings: Report[] | null = null,
  baselineReports: Record<number, Report> = {}
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
      // The execution's runs (phase 33's compare card picks its baseline
      // from these); null keeps the old behaviour — endpoint fails, card
      // hides. Baseline reports served per run id, on pick.
      if (url.endsWith('/api/executions/1/reports')) {
        return siblings === null ? json({ message: 'no siblings' }, 500) : json(siblings);
      }
      for (const [id, rep] of Object.entries(baselineReports)) {
        if (url === `/api/runs/${id}/report`) {
          return json(rep);
        }
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
  localStorage.removeItem(PROJECT_STORAGE_KEY);
});

describe('ReportDetail time series (mounted)', () => {
  it('renders the hero and latency charts from the run\'s series points', async () => {
    await renderReportDetail();

    expect(container!.textContent).toContain('Time series');
    // The hero leads the time-series card; latency follows (the overlay
    // card adds its own).
    for (const id of ['chart-hero', 'chart-latency']) {
      const wrap = container!.querySelector(`[data-testid="${id}"]`);
      expect(wrap?.querySelector('svg[role="img"]')).not.toBeNull();
    }
    // The hero folds VUs, RPS, and error % into its one shared chart.
    expect(container!.querySelector('[data-series="VUs"]')).not.toBeNull();
    expect(container!.querySelector('[data-series="RPS"]')).not.toBeNull();
    expect(container!.querySelector('[data-series="error %"]')).not.toBeNull();
    // p95 is the default percentile selection.
    expect(container!.querySelector('[data-series="p95"]')).not.toBeNull();
    // The time axis formats ticks as wall-clock times.
    const xLabels = Array.from(container!.querySelectorAll('svg text')).map((t) => t.textContent);
    expect(xLabels.some((l) => /\d{2}:\d{2}:\d{2}/.test(l ?? ''))).toBe(true);
  });

  it('shows chart-hero first; the folded charts keep hidden testid wrappers', async () => {
    await renderReportDetail();

    const hero = container!.querySelector('[data-testid="chart-hero"]');
    const latency = container!.querySelector('[data-testid="chart-latency"]');
    expect(hero).not.toBeNull();
    expect(latency).not.toBeNull();
    // The hero comes first in DOM order.
    expect(hero!.compareDocumentPosition(latency!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    for (const id of ['chart-vus-rps', 'chart-errors']) {
      const wrap = container!.querySelector(`[data-testid="${id}"]`);
      expect(wrap).not.toBeNull();
      expect(wrap!.className).toContain('hidden');
      expect(wrap!.querySelector('svg')).toBeNull();
    }
    expect(container!.textContent).toContain('folded into hero chart');
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

/** The projects the phase 32 filter resolves names from (live shapes). */
const projectsFixture = [
  { id: 1, name: 'phase16-live', owner: 'honryu', tenant_id: 1, created_time: '2026-08-01T00:00:00Z' },
  { id: 2, name: 'phase16-sched', owner: 'heri', tenant_id: 1, created_time: '2026-08-02T00:00:00Z' },
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
          { id: 2, name: 'beta-exec', project_id: 2, engine: 'jmeter', created_time: '2026-09-02T00:00:00Z' },
        ]);
      }
      if (url.endsWith('/api/projects')) {
        return json(projectsFixture);
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

describe('ReportDetail thresholds card (phase 29)', () => {
  it('renders a verdict per configured criterion: pass, fail, unparsed', async () => {
    const report: Report = {
      ...reportFixture,
      criteria: ['failures>10%', 'p95>500ms', 'p99<1s for 5s'],
      failing_criteria: [{ criterion: 'p95>500ms' }, { criterion: 'p99<1s for 5s', unparsed: true }],
    };
    await renderReportDetail(() => json({ points: [] }), [], report);

    expect(container!.querySelector('[data-testid="thresholds-card"]')).not.toBeNull();
    // failures>10% passed: not named in failing_criteria.
    expect(container!.querySelector('[data-testid="threshold-pass-0"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="threshold-fail-0"]')).toBeNull();
    // p95>500ms tripped.
    expect(container!.querySelector('[data-testid="threshold-fail-1"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="threshold-pass-1"]')).toBeNull();
    // The for-window clause is outside the grammar: unknown, not failed.
    expect(container!.querySelector('[data-testid="threshold-unparsed-2"]')).not.toBeNull();
    expect(container!.textContent).toContain('could not be evaluated');
  });

  it('says no criteria are configured and points at the Configuration card', async () => {
    await renderReportDetail();

    expect(container!.querySelector('[data-testid="thresholds-card"]')).not.toBeNull();
    expect(container!.textContent).toContain('No criteria configured');
    expect(container!.textContent).toContain('Configuration card');
    expect(container!.querySelector('[data-testid="threshold-row-0"]')).toBeNull();
  });

  it('normalizes a null verdict from the wire into no criteria', async () => {
    // Go marshals a nil slice as null; getRunReport must normalize before
    // the card renders.
    const report = { ...reportFixture, criteria: null, failing_criteria: null };
    await renderReportDetail(() => json({ points: [] }), [], report);

    expect(container!.querySelector('[data-testid="thresholds-card"]')).not.toBeNull();
    expect(container!.textContent).toContain('No criteria configured');
  });
});

// Phase 32: the global project switcher's stored selection scopes the list
// client-side (project_id === selected), with a chip that clears back to
// all projects -- the same contract Executions.test.tsx pins mounted.
describe('ReportsList project filter (phase 32)', () => {
  it('scopes rows to the stored project and shows the project chip', async () => {
    localStorage.setItem(PROJECT_STORAGE_KEY, '1');
    await renderReportsList(navPersonas.alice);

    expect(container!.querySelector('[data-testid="execution-1"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="execution-2"]')).toBeNull();
    const chip = container!.querySelector('[data-testid="filter-project"]');
    expect(chip?.textContent).toContain('project: phase16-live');
  });

  it('clearing the chip returns every execution and unscopes the stored selection', async () => {
    localStorage.setItem(PROJECT_STORAGE_KEY, '2');
    await renderReportsList(navPersonas.alice);

    expect(container!.querySelector('[data-testid="execution-1"]')).toBeNull();
    await act(async () => {
      container!
        .querySelector('[data-testid="filter-project"] button')!
        .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await act(async () => {});

    expect(container!.querySelector('[data-testid="execution-1"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="execution-2"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="filter-project"]')).toBeNull();
    expect(localStorage.getItem(PROJECT_STORAGE_KEY)).toBe('');
  });

  it('shows every execution, with no chip, when no project is stored', async () => {
    await renderReportsList(navPersonas.alice);

    expect(container!.querySelector('[data-testid="execution-1"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="execution-2"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="filter-project"]')).toBeNull();
  });
});

// Phase 33: the Overview tab's Compare card — the run on screen against a
// picked baseline from the same execution. Pure helpers first (band edges,
// default pick), then the mounted card over stubbed sibling/baseline
// reports.
describe('deltaTone (phase 33)', () => {
  it('reads beyond +10% as worse for lower-is-better, beyond -10% as better', () => {
    expect(deltaTone('lower', 25)).toBe('worse');
    expect(deltaTone('lower', -25)).toBe('better');
  });

  it('flips for higher-is-better metrics (samples, rps)', () => {
    expect(deltaTone('higher', -25)).toBe('worse');
    expect(deltaTone('higher', 25)).toBe('better');
  });

  it('treats the band edges and inside as neutral — exactly ±10% does not shout', () => {
    expect(COMPARE_BAND_PCT).toBe(10);
    expect(deltaTone('lower', COMPARE_BAND_PCT)).toBe('neutral');
    expect(deltaTone('lower', -COMPARE_BAND_PCT)).toBe('neutral');
    expect(deltaTone('higher', 5)).toBe('neutral');
    expect(deltaTone('lower', 0)).toBe('neutral');
  });

  it('an unmeasured delta (null — baseline 0 or metric missing) stays neutral', () => {
    expect(deltaTone('lower', null)).toBe('neutral');
  });
});

describe('defaultBaselineRun (phase 33)', () => {
  it('picks the newest other passed run — the list arrives newest-first', () => {
    const others: Report[] = [
      { ...reportFixture, run_id: 8, outcome: 'failed' },
      { ...reportFixture, run_id: 7, outcome: 'passed' },
      { ...reportFixture, run_id: 6, outcome: 'passed' },
    ];
    expect(defaultBaselineRun(others)).toBe(7);
  });

  it('falls back to the newest other run when nothing passed yet', () => {
    const others: Report[] = [
      { ...reportFixture, run_id: 8, outcome: 'failed' },
      { ...reportFixture, run_id: 7, outcome: 'aborted' },
    ];
    expect(defaultBaselineRun(others)).toBe(8);
  });

  it('returns null with no other run — the card hides', () => {
    expect(defaultBaselineRun([])).toBeNull();
  });
});

describe('ReportDetail compare card (phase 33, mounted)', () => {
  // The run on screen: p95 improved 25% vs baseline 7, p99 regressed 25%,
  // samples down 16.7%, rps down 15.8%, error rate up 1100% — one of each
  // tone so the colouring is assertable end to end.
  const currentReport: Report = {
    ...reportFixture,
    latency: { '50': 0.05, '95': 0.15, '99': 0.5 },
    achieved: { concurrency: 10, throughput: 80, samples: 5000, failed: 30 },
    error_rate: 0.006,
  };
  const baseline7: Report = {
    ...reportFixture,
    run_id: 7,
    started_at: '2026-09-03T10:00:00Z',
    outcome: 'passed',
    latency: { '50': 0.05, '95': 0.2, '99': 0.4 },
    achieved: { concurrency: 10, throughput: 95, samples: 6000, failed: 3 },
    error_rate: 0.0005,
  };
  const baseline8: Report = {
    ...reportFixture,
    run_id: 8,
    started_at: '2026-09-04T09:00:00Z',
    outcome: 'failed',
    latency: { '50': 0.05, '95': 0.1, '99': 0.3 },
    achieved: { concurrency: 10, throughput: 99, samples: 6000, failed: 3 },
    error_rate: 0.0005,
  };
  // Newest first, the list endpoint's contract: 9 is the run on screen.
  const siblingList: Report[] = [
    currentReport,
    baseline8,
    baseline7,
  ];

  /** The delta cell of a metric row: the td after the two value cells. */
  const deltaCell = (name: string) =>
    container!.querySelector(`[data-testid="compare-metric-${name}"]`)?.querySelectorAll('td')[3] ?? null;

  it('renders under the thresholds card with computed, coloured deltas', async () => {
    await renderReportDetail(
      () => json({ points: [] }),
      [],
      currentReport,
      siblingList,
      { 7: baseline7 }
    );

    const card = container!.querySelector('[data-testid="compare-card"]');
    expect(card).not.toBeNull();
    // Directly under the thresholds card, before the Load card.
    const thresholds = container!.querySelector('[data-testid="thresholds-card"]');
    expect(thresholds!.compareDocumentPosition(card!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    // p95 150ms vs 200ms: -25%, emerald.
    const p95 = deltaCell('p95');
    expect(p95?.textContent).toBe('-25.0%');
    expect(p95?.className).toContain('emerald');
    // p99 500ms vs 400ms: +25%, rose.
    const p99 = deltaCell('p99');
    expect(p99?.textContent).toBe('+25.0%');
    expect(p99?.className).toContain('rose');
    // samples 5000 vs 6000: -16.7% (down >10%), rose.
    expect(deltaCell('samples')?.className).toContain('rose');
    // rps 80 vs 95: -15.8%, rose.
    expect(deltaCell('rps')?.className).toContain('rose');
    // error rate 0.6% vs 0.05%: rose.
    expect(deltaCell('error_rate')?.className).toContain('rose');
    // Latency renders ms although the wire carries seconds.
    const p95Values = container!.querySelector('[data-testid="compare-metric-p95"]')?.textContent;
    expect(p95Values).toContain('150.0 ms');
    expect(p95Values).toContain('200.0 ms');
  });

  it('defaults the baseline to the previous passed run, not the newest sibling', async () => {
    const calls: string[] = [];
    await renderReportDetail(() => json({ points: [] }), calls, currentReport, siblingList, {
      7: baseline7,
      8: baseline8,
    });

    // Sibling 8 (failed) is newer than 7 (passed): the default must skip it.
    expect(calls.some((u) => u === '/api/runs/7/report')).toBe(true);
    expect(container!.querySelector('[data-testid="compare-metric-p95"]')?.textContent).toContain('200.0 ms');
    expect(container!.querySelector('[data-testid="compare-baseline-toggle"]')?.textContent).toContain('#7');
  });

  it('picks another baseline from the popover and refetches its report', async () => {
    const calls: string[] = [];
    await renderReportDetail(() => json({ points: [] }), calls, currentReport, siblingList, {
      7: baseline7,
      8: baseline8,
    });
    expect(calls.filter((u) => u === '/api/runs/8/report').length).toBe(0);

    await act(async () => {
      container!
        .querySelector('[data-testid="compare-baseline-toggle"]')!
        .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    const opt8 = container!.querySelector('[data-testid="compare-baseline-8"]');
    expect(opt8?.textContent).toContain('#8');
    expect(opt8?.textContent).toContain('failed');
    // The current run is never an option.
    expect(container!.querySelector('[data-testid="compare-baseline-9"]')).toBeNull();

    await act(async () => {
      opt8!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await act(async () => {});

    expect(calls.filter((u) => u === '/api/runs/8/report').length).toBe(1);
    // p95 against run 8: 150ms vs 100ms = +50%, rose (it was emerald vs 7).
    const p95 = deltaCell('p95');
    expect(p95?.textContent).toBe('+50.0%');
    expect(p95?.className).toContain('rose');
  });

  it('hides the card entirely when the execution has no comparable run', async () => {
    await renderReportDetail(() => json({ points: [] }), [], currentReport, [currentReport]);
    expect(container!.querySelector('[data-testid="compare-card"]')).toBeNull();
  });

  it('hides the card when the sibling list cannot be fetched', async () => {
    await renderReportDetail(() => json({ points: [] }), [], currentReport, null);
    expect(container!.querySelector('[data-testid="compare-card"]')).toBeNull();
  });
});

// Task 3: the per-label p95 diff under the headline rows — shared labels
// as delta rows, one-sided labels badged new/dropped, alphabetical order.
describe('ReportDetail per-label p95 diff (phase 33, mounted)', () => {
  const withLabels: Report = {
    ...reportFixture,
    latency: { '50': 0.05, '95': 0.15, '99': 0.5 },
    labels: [
      { label: 'beta', samples: 50, failed: 0, error_rate: 0, latency: { '95': 0.3 } },
      { label: 'alpha', samples: 100, failed: 0, error_rate: 0, latency: { '95': 0.2 } },
    ],
  };
  const baseWithLabels: Report = {
    ...reportFixture,
    run_id: 7,
    started_at: '2026-09-03T10:00:00Z',
    outcome: 'passed',
    latency: { '50': 0.05, '95': 0.2, '99': 0.4 },
    labels: [
      { label: 'gamma', samples: 40, failed: 0, error_rate: 0, latency: { '95': 0.25 } },
      { label: 'alpha', samples: 100, failed: 0, error_rate: 0, latency: { '95': 0.1 } },
    ],
  };

  it('diffs shared labels and badges one-sided labels, alphabetically', async () => {
    await renderReportDetail(() => json({ points: [] }), [], withLabels, [withLabels, baseWithLabels], {
      7: baseWithLabels,
    });

    const table = container!.querySelector('[data-testid="compare-labels"]');
    expect(table?.textContent).toContain('Per-label p95 diff');

    // Alphabetical regardless of each report's own label order.
    const rows = Array.from(table!.querySelectorAll('tr[data-testid^="compare-label-"]'));
    expect(rows.map((r) => r.getAttribute('data-testid'))).toEqual([
      'compare-label-alpha',
      'compare-label-beta',
      'compare-label-gamma',
    ]);

    // Shared: alpha 200ms vs 100ms → +100%, rose. Wire seconds → ms.
    const alphaCells = rows[0].querySelectorAll('td');
    expect(alphaCells[1].textContent).toBe('200.0 ms');
    expect(alphaCells[2].textContent).toBe('100.0 ms');
    expect(alphaCells[3].textContent).toBe('+100.0%');
    expect(alphaCells[3].querySelector('span')?.className).toContain('rose');

    // Only in the current run: "new"; only in the baseline: "dropped".
    const betaCells = rows[1].querySelectorAll('td');
    expect(betaCells[1].textContent).toBe('300.0 ms');
    expect(betaCells[2].textContent).toBe('—');
    expect(betaCells[3].textContent).toContain('new');
    const gammaCells = rows[2].querySelectorAll('td');
    expect(gammaCells[1].textContent).toBe('—');
    expect(gammaCells[2].textContent).toBe('250.0 ms');
    expect(gammaCells[3].textContent).toContain('dropped');
  });

  it('hides the label table when neither run recorded labels', async () => {
    const current: Report = { ...reportFixture, latency: { '95': 0.15 } };
    const base: Report = { ...reportFixture, run_id: 7, outcome: 'passed', latency: { '95': 0.2 } };
    await renderReportDetail(() => json({ points: [] }), [], current, [current, base], { 7: base });

    expect(container!.querySelector('[data-testid="compare-card"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="compare-labels"]')).toBeNull();
  });
});
