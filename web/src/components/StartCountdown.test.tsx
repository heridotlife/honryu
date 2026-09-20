// Phase 93: the Start flow's countdown component (see StartCountdown.tsx).
// Fake timers drive the 1s ticker; the assertions pin the spec's UX
// contract: an explicit seconds countdown (not a spinner), polite live
// announcements ONLY at the scaled marks, cancel that stops the countdown
// without completing, and the reduced-motion static render. Phase 96 adds
// the scaled-length contract: the marks (and the ring/text) follow
// whatever seconds the operator's preference handed in.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import StartCountdown, { announceMarks } from './StartCountdown';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | null = null;
let root: Root | null = null;
const onComplete = vi.fn();
const onCancel = vi.fn();

async function mountCountdown(seconds?: number) {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container!);
  await act(async () => {
    root!.render(
      seconds === undefined ? (
        <StartCountdown onComplete={onComplete} onCancel={onCancel} />
      ) : (
        <StartCountdown seconds={seconds} onComplete={onComplete} onCancel={onCancel} />
      )
    );
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

describe('announceMarks', () => {
  it('scales the cadence to the length: 10 → {10,5,1}, 60 → {60,30,1}', () => {
    expect([...announceMarks(10)].sort((a, b) => b - a)).toEqual([10, 5, 1]);
    expect([...announceMarks(60)].sort((a, b) => b - a)).toEqual([60, 30, 1]);
  });

  it('floors the half mark so short lengths dedupe onto the final second', () => {
    // 3's half is 1.5 — the floor lands on 1, which the final mark already
    // covers; 1 has no room for a middle mark at all; 2 keeps start+final.
    expect([...announceMarks(3)].sort((a, b) => b - a)).toEqual([3, 1]);
    expect([...announceMarks(1)].sort((a, b) => b - a)).toEqual([1]);
    expect([...announceMarks(2)].sort((a, b) => b - a)).toEqual([2, 1]);
  });
});

describe('StartCountdown at a scaled length (phase 96)', () => {
  it('counts down 3s, announcing only at 3 and 1', async () => {
    await mountCountdown(3);
    expect(remainingText()).toBe('Load test starts in 3s');
    expect(announceText()).toBe('Load test starts in 3s');

    await tick(1_000);
    expect(remainingText()).toBe('Load test starts in 2s');
    expect(announceText()).toBe('Load test starts in 3s'); // 2 is not a mark

    await tick(1_000);
    expect(announceText()).toBe('Load test starts in 1s');

    await tick(1_000);
    expect(onComplete).toHaveBeenCalledTimes(1);
    expect(remainingText()).toBe('Starting…');
  });

  it('counts down a single second: mount-mark announcement, immediate complete', async () => {
    await mountCountdown(1);
    expect(remainingText()).toBe('Load test starts in 1s');
    expect(announceText()).toBe('Load test starts in 1s');

    await tick(1_000);
    expect(onComplete).toHaveBeenCalledTimes(1);
    expect(onCancel).not.toHaveBeenCalled();
  });

  it('counts down 60s with the ring drawn at a sixtieth per second', async () => {
    await mountCountdown(60);
    expect(remainingText()).toBe('Load test starts in 60s');
    await tick(30_000);
    expect(remainingText()).toBe('Load test starts in 30s');
    expect(announceText()).toBe('Load test starts in 30s'); // 30 IS a mark
    await tick(28_000);
    expect(announceText()).toBe('Load test starts in 30s'); // 2 is not
    await tick(1_000);
    expect(announceText()).toBe('Load test starts in 1s');
  });
});
