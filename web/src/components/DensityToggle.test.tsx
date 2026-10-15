import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it } from 'vitest';
import DensityToggle, { DENSITY_STORAGE_KEY } from './DensityToggle';

// Phase 106: the density switch, unit-pinned the same way the theme toggle
// is -- localStorage in, html class out, persistence on click. This suite
// follows the repo's createRoot+act convention rather than
// @testing-library/react, which the project deliberately does not depend
// on (no new dependencies is a phase rule).

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | null = null;
let root: Root | null = null;

async function renderToggle() {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(<DensityToggle />);
  });
}

function option(mode: string): HTMLButtonElement {
  return container!.querySelector(`[data-density="${mode}"]`)!;
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
  // The component's side effects live OUTSIDE the container -- clean them
  // or the next test inherits this one's mode.
  document.documentElement.classList.remove('density-compact');
  localStorage.removeItem(DENSITY_STORAGE_KEY);
});

describe('DensityToggle (phase 106)', () => {
  it('resolves the saved compact choice on mount: html class + active option', async () => {
    localStorage.setItem(DENSITY_STORAGE_KEY, 'compact');
    await renderToggle();

    expect(document.documentElement.classList.contains('density-compact')).toBe(true);
    expect(option('compact').getAttribute('aria-pressed')).toBe('true');
    expect(option('comfortable').getAttribute('aria-pressed')).toBe('false');
  });

  it('defaults to comfortable when no choice is saved -- no OS fallback', async () => {
    await renderToggle();

    expect(document.documentElement.classList.contains('density-compact')).toBe(false);
    expect(option('comfortable').getAttribute('aria-pressed')).toBe('true');
    // The container carries the testid contract the layout and e2e select on.
    expect(container!.querySelector('[data-testid="density-toggle"]')).not.toBeNull();
  });

  it('an unknown saved value falls back to comfortable, not compact', async () => {
    localStorage.setItem(DENSITY_STORAGE_KEY, 'cozy');
    await renderToggle();

    expect(document.documentElement.classList.contains('density-compact')).toBe(false);
    expect(option('comfortable').getAttribute('aria-pressed')).toBe('true');
  });

  it('clicking compact persists the choice and adds .density-compact to <html>', async () => {
    await renderToggle();

    await act(async () => {
      option('compact').click();
    });

    expect(document.documentElement.classList.contains('density-compact')).toBe(true);
    expect(localStorage.getItem(DENSITY_STORAGE_KEY)).toBe('compact');
    expect(option('compact').getAttribute('aria-pressed')).toBe('true');
  });

  it('clicking back to comfortable removes the class and persists it', async () => {
    localStorage.setItem(DENSITY_STORAGE_KEY, 'compact');
    await renderToggle();
    expect(document.documentElement.classList.contains('density-compact')).toBe(true);

    await act(async () => {
      option('comfortable').click();
    });

    expect(document.documentElement.classList.contains('density-compact')).toBe(false);
    expect(localStorage.getItem(DENSITY_STORAGE_KEY)).toBe('comfortable');
    expect(option('comfortable').getAttribute('aria-pressed')).toBe('true');
  });

  it('renders both options as icon + label segmented buttons', async () => {
    await renderToggle();

    for (const mode of ['comfortable', 'compact']) {
      const btn = option(mode);
      // The glyph is an inline 16px SVG stroked with the text color...
      const svg = btn.querySelector('svg');
      expect(svg).not.toBeNull();
      expect(svg!.getAttribute('class')).toContain('h-4');
      expect(svg!.getAttribute('stroke')).toBe('currentColor');
      // ...and the label survives as text (compact tightens whitespace,
      // it never trades the words away).
      expect(btn.textContent).toMatch(/Comfortable|Compact/);
    }
    expect(container!.querySelector('[data-testid="density-toggle"]')!.getAttribute('role')).toBe('group');
  });
});
