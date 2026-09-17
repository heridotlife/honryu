// One field's inline error (phase 77): icon + text, never colour alone,
// announced by screen readers via role=alert. Rendered next to the field
// it names, after blur or a failed submit (see useFieldValidation).
import { CircleAlert } from 'lucide-react';

export interface FieldErrorProps {
  /** The field's verdict, in the operator's words. */
  message: string;
  /** Optional element id (for aria-describedby wiring). */
  id?: string;
  /** Optional test hook. */
  testId?: string;
}

export default function FieldError({ message, id, testId }: FieldErrorProps) {
  return (
    <p
      id={id}
      role="alert"
      data-testid={testId}
      className="mt-1 flex items-start gap-1 text-caption text-red-600 dark:text-red-400"
    >
      <CircleAlert aria-hidden className="mt-0.5 h-3.5 w-3.5 shrink-0" />
      <span>{message}</span>
    </p>
  );
}
