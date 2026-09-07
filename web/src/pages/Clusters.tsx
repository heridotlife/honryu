// Read-only view of the cluster registry (GET /api/clusters): where load
// can run and how each registered cluster routes. Writes -- registration,
// token rotation, deletion -- are deliberately not offered here; they stay
// API/CLI operations (phase 13 spec: the SPA must not become an auth
// surface). No health probing either: this shows stored registration
// state, not live connectivity.
import { useEffect, useState } from 'react';
import Card, { CardContent } from '../components/ui/Card';
import { ApiError } from '../api/client';
import { listClusters } from '../api/clusters';
import type { Cluster, ClusterOrigin } from '../api/clusters';
import CapacityMeter from '../components/CapacityMeter';

const originClasses: Record<ClusterOrigin, string> = {
  operator: 'bg-sky-100 text-sky-800 dark:bg-sky-900/30 dark:text-sky-300',
  byoc: 'bg-violet-100 text-violet-800 dark:bg-violet-900/30 dark:text-violet-300',
};

function OriginBadge({ origin }: { origin: ClusterOrigin }) {
  return (
    <span className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${originClasses[origin]}`}>
      {origin}
    </span>
  );
}

/** One-line explanation of who owns a cluster's credentials (domain Origin's doc, humanized). */
export function originDescription(origin: ClusterOrigin): string {
  switch (origin) {
    case 'operator':
      return 'home-cluster Secret managed by the platform operator';
    case 'byoc':
      return 'customer-supplied kubeconfig (bring your own cluster)';
  }
}

/** NaN-safe timestamp formatting, matching the other pages' formatTime behavior. */
export function formatClusterTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

/** Capacity numbers for one cluster row, when the backend offers any.
 * Honest scope (phase 22): GET /api/clusters' clusterResponse carries
 * registration fields only -- no engine counts, no ceiling -- so this
 * returns nothing today and every row renders the meter's "no capacity
 * reported" state. This mapping is the single place to light the meters
 * up when the API grows real fields (phase 23 backend candidate). */
export function clusterCapacity(cluster: Cluster): { used?: number; ceiling?: number } {
  // Phase 25: the quota ledger's aggregate rides the cluster row. Both
  // fields must be present -- one without the other is a half-wired read,
  // and the meter's no-data state is the honest render for that too.
  if (typeof cluster.engines_used === 'number' && typeof cluster.engines_ceiling === 'number') {
    return { used: cluster.engines_used, ceiling: cluster.engines_ceiling };
  }
  return {};
}

/** The cluster registry, read-only. */
export default function Clusters() {
  const [clusters, setClusters] = useState<Cluster[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    listClusters()
      .then(setClusters)
      .catch((err: unknown) => setError(err instanceof ApiError ? err.message : 'Failed to load clusters.'));
  }, []);

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-display-sm text-slate-900 dark:text-white">Clusters</h1>
        <p className="text-body-sm mt-1 text-slate-500 dark:text-slate-400">
          Where load can run: the registered clusters and how each one routes engines and metrics.
        </p>
      </div>

      <Card>
        <CardContent className="space-y-4">
          <p className="text-caption text-slate-500 dark:text-slate-400">
            Read-only view of stored registration state. Registering, rotating ingest tokens, and deleting clusters are
            API operations — see <code className="rounded bg-slate-100 px-1 dark:bg-slate-900">POST /api/clusters</code>
            {' '}and friends. An empty list means only the deployment&apos;s default cluster exists (it needs no entry).
          </p>

          {error && (
            <p className="text-sm text-red-600 dark:text-red-400" role="alert">
              {error}
            </p>
          )}

          {clusters && clusters.length === 0 && (
            <p className="text-body-sm text-slate-500 dark:text-slate-400">
              No registered clusters — all load runs on the deployment&apos;s default cluster.
            </p>
          )}

          {clusters && clusters.length > 0 && (
            <div className="overflow-x-auto">
              <table className="w-full text-left text-body-sm">
                <thead>
                  <tr className="text-caption border-b border-slate-200 text-slate-500 dark:border-slate-700 dark:text-slate-400">
                    <th scope="col" className="px-3 py-2 font-medium">Name</th>
                    <th scope="col" className="px-3 py-2 font-medium">Origin</th>
                    <th scope="col" className="px-3 py-2 font-medium">Capacity</th>
                    <th scope="col" className="px-3 py-2 font-medium">Engine namespace</th>
                    <th scope="col" className="px-3 py-2 font-medium">Sidecar image</th>
                    <th scope="col" className="px-3 py-2 font-medium">Ingest URL</th>
                    <th scope="col" className="px-3 py-2 font-medium">API server</th>
                    <th scope="col" className="px-3 py-2 font-medium">Credential Secret</th>
                    <th scope="col" className="px-3 py-2 font-medium">Registered</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {clusters.map((c) => (
                    <tr key={c.name} title={originDescription(c.origin)}>
                      <td className="px-3 py-2 font-medium whitespace-nowrap text-slate-900 dark:text-white">{c.name}</td>
                      <td className="px-3 py-2">
                        <OriginBadge origin={c.origin} />
                      </td>
                      <td className="px-3 py-2 whitespace-nowrap">
                        <CapacityMeter label="engines" {...clusterCapacity(c)} />
                      </td>
                      <td className="px-3 py-2 whitespace-nowrap">{c.namespace}</td>
                      <td className="px-3 py-2">
                        <code className="text-caption break-all">{c.sidecar_image || '—'}</code>
                      </td>
                      <td className="px-3 py-2">
                        <code className="text-caption break-all">{c.ingest_url || '—'}</code>
                      </td>
                      <td className="px-3 py-2">
                        <code className="text-caption break-all">{c.api_url || '—'}</code>
                      </td>
                      <td className="px-3 py-2">
                        <code className="text-caption break-all">{c.secret_ref || '—'}</code>
                      </td>
                      <td className="px-3 py-2 whitespace-nowrap">
                        <span className="text-slate-700 dark:text-slate-300">{formatClusterTime(c.created_time)}</span>
                        {c.created_by && (
                          <span className="text-caption block text-slate-500 dark:text-slate-400">by {c.created_by}</span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
