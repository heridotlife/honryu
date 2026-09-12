// Button (phase 52): the variant class contract, pinned as strings because
// jsdom computes no paint. The accent variant is the ONE amber control in
// the design system -- the single primary CTA per view -- while primary
// stays the sky gradient; these tests fail loudly if either drifts.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it } from 'vitest';
import Button from './Button';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | null = null;
let root: Root | null = null;

async function renderButton(variant: 'primary' | 'accent' | 'destructive') {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container!);
  await act(async () => {
    root!.render(<Button variant={variant}>Go</Button>);
  });
  return container!.querySelector('button')!;
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

describe('Button variants (phase 52)', () => {
  it('accent: amber ground, white text, amber focus ring', async () => {
    const el = await renderButton('accent');
    expect(el.className).toContain('bg-amber-600');
    expect(el.className).toContain('text-white');
    expect(el.className).toContain('hover:bg-amber-700');
    expect(el.className).toContain('dark:hover:bg-amber-500');
    expect(el.className).toContain('focus:ring-amber-500');
  });

  it('primary: still the sky gradient -- accent must not recolor it', async () => {
    const el = await renderButton('primary');
    expect(el.className).toContain('from-sky-500');
    expect(el.className).toContain('to-cyan-500');
    expect(el.className).toContain('focus:ring-sky-500');
    expect(el.className).not.toContain('amber');
  });

  it('destructive: untouched by the accent addition', async () => {
    const el = await renderButton('destructive');
    expect(el.className).toContain('border-red-600');
    expect(el.className).toContain('focus:ring-red-500');
  });
});
