// The schedule drawer (phase 108): list, create, and delete an execution's
// time-triggered schedules over POST/GET/DELETE
// /api/executions/{id}/schedules -- the surface the backend has had since
// the schedule phase but no UI exposed. Create needs the tenant whose
// quota the occurrences reserve (a form field the handler demands); no
// execution response carries a tenant, so the drawer resolves it from the
// execution's project on open. Modal chrome is the shared ui/Modal (phase
// 77): focus trap, Escape, tap-away, focus returned to the opener.
import { useEffect, useState } from 'react';
import { CalendarClock, Clock, Trash2 } from 'lucide-react';
import Button from './ui/Button';
import EmptyState from './EmptyState';
import Input from './ui/Input';
import Modal from './ui/Modal';
import ActionErrorDetails from './ActionErrorDetails';
import { ApiError, errorDetails } from '../api/client';
import { getProjectsByProjectId } from '../api/generated';
import { createSchedule, deleteSchedule, listSchedules, type Schedule } from '../api/schedules';

/** The recurring presets (phase 108 spec): one tap fills the cron input;
 * Custom leaves it for the operator. The expressions are standard 5-field
 * crons -- the grammar the server's robfig ParseStandard enforces. */
const PRESETS: ReadonlyArray<{ label: string; cron: string }> = [
  { label: 'Hourly', cron: '0 * * * *' },
  { label: 'Daily', cron: '0 6 * * *' },
  { label: 'Weekly', cron: '0 6 * * 1' },
];

