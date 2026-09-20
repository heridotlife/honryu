// Phase 96: reactive state over the countdown preference
// (lib/countdownPref). The sync model is ProjectSwitcher's: localStorage
// is the one source of truth; the native `storage` event carries changes
// from OTHER tabs (it never fires in the tab that wrote), and a window
// CustomEvent carries them between consumers in THIS tab (the chip and
// the popover are separate mounted consumers — no context provider for a
// two-reader preference).
//
// useStartFlow deliberately does NOT use this hook: it reads the
// preference directly at begin() time, so the value is captured per
// launch rather than tracked per render.
import { useCallback, useEffect, useState } from 'react';
import { COUNTDOWN_STORAGE_KEY, getCountdownSeconds, setCountdownSeconds } from '../lib/countdownPref';

/**
 * Window event fired on preference change within this tab (the native
 * `storage` event only fires in other tabs, so this is how the chip sees
 * the popover's writes without a provider).
 */
const COUNTDOWN_CHANGED_EVENT = 'honryu:start-countdown-changed';

/** What useCountdownPref hands its consumer: [current, persist]. */
export function useCountdownPref(): [seconds: number, set: (seconds: number) => void] {
  const [seconds, setSecondsState] = useState(() => getCountdownSeconds());

  useEffect(() => {
    const sync = () => setSecondsState(getCountdownSeconds());
    // key null covers localStorage.clear() (a storage event with no key).
    const onStorage = (event: StorageEvent) => {
      if (event.key === null || event.key === COUNTDOWN_STORAGE_KEY) {
        sync();
      }
    };
    window.addEventListener('storage', onStorage);
    window.addEventListener(COUNTDOWN_CHANGED_EVENT, sync);
    return () => {
      window.removeEventListener('storage', onStorage);
      window.removeEventListener(COUNTDOWN_CHANGED_EVENT, sync);
    };
  }, []);

  // Invalid values are refused inside setCountdownSeconds (no write), so
  // every consumer re-reads the same unchanged value on the sync event.
  const set = useCallback((next: number) => {
    setCountdownSeconds(next);
    window.dispatchEvent(new CustomEvent(COUNTDOWN_CHANGED_EVENT));
  }, []);

  return [seconds, set];
}
