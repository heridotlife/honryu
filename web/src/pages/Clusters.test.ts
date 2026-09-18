import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import Clusters, { clusterCapacity, engineImageRows, engineImages, formatCalibratedShortDate, formatClusterTime, originDescription } from './Clusters';
import type { Cluster } from '../api/clusters';
import type { CapacityProfileSummary } from '../api/calibration';

/** A registered cluster row exactly as GET /api/clusters serves it. */
const registered: Cluster = {
  name: 'honryu',
  api_url: 'https://kubernetes.default.svc:443',
  ingest_url: 'https://honryu.pve.heri.life/api/ingest',
  sidecar_image: 'registry.pve.heri.life/honryu/honryu-sidecar:phase16',
  namespace: 'honryu',
  secret_ref: 'cluster-honryu-explicit-creds',
  origin: 'operator',
  created_by: 'alice',
  created_time: '2026-09-05T10:29:02.848672Z',
};

describe('originDescription', () => {
  // Both wire values need a credential-ownership explanation -- the page
  // renders it as the row's title attribute.
  it('explains both origins', () => {
    expect(originDescription('operator')).toBe('home-cluster Secret managed by the platform operator');
    expect(originDescription('byoc')).toBe('customer-supplied kubeconfig (bring your own cluster)');
  });
});

describe('clusterCapacity', () => {
  // Phase 25 flipped the phase-22 honesty pin: capacity numbers now ride
  // the wire (engines_used/engines_ceiling, both required), and their
  // absence -- no quota ledger wired, or a half-wired read -- still maps to
  // the meter's honest "no capacity reported" state.
  it('maps wire capacity numbers onto the meter props', () => {
    expect(clusterCapacity({ ...registered, engines_used: 2, engines_ceiling: 12 })).toEqual({
      used: 2,
      ceiling: 12,
    });
  });
  it('reports no capacity numbers when the fields are absent', () => {
    expect(clusterCapacity(registered)).toEqual({});
  });
  it('reports no capacity numbers when only one field is present (half-wired read)', () => {
    expect(clusterCapacity({ ...registered, engines_ceiling: 12 })).toEqual({});
    expect(clusterCapacity({ ...registered, engines_used: 2 })).toEqual({});
  });
});

describe('formatClusterTime', () => {
  it('formats a valid ISO timestamp via the locale', () => {
    const iso = '2026-08-17T00:00:00Z';
    expect(formatClusterTime(iso)).toBe(new Date(iso).toLocaleString());
  });

  it('passes an unparseable value through unchanged rather than rendering "Invalid Date"', () => {
    expect(formatClusterTime('not-a-date')).toBe('not-a-date');
  });
});

// Phase 55: the fleet summary card renders exactly what cluster rows
// carry -- distinct sidecar images as plain code text (the engine images
// config has no API surface), and no capacity-profiles row (no list
// endpoint exists; only per-scenario reads).
describe('engineImages', () => {
  it('collects distinct sidecar images in first-seen order, dropping blanks', () => {
    const list: Cluster[] = [
      registered,
      { ...registered, name: 'byoc-1', origin: 'byoc', sidecar_image: 'registry.example.org/other:1' },
      { ...registered, name: 'dup', sidecar_image: 'registry.example.org/other:1' },
      { ...registered, name: 'blank', sidecar_image: '' },
    ];
    expect(engineImages(list)).toEqual([
      'registry.pve.heri.life/honryu/honryu-sidecar:phase16',
      'registry.example.org/other:1',
    ]);
  });

  it('is empty for an empty registry', () => {
    expect(engineImages([])).toEqual([]);
  });
});

// Phase 61: the fleet summary's engine-image lines are grouped per unique
// image with the clusters that run it -- a fleet on mixed engine versions
// reads which cluster is on which image without scanning the table.
describe('engineImageRows', () => {
  it('groups cluster names under each unique image, blanks dropped, order kept', () => {
    const list: Cluster[] = [
      registered,
      { ...registered, name: 'byoc-1', origin: 'byoc', sidecar_image: 'registry.example.org/other:1' },
      { ...registered, name: 'dup', sidecar_image: 'registry.example.org/other:1' },
      { ...registered, name: 'blank', sidecar_image: '' },
    ];
    expect(engineImageRows(list)).toEqual([
      { image: 'registry.pve.heri.life/honryu/honryu-sidecar:phase16', clusters: ['honryu'] },
      { image: 'registry.example.org/other:1', clusters: ['byoc-1', 'dup'] },
    ]);
  });

  it('is empty when no cluster carries an engine image', () => {
    expect(engineImageRows([{ ...registered, sidecar_image: '' }])).toEqual([]);
  });
});

