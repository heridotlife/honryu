import { useEffect, useState } from 'react';
import Button from './ui/Button';
import Card, { CardContent, CardHeader, CardTitle } from './ui/Card';
import Input from './ui/Input';
import { ApiError } from '../api/client';
import { createWebhook, deleteWebhook, listWebhooks, setWebhookEnabled } from '../api/webhooks';
import type { Webhook } from '../api/webhooks';

export interface WebhooksCardProps {
  /** The project whose run-completion endpoints this card administers. */
  projectId: number;
}

/** URLs are receiver endpoints and get long; keep the host and tail visible. */
function truncateMiddle(url: string, max = 48): string {
  if (url.length <= max) {
    return url;
  }
  const keep = max - 1; // one character for the ellipsis
  const head = url.slice(0, Math.ceil(keep * 0.6));
  const tail = url.slice(url.length - Math.floor(keep * 0.4));
  return `${head}…${tail}`;
}

/**
 * The project's run-completion webhook registry (phase 40): list, register,
 * pause/resume, delete. Delivery itself is the backend's background fan-out
 * (phase 38); this card only edits the registry. The signing secret is
 * write-only -- it leaves the browser once, at registration, and is never
 * shown again.
 */
export default function WebhooksCard({ projectId }: WebhooksCardProps) {
  const [hooks, setHooks] = useState<Webhook[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [url, setUrl] = useState('');
  const [secret, setSecret] = useState('');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // The delete confirm is two-step inline: the first click arms the button,
  // the second (or a click anywhere else) disarms or deletes. No browser
  // dialogs in the SPA, the same restraint the rest of the surface keeps.
  const [confirmId, setConfirmId] = useState<number | null>(null);

  useEffect(() => {
    let alive = true;
    setHooks(null);
    setError(null);
    listWebhooks(projectId)
      .then(rows => {
        if (alive) setHooks(rows);
      })
      .catch((err: unknown) => {
        if (alive) setError(err instanceof ApiError ? err.message : 'failed to load webhooks');
      });
    return () => {
      alive = false;
    };
  }, [projectId]);

  const msg = (err: unknown, fallback: string): string => (err instanceof ApiError ? err.message : fallback);

  async function add(e: React.FormEvent): Promise<void> {
    e.preventDefault();
    setFormError(null);
    setBusy(true);
    try {
      const created = await createWebhook(projectId, url.trim(), secret !== '' ? secret : undefined);
      setHooks(prev => [...(prev ?? []), created]);
      setUrl('');
      setSecret('');
    } catch (err: unknown) {
      setFormError(msg(err, 'failed to register webhook'));
    } finally {
      setBusy(false);
    }
  }

  async function toggle(hook: Webhook): Promise<void> {
    setError(null);
    try {
      await setWebhookEnabled(projectId, hook.id, !hook.enabled);
      setHooks(prev => (prev ?? []).map(w => (w.id === hook.id ? { ...w, enabled: !w.enabled } : w)));
    } catch (err: unknown) {
      setError(msg(err, 'failed to update webhook'));
    }
  }

  async function remove(hook: Webhook): Promise<void> {
    setError(null);
    try {
      await deleteWebhook(projectId, hook.id);
      setHooks(prev => (prev ?? []).filter(w => w.id !== hook.id));
    } catch (err: unknown) {
      setError(msg(err, 'failed to delete webhook'));
    } finally {
      setConfirmId(null);
    }
  }

  return (
    <Card padding="none">
      <CardHeader className="px-4 pt-4 sm:px-6 sm:pt-6">
        <CardTitle>Webhooks</CardTitle>
        <p className="text-caption mt-1 text-slate-500 dark:text-slate-400">
          Delivers on every run completion (pass or fail). Signed with HMAC if a secret is set.
        </p>
      </CardHeader>
      <CardContent>
        {error && (
          <p className="text-body-sm px-4 pb-2 text-red-600 dark:text-red-400 sm:px-6" role="alert">
            {error}
          </p>
        )}
        {hooks === null ? (
          <p className="text-body-sm p-4 text-slate-500 dark:text-slate-400 sm:px-6">Loading webhooks…</p>
        ) : hooks.length === 0 ? (
          <p className="text-body-sm p-4 text-slate-500 dark:text-slate-400 sm:px-6">No webhooks registered.</p>
        ) : (
          <ul className="divide-y divide-slate-200 dark:divide-slate-700">
            {hooks.map(hook => (
              <li
                key={hook.id}
                data-testid={`webhook-row-${hook.id}`}
                className="flex items-center justify-between gap-3 p-4 sm:px-6"
              >
                <div className="min-w-0">
                  <p className="text-body-sm truncate font-medium text-slate-900 dark:text-slate-100" title={hook.url}>
                    {truncateMiddle(hook.url)}
                  </p>
                  <p className="text-caption text-slate-500 dark:text-slate-400">
                    {hook.has_secret ? 'signed · ' : ''}
                    {hook.created_by ? `${hook.created_by} · ` : ''}
                    {new Date(hook.created_time).toLocaleString()}
                  </p>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <button
                    type="button"
                    role="switch"
                    aria-checked={hook.enabled}
                    aria-label={hook.enabled ? 'Pause webhook' : 'Resume webhook'}
                    data-testid={`webhook-toggle-${hook.id}`}
                    onClick={() => void toggle(hook)}
                    className={`relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 focus:outline-none focus:ring-2 focus:ring-sky-500 focus:ring-offset-2 ${
                      hook.enabled ? 'bg-sky-600' : 'bg-slate-200 dark:bg-slate-600'
                    }`}
                  >
                    <span
                      aria-hidden="true"
                      className={`pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ${
                        hook.enabled ? 'translate-x-5' : 'translate-x-0'
                      }`}
                    />
                  </button>
                  <Button
                    size="sm"
                    variant={confirmId === hook.id ? 'primary' : 'outline'}
                    data-testid={`webhook-delete-${hook.id}`}
                    onClick={() => {
                      if (confirmId === hook.id) {
                        void remove(hook);
                      } else {
                        setConfirmId(hook.id);
                      }
                    }}
                  >
                    {confirmId === hook.id ? 'Confirm delete?' : 'Delete'}
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
        <form
          onSubmit={e => void add(e)}
          className="flex flex-col gap-2 border-t border-slate-200 p-4 dark:border-slate-700 sm:flex-row sm:items-start sm:px-6"
        >
          <div className="flex-1">
            <Input
              data-testid="webhook-url-input"
              type="url"
              placeholder="https://receiver.example.com/runs"
              value={url}
              onChange={e => setUrl(e.target.value)}
              aria-label="Webhook URL"
            />
          </div>
          <div className="flex-1">
            <Input
              data-testid="webhook-secret-input"
              type="password"
              placeholder="Secret (optional)"
              value={secret}
              onChange={e => setSecret(e.target.value)}
              aria-label="Webhook secret"
              autoComplete="off"
            />
          </div>
          <Button type="submit" data-testid="add-webhook-btn" disabled={busy || url.trim() === ''}>
            Add
          </Button>
        </form>
        {formError && (
          <p className="text-body-sm px-4 pb-4 text-red-600 dark:text-red-400 sm:px-6" role="alert">
            {formError}
          </p>
        )}
      </CardContent>
    </Card>
  );
}
