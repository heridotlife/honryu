import { act } from 'react';
import type { ReactElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import ProjectSwitcher, { PROJECT_STORAGE_KEY, useProjectSelection } from './ProjectSwitcher';

// The mounted half, DashboardLayout.test.tsx's style: createRoot + act,
// fetch stubbed per-URL. The switcher is session-independent (projects are
// caller-scoped server-side), so no SessionProvider is needed here.

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const projectsFixture = [
  { id: 1, name: 'phase16-live', owner: 'honryu', tenant_id: 1, created_time: '2026-08-01T00:00:00Z' },
  { id: 2, name: 'phase16-sched', owner: 'heri', tenant_id: 1, created_time: '2026-08-02T00:00:00Z' },
];

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

let container: HTMLDivElement | null = null;
let root: Root | null = null;

/** Renders the switcher (plus optional siblings) against a /api/projects stub. */
async function renderSwitcher(
  projectsBody: () => Response = () => json(projectsFixture),
  extra?: (switcher: ReactElement) => ReactElement
) {
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url === '/api/projects' || url.endsWith('/api/projects')) {
        return projectsBody();
      }
      return json({ message: `no stub for ${url}` }, 500);
    })
  );
  root = createRoot(container);
  const el = <ProjectSwitcher />;
  await act(async () => {
    root!.render(extra ? extra(el) : el);
  });
  // Flush the projects fetch's promise chain.
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

/** Opens the dropdown and returns it. */
async function openMenu() {
  await act(async () => {
    container!.querySelector('[data-testid="project-switcher"] button')!.dispatchEvent(
      new MouseEvent('click', { bubbles: true })
    );
  });
  const menu = container!.querySelector('[role="listbox"]');
  expect(menu).not.toBeNull();
  return menu!;
}

describe('ProjectSwitcher (mounted)', () => {
  it('renders the option list from the API, defaulting to All projects when nothing is stored', async () => {
    await renderSwitcher();

    const button = container!.querySelector('[data-testid="project-switcher"] button')!;
    expect(button.textContent).toContain('All projects');

    await openMenu();
    const optionAll = container!.querySelector('[data-testid="project-option-all"]');
    expect(optionAll?.textContent).toContain('All projects');
    expect(optionAll?.getAttribute('aria-selected')).toBe('true');
    for (const p of projectsFixture) {
      const option = container!.querySelector(`[data-testid="project-option-${p.id}"]`);
      expect(option?.textContent).toContain(p.name);
      expect(option?.getAttribute('aria-selected')).toBe('false');
    }
  });

  it('clicking an option persists the id to localStorage, fires the callback, and closes the menu', async () => {
    const onSelect = vi.fn();
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => json(projectsFixture))
    );
    root = createRoot(container);
    await act(async () => {
      root!.render(<ProjectSwitcher onSelect={onSelect} />);
    });
    await act(async () => {});
    await openMenu();

    await act(async () => {
      container!.querySelector('[data-testid="project-option-2"]')!.dispatchEvent(
        new MouseEvent('click', { bubbles: true })
      );
    });

    expect(localStorage.getItem(PROJECT_STORAGE_KEY)).toBe('2');
    expect(onSelect).toHaveBeenCalledWith('2');
    // The button now names the selection; the menu closed; the row is marked.
    expect(container!.querySelector('[data-testid="project-switcher"] button')!.textContent).toContain(
      'phase16-sched'
    );
    expect(container!.querySelector('[role="listbox"]')).toBeNull();
  });

  it('choosing All projects clears the stored selection back to ""', async () => {
    localStorage.setItem(PROJECT_STORAGE_KEY, '1');
    await renderSwitcher();
    await openMenu();

    await act(async () => {
      container!.querySelector('[data-testid="project-option-all"]')!.dispatchEvent(
        new MouseEvent('click', { bubbles: true })
      );
    });

    expect(localStorage.getItem(PROJECT_STORAGE_KEY)).toBe('');
    expect(container!.querySelector('[data-testid="project-switcher"] button')!.textContent).toContain(
      'All projects'
    );
  });

  it('hides the control when the caller has no projects or the fetch fails', async () => {
    await renderSwitcher(() => json([]));
    expect(container!.querySelector('[data-testid="project-switcher"]')).toBeNull();

    await renderSwitcher(() => json({ message: 'boom' }, 500));
    expect(container!.querySelector('[data-testid="project-switcher"]')).toBeNull();
  });

  // The sync mechanism phase 32 rides on: the nav switcher and the page
  // filters are separate consumers of the same localStorage key, and the
  // broadcast event is what keeps one tab's consumers coherent. Two
  // mounted switchers model that without a page under them.
  it('keeps separately mounted consumers in sync through the broadcast event', async () => {
    await renderSwitcher(() => json(projectsFixture), (switcher) => (
      <div>
        {switcher}
        <ProjectSwitcher />
      </div>
    ));

    const buttons = Array.from(container!.querySelectorAll('[data-testid="project-switcher"] button'));
    expect(buttons.length).toBe(2);
    await act(async () => {
      buttons[0].dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await act(async () => {
      container!.querySelector('[data-testid="project-option-1"]')!.dispatchEvent(
        new MouseEvent('click', { bubbles: true })
      );
    });

    for (const b of buttons) {
      expect(b.textContent).toContain('phase16-live');
    }
  });
});

describe('useProjectSelection', () => {
  it('normalizes a stored id the caller cannot see back to all projects once the list loads', async () => {
    localStorage.setItem(PROJECT_STORAGE_KEY, '99');
    let selection: ReturnType<typeof useProjectSelection> | null = null;
    function Probe() {
      selection = useProjectSelection();
      return null;
    }
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => json(projectsFixture))
    );
    root = createRoot(container);
    await act(async () => {
      root!.render(<Probe />);
    });
    await act(async () => {});

    expect(selection!.selectedId).toBe('');
    expect(selection!.selectedName).toBe('');
  });
});
