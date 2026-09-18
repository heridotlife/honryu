import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import CardTable, { cardListTestId, type CardTableColumn } from './CardTable';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

// Phase 79: the shared table-or-cards switch. jsdom computes no matchMedia,
// so every mount here drives the branch through the same query-aware stub
// the suite's DashboardLayout stubs approximate: min-width queries answer
// `wide`, everything else (the theme's prefers-color-scheme) answers false.
// The stub also exposes a real listener API so the live-flip pin can fire a
// change event through the hook.

interface Row {
  id: number;
  name: string;
  status: string;
}

const rows: Row[] = [
  { id: 1, name: 'checkout-baseline', status: 'passed' },
  { id: 2, name: 'search-smoke', status: 'failed' },
];

const columns: CardTableColumn<Row>[] = [
  {
    key: 'name',
    header: 'Name',
    primary: true,
    thClassName: 'px-4 py-3',
    tdClassName: 'px-4 py-3',
    render: (r) => r.name,
  },
  {
    key: 'status',
    header: 'Status',
    cellTestId: (r) => `status-${r.id}`,
    thClassName: 'px-4 py-3',
    tdClassName: 'px-4 py-3',
    render: (r) => r.status,
  },
];

function matchMediaStubs(wide: boolean): Set<(e: MediaQueryListEvent) => void> {
  const listeners = new Set<(e: MediaQueryListEvent) => void>();
  vi.stubGlobal(
    'matchMedia',
    vi.fn((query: string) => ({
      matches: query.includes('min-width') ? wide : false,
      addEventListener: (_query: string, cb: (e: MediaQueryListEvent) => void) => {
        listeners.add(cb);
      },
      removeEventListener: (_query: string, cb: (e: MediaQueryListEvent) => void) => {
        listeners.delete(cb);
      },
    })),
  );
  return listeners;
}

let container: HTMLDivElement | null = null;
let root: Root | null = null;

async function renderCardTable(props?: Partial<Parameters<typeof CardTable<Row>>[0]>) {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <CardTable<Row>
        tableTestId="list-table"
        columns={columns}
        rows={rows}
        rowKey={(r) => String(r.id)}
        rowTestId={(r) => `row-${r.id}`}
        {...props}
      />,
    );
  });
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

