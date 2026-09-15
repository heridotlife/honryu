// Types and fetchers for the capacity panel (R7): the (engine, cpu, memory)
// fan-out query and calibration job progress. Shapes mirror
// calibration_handlers.go's response structs exactly.
//
// Phase 69 migration: the two scenario GET fetchers below —
// getCapacityProfile and fanOutCapacity, historically raw apiClient.get
// calls — now delegate to the generated client's typed wrappers
// (getScenariosByScenarioIdCapacityProfile[Fanout] in web/src/api/
// generated.ts, built from api/openapi.yaml by web/scripts/gen-client.mjs).
// Their wire contracts, pinned by calibration.test.ts:
//
//   getCapacityProfile(scenarioId, key)
//     GET /api/scenarios/{scenarioId}/capacity-profile
//         ?engine={key.engine}&cpu={key.cpu}&memory={key.memory}
//     200 -> CapacityProfile JSON (all fields always present); 404 (ApiError)
//     when no profile exists for the exact key — the planner and the
//     editor's save-guard branch on that typed 404.
//
//   fanOutCapacity(scenarioId, key, targetQPS)
//     GET /api/scenarios/{scenarioId}/capacity-profile/fanout
//         ?engine=...&cpu=...&memory=...&target_qps={String(targetQPS)}
//     200 -> { status, engines? }; engines is present ONLY alongside
//     status "ok". status is the domain's six-value verdict
//     (internal/domain/capacityprofile: ok, target_limited, inconclusive,
//     engine_floor, stale, no_profile) — the spec's FanOutResult enum
//     gained engine_floor in this same phase so the generated type
//     matches the wire.
//
// Page-facing signatures and types are unchanged; the narrowing to them
// happens once at each seam. The remaining apiClient calls in this file
// (capacity-profiles list, calibrations job/create/trigger) are NOT
// scenario routes and stay hand-written.
import { apiClient, ApiError } from './client';
import {
  getScenariosByScenarioIdCapacityProfile,
  getScenariosByScenarioIdCapacityProfileFanout,
} from './generated';

/** One (scenario, engine, cpu, memory) fan-out key; engine pins the executor. */
export interface CapacityKey {
  engine: string;
  cpu: string;
  memory: string;
}

/**
 * FanOutResult's status — why the engine count is or is not shown:
 * ok (profile fresh, engines valid), no_profile, stale (scenario edited
 * after calibration), target_limited (the target's health bounded the
 * search), inconclusive (search budget exhausted, neither end saturated),
 * engine_floor (phase 44: the engine saturated at every rate, even the
 * lowest -- no measurable throughput to fan out from).
 */
export type FanOutStatus = 'ok' | 'no_profile' | 'stale' | 'target_limited' | 'inconclusive' | 'engine_floor';

export interface FanOutResponse {
  status: FanOutStatus;
  /** Required engine count; meaningful ONLY when status is ok. */
  engines?: number;
}

/**
 * Turns a target aggregate QPS into an engine count. The response's status
 * is the contract: engines is present ONLY alongside "ok" — the panel
 * renders a number exclusively in that case.
 */
export async function fanOutCapacity(scenarioId: number, key: CapacityKey, targetQPS: number): Promise<FanOutResponse> {
  // Phase 69: was a hand-rolled apiClient.get with the same URLSearchParams
  // in this exact key order; the generated wrapper's toQuery serializes
  // identically (insertion order, String() for scalars).
  const fan = await getScenariosByScenarioIdCapacityProfileFanout(scenarioId, {
    query: {
      engine: key.engine,
      cpu: key.cpu,
      memory: key.memory,
      target_qps: targetQPS,
    },
  });
  // The generated status is the spec's six-value enum (engine_floor
  // included since this phase); the page contract requires it. Narrow once.
  return { status: fan.status as FanOutStatus, engines: fan.engines };
}

export interface CapacityProfile {
  scenario_id: number;
  engine: string;
  cpu: string;
  memory: string;
  per_pod_qps: number;
  saturated_by: string;
  scenario_fingerprint: string;
  calibrated_at: string;
  job_id: number;
}

/** The stored profile for one exact key; 404 (ApiError) when none. */
export async function getCapacityProfile(scenarioId: number, key: CapacityKey): Promise<CapacityProfile> {
  // Phase 69: was a hand-rolled apiClient.get; same wire shape via the
  // generated wrapper.
  const p = await getScenariosByScenarioIdCapacityProfile(scenarioId, {
    query: { engine: key.engine, cpu: key.cpu, memory: key.memory },
  });
  // Narrow the generated row (spec-optional fields, saturated_by the
  // handler's closed engine/target/neither verdict) into the page
  // contract: the handler's response struct carries every field without
  // omitempty, so each is always on the wire.
  return {
    scenario_id: p.scenario_id as number,
    engine: p.engine as string,
    cpu: p.cpu as string,
    memory: p.memory as string,
    per_pod_qps: p.per_pod_qps as number,
    saturated_by: p.saturated_by as string,
    scenario_fingerprint: p.scenario_fingerprint as string,
    calibrated_at: p.calibrated_at as string,
    job_id: p.job_id as number,
  };
}

