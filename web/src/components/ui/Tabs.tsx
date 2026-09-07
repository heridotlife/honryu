// Accessible tab primitives (phase 28): a controlled Tabs strip and its
// TabPanel counterpart. All state lives with the caller -- Tabs takes
// `active` and reports changes through onChange, so pages can mirror the
// selection into the URL without syncing two sources of truth.
//
// The ARIA Authoring Practices wiring: the strip is a nav[role=tablist]
// of button[role=tab]s with id `tab-{id}` pointing at aria-controls
// `panel-{id}`; the matching TabPanel renders the other half of the pair
// (role=tabpanel, aria-labelledby back to the tab). Selection follows
// focus -- ArrowRight/ArrowLeft move selection (with wrap) and focus to
// the neighbouring tab; Enter/Space need no code because native buttons
// already turn them into clicks. Inactive panels keep rendering in the
// DOM behind the `hidden` attribute: tests and deep links address every
// panel without clicking first, and switching tabs costs no refetch.
import { useRef } from 'react';
import type { HTMLAttributes, KeyboardEvent, ReactNode } from 'react';

/** One entry of the tab strip: `id` names the panel it owns. */
export interface TabDef {
  id: string;
  label: string;
}

export interface TabsProps extends Omit<HTMLAttributes<HTMLElement>, 'onChange'> {
  tabs: TabDef[];
  /** The selected tab id (controlled: Tabs itself holds no state). */
  active: string;
  onChange: (id: string) => void;
}

export function Tabs({ tabs, active, onChange, className = '', ...rest }: TabsProps) {
  // Roving-tabindex focus target per slot; null after a slot's button unmounts.
  const buttonRefs = useRef<(HTMLButtonElement | null)[]>([]);

  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>, index: number) => {
    let next: number;
    if (e.key === 'ArrowRight') {
      next = (index + 1) % tabs.length;
    } else if (e.key === 'ArrowLeft') {
      next = (index - 1 + tabs.length) % tabs.length;
    } else {
      return;
    }
    e.preventDefault();
    onChange(tabs[next].id);
    buttonRefs.current[next]?.focus();
  };

  return (
    <nav
      role="tablist"
      aria-label="Tabs"
      {...rest}
      className={`flex flex-wrap items-end gap-1 border-b border-slate-200 dark:border-slate-700 ${className}`}
    >
      {tabs.map((tab, i) => {
        const isActive = tab.id === active;
        return (
          <button
            key={tab.id}
            ref={(el) => {
              buttonRefs.current[i] = el;
            }}
            type="button"
            role="tab"
            id={`tab-${tab.id}`}
            aria-selected={isActive}
            aria-controls={`panel-${tab.id}`}
            tabIndex={isActive ? 0 : -1}
            onClick={() => onChange(tab.id)}
            onKeyDown={(e) => onKeyDown(e, i)}
            className={`
              -mb-px rounded-t-md border-b-2 px-3 py-2 text-caption font-medium transition-colors
              focus:outline-none focus:ring-2 focus:ring-sky-500 focus:ring-offset-2
              ${
                isActive
                  ? 'border-sky-500 bg-slate-50 text-sky-600 dark:bg-slate-800 dark:text-sky-400'
                  : 'border-transparent text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200'
              }
            `}
          >
            {tab.label}
          </button>
        );
      })}
    </nav>
  );
}

export interface TabPanelProps extends HTMLAttributes<HTMLDivElement> {
  /** The panel's tab id -- must match a Tabs entry so the aria pair wires up. */
  id: string;
  /** The workspace's active tab id; any other value hides (not unmounts) this panel. */
  active: string;
  children: ReactNode;
}

export function TabPanel({ id, active, className = '', children, ...rest }: TabPanelProps) {
  return (
    <div
      role="tabpanel"
      id={`panel-${id}`}
      aria-labelledby={`tab-${id}`}
      hidden={active !== id}
      {...rest}
      className={className}
    >
      {children}
    </div>
  );
}
