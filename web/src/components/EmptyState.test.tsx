// The shared EmptyState (phase 76): slot rendering (icon, title,
// description), the single-action rule (Link for `to`, Button for
// `onClick`, no control at all without an action), and the testid
// convention every page's empty branch pins. Mounted in the house
// createRoot + act style; MemoryRouter because the `to` action renders
// a Link.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { FlaskConical } from 'lucide-react';
import EmptyState from './EmptyState';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | null = null;
let root: Root | null = null;

async function renderEmptyState(props: Parameters<typeof EmptyState>[0]) {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter>
        <EmptyState {...props} />
      </MemoryRouter>,
    );
  });
  await act(async () => {});
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
  vi.restoreAllMocks();
});

describe('EmptyState', () => {
  it('renders title, description, and the icon slot (icon never carries meaning alone)', async () => {
    await renderEmptyState({
      icon: <FlaskConical className="size-6" />,
      title: 'No scenarios yet',
      description: 'Create one from a template.',
    });

    const rootEl = container!.querySelector('[data-testid="empty-state"]')!;
    expect(rootEl).not.toBeNull();
    expect(rootEl.querySelector('[data-testid="empty-state-title"]')?.textContent).toBe('No scenarios yet');
    expect(rootEl.querySelector('[data-testid="empty-state-description"]')?.textContent).toBe(
      'Create one from a template.',
    );
    // The icon is decorative: aria-hidden, and the meaning sits in the text.
    expect(rootEl.querySelector('[aria-hidden="true"] svg')).not.toBeNull();
  });

  it('renders a route action as a link carrying the href', async () => {
    await renderEmptyState({ title: 'No reports yet', action: { label: 'Run a scenario first', to: '/scenarios' } });

    const action = container!.querySelector<HTMLAnchorElement>('[data-testid="empty-state-action"]')!;
    expect(action.tagName).toBe('A');
    expect(action.getAttribute('href')).toBe('/scenarios');
    expect(action.textContent).toBe('Run a scenario first');
  });

  it('renders a behavior action as a button that fires once per click', async () => {
    const onClick = vi.fn();
    await renderEmptyState({ title: 'No runs yet', action: { label: 'Run this scenario', onClick } });

    const action = container!.querySelector<HTMLButtonElement>('[data-testid="empty-state-action"]')!;
    expect(action.tagName).toBe('BUTTON');
    await act(async () => {
      action.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it('renders message-only when there is no action -- no dead button', async () => {
    await renderEmptyState({ title: 'Nothing scheduled', description: 'Check back later.' });

    expect(container!.querySelector('[data-testid="empty-state-action"]')).toBeNull();
    expect(container!.querySelector('a')).toBeNull();
    expect(container!.querySelector('button')).toBeNull();
  });

  it('honors a page-level test id for the root and its slots', async () => {
    await renderEmptyState({ title: 'No clusters', testId: 'clusters-empty' });

    expect(container!.querySelector('[data-testid="clusters-empty"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="clusters-empty-title"]')?.textContent).toBe('No clusters');
  });
});
