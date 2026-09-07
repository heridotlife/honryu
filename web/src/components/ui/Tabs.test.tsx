// The Tabs primitive's mounted contract (DashboardLayout.test.tsx's style:
// createRoot + act): the ARIA wiring, arrow-key cycling with wrap, and the
// render-everything rule -- inactive panels keep their hidden attribute but
// stay in the DOM, because pages address every panel without clicking first.
import { act, useState } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it } from 'vitest';
import { TabPanel, Tabs } from './Tabs';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const TABS = [
  { id: 'overview', label: 'Overview' },
  { id: 'timeseries', label: 'Time series' },
  { id: 'labels', label: 'Labels' },
];

/** Controlled harness mirroring how pages use Tabs: selection lives above. */
function Harness() {
  const [active, setActive] = useState('overview');
  return (
    <div>
      <Tabs tabs={TABS} active={active} onChange={setActive} data-testid="tabs" />
      {TABS.map((t) => (
        <TabPanel key={t.id} id={t.id} active={active}>
          {t.label} content
        </TabPanel>
      ))}
    </div>
  );
}

let container: HTMLDivElement | null = null;
let root: Root | null = null;

async function renderHarness() {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(<Harness />);
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
});

function tabButton(id: string): HTMLButtonElement {
  const el = container!.querySelector(`[role="tab"]#tab-${id}`);
  if (!(el instanceof HTMLButtonElement)) {
    throw new Error(`tab ${id} not rendered`);
  }
  return el;
}

async function pressKey(el: Element, key: string) {
  await act(async () => {
    el.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }));
  });
}

describe('Tabs ARIA wiring', () => {
  it('renders a tablist whose tabs point at their panels', async () => {
    await renderHarness();

    expect(container!.querySelector('[data-testid="tabs"]')?.getAttribute('role')).toBe('tablist');
    const tabs = Array.from(container!.querySelectorAll('[role="tab"]'));
    expect(tabs.map((t) => t.textContent)).toEqual(['Overview', 'Time series', 'Labels']);
    for (const tab of tabs) {
      const id = tab.id.replace(/^tab-/, '');
      expect(tab.getAttribute('aria-controls')).toBe(`panel-${id}`);
      const panel = container!.querySelector(`#${tab.getAttribute('aria-controls')}`);
      expect(panel?.getAttribute('role')).toBe('tabpanel');
      expect(panel?.getAttribute('aria-labelledby')).toBe(tab.id);
    }
  });

  it('marks exactly the active tab selected and only it tabbable', async () => {
    await renderHarness();

    expect(tabButton('overview').getAttribute('aria-selected')).toBe('true');
    expect(tabButton('timeseries').getAttribute('aria-selected')).toBe('false');
    expect(tabButton('labels').getAttribute('aria-selected')).toBe('false');
    expect(tabButton('overview').tabIndex).toBe(0);
    expect(tabButton('timeseries').tabIndex).toBe(-1);
  });

  it('selects a tab on click', async () => {
    await renderHarness();
    await act(async () => {
      tabButton('labels').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });

    expect(tabButton('labels').getAttribute('aria-selected')).toBe('true');
    expect(tabButton('overview').getAttribute('aria-selected')).toBe('false');
  });
});

describe('Tabs keyboard cycling', () => {
  it('ArrowRight moves selection and focus to the next tab, wrapping at the end', async () => {
    await renderHarness();

    await pressKey(tabButton('overview'), 'ArrowRight');
    expect(tabButton('timeseries').getAttribute('aria-selected')).toBe('true');
    expect(document.activeElement).toBe(tabButton('timeseries'));

    await pressKey(tabButton('timeseries'), 'ArrowRight');
    expect(tabButton('labels').getAttribute('aria-selected')).toBe('true');

    // Wrap: past the last tab is the first.
    await pressKey(tabButton('labels'), 'ArrowRight');
    expect(tabButton('overview').getAttribute('aria-selected')).toBe('true');
    expect(document.activeElement).toBe(tabButton('overview'));
  });

  it('ArrowLeft moves selection back, wrapping at the start', async () => {
    await renderHarness();

    // Wrap: before the first tab is the last.
    await pressKey(tabButton('overview'), 'ArrowLeft');
    expect(tabButton('labels').getAttribute('aria-selected')).toBe('true');

    await pressKey(tabButton('labels'), 'ArrowLeft');
    expect(tabButton('timeseries').getAttribute('aria-selected')).toBe('true');
  });
});

describe('TabPanel render-everything rule', () => {
  it('keeps inactive panels in the DOM behind the hidden attribute', async () => {
    await renderHarness();

    const activePanel = container!.querySelector('#panel-overview');
    expect(activePanel?.getAttribute('hidden')).toBeNull();
    expect(activePanel?.textContent).toContain('Overview content');

    const inactive = container!.querySelector('#panel-timeseries');
    expect(inactive).not.toBeNull();
    expect(inactive?.hasAttribute('hidden')).toBe(true);
    expect(inactive?.textContent).toContain('Time series content');

    // Hidden state follows selection, without unmounting anything.
    await act(async () => {
      tabButton('labels').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(container!.querySelector('#panel-labels')?.hasAttribute('hidden')).toBe(false);
    expect(container!.querySelector('#panel-overview')?.hasAttribute('hidden')).toBe(true);
  });
});
