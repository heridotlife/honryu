// The form-level error summary (phase 77): on a failed submit it renders
// at the form's top, takes focus (role=alert, focusable), and links each
// entry to its field -- keyboard repair is one Enter away, and the failure
// is announced, not just coloured. Entries without a fieldId are
// form-level messages (nothing to jump to).
import { useEffect, useRef } from 'react';
import { CircleAlert } from 'lucide-react';

export interface ErrorSummaryEntry {
  /** The linked field's element id; omitted for form-level messages. */
  fieldId?: string;
  /** What is wrong, in the operator's words. */
  message: string;
}

export interface ErrorSummaryProps {
  /** One entry per problem, in field order. Empty renders nothing. */
  entries: ErrorSummaryEntry[];
  /** The heading line above the entries. */
  title?: string;
  /** Test hook. */
  testId?: string;
}

export default function ErrorSummary({ entries, title = 'Fix these to continue:', testId }: ErrorSummaryProps) {
  const ref = useRef<HTMLDivElement | null>(null);
  const shown = entries.length > 0;

  // Focus moves to the summary when it appears -- the failed submit's
  // "you are here" for keyboard and screen-reader operators alike.
  useEffect(() => {
    if (shown) ref.current?.focus();
  }, [shown]);

  if (!shown) return null;

  return (
    <div
      ref={ref}
      role="alert"
      tabIndex={-1}
      data-testid={testId}
      className="rounded-lg border border-red-300 bg-red-50 p-3 focus:outline-none focus:ring-2 focus:ring-red-500 dark:border-red-800 dark:bg-red-950/40"
    >
      <p className="flex items-center gap-1.5 text-sm font-medium text-red-800 dark:text-red-200">
        <CircleAlert aria-hidden className="h-4 w-4 shrink-0" />
        {title}
      </p>
      <ul className="mt-1 list-disc space-y-0.5 pl-6">
        {entries.map(entry => (
          <li key={entry.message} className="text-sm text-red-700 dark:text-red-300">
            {entry.fieldId !== undefined ? (
              <a
                href={`#${entry.fieldId}`}
                className="underline focus:outline-none focus:ring-2 focus:ring-red-500"
                onClick={e => {
                  // The anchor names the target for middle-click/copy
                  // semantics; activation is a focus jump.
                  e.preventDefault();
                  document.getElementById(entry.fieldId!)?.focus();
                }}
              >
                {entry.message}
              </a>
            ) : (
              entry.message
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
