import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it } from 'vitest';
import RunStatusBadge, { outcomeIcons } from './RunStatusBadge';
import type { Outcome } from '../api/reports';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

// The never-color-only rule (phase 67b): every outcome renders its icon AND
// its word, over OutcomeBadge's shared classes.
let container: HTMLDivElement | null = null;
let root: Root | null = null;

async function renderBadge(outcome: Outcome) {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(<RunStatusBadge outcome={outcome} />);
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

describe('RunStatusBadge', () => {
  it('pairs an icon with the outcome word for every outcome', async () => {
    for (const outcome of ['passed', 'failed', 'aborted', 'error'] as Outcome[]) {
      await renderBadge(outcome);
      const chip = container!.querySelector('span')!;
      // The word is the accessible, color-free channel.
      expect(chip.textContent).toBe(outcome);
      // The icon rides beside it (lucide renders an svg).
      expect(chip.querySelector('svg')).not.toBeNull();
      // And every outcome has a distinct glyph pinned in the map.
      expect(outcomeIcons[outcome]).toBeDefined();
    }
  });
});
