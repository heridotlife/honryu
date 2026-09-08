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
  /** Per-status counts, dominant first (Phase 30). Absent -- never [] --
   * for runs whose engine reports no response codes or reports from before
   * they were accumulated; do not materialize it. */
  statuses?: StatusBadge[];
}

/** One HTTP status a label's requests returned, and how often. */
export interface StatusBadge {
  code: string;
  count: number;
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
  return normalizeVerdicts(await apiClient.get<Report>(`/runs/${runId}/report`));
}

/** The Phase 29 verdict arrays arrive null/absent when the backend had
 * none to send (Go's nil slice encodes as JSON null); normalized to [] so
 * every consumer can treat them as arrays. Shared by the session'd and
 * shared fetches, which serve the same payload. */
function normalizeVerdicts(rep: Report): Report {
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

// --- Share links (phase 34) -------------------------------------------------

/** The POST /share response: a freshly minted public link. expires_at is
 * null for a never-expiring link. */
export interface ShareLink {
  token: string;
  /** SPA path ('/share/<token>'); prefix the origin for a hand-out URL. */
  url: string;
  expires_at: string | null;
}

/** One row of the share-link list: what the share dialog shows and what
 * revoke is keyed by. */
export interface ShareLinkInfo {
  token: string;
  created_by: string;
  created_time: string;
  expires_at: string | null;
}

/** POST /api/runs/{run_id}/share. expiresInHours omitted mints a
 * never-expiring link; the backend clamps to 720 (30 days). */
export function shareRun(runId: number, expiresInHours?: number): Promise<ShareLink> {
  const body =
    expiresInHours === undefined ? undefined : JSON.stringify({ expires_in_hours: expiresInHours });
  return apiClient.request<ShareLink>(`/runs/${runId}/share`, {
    method: 'POST',
    // The one honryu mutation family with a JSON body besides the session
    // endpoints: the issue route parses JSON, not a form.
    ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body }),
  });
}

/** GET /api/runs/{run_id}/share — the run's links in issue order. */
export async function listShares(runId: number): Promise<ShareLinkInfo[]> {
  const got = await apiClient.get<ShareLinkInfo[] | null>(`/runs/${runId}/share`);
  return got ?? [];
}

/** DELETE /api/runs/{run_id}/share/{token} — revoke one link. */
export function revokeShare(runId: number, token: string): Promise<void> {
  return apiClient.request<void>(`/runs/${runId}/share/${token}`, { method: 'DELETE' });
}

/** GET /api/share/{token} — the public fetch: the same Report payload the
 * session'd route serves, verdict arrays normalized the same way. No
 * session required; 404 (ApiError) on an unknown, revoked, or expired
 * link. */
export async function fetchShared(token: string): Promise<Report> {
  return normalizeVerdicts(await apiClient.get<Report>(`/share/${token}`));
}

/** The hand-out form of a share link: absolute, so pasting it anywhere
 * (chat, email) lands on the SPA whatever its path was. */
export function shareLinkUrl(token: string): string {
  return `${window.location.origin}/share/${token}`;
}
