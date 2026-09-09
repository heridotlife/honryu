// Types and fetchers for the executions list (G1's GET /api/executions) and
// per-execution fetch. Field names mirror ExecutionSummary's JSON tags in
// internal/adapters/httpapi/execution_handlers.go's toExecutionSummary.
import { apiClient } from './client';

/** One row of the executions list; the caller-scoped, newest-first summary. */
export interface ExecutionSummary {
  id: number;
  name: string;
  project_id: number;
  engine?: string;
  /** "normal" | "calibrate_engine" (phase 39); absent on pre-phase39
   * backends, which only ever produced normal executions. */
  kind?: string;
  cluster?: string;
  created_time: string;
}

/** GET /api/executions -- every execution of a project the caller may see, newest first. */
export function listExecutions(): Promise<ExecutionSummary[]> {
  return apiClient.get<ExecutionSummary[]>('/executions');
}
