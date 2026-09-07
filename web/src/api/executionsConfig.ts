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

/** The bare loadprofile.Profile the JSON PUT decodes (GET wraps it in
 * {"multi-test": ...}; NewTest's flow PUTs this same bare shape). */
export interface ExecutionConfig {
  name: string;
  project_id: number;
  execution_id: number;
  tests: ConfigTest[];
  csv_split?: boolean;
  /** Taurus pass/fail criteria (e.g. "failures>10%", "p95>500ms") evaluated
   * against every run report. Absent on the wire = none configured; a save
   * always sends an array (empty clears), so removals persist. */
  criteria?: string[];
}

/** GET /api/executions/{id}/config — unwraps the multi-test envelope. */
export async function getExecutionConfig(executionId: number): Promise<ExecutionConfig> {
  const wrapped = await apiClient.get<{ 'multi-test': ExecutionConfig }>(
    `/executions/${executionId}/config`,
  );
  return wrapped['multi-test'];
}

/** PUT /api/executions/{id}/config — bare profile, not the wrapper. */
export async function putExecutionConfig(executionId: number, cfg: ExecutionConfig): Promise<void> {
  await apiClient.putRaw(
    `/executions/${executionId}/config`,
    'application/json',
    JSON.stringify(cfg),
  );
}
