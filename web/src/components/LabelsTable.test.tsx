// LabelsTable's sort/hide/format contract, DashboardLayout.test.tsx's
// mounted style: createRoot + act. No router needed -- the table renders
// plain cells and buttons.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import LabelsTable, { formatErrorRate, formatMs, sortLabels } from './LabelsTable';
import type { LabelSummary } from '../api/reports';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

// Three labels with distinct values on every sortable column; two of them
// miss percentile keys so the em-dash and sink-on-sort rules have cases.
const labels: LabelSummary[] = [
  { label: 'GET /orders', samples: 600, failed: 3, error_rate: 0.0124, latency: { '50': 0.05, '95': 0.2, '99': 0.4 } },
  { label: 'POST /login', samples: 900, failed: 9, error_rate: 0.01, latency: { '50': 0.03, '95': 0.1 } },
  { label: 'GET /health', samples: 300, failed: 0, error_rate: 0, latency: {} },
];

let container: HTMLDivElement | null = null;
let root: Root | null = null;

async function renderTable(rows?: LabelSummary[]) {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(<LabelsTable labels={rows} />);
  });
}

afterEach(() => {
  const r = root;
  if (r !== null && container !== null) {
    act(() => {
      r.unmount();
    });
  }
  container?.remove();
  container = null;
  root = null;
  vi.clearAllMocks();
});

/** The label column of each rendered row, top to bottom. */
function rowOrder(): string[] {
  return Array.from(container?.querySelectorAll('tbody tr td:first-child') ?? []).map((td) => td.textContent ?? '');
}

