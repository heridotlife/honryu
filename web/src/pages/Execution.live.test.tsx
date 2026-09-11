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
) {
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      calls.push(url);
      if (url.endsWith('/api/me')) {
        return json({ subject: 'demo:a', name: 'a', email: '', global_roles: [], tenants: {}, permissions: { '*': ['*'] }, demo: true });
      }
      if (url.endsWith('/api/executions/5')) {
        return json({
          id: 5, name: 'demo', project_id: 1, csv_split: false,
          created_time: '2026-09-05T10:00:00Z', load_profile: [], data: [],
          engine: 'jmeter', kind: 'normal', ...infoOver,
        });
      }
      if (url.endsWith('/api/executions/5/status')) {
        return json({ ...statusFixture(phase), ...statusOver });
      }
      if (url.endsWith('/api/executions/5/reports')) {
        return json([]);
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
      <MemoryRouter initialEntries={['/executions/5']}>
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
    expect(section?.querySelector('h3')?.textContent).toBe('Live');

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
    expect(pills.map((p) => p.getAttribute('data-testid'))).toEqual([
      'trend-pill-12',
      'trend-pill-11',
      'trend-pill-10',
    ]);
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
    expect(calls.some((u) => u.endsWith('/api/executions/5/error-signatures'))).toBe(true);

    const code = container!.querySelector('[data-testid="signature-group-by-code"]') as HTMLButtonElement;
    await act(async () => {
      code.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    // The non-default axis rides the query string (trends.ts contract).
    expect(calls.some((u) => u.endsWith('/api/executions/5/error-signatures?by=code'))).toBe(true);
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
// single message line when it did not. The fetch stub is swapped after
// mount because runAction fetches at click time.
describe('Execution action-error details (mounted)', () => {
  const clickTrigger = async () => {
    const btn = Array.from(container!.querySelectorAll('button')).find((b) => b.textContent === 'Trigger');
    expect(btn).toBeDefined();
    await act(async () => {
      btn!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await act(async () => {});
  };

  it('renders the hint code line and the quota numbers under the message', async () => {
    await renderExecution('deployed');
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        if (String(input).endsWith('/api/executions/5/trigger')) {
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
            429,
          );
        }
        return json({ message: 'no stub' }, 500);
      }),
    );

    await clickTrigger();

    expect(container!.querySelector('[role="alert"]')?.textContent).toContain(
      'reservation would exceed tenant quota',
    );
    const details = container!.querySelector('[data-testid="action-error-details"]');
    expect(details?.querySelector('code')?.textContent).toBe('PUT /api/tenants/{tenant_id}/quota ceiling=1');
    expect(details?.textContent).toContain('used 0 / ceiling 1 — requested 2');
  });

  it('renders a 409 with details (engines finished) as message plus hint line', async () => {
    await renderExecution('deployed');
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        if (String(input).endsWith('/api/executions/5/trigger')) {
          return json(
            {
              message: 'run: engines already finished, redeploy before triggering: 3 orphaned shard completion(s)',
              details: {
                orphaned_completions: 3,
                hint: 'purge the execution and redeploy before triggering',
              },
            },
            409,
          );
        }
        return json({ message: 'no stub' }, 500);
      }),
    );

    await clickTrigger();

    expect(container!.querySelector('[role="alert"]')?.textContent).toContain('engines already finished');
    expect(container!.querySelector('[data-testid="action-error-details"] code')?.textContent).toBe(
      'purge the execution and redeploy before triggering',
    );
  });

  it('message-only failures keep the single alert line (no details node)', async () => {
    await renderExecution('deployed');
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        if (String(input).endsWith('/api/executions/5/trigger')) {
          return json({ message: 'execution not found' }, 404);
        }
        return json({ message: 'no stub' }, 500);
      }),
    );

    await clickTrigger();

    expect(container!.querySelector('[role="alert"]')?.textContent).toContain('execution not found');
    expect(container!.querySelector('[data-testid="action-error-details"]')).toBeNull();
  });
});
