// The scenario's edit history (phase 80): the append-only version list the
// backend captures on every write. Collapsed by default -- the history is
// an audit surface, not the editor's main act. Rows show number, time, and
// actor; expanding a row lazily fetches that version's snapshot and shows
// its fields (a diff-lite record, not a visual diff). Restore demands an
// explicit confirmation naming the version -- restoring is a rewind, never
// a silent overwrite -- and after it lands the scenario state is refetched
// by the parent (the pre-restore state rides along as a new version, so
// the history itself stays append-only).
import { useEffect, useState } from 'react';
import { ChevronDown, ChevronRight, History, RotateCcw } from 'lucide-react';
import Button from './ui/Button';
import { ApiError } from '../api/client';
import {
  getScenarioVersion,
  listScenarioVersions,
  restoreScenarioVersion,
  type ScenarioSnapshot,
  type ScenarioVersion,
} from '../api/scenarios';
import { formatRowTime } from '../lib/executionRow';
import { useSession } from '../hooks/useSession';

interface ScenarioVersionHistoryProps {
  scenarioId: number;
  /** Called after a restore lands, so the page refetches the scenario. */
  onRestored?: () => void;
}

/** The snapshot field-list for one expanded version: what the scenario
 * looked like, field by field. Absent things say so honestly. */
function SnapshotFields({ snapshot }: { snapshot: ScenarioSnapshot }) {
  return (
    <dl className="grid grid-cols-[minmax(6rem,auto)_1fr] gap-x-3 gap-y-1 text-caption" data-testid={`version-snapshot-fields`}>
      <dt className="text-slate-500 dark:text-slate-400">Name</dt>
      <dd className="font-medium text-slate-900 dark:text-white">{snapshot.name}</dd>
      <dt className="text-slate-500 dark:text-slate-400">Kind</dt>
      <dd className="text-slate-700 dark:text-slate-300">
        {snapshot.kind}
        {snapshot.kind === 'native' && snapshot.engine !== '' ? ` · ${snapshot.engine}` : ''}
      </dd>
      {snapshot.isTemplate && (
        <>
          <dt className="text-slate-500 dark:text-slate-400">Template</dt>
          <dd className="text-slate-700 dark:text-slate-300">{snapshot.templateName}</dd>
        </>
      )}
      <dt className="text-slate-500 dark:text-slate-400">Test file</dt>
      <dd className="text-slate-700 dark:text-slate-300">{snapshot.testFile || 'none'}</dd>
      <dt className="text-slate-500 dark:text-slate-400">Data files</dt>
      <dd className="text-slate-700 dark:text-slate-300">
        {snapshot.data.length === 0 ? 'none' : snapshot.data.join(', ')}
      </dd>
      <dt className="text-slate-500 dark:text-slate-400">Requests</dt>
      <dd className="text-slate-700 dark:text-slate-300">
        {snapshot.requests ? 'stored fragment' : 'none'}
      </dd>
    </dl>
  );
}

