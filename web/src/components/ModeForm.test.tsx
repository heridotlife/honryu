// The Simple-mode form (phase 90): renders mode/rate/duration, mirrors
// the server's input validation inline, surfaces soak's soft guidance,
// and reports submittability through the StageEditor convention. It must
// never derive resolved numbers -- there is nothing to pin there beyond
// the absence of any such UI.
import { describe, expect, it, vi, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ModeForm from './ModeForm';
import { initialModeForm, type ModeFormValue } from '../lib/modeConfig';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | null = null;
let root: Root | null = null;
let value: ModeFormValue = initialModeForm;
const validity: boolean[] = [];

/** Re-render the controlled form with the current value (a controlled
 * component only changes what the host feeds it). */
async function rerender() {
  await act(async () => {
    root!.render(
      <ModeForm
        value={value}
        onChange={next => {
          value = next;
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
async function setNumber(selector: string, v: string) {
  const el = container!.querySelector(selector) as HTMLInputElement;
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  await act(async () => {
    setter.call(el, v);
    el.dispatchEvent(new Event('input', { bubbles: true }));
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
  value = initialModeForm;
  validity.length = 0;
  vi.restoreAllMocks();
});

describe('ModeForm (phase 90)', () => {
  it('renders the three stated inputs with the defaults', async () => {
    await render();
    expect((container!.querySelector('[data-testid="mode-select"]') as HTMLSelectElement).value).toBe('burst');
    expect((container!.querySelector('[data-testid="mode-qps"]') as HTMLInputElement).value).toBe('100');
    expect((container!.querySelector('[data-testid="mode-duration"]') as HTMLInputElement).value).toBe('10');
    expect((container!.querySelector('[data-testid="mode-duration-unit"]') as HTMLSelectElement).value).toBe('m');
  });

  it('reports validity transitions and shows inline errors for bad input', async () => {
    await render();
    expect(validity.at(-1)).toBe(true);
    await setNumber('[data-testid="mode-qps"]', '0');
    expect(container!.querySelector('[data-testid="mode-qps-error"]')?.textContent).toContain('positive');
    expect(validity.at(-1)).toBe(false);
    await setNumber('[data-testid="mode-duration"]', '0');
    expect(container!.querySelector('[data-testid="mode-duration-error"]')?.textContent).toContain('positive');
    await setNumber('[data-testid="mode-qps"]', '250');
    expect(container!.querySelector('[data-testid="mode-qps-error"]')).toBeNull();
    expect(validity.at(-1)).toBe(false); // duration still invalid
  });

  it('surfaces the soak duration warning softly (role=status, not an error)', async () => {
    await render();
    // burst default: no warning
    expect(container!.querySelector('[data-testid="soak-warning"]')).toBeNull();
    const select = container!.querySelector('[data-testid="mode-select"]') as HTMLSelectElement;
    const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')!.set!;
    await act(async () => {
      setter.call(select, 'soak');
      select.dispatchEvent(new Event('change', { bubbles: true }));
    });
    await rerender();
    const warn = container!.querySelector('[data-testid="soak-warning"]') as HTMLElement;
    expect(warn.getAttribute('role')).toBe('status');
    expect(warn.textContent).toContain('soak');
  });

  it('offers exactly the three modes', async () => {
    await render();
    const options = Array.from(container!.querySelectorAll('[data-testid="mode-select"] option')).map(
      o => (o as HTMLOptionElement).value
    );
    expect(options).toEqual(['burst', 'ramp', 'soak']);
  });

  it('renders no concurrency/engines/ramp-up inputs — the server resolves those', async () => {
    await render();
    const labels = Array.from(container!.querySelectorAll('input,select')).map(el => el.getAttribute('aria-label'));
    expect(labels).not.toContain('concurrency');
    expect(labels).not.toContain('engines');
    expect(labels).not.toContain('rampup');
    expect(container!.textContent).not.toMatch(/little'?s law/i);
  });
});
