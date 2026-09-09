// Types and fetchers for the live status page's two data sources: GET
// /api/executions/{execution_id}/status (a snapshot) and GET
// /api/executions/{execution_id}/stream (an SSE feed of engine.Metric
// events, one per measured interval). Field names mirror the Go JSON tags
// exactly -- see internal/app/lifecycleapp/service.go's Status/ScenarioStatus
// and internal/domain/engine/engine.go's Metric.
import { apiClient } from './client';

export type Phase = 'idle' | 'deployed' | 'running';

export interface ScenarioStatus {
  scenario_id: number;
  engines: number;
  engines_deployed: number;
  engines_reachable: boolean;
  in_progress: boolean;
  started_time?: string;
}

export interface ExecutionStatus {
  phase: Phase;
  pool_size: number;
  status: ScenarioStatus[];
}

export async function getExecutionStatus(executionId: number): Promise<ExecutionStatus> {
  const got = await apiClient.get<ExecutionStatus>(`/executions/${executionId}/status`);
  // Go marshals a nil []ScenarioStatus as JSON null, not [], when no
  // scenarios are deployed yet (see internal/app/lifecycleapp's Status).
  return { ...got, status: got.status ?? [] };
}

export interface EngineMetric {
  threads: number;
  latency: number;
  label: string;
  status: string;
  raw: string;
  execution_id: string;
  scenario_id: string;
  engine_id: string;
  run_id: string;
}

/** Subscribes to an execution's live metric stream. Returns an unsubscribe function. The optional handlers surface the EventSource's own open/error transitions (connection tracking) without every caller needing the source itself. */
export function streamExecutionMetrics(
  executionId: number,
  onMetric: (m: EngineMetric) => void,
  handlers: { onOpen?: () => void; onError?: () => void } = {}
): () => void {
  const source = new EventSource(`${apiClient.baseUrl}/executions/${executionId}/stream`);
  source.onopen = () => handlers.onOpen?.();
  source.onerror = () => handlers.onError?.();
  source.onmessage = (event: MessageEvent<string>) => {
    try {
      onMetric(JSON.parse(event.data) as EngineMetric);
    } catch {
      // Malformed event; drop it rather than take down the whole stream.
    }
  };
  return () => source.close();
}

/** One line of an execution's requested load profile (domain loadprofile.Entry's JSON shape). */
export interface LoadProfileEntry {
  name: string;
  scenario_id: number;
  concurrency: number;
  rampup: number;
  engines: number;
  throughput?: number;
}

/** A data file the execution references, as a served URL (executionapp.FileRef). */
export interface FileRef {
  filename: string;
  url: string;
}

/**
 * GET /api/executions/{execution_id} -- the execution's immutable setup:
 * which engine kind runs it, on which cluster, under what load profile.
 * Field names mirror httpapi's executionResponse JSON tags exactly. engine
 * and cluster are omitempty: absent means the deployment defaults.
 */
export interface ExecutionInfo {
  id: number;
  name: string;
  project_id: number;
  engine?: string;
  /** "normal" | "calibrate_engine" (phase 39); absent on pre-phase39
   * backends, which only ever produced normal executions. */
  kind?: string;
  cluster?: string;
  csv_split: boolean;
  created_time: string;
  load_profile: LoadProfileEntry[];
  data: FileRef[];
}

/**
 * The current output of a scenario's engine pod (text/plain). This is the
 * LIVE stream -- the durable, after-the-fact copy lives in the object store
 * (see reports.ts getShardLog). The hub polls this for the tail view.
 */
export function getScenarioPodLog(executionId: number, scenarioId: number): Promise<string> {
  return apiClient.text(`/executions/${executionId}/scenarios/${scenarioId}/logs`);
}
export function getExecutionInfo(executionId: number): Promise<ExecutionInfo> {
  return apiClient.get<ExecutionInfo>(`/executions/${executionId}`);
}