describe('CardTable (phase 79)', () => {
  it('renders the table branch at md with the page markup and testids', async () => {
    matchMediaStubs(true);
    await renderCardTable();

    // The sm+ branch is a real <table> carrying the page's testid; no card
    // list exists beside it (one branch, never both).
    const table = container!.querySelector('table[data-testid="list-table"]');
    expect(table).not.toBeNull();
    expect(container!.querySelector(`[data-testid="${cardListTestId('list-table')}"]`)).toBeNull();

    // Header per column, in definition order.
    const headers = Array.from(table!.querySelectorAll('th')).map((th) => th.textContent);
    expect(headers).toEqual(['Name', 'Status']);

    // Row testid on the <tr>, cell testid on the <td>.
    const tr = container!.querySelector('[data-testid="row-1"]');
    expect(tr?.tagName).toBe('TR');
    expect(tr?.querySelector('[data-testid="status-1"]')?.tagName).toBe('TD');
    expect(container!.textContent).toContain('checkout-baseline');
    expect(container!.textContent).toContain('search-smoke');
  });

  it('renders the card branch below sm: primary column as title, the rest label:value pairs', async () => {
    matchMediaStubs(false);
    await renderCardTable();

    // The narrow branch replaces the table outright -- never horizontal
    // scroll as the only mechanism on a phone.
    expect(container!.querySelector('table[data-testid="list-table"]')).toBeNull();
    const list = container!.querySelector(`ul[data-testid="${cardListTestId('list-table')}"]`);
    expect(list).not.toBeNull();

    // Card roots carry the row testids; one card per row.
    const cards = list!.querySelectorAll('li');
    expect(cards.length).toBe(2);

    // The primary column is the card title...
    const card1 = container!.querySelector('[data-testid="row-1"]')!;
    expect(card1.tagName).toBe('LI');
    expect(card1.textContent).toContain('checkout-baseline');

    // ...and never repeats as a label:value pair -- the pairs are the other
    // columns, labelled with the same header strings the table used.
    const pairs = Array.from(card1.querySelectorAll('dt')).map((dt) => dt.textContent);
    expect(pairs).toEqual(['Status']);

    // Cell testids ride the pair's value slot below sm.
    const value = card1.querySelector('[data-testid="status-1"]');
    expect(value).not.toBeNull();
    expect(value?.tagName).toBe('DD');
    expect(value?.textContent).toBe('passed');
  });

  it('keeps data-testid parity between the two branches', async () => {
    matchMediaStubs(true);
    await renderCardTable();
    const tableTestids = Array.from(container!.querySelectorAll('[data-testid]'))
      .map((el) => el.getAttribute('data-testid'))
      .filter((id) => id !== 'list-table')
      .sort();

    await act(async () => {
      root!.unmount();
    });
    container?.remove();

    matchMediaStubs(false);
    await renderCardTable();
    const cardTestids = Array.from(container!.querySelectorAll('[data-testid]'))
      .map((el) => el.getAttribute('data-testid'))
      .filter((id) => id !== cardListTestId('list-table'))
      .sort();

    // Same rows, same cells, same ids -- only the wrapper tags differ.
    expect(cardTestids).toEqual(tableTestids);
  });

  it('flips branches on a live matchMedia change without a remount', async () => {
    const listeners = matchMediaStubs(false);
    await renderCardTable();
    expect(container!.querySelector('table[data-testid="list-table"]')).toBeNull();

    // The viewport crosses sm: the hook re-reads the query and the table
    // branch takes over (a rotation or window drag, not a reload).
    await act(async () => {
      for (const cb of listeners) {
        cb({ matches: true } as MediaQueryListEvent);
      }
    });
    expect(container!.querySelector('table[data-testid="list-table"]')).not.toBeNull();
    expect(container!.querySelector(`[data-testid="${cardListTestId('list-table')}"]`)).toBeNull();
  });

  it('defaults to the table branch when matchMedia is unavailable (jsdom, no stub)', async () => {
    // No stub: window.matchMedia does not exist in jsdom. The desktop
    // contract (every existing table-mode test, the e2e harness) must hold.
    await renderCardTable();
    expect(container!.querySelector('table[data-testid="list-table"]')).not.toBeNull();
  });

  it('fires row clicks in both branches', async () => {
    const clicked: Row[] = [];
    const onClick = (r: Row): void => {
      clicked.push(r);
    };

    matchMediaStubs(true);
    await renderCardTable({ onRowClick: onClick });
    await act(async () => {
      container!.querySelector('[data-testid="row-2"]')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(clicked.map((r) => r.id)).toEqual([2]);

    await act(async () => {
      root!.unmount();
    });
    container?.remove();

    matchMediaStubs(false);
    await renderCardTable({ onRowClick: onClick });
    await act(async () => {
      container!.querySelector('[data-testid="row-1"]')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(clicked.map((r) => r.id)).toEqual([2, 1]);
  });

  it('renders row actions as a trailing column at md and a card footer below sm', async () => {
    const actions = {
      header: '',
      thClassName: 'px-4 py-3',
      tdClassName: 'px-4 py-3 text-right',
      render: (r: Row) => <button type="button" data-testid={`act-${r.id}`}>Revoke</button>,
    };

    matchMediaStubs(true);
    await renderCardTable({ actions });
    const tr = container!.querySelector('[data-testid="row-1"]')!;
    expect(tr.querySelectorAll('td').length).toBe(3); // two columns + actions
    expect(tr.querySelector('[data-testid="act-1"]')?.textContent).toBe('Revoke');

    await act(async () => {
      root!.unmount();
    });
    container?.remove();

    matchMediaStubs(false);
    await renderCardTable({ actions });
    const card = container!.querySelector('[data-testid="row-1"]')!;
    expect(card.textContent).toContain('Revoke'); // inline at the card bottom
    expect(card.querySelector('dt')?.textContent).not.toBe(''); // actions never become a pair
  });

  it('keeps an empty roster visible in both branches via emptyMessage', async () => {
    matchMediaStubs(true);
    await renderCardTable({ rows: [], emptyMessage: 'No role grants in this tenant yet.', emptyTdClassName: 'px-3 py-2' });
    const cell = container!.querySelector('table[data-testid="list-table"] td[colspan="2"]');
    expect(cell?.textContent).toBe('No role grants in this tenant yet.');

    await act(async () => {
      root!.unmount();
    });
    container?.remove();

    matchMediaStubs(false);
    await renderCardTable({ rows: [], emptyMessage: 'No role grants in this tenant yet.', emptyTdClassName: 'px-3 py-2' });
    const empty = container!.querySelector(`[data-testid="${cardListTestId('list-table')}-empty"]`);
    expect(empty?.textContent).toBe('No role grants in this tenant yet.');
  });
});

// Phase 86: the delta-table conversions needed richer hooks than plain
// testids -- RunCompare's suite pins rows by data-metric, cells by
// data-delta, and the regression chip lives inside the candidate's <th>.
// The extensions keep the same mode-for-mode discipline: row/cell
// attributes ride both branches, per-row td classes stay table-branch
// styling, and header extras are th-only (a column header has no card
// counterpart -- the cells it summarizes render below sm too).
describe('CardTable (phase 86 extensions)', () => {
  const attrColumns: CardTableColumn<Row>[] = [
    {
      key: 'name',
      header: 'Name',
      primary: true,
      thClassName: 'px-4 py-3',
      tdClassName: 'px-4 py-3',
      render: (r) => r.name,
    },
    {
      key: 'status',
      header: 'Status',
      cellTestId: (r) => `status-${r.id}`,
      cellAttributes: (r) => ({ 'data-run': String(r.id) }),
      thClassName: 'px-4 py-3',
      // Per-row tone: the failed row colors, the passed one does not.
      tdClassName: (r) => `px-4 py-3 ${r.status === 'failed' ? 'text-red-600' : ''}`,
      render: (r) => r.status,
    },
  ];

  it('spreads row/cell attributes on the tr/td and again on the card root/value slot', async () => {
    matchMediaStubs(true);
    await renderCardTable({ columns: attrColumns, rowAttributes: (r) => ({ 'data-row': r.name }) });
    const tr = container!.querySelector('[data-testid="row-1"]')!;
    expect(tr.tagName).toBe('TR');
    expect(tr.getAttribute('data-row')).toBe('checkout-baseline');
    const td = tr.querySelector('[data-testid="status-1"]')!;
    expect(td.tagName).toBe('TD');
    expect(td.getAttribute('data-run')).toBe('1');
    // The per-row td class resolves per row: only the failed row colors.
    expect(td.className).not.toContain('text-red-600');
    expect(container!.querySelector('[data-testid="row-2"] [data-testid="status-2"]')!.className).toContain('text-red-600');

    await act(async () => {
      root!.unmount();
    });
    container?.remove();

    matchMediaStubs(false);
    await renderCardTable({ columns: attrColumns, rowAttributes: (r) => ({ 'data-row': r.name }) });
    const li = container!.querySelector('[data-testid="row-1"]')!;
    expect(li.tagName).toBe('LI');
    expect(li.getAttribute('data-row')).toBe('checkout-baseline');
    const dd = li.querySelector('[data-testid="status-1"]')!;
    expect(dd.tagName).toBe('DD');
    expect(dd.getAttribute('data-run')).toBe('1');
  });

  it('renders thAttributes and headerExtra in the th only -- never in the card branch', async () => {
    const chipColumns: CardTableColumn<Row>[] = [
      { key: 'name', header: 'Name', primary: true, thClassName: 'px-4 py-3', tdClassName: 'px-4 py-3', render: (r) => r.name },
      {
        key: 'status',
        header: 'Status',
        thAttributes: { 'data-owner': 'status' },
        headerExtra: (
          <span data-testid="col-chip" className="ml-2">
            verdict
          </span>
        ),
        thClassName: 'px-4 py-3',
        tdClassName: 'px-4 py-3',
        render: (r) => r.status,
      },
    ];

    matchMediaStubs(true);
    await renderCardTable({ columns: chipColumns });
    const th = container!.querySelector('th[data-owner="status"]')!;
    expect(th).not.toBeNull();
    // The extra rides INSIDE the th, right after the header text.
    expect(th.querySelector('[data-testid="col-chip"]')?.textContent).toBe('verdict');
    expect(th.textContent).toBe('Statusverdict');

    await act(async () => {
      root!.unmount();
    });
    container?.remove();

    matchMediaStubs(false);
    await renderCardTable({ columns: chipColumns });
    // Below sm the header extra is gone outright and the pair label stays
    // the plain header string.
    expect(container!.querySelector('[data-testid="col-chip"]')).toBeNull();
    const card = container!.querySelector('[data-testid="row-1"]')!;
    expect(Array.from(card.querySelectorAll('dt')).map((dt) => dt.textContent)).toEqual(['Status']);
  });
});
