import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { mobileMenuClasses, navItemsFor } from './DashboardLayout';
import DashboardLayout from './DashboardLayout';
import { SessionProvider } from '../hooks/useSession';
import type { SessionInfo } from '../api/session';
import { can as canOn } from '../api/session';

// Regression guard for the mobile layout bug found live on 2026-08-18: the
// drawer was hidden with `invisible` but left in normal flow, so it still
// reserved its full height. Measured against the deployed SPA at a 390x664
// viewport, <nav> came out 370px tall instead of h-16's 64px and <main>
// started at y=370 on all five pages -- ~306px of empty band, about 46% of
// the viewport, which is what read on a phone as "broken with half hovering
// empty". Positioning the drawer out of flow is the fix, so that is what
// these assert.
describe('mobileMenuClasses', () => {
  // The load-bearing one. A closed drawer that is not absolutely positioned
  // reserves layout height no matter how it is visually hidden -- which is
  // exactly how the bug shipped.
  it('positions the drawer out of document flow in both states', () => {
    for (const isOpen of [true, false]) {
      const classes = mobileMenuClasses(isOpen);
      expect(classes).toContain('absolute');
      expect(classes).toContain('top-full');
    }
  });

  it('hides a closed drawer without letting it swallow taps', () => {
    const closed = mobileMenuClasses(false);
    // invisible/opacity-0 hide it; pointer-events-none stops its links
    // intercepting taps aimed at the burger button underneath, since a
    // closed drawer still overlaps the nav bar.
    expect(closed).toContain('invisible');
    expect(closed).toContain('opacity-0');
    expect(closed).toContain('pointer-events-none');
  });

  // -translate-y-2 (the original) moved it only 8px, leaving it peeking out
  // from behind the bar; -translate-y-full clears it by its own height.
  it('slides a closed drawer fully behind the nav bar', () => {
    expect(mobileMenuClasses(false)).toContain('-translate-y-full');
    expect(mobileMenuClasses(false)).not.toContain('-translate-y-2');
  });

  it('reveals an open drawer', () => {
    const open = mobileMenuClasses(true);
    expect(open).toContain('translate-y-0');
    expect(open).toContain('opacity-100');
    expect(open).not.toContain('invisible');
    expect(open).not.toContain('pointer-events-none');
  });

  // The click-outside handler matches on this class to tell "inside the
  // drawer" from "outside it"; losing it silently breaks tap-away-to-close.
  it('carries the marker class the click-outside handler matches on', () => {
    expect(mobileMenuClasses(true)).toContain('mobile-menu');
  });

  // Desktop keeps its own inline nav links, so the drawer must stay hidden
  // there regardless of the open flag.
  it('stays hidden at md and above', () => {
    expect(mobileMenuClasses(true)).toContain('md:hidden');
    expect(mobileMenuClasses(false)).toContain('md:hidden');
  });
});

// The four personas' permission maps, exactly as authapp.Service.Permissions
// emits them from DefaultCatalog -- the nav is a pure function of this map.
const personas: Record<string, Record<string, string[]>> = {
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

describe('navItemsFor', () => {
  it('admin sees every surface', () => {
    expect(navItemsFor((r, a) => canOn(personas.alice, r, a)).map((i) => i.href)).toEqual([
      '/reports',
      '/executions',
      '/reservations',
      '/campaigns',
      '/tenants',
      '/clusters',
    ]);
  });

  it('the tenant roles see read surfaces only -- no campaigns, no clusters', () => {
    for (const who of ['bob', 'carol']) {
      const hrefs = navItemsFor((r, a) => canOn(personas[who], r, a)).map((i) => i.href);
      expect(hrefs).toEqual(['/reports', '/executions', '/reservations']);
    }
  });

  it('campaign_manager adds campaigns but never clusters (AC4)', () => {
    const hrefs = navItemsFor((r, a) => canOn(personas.dave, r, a)).map((i) => i.href);
    expect(hrefs).toEqual(['/reports', '/executions', '/reservations', '/campaigns']);
  });

  it('unauthenticated holds nothing: the nav is just the logo', () => {
    expect(navItemsFor(() => false)).toEqual([]);
  });
});

// The mounted half of task 23: the nav a persona actually renders, the
// persistent demo banner, and the logout flow (AC13/AC14's UI side).
(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const carol: SessionInfo = {
  subject: 'demo:carol',
  name: 'Carol (tenant viewer)',
  email: '',
  global_roles: [],
  tenants: { '1': ['tenant_viewer'] },
  permissions: personas.carol,
  demo: true,
};

function stubApi(handlers: Record<string, () => Response>, calls: string[] = []) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    for (const [path, respond] of Object.entries(handlers)) {
      if (url.endsWith(path)) {
        return respond();
      }
    }
    return new Response(JSON.stringify({ message: `no stub for ${url}` }), { status: 500 });
  });
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

let container: HTMLDivElement | null = null;
let root: Root | null = null;
let spyPath = '';

function LocationSpy() {
  spyPath = useLocation().pathname;
  return null;
}