async function clickSort(key: string) {
  await act(async () => {
    container!.querySelector(`[data-testid="sort-${key}"]`)!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

describe('LabelsTable (mounted)', () => {
  it('renders one row per label with percent error rate and millisecond latency', async () => {
    await renderTable(labels);

    expect(rowOrder()).toEqual(['GET /health', 'GET /orders', 'POST /login']);
    const text = container!.textContent ?? '';
    // error_rate arrives as a fraction on the wire; two decimals as percent.
    expect(text).toContain('1.24%');
    expect(text).toContain('1.00%');
    expect(text).toContain('0.00%');
    // latency arrives in seconds; rendered in ms.
    expect(text).toContain('50.0 ms');
    expect(text).toContain('200.0 ms');
  });

  it('renders an em-dash where a label recorded no sample for that percentile', async () => {
    await renderTable(labels);

    const row = (label: string) =>
      Array.from(container!.querySelectorAll('tbody tr')).find((tr) => tr.textContent?.includes(label));
    const cellsOf = (tr: Element) => Array.from(tr.querySelectorAll('td[data-label^="p"]')).map((td) => td.textContent);

    // POST /login has p50/p95 but no p99.
    expect(cellsOf(row('POST /login')!)).toEqual(['30.0 ms', '100.0 ms', '—']);
    // GET /health recorded nothing at all.
    expect(cellsOf(row('GET /health')!)).toEqual(['—', '—', '—']);
  });

  it('hides the whole card when labels are absent or empty', async () => {
    await renderTable(undefined);
    expect(container!.textContent).toBe('');
    expect(container!.querySelector('[data-testid="labels-card"]')).toBeNull();

    await renderTable([]);
    expect(container!.textContent).toBe('');
    expect(container!.querySelector('[data-testid="labels-card"]')).toBeNull();
  });

  // Phase 30: the per-label status breakdown. Badges render in the wire's
  // own order (dominant first), coloured by class -- 2xx green, 5xx red --
  // and a label with no statuses at all (a run whose engine reports no
  // codes, or a report from before they were counted) shows an em-dash,
  // never empty badges.
  it('renders status badges per label and an em-dash where none were reported', async () => {
    await renderTable([
      {
        label: 'GET /cart',
        samples: 200,
        failed: 20,
        error_rate: 0.1,
        latency: {},
        statuses: [
          { code: '200', count: 180 },
          { code: '500', count: 20 },
        ],
      },
      { label: 'GET /legacy', samples: 10, failed: 0, error_rate: 0, latency: {} },
    ]);

    const ok = container!.querySelector('[data-testid="status-badge-0-200"]')!;
    const err = container!.querySelector('[data-testid="status-badge-0-500"]')!;
    expect(ok.textContent).toBe('200×180');
    expect(err.textContent).toBe('500×20');
    // Wire order (dominant first) is the render order.
    expect(ok.compareDocumentPosition(err) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    // Colour by status class: green for 2xx, red for 5xx.
    expect(ok.className).toContain('emerald');
    expect(err.className).toContain('red');

    const legacyRow = Array.from(container!.querySelectorAll('tbody tr')).find((tr) =>
      tr.textContent?.includes('GET /legacy'),
    )!;
    expect(legacyRow.querySelector('td[data-label="statuses"]')?.textContent).toBe('—');
    expect(container!.querySelectorAll('[data-testid^="status-badge-1"]').length).toBe(0);
  });

  it('sorts by label alphabetically and toggles direction on a second click', async () => {
    await renderTable(labels);

    // Default state: label ascending.
    expect(rowOrder()).toEqual(['GET /health', 'GET /orders', 'POST /login']);
    const header = container!.querySelector('[data-testid="sort-label"]')!;
    expect(header.textContent).toContain('↑');

    await clickSort('label');
    expect(rowOrder()).toEqual(['POST /login', 'GET /orders', 'GET /health']);
    expect(container!.querySelector('[data-testid="sort-label"]')!.textContent).toContain('↓');
  });

  it('sorts every numeric column both directions', async () => {
    await renderTable(labels);
    const samplesDesc = ['POST /login', 'GET /orders', 'GET /health'];
    const samplesAsc = [...samplesDesc].reverse();
    const errorAsc = ['GET /health', 'POST /login', 'GET /orders'];
    const errorDesc = [...errorAsc].reverse();

    // samples: first click asc, second desc.
    await clickSort('samples');
    expect(rowOrder()).toEqual(samplesAsc);
    await clickSort('samples');
    expect(rowOrder()).toEqual(samplesDesc);

    // error rate.
    await clickSort('errorRate');
    expect(rowOrder()).toEqual(errorAsc);
    await clickSort('errorRate');
    expect(rowOrder()).toEqual(errorDesc);

    // p50: measured values sort, the label with no sample sinks in both
    // directions.
    await clickSort('p50');
    expect(rowOrder()).toEqual(['POST /login', 'GET /orders', 'GET /health']);
    await clickSort('p50');
    expect(rowOrder()).toEqual(['GET /orders', 'POST /login', 'GET /health']);

    // p95.
    await clickSort('p95');
    expect(rowOrder()).toEqual(['POST /login', 'GET /orders', 'GET /health']);
    await clickSort('p95');
    expect(rowOrder()).toEqual(['GET /orders', 'POST /login', 'GET /health']);

    // p99: only GET /orders measured it; the em-dashes sink either way.
    await clickSort('p99');
    expect(rowOrder()[0]).toBe('GET /orders');
    await clickSort('p99');
    expect(rowOrder()[0]).toBe('GET /orders');
  });

  it('marks the active column with aria-sort', async () => {
    await renderTable(labels);

    const th = (key: string) =>
      container!.querySelector(`[data-testid="sort-${key}"]`)!.closest('th') as HTMLTableCellElement;
    expect(th('label').getAttribute('aria-sort')).toBe('ascending');
    expect(th('samples').getAttribute('aria-sort')).toBe('none');

    await clickSort('samples');
    expect(th('samples').getAttribute('aria-sort')).toBe('ascending');
    expect(th('label').getAttribute('aria-sort')).toBe('none');

    await clickSort('samples');
    expect(th('samples').getAttribute('aria-sort')).toBe('descending');
  });
});

describe('sortLabels (pure)', () => {
  it('never mutates the input and keeps ties in place', () => {
    const input = [
      { label: 'b', samples: 1, failed: 0, error_rate: 0, latency: {} },
      { label: 'a', samples: 1, failed: 0, error_rate: 0, latency: {} },
    ];
    const sorted = sortLabels(input, 'label', 1);
    expect(sorted.map((l) => l.label)).toEqual(['a', 'b']);
    expect(input.map((l) => l.label)).toEqual(['b', 'a']);
  });
});

describe('formatting helpers', () => {
  it('formats percent and milliseconds', () => {
    expect(formatErrorRate(0.0124)).toBe('1.24%');
    expect(formatErrorRate(0)).toBe('0.00%');
    expect(formatMs(0.05)).toBe('50.0 ms');
    expect(formatMs(1.23456)).toBe('1234.6 ms');
  });
});
