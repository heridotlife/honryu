// Field-level validation state (phase 77): the shared half of "validate on
// blur" -- touched-per-field plus a submit-attempted flag -- so every form
// shows a field's error the same way: once the operator has left the field
// (or tried to submit), not while they are still typing into it. The
// VALUES and the validators stay in the components; this hook only
// decides WHEN an error is allowed to render.
import { useCallback, useState } from 'react';

export interface UseFieldValidation {
  /** Marks a field touched; call from the input's onBlur. */
  blur: (field: string) => void;
  /** Marks the form submit-attempted: after a failed submit every field's
   * error renders regardless of blur (the summary's backstop). */
  markSubmitted: () => void;
  /** True once the form has been submitted (and not reset). */
  submitted: boolean;
  /** The error to render for a field: the message only after blur or a
   * submit attempt, null before -- typing feedback stays quiet. */
  visibleError: (field: string, error: string | null) => string | null;
  /** Clears touched + submitted state (after a successful save). */
  reset: () => void;
}

export function useFieldValidation(): UseFieldValidation {
  const [touched, setTouched] = useState<Record<string, boolean>>({});
  const [submitted, setSubmitted] = useState(false);

  const blur = useCallback((field: string) => {
    setTouched(prev => ({ ...prev, [field]: true }));
  }, []);

  const markSubmitted = useCallback(() => setSubmitted(true), []);

  const reset = useCallback(() => {
    setTouched({});
    setSubmitted(false);
  }, []);

  const visibleError = useCallback(
    (field: string, error: string | null): string | null => {
      if (error === null) return null;
      return touched[field] === true || submitted ? error : null;
    },
    [touched, submitted]
  );

  return { blur, markSubmitted, submitted, visibleError, reset };
}