export default function ScenarioVersionHistory({ scenarioId, onRestored }: ScenarioVersionHistoryProps) {
  const { can } = useSession();
  const [open, setOpen] = useState(false);
  const [versions, setVersions] = useState<ScenarioVersion[] | null>(null);
  const [listError, setListError] = useState<string | null>(null);

  // Which row is expanded (lazily fetches its snapshot), which row is
  // awaiting a named-version confirm, and what feedback/error shows.
  const [expanded, setExpanded] = useState<number | null>(null);
  const [snapshots, setSnapshots] = useState<Record<number, ScenarioSnapshot | null>>({});
  const [confirming, setConfirming] = useState<number | null>(null);
  const [restoring, setRestoring] = useState(false);
  const [restored, setRestored] = useState<{ restoredFrom: number; version: number } | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const load = () => {
    setListError(null);
    listScenarioVersions(scenarioId)
      .then((rows) => setVersions(rows))
      .catch((err: unknown) => {
        setListError(err instanceof ApiError ? err.message : 'Failed to load version history.');
      });
  };

  useEffect(() => {
    if (!open) {
      return;
    }
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, scenarioId]);

  const expand = (version: number) => {
    if (expanded === version) {
      setExpanded(null);
      return;
    }
    setExpanded(version);
    if (snapshots[version] !== undefined) {
      return;
    }
    getScenarioVersion(scenarioId, version)
      .then((snap) => setSnapshots((prev) => ({ ...prev, [version]: snap })))
      .catch(() => setSnapshots((prev) => ({ ...prev, [version]: null })));
  };

  const doRestore = (version: number) => {
    setRestoring(true);
    setActionError(null);
    restoreScenarioVersion(scenarioId, version)
      .then((res) => {
        setConfirming(null);
        setRestored(res);
        // The list gains the pre-restore capture; refetch it, and let the
        // page refetch the scenario itself.
        load();
        onRestored?.();
      })
      .catch((err: unknown) => {
        setActionError(err instanceof ApiError ? err.message : 'Restore failed.');
      })
      .finally(() => setRestoring(false));
  };

  const mayRestore = can('scenario', 'update');

  return (
    <div data-testid="version-history-card">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <button
          type="button"
          data-testid="version-history-toggle"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
          className="flex items-center gap-1.5 text-body-sm font-semibold text-slate-900 hover:underline focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:text-white"
        >
          <History className="size-4" aria-hidden />
          Version history
          {open ? <ChevronDown className="size-4" aria-hidden /> : <ChevronRight className="size-4" aria-hidden />}
        </button>
      </div>

      {!open ? null : listError !== null ? (
        <p className="mt-2 text-sm text-red-600 dark:text-red-400" role="alert" data-testid="version-history-error">
          {listError}
        </p>
      ) : versions === null ? (
        <div className="mt-2 space-y-2" data-testid="version-history-loading">
          <div className="h-8 animate-pulse rounded bg-slate-100 dark:bg-slate-700/50" />
        </div>
      ) : (
        <div className="mt-2" data-testid="version-history">
          {restored !== null && (
            <p
              className="mb-2 rounded-lg border border-emerald-200 bg-emerald-50 px-3 py-2 text-body-sm text-emerald-800 dark:border-emerald-800 dark:bg-emerald-950/30 dark:text-emerald-200"
              role="status"
              data-testid="version-restored"
            >
              Restored to version {restored.restoredFrom}. The overwritten state is kept as version{' '}
              {restored.version}.
            </p>
          )}
          {actionError !== null && (
            <p className="mb-2 text-sm text-red-600 dark:text-red-400" role="alert" data-testid="version-action-error">
              {actionError}
            </p>
          )}
          {versions.length === 0 ? (
            <p className="text-body-sm text-slate-500 dark:text-slate-400" data-testid="version-history-empty">
              No versions recorded yet.
            </p>
          ) : (
            <ul className="divide-y divide-slate-100 rounded-lg border border-slate-200 dark:divide-slate-800 dark:border-slate-700">
              {versions.map((v) => (
                <li key={v.id} data-testid={`version-row-${v.version}`} className="px-3 py-2">
                  <div className="flex flex-wrap items-center gap-2">
                    <button
                      type="button"
                      data-testid={`version-expand-${v.version}`}
                      aria-expanded={expanded === v.version}
                      onClick={() => expand(v.version)}
                      className="flex items-center gap-1.5 text-body-sm font-medium text-sky-600 hover:underline focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:text-sky-400"
                    >
                      {expanded === v.version ? (
                        <ChevronDown className="size-4" aria-hidden />
                      ) : (
                        <ChevronRight className="size-4" aria-hidden />
                      )}
                      Version {v.version}
                    </button>
                    <span className="text-caption text-slate-500 dark:text-slate-400">
                      {formatRowTime(v.createdTime)}
                      {' · '}
                      {v.createdBy ?? 'unknown'}
                    </span>
                    {mayRestore && (
                      <Button
                        variant="outline"
                        size="sm"
                        className="ml-auto"
                        data-testid={`version-restore-${v.version}`}
                        onClick={() => {
                          setRestored(null);
                          setActionError(null);
                          setConfirming(v.version);
                        }}
                      >
                        <RotateCcw className="size-3.5" aria-hidden />
                        Restore
                      </Button>
                    )}
                  </div>
                  {expanded === v.version && (
                    <div className="mt-2 rounded bg-slate-50 px-3 py-2 dark:bg-slate-800/60" data-testid={`version-snapshot-${v.version}`}>
                      {snapshots[v.version] === undefined ? (
                        <p className="text-caption text-slate-500 dark:text-slate-400">Loading snapshot…</p>
                      ) : snapshots[v.version] === null ? (
                        <p className="text-caption text-red-600 dark:text-red-400">Snapshot could not be loaded.</p>
                      ) : (
                        <SnapshotFields snapshot={snapshots[v.version] as ScenarioSnapshot} />
                      )}
                    </div>
                  )}
                </li>
              ))}
            </ul>
          )}

          {confirming !== null && (
            <div
              className="mt-2 rounded-lg border border-amber-300 bg-amber-50 p-4 text-sm text-amber-900 dark:border-amber-700 dark:bg-amber-900/30 dark:text-amber-200"
              role="alertdialog"
              aria-label={`Restore version ${confirming}`}
              data-testid="version-confirm"
            >
              <p className="font-medium">Restore this scenario to version {confirming}?</p>
              <p className="mt-1">
                The scenario will be rewound to how it looked at version {confirming}. The current state is not lost:
                it is kept as a new version. History is never overwritten.
              </p>
              <div className="mt-3 flex gap-2">
                <Button
                  data-testid="version-confirm-restore"
                  onClick={() => doRestore(confirming)}
                  disabled={restoring}
                >
                  {restoring ? 'Restoring…' : `Restore version ${confirming}`}
                </Button>
                <Button variant="outline" data-testid="version-confirm-cancel" onClick={() => setConfirming(null)}>
                  Cancel
                </Button>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
