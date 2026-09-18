// The mode-aware configuration card (phase 90): renders a mode entry's
// resolved numbers with the derivation note (Q8), and offers the Simple
// re-apply form — the "calibrate, then re-configure" loop's closing half.
// Mounted like ExecutionConfigCard (idle executions); renders nothing for
// a config with no mode provenance, so Advanced executions keep the
// ordinary card untouched. The PUT goes through the same server
// resolution as NewTest's Simple submit; a 409 surfaces inline with the
// structured remediation.
import { useEffect, useState } from 'react';
import Card, { CardHeader, CardTitle, CardContent } from './ui/Card';
import Button from './ui/Button';
import ActionErrorDetails from './ActionErrorDetails';
import ModeForm from './ModeForm';
import { getExecutionConfig, putExecutionConfig, type ConfigTest } from '../api/executionsConfig';
import { getCapacityProfile } from '../api/calibration';
import {
  buildModeTest,
  modeChipLabel,
  modeDerivationLines,
  modeFormValid,
  type ModeFormValue,
} from '../lib/modeConfig';
import { ApiError, errorDetails } from '../api/client';

interface Props {
  executionId: number;
  canUpdate: boolean;
  /** The capacity key the page already resolved (engine + pod size). */
  capacityKey: { engine: string; cpu: string; memory: string };
}

/** Seconds -> {duration, unit} for prefilling the re-apply form. */
function secondsToForm(seconds: number): Pick<ModeFormValue, 'duration' | 'unit'> {
  if (seconds >= 3600 && seconds % 3600 === 0) {
    return { duration: seconds / 3600, unit: 'h' };
  }
  return { duration: Math.round(seconds / 60), unit: 'm' };
}

export default function ModeConfigCard({ executionId, canUpdate, capacityKey }: Props) {
  const [test, setTest] = useState<ConfigTest | null>(null);
  const [projectId, setProjectId] = useState(0);
  const [perPodQps, setPerPodQps] = useState<number | undefined>(undefined);
  const [form, setForm] = useState<ModeFormValue | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [errorDetail, setErrorDetail] = useState<Record<string, unknown> | null>(null);
  const [appliedAt, setAppliedAt] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    setTest(null);
    setPerPodQps(undefined);
    setError(null);
    setErrorDetail(null);
    setAppliedAt(null);
    getExecutionConfig(executionId)
      .then(cfg => {
        if (!alive) return;
        setProjectId(cfg.project_id);
        const modeTest = cfg.tests.find(t => t.mode);
        if (!modeTest) {
          setTest(null);
          return;
        }
        setTest(modeTest);
        setForm({
          mode: modeTest.mode as ModeFormValue['mode'],
          qps: modeTest.throughput ?? 0,
          ...secondsToForm(modeTest.duration),
        });
        // Best-effort enrichment of the derivation note: the profile's
        // per-pod rate. Absent (never calibrated under this key since,
        // or fetch failed) degrades the note's wording, nothing else.
        getCapacityProfile(modeTest.scenario_id, capacityKey)
          .then(p => {
            if (alive) setPerPodQps(p.per_pod_qps);
          })
          .catch(() => {
            /* note degrades to the generic wording */
          });
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
    if (!form || !test || !modeFormValid(form)) {
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
      tests: [buildModeTest(test.name || 'test', test.scenario_id, form)],
    };
    putExecutionConfig(executionId, cfg)
      .then(() => getExecutionConfig(executionId))
      .then(fresh => {
        const modeTest = fresh.tests.find(t => t.mode);
        if (modeTest) {
          setTest(modeTest);
          setForm({
            mode: modeTest.mode as ModeFormValue['mode'],
            qps: modeTest.throughput ?? 0,
            ...secondsToForm(modeTest.duration),
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

  if (error && !test) {
    return null; // the ordinary config card surfaces load failures
  }
  if (!test || !form) {
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
            {modeChipLabel(test)}
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
            <tr>
              <td className="px-3 py-2">{test.scenario_id}</td>
              <td className="px-3 py-2">{test.concurrency}</td>
              <td className="px-3 py-2">{test.rampup}</td>
              <td className="px-3 py-2">{test.engines}</td>
              <td className="px-3 py-2">{test.throughput ?? 'unlimited'}</td>
              <td className="px-3 py-2">{test.duration}s</td>
            </tr>
          </tbody>
        </table>
        <ul className="space-y-1" data-testid="mode-derivation">
          {modeDerivationLines(test, perPodQps).map(line => (
            <li key={line} className="text-caption text-slate-500 dark:text-slate-400">
              {line}
            </li>
          ))}
        </ul>
        <p className="text-caption text-slate-500 dark:text-slate-400">
          Resolved once, at save time — a later recalibration does not change a stored config.
        </p>
        {canUpdate && (
          <div className="space-y-3 border-t border-slate-200 pt-4 dark:border-slate-700">
            <p className="text-caption font-medium text-slate-600 dark:text-slate-300">
              Re-apply (re-resolves against the current calibration)
            </p>
            <ModeForm value={form} onChange={setForm} />
            <Button onClick={apply} disabled={busy || !modeFormValid(form)} data-testid="mode-reapply">
              {busy ? 'Applying…' : 'Re-apply simple config'}
            </Button>
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
