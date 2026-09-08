// The share dialog (phase 34): issue, list, and revoke token-gated public
// links to one run's report. Anyone holding a link reads that report with
// no session at all, so the dialog's job is to make the link's reach
// legible: what it opens, when (if ever) it dies, and how to kill it.
// Copy mechanics are CopyButton's copyText, reused rather than reimplemented;
// the modal chrome follows the compare picker's overlay/tap-away/Escape
// conventions (the SPA has no generic Modal -- this is its first dialog).
import { useEffect, useState } from 'react';
import { Check, Share2, TriangleAlert, X } from 'lucide-react';
import Button from './ui/Button';
import { copyText } from './ui/CopyButton';
import { ApiError } from '../api/client';
import { listShares, revokeShare, shareLinkUrl, shareRun } from '../api/reports';
import type { ShareLink, ShareLinkInfo } from '../api/reports';

/** The expiry picker's choices; hours undefined mints a never-expiring link. */
const EXPIRY_OPTIONS: ReadonlyArray<{ value: string; label: string; hours?: number }> = [
  { value: 'never', label: 'Never expires' },
  { value: '24', label: 'Expires in 1 day', hours: 24 },
  { value: '168', label: 'Expires in 7 days', hours: 168 },
  { value: '720', label: 'Expires in 30 days', hours: 720 },
];

function formatTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

export interface ShareRunModalProps {
  /** The run whose report the links open. */
  runId: number;
  /** Closes the dialog; the parent owns the open state. */
  onClose: () => void;
}