describe('Clusters fleet summary (phase 55, mounted)', () => {
  it('renders a labelled region listing the distinct engine images', async () => {
    await mountClusters([
      registered,
      { ...registered, name: 'byoc-1', origin: 'byoc', sidecar_image: 'registry.example.org/other:1' },
      { ...registered, name: 'dup', sidecar_image: 'registry.example.org/other:1' },
    ]);

    const region = container!.querySelector('[role="region"][aria-label="Fleet summary"]');
    expect(region).not.toBeNull();
    const codes = Array.from(region!.querySelectorAll('code')).map((el) => el.textContent);
    expect(codes).toEqual([
      'registry.pve.heri.life/honryu/honryu-sidecar:phase16',
      'registry.example.org/other:1',
    ]);
  });

  it('renders an honest line when no clusters are registered', async () => {
    await mountClusters([]);

    const region = container!.querySelector('[role="region"][aria-label="Fleet summary"]');
    expect(region).not.toBeNull();
    expect(region!.querySelectorAll('code')).toHaveLength(0);
    expect(region!.textContent).toContain('No registered clusters');
  });

  // Phase 61: the engine-image row is one line per unique image, attributed
  // across the fleet; it vanishes entirely when no engine config exists.
  it('renders one line per unique image with the clusters that run it (phase 61)', async () => {
    await mountClusters([
      registered,
      { ...registered, name: 'byoc-1', origin: 'byoc', sidecar_image: 'registry.example.org/other:1' },
      { ...registered, name: 'dup', sidecar_image: 'registry.example.org/other:1' },
    ]);

    const row = container!.querySelector('[data-testid="fleet-engine-images"]');
    expect(row).not.toBeNull();
    // Two unique images across a three-cluster fleet: two lines.
    const lines = Array.from(row!.querySelectorAll('p'));
    expect(lines).toHaveLength(2);
    expect(lines[0]!.textContent).toContain('registry.pve.heri.life/honryu/honryu-sidecar:phase16');
    expect(lines[0]!.textContent).toContain('1 cluster: honryu');
    expect(lines[1]!.querySelector('code')!.textContent).toBe('registry.example.org/other:1');
    expect(lines[1]!.textContent).toContain('2 clusters: byoc-1, dup');
  });

  it('renders no engine-image row when no cluster carries an engine config (phase 61)', async () => {
    await mountClusters([
      { ...registered, sidecar_image: '' },
      { ...registered, name: 'byoc-1', origin: 'byoc', sidecar_image: '' },
    ]);

    const region = container!.querySelector('[role="region"][aria-label="Fleet summary"]');
    expect(region).not.toBeNull();
    expect(container!.querySelector('[data-testid="fleet-engine-images"]')).toBeNull();
    expect(region!.querySelectorAll('code')).toHaveLength(0);
    expect(region!.textContent).toContain('No registered cluster carries an engine image');
  });
});

describe('formatCalibratedShortDate', () => {
  it('renders a calibrated timestamp as a short date, never raw ISO', () => {
    const iso = '2026-09-12T08:30:00Z';
    expect(formatCalibratedShortDate(iso)).toBe(new Date(iso).toLocaleDateString());
  });

  it('passes an unparseable value through unchanged rather than rendering "Invalid Date"', () => {
    expect(formatCalibratedShortDate('not-a-date')).toBe('not-a-date');
  });
});

