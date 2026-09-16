// The shared empty state (phase 76): every "nothing here yet" surface
// renders this instead of a bare sentence, so first-run guidance reads the
// same across the SPA. Three slots, never more: an optional icon (the
// slot's content is decorative -- meaning lives in the text, so the state
// is never color- or glyph-only), a title, a one-line description, and AT
// MOST ONE primary action. The single-action rule is the component's whole
// point: an empty state answers "what do I do now" with exactly one next
// step -- a Link when the step is a route (`to`), a Button when it is a
// behavior (`onClick`). No action prop renders no control: a message-only
// empty state is honest where the only conceivable button would be dead
// (e.g. flows the SPA deliberately does not offer).
import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import Button from './ui/Button';

export interface EmptyStateAction {
  label: string;
  /** Route href: renders a Link (needs a Router ancestor, which every page has). */
  to?: string;
  /** Behavior: renders a Button. Ignored when `to` is set -- the route wins. */
  onClick?: () => void;
}

export interface EmptyStateProps {
  /** Decorative icon (lucide, sized ~size-6). Meaning must never ride on it alone. */
  icon?: ReactNode;
  title: string;
  /** One line of guidance -- why it's empty and/or what happens next. */
  description?: string;
  /** The ONE primary action, or none (message-only). */
  action?: EmptyStateAction;
  /** Page-level test id (defaults to the generic empty-state marker). */
  testId?: string;
}

/** Link styling mirrors ui/Button's primary variant so the two action
 * flavors read as one control family; Button itself cannot carry an href. */
const actionLinkClass =
  'inline-flex min-h-[44px] items-center justify-center rounded-lg bg-gradient-to-r from-sky-500 to-cyan-500 px-4 py-2.5 text-base font-medium text-white shadow-md transition-all duration-200 hover:from-sky-600 hover:to-cyan-600 hover:shadow-lg focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:from-sky-600 dark:to-cyan-600 dark:hover:from-sky-700 dark:hover:to-cyan-700';

export default function EmptyState({ icon, title, description, action, testId = 'empty-state' }: EmptyStateProps) {
  return (
    <div data-testid={testId} className="flex flex-col items-center justify-center gap-3 px-6 py-12 text-center">
      {icon && (
        <div
          aria-hidden="true"
          className="flex size-12 items-center justify-center rounded-full bg-slate-100 text-slate-400 dark:bg-slate-800 dark:text-slate-500"
        >
          {icon}
        </div>
      )}
      <p className="text-body-sm font-medium text-slate-900 dark:text-white" data-testid={`${testId}-title`}>
        {title}
      </p>
      {description && (
        <p className="text-body-sm max-w-md text-slate-500 dark:text-slate-400" data-testid={`${testId}-description`}>
          {description}
        </p>
      )}
      {action &&
        (action.to ? (
          <Link to={action.to} className={actionLinkClass} data-testid={`${testId}-action`}>
            {action.label}
          </Link>
        ) : (
          <Button onClick={action.onClick} data-testid={`${testId}-action`}>
            {action.label}
          </Button>
        ))}
    </div>
  );
}