export default function ShareRunModal({ runId, onClose }: ShareRunModalProps) {
  const [links, setLinks] = useState<ShareLinkInfo[] | null>(null);
  const [generated, setGenerated] = useState<ShareLink | null>(null);
  const [expiry, setExpiry] = useState('never');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const reload = async () => {
    try {
      setLinks(await listShares(runId));
    } catch (err) {
      setLinks([]);
      setError(err instanceof ApiError ? err.message : 'Failed to load share links.');
    }
  };

  // Existing links load once per open: the list is what makes the link's
  // reach legible ("what is already out in the world"), not a live feed.
  useEffect(() => {
    void reload();
  }, [runId]);

  // Escape closes, the compare picker's convention.
  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [onClose]);

  const generate = async () => {
    const option = EXPIRY_OPTIONS.find((o) => o.value === expiry);
    setBusy(true);
    setError(null);
    setCopied(false);
    try {
      setGenerated(await shareRun(runId, option?.hours));
      await reload();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to create share link.');
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (token: string) => {
    setBusy(true);
    setError(null);
    try {
      await revokeShare(runId, token);
      if (generated?.token === token) setGenerated(null);
      await reload();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to revoke share link.');
    } finally {
      setBusy(false);
    }
  };

  const copy = async () => {
    if (generated === null) return;
    setCopied(await copyText(shareLinkUrl(generated.token)));
  };

  return (
    // The overlay is the tap-away target; only a tap on the backdrop itself
    // (not the dialog) closes, so selecting the link text never does.
    <div
      data-testid="share-modal-overlay"
      className="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Share run report"
        data-testid="share-modal"
        className="max-h-[85vh] w-full max-w-xl overflow-y-auto rounded-xl bg-white p-6 shadow-2xl dark:bg-slate-900"
      >
        <div className="mb-4 flex items-start justify-between gap-4">
          <div>
            <h2 className="text-heading-md text-slate-900 dark:text-white">Share run #{runId}</h2>
            <p className="text-caption mt-1 text-slate-500 dark:text-slate-400">
              Anyone with the link views this run&apos;s report — no sign-in needed.
            </p>
          </div>
          <button
            type="button"
            aria-label="Close share dialog"
            onClick={onClose}
            className="rounded p-1 text-slate-500 transition-colors hover:bg-slate-100 focus:outline-none focus:ring-2 focus:ring-sky-500 dark:text-slate-400 dark:hover:bg-slate-800"
          >
            <X aria-hidden className="h-5 w-5" />
          </button>
        </div>

        <div className="space-y-4">
          {/* Issue: expiry choice + generate. The generated link reads as an
              absolute URL -- what actually gets pasted to a customer. */}
          <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
            <div className="flex-1">
              <label
                htmlFor="share-expiry"
                className="mb-2 block text-sm font-medium text-slate-700 dark:text-slate-300"
              >
                Link lifetime
              </label>
              <select
                id="share-expiry"
                data-testid="share-expiry-select"
                value={expiry}
                onChange={(e) => setExpiry(e.target.value)}
                className="block w-full min-h-[44px] rounded-lg border border-slate-300 bg-white px-3 py-2 text-base text-slate-900 transition-colors focus:border-sky-500 focus:ring-2 focus:ring-sky-500 focus:outline-none dark:border-slate-700 dark:bg-slate-900 dark:text-white"
              >
                {EXPIRY_OPTIONS.map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label}
                  </option>
                ))}
              </select>
            </div>
            <Button type="button" onClick={() => void generate()} disabled={busy} data-testid="generate-share-btn">
              {busy ? 'Working…' : 'Generate link'}
            </Button>
          </div>

          {generated !== null && (
            <div className="rounded-lg border border-slate-200 p-3 dark:border-slate-700" data-testid="share-generated">
              <div className="flex flex-wrap items-center gap-2">
                <code className="min-w-0 flex-1 overflow-x-auto rounded bg-slate-100 px-2 py-1 font-mono text-caption break-all text-slate-900 dark:bg-slate-950 dark:text-slate-100">
                  {shareLinkUrl(generated.token)}
                </code>
                <button
                  type="button"
                  data-testid="copy-share-url"
                  onClick={() => void copy()}
                  className="inline-flex min-h-[32px] items-center gap-1 rounded-md border border-slate-300 px-2 py-1 text-caption font-medium text-slate-600 transition-colors hover:bg-slate-100 focus:outline-none focus:ring-2 focus:ring-sky-500 dark:border-slate-600 dark:text-slate-300 dark:hover:bg-slate-800"
                >
                  {copied ? (
                    <Check aria-hidden className="h-3.5 w-3.5" />
                  ) : (
                    <Share2 aria-hidden className="h-3.5 w-3.5" />
                  )}
                  <span aria-live="polite">{copied ? 'Copied' : 'Copy link'}</span>
                </button>
              </div>
              <p className="text-caption mt-2 text-slate-500 dark:text-slate-400">
                {generated.expires_at
                  ? `Expires ${formatTime(generated.expires_at)}.`
                  : 'Never expires; revoke it here when it should stop working.'}
              </p>
            </div>
          )}

          {error && (
            <p className="text-sm text-red-600 dark:text-red-400" role="alert">
              {error}
            </p>
          )}

          {/* Existing links: what is already out in the world, each revocable
              on its own -- one customer's revoke must never break another's. */}
          <div>
            <p className="text-caption mb-2 font-medium text-slate-500 dark:text-slate-400">
              Existing links
            </p>
            {links === null ? (
              <p className="text-body-sm text-slate-500 dark:text-slate-400">Loading…</p>
            ) : links.length === 0 ? (
              <p className="text-body-sm text-slate-500 dark:text-slate-400">
                No links yet. Generate one to share this report.
              </p>
            ) : (
              <ul className="divide-y divide-slate-200 rounded-lg border border-slate-200 dark:divide-slate-700 dark:border-slate-700">
                {links.map((l) => (
                  <li key={l.token} className="flex flex-wrap items-center justify-between gap-2 px-3 py-2">
                    <div className="min-w-0" data-testid={`share-link-${l.token}`}>
                      <p className="truncate font-mono text-caption text-slate-700 dark:text-slate-300" title={l.token}>
                        …{l.token.slice(-8)}
                      </p>
                      <p className="text-caption text-slate-500 dark:text-slate-400">
                        created {formatTime(l.created_time)}
                        {l.created_by ? ` by ${l.created_by}` : ''}
                        {' · '}
                        {l.expires_at ? `expires ${formatTime(l.expires_at)}` : 'never expires'}
                      </p>
                    </div>
                    <Button
                      variant="secondary"
                      size="sm"
                      disabled={busy}
                      onClick={() => void revoke(l.token)}
                      data-testid={`revoke-share-${l.token}`}
                    >
                      Revoke
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </div>

          <p className="flex items-start gap-1.5 text-caption text-slate-500 dark:text-slate-400">
            <TriangleAlert aria-hidden className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            A link is a credential: anyone holding it reads this report until it expires or is revoked.
          </p>
        </div>
      </div>
    </div>
  );
}
