// Phase 93: the one-click Start orchestration — the chain the spec draws:
// POST deploy → wait for phase 'deployed' → 10s countdown (StartCountdown)
// → POST trigger. Already deployed (a finished run's engines, or a manual
// stop's): no deploy POST at all — straight to the countdown, because
// trigger-on-deployed is exactly the trigger. Pure web chaining of the
// two existing endpoints; the
// trigger handler's own bounded readiness wait (TriggerReadyPoll/Timeout,
// phase 24) covers any residual scheduling lag after the countdown, which
// is exactly why the client does not poll engines-reachable before firing.
//
// Mid-flow status observation rides the PAGE's existing 10s status poll:
// the hook watches the phase it is handed, so a mid-countdown flip (idle
// reaper purge, someone else triggering) cancels the countdown without a
// second polling loop. Worst case a flip lands just after the final tick —
// then the trigger POST is the authority and its 409 surfaces normally.
import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError, errorDetails } from '../api/client';
import { deployExecution, triggerExecution } from '../api/lifecycle';
import { getExecutionStatus, type ExecutionStatus, type Phase } from '../api/status';

/** The flow's steps, in order; null = no flow (the idle control set). */
export type StartStep = 'deploying' | 'counting' | 'triggering';

/** Fixed by the spec: 10s v1, not configurable. */
export const START_COUNTDOWN_SECONDS = 10;

export interface UseStartFlowArgs {
  executionId: number;
  /** The page's current phase — the already-deployed shortcut at begin, the deploy-completion signal, and the mid-countdown flip watch. */
  phase: Phase | null;
  /** Pushes a fresh status snapshot into the page's state (the immediate post-mutation refresh, runStop's pattern). */
  onStatus: (s: ExecutionStatus) => void;
  /** Surfaces a failure exactly where runAction's errors go (message + ActionErrorDetails payload). */
  onError: (message: string, details: Record<string, unknown> | null) => void;
  /** Clears the previous action error when a Start begins or lands. */
  onReset: () => void;
}

export function useStartFlow({ executionId, phase, onStatus, onError, onReset }: UseStartFlowArgs) {
  const [step, setStep] = useState<StartStep | null>(null);
  // Bumped by cancel (and by aborts): a deploy or trigger response that
  // lands after the operator walked away is dropped, not surfaced — the
  // user ended the flow, so the flow may not still speak.
  const runRef = useRef(0);

  const fail = useCallback(
    (err: unknown, fallback: string) => {
      setStep(null);
      onError(err instanceof ApiError ? err.message : fallback, errorDetails(err));
    },
    [onError]
  );

  /** Start clicked. Idle: deploy first, then the phase-watch effect
   *  below opens the countdown on 'deployed'. Already deployed: skip the
   *  deploy POST entirely — straight to the countdown (trigger on
   *  deployed IS the trigger). No-op if a flow is already in flight. */
  const begin = useCallback(() => {
    if (step !== null) {
      return;
    }
    onReset();
    if (phase === 'deployed') {
      setStep('counting');
      return;
    }
    setStep('deploying');
    const run = ++runRef.current;
    deployExecution(executionId)
      .then(() => {
        if (runRef.current !== run) {
          return;
        }
        // Deploy's 200 means pods were created, not that the phase
        // flipped yet. Refresh the snapshot once now (runAction's
        // pattern); the page's 10s poll carries the rest and the
        // phase-watch effect below starts the countdown on 'deployed'.
        getExecutionStatus(executionId)
          .then(onStatus)
          .catch(() => {
            // Non-fatal: the poll keeps the watch fed even if this one
            // refresh is lost.
          });
      })
      .catch(err => {
        if (runRef.current !== run) {
          return;
        }
        fail(err, 'start failed: deploy did not complete.');
      });
  }, [step, phase, executionId, onReset, onStatus, fail]);

  /** The countdown ran out: fire the trigger. No cancel from here on — the door is open. */
  const countdownComplete = useCallback(() => {
    setStep('triggering');
    const run = ++runRef.current;
    triggerExecution(executionId)
      .then(() => {
        if (runRef.current !== run) {
          return;
        }
        setStep(null);
        onReset();
        getExecutionStatus(executionId)
          .then(onStatus)
          .catch(() => {});
      })
      .catch(err => {
        if (runRef.current !== run) {
          return;
        }
        fail(err, 'start failed: trigger did not complete.');
      });
  }, [executionId, onReset, onStatus, fail]);

  // The phase watch — the only transition source besides the API calls
  // themselves. Deploying waits for 'deployed' (the pods settling); any
  // non-deployed phase during the countdown, or someone else's run
  // starting during deploy, aborts the chain with the honest reason.
  useEffect(() => {
    if (step === 'deploying' && phase === 'deployed') {
      setStep('counting');
      return;
    }
    if (step === 'deploying' && phase === 'running') {
      runRef.current++;
      setStep(null);
      onError('start aborted: the execution is already running.', null);
      return;
    }
    if (step === 'counting' && phase !== 'deployed') {
      runRef.current++;
      setStep(null); // unmounts StartCountdown → its interval clears
      onError(`start aborted: the execution is no longer deployed (phase ${phase ?? 'unknown'}).`, null);
    }
  }, [step, phase, onError]);

  /** The operator's exit: back to the phase's control set, no trigger
   *  fired, stale API responses invalidated. Engines already deploying
   *  keep deploying — cancel never rolls a deploy back; the idle TTL
   *  reaps them if nothing runs. */
  const cancel = useCallback(() => {
    runRef.current++;
    setStep(null);
  }, []);

  return { step, begin, cancel, countdownComplete, seconds: START_COUNTDOWN_SECONDS };
}
