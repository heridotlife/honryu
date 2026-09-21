// The mounted half of the Execution page's live chart (DashboardLayout/
// Reports.test.tsx's createRoot + act style): fetch is stubbed per-URL for
// the page's snapshot endpoints, EventSource is faked for the stream, and
// the Live section's idle/disconnected/chart states are asserted as the
// DOM renders them. Execution.test.ts (the pure-function tests) stays
// untouched; these need JSX, hence a separate .tsx file.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import Execution from './Execution';
import { SessionProvider } from '../hooks/useSession';
import type { EngineMetric, ExecutionStatus } from '../api/status';
import type { ErrorSignatureHistory, TrendPoint } from '../api/trends';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const metric = (over: Partial<EngineMetric> = {}): EngineMetric => ({
  threads: 2,
  latency: 0.1,
  label: 'req',
  status: '200',
  raw: '',
  execution_id: '5',
  scenario_id: '1',
  engine_id: '0',
  run_id: '1',
  ...over,
});

/** EventSource stand-in: the page's stream needs construction, handlers, close. */
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  url: string;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  closed = false;
  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }
  close() {
    this.closed = true;
  }
  open() {
    this.onopen?.();
  }
  error() {
    this.onerror?.();
  }
  emit(m: EngineMetric) {
    this.onmessage?.({ data: JSON.stringify(m) });
  }
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const statusFixture = (phase: ExecutionStatus['phase']): ExecutionStatus => ({
  phase,
  pool_size: 0,
  status: [],
});

let container: HTMLDivElement | null = null;
let root: Root | null = null;

/** Renders /executions/5 with every fetch stubbed; info is a normal
 * jmeter execution (engine SET, kind normal -- execution 11's exact shape
 * from the phase 39 bug report), so CapacityPanel stays away: the card
 * mounts only on calibrate_engine executions. `infoOver` overrides the
 * info fixture's fields (e.g. kind: 'calibrate_engine'). `trend` seeds
 * /executions/5/trend (empty by default so the phase 33 strip stays
 * hidden); null fails the endpoint. `signatures` seeds the phase 37
 * failure-history endpoint (all-green by default -- the honest demo
 * state); null fails it. `calls` collects every fetched URL so tests can
 * assert refetch behaviour. */
async function renderExecution(
  phase: ExecutionStatus['phase'] = 'running',
  trend: TrendPoint[] | null = [],
  signatures: ErrorSignatureHistory | null = { execution_id: 5, grouped_by: 'label', groups: [] },
  calls: string[] = [],
  infoOver: Record<string, unknown> = {},
  statusOver: Record<string, unknown> = {},
  initialState: unknown = undefined,
  reports: unknown = [],
) {
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      calls.push(url);
      if (url.endsWith('/api/me')) {
        return json({
          subject: 'demo:a',
          name: 'a',
          email: '',
          global_roles: [],
          tenants: {},
          permissions: { '*': ['*'] },
          demo: true,
        });
      }
      if (url.endsWith('/api/executions/5')) {
        return json({
          id: 5,
          name: 'demo',
          project_id: 1,
          csv_split: false,
          created_time: '2026-09-05T10:00:00Z',
          load_profile: [],
          data: [],
          engine: 'jmeter',
          kind: 'normal',
          ...infoOver,
        });
      }
      if (url.endsWith('/api/executions/5/status')) {
        return json({ ...statusFixture(phase), ...statusOver });
      }
      if (url.endsWith('/api/executions/5/reports')) {
        return json(reports);
      }
      if (url.includes('/api/scenarios/1/capacity-profile/fanout')) {
        return json({ status: 'no_profile' });
      }
      if (url.includes('/api/executions/5/trend')) {
        return trend === null ? json({ message: 'trend backend down' }, 500) : json({ execution_id: 5, points: trend });
      }
      if (url.includes('/api/executions/5/error-signatures')) {
        return signatures === null ? json({ message: 'signatures backend down' }, 500) : json(signatures);
      }
      return json({ message: `no stub for ${url}` }, 500);
    })
  );
  vi.stubGlobal('EventSource', FakeEventSource);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter initialEntries={[{ pathname: '/executions/5', state: initialState }]}>
        <SessionProvider>
          <Routes>
            <Route path="/executions/:id" element={<Execution />} />
          </Routes>
        </SessionProvider>
      </MemoryRouter>
    );
  });
  // Flush the page's fetches.
  await act(async () => {});
}

