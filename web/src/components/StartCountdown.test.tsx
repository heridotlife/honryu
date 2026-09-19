// Phase 93: the Start flow's countdown component (see StartCountdown.tsx).
// Fake timers drive the 1s ticker; the assertions pin the spec's UX
// contract: an explicit seconds countdown (not a spinner), polite live
// announcements ONLY at the 10/5/1 marks, cancel that stops the countdown
// without completing, and the reduced-motion static render.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import StartCountdown from './StartCountdown';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | null = null;
let root: Root | null = null;
const onComplete = vi.fn();
const onCancel = vi.fn();

async function mountCountdown() {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container!);
  await act(async () => {
    root!.render(<StartCountdown onComplete={onComplete} onCancel={onCancel} />);
  });
}

const remainingText = () => container!.querySelector('[data-testid="start-countdown-remaining"]')?.textContent ?? '';
const announceText = () => container!.querySelector('[data-testid="start-countdown-announce"]')?.textContent ?? '';

async function tick(ms: number) {
  await act(async () => {
    vi.advanceTimersByTime(ms);
  });
}

beforeEach(() => {
  // The component owns its interval, so fake timers must be live before
  // mount (the same discipline the purge-decay test applies to its timer).
  vi.useFakeTimers();
  onComplete.mockClear();
  onCancel.mockClear();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
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

describe('StartCountdown', () => {
  it('renders the ring and the full seconds text, ticking once per second', async () => {
    await mountCountdown();
    expect(remainingText()).toBe('Load test starts in 10s');
    expect(container!.querySelector('[data-testid="start-countdown-ring"]')).not.toBeNull();

    await tick(1_000);
    expect(remainingText()).toBe('Load test starts in 9s');
    await tick(3_000);
    expect(remainingText()).toBe('Load test starts in 6s');
  });

  it('announces only at the 10, 5 and 1 second marks', async () => {
    await mountCountdown();
    // The mount-mark text rides the initial render of the live region.
    expect(announceText()).toBe('Load test starts in 10s');

    // 9..6: the region must NOT change — no per-tick narration.
    await tick(4_000);
    expect(remainingText()).toBe('Load test starts in 6s');
    expect(announceText()).toBe('Load test starts in 10s');

    await tick(1_000);
    expect(announceText()).toBe('Load test starts in 5s');

    // 4..2: silent again.
    await tick(3_000);
    expect(remainingText()).toBe('Load test starts in 2s');
    expect(announceText()).toBe('Load test starts in 5s');

    await tick(1_000);
    expect(announceText()).toBe('Load test starts in 1s');
  });

  it('completes exactly once at zero and stops ticking', async () => {
    await mountCountdown();
    await tick(10_000);
    expect(onComplete).toHaveBeenCalledTimes(1);
    // Zero is the single pre-unmount paint: it says what happens next.
    expect(remainingText()).toBe('Starting…');

    // The interval is cleaned up: no second completion, no crash.
    await tick(5_000);
    expect(onComplete).toHaveBeenCalledTimes(1);
    expect(onCancel).not.toHaveBeenCalled();
  });

  it('cancel stops the countdown without ever completing', async () => {
    await mountCountdown();
    await tick(3_000);
    expect(remainingText()).toBe('Load test starts in 7s');

    const btn = container!.querySelector('[data-testid="start-countdown-cancel"]') as HTMLButtonElement;
    await act(async () => {
      btn.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(onCancel).toHaveBeenCalledTimes(1);

    // The component stopped its own timer: advancing past the original
    // deadline never fires the completion the cancel promised to prevent.
    await tick(30_000);
    expect(onComplete).not.toHaveBeenCalled();
  });

  it('renders static text without the ring under prefers-reduced-motion', async () => {
    // jsdom has no matchMedia; stub the reduce query as matching.
    vi.stubGlobal(
      'matchMedia',
      vi.fn().mockReturnValue({ matches: true, addEventListener: () => {}, removeEventListener: () => {} })
    );
    await mountCountdown();
    expect(container!.querySelector('[data-testid="start-countdown-ring"]')).toBeNull();
    expect(remainingText()).toBe('Load test starts in 10s');
    // The cancel affordance survives reduced motion — it is not decoration.
    expect(container!.querySelector('[data-testid="start-countdown-cancel"]')).not.toBeNull();
  });
});
