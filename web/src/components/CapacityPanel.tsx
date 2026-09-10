import { useEffect, useState } from 'react';
import Button from './ui/Button';
import Card, { CardContent, CardHeader, CardTitle } from './ui/Card';
import { ApiError } from '../api/client';
import {
  fanOutCapacity,
  getCalibrationJob,
  triggerCalibration,
  type CalibrationJob,
  type CapacityKey,
  type FanOutStatus,
} from '../api/calibration';
import type { ExecutionInfo } from '../api/status';

export interface CapacityPanelProps {
  scenarioId: number;
  executionId: number;
  keyInfo: CapacityKey;
  /** The panel polls the job while a search is in flight. */
  targetQPS: number;
}

/**
 * Per-status explanation and call to action. The number is rendered ONLY
 * for "ok" — every other status explains itself instead of guessing.
 */
export function fanOutCopy(status: FanOutStatus): { title: string; detail: string; cta: string | null } {
  switch (status) {
    case 'ok':
      return {
        title: 'Calibrated',
        detail: 'The profile is fresh and matches the scenario.',
        cta: null,
      };
    case 'no_profile':
      return {
        title: 'No capacity profile',
        detail: 'This scenario has never been calibrated for this pod size.',
        cta: 'Run a calibration to size the fleet.',
      };
    case 'stale':
      return {
        title: 'Profile stale',
        detail: 'The scenario changed after this profile was calibrated.',
        cta: 'Recalibrate to restore a trustworthy count.',
      };
    case 'target_limited':
      return {
        title: 'Target-limited result',
        detail: 'The target (not the engine) saturated first, so the per-pod number is a floor.',
        cta: 'Fix target health or treat the count as a lower bound.',
      };
    case 'inconclusive':
      return {
        title: 'Inconclusive',
        detail: 'The search exhausted its budget with neither the engine nor the target saturated.',
        cta: 'Raise max QPS or steps and recalibrate.',
      };
    case 'engine_floor':
      return {
        title: 'Engine saturates below measurable load',
        detail:
          'Every search step saturated, even at the lowest rate — this scenario is too light for a single pod to reach steady measurable throughput, or the criterion is too strict.',
        cta: 'Loosen the criterion or check the scenario, then recalibrate.',
      };
  }
}

/** The phase line for an in-flight job: bracketing/bisecting with counters. */
export function jobProgressLine(job: CalibrationJob): string {
  if (job.phase === 'bracketing' || job.phase === 'bisecting') {
    const next = job.next_requested_qps !== undefined ? `, next ${job.next_requested_qps.toFixed(0)} qps` : '';
    return `${job.phase} — step ${job.step_count}${next}`;
  }
  if (job.phase === 'done') {
    return `done — ${job.result ? `${job.result.per_pod_qps.toFixed(0)} qps/pod (${job.result.saturated_by})` : 'result recorded'}`;
  }
  if (job.phase === 'failed') {
    return `failed — ${job.failure_reason || 'operational error'}`;
  }
  return job.phase;
}

/** True while the job deserves polling (will still change on its own). */
export function jobIsActive(job: CalibrationJob): boolean {
  return job.phase === 'pending' || job.phase === 'bracketing' || job.phase === 'bisecting';
}

/** The minimal execution info the mount gate needs: an engine and a kind. */
export type CapacityExecutionInfo = Pick<ExecutionInfo, 'engine' | 'kind'>;

/**
 * The panel's mount gate (phase 39): the Capacity card and its Calibrate
 * button exist ONLY on calibrate_engine executions -- triggerCalibration
 * rejects every other kind (calibrationapp.ErrExecutionNotCalibration, a
 * 400), and before Kind rode the wire the SPA could not tell the two
 * apart, so any execution with an engine showed a Calibrate button that
 * could only ever fail. An absent kind (pre-phase39 backend) is treated
 * as normal: those backends never created calibrate_engine executions.
 */
export function isCalibrationExecution(
  info: CapacityExecutionInfo | null | undefined,
): boolean {
  return !!info?.engine && info.kind === 'calibrate_engine';
}

export default function CapacityPanel({ scenarioId, executionId, keyInfo, targetQPS }: CapacityPanelProps) {
  const [status, setStatus] = useState<FanOutStatus | null>(null);
  const [engines, setEngines] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);
  const [job, setJob] = useState<CalibrationJob | null>(null);

  useEffect(() => {
    let alive = true;
    setStatus(null);
    setEngines(null);
    setError(null);
    fanOutCapacity(scenarioId, keyInfo, targetQPS)
      .then((res) => {
        if (!alive) return;
        setStatus(res.status);
        setEngines(res.engines ?? null);
      })
      .catch((err: unknown) => {
        if (alive) setError(err instanceof ApiError ? err.message : 'Failed to load capacity.');
      });
    return () => {
      alive = false;
    };
  }, [scenarioId, keyInfo.engine, keyInfo.cpu, keyInfo.memory, targetQPS]);

  // Poll the in-flight job every 5s until it settles.
  useEffect(() => {
    if (!job || !jobIsActive(job)) {
      return;
    }
    const t = setInterval(() => {
      getCalibrationJob(job.id)
        .then((j) => {
          setJob(j);
          if (j.phase === 'done') {
            // The search concluded: refresh the fan-out verdict.
            fanOutCapacity(scenarioId, keyInfo, targetQPS).then((res) => {
              setStatus(res.status);
              setEngines(res.engines ?? null);
            });
          }
        })
        .catch(() => {
          // Transient poll errors are non-fatal; the next tick retries.
        });
    }, 5000);
    return () => clearInterval(t);
  }, [job, scenarioId, keyInfo, targetQPS]);

  const startCalibration = () => {
    setStarting(true);
    setError(null);
    triggerCalibration(executionId)
      .then((j) => setJob(j))
      .catch((err: unknown) => {
        setError(err instanceof ApiError ? err.message : 'Failed to start calibration.');
      })
      .finally(() => setStarting(false));
  };

  const copy = status ? fanOutCopy(status) : null;

  return (
    <Card data-testid="capacity-panel">
      <CardHeader>
        <CardTitle>Capacity</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {error && (
          <p className="text-sm text-red-600 dark:text-red-400" role="alert">
            {error}
          </p>
        )}
        {!error && status === null && !job && <p className="text-body-sm text-slate-500 dark:text-slate-400">Loading…</p>}
        {copy && (
          <div>
            {status === 'ok' && engines !== null ? (
              <p className="text-heading-md text-slate-900 dark:text-white">
                {engines} engine{engines === 1 ? '' : 's'}
              </p>
            ) : (
              <p className="text-body-sm font-medium text-slate-900 dark:text-white">{copy.title}</p>
            )}
            <p className="text-caption text-slate-500 dark:text-slate-400">{copy.detail}</p>
            {copy.cta && <p className="text-caption mt-1 text-sky-700 dark:text-sky-400">{copy.cta}</p>}
            {status !== 'ok' && (
              <Button className="mt-2" onClick={startCalibration} disabled={starting || job !== null && jobIsActive(job)}>
                {starting ? 'Starting…' : 'Run search'}
              </Button>
            )}
          </div>
        )}
        {job && (
          <p className="text-caption font-mono text-slate-600 dark:text-slate-300" role="status">
            {jobProgressLine(job)}
          </p>
        )}
      </CardContent>
    </Card>
  );
}
