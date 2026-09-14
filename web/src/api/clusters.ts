// Fetcher for GET /api/clusters (the cluster registry list). Phase 64: both
// the fetcher and the Cluster type are re-exports from the generated client
// (web/src/api/generated.ts, typed from api/openapi.yaml) -- the hand-rolled
// originals lived here until the migration. Field names mirror
// internal/adapters/httpapi/cluster_handlers.go's clusterResponse JSON tags
// exactly -- note the registry's secrets never appear here: the encrypted
// credential and ingest-token hash stay server-side, and the minted token is
// returned only once, at registration.
import type { Cluster } from './generated';

export { getClusters as listClusters } from './generated';
export type { Cluster };

/** The spec's Cluster.origin enum, under the page-facing name Clusters.tsx
 * filters on. Derived from the generated type so spec and page cannot drift. */
export type ClusterOrigin = Cluster['origin'];