/** One row of the fleet-wide capacity matrix (GET /api/capacity-profiles,
 * phase 56): a CapacityProfile WITHOUT its internal staleness detail -- no
 * scenario_fingerprint, no job_id; only the per-scenario GET serves those
 * to tooling that needs them. */
export interface CapacityProfileSummary {
  scenario_id: number;
  engine: string;
  cpu: string;
  memory: string;
  per_pod_qps: number;
  saturated_by: string;
  calibrated_at: string;
}

/** The fleet-wide capacity matrix, ordered by scenario then biggest pod
 * first (the backend owns the order -- cpu compared as milli-cores),
 * scoped server-side to projects the caller may see. */
export function listCapacityProfiles(): Promise<CapacityProfileSummary[]> {
  return apiClient.get<CapacityProfileSummary[]>(`/capacity-profiles`);
}

export interface CalibrationStep {
  requested_qps: number;
  achieved_qps: number;
  classification: string;
}

export interface CalibrationJob {
  id: number;
  execution_id: number;
  /** pending -> bracketing -> bisecting -> done | failed. */
  phase: string;
  step_count: number;
  /** The QPS the job's next step will run at; absent once done/failed. */
  next_requested_qps?: number;
  result?: { saturated_by: string; per_pod_qps: number };
  failure_reason?: string;
  created_time: string;
  steps: CalibrationStep[];
}

/** What the editor's save-guard needs to know about a scenario's profile. */
export interface ProfileGuard {
  /** A profile exists AND matches the scenario's current content. */
  fresh: boolean;
  perPodQPS?: number;
  calibratedAt?: string;
}

/**
 * The save-guard lookup (R8): fetches the profile and reports whether it
 * is FRESH (exists + fingerprint matches the scenario's current content --
 * the server computes the match, so the client never re-hashes). A 404
 * means no profile: fresh=false with no numbers, and the editor stays
 * silent (nothing to invalidate). Other errors propagate.
 */
export async function getProfileGuard(scenarioId: number, key: CapacityKey): Promise<ProfileGuard> {
  try {
    const p = await getCapacityProfile(scenarioId, key);
    // The list endpoint embeds staleness via the fan-out status; the
    // profile GET alone cannot say fresh vs stale -- so re-ask fan-out,
    // whose status IS the staleness verdict for this exact key.
    const fan = await fanOutCapacity(scenarioId, key, 1);
    return {
      fresh: fan.status === 'ok',
      perPodQPS: p.per_pod_qps,
      calibratedAt: p.calibrated_at,
    };
  } catch (err: unknown) {
    if (err instanceof ApiError && err.status === 404) {
      return { fresh: false };
    }
    throw err;
  }
}

export function getCalibrationJob(jobId: number): Promise<CalibrationJob> {
  return apiClient.get(`/calibrations/${jobId}`);
}

/** calibrationExecutionResponse's shape (the createCalibration handler's
 * 201 body): the created execution's id plus the spec as PERSISTED -- the
 * server re-reads what Create stored, so these bounds carry
 * Spec.WithDefaults' filled-in values, not the request's zero ones. */
export interface CalibrationExecution {
  execution_id: number;
  name: string;
  project_id: number;
  engine?: string;
  cpu: string;
  memory: string;
  criterion: string;
  seed_qps: number;
  max_qps: number;
  max_steps: number;
  hold_seconds: number;
}

/** What createCalibration needs; the optional bounds mirror the handler's
 * own form fields -- absent means "keep the domain default". */
export interface CreateCalibrationInput {
  projectId: number;
  name: string;
  engine: string;
  criterion: string;
  cpu: string;
  memory: string;
  /** The scenario the calibration measures; it must run on the source. */
  scenarioId: number;
  /** The execution whose load-profile entry for the scenario the backend
   *  copies as the calibration's single-pod starting entry (phase 41). */
  sourceExecutionId: number;
  seedQps?: number;
  maxQps?: number;
  maxSteps?: number;
  holdSeconds?: number;
}

/**
 * POST /api/calibrations: creates a CalibrateEngine execution configured
 * for one capacity search AND bound to the scenario -- the backend copies
 * the source execution's entry for it, so the created execution can be
 * triggered immediately (phase 41 closed the dead-shell gap where this
 * POST carried no scenario at all and the execution could never run).
 */
export function createCalibration(input: CreateCalibrationInput): Promise<CalibrationExecution> {
  const form = new URLSearchParams({
    project_id: String(input.projectId),
    name: input.name,
    engine: input.engine,
    criterion: input.criterion,
    cpu: input.cpu,
    memory: input.memory,
    scenario_id: String(input.scenarioId),
    source_execution_id: String(input.sourceExecutionId),
  });
  if (input.seedQps !== undefined) form.set('seed_qps', String(input.seedQps));
  if (input.maxQps !== undefined) form.set('max_qps', String(input.maxQps));
  if (input.maxSteps !== undefined) form.set('max_steps', String(input.maxSteps));
  if (input.holdSeconds !== undefined) form.set('hold_seconds', String(input.holdSeconds));
  return apiClient.post('/calibrations', form);
}

/**
 * Starts a fresh search over an already-configured CalibrateEngine
 * execution; the created job is returned (phase pending).
 */
export function triggerCalibration(executionId: number): Promise<CalibrationJob> {
  return apiClient.post(`/executions/${executionId}/calibration/trigger`, new URLSearchParams());
}
