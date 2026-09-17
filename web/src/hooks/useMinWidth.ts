import { useEffect, useState } from 'react';

/**
 * Reactive CSS-media-query match, pinned to "at least this width" queries
 * (phase 79). CardTable asks for Tailwind's sm breakpoint
 * (`(min-width: 640px)`) to pick its table branch (wide) or card branch
 * (narrow), so exactly one of the two is ever in the DOM -- row-level
 * data-testids can therefore live on both the <tr> and the card root
 * without ever duplicating for getElementById-style selectors.
 *
 * jsdom computes no matchMedia, and the suite's existing minimal stubs
 * return a bare `{ matches }` object with no listener API, so this hook:
 *   - defaults to `true` (the wide/table branch) whenever matchMedia is
 *     unavailable -- every desktop-contract test and the e2e harness keep
 *     the exact markup they pin today; and
 *   - attaches its change listener through optional chaining, so a stub
 *     without addEventListener degrades to a one-shot read.
 */
export function useMinWidth(query: string): boolean {
  const [wide, setWide] = useState<boolean>(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
      return true;
    }
    return window.matchMedia(query).matches;
  });

  useEffect(() => {
    if (typeof window.matchMedia !== 'function') {
      return undefined;
    }
    const mql = window.matchMedia(query);
    // Re-read here rather than trusting the lazy initializer: the stub (or
    // the real media) may have changed between render and effect.
    setWide(mql.matches);
    const onChange = (e: MediaQueryListEvent): void => {
      setWide(e.matches);
    };
    mql.addEventListener?.('change', onChange);
    return () => {
      mql.removeEventListener?.('change', onChange);
    };
  }, [query]);

  return wide;
}

export default useMinWidth;
