// The SPA's one dialog (phase 77): overlay + dialog chrome with the
// accessibility plumbing every dialog needs and no caller should re-derive
// -- focus moves to the first focusable element on open, Tab/Shift+Tab
// cycle inside the dialog (the trap the phase 34/39 dialogs lacked: Tab
// used to escape into the page behind), Escape closes, a tap on the
// backdrop closes, and on close focus returns to the element that opened
// the dialog. The title is labelled via aria-labelledby so screen readers
// announce the dialog by its heading.
import { useEffect, useId, useRef } from 'react';
import type { MouseEvent as ReactMouseEvent, ReactNode } from 'react';
import { X } from 'lucide-react';

/** The selectors Tab may cycle through, in DOM order. Visibility is NOT
 * filtered: hidden-in-CSS focusables are a pre-existing possibility no
 * dialog here has, and layout-based filtering (offsetParent) reads as null
 * under jsdom, which would break the trap's tests. */
const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(', ');

export interface ModalProps {
  /** The dialog's heading; doubles as its accessible name via
   * aria-labelledby. */
  title: ReactNode;
  /** Optional line under the title (the two existing dialogs' tagline). */
  subtitle?: ReactNode;
  /** Closes the dialog; the parent owns the open state. */
  onClose: () => void;
  /** Everything below the header row. */
  children: ReactNode;
  /** The close button's aria-label -- name WHAT is being closed. */
  closeLabel?: string;
  /** Backdrop testid (the tap-away target). */
  overlayTestId?: string;
  /** The dialog element's testid. */
  dialogTestId?: string;
}

export default function Modal({
  title,
  subtitle,
  onClose,
  children,
  closeLabel = 'Close dialog',
  overlayTestId,
  dialogTestId,
}: ModalProps) {
  const labelledById = useId();
  const dialogRef = useRef<HTMLDivElement | null>(null);
  // The element focus returns to on close, captured before the first
  // focusable steals it.
  const openerRef = useRef<HTMLElement | null>(null);

  // On open: remember the opener, move focus into the dialog (first
  // focusable; the dialog itself when it has none). On close: hand focus
  // back -- keyboard operators land where they left the page.
  useEffect(() => {
    openerRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const dialog = dialogRef.current;
    const first = dialog?.querySelector<HTMLElement>(FOCUSABLE) ?? null;
    (first ?? dialog)?.focus();
    return () => {
      const opener = openerRef.current;
      if (opener !== null && document.contains(opener)) {
        opener.focus();
      }
    };
  }, []);

  // Escape closes (the SPA's dialog convention); Tab is trapped: from the
  // last focusable it wraps to the first, Shift+Tab from the first wraps
  // to the last, and focus that somehow left the dialog is pulled back in.
  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') {
        onClose();
        return;
      }
      if (event.key !== 'Tab') {
        return;
      }
      const dialog = dialogRef.current;
      if (dialog === null) {
        return;
      }
      const focusables = Array.from(dialog.querySelectorAll<HTMLElement>(FOCUSABLE));
      if (focusables.length === 0) {
        event.preventDefault();
        return;
      }
      const current = document.activeElement;
      const index = current instanceof HTMLElement ? focusables.indexOf(current) : -1;
      if (event.shiftKey) {
        if (index <= 0) {
          event.preventDefault();
          focusables[focusables.length - 1].focus();
        }
      } else if (index === -1 || index === focusables.length - 1) {
        event.preventDefault();
        focusables[0].focus();
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [onClose]);

  return (
    // The overlay is the tap-away target; only a press on the backdrop
    // itself (not the dialog) closes, so selecting text inside never does.
    <div
      data-testid={overlayTestId}
      className="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4"
      onMouseDown={(e: ReactMouseEvent<HTMLDivElement>) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={labelledById}
        tabIndex={-1}
        data-testid={dialogTestId}
        className="max-h-[85vh] w-full max-w-xl overflow-y-auto rounded-xl bg-white p-6 shadow-2xl dark:bg-slate-900"
      >
        <div className="mb-4 flex items-start justify-between gap-4">
          <div>
            <h2 id={labelledById} className="text-heading-md text-slate-900 dark:text-white">
              {title}
            </h2>
            {subtitle !== undefined && (
              <p className="text-caption mt-1 text-slate-500 dark:text-slate-400">{subtitle}</p>
            )}
          </div>
          <button
            type="button"
            aria-label={closeLabel}
            onClick={onClose}
            className="rounded p-1 text-slate-500 transition-colors hover:bg-slate-100 focus:outline-none focus:ring-2 focus:ring-sky-500 dark:text-slate-400 dark:hover:bg-slate-800"
          >
            <X aria-hidden className="h-5 w-5" />
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}
