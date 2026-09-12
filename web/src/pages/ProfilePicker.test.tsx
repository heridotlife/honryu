// The picker is phase 20's only interactive identity surface: which cards
// render, what a click does (POST /api/session -> refresh -> /home), and
// what the already-authenticated visitor sees. Mounted with createRoot+act
// for the same reason as useSession.test.tsx: the behaviour spans fetch,
// context state, and the router, and none of it is visible to a pure test.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { SessionProvider } from '../hooks/useSession';
import ProfilePicker from './ProfilePicker';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const alice = { subject: 'demo:alice', name: 'Alice (service provider admin)', email: '', global_roles: ['service_provider_admin'], tenants: {}, permissions: { '*': ['*'] }, demo: true };

/** Routes stubbed fetch by path; records every call in order. */
function stubApi(handlers: Record<string, (init?: RequestInit) => Response>, calls: string[] = []) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}${init?.body ? ` ${String(init.body)}` : ''}`);
    for (const [path, respond] of Object.entries(handlers)) {
      if (url.endsWith(path)) {
        return respond(init);
      }
    }
    return new Response(JSON.stringify({ message: `no stub for ${url}` }), { status: 500 });
  });
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

let container: HTMLDivElement | null = null;
let root: Root | null = null;
let locationPath = '';

/** Records the router's current path on every render, wherever we are. */
function LocationSpy() {
  locationPath = useLocation().pathname;
  return null;
}

/** A /home stand-in proving the router actually landed there. */
function Where() {
  return <p data-testid="reports">reports</p>;
}

/** Mounts the picker exactly as the app does: SessionProvider + router. */
async function renderPicker(fetchMock: ReturnType<typeof stubApi>) {
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal('fetch', fetchMock);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter initialEntries={['/']}>
        <SessionProvider>
          <LocationSpy />
          <Routes>
            <Route path="/" element={<ProfilePicker />} />
            <Route path="/home" element={<Where />} />
          </Routes>
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
  locationPath = '';
});

describe('ProfilePicker', () => {
  it('renders one card per profile when unauthenticated', async () => {
    await renderPicker(
      stubApi({
        '/api/me': () => json({ message: 'unauthenticated' }, 401),
        '/api/session/profiles': () =>
          json({ profiles: [
            { id: 'alice', name: 'Alice (service provider admin)' },
            { id: 'bob', name: 'Bob (tenant editor)' },
          ] }),
      })
    );

    const buttons = container?.querySelectorAll('button') ?? [];
    expect(buttons.length).toBe(2);
    expect(buttons[0].textContent).toContain('Alice (service provider admin)');
    expect(buttons[1].textContent).toContain('bob');
  });

  it('shows the profiles error instead of empty cards when demo is off', async () => {
    await renderPicker(
      stubApi({
        '/api/me': () => json({ message: 'unauthenticated' }, 401),
        '/api/session/profiles': () => json({ message: 'demo sessions not configured' }, 404),
      })
    );

    expect(container?.querySelector('[data-testid="profiles-error"]')?.textContent).toBe('demo sessions not configured');
    expect(container?.querySelectorAll('button').length).toBe(0);
  });

  it('selecting a card signs in and lands on /home', async () => {
    const calls: string[] = [];
    let authenticated = false;
    await renderPicker(
      stubApi(
        {
          '/api/me': () => (authenticated ? json(alice) : json({ message: 'unauthenticated' }, 401)),
          '/api/session/profiles': () => json({ profiles: [{ id: 'alice', name: 'Alice (service provider admin)' }] }),
          '/api/session': () => {
            authenticated = true;
            return new Response(null, { status: 204 });
          },
        },
        calls
      )
    );
    expect(locationPath).toBe('/');

    await act(async () => {
      (container?.querySelector('button') as HTMLButtonElement).click();
    });

    expect(calls).toEqual([
      'GET /api/me',
      'GET /api/session/profiles',
      'POST /api/session {"profile":"alice"}',
      'GET /api/me',
    ]);
    expect(locationPath).toBe('/home');
  });

  it('stays on / and surfaces the error when the profile is unknown', async () => {
    const calls: string[] = [];
    await renderPicker(
      stubApi(
        {
          '/api/me': () => json({ message: 'unauthenticated' }, 401),
          '/api/session/profiles': () => json({ profiles: [{ id: 'ghost', name: 'Ghost' }] }),
          '/api/session': () => json({ message: 'unknown profile' }, 404),
        },
        calls
      )
    );

    await act(async () => {
      (container?.querySelector('button') as HTMLButtonElement).click();
    });

    expect(locationPath).toBe('/');
    expect(container?.querySelector('[data-testid="select-error"]')?.textContent).toBe('unknown profile');
    // The card re-enables so the mistake is recoverable.
    expect((container?.querySelector('button') as HTMLButtonElement).disabled).toBe(false);
  });

  it('redirects an already-authenticated visitor to /home without rendering cards', async () => {
    const calls: string[] = [];
    await renderPicker(
      stubApi(
        {
          '/api/me': () => json(alice),
        },
        calls
      )
    );

    expect(calls).toEqual(['GET /api/me']);
    expect(container?.querySelectorAll('button').length).toBe(0);
    expect(locationPath).toBe('/home');
  });
});
