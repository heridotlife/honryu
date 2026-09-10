// Types and fetchers for per-project report digests (phase 42):
// PUT/GET/DELETE /api/projects/{project_id}/digest and
// GET /api/projects/{project_id}/digests. Field names mirror
// digest_handlers.go's digestConfigResponse / digestRowResponse exactly.
// A 404 from the config GET is not an error state to the card -- it is the
// "off" state (no schedule row exists), so getDigestConfig resolves
// it to null rather than throwing.
import { apiClient, ApiError } from './client';

/** The two period words the backend accepts; anything else is a 400. */
export type DigestPeriod = 'daily' | 'weekly';

/** The project's firing configuration; absent (null) means off. */
export interface DigestConfig {
  project_id: number;
  period: DigestPeriod;
  enabled: boolean;
  /** ISO timestamp of the last claimed fire; absent when none has fired. */
  last_fired?: string;
}

/** Outcome counts inside one digest window. */
export interface DigestByOutcome {
  passed: number;
  failed: number;
  aborted: number;
}

/** One execution's share of a digest window. */
export interface DigestExecution {
  execution_id: number;
  name: string;
  runs: number;
  worst_outcome: string;
}

/** One stored digest row, newest-first from the feed endpoint. */
export interface DigestRow {
  id: number;
  period: DigestPeriod;
  window_start: string;
  window_end: string;
  runs_total: number;
  by_outcome: DigestByOutcome;
  threshold_failures: number;
  executions: DigestExecution[];
}

/** GET config; resolves 404 to null ("off"), propagates other failures. */
export async function getDigestConfig(projectId: number): Promise<DigestConfig | null> {
  try {
    return await apiClient.get<DigestConfig>(`/projects/${projectId}/digest`);
  } catch (err: unknown) {
    if (err instanceof ApiError && err.status === 404) {
      return null;
    }
    throw err;
  }
}

/** PUT config -- the "on"/"switch period" path (upsert + enable). */
export function setDigestConfig(projectId: number, period: DigestPeriod): Promise<DigestConfig> {
  const form = new URLSearchParams({ period });
  return apiClient.request<DigestConfig>(`/projects/${projectId}/digest`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: form.toString(),
  });
}

/** DELETE config -- the "off" path (204; the stored feed is history and stays). */
export function deleteDigestConfig(projectId: number): Promise<void> {
  return apiClient.request<void>(`/projects/${projectId}/digest`, { method: 'DELETE' });
}

/** GET the stored digest feed, newest first. */
export function listDigests(projectId: number, limit = 10): Promise<DigestRow[]> {
  return apiClient.get<DigestRow[]>(`/projects/${projectId}/digests?limit=${limit}`);
}
