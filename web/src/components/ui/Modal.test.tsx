// The shared dialog's own contract (phase 77), the mounted createRoot +
// act style: role/aria wiring (dialog announced by its title via
// aria-labelledby), focus moves to the first focusable on open, Tab and
// Shift+Tab cycle INSIDE the dialog (the trap phase 34/39 dialogs
// lacked), Escape closes, a backdrop press closes but a press inside
// does not, and focus returns to the opener on close.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import Modal from './Modal';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | null = null;
let root: Root | null = null;
const closed = vi.fn();

async function renderModal(): Promise<void> {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <Modal
        title="Example dialog"
        subtitle="What this dialog is for."
        onClose={closed}
        closeLabel="Close example"
        overlayTestId="modal-overlay"
        dialogTestId="modal-dialog"
      >
        <button type="button" data-testid="second-control">
          Second
        </button>
        <a href="#somewhere" data-testid="last-control">
          A link
        </a>
      </Modal>,
    );
  });
}

function dialog(): HTMLElement {
  return container!.querySelector('[data-testid="modal-dialog"]') as HTMLElement;
}

function press(key: string, init: KeyboardEventInit = {}): void {
  act(() => {
    document.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, ...init }));
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
  closed.mockClear();
});

describe('Modal', () => {
  it('exposes role=dialog, aria-modal, and a labelledby pointing at the visible title', async () => {
    await renderModal();
    const d = dialog();
    expect(d.getAttribute('role')).toBe('dialog');
    expect(d.getAttribute('aria-modal')).toBe('true');
    const labelledBy = d.getAttribute('aria-labelledby');
    expect(labelledBy).not.toBeNull();
    // The label resolves to the heading INSIDE this dialog, with its text.
    const label = document.getElementById(labelledBy!);
    expect(label).not.toBeNull();
    expect(d.contains(label)).toBe(true);
    expect(label!.tagName).toBe('H2');
    expect(label!.textContent).toBe('Example dialog');
  });

  it('moves focus to the first focusable element on open (the close button)', async () => {
    await renderModal();
    expect(document.activeElement).toBe(container!.querySelector('[aria-label="Close example"]'));
  });

  it('traps Tab: from the last focusable it wraps to the first', async () => {
    await renderModal();
    const last = container!.querySelector('[data-testid="last-control"]') as HTMLElement;
    act(() => {
      last.focus();
    });
    press('Tab');
    expect(document.activeElement).toBe(container!.querySelector('[aria-label="Close example"]'));
  });

  it('traps Shift+Tab: from the first focusable it wraps to the last', async () => {
    await renderModal();
    // Open focus sits on the close button (first). Shift+Tab wraps.
    press('Tab', { shiftKey: true });
    expect(document.activeElement).toBe(container!.querySelector('[data-testid="last-control"]'));
  });

  it('pulls focus that left the dialog back to the first focusable on Tab', async () => {
    await renderModal();
    act(() => {
      (document.body as HTMLElement).focus();
    });
    press('Tab');
    expect(document.activeElement).toBe(container!.querySelector('[aria-label="Close example"]'));
  });

  it('closes on Escape', async () => {
    await renderModal();
    press('Escape');
    expect(closed).toHaveBeenCalledTimes(1);
  });

  it('closes on a backdrop press but not on a press inside the dialog', async () => {
    await renderModal();
    const overlay = container!.querySelector('[data-testid="modal-overlay"]')!;
    act(() => {
      overlay.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    });
    expect(closed).toHaveBeenCalledTimes(1);
    act(() => {
      container!
        .querySelector('[data-testid="second-control"]')!
        .dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    });
    // Still one: the in-dialog press never reached the close decision.
    expect(closed).toHaveBeenCalledTimes(1);
  });

  it('restores focus to the opener when it closes', async () => {
    // The opener: focused before the dialog mounts, still mounted after
    // it closes (the browser focuses a clicked button; jsdom does not,
    // so the test does it explicitly).
    const opener = document.createElement('button');
    opener.textContent = 'Open';
    document.body.appendChild(opener);
    act(() => {
      opener.focus();
    });
    await renderModal();
    expect(document.activeElement).not.toBe(opener);
    act(() => {
      root!.unmount();
    });
    root = null;
    expect(document.activeElement).toBe(opener);
    opener.remove();
  });
});