function formatTime(iso: string | undefined): string {
  if (iso === undefined) return '';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

/** The schedule's next still-reserved future fire time -- the one number
 * that answers "when does this next do anything". */
function nextFire(s: Schedule): string | undefined {
  const now = Date.now();
  return (s.occurrences ?? [])
    .filter((o) => o.status === 'reserved' && o.fire_time !== undefined && Date.parse(o.fire_time) > now)
    .map((o) => o.fire_time as string)
    .sort()[0];
}

export interface ScheduleModalProps {
  /** The execution whose schedules the drawer manages. */
  executionId: number;
  /** Display name for the header -- which execution is being scheduled. */
  executionName: string;
  /** The execution's project; the create form resolves the project's
   * tenant_id from it (the handler requires the form field, and no
   * execution response carries a tenant). */
  projectId: number;
  /** Closes the drawer; the parent owns the open state. */
  onClose: () => void;
  /** Called after a successful create or delete -- the parent's hook to
   * refetch whatever the mutation changed (the executions list itself does
   * not change: schedules are not part of an execution row). */
  onMutated: () => void;
}

export default function ScheduleModal({ executionId, executionName, projectId, onClose, onMutated }: ScheduleModalProps) {
  const [schedules, setSchedules] = useState<Schedule[] | null>(null);
  const [tenantId, setTenantId] = useState<number | null>(null);
  const [tenantResolved, setTenantResolved] = useState(false);
  const [kind, setKind] = useState<'one_shot' | 'recurring'>('recurring');
  const [fireAt, setFireAt] = useState('');
  const [recurrence, setRecurrence] = useState('');
  const [confirmingId, setConfirmingId] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  // The create/delete API error line (message + optional structured
  // details via ActionErrorDetails), kept separate from formError so a
  // server refusal never clears a half-filled form's field messages.
  const [error, setError] = useState<string | null>(null);
  const [errorPayload, setErrorPayload] = useState<Record<string, unknown> | null>(null);
  const [formError, setFormError] = useState<string | null>(null);

  const reload = async () => {
    try {
      setSchedules(await listSchedules(executionId));
    } catch (err) {
      setSchedules([]);
      setError(err instanceof ApiError ? err.message : 'Failed to load schedules.');
    }
  };

  // On open: load the schedule list and resolve the project's tenant. The
  // list is the drawer's substance; the tenant only feeds the create form,
  // but resolving it up front surfaces "this project has no tenant" before
  // the operator has typed anything.
  useEffect(() => {
    void reload();
    let alive = true;
    getProjectsByProjectId(projectId)
      .then((p) => {
        if (alive) setTenantId(typeof p.tenant_id === 'number' ? p.tenant_id : null);
      })
      .catch(() => {
        if (alive) setTenantId(null);
      })
      .finally(() => {
        if (alive) setTenantResolved(true);
      });
    return () => {
      alive = false;
    };
  }, [executionId, projectId]);

  const submit = async () => {
    setFormError(null);
    setError(null);
    setErrorPayload(null);
    if (kind === 'one_shot') {
      // The server does NOT reject a past fire_at -- it creates a schedule
      // with zero occurrences that silently never fires -- so the future
      // check here is the real gate, not decoration.
      if (fireAt.trim() === '') {
        setFormError('A fire-at time is required.');
        return;
      }
      const when = new Date(fireAt);
      if (Number.isNaN(when.getTime())) {
        setFormError('The fire-at time is not a valid date/time.');
        return;
      }
      if (when.getTime() <= Date.now()) {
        setFormError('The fire-at time must be in the future.');
        return;
      }
    } else {
      if (recurrence.trim() === '') {
        setFormError('A cron expression is required (pick a preset or enter one).');
        return;
      }
      // Convenience, not the gate: catch the classic 6-field (seconds)
      // cron before the round-trip. The server's robfig parser is the
      // authority and its error renders inline below when it disagrees.
      if (recurrence.trim().split(/\s+/).length !== 5) {
        setFormError('The cron expression must have 5 fields (minute hour day month weekday).');
        return;
      }
    }
    if (!tenantResolved || tenantId === null) {
      setFormError("This execution's project has no tenant, so its schedules cannot reserve quota.");
      return;
    }

    setBusy(true);
    try {
      await createSchedule(executionId, {
        tenantId,
        kind,
        // datetime-local carries no timezone; the instant is what the
        // operator's clock means, which toISOString() expresses in UTC.
        fireAt: kind === 'one_shot' ? new Date(fireAt).toISOString() : undefined,
        recurrence: kind === 'recurring' ? recurrence.trim() : undefined,
      });
      onMutated();
      // Success: show it in the list, not the form -- clear the fields the
      // created schedule superseded so the next one starts empty.
      setFireAt('');
      setRecurrence('');
      await reload();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to create schedule.');
      setErrorPayload(errorDetails(err));
    } finally {
      setBusy(false);
    }
  };

  const remove = async (scheduleId: number) => {
    setBusy(true);
    setError(null);
    setErrorPayload(null);
    try {
      await deleteSchedule(executionId, scheduleId);
      setConfirmingId(null);
      onMutated();
      await reload();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to delete schedule.');
      setErrorPayload(errorDetails(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={`Schedules — ${executionName}`}
      subtitle={'Time-triggered runs: fire once at an instant, or on a cron recurrence.'}
      onClose={onClose}
      closeLabel="Close schedule drawer"
      overlayTestId="schedule-overlay"
      dialogTestId="schedule-drawer"
    >
      <div className="space-y-5">
        {/* Existing schedules: what is already going to fire, each with its
            next still-reserved occurrence and a two-step delete (the v1
            edit path is delete + recreate). */}
        <section aria-label="Existing schedules">
          <p className="text-caption mb-2 font-medium text-slate-500 dark:text-slate-400">Existing</p>
          {schedules === null ? (
            <p className="text-body-sm text-slate-500 dark:text-slate-400">Loading…</p>
          ) : schedules.length === 0 ? (
            <EmptyState
              icon={<Clock aria-hidden className="size-6" />}
              title="No schedules"
              description="No schedules — this execution fires only when triggered manually."
              testId="schedule-empty"
            />
          ) : (
            <ul className="divide-y divide-slate-200 rounded-lg border border-slate-200 dark:divide-slate-700 dark:border-slate-700">
              {schedules.map((s) => {
                const next = nextFire(s);
                const reserved = (s.occurrences ?? []).filter((o) => o.status === 'reserved').length;
                return (
                  <li key={s.id} data-testid="schedule-row" className="flex flex-wrap items-center justify-between gap-2 px-3 py-2">
                    <div className="min-w-0">
                      <div className="flex flex-wrap items-center gap-2">
                        <span
                          data-testid="schedule-kind"
                          className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-caption font-medium ${
                            s.kind === 'one_shot'
                              ? 'bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300'
                              : 'bg-sky-100 text-sky-800 dark:bg-sky-900/40 dark:text-sky-300'
                          }`}
                        >
                          {s.kind === 'one_shot' ? 'One-shot' : 'Recurring'}
                        </span>
                        {/* The trigger itself: the instant, or the cron
                            expression verbatim (mono -- it is punctuation
                            the operator typed or a preset filled). */}
                        <span className="font-mono text-caption text-slate-900 dark:text-slate-100">
                          {s.kind === 'one_shot' ? formatTime(s.fire_at) : s.recurrence}
                        </span>
                      </div>
                      <p className="text-caption mt-1 text-slate-500 dark:text-slate-400">
                        {next !== undefined && (
                          <>
                            <CalendarClock aria-hidden className="mr-1 inline h-3.5 w-3.5 align-[-2px]" />
                            next fire {formatTime(next)}
                            {' · '}
                          </>
                        )}
                        {(s.occurrences ?? []).length === 0
                          ? 'no occurrences in the 7-day horizon'
                          : `${(s.occurrences ?? []).length} occurrence${(s.occurrences ?? []).length === 1 ? '' : 's'} · ${reserved} reserved`}
                      </p>
                    </div>
                    {/* Two-step delete: the first press arms, the second
                        confirms -- destructive actions read their cost. */}
                    {confirmingId === s.id ? (
                      <div className="flex items-center gap-2" data-testid={`schedule-confirm-${s.id}`}>
                        <Button
                          variant="destructive"
                          size="sm"
                          disabled={busy}
                          onClick={() => void remove(s.id as number)}
                          data-testid={`schedule-confirm-delete-${s.id}`}
                        >
                          Confirm delete
                        </Button>
                        <Button variant="ghost" size="sm" disabled={busy} onClick={() => setConfirmingId(null)} data-testid={`schedule-cancel-delete-${s.id}`}>
                          Cancel
                        </Button>
                      </div>
                    ) : (
                      <Button
                        variant="secondary"
                        size="sm"
                        disabled={busy}
                        onClick={() => setConfirmingId(s.id as number)}
                        data-testid={`schedule-delete-${s.id}`}
                        aria-label={`Delete schedule ${s.id}`}
                      >
                        <Trash2 aria-hidden className="h-3.5 w-3.5" />
                        Delete
                      </Button>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
        </section>

        {/* Create: kind segmented control, then the one field the chosen
            kind reads. noValidate -- the drawer's own guards render inline
            (testable) instead of browser bubbles. */}
        <section aria-label="Create a schedule">
          <p className="text-caption mb-2 font-medium text-slate-500 dark:text-slate-400">Create</p>
          <form
            className="space-y-3"
            noValidate
            onSubmit={(e) => {
              e.preventDefault();
              void submit();
            }}
          >
            <fieldset data-testid="schedule-kind-input">
              <legend className="mb-2 block text-sm font-medium text-slate-700 dark:text-slate-300">Kind</legend>
              <div className="flex overflow-hidden rounded-lg border border-slate-300 dark:border-slate-700">
                {(['one_shot', 'recurring'] as const).map((k) => (
                  <label
                    key={k}
                    className={`flex flex-1 cursor-pointer items-center justify-center gap-2 px-3 py-2 text-sm font-medium transition-colors focus-within:ring-2 focus-within:ring-inset focus-within:ring-sky-500 ${
                      kind === k
                        ? 'bg-sky-600 text-white'
                        : 'bg-white text-slate-600 hover:bg-slate-50 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800'
                    }`}
                  >
                    <input
                      type="radio"
                      name="schedule-kind"
                      value={k}
                      checked={kind === k}
                      onChange={() => setKind(k)}
                      className="sr-only"
                      data-testid={`schedule-kind-${k}`}
                    />
                    {k === 'one_shot' ? 'One-shot' : 'Recurring'}
                  </label>
                ))}
              </div>
            </fieldset>

            {kind === 'one_shot' ? (
              <Input
                label="Fire at"
                type="datetime-local"
                helperText="The run fires once, at this instant (your local time)."
                data-testid="schedule-fireat-input"
                value={fireAt}
                onChange={(e) => setFireAt(e.target.value)}
              />
            ) : (
              <div className="space-y-2">
                <Input
                  label="Cron expression"
                  type="text"
                  placeholder="0 6 * * *"
                  className="font-mono"
                  helperText="Standard 5 fields: minute hour day month weekday."
                  data-testid="schedule-cron-input"
                  value={recurrence}
                  onChange={(e) => setRecurrence(e.target.value)}
                />
                <div className="flex flex-wrap gap-2" role="group" aria-label="Cron presets">
                  {PRESETS.map((p) => (
                    <button
                      key={p.label}
                      type="button"
                      data-testid={`schedule-preset-${p.label.toLowerCase()}`}
                      aria-pressed={recurrence === p.cron}
                      onClick={() => setRecurrence(p.cron)}
                      className={`rounded-full px-3 py-1 text-caption font-medium transition-colors focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 ${
                        recurrence === p.cron
                          ? 'bg-sky-600 text-white'
                          : 'bg-slate-100 text-slate-600 hover:bg-slate-200 dark:bg-slate-700/50 dark:text-slate-300 dark:hover:bg-slate-700'
                      }`}
                    >
                      {p.label}
                      <span className="ml-1.5 font-mono opacity-70">{p.cron}</span>
                    </button>
                  ))}
                </div>
              </div>
            )}

            {formError && (
              <p className="text-sm text-red-600 dark:text-red-400" role="alert" data-testid="schedule-form-error">
                {formError}
              </p>
            )}
            {error && (
              <div role="alert">
                <p className="text-sm text-red-600 dark:text-red-400">{error}</p>
                <ActionErrorDetails details={errorPayload} />
              </div>
            )}

            <div className="flex items-center justify-end gap-2">
              <Button type="submit" disabled={busy} data-testid="schedule-create">
                {busy ? 'Working…' : 'Create schedule'}
              </Button>
            </div>
          </form>
        </section>

        <p className="text-caption text-slate-500 dark:text-slate-400">
          Occurrences reserve quota for the next 7 days; an occurrence that does not fit is marked rejected, not
          hidden. Editing is delete + recreate.
        </p>
      </div>
    </Modal>
  );
}
