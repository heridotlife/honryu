// The mode-aware configuration card (phase 90): renders the mode entries'
// resolved numbers with their derivation notes (Q8), and offers the Simple
// re-apply form — the "calibrate, then re-configure" loop's closing half.
// Phase 91 adds the two halves the snapshot model needed: a Re-resolve
// action (POST /config/re-resolve) that re-runs every mode entry against
// the CURRENT calibration and shows the per-entry old → new diff, and
// multi-entry rendering for multi-scenario Simple configs (the re-apply
// form stays single-scenario and is hidden for multi-entry configs — its
// PUT replaces the whole profile and would drop the other entries).
// Mounted like ExecutionConfigCard (idle executions only — the same guard
// the config edit has); renders nothing for a config with no mode
// provenance, so Advanced executions keep the ordinary card untouched.
import { useEffect, useState } from 'react';
import Card, { CardHeader, CardTitle, CardContent } from './ui/Card';
import Button from './ui/Button';
import ActionErrorDetails from './ActionErrorDetails';
import ModeForm from './ModeForm';
import {
  getExecutionConfig,
  putExecutionConfig,
  reResolveExecutionConfig,
  type ConfigTest,
  type ReResolveEntry,
} from '../api/executionsConfig';
import { getCapacityProfile } from '../api/calibration';
import {
  buildModeTest,
  modeChipLabel,
  modeDerivationLines,
  modeFormValid,
  secondsToModeForm,
  type ModeFormValue,
} from '../lib/modeConfig';
import { ApiError, errorDetails } from '../api/client';

interface Props {
  executionId: number;
  canUpdate: boolean;
  /** The capacity key the page already resolved (engine + pod size). */
  capacityKey: { engine: string; cpu: string; memory: string };
}

/** Seconds -> {duration, unit} for prefilling the re-apply form (the
 *  shared inverse of durationSeconds, phase 94's extraction). */
const secondsToForm = secondsToModeForm;

/** The four numbers a re-resolve can move, one diff cell's "old → new". */
function diffCell(label: string, before: number, after: number): string {
  return `${label} ${before} → ${after}`;
}

