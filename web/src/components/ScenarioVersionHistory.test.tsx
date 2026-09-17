// The scenario version-history card (phase 80), mounted with the house
// createRoot + act pattern and fetch stubbed per-URL: collapsed by default,
// newest-first list rows, lazily fetched snapshot on expand, the
// named-version confirm gating the restore, and the post-restore refetch
// (the card's list AND the parent's scenario callback).
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ScenarioVersionHistory from './ScenarioVersionHistory';
import { SessionProvider } from '../hooks/useSession';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

interface Call {
  method: string;
  url: string;
  body: string;
}

let container: HTMLDivElement | null = null;
let root: Root | null = null;
let calls: Call[] = [];
let restoreCount = 0;

// The seeded history: three versions, newest first on the wire. v2's actor
// is null (the honest unknown); the others name their principals.
let versionsFixture: unknown[] = [
  { id: 3, scenario_id: 9, version: 3, created_time: '2026-09-21T03:00:00Z', created_by: 'carol' },
  { id: 2, scenario_id: 9, version: 2, created_time: '2026-09-20T02:00:00Z', created_by: null },
  { id: 1, scenario_id: 9, version: 1, created_time: '2026-09-19T01:00:00Z', created_by: 'alice' },
];

// What GET .../versions/2 answers (a snapshot with files and a fragment).
let snapshotFixture: unknown = {
  id: 9,
  scenario_id: 9,
  version: 2,
  created_time: '2026-09-20T02:00:00Z',
  created_by: null,
  snapshot: {
    id: 9,
    name: 'checkout-load',
    project_id: 2,
    kind: 'portable',
    engine: '',
    tenant_id: null,
    created_by: 'op',
    updated_by: '',
    created_time: '2026-09-19T01:00:00Z',
    is_template: false,
    template_name: '',
    test_file: '',
    data: ['users.csv'],
    requests: 'requests:\n  - url: /checkout\n',
  },
};

// What the session picker says: with scenario:update the Restore buttons
// render; without, they hide.
let permissionsFixture: Record<string, string[]> = { scenario: ['read', 'update'] };

function stubFetch() {
  calls = [];
  restoreCount = 0;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const method = (init?.method as string) ?? 'GET';
      const url = String(input);
      calls.push({ method, url, body: String(init?.body ?? '') });
      if (url.endsWith('/api/me')) {
        return json({
          profile: 'op',
          permissions: permissionsFixture,
        });
      }
      if (url.endsWith('/api/scenarios/9/versions') && method === 'GET') {
        return json(versionsFixture);
      }
      if (url.endsWith('/api/scenarios/9/versions/2') && method === 'GET') {
        return json(snapshotFixture);
      }
      if (url.endsWith('/api/scenarios/9/versions/1/restore') && method === 'POST') {
        restoreCount++;
        return json({ message: 'restored', restored_from: 1, version: 4 });
      }
      return json({ message: `no stub for ${url}` }, 500);
    }),
  );
}

async function renderHistory(onRestored?: () => void): Promise<void> {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <SessionProvider>
        <ScenarioVersionHistory scenarioId={9} onRestored={onRestored} />
      </SessionProvider>,
    );
  });
  await act(async () => {});
}

function click(testid: string): void {
  const el = container!.querySelector(`[data-testid="${testid}"]`) as HTMLButtonElement | null;
  if (el === null) {
    throw new Error(`no element ${testid}`);
  }
  act(() => {
    el.click();
  });
}

function q(testid: string): Element | null {
  return container!.querySelector(`[data-testid="${testid}"]`);
}

async function openHistory(): Promise<void> {
  click('version-history-toggle');
  await act(async () => {});
}

beforeEach(() => {
  permissionsFixture = { scenario: ['read', 'update'] };
  versionsFixture = [
    { id: 3, scenario_id: 9, version: 3, created_time: '2026-09-21T03:00:00Z', created_by: 'carol' },
    { id: 2, scenario_id: 9, version: 2, created_time: '2026-09-20T02:00:00Z', created_by: null },
    { id: 1, scenario_id: 9, version: 1, created_time: '2026-09-19T01:00:00Z', created_by: 'alice' },
  ];
});

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