// Phase 56: the fleet summary card gets its capacity matrix back now that
// GET /api/capacity-profiles exists -- rows as served (backend-ordered),
// short dates, saturated_by as plain text, and an honest empty state.
describe('Clusters capacity matrix (phase 56, mounted)', () => {
  const matrixRows: CapacityProfileSummary[] = [
    {
      scenario_id: 6,
      engine: 'jmeter',
      cpu: '2',
      memory: '2Gi',
      per_pod_qps: 4543.9,
      saturated_by: 'engine',
      calibrated_at: '2026-09-12T08:30:00Z',
    },
    {
      scenario_id: 6,
      engine: 'jmeter',
      cpu: '250m',
      memory: '256Mi',
      per_pod_qps: 8.24,
      saturated_by: 'target',
      calibrated_at: '2026-08-01T10:00:00Z',
    },
  ];

  it('renders the matrix rows in server order with visible headers and short dates', async () => {
    await mountClusters([registered], matrixRows);

    const region = container!.querySelector('[role="region"][aria-label="Fleet summary"]');
    expect(region).not.toBeNull();
    const headers = Array.from(region!.querySelectorAll('th')).map((th) => th.textContent);
    expect(headers).toEqual(['Pod size', 'Per-pod QPS', 'Saturated by', 'Calibrated']);
    const rows = Array.from(region!.querySelectorAll('tbody tr')).map((tr) =>
      Array.from(tr.querySelectorAll('td')).map((td) => td.textContent)
    );
    expect(rows).toEqual([
      ['2 / 2Gi', '4543.9', 'engine', new Date('2026-09-12T08:30:00Z').toLocaleDateString()],
      ['250m / 256Mi', '8.24', 'target', new Date('2026-08-01T10:00:00Z').toLocaleDateString()],
    ]);
    // House rule: the raw ISO timestamps never reach the screen.
    expect(region!.textContent).not.toContain('2026-09-12T08:30:00Z');
    expect(region!.textContent).not.toContain('2026-08-01T10:00:00Z');
  });

  it('renders saturated_by as plain text, not a code or badge', async () => {
    await mountClusters([registered], matrixRows);

    const cells = Array.from(container!.querySelectorAll('[aria-label="Fleet summary"] tbody td:nth-child(3)'));
    expect(cells.map((td) => `${td.children.length}:${td.textContent}`)).toEqual(['0:engine', '0:target']);
  });

  it('renders an honest empty state when nothing has been calibrated', async () => {
    await mountClusters([registered], []);

    const region = container!.querySelector('[role="region"][aria-label="Fleet summary"]');
    expect(region).not.toBeNull();
    expect(region!.querySelectorAll('table')).toHaveLength(0);
    expect(region!.textContent).toContain('No calibrations yet');
  });
});

// Phase 51 landmark gate (mounted; createElement so this stays a .ts file):
// the page's outermost element is a labelled region a screen reader can jump to.
(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | null = null;
let root: Root | null = null;

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

describe('Clusters landmarks (phase 51)', () => {
  it('renders the page as a labelled region', async () => {
    await mountClusters([]);

    const region = container!.querySelector('[role="region"]');
    expect(region).not.toBeNull();
    expect(region!.getAttribute('aria-label')).toBe('Clusters panel');
  });
});

/** Mount the page over stubbed /api/clusters and /api/capacity-profiles
 * replies, routed by URL (the landmark test's createElement + act pattern;
 * module-level container/root so afterEach cleans up). Phase 55 additions
 * pin what a browser actually sees; phase 56 adds the capacity matrix. */
async function mountClusters(payload: Cluster[], profiles: CapacityProfileSummary[] = []) {
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const body = String(input).includes('/api/capacity-profiles') ? profiles : payload;
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
    })
  );
  root = createRoot(container);
  await act(async () => {
    root!.render(createElement(Clusters));
  });
  await act(async () => {}); // flush the listClusters + listCapacityProfiles fetches
}

// Phase 55: clusterCapacity's unit pins cover the mapping; these mounted
// tests pin the page-level light-up the stale phase-22 comments claimed
// could never happen -- a row carrying both wire fields renders a real
// meter, a row without them renders the honest line.
describe('Clusters capacity meters (phase 55, mounted)', () => {
  it('lights the meter up when a row carries both wire fields', async () => {
    await mountClusters([{ ...registered, engines_used: 2, engines_ceiling: 12 }]);

    const meter = container!.querySelector('[role="img"]');
    expect(meter).not.toBeNull();
    expect(meter!.getAttribute('aria-label')).toBe('2 of 12 engines in use');
    expect(container!.textContent).toContain('2 / 12 engines');
    expect(container!.querySelectorAll('rect')).toHaveLength(2); // track + fill
  });

  it('renders the honest no-capacity line when the fields are absent', async () => {
    await mountClusters([registered]);

    expect(container!.querySelector('[role="img"]')).toBeNull();
    expect(container!.querySelectorAll('rect')).toHaveLength(0);
    expect(container!.textContent).toContain('no capacity reported');
  });
});

// Phase 76: the empty registry renders the shared EmptyState -- register
// HINT, not a register button: the SPA deliberately offers no registration
// flow (phase 13: writes stay API operations), so an action here would be
// dead. Message-only is the honest shape.
describe('Clusters empty state (phase 76)', () => {
  it('renders the register hint message-only after an empty registry load', async () => {
    await mountClusters([]);

    const empty = container!.querySelector('[data-testid="clusters-empty"]')!;
    expect(empty).not.toBeNull();
    expect(empty.querySelector('[data-testid="clusters-empty-title"]')?.textContent).toBe('No registered clusters');
    expect(empty.querySelector('[data-testid="clusters-empty-description"]')?.textContent).toContain(
      "default cluster",
    );
    expect(empty.textContent).toContain('POST /api/clusters');
    expect(empty.querySelector('button, a')).toBeNull();
  });
});

