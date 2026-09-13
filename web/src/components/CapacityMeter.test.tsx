// CapacityMeter's mounted tests (StageEditor.test.tsx's createRoot + act
// pattern; no testing-library in the house deps). Since phase 25 the
// numbers are real on quota-wired deployments, so the states that matter
// are (a) the honest "no capacity reported" line when the wire fields are
// absent and (b) the bar geometry when they exist -- which is exactly what
// /api/clusters rows carry today.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it } from 'vitest';
import CapacityMeter, { capacityFraction } from './CapacityMeter';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | null = null;
let root: Root | null = null;

async function renderMeter(props: { label: string; used?: number; ceiling?: number }) {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(<CapacityMeter {...props} />);
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

describe('capacityFraction', () => {
  it('returns the ratio for real numbers', () => {
    expect(capacityFraction(3, 10)).toBe(0.3);
    expect(capacityFraction(10, 10)).toBe(1);
  });

  it('is undefined unless both numbers are present', () => {
    expect(capacityFraction()).toBeUndefined();
    expect(capacityFraction(3)).toBeUndefined();
    expect(capacityFraction(undefined, 10)).toBeUndefined();
  });

  it('is undefined for non-finite input or a non-positive ceiling', () => {
    expect(capacityFraction(Number.NaN, 10)).toBeUndefined();
    expect(capacityFraction(3, Number.NaN)).toBeUndefined();
    expect(capacityFraction(3, 0)).toBeUndefined();
    expect(capacityFraction(3, -2)).toBeUndefined();
  });
});

describe('CapacityMeter honest empty state', () => {
  it('renders the no-capacity line, not a bar, when numbers are absent', async () => {
    await renderMeter({ label: 'engines' });
    expect(container!.textContent).toBe('no capacity reported');
    expect(container!.querySelector('svg')).toBeNull();
  });

  it('stays honest when only one of used/ceiling exists', async () => {
    await renderMeter({ label: 'engines', used: 3 });
    expect(container!.textContent).toBe('no capacity reported');
    await renderMeter({ label: 'engines', ceiling: 10 });
    expect(container!.textContent).toBe('no capacity reported');
  });
});

describe('CapacityMeter bar', () => {
  it('draws a track plus a proportional fill and the used/ceiling label', async () => {
    await renderMeter({ label: 'engines', used: 3, ceiling: 10 });
    const rects = Array.from(container!.querySelectorAll('rect'));
    expect(rects).toHaveLength(2);
    const track = rects[0];
    const fill = rects[1];
    expect(track.getAttribute('width')).toBe('120');
    expect(fill.getAttribute('width')).toBe('36'); // 30% of the 120px track
    expect(container!.textContent).toContain('3 / 10 engines');
  });

  it('carries an accessible summary naming the numbers', async () => {
    await renderMeter({ label: 'engines', used: 3, ceiling: 10 });
    const img = container!.querySelector('[role="img"]');
    expect(img?.getAttribute('aria-label')).toBe('3 of 10 engines in use');
  });

  it('renders an empty track for zero used', async () => {
    await renderMeter({ label: 'engines', used: 0, ceiling: 10 });
    const fill = container!.querySelectorAll('rect')[1];
    expect(fill.getAttribute('width')).toBe('0');
    expect(container!.textContent).toContain('0 / 10 engines');
  });

  it('turns red at 100% and names the over-capacity state', async () => {
    await renderMeter({ label: 'engines', used: 10, ceiling: 10 });
    const img = container!.querySelector('[role="img"]');
    expect(img?.className).toContain('text-red-600');
    expect(img?.getAttribute('aria-label')).toContain('at or over capacity');
  });

  it('caps the fill at the track while the numbers carry the overflow', async () => {
    await renderMeter({ label: 'engines', used: 12, ceiling: 10 });
    const fill = container!.querySelectorAll('rect')[1];
    expect(fill.getAttribute('width')).toBe('120'); // capped, not 144
    expect(container!.textContent).toContain('12 / 10 engines');
    expect(container!.querySelector('[role="img"]')?.className).toContain('text-red-600');
  });

  it('clamps a negative used count to an empty fill', async () => {
    await renderMeter({ label: 'engines', used: -4, ceiling: 10 });
    const fill = container!.querySelectorAll('rect')[1];
    expect(fill.getAttribute('width')).toBe('0');
  });
});