async function renderLayout(fetchMock: ReturnType<typeof stubApi>) {
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal('fetch', fetchMock);
  // DashboardLayout's theme effect asks matchMedia, which jsdom lacks.
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: false }));
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter initialEntries={['/reports']}>
        <LocationSpy />
        <SessionProvider>
          <DashboardLayout>
            <p data-testid="content">page</p>
          </DashboardLayout>
        </SessionProvider>
      </MemoryRouter>
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
  spyPath = '';
});

describe('DashboardLayout (mounted)', () => {
  it('renders the persona-filtered nav and the persistent demo banner', async () => {
    await renderLayout(stubApi({ '/api/me': () => json(carol) }));

    const navHrefs = Array.from(container?.querySelectorAll('[data-testid="nav-links"] a') ?? []).map(
      (a) => (a as HTMLAnchorElement).getAttribute('href')
    );
    expect(navHrefs).toEqual(['/reports', '/executions', '/reservations']);

    const banner = container?.querySelector('[data-testid="demo-banner"]');
    expect(banner).not.toBeNull();
    expect(banner?.textContent).toContain('Carol (tenant viewer)');
  });

  it('unauthenticated renders an empty nav and no banner', async () => {
    await renderLayout(
      stubApi({
        '/api/me': () => json({ message: 'unauthenticated' }, 401),
        '/api/session/profiles': () => json({ profiles: [] }),
      })
    );

    expect(container?.querySelectorAll('[data-testid="nav-links"] a').length).toBe(0);
    expect(container?.querySelector('[data-testid="demo-banner"]')).toBeNull();
  });

  it('logout DELETEs the session, re-resolves to unauthenticated, and lands on /', async () => {
    const calls: string[] = [];
    let authed = true;
    await renderLayout(
      stubApi(
        {
          '/api/me': () => (authed ? json(carol) : json({ message: 'unauthenticated' }, 401)),
          '/api/session': () => {
            authed = false;
            return new Response(null, { status: 204 });
          },
          '/api/session/profiles': () => json({ profiles: [] }),
        },
        calls
      )
    );
    expect(spyPath).toBe('/reports');

    const bannerButton = container?.querySelector('[data-testid="demo-banner"] button') as HTMLButtonElement;
    await act(async () => {
      bannerButton.click();
    });

    // The nav's project switcher (phase 32) fetches its list once on
    // mount, right after the session resolves.
    expect(calls).toEqual([
      'GET /api/me',
      'GET /api/projects',
      'DELETE /api/session',
      'GET /api/me',
      'GET /api/session/profiles',
    ]);
    expect(spyPath).toBe('/');
    // The nav is empty again: the picker is what unauthenticated looks like.
    expect(container?.querySelectorAll('[data-testid="nav-links"] a').length).toBe(0);
  });
});

// Task 5 (phase 23): keyboard navigation in the shell -- the skip link,
// Escape-to-close for the mobile drawer, and the focus-ring contract on
// the nav links. The theme toggle needs no assertion here: it is a
// ui/Button, whose base classes already carry the house focus ring
// (focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500).
describe('DashboardLayout (keyboard)', () => {
  it('renders the skip link as the first anchor, targeting #main', async () => {
    await renderLayout(stubApi({ '/api/me': () => json(carol) }));

    const skip = container?.querySelector('a[href="#main"]');
    expect(skip).not.toBeNull();
    expect(skip?.textContent).toContain('Skip to content');
    // The anchor's target exists: main carries id="main".
    expect(container?.querySelector('main#main')).not.toBeNull();
    // DOM-order first: the skip link precedes the nav so Tab reaches it
    // before any nav chrome.
    expect(container?.querySelector('a')).toBe(skip);

    // Nav links (inline desktop set) carry the house focus ring; the
    // class string is the contract here, same as mobileMenuClasses above.
    for (const link of Array.from(container?.querySelectorAll('[data-testid="nav-links"] a') ?? [])) {
      expect(link.className).toContain('focus:ring-2');
      expect(link.className).toContain('focus:ring-sky-500');
    }
  });

  it('focuses the skip link on the first Tab from the page body', async () => {
    await renderLayout(stubApi({ '/api/me': () => json(carol) }));

    // jsdom implements no sequential focus navigation (a Tab keydown moves
    // nothing on its own) and this repo carries no @testing-library/user-event,
    // so this models userEvent.tab() the way the browser computes it for the
    // forward-from-body case: the first element in DOM order matching the
    // tabbable selector.
    const tabbable = document.body.querySelector<HTMLElement>(
      'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
    );
    tabbable?.focus();

    const skip = container?.querySelector('a[href="#main"]');
    expect(tabbable).toBe(skip);
    expect(document.activeElement).toBe(skip);
  });

  it('Escape closes the open mobile menu', async () => {
    await renderLayout(stubApi({ '/api/me': () => json(carol) }));

    const burger = container?.querySelector('.mobile-menu-button') as HTMLButtonElement;
    await act(async () => {
      burger.click();
    });
    const drawer = container?.querySelector('.mobile-menu');
    expect(drawer?.className).toContain('translate-y-0');
    expect(drawer?.className).not.toContain('invisible');

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    });
    expect(drawer?.className).toContain('-translate-y-full');
    expect(drawer?.className).toContain('invisible');
  });
});
