// The global project switcher (phase 32): a compact nav control that
// scopes the operator's whole view to one project, k6-style. Selection
// lives in localStorage ("honryu.project": the project id, or "" for all)
// and is broadcast through a window event so the nav switcher and every
// page-level filter stay in sync within the tab.
import { useEffect, useState } from 'react';
import { Check, ChevronDown } from 'lucide-react';
import { listProjects } from '../api/projects';
import type { Project } from '../api/projects';

/** localStorage key holding the selected project id ("" = all projects). */
export const PROJECT_STORAGE_KEY = 'honryu.project';

/**
 * Window event fired on selection change. The native `storage` event only
 * fires in OTHER tabs, so this is how the nav switcher and the page
 * filters -- separate mounted consumers of the same key -- see each
 * other's changes without a context provider.
 */
const PROJECT_CHANGED_EVENT = 'honryu:project-changed';

/** What useProjectSelection hands its consumer. */
export interface ProjectSelection {
  /** Every project the caller may see; [] on error, never null. */
  projects: Project[];
  /** The effective selection: "" means all projects. */
  selectedId: string;
  /** The selected project's name, or "" when the list cannot resolve it yet. */
  selectedName: string;
  /** Persists a selection ("" clears to all) and syncs every consumer. */
  select: (id: string) => void;
}

/**
 * The shared project-selection state: reads the stored id once, fetches
 * the project list once (names for the switcher, and existence so a
 * stale stored id -- a project since deleted or shared from another
 * deployment -- filters nothing and falls back to all), and re-syncs
 * whenever any consumer selects. Pages and the switcher mount this
 * independently; localStorage keeps them one source of truth.
 */
export function useProjectSelection(): ProjectSelection {
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [storedId, setStoredId] = useState<string>(() => localStorage.getItem(PROJECT_STORAGE_KEY) ?? '');

  useEffect(() => {
    let alive = true;
    listProjects()
      .then((rows) => {
        if (alive) setProjects(rows);
      })
      .catch(() => {
        if (alive) setProjects([]);
      });
    return () => {
      alive = false;
    };
  }, []);

  // Other consumers' selections (the nav switcher, a page filter's clear
  // chip) land in localStorage first; re-read on the broadcast event.
  useEffect(() => {
    const sync = () => setStoredId(localStorage.getItem(PROJECT_STORAGE_KEY) ?? '');
    window.addEventListener(PROJECT_CHANGED_EVENT, sync);
    return () => window.removeEventListener(PROJECT_CHANGED_EVENT, sync);
  }, []);

  const select = (id: string) => {
    localStorage.setItem(PROJECT_STORAGE_KEY, id);
    window.dispatchEvent(new CustomEvent(PROJECT_CHANGED_EVENT));
  };

  // While the list loads the stored id filters optimistically (no unfiltered
  // flash on the page); once loaded, an id the caller cannot see normalizes
  // to all projects rather than filtering everything out.
  const known =
    projects === null || storedId === '' || projects.some((p) => String(p.id) === storedId);
  const selectedId = known ? storedId : '';
  const selectedName =
    selectedId === '' ? '' : (projects ?? []).find((p) => String(p.id) === selectedId)?.name ?? '';

  return { projects: projects ?? [], selectedId, selectedName, select };
}

/**
 * The nav control (phase 32): current project name (or "All projects"),
 * click to open a visible list of projects with an "All projects" entry
 * on top -- a popover menu, the way k6 does it, never a select input.
 * Hides itself while the list loads and when the caller has no projects
 * (or the fetch fails): a control that cannot switch anything is noise.
 */
export default function ProjectSwitcher({ onSelect }: { onSelect?: (id: string) => void }) {
  const { projects, selectedId, selectedName, select } = useProjectSelection();
  const [open, setOpen] = useState(false);

  // Tap-away and Escape close, the mobile drawer's pattern: listeners
  // exist only while open, and the root matches on its marker class so
  // the toggle button is excluded from the outside-click check.
  useEffect(() => {
    if (!open) {
      return;
    }
    const handleClickOutside = (event: MouseEvent) => {
      const target = event.target as Element | null;
      if (!target?.closest('.project-switcher')) {
        setOpen(false);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('mousedown', handleClickOutside);
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [open]);

  if (projects === null || projects.length === 0) {
    return null;
  }

  const choose = (id: string) => {
    select(id);
    onSelect?.(id);
    setOpen(false);
  };

  const optionClass = (selected: boolean) =>
    `flex w-full items-center justify-between gap-2 rounded px-3 py-2 text-left text-sm transition-colors ${
      selected
        ? 'bg-sky-50 font-medium text-sky-700 dark:bg-sky-900/30 dark:text-sky-300'
        : 'text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'
    } focus:outline-none focus:ring-2 focus:ring-inset focus:ring-sky-500`;

  return (
    <div className="project-switcher relative" data-testid="project-switcher">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="listbox"
        aria-expanded={open}
        title="Switch project"
        className="flex min-h-[44px] max-w-44 items-center gap-1 rounded-md px-2 py-2 text-sm font-medium text-slate-600 transition-colors hover:bg-slate-100 hover:text-sky-600 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:text-slate-300 dark:hover:bg-slate-800 dark:hover:text-sky-400"
      >
        <span className="truncate">{selectedName !== '' ? selectedName : 'All projects'}</span>
        <ChevronDown aria-hidden className={`h-4 w-4 shrink-0 transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>
      {open && (
        <div
          role="listbox"
          aria-label="Project"
          className="absolute right-0 top-full z-50 mt-2 w-56 rounded-lg border border-slate-200 bg-white py-1 shadow-lg dark:border-slate-800 dark:bg-slate-950"
        >
          <div className="px-3 pb-1 pt-2">
            <button
              type="button"
              role="option"
              aria-selected={selectedId === ''}
              data-testid="project-option-all"
              onClick={() => choose('')}
              className={optionClass(selectedId === '')}
            >
              <span>All projects</span>
              {selectedId === '' && <Check aria-hidden className="h-4 w-4 text-sky-600 dark:text-sky-400" />}
            </button>
          </div>
          <div className="max-h-72 overflow-y-auto px-3 pb-2 pt-1">
            {projects.map((p) => {
              const selected = String(p.id) === selectedId;
              return (
                <button
                  key={p.id}
                  type="button"
                  role="option"
                  aria-selected={selected}
                  data-testid={`project-option-${p.id}`}
                  onClick={() => choose(String(p.id))}
                  className={optionClass(selected)}
                >
                  <span className="truncate">{p.name}</span>
                  {selected && <Check aria-hidden className="h-4 w-4 shrink-0 text-sky-600 dark:text-sky-400" />}
                </button>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}