describe('Clusters loading vs empty (phase 76)', () => {
  it('renders a skeleton, never the empty state, while the registry is in flight', async () => {
    let release!: (value: Response) => void;
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        if (String(input).includes('/api/capacity-profiles')) {
          return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } });
        }
        return new Promise<Response>((resolve) => {
          release = resolve;
        });
      }),
    );
    root = createRoot(container);
    await act(async () => {
      root!.render(createElement(Clusters));
    });
    await act(async () => {}); // flush the profiles fetch; clusters still pending

    expect(container!.querySelector('[data-testid="clusters-loading"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="clusters-empty"]')).toBeNull();

    await act(async () => {
      release(new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    });
    expect(container!.querySelector('[data-testid="clusters-loading"]')).toBeNull();
    expect(container!.querySelector('[data-testid="clusters-empty"]')).not.toBeNull();
  });
});

// Phase 86: card-mode render pins -- the registry table and the capacity
// matrix render through the shared CardTable, so below sm each becomes a
// card list off the same column definitions. The stub mirrors CardTable's
// own: min-width queries answer the flag, everything else false; default
// mounts (no stub) keep the table branch every pin above asserts against.
function stubCardMode(wide: boolean): void {
  vi.stubGlobal(
    'matchMedia',
    vi.fn((query: string) => ({ matches: query.includes('min-width') ? wide : false })),
  );
}

describe('Clusters card mode (phase 86)', () => {
  it('renders the registry as cards below sm: cluster name the title, origin hint and meter intact', async () => {
    stubCardMode(false);
    await mountClusters([{ ...registered, engines_used: 2, engines_ceiling: 12 }]);

    // The narrow branch replaces the table outright.
    expect(container!.querySelector('table[data-testid="clusters-table"]')).toBeNull();
    const list = container!.querySelector('ul[data-testid="clusters-table-cards"]');
    expect(list).not.toBeNull();
    const cards = Array.from(list!.querySelectorAll('li'));
    expect(cards).toHaveLength(1);

    // The cluster name is the card title and never repeats as a pair; the
    // pairs are the other eight columns, labelled with the same header
    // strings the table used.
    const card = cards[0]!;
    expect(card.textContent).toContain('honryu');
    expect(Array.from(card.querySelectorAll('dt')).map((dt) => dt.textContent)).toEqual([
      'Origin',
      'Capacity',
      'Engine namespace',
      'Sidecar image',
      'Ingest URL',
      'API server',
      'Credential Secret',
      'Registered',
    ]);

    // The row's origin hint rides the card root the way it rode the <tr>.
    expect(card.getAttribute('title')).toBe(originDescription('operator'));
    // The capacity meter rides the Capacity pair's value slot.
    expect(card.querySelector('dd [role="img"]')?.getAttribute('aria-label')).toBe('2 of 12 engines in use');
  });

  it('renders the capacity matrix as cards below sm: pod size the title, figures the pairs', async () => {
    stubCardMode(false);
    await mountClusters(
      [registered],
      [
        {
          scenario_id: 6,
          engine: 'jmeter',
          cpu: '2',
          memory: '2Gi',
          per_pod_qps: 4543.9,
          saturated_by: 'engine',
          calibrated_at: '2026-09-12T08:30:00Z',
        },
      ],
    );

    expect(container!.querySelector('table[data-testid="capacity-matrix"]')).toBeNull();
    const region = container!.querySelector('[role="region"][aria-label="Fleet summary"]')!;
    const list = region.querySelector('ul[data-testid="capacity-matrix-cards"]');
    expect(list).not.toBeNull();
    const card = list!.querySelector('li')!;
    // The pod size is the card title (code text kept); the figures are
    // the pairs, short dates per the house rule.
    expect(card.querySelector('code')!.textContent).toBe('2 / 2Gi');
    expect(Array.from(card.querySelectorAll('dt')).map((dt) => dt.textContent)).toEqual([
      'Per-pod QPS',
      'Saturated by',
      'Calibrated',
    ]);
    expect(card.textContent).toContain('4543.9');
    expect(card.textContent).toContain(new Date('2026-09-12T08:30:00Z').toLocaleDateString());
    expect(card.textContent).not.toContain('2026-09-12T08:30:00Z');
  });
});
