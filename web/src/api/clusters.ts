// Types and fetcher for GET /api/clusters (the cluster registry list).
// Field names mirror internal/adapters/httpapi/cluster_handlers.go's
// clusterResponse JSON tags exactly -- note the registry's secrets never
// appear here: the encrypted credential and ingest-token hash stay
// server-side, and the minted token is returned only once, at registration.
import { apiClient } from './client';

export type ClusterOrigin = 'operator' | 'byoc';

export interface Cluster {
  name: string;
  api_url: string;
  ingest_url: string;
  sidecar_image: string;
  namespace: string;
  secret_ref: string;
  origin: ClusterOrigin;
  created_by?: string;
  created_time: string;
  /** Aggregate engines in use on the cluster (quota ledger, phase 25).
   *  Absent when the deployment has no quota ledger wired -- the meter
   *  renders its honest "no capacity reported" state off the absence. */
  engines_used?: number;
  /** Aggregate engine ceiling across tenants (quota ledger, phase 25). */
  engines_ceiling?: number;
}

/**
 * listClusters always encodes an array (built with make(), never nil), so
 * no null normalization is needed -- unlike listExecutionReports, whose
 * nil-slice-as-null quirk reports.ts documents.
 */
export function listClusters(): Promise<Cluster[]> {
  return apiClient.get<Cluster[]>(`/clusters`);
}
