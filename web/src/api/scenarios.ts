// Fetchers for scenario fragments (R4's editor data plane), delegating to the
// generated client (phase 64 migration; the hand-rolled originals called
// apiClient directly). GET /api/scenarios/{id}/requests returns the
// fragment's YAML bytes verbatim (text/yaml, G2); PUT takes them back the
// same way (G3, any of the text/yaml media types). Round-tripping is
// byte-exact by contract -- the editor must never reformat what it did not
// touch.
import { ApiError } from './client';
import {
  getScenariosByScenarioIdRequests,
  getScenariosByScenarioIdThresholds,
  getTemplates,
  postScenariosByScenarioIdInstantiate,
  postScenariosByScenarioIdRequestsValidate,
  putScenariosByScenarioIdRequests,
  putScenariosByScenarioIdThresholds,
} from './generated';

/** The fragment's YAML exactly as stored (no server-side normalization). */
export function getScenarioRequests(scenarioId: number): Promise<string> {
  return getScenariosByScenarioIdRequests(scenarioId);
}

/**
 * Saves the fragment. The body is sent verbatim as text/yaml -- the G3
 * handler dispatches on media type and stores non-multipart bodies
 * byte-for-byte. The wire answers with a {message} envelope; the page
 * contract here stays void.
 */
export async function setScenarioRequests(scenarioId: number, yaml: string): Promise<void> {
  await putScenariosByScenarioIdRequests(scenarioId, yaml);
}

/** One finding from G4/G6, line-anchored (mirrors scenarioapp.Diagnostic).
 * The page contract deliberately narrows the generated type: a 400 only
 * carries error diagnostics and a 200 only info findings, both always with
 * message and line. */
export interface Diagnostic {
  severity: 'error' | 'info';
  message: string;
  line: number;
  col?: number;
  path?: string;
}

export interface ValidateResponse {
  valid: boolean;
  diagnostics: Diagnostic[];
}

/**
 * G5's validate endpoint: same checks as store, nothing persisted. A 400
 * carries DiagnosticsError -- the reasons ride in the error body's
 * diagnostics array, unwrapped here. Other errors propagate.
 */
export async function validateScenarioRequests(
  scenarioId: number,
  yaml: string,
): Promise<ValidateResponse> {
  try {
    const res = await postScenariosByScenarioIdRequestsValidate(scenarioId, yaml);
    return { valid: res.valid ?? true, diagnostics: (res.diagnostics ?? []) as Diagnostic[] };
  } catch (err) {
    if (err instanceof ApiError && err.status === 400) {
      const diags = (err.data as { diagnostics?: Diagnostic[] } | undefined)?.diagnostics;
      return { valid: false, diagnostics: diags ?? [] };
    }
    throw err;
  }
}

/** One template in the catalog: a global starting point (project 0, no
 * tenant) the instantiate endpoint clones into an ordinary scenario. The
 * page contract deliberately narrows the generated Scenario row to what the
 * picker renders. */
export interface Template {
  id: number;
  name: string;
  /** The template's slug (e.g. httpbin-baseline). */
  templateName: string;
}

/** GET /api/templates -- the template catalog, in id order. Server-driven:
 * the picker renders exactly this list; nothing is cloned client-side. */
export async function listTemplates(): Promise<Template[]> {
  const got = await getTemplates();
  return (got ?? []).map((t) => ({ id: t.id as number, name: t.name as string, templateName: t.template_name as string }));
}

/** Instantiate input: the clone's name and owning project, plus the one
 * documented override (the fragment's default-address). Everything else the
 * template carries is cloned server-side, verbatim. */
export interface InstantiateInput {
  name: string;
  projectId: number;
  targetUrl?: string;
}

/**
 * POST /api/scenarios/{id}/instantiate -- clone a template into a fresh,
 * ordinary scenario and return its id. The override is sent only when set,
 * so the wire carries exactly what the caller chose.
 */
export async function instantiateScenario(templateId: number, input: InstantiateInput): Promise<number> {
  const sc = await postScenariosByScenarioIdInstantiate(templateId, {
    name: input.name,
    project_id: input.projectId,
    overrides: input.targetUrl ? { target_url: input.targetUrl } : undefined,
  });
  return sc.id as number;
}

// --- Scenario thresholds (phase 72) ------------------------------------------

/** One threshold definition as the editor edits it: metric, direction, and
 * the bound in the metric's own unit. The generated enum types are narrowed
 * to string on purpose here -- the editor validates the spelling against the
 * same enum the page renders as options, and the wire carries what the user
 * chose. */
export interface ThresholdInput {
  metric: string;
  comparison: string;
  value: number;
}

/** A stored threshold: the definition plus its server-assigned id and stamp
 * (the editor syncs on ids, so an idempotent re-save is a no-op). */
export interface StoredThreshold extends ThresholdInput {
  id: number;
}

/** GET /api/scenarios/{id}/thresholds -- the scenario's bounds, definition
 * order. Normalized to an array: empty when none are defined, never null. */
export async function listThresholds(scenarioId: number): Promise<StoredThreshold[]> {
  const got = await getScenariosByScenarioIdThresholds(scenarioId);
  return (got ?? []).map((t) => ({
    id: t.id,
    metric: t.metric,
    comparison: t.comparison,
    value: t.value,
  }));
}

/**
 * PUT /api/scenarios/{id}/thresholds -- replace-all, the editor-save
 * semantics: the given list is the whole truth; saving it twice is a no-op,
 * an empty list clears. Responds with the stored set (ids assigned), which
 * becomes the editor's new state.
 */
export async function saveThresholds(scenarioId: number, thresholds: ThresholdInput[]): Promise<StoredThreshold[]> {
  return putScenariosByScenarioIdThresholds(scenarioId, {
    thresholds: thresholds.map((t) => ({
      metric: t.metric as 'http_p95_ms' | 'http_p99_ms' | 'error_rate' | 'throughput_qps',
      comparison: t.comparison as 'lt' | 'gt',
      value: t.value,
    })),
  });
}
