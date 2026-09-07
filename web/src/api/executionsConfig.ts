// Execution config fetch + save (phase 27): the multi-test wrapper shape
// GET/PUT /api/executions/{id}/config already serve. Throughput is the
// operator-facing "target QPS" -- omitted from the wire means unlimited,
// preserved verbatim on round-trip.
import { apiClient } from './client';

/** One test row of the multi-test config. */
export interface ConfigTest {
  name: string;
  scenario_id: number;
  concurrency: number;
  rampup: number;
  engines: number;
  duration: number;
  /** Target QPS; undefined = unlimited (omitted on the wire). */
  throughput?: number;
  csv_split?: boolean;
}

/** The {"multi-test": {...}} wrapper the endpoint requires. */
export interface ExecutionConfig {
  'multi-test': {
    name: string;
    project_id: number;
    execution_id: number;
    tests: ConfigTest[];
    csv_split?: boolean;
  };
}

/** GET /api/executions/{id}/config */
export function getExecutionConfig(executionId: number): Promise<ExecutionConfig> {
  return apiClient.get<ExecutionConfig>(`/executions/${executionId}/config`);
}

/** PUT /api/executions/{id}/config -- full config back, same wrapper. */
export async function putExecutionConfig(executionId: number, cfg: ExecutionConfig): Promise<void> {
  await apiClient.putRaw(
    `/executions/${executionId}/config`,
    'application/json',
    JSON.stringify(cfg),
  );
}
