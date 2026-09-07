// Types and fetchers for the two read-only report endpoints this page uses:
// GET /api/executions/{execution_id}/reports and GET /api/runs/{run_id}/report.
// Field names mirror internal/domain/report.Report's JSON tags exactly.
import { apiClient } from './client';

export interface Load {
  concurrency: number;
  throughput: number;
  duration_seconds?: number;
  samples?: number;
  failed?: number;
}

export interface Attribution {
  target: number;
  engine: number;
  unknown: number;
}

export type ErrorSide = 'target' | 'engine' | 'unknown';

export interface ErrorSignature {
  label: string;
  response_code?: string;
  side: ErrorSide;
  count: number;
  exemplars?: string[];
}

export interface LabelSummary {
  label: string;
  samples: number;
  failed: number;
  error_rate: number;
  latency: Record<string, number>;
}

export type Outcome = 'passed' | 'failed' | 'aborted' | 'error';

/** One configured criterion the run tripped (unparsed absent/false), or
 * could not be evaluated at all (unparsed true) — Phase 29's verdict layer.
 * unparsed is omitempty on the wire, hence optional here. */
export interface FailingCriterion {
  criterion: string;
  unparsed?: boolean;
}

export interface Report {
  execution_id: number;
  scenario_id: number;
  run_id: number;
  engine?: string;
  /** Load origin: empty/absent means the deployment default cluster. */
  cluster?: string;
  /** Trace id the run's load carried (traceparent/baggage); absent on runs that predate it. */
  correlation_id?: string;
  started_at: string;
  ended_at: string;
  outcome: Outcome;
  requested: Load;
  achieved: Load;
  error_rate: number;
  /** Percentile (as a string, e.g. "95") -> response time in seconds. */
  latency: Record<string, number>;
  attribution: Attribution;
  errors?: ErrorSignature[];
  labels?: LabelSummary[];
  /** The execution's configured pass/fail criteria, as configured (Phase 29). */
  criteria?: string[] | null;
  /** Which configured criteria this run tripped (or could not parse). */
  failing_criteria?: FailingCriterion[] | null;
}

/**
 * Most recent first, per the backend's own ordering (ListReports). The
 * backend encodes a nil slice as JSON null rather than [] (Go's json
 * package does this for an unset slice) -- normalized here so callers can
 * always treat the result as an array.
 */
export async function listExecutionReports(executionId: number, limit?: number): Promise<Report[]> {
  const query = limit ? `?limit=${limit}` : '';
  const got = await apiClient.get<Report[] | null>(`/executions/${executionId}/reports${query}`);
  return got ?? [];
}

/** GET /api/runs/{run_id}/report. The Phase 29 verdict arrays normalize
 * null/absent to [] — the same convention listExecutionReports applies — so
 * a run with no criteria renders "none" rather than crashing on null. */
export async function getRunReport(runId: number): Promise<Report> {
  const rep = await apiClient.get<Report>(`/runs/${runId}/report`);
  if (rep.criteria == null) rep.criteria = [];
  if (rep.failing_criteria == null) rep.failing_criteria = [];
  return rep;
}

/** The two shard object kinds the run endpoints expose (task 30). */
export type ShardObjectKind = 'config' | 'log';

/** Builds the object-store path for a shard's config or log; serves as the single source of that URL shape. */
export function shardObjectUrl(runId: number, scenarioId: number, shard: number, kind: ShardObjectKind): string {
  return `/runs/${runId}/scenarios/${scenarioId}/shards/${shard}/${kind}`;
}

/** A shard's compiled Taurus config exactly as the run used it (text/plain). */
export function getShardConfig(runId: number, scenarioId: number, shard: number): Promise<string> {
  return apiClient.text(shardObjectUrl(runId, scenarioId, shard, 'config'));
}

/** A shard's captured engine output, durable after the pod that produced it is deleted (text/plain). */
export function getShardLog(runId: number, scenarioId: number, shard: number): Promise<string> {
  return apiClient.text(shardObjectUrl(runId, scenarioId, shard, 'log'));
}
