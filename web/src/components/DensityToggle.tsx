import { useEffect, useState } from 'react';

/**
 * DensityToggle (phase 106) -- the comfortable/compact row-density switch,
 * built on the exact pattern the theme toggle proved out in
 * DashboardLayout: a localStorage-backed preference applied as a class on
 * <html> (`density-compact`), resolved once on mount, persisted on click.
 *
 * Deliberately prop-free and self-contained so it can sit in the desktop
 * nav and the mobile drawer without wiring: every instance resolves the
 * same key, so they cannot disagree about the html class. Density has NO
 * OS-preference fallback (unlike theme) -- it is a table-density
 * preference, not an accessibility medium, so absence of a saved choice
 * simply means comfortable.
 */

export type DensityMode = 'comfortable' | 'compact';

/** The localStorage key, shared with DashboardLayout's mount-time resolve. */
export const DENSITY_STORAGE_KEY = 'honryu-density';

/** The saved choice, or comfortable when absent/unknown -- never an OS guess. */
export function resolveDensity(): DensityMode {
  return localStorage.getItem(DENSITY_STORAGE_KEY) === 'compact' ? 'compact' : 'comfortable';
}

/** Mirrors DashboardLayout's applyTheme: the html class IS the mode. */
export function applyDensity(mode: DensityMode) {
  document.documentElement.classList.toggle('density-compact', mode === 'compact');
}

/** Row glyphs: the same three rows, spread for comfortable, tight for compact. */
function DensityGlyph({ tight }: { tight: boolean }) {
  // 16px, stroke currentColor so the option's text color carries the icon.
  const ys = tight ? [5.75, 8, 10.25] : [3.25, 8, 12.75];
  return (
    <svg
      viewBox="0 0 16 16"
      className="h-4 w-4 shrink-0"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.5}
      strokeLinecap="round"
      aria-hidden
    >
      {ys.map((y) => (
        <line key={y} x1="2.75" y1={y} x2="13.25" y2={y} />
      ))}
    </svg>
  );
}

const options: { mode: DensityMode; label: string; tight: boolean }[] = [
  { mode: 'comfortable', label: 'Comfortable', tight: false },
  { mode: 'compact', label: 'Compact', tight: true },
];

/**
 * The active option speaks the nav's active-link sky; inactive options
 * match the ghost-button slate, same contract as every segmented control
 * in the SPA (ReportsTrend's axis switch, Executions' engine filter).
 */
export default function DensityToggle() {
  const [mode, setMode] = useState<DensityMode>('comfortable');

  // Mount-time resolution: saved choice -> html class, before the first
  // user interaction. DashboardLayout ALSO resolves density in its own
  // mount effect (it gates first paint), which is idempotent -- both read
  // this same key through resolveDensity.
  useEffect(() => {
    const initial = resolveDensity();
    setMode(initial);
    applyDensity(initial);
  }, []);

  const select = (next: DensityMode) => {
    setMode(next);
    localStorage.setItem(DENSITY_STORAGE_KEY, next);
    applyDensity(next);
  };

  return (
    <div
      data-testid="density-toggle"
      role="group"
      aria-label="Density"
      className="flex gap-1 rounded-md border border-slate-200 p-1 dark:border-slate-700"
    >
      {options.map((opt) => {
        const active = mode === opt.mode;
        return (
          <button
            key={opt.mode}
            type="button"
            data-density={opt.mode}
            aria-pressed={active}
            onClick={() => select(opt.mode)}
            className={`flex min-h-[32px] items-center gap-1.5 rounded-md px-2.5 py-1 text-caption font-medium transition-colors duration-200 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 ${
              active
                ? 'bg-sky-50 text-sky-700 dark:bg-sky-900/30 dark:text-sky-300'
                : 'text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'
            }`}
          >
            <DensityGlyph tight={opt.tight} />
            {opt.label}
          </button>
        );
      })}
    </div>
  );
}
