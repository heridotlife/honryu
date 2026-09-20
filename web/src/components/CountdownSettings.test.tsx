// Phase 96: the countdown control surface (see CountdownSettings.tsx),
// mounted createRoot + act (the house pattern). Pins the spec's UX
// contract: the labelled popover (label, helper, min/max/step, described
// by), save-on-change with invalid drafts refused and reverted, the
// storage-event sync that keeps a second tab honest, the in-tab event
// that keeps the chip and popover honest, and the chip's ≠-default rule.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import CountdownSettings, { CountdownChip } from './CountdownSettings';
import { COUNTDOWN_STORAGE_KEY, DEFAULT_COUNTDOWN_SECONDS } from '../lib/countdownPref';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | null = null;
let root: Root | null = null;

/** Mounts the gear + popover and (optionally) a sibling chip consumer. */
async function mount(withChip = false) {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container!);
  await act(async () => {
    root!.render(
      withChip ? (
        <>
          <CountdownSettings />
          <CountdownChip />
        </>
      ) : (
        <CountdownSettings />
      )
    );
  });
}

const gear = () => container!.querySelector('[data-testid="countdown-settings-button"]') as HTMLButtonElement;
const popover = () => container!.querySelector('[data-testid="countdown-settings-popover"]');
const input = () => container!.querySelector('[data-testid="countdown-seconds-input"]') as HTMLInputElement;
const chip = () => container!.querySelector('[data-testid="countdown-chip"]');

const click = async (el: Element) => {
  await act(async () => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
};

/** Native value setter + input event (React's tracker ignores plain writes). */
async function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
}

const blur = async (el: HTMLElement) => {
  await act(async () => {
    el.dispatchEvent(new FocusEvent('focusout', { bubbles: true }));
  });
};

/** The other tab's write, simulated: the value lands in localStorage AND
 *  the native storage event fires (in real browsers the event is what
 *  carries the change; the write itself is silent cross-tab). */
const otherTabWrites = async (value: string) => {
  await act(async () => {
    localStorage.setItem(COUNTDOWN_STORAGE_KEY, value);
    window.dispatchEvent(new StorageEvent('storage', { key: COUNTDOWN_STORAGE_KEY, newValue: value }));
  });
};

beforeEach(() => {
  localStorage.removeItem(COUNTDOWN_STORAGE_KEY);
});

afterEach(() => {
  const r = root;
  if (r !== null && container !== null) {
    act(() => {
      r.unmount();
    });
  }
  vi.unstubAllGlobals();
  container?.remove();
  container = null;
  root = null;
});

describe('CountdownSettings popover', () => {
  it('opens on the gear and renders the labelled input with helper text', async () => {
    await mount();
    expect(gear().getAttribute('aria-label')).toBe('Countdown settings');
    expect(gear().getAttribute('aria-expanded')).toBe('false');
    expect(popover()).toBeNull();

    await click(gear());
    expect(gear().getAttribute('aria-expanded')).toBe('true');
    expect(popover()).not.toBeNull();

    // The visible label is tied to the input; the helper is its
    // aria-describedby target; the bounds ride on the element itself.
    const label = container!.querySelector('label[for="countdown-seconds-input"]')!;
    expect(label.textContent).toBe('Launch countdown (s)');
    expect(input().type).toBe('number');
    expect(input().min).toBe('0');
    expect(input().max).toBe('60');
    expect(input().step).toBe('1');
    expect(input().getAttribute('aria-describedby')).toBe('countdown-seconds-help');
    expect(container!.querySelector('#countdown-seconds-help')?.textContent).toBe('0 starts immediately');
    // The field mirrors the stored preference (default 10).
    expect(input().value).toBe(String(DEFAULT_COUNTDOWN_SECONDS));
  });

  it('saves on change: typing a valid value persists and the field shows it', async () => {
    await mount();
    await click(gear());
    await type(input(), '5');
    expect(localStorage.getItem(COUNTDOWN_STORAGE_KEY)).toBe('5');
    expect(input().value).toBe('5');
  });

  it('refuses invalid drafts: nothing persists, blur reverts to the stored value', async () => {
    localStorage.setItem(COUNTDOWN_STORAGE_KEY, '5');
    await mount();
    await click(gear());
    expect(input().value).toBe('5');

    for (const bad of ['61', '-1', '2.5', '']) {
      await type(input(), bad);
      expect(localStorage.getItem(COUNTDOWN_STORAGE_KEY), `draft ${JSON.stringify(bad)}`).toBe('5');
    }
    await blur(input());
    expect(input().value).toBe('5');
  });

  it('Enter ends the edit the same way blur does', async () => {
    localStorage.setItem(COUNTDOWN_STORAGE_KEY, '5');
    await mount();
    await click(gear());
    await type(input(), '61');
    await act(async () => {
      input().dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    });
    expect(input().value).toBe('5');
    expect(localStorage.getItem(COUNTDOWN_STORAGE_KEY)).toBe('5');
  });

  it('a storage event from another tab updates the open popover in place', async () => {
    await mount();
    await click(gear());
    expect(input().value).toBe('10');

    await otherTabWrites('7');
    expect(input().value).toBe('7');
  });

  it('Escape closes the popover', async () => {
    await mount();
    await click(gear());
    expect(popover()).not.toBeNull();

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    });
    expect(popover()).toBeNull();
    // Toggle state followed: reopening works.
    await click(gear());
    expect(popover()).not.toBeNull();
  });

  it('an outside tap closes the popover without eating the toggle click', async () => {
    await mount();
    await click(gear());
    expect(popover()).not.toBeNull();

    await act(async () => {
      document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    });
    expect(popover()).toBeNull();
  });
});

describe('CountdownChip', () => {
  it('is absent at the default and appears as "Ns" for any other stored value', async () => {
    await mount(true);
    expect(chip()).toBeNull();

    await otherTabWrites('5');
    expect(chip()?.textContent).toBe('5s');

    await otherTabWrites('0');
    expect(chip()?.textContent).toBe('0s');

    await otherTabWrites('10');
    expect(chip()).toBeNull();
  });

  it('follows a change made through the popover in this tab (in-tab sync)', async () => {
    await mount(true);
    expect(chip()).toBeNull();

    await click(gear());
    await type(input(), '3');
    expect(localStorage.getItem(COUNTDOWN_STORAGE_KEY)).toBe('3');
    expect(chip()?.textContent).toBe('3s');
  });
});