describe('ScenarioVersionHistory', () => {
  it('is collapsed by default and opens on the toggle', async () => {
    stubFetch();
    await renderHistory();
    expect(q('version-history')).toBeNull();
    const toggle = q('version-history-toggle') as HTMLButtonElement;
    expect(toggle.getAttribute('aria-expanded')).toBe('false');

    await openHistory();
    expect(q('version-history')).not.toBeNull();
    expect((q('version-history-toggle') as HTMLButtonElement).getAttribute('aria-expanded')).toBe('true');
  });

  it('lists the versions newest first with stamp and actor', async () => {
    stubFetch();
    await renderHistory();
    await openHistory();

    const rows = Array.from(container!.querySelectorAll('[data-testid^="version-row-"]'));
    const ids = rows.map((r) => r.getAttribute('data-testid'));
    expect(ids).toEqual(['version-row-3', 'version-row-2', 'version-row-1']);
    // v2's actor is null on the wire: "unknown", never a fabricated name.
    expect(q('version-row-2')!.textContent).toContain('unknown');
    expect(q('version-row-1')!.textContent).toContain('alice');
  });

  it('expands a row into that version\'s snapshot fields', async () => {
    stubFetch();
    await renderHistory();
    await openHistory();

    expect(q('version-snapshot-2')).toBeNull();
    click('version-expand-2');
    await act(async () => {});

    expect(q('version-snapshot-2')).not.toBeNull();
    const fields = q('version-snapshot-2')!;
    expect(fields.textContent).toContain('checkout-load');
    expect(fields.textContent).toContain('portable');
    expect(fields.textContent).toContain('users.csv');
    expect(fields.textContent).toContain('stored fragment');
    // The snapshot was fetched lazily, once, from the versioned endpoint.
    const snapCalls = calls.filter((c) => c.url.endsWith('/api/scenarios/9/versions/2'));
    expect(snapCalls).toHaveLength(1);

    // Collapse again hides it.
    click('version-expand-2');
    expect(q('version-snapshot-2')).toBeNull();
  });

  it('gates restore behind a confirm naming the version, then refetches', async () => {
    stubFetch();
    const onRestored = vi.fn();
    await renderHistory(onRestored);
    await openHistory();

    // Choosing Restore shows the confirm and fires nothing yet.
    click('version-restore-1');
    expect(q('version-confirm')).not.toBeNull();
    expect(q('version-confirm')!.textContent).toContain('version 1');
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(0);
    expect(onRestored).not.toHaveBeenCalled();

    // Cancel: still nothing on the wire.
    click('version-confirm-cancel');
    expect(q('version-confirm')).toBeNull();
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(0);

    // Confirming posts the restore; the card refetches its list and the
    // parent is told to refetch the scenario; the feedback names both
    // versions.
    click('version-restore-1');
    click('version-confirm-restore');
    await act(async () => {});
    expect(restoreCount).toBe(1);
    expect(onRestored).toHaveBeenCalledTimes(1);
    const listCalls = calls.filter((c) => c.url.endsWith('/api/scenarios/9/versions') && c.method === 'GET');
    expect(listCalls.length).toBeGreaterThanOrEqual(2);
    expect(q('version-restored')!.textContent).toContain('Restored to version 1');
    expect(q('version-restored')!.textContent).toContain('version 4');
  });

  it('hides restore without the scenario:update grant', async () => {
    stubFetch();
    permissionsFixture = { scenario: ['read'] };
    await renderHistory();
    await openHistory();

    expect(q('version-row-1')).not.toBeNull();
    expect(q('version-restore-1')).toBeNull();
  });

  it('surfaces a restore failure without losing the list', async () => {
    stubFetch();
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const method = (init?.method as string) ?? 'GET';
        const url = String(input);
        calls.push({ method, url, body: String(init?.body ?? '') });
        if (url.endsWith('/api/me')) {
          return json({ profile: 'op', permissions: permissionsFixture });
        }
        if (url.endsWith('/api/scenarios/9/versions') && method === 'GET') {
          return json(versionsFixture);
        }
        if (url.endsWith('/restore') && method === 'POST') {
          return json({ message: 'version not found' }, 404);
        }
        return json({ message: `no stub for ${url}` }, 500);
      }),
    );
    await renderHistory();
    await openHistory();
    click('version-restore-1');
    click('version-confirm-restore');
    await act(async () => {});
    expect(q('version-action-error')!.textContent).toContain('version not found');
    expect(q('version-row-1')).not.toBeNull();
  });
});