export default function ModeConfigCard({ executionId, canUpdate, capacityKey }: Props) {
  const [tests, setTests] = useState<ConfigTest[] | null>(null);
  const [singleTest, setSingleTest] = useState(false);
  const [projectId, setProjectId] = useState(0);
  const [perPodQps, setPerPodQps] = useState<Record<number, number>>({});
  const [form, setForm] = useState<ModeFormValue | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [errorDetail, setErrorDetail] = useState<Record<string, unknown> | null>(null);
  const [appliedAt, setAppliedAt] = useState<string | null>(null);
  // The re-resolve flow: idle (no button click yet) → confirming (the
  // inline confirm bar) → the fetched diff replaces it.
  const [confirming, setConfirming] = useState(false);
  const [diff, setDiff] = useState<ReResolveEntry[] | null>(null);

  useEffect(() => {
    let alive = true;
    setTests(null);
    setPerPodQps({});
    setError(null);
    setErrorDetail(null);
    setAppliedAt(null);
    setConfirming(false);
    setDiff(null);
    getExecutionConfig(executionId)
      .then(cfg => {
        if (!alive) return;
        setProjectId(cfg.project_id);
        const modeTests = cfg.tests.filter(t => t.mode);
        if (modeTests.length === 0) {
          setTests(null);
          return;
        }
        setTests(modeTests);
        // The re-apply form is the phase-90 single-scenario surface: it
        // PUTs a one-test profile, so it must only appear when that IS the
        // whole config — otherwise the PUT would silently drop the other
        // entries.
        setSingleTest(cfg.tests.length === 1);
        setForm({
          mode: modeTests[0].mode as ModeFormValue['mode'],
          qps: modeTests[0].throughput ?? 0,
          ...secondsToForm(modeTests[0].duration),
        });
        // Best-effort enrichment of each entry's derivation note: the
        // profile's per-pod rate. Absent (never calibrated under this key
        // since, or fetch failed) degrades the note's wording, nothing else.
        for (const t of modeTests) {
          getCapacityProfile(t.scenario_id, capacityKey)
            .then(p => {
              if (alive) setPerPodQps(prev => ({ ...prev, [t.scenario_id]: p.per_pod_qps }));
            })
            .catch(() => {
              /* note degrades to the generic wording */
            });
        }
      })
      .catch((e: unknown) => {
        if (alive) setError(e instanceof ApiError ? e.message : 'failed to load config');
      });
    return () => {
      alive = false;
    };
    // capacityKey is the page's stable object per execution; form state
    // derives from the fetch, not from it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [executionId]);

  const apply = () => {
    const first = tests?.[0];
    if (!form || !first || !modeFormValid(form)) {
      return;
    }
    setBusy(true);
    setError(null);
    setErrorDetail(null);
    // Re-apply PUTs a fresh Simple statement over the same scenario: the
    // server re-resolves against the CURRENT capacity profile, which is
    // exactly the recalibration this loop exists to pick up.
    const cfg = {
      name: `execution-${executionId}`,
      project_id: projectId,
      execution_id: executionId,
      tests: [buildModeTest(first.name || 'test', first.scenario_id, form)],
    };
    putExecutionConfig(executionId, cfg)
      .then(() => getExecutionConfig(executionId))
      .then(fresh => {
        const modeTests = fresh.tests.filter(t => t.mode);
        if (modeTests.length > 0) {
          setTests(modeTests);
          setForm({
            mode: modeTests[0].mode as ModeFormValue['mode'],
            qps: modeTests[0].throughput ?? 0,
            ...secondsToForm(modeTests[0].duration),
          });
        }
        setAppliedAt(new Date().toLocaleTimeString());
      })
      .catch((e: unknown) => {
        setError(e instanceof ApiError ? e.message : 'failed to apply config');
        setErrorDetail(errorDetails(e));
      })
      .finally(() => setBusy(false));
  };

  // Re-resolve re-runs the STORED statements through the current
  // calibration server-side and answers with each entry's old/new numbers;
  // the diff panel is the confirmation of what changed. A 409 (profile
  // missing/stale since the save) surfaces inline with the structured
  // remediation — nothing was persisted then.
  const reResolve = () => {
    setBusy(true);
    setError(null);
    setErrorDetail(null);
    reResolveExecutionConfig(executionId)
      .then(entries => {
        setDiff(entries);
        setConfirming(false);
        return getExecutionConfig(executionId);
      })
      .then(fresh => {
        const modeTests = fresh.tests.filter(t => t.mode);
        if (modeTests.length > 0) {
          setTests(modeTests);
          setForm({
            mode: modeTests[0].mode as ModeFormValue['mode'],
            qps: modeTests[0].throughput ?? 0,
            ...secondsToForm(modeTests[0].duration),
          });
        }
      })
      .catch((e: unknown) => {
        setError(e instanceof ApiError ? e.message : 'failed to re-resolve config');
        setErrorDetail(errorDetails(e));
        setConfirming(false);
      })
      .finally(() => setBusy(false));
  };

  if (error && !tests) {
    return null; // the ordinary config card surfaces load failures
  }
  if (!tests || !form) {
    return null;
  }

  return (
    <Card data-testid="mode-config-card">
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle>
          Simple configuration{' '}
          <span
            className="ml-1 inline-flex items-center rounded-full bg-sky-100 px-2.5 py-0.5 text-xs font-medium text-sky-800 dark:bg-sky-900/30 dark:text-sky-300"
            data-testid="mode-config-chip"
          >
            {modeChipLabel(tests[0])}
          </span>
        </CardTitle>
        {appliedAt && <span className="text-caption text-slate-500">re-applied {appliedAt}</span>}
      </CardHeader>
      <CardContent className="space-y-4">
        <table className="w-full text-left text-body-sm" data-testid="mode-config-table">
          <thead>
            <tr className="text-caption border-b border-slate-200 text-slate-500 dark:border-slate-700 dark:text-slate-400">
              <th scope="col" className="px-3 py-2 font-medium">
                Scenario
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                Mode
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                Concurrency
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                Ramp-up (s)
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                Engines
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                Target QPS
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                Duration
              </th>
            </tr>
          </thead>
          <tbody>
            {tests.map(t => (
              <tr key={t.scenario_id} data-testid={`mode-config-row-${t.scenario_id}`}>
                <td className="px-3 py-2">{t.scenario_id}</td>
                <td className="px-3 py-2">{t.mode}</td>
                <td className="px-3 py-2">{t.concurrency}</td>
                <td className="px-3 py-2">{t.rampup}</td>
                <td className="px-3 py-2">{t.engines}</td>
                <td className="px-3 py-2">{t.throughput ?? 'unlimited'}</td>
                <td className="px-3 py-2">{t.duration}s</td>
              </tr>
            ))}
          </tbody>
        </table>
        {tests.map(t => (
          <ul key={t.scenario_id} className="space-y-1" data-testid="mode-derivation">
            {modeDerivationLines(t, perPodQps[t.scenario_id]).map(line => (
              <li key={line} className="text-caption text-slate-500 dark:text-slate-400">
                {line}
              </li>
            ))}
          </ul>
        ))}
        <p className="text-caption text-slate-500 dark:text-slate-400">
          Resolved once, at save time — a later recalibration does not change a stored config.
        </p>
        {canUpdate && (
          <div className="space-y-3 border-t border-slate-200 pt-4 dark:border-slate-700">
            <div className="flex flex-wrap items-center gap-3">
              {confirming ? (
                <>
                  <span className="text-caption text-slate-600 dark:text-slate-300">
                    Re-resolve every mode entry against the current calibration?
                  </span>
                  <Button onClick={reResolve} disabled={busy} data-testid="mode-reresolve-confirm">
                    {busy ? 'Re-resolving…' : 'Yes, re-resolve'}
                  </Button>
                  <Button
                    variant="ghost"
                    onClick={() => setConfirming(false)}
                    disabled={busy}
                    data-testid="mode-reresolve-cancel"
                  >
                    Cancel
                  </Button>
                </>
              ) : (
                <Button onClick={() => setConfirming(true)} disabled={busy} data-testid="mode-reresolve">
                  Re-resolve against current calibration
                </Button>
              )}
            </div>
            {diff && (
              <div
                className="rounded-md border border-slate-200 p-3 dark:border-slate-700"
                data-testid="mode-reresolve-diff"
              >
                <p className="text-caption font-medium text-slate-600 dark:text-slate-300">Re-resolved</p>
                <ul className="mt-2 space-y-2">
                  {diff.map(entry => (
                    <li
                      key={entry.scenario_id}
                      className="text-caption text-slate-600 dark:text-slate-300"
                      data-testid={`mode-reresolve-diff-${entry.scenario_id}`}
                    >
                      <span className="font-medium">scenario {entry.scenario_id}</span>
                      {entry.mode ? ` (${entry.mode})` : ''} —{' '}
                      {entry.changed ? (
                        <span className="flex flex-col gap-0.5 sm:flex-row sm:flex-wrap sm:gap-x-4">
                          <span>{diffCell('engines', entry.before.engines, entry.after.engines)}</span>
                          <span>{diffCell('concurrency', entry.before.concurrency, entry.after.concurrency)}</span>
                          <span>{diffCell('rampup', entry.before.rampup, entry.after.rampup)}</span>
                          <span>{diffCell('throughput', entry.before.throughput, entry.after.throughput)}</span>
                        </span>
                      ) : (
                        <span>unchanged</span>
                      )}
                    </li>
                  ))}
                </ul>
              </div>
            )}
            {singleTest ? (
              <>
                <p className="text-caption font-medium text-slate-600 dark:text-slate-300">
                  Re-apply (restates this scenario&rsquo;s mode, rate, and duration)
                </p>
                <ModeForm value={form} onChange={setForm} />
                <Button onClick={apply} disabled={busy || !modeFormValid(form)} data-testid="mode-reapply">
                  {busy ? 'Applying…' : 'Re-apply simple config'}
                </Button>
              </>
            ) : (
              <p className="text-caption text-slate-500 dark:text-slate-400" data-testid="mode-reapply-unavailable">
                Multi-scenario config: re-resolve refreshes every entry; restating one scenario&rsquo;s mode happens on
                its own execution.
              </p>
            )}
          </div>
        )}
        {error && (
          <div>
            <p className="text-sm text-red-600 dark:text-red-400" role="alert">
              {error}
            </p>
            <ActionErrorDetails details={errorDetail} />
          </div>
        )}
      </CardContent>
    </Card>
  );
}
