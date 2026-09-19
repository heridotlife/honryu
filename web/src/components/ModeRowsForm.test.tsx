// The multi-scenario Simple load form (phase 91): one row per scenario
// with per-row mode/rate/duration, add/remove rows, per-row validation
// (qps/duration/duplicate name) and per-row soak guidance, reporting
// submittability through the StageEditor convention. Like ModeForm, it
// never derives resolved numbers -- there is nothing to pin beyond their
// absence.
import { describe, expect, it, vi, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ModeRowsForm from './ModeRowsForm';
import { initialModeRows, type ModeRowValue } from '../lib/modeConfig';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | null = null;
let root: Root | null = null;
let rows: ModeRowValue[] = initialModeRows.map(r => ({ ...r }));
const validity: boolean[] = [];

/** Re-render the controlled form with the current rows (a controlled
 * component only changes what the host feeds it). */
async function rerender() {
  await act(async () => {
    root!.render(
      <ModeRowsForm
        value={rows}
        onChange={next => {
          rows = next;
        }}
        onValidityChange={v => validity.push(v)}
      />
    );
  });
}

async function render() {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await rerender();
  await act(async () => {});
}

/** Native value setter + input event (React's tracker ignores plain writes). */
async function setInput(selector: string, v: string) {
  const el = container!.querySelector(selector) as HTMLInputElement;
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  await act(async () => {
    setter.call(el, v);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
  await rerender();
}

async function setSelect(selector: string, v: string) {
  const el = container!.querySelector(selector) as HTMLSelectElement;
  const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')!.set!;
  await act(async () => {
    setter.call(el, v);
    el.dispatchEvent(new Event('change', { bubbles: true }));
  });
  await rerender();
}

async function click(selector: string) {
  await act(async () => {
    container!.querySelector(selector)!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
  await rerender();
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
  rows = initialModeRows.map(r => ({ ...r }));
  validity.length = 0;
  vi.restoreAllMocks();
});

describe('ModeRowsForm (phase 91)', () => {
  it('renders the single default row with the mode-form defaults', async () => {
    await render();
    expect(container!.querySelectorAll('[data-testid^="mode-row-"][data-testid$="-qps"]')).toHaveLength(1);
    expect((container!.querySelector('[data-testid="mode-row-0-qps"]') as HTMLInputElement).value).toBe('100');
    expect((container!.querySelector('[data-testid="mode-row-0-duration"]') as HTMLInputElement).value).toBe('10');
    expect(validity.at(-1)).toBe(true);
  });

  it('adds and removes rows; the last row cannot be removed', async () => {
    await render();
    await click('[data-testid="mode-add-row"]');
    expect(container!.querySelectorAll('[data-testid^="mode-row-"][data-testid$="-qps"]')).toHaveLength(2);
    await click('[aria-label="remove scenario 1"]');
    expect(container!.querySelectorAll('[data-testid^="mode-row-"][data-testid$="-qps"]')).toHaveLength(1);
    const remove = container!.querySelector('[aria-label="remove scenario 1"]') as HTMLButtonElement;
    expect(remove.disabled).toBe(true);
  });

  it('validates per row: the second row can be invalid while the first stays clean', async () => {
    await render();
    await click('[data-testid="mode-add-row"]');
    await setInput('[data-testid="mode-row-1-qps"]', '0');
    expect(container!.querySelector('[data-testid="mode-row-0-qps-error"]')).toBeNull();
    expect(container!.querySelector('[data-testid="mode-row-1-qps-error"]')?.textContent).toContain('positive');
    expect(validity.at(-1)).toBe(false);
    await setInput('[data-testid="mode-row-1-qps"]', '25');
    expect(container!.querySelector('[data-testid="mode-row-1-qps-error"]')).toBeNull();
    expect(validity.at(-1)).toBe(true);
  });

  it('flags a scenario name used on two rows', async () => {
    await render();
    await click('[data-testid="mode-add-row"]');
    await setInput('[data-testid="mode-row-0-name"]', 'checkout');
    await setInput('[data-testid="mode-row-1-name"]', 'checkout');
    expect(container!.querySelector('[data-testid="mode-row-0-name-error"]')?.textContent).toContain('another row');
    expect(validity.at(-1)).toBe(false);
    await setInput('[data-testid="mode-row-1-name"]', 'search');
    expect(container!.querySelector('[data-testid="mode-row-0-name-error"]')).toBeNull();
    expect(validity.at(-1)).toBe(true);
  });

  it('surfaces the soak warning per row, independently', async () => {
    await render();
    await click('[data-testid="mode-add-row"]');
    // Row 1: burst — quiet. Row 2: soak at the default 10m — warned.
    await setSelect('[data-testid="mode-row-1-mode"]', 'soak');
    expect(container!.querySelector('[data-testid="soak-warning-0"]')).toBeNull();
    const warn = container!.querySelector('[data-testid="soak-warning-1"]') as HTMLElement;
    expect(warn.getAttribute('role')).toBe('status');
    expect(warn.textContent).toContain('soak');
    // Raising row 2 to an hour clears its own warning.
    await setInput('[data-testid="mode-row-1-duration"]', '1');
    await setSelect('[data-testid="mode-row-1-duration-unit"]', 'h');
    expect(container!.querySelector('[data-testid="soak-warning-1"]')).toBeNull();
  });

  it('renders no concurrency/engines/ramp-up inputs — the server resolves those', async () => {
    await render();
    const labels = Array.from(container!.querySelectorAll('input,select')).map(el => el.getAttribute('aria-label'));
    expect(labels.some(l => l === null)).toBe(false);
    expect(labels).not.toContain('concurrency');
    expect(labels).not.toContain('engines');
    expect(labels).not.toContain('rampup');
  });
});