afterEach(() => {
  vi.unstubAllGlobals();
  FakeEventSource.instances = [];
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

describe('Execution live section (mounted)', () => {
  it('renders the Live heading while running, disconnected first, then idle once the stream opens', async () => {
    await renderExecution('running');

    const section = container!.querySelector('[data-testid="live-section"]');
    expect(section).not.toBeNull();
    expect(section?.querySelector('h2')?.textContent).toBe('Live');

    // Before the first open: the disconnected banner, not the idle text.
    expect(container!.querySelector('[data-testid="live-disconnected"]')?.textContent).toContain(
      'Stream disconnected — reconnecting…'
    );
    expect(container!.querySelector('[data-testid="live-idle"]')).toBeNull();

    const source = FakeEventSource.instances[0];
    expect(source.url).toBe('/api/executions/5/stream');
    await act(async () => {
      source.open();
    });
    expect(container!.querySelector('[data-testid="live-disconnected"]')).toBeNull();
    expect(container!.querySelector('[data-testid="live-idle"]')?.textContent).toContain('Waiting for first events…');
  });

  it('charts streamed events and switches the latency percentile client-side', async () => {
    await renderExecution('running');
    const source = FakeEventSource.instances[0];
    await act(async () => {
      source.open();
      source.emit(metric({ threads: 3, latency: 0.12 }));
    });

    const vusChart = container!.querySelector('[data-testid="live-chart-vus-rps"]');
    expect(vusChart?.querySelector('[data-series="VUs"]')).not.toBeNull();
    expect(vusChart?.querySelector('[data-series="RPS"]')).not.toBeNull();

    // p95 is the default percentile selection.
    const latencyChart = container!.querySelector('[data-testid="live-chart-latency"]');
    expect(latencyChart?.querySelector('[data-series="p95"]')).not.toBeNull();

    const pill = container!.querySelector('[data-testid="pct-50"]') as HTMLButtonElement;
    expect(pill.getAttribute('aria-pressed')).toBe('false');
    await act(async () => {
      pill.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(latencyChart?.querySelector('[data-series="p50"]')).not.toBeNull();
    expect(latencyChart?.querySelector('[data-series="p95"]')).toBeNull();
    expect(pill.getAttribute('aria-pressed')).toBe('true');
  });

  it('keeps the charts visible under the disconnected banner when the stream drops mid-run', async () => {
    await renderExecution('running');
    const source = FakeEventSource.instances[0];
    await act(async () => {
      source.open();
      source.emit(metric());
    });
    expect(container!.querySelector('[data-testid="live-chart-vus-rps"]')).not.toBeNull();

    await act(async () => {
      source.error();
    });
    expect(container!.querySelector('[data-testid="live-disconnected"]')).not.toBeNull();
    // The data already received stays on screen.
    expect(container!.querySelector('[data-testid="live-chart-vus-rps"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="live-idle"]')).toBeNull();
  });

  it('hides the Live section entirely when idle with nothing received', async () => {
    await renderExecution('idle');
    await act(async () => {
      FakeEventSource.instances[0]?.open();
    });
    expect(container!.querySelector('[data-testid="live-section"]')).toBeNull();
    expect(container!.textContent).not.toContain('Waiting for first events');
  });
});

// Phase 39: the Capacity card mounts only on calibrate_engine executions.
// The bug: it mounted for ANY engine'd execution, so a normal soak's
// Calibrate button could only ever earn a 400 from calibrationapp.Trigger
// ("execution 11 is normal"). The default fixture is exactly that shape.
describe('Execution capacity panel gate (mounted)', () => {
  it('keeps the Capacity card off a normal execution even with an engine set', async () => {
    await renderExecution('running');
    expect(container!.querySelector('[data-testid="capacity-panel"]')).toBeNull();
  });

  it('mounts the Capacity card on a calibrate_engine execution', async () => {
    // The fan-out lookup is unstubbed and 500s; that is fine here -- the
    // panel still mounts (into its error state), which is the assertion.
    await renderExecution('running', [], undefined, [], { kind: 'calibrate_engine' });
    expect(container!.querySelector('[data-testid="capacity-panel"]')).not.toBeNull();
  });
});

// Phase 44: ONE calibrate verb per execution kind. The scenario-row
// "Calibrate scenario..." button creates a NEW calibration execution, so
// it exists only on normal executions; on a calibrate_engine execution the
// Capacity panel is the single surface and its trigger says "Run search".
// Before the split, both buttons rendered on exec 15 with the same label
// "Calibrate" -- two different verbs wearing one word.
const scenarioRow = {
  status: [{ scenario_id: 1, engines: 1, engines_deployed: 1, engines_reachable: true, in_progress: false }],
};

describe('Execution calibrate verb (mounted)', () => {
  it('normal execution: scenario row offers "Calibrate scenario...", no Capacity panel', async () => {
    await renderExecution('running', [], undefined, [], {}, scenarioRow);
    const btn = container!.querySelector('[data-testid="calibrate-scenario-btn"]');
    expect(btn?.textContent).toBe('Calibrate scenario...');
    expect(container!.querySelector('[data-testid="capacity-panel"]')).toBeNull();
  });

  it('calibrate execution: no scenario-row button; the panel offers "Run search" instead', async () => {
    await renderExecution('running', [], undefined, [], { kind: 'calibrate_engine' }, scenarioRow);
    expect(container!.querySelector('[data-testid="calibrate-scenario-btn"]')).toBeNull();
    const panel = container!.querySelector('[data-testid="capacity-panel"]');
    expect(panel).not.toBeNull();
    // The fan-out stub answers no_profile, so the panel's trigger renders.
    const runSearch = panel!.querySelector('button');
    expect(runSearch?.textContent).toBe('Run search');
  });
});

// Phase 33: the recent-runs strip under the header — pills from the
// execution's trend, newest first, outcome-coloured, with the REGRESSED
// mark exactly when the backend set regressed (omitempty: absent = false).
describe('Execution trend strip (mounted)', () => {
  const trendPoint = (over: Partial<TrendPoint>): TrendPoint => ({
    run_id: 1,
    outcome: 'passed',
    achieved_throughput: 95,
    requested_throughput: 100,
    error_rate: 0.001,
    p50: 0.05,
    p90: 0.1,
    p95: 0.2,
    p99: 0.4,
    hit_target_qps: true,
    has_comparable_predecessor: true,
    ...over,
  });

  it('renders the last runs as pills, newest first, outcome-coloured, regressed marked', async () => {
    await renderExecution('idle', [
      trendPoint({ run_id: 12, outcome: 'failed', hit_target_qps: false, regressed: true }),
      // regressed stays ABSENT on the wire for a healthy run: no badge.
      trendPoint({ run_id: 11 }),
      trendPoint({ run_id: 10, outcome: 'aborted', has_comparable_predecessor: false }),
    ]);

    const strip = container!.querySelector('[data-testid="trend-strip"]');
    expect(strip).not.toBeNull();
    expect(strip?.textContent).toContain('Recent runs');

    const pills = Array.from(strip!.querySelectorAll('a[data-testid^="trend-pill-"]'));
    // The endpoint's order is newest-first; the strip must not re-sort.
    expect(pills.map(p => p.getAttribute('data-testid'))).toEqual(['trend-pill-12', 'trend-pill-11', 'trend-pill-10']);
    // Outcome colours: failed rose, passed emerald, aborted slate.
    expect(pills[0].className).toContain('rose');
    expect(pills[1].className).toContain('emerald');
    expect(pills[2].className).toContain('slate');
    // Each pill deep-links its run's report.
    expect(pills[0].getAttribute('href')).toBe('/reports/12');

    // The regressed pill carries the badge and its tooltip; the healthy
    // one (field absent) and the aborted one carry nothing.
    const badge = strip!.querySelector('[data-testid="trend-regressed-12"]');
    expect(badge?.textContent).toBe('REGRESSED');
    expect(badge?.getAttribute('title')).toBe('missed target QPS vs previous hit');
    expect(strip!.querySelector('[data-testid="trend-regressed-11"]')).toBeNull();
    expect(strip!.querySelector('[data-testid="trend-regressed-10"]')).toBeNull();
  });

  it('hides the strip when the execution has no trend runs', async () => {
    await renderExecution('idle', []);
    expect(container!.querySelector('[data-testid="trend-strip"]')).toBeNull();
  });

  it('hides the strip when the trend endpoint fails', async () => {
    await renderExecution('idle', null);
    expect(container!.querySelector('[data-testid="trend-strip"]')).toBeNull();
  });
});

// The Execution page is deep-linkable (/executions/{id}); its CopyLink
// (task 4) hands that URL to a colleague. Same mounted harness as the live
// section, plus the clipboard stub CopyLink.test.tsx uses.
describe('Execution failure history (mounted)', () => {
  const signatureHistory = (over: Partial<ErrorSignatureHistory> = {}): ErrorSignatureHistory => ({
    execution_id: 5,
    grouped_by: 'label',
    groups: [
      {
        key: 'GET /orders',
        total_count: 12,
        rows: [
          { label: 'GET /orders', response_code: '503', side: 'target', total_count: 9, run_count: 3 },
          { label: 'GET /orders', response_code: '500', side: 'target', total_count: 3, run_count: 2 },
        ],
      },
    ],
    ...over,
  });

  it('renders the all-green empty state (demo data has no failures)', async () => {
    await renderExecution('idle');

    const card = container!.querySelector('[data-testid="signature-history"]');
    expect(card).not.toBeNull();
    expect(card?.textContent).toContain("No failures recorded across this execution's runs.");
    // The default axis is label.
    expect(card!.querySelector('[data-testid="signature-group-by-label"]')?.getAttribute('aria-pressed')).toBe('true');
    expect(card!.querySelector('[data-testid="signature-group-by-code"]')?.getAttribute('aria-pressed')).toBe('false');
  });

  it('lists groups and reveals leaf signature rows on expand', async () => {
    await renderExecution('idle', [], signatureHistory());

    const card = container!.querySelector('[data-testid="signature-history"]')!;
    // Group row: key and the safe re-summed total. The group row carries no
    // run count on purpose -- summing leaf run_counts double-counts a run
    // that hit two codes under one label.
    expect(card.textContent).toContain('GET /orders');
    expect(card.textContent).toContain('12');
    // Leaf rows are hidden until the group expands.
    const toggle = card.querySelector('button[aria-expanded]') as HTMLButtonElement;
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(card.textContent).not.toContain('target | 503 |');

    await act(async () => {
      toggle.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    expect(card.textContent).toContain('target | 503 | GET /orders');
    expect(card.textContent).toContain('9');
    expect(card.textContent).toContain('3 runs');
  });

  it('switching the group-by axis refetches with by=code and re-collapses', async () => {
    const calls: string[] = [];
    await renderExecution('idle', [], signatureHistory(), calls);
    expect(calls.some(u => u.endsWith('/api/executions/5/error-signatures'))).toBe(true);

    const code = container!.querySelector('[data-testid="signature-group-by-code"]') as HTMLButtonElement;
    await act(async () => {
      code.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    // The non-default axis rides the query string (trends.ts contract).
    expect(calls.some(u => u.endsWith('/api/executions/5/error-signatures?by=code'))).toBe(true);
    expect(code.getAttribute('aria-pressed')).toBe('true');
  });

  it('surfaces a failed endpoint as an alert, not a crash', async () => {
    await renderExecution('idle', [], null);

    const card = container!.querySelector('[data-testid="signature-history"]')!;
    expect(card.querySelector('[role="alert"]')?.textContent).toContain('signatures backend down');
  });
});

describe('Execution copy-link (mounted)', () => {
  it('renders near the heading and copies the page URL on click', async () => {
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    await renderExecution('running');

    const btn = container!.querySelector('[data-testid="copy-link"]');
    expect(btn?.textContent).toContain('Copy link');

    await act(async () => {
      btn!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });

    expect(writeText).toHaveBeenCalledTimes(1);
    expect(writeText).toHaveBeenCalledWith(window.location.href);
    expect(container!.querySelector('[data-testid="copy-link"]')?.textContent).toContain('Copied');
  });
});

// Phase 24: the action-error block surfaces the structured details envelope
// when the failing response carried one (429 quota refusal), and stays a
// single message line when it did not. Phase 93: Stop is the hub's one
// direct mutation, so it is the vehicle here; the Start flow's own error
// surfacing (deploy/trigger failures) is covered in
// Execution.start.test.tsx. The fetch stub is swapped after mount because
// the mutation fetches at click time.
describe('Execution action-error details (mounted)', () => {
  const clickStop = async () => {
    const btn = container!.querySelector('[data-testid="lifecycle-stop"]');
    expect(btn).not.toBeNull();
    await act(async () => {
      btn!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await act(async () => {});
  };

  it('renders the hint code line and the quota numbers under the message', async () => {
    await renderExecution('running');
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        if (String(input).endsWith('/api/executions/5/stop')) {
          return json(
            {
              message: 'reservation would exceed tenant quota',
              details: {
                tenant_id: 1,
                cluster: '',
                requested: 2,
                used: 0,
                ceiling: 1,
                hint: 'PUT /api/tenants/{tenant_id}/quota ceiling=1',
              },
            },
            429
          );
        }
        return json({ message: 'no stub' }, 500);
      })
    );

    await clickStop();

    expect(container!.querySelector('[role="alert"]')?.textContent).toContain('reservation would exceed tenant quota');
    const details = container!.querySelector('[data-testid="action-error-details"]');
    expect(details?.querySelector('code')?.textContent).toBe('PUT /api/tenants/{tenant_id}/quota ceiling=1');
    expect(details?.textContent).toContain('used 0 / ceiling 1 — requested 2');
  });

  it('message-only failures keep the single alert line (no details node)', async () => {
    await renderExecution('running');
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        if (String(input).endsWith('/api/executions/5/stop')) {
          return json({ message: 'execution not found' }, 404);
        }
        return json({ message: 'no stub' }, 500);
      })
    );

    await clickStop();

    expect(container!.querySelector('[role="alert"]')?.textContent).toContain('execution not found');
    expect(container!.querySelector('[data-testid="action-error-details"]')).toBeNull();
  });
});

// Phase 51 audit follow-up, re-pinned for phase 93's two-button hub:
// Start wears the accent (amber — the page's ONE primary CTA, phase 52's
// rule), Stop keeps the neutral outline. The wiring predates the audit;
// this pins it so a Button refactor cannot silently drop the accent
// distinction. Test-only: no visual change.
describe('Execution lifecycle button variants (phase 51/93)', () => {
  it('paints Start accent (amber) and Stop outline', async () => {
    // running: Stop is the rendered control.
    await renderExecution('running');
    const stop = container!.querySelector('[data-testid="lifecycle-stop"]') as HTMLButtonElement;
    expect(stop.className).toContain('border-slate-300');
    expect(stop.className).not.toContain('bg-amber-600');
    expect(stop.className).not.toContain('text-red-600');

    // idle: Start is the offered action (accent amber — not the sky
    // gradient, not red).
    await renderExecution('idle');
    const start = container!.querySelector('[data-testid="lifecycle-start"]') as HTMLButtonElement;
    expect(start.className).toContain('bg-amber-600');
    expect(start.className).not.toContain('from-sky-500');
    expect(start.className).not.toContain('text-red-600');

    // deployed: Start again — the straight-countdown entry.
    await renderExecution('deployed');
    const startAgain = container!.querySelector('[data-testid="lifecycle-start"]') as HTMLButtonElement;
    expect(startAgain.className).toContain('bg-amber-600');
  });
});

// Phase 52 heading contract: the page h1 stays "Execution #5" and every
// section is an h2 -- Past runs, Failure history across runs, Scenario
// editor, Engine logs, Live -- with no h3 anywhere between. CardTitle used
// to render h3, which skipped a level under the h1 (the operator audit's
// outline finding); this test fails if the outline regresses.
describe('Execution heading outline (phase 52)', () => {
  it('renders the four sections as h2 and no h3 under the h1', async () => {
    // A running execution with one deployed scenario: every section card
    // renders (the scenario cards need status rows to exist).
    await renderExecution(
      'running',
      [],
      { execution_id: 5, grouped_by: 'label', groups: [] },
      [],
      {},
      {
        status: [{ scenario_id: 1, engines: 1, engines_deployed: 1, engines_reachable: true, in_progress: false }],
      }
    );

    const headings = Array.from(container!.querySelectorAll('h1, h2, h3'));
    const h1s = headings.filter(h => h.tagName === 'H1');
    const h2s = headings.filter(h => h.tagName === 'H2');
    const h3s = headings.filter(h => h.tagName === 'H3');

    // Exactly one h1: the page title.
    expect(h1s.map(h => h.textContent)).toEqual(['Execution #5']);
    // Every section is an h2: at least the five named ones.
    expect(h2s.length).toBeGreaterThanOrEqual(5);
    const h2Text = h2s.map(h => h.textContent?.trim());
    for (const label of ['Past runs', 'Failure history across runs', 'Scenario editor', 'Engine logs', 'Live']) {
      expect(h2Text, `missing h2 section "${label}"`).toContain(label);
    }
    // No skipped level anywhere on the page.
    expect(h3s).toEqual([]);
  });
});

// Phase 52: the hub's breadcrumb -- "Scenarios / #5" (the trail root moved
// to the scenario list in phase 67b; the flat /executions list redirects
// there). One ancestor link back, the current page marked aria-current.
describe('Execution breadcrumbs (phase 52)', () => {
  it('renders the trail with one link and aria-current on the id', async () => {
    await renderExecution('idle');

    const nav = container!.querySelector('nav[aria-label="breadcrumb"]');
    expect(nav).not.toBeNull();
    const items = Array.from(nav!.querySelectorAll('li'));
    expect(items.map(li => li.textContent?.trim())).toEqual(['Scenarios', '#5']);
    const links = Array.from(nav!.querySelectorAll('a')).map(a => a.getAttribute('href'));
    expect(links).toEqual(['/scenarios']);
    const current = nav!.querySelector('[aria-current="page"]');
    expect(current?.textContent).toBe('#5');
    expect(nav!.querySelectorAll('svg[aria-hidden="true"]').length).toBe(1);
  });
});

// Phase 54: the fan-out planner extends to NORMAL executions -- but only
// when a scenario is actually bound (an unbound execution has no scenario
// to fan out from), and collapsed by default so an untouched card never
// fetches.
describe('Execution capacity planner (phase 54)', () => {
  it('stays hidden when no scenario is bound', async () => {
    await renderExecution('idle');

    expect(container!.querySelector('[aria-label="Capacity planner"]')).toBeNull();
  });

  it('mounts collapsed for a bound normal execution', async () => {
    const calls: string[] = [];
    await renderExecution(
      'idle',
      [],
      { execution_id: 5, grouped_by: 'label', groups: [] },
      calls,
      {},
      {
        status: [{ scenario_id: 1, engines: 2, engines_deployed: 2, engines_reachable: true, in_progress: false }],
      }
    );

    const region = container!.querySelector('[aria-label="Capacity planner"]');
    expect(region).not.toBeNull();
    const details = region!.querySelector('[data-testid="planner-details"]') as HTMLDetailsElement;
    expect(details).not.toBeNull();
    expect(details.open).toBe(false);
    // The planner's own Compute is explicit: no planner-driven fan-out at
    // mount (the scenario editor's save-guard may probe separately).
    expect(calls.some((url: string) => url.includes('target_qps=100'))).toBe(false);
  });

  it('stays off calibrate_engine executions, which keep the calibration panel', async () => {
    await renderExecution(
      'idle',
      [],
      { execution_id: 5, grouped_by: 'label', groups: [] },
      [],
      { kind: 'calibrate_engine' },
      {
        status: [{ scenario_id: 1, engines: 2, engines_deployed: 2, engines_reachable: true, in_progress: false }],
      }
    );

    expect(container!.querySelector('[aria-label="Capacity planner"]')).toBeNull();
    expect(container!.querySelector('[data-testid="capacity-panel"]')).not.toBeNull();
  });
});

// Phase 62: the "Calibration spec" card -- what a calibrate_engine execution
// was configured with, served additively on the execution detail. Rendered
// only when the backend actually served a spec; a normal execution (and a
// calibrate-kind row without recorded bounds) gets no section at all.
describe('Execution calibration spec (phase 62)', () => {
  const spec = {
    criterion: 'failures>5%',
    seed_qps: 10,
    max_qps: 10000,
    max_steps: 20,
    hold_seconds: 30,
    cpu: '1',
    memory: '512Mi',
  };

  it('renders criterion as code plus the range/budget/pod lines on a calibrate execution', async () => {
    await renderExecution('running', [], undefined, [], { kind: 'calibrate_engine', calibration: spec });
    const card = container!.querySelector('[data-testid="calibration-spec"]');
    expect(card).not.toBeNull();
    expect(card!.querySelector('[data-testid="calibration-spec-criterion"]')?.textContent).toBe('failures>5%');
    const text = card!.textContent ?? '';
    expect(text).toContain('seed 10 → max 10000 qps');
    expect(text).toContain('up to 20 steps · 30s hold per step');
    expect(text).toContain('pod: 1 CPU · 512Mi memory');
  });

  it('renders no section on a normal execution', async () => {
    await renderExecution('running');
    expect(container!.querySelector('[data-testid="calibration-spec"]')).toBeNull();
  });

  it('renders no section on a calibrate_kind execution whose spec the backend omitted', async () => {
    await renderExecution('running', [], undefined, [], { kind: 'calibrate_engine' });
    expect(container!.querySelector('[data-testid="calibration-spec"]')).toBeNull();
  });
});

// Phase 88: a fan-out execution's own section -- the badge in the header,
// one readiness card per target cluster (each read through the status
// endpoint's cluster scoping, so a lagging cluster shows as ITS gap), and
// the latest run's per-cluster results table. An ordinary execution mounts
// none of it.
describe('Execution fan-out section (phase 88, mounted)', () => {
  /** Renders /executions/5 as a two-target fan-out execution: per-cluster
   * status fixtures differ so the test can tell the two reads apart, and
   * reports[0] carries a per-cluster breakdown. */
  async function renderFanOutExecution(reports: unknown[] = []) {
    container = document.createElement('div');
    document.body.appendChild(container);
    const fetched: string[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        fetched.push(url);
        if (url.endsWith('/api/me')) {
          return json({
            subject: 'demo:a',
            name: 'a',
            email: '',
            global_roles: [],
            tenants: {},
            permissions: { '*': ['*'] },
            demo: true,
          });
        }
        if (url.endsWith('/api/executions/5')) {
          return json({
            id: 5,
            name: 'everywhere',
            project_id: 1,
            csv_split: false,
            created_time: '2026-09-05T10:00:00Z',
            load_profile: [],
            data: [],
            engine: 'jmeter',
            kind: 'normal',
            fanout_targets: ['eu-1', 'us-1'],
          });
        }
        // Per-cluster scoping: eu-1 fully up, us-1 still one pod short and
        // unreachable -- the lagging cluster must stay visible as its own.
        if (url.endsWith('/api/executions/5/status?cluster=eu-1')) {
          return json({
            phase: 'deployed',
            pool_size: 2,
            status: [{ scenario_id: 1, engines: 2, engines_deployed: 2, engines_reachable: true, in_progress: false }],
          });
        }
        if (url.endsWith('/api/executions/5/status?cluster=us-1')) {
          return json({
            phase: 'deployed',
            pool_size: 1,
            status: [{ scenario_id: 1, engines: 2, engines_deployed: 1, engines_reachable: false, in_progress: false }],
          });
        }
        if (url.endsWith('/api/executions/5/status')) {
          return json(statusFixture('deployed'));
        }
        if (url.endsWith('/api/executions/5/reports')) {
          return json(reports);
        }
        if (url.includes('/api/scenarios/1/capacity-profile/fanout')) {
          return json({ status: 'no_profile' });
        }
        if (url.includes('/api/executions/5/trend')) {
          return json({ execution_id: 5, points: [] });
        }
        if (url.includes('/api/executions/5/error-signatures')) {
          return json({ execution_id: 5, grouped_by: 'label', groups: [] });
        }
        return json({ message: `no stub for ${url}` }, 500);
      })
    );
    vi.stubGlobal('EventSource', FakeEventSource);
    root = createRoot(container);
    await act(async () => {
      root!.render(
        <MemoryRouter initialEntries={['/executions/5']}>
          <SessionProvider>
            <Routes>
              <Route path="/executions/:id" element={<Execution />} />
            </Routes>
          </SessionProvider>
        </MemoryRouter>
      );
    });
    await act(async () => {});
    return fetched;
  }

  it('renders the fan-out badge and one card per target, each via its own cluster-scoped status read', async () => {
    const fetched = await renderFanOutExecution();

    expect(container!.querySelector('[data-testid="fanout-badge"]')?.textContent).toContain('fan-out ×2');
    const section = container!.querySelector('[data-testid="fanout-section"]');
    expect(section).not.toBeNull();
    expect(fetched).toContain('/api/executions/5/status?cluster=eu-1');
    expect(fetched).toContain('/api/executions/5/status?cluster=us-1');

    const eu = container!.querySelector('[data-testid="fanout-cluster-card-eu-1"]');
    const us = container!.querySelector('[data-testid="fanout-cluster-card-us-1"]');
    expect(eu).not.toBeNull();
    expect(us).not.toBeNull();
    // The two cards show their OWN clusters' readiness, not the aggregate.
    expect(eu!.querySelector('[data-testid="fanout-cluster-eu-1-scenarios"]')!.textContent).toContain('2/2 deployed');
    expect(us!.querySelector('[data-testid="fanout-cluster-us-1-scenarios"]')!.textContent).toContain('1/2 deployed');
    expect(us!.textContent).toContain('unreachable');
  });

  it('renders the latest run per-cluster results when the report carries them, and nothing when it does not', async () => {
    const report = {
      run_id: 7,
      started_at: '2026-09-05T10:00:00Z',
      ended_at: '2026-09-05T10:05:00Z',
      outcome: 'passed',
      samples: 150,
      failed: 30,
      avg_latency: 0.1,
      peak_rps: 100,
      cluster_results: [
        { cluster: 'eu-1', outcome: 'passed', samples: 100, failed: 10 },
        { cluster: 'us-1', outcome: 'passed', samples: 50, failed: 20 },
      ],
    };
    await renderFanOutExecution([report]);
    const table = container!.querySelector('[data-testid="fanout-latest-cluster-results"]')!;
    expect(table).not.toBeNull();
    const rows = Array.from(table.querySelectorAll('tbody tr')).map(tr => tr.textContent);
    expect(rows[0]).toContain('eu-1');
    expect(rows[0]).toContain('100');
    expect(rows[1]).toContain('us-1');
    expect(rows[1]).toContain('50');

    // A report without the split (single-cluster, or a restart lost it)
    // mounts no empty table -- honest absence.
    await renderFanOutExecution([{ ...report, cluster_results: undefined }]);
    expect(container!.querySelector('[data-testid="fanout-cluster-results"]')).toBeNull();
  });

  it('mounts no fan-out section on an ordinary single-cluster execution', async () => {
    await renderExecution('deployed');
    expect(container!.querySelector('[data-testid="fanout-section"]')).toBeNull();
    expect(container!.querySelector('[data-testid="fanout-badge"]')).toBeNull();
  });
});

// Phase 90: the execution page's mode surfaces -- the header chip stating
// what was asked for, and the arrival banner a refused Simple submit
// navigated here with (this page owns the Calibrate remediation).
describe('Execution page mode surfaces (phase 90)', () => {
  const modeEntry = {
    name: 'checkout',
    scenario_id: 1,
    concurrency: 375,
    rampup: 0,
    engines: 4,
    throughput: 500,
    duration: 600,
    mode: 'burst',
  };

  it('shows the mode chip on the header for a mode-derived execution', async () => {
    await renderExecution('idle', [], { execution_id: 5, grouped_by: 'label', groups: [] }, [], {
      load_profile: [modeEntry],
    });
    const chip = container!.querySelector('[data-testid="mode-chip"]');
    expect(chip?.textContent).toBe('burst · 500 rps · 10m');
  });

  it('shows no chip for an advanced execution', async () => {
    await renderExecution('idle', [], { execution_id: 5, grouped_by: 'label', groups: [] });
    expect(container!.querySelector('[data-testid="mode-chip"]')).toBeNull();
  });

  it('renders the carried-over config refusal once, with its structured details', async () => {
    await renderExecution(
      'idle',
      [],
      { execution_id: 5, grouped_by: 'label', groups: [] },
      [],
      {},
      {},
      {
        configError:
          'Step "save load config" failed: executionapp: mode config refused: capacity profile status "no_profile"',
        configErrorDetail: {
          fanout_status: 'no_profile',
          hint: 'calibrate this scenario first (Execution page → Calibrate scenario), or configure it in Advanced mode',
        },
      }
    );
    const banner = container!.querySelector('[data-testid="config-error-banner"]');
    expect(banner?.querySelector('[role="alert"]')?.textContent).toContain('no_profile');
    expect(banner?.querySelector('[data-testid="action-error-details"]')?.textContent).toContain(
      'calibrate this scenario first'
    );
    expect(banner?.textContent).toContain('The test was created, but its load configuration was not saved.');
  });

  it('renders no banner for an arrival without config state', async () => {
    await renderExecution('idle', [], { execution_id: 5, grouped_by: 'label', groups: [] });
    expect(container!.querySelector('[data-testid="config-error-banner"]')).toBeNull();
  });
});

// Phase 99's render matrix on the execution page: the Past runs card is the
// report surface here, and a leak-suspected run's row carries the amber
// banner under it -- nothing for a flat trend (data, not a finding) or a run
// without one (every window under two minutes).
describe('Execution past-runs soak leak banner (phase 99)', () => {
  const row = (over: Record<string, unknown>) => ({
    run_id: 9,
    outcome: 'passed',
    started_at: '2026-09-05T10:00:00Z',
    ...over,
  });

  it('renders the banner under a leak-suspected run only, carrying the figures', async () => {
    await renderExecution('running', [], undefined, [], {}, {}, undefined, [
      row({
        run_id: 21,
        soak_trend: { first_half_ms: 174.6, second_half_ms: 325.4, slope_ms_per_min: 120.8, leak_suspected: true },
      }),
      row({
        run_id: 22,
        soak_trend: { first_half_ms: 200.2, second_half_ms: 199.8, slope_ms_per_min: 0, leak_suspected: false },
      }),
      row({ run_id: 23 }),
    ]);

    const banners = container!.querySelectorAll('[data-testid="soak-leak-banner"]');
    expect(banners.length).toBe(1);
    expect(banners[0].textContent).toContain('175ms → 325ms');
    expect(banners[0].textContent).toContain('+121ms/min');
    expect(banners[0].textContent).toContain('consistent with a resource leak at steady load');
  });

  it('renders nothing when no run is suspected', async () => {
    await renderExecution('running', [], undefined, [], {}, {}, undefined, [
      row({
        run_id: 22,
        soak_trend: { first_half_ms: 200.2, second_half_ms: 199.8, slope_ms_per_min: 0, leak_suspected: false },
      }),
      row({ run_id: 23 }),
    ]);

    expect(container!.querySelectorAll('[data-testid="soak-leak-banner"]').length).toBe(0);
  });
});
