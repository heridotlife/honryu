// Breadcrumbs (phase 52): the small "Executions / #5 / Compare" trail for
// the execution detail and compare routes -- the audit's fix for those two
// pages being deep-linkable with no visible way back up. Only the pages
// that opt in render one; it is not a global nav surface. The trail is a
// labelled nav over a list, links for every ancestor, and the current page
// as plain text carrying aria-current="page".
import { Fragment } from 'react';
import { Link } from 'react-router-dom';
import { ChevronRight } from 'lucide-react';

export interface Crumb {
  label: string;
  /** Absent on the trail's last item (where you are), present on ancestors. */
  href?: string;
}

export default function Breadcrumbs({ items }: { items: Crumb[] }) {
  return (
    <nav aria-label="breadcrumb" data-testid="breadcrumbs">
      <ol className="flex flex-wrap items-center gap-1 text-caption text-slate-500 dark:text-slate-400">
        {items.map((item, i) => {
          const last = i === items.length - 1;
          return (
            <li key={`${item.label}-${i}`} className="flex items-center gap-1">
              {i > 0 && (
                <Fragment>
                  <ChevronRight aria-hidden className="h-3.5 w-3.5 shrink-0 text-slate-400 dark:text-slate-500" />
                </Fragment>
              )}
              {!last && item.href !== undefined ? (
                <Link
                  to={item.href}
                  className="rounded font-medium text-sky-600 hover:underline focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:text-sky-400"
                >
                  {item.label}
                </Link>
              ) : (
                <span aria-current={last ? 'page' : undefined} className="font-medium text-slate-700 dark:text-slate-300">
                  {item.label}
                </span>
              )}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}
