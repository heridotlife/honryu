// Read-only view of the cluster registry (GET /api/clusters): where load
// can run and how each registered cluster routes. Writes -- registration,
// token rotation, deletion -- are deliberately not offered here; they stay
// API/CLI operations (phase 13 spec: the SPA must not become an auth
// surface). No health probing either: this shows stored registration
// state, not live connectivity.
import { useEffect, useState } from 'react';
import { Server } from 'lucide-react';
import Card, { CardContent } from '../components/ui/Card';
import EmptyState from '../components/EmptyState';
import { ApiError } from '../api/client';
import { listClusters } from '../api/clusters';
import type { Cluster, ClusterOrigin } from '../api/clusters';
import { listCapacityProfiles } from '../api/calibration';
import type { CapacityProfileSummary } from '../api/calibration';
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

/** A profile's calibration time as a short date (house rule: no raw ISO on
 * screen), NaN-safe the same way formatClusterTime is. */
export function formatCalibratedShortDate(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString();
}

/** Capacity numbers for one cluster row, when the backend offers any.
 * Phase 25 wired the fields: GET /api/clusters' listClusters handler runs
 * every row through withCapacity, which fills engines_used/engines_ceiling
 * when the quota ledger is wired (nil Quota or a failed read omits them via
 * pointers + omitempty -- wire shape stays backward compatible). Both
 * fields must be present -- one without the other is a half-wired read,
 * and the meter's no-data state is the honest render for that too.
 *
 * Known gap (phase 55; backend out of scope): the endpoint lists
 * registered clusters only -- the deployment's default cluster never gets
 * a row, so its capacity is structurally invisible on this page even when
 * the quota ledger covers it. Nothing here can render it today. */
export function clusterCapacity(cluster: Cluster): { used?: number; ceiling?: number } {
  if (typeof cluster.engines_used === 'number' && typeof cluster.engines_ceiling === 'number') {
    return { used: cluster.engines_used, ceiling: cluster.engines_ceiling };
  }
  return {};
}

/** One unique engine image across the registry, and the clusters that run
 * it (registry order, first-seen image order, blanks dropped) -- the fleet
 * summary's engine-image rows render exactly what cluster rows carry
 * (phase 55: the engine images config has no API surface, so there is
 * nothing else to show; phase 61: one line per unique image, attributed
 * across the fleet). */
export function engineImageRows(clusters: Cluster[]): { image: string; clusters: string[] }[] {
  const rows: { image: string; clusters: string[] }[] = [];
  const byImage = new Map<string, string[]>();
  for (const c of clusters) {
    if (c.sidecar_image === '') continue;
    let names = byImage.get(c.sidecar_image);
    if (names === undefined) {
      names = [];
      byImage.set(c.sidecar_image, names);
      rows.push({ image: c.sidecar_image, clusters: names });
    }
    names.push(c.name);
  }
  return rows;
}

/** Distinct sidecar images across the registry, first-seen order, blanks
 * dropped -- the image half of engineImageRows. */
export function engineImages(clusters: Cluster[]): string[] {
  return engineImageRows(clusters).map((r) => r.image);
}

/** The cluster registry, read-only. */
export default function Clusters() {
  const [clusters, setClusters] = useState<Cluster[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  // The fleet-wide capacity matrix (phase 56): loaded alongside the
  // registry, but independently -- a profiles failure must not take the
  // registry view down with it.
  const [profiles, setProfiles] = useState<CapacityProfileSummary[] | null>(null);
  const [profilesError, setProfilesError] = useState(false);
  const fleetImageRows = clusters === null ? null : engineImageRows(clusters);

  useEffect(() => {
    listClusters()
      .then(setClusters)
      .catch((err: unknown) => setError(err instanceof ApiError ? err.message : 'Failed to load clusters.'));
    listCapacityProfiles()
      .then(setProfiles)
      .catch(() => setProfilesError(true));
  }, []);

  return (
    <div className="space-y-6" role="region" aria-label="Clusters panel">
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

          {/* Phase 76: loading is its own state -- skeleton geometry, so
              it can never blur into the empty state below. */}
          {clusters === null && !error && (
            <div className="space-y-2" data-testid="clusters-loading">
              {[0, 1, 2].map((i) => (
                <div key={i} className="h-10 animate-pulse rounded-lg bg-slate-100 dark:bg-slate-700/50" />
              ))}
            </div>
          )}

          {clusters && clusters.length === 0 && (
            <EmptyState
              testId="clusters-empty"
              icon={<Server className="size-6" />}
              title="No registered clusters"
              description="All load runs on the deployment's default cluster. Registering a cluster is an API operation — POST /api/clusters and friends."
            />
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

      {clusters && (
        <Card role="region" aria-label="Fleet summary">
          <CardContent className="space-y-3">
            <h2 className="text-caption font-medium tracking-wide text-slate-500 uppercase dark:text-slate-400">
              Fleet summary
            </h2>
            <p className="text-caption text-slate-500 dark:text-slate-400">Engine images</p>
            {fleetImageRows && fleetImageRows.length > 0 ? (
              /* Phase 61: one compact line per unique image, attributed
                 across the fleet -- a fleet running mixed engine versions
                 reads which clusters are on which image without scanning
                 the registry table. */
              <div data-testid="fleet-engine-images" className="space-y-1">
                {fleetImageRows.map(({ image, clusters: names }) => (
                  <p key={image} className="text-body-sm">
                    <code className="text-caption rounded bg-slate-100 px-1.5 py-0.5 break-all dark:bg-slate-900">
                      {image}
                    </code>
                    <span className="text-caption ml-2 text-slate-500 dark:text-slate-400">
                      {names.length} {names.length === 1 ? 'cluster' : 'clusters'}: {names.join(', ')}
                    </span>
                  </p>
                ))}
              </div>
            ) : (
              <p className="text-body-sm text-slate-500 dark:text-slate-400">
                {clusters !== null && clusters.length > 0
                  ? 'No registered cluster carries an engine image — set one when registering.'
                  : 'No registered clusters — engine images appear here once clusters are registered.'}
              </p>
            )}

            {profilesError && (
              <p className="text-body-sm text-slate-500 dark:text-slate-400">
                Capacity profiles could not be loaded.
              </p>
            )}

            {profiles !== null && !profilesError && (
              <div>
                <p className="text-caption text-slate-500 dark:text-slate-400">Capacity matrix</p>
                {profiles.length > 0 ? (
                  <div className="mt-1 overflow-x-auto">
                    {/* Rows arrive in the backend's order: scenario ascending,
                        then biggest pod first (cpu compared as milli-cores).
                        Calibrated dates render as short dates -- no raw ISO. */}
                    <table className="w-full text-left text-body-sm">
                      <thead>
                        <tr className="text-caption border-b border-slate-200 text-slate-500 dark:border-slate-700 dark:text-slate-400">
                          <th scope="col" className="px-3 py-2 font-medium">Pod size</th>
                          <th scope="col" className="px-3 py-2 font-medium">Per-pod QPS</th>
                          <th scope="col" className="px-3 py-2 font-medium">Saturated by</th>
                          <th scope="col" className="px-3 py-2 font-medium">Calibrated</th>
                        </tr>
                      </thead>
                      <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                        {profiles.map((p) => (
                          <tr key={`${p.scenario_id}-${p.engine}-${p.cpu}-${p.memory}`}>
                            <td className="px-3 py-2 whitespace-nowrap">
                              <code className="text-caption">
                                {p.cpu} / {p.memory}
                              </code>
                            </td>
                            <td className="px-3 py-2 whitespace-nowrap">{p.per_pod_qps}</td>
                            <td className="px-3 py-2">{p.saturated_by}</td>
                            <td className="px-3 py-2 whitespace-nowrap text-slate-700 dark:text-slate-300">
                              {formatCalibratedShortDate(p.calibrated_at)}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                ) : (
                  <p className="text-body-sm mt-1 text-slate-500 dark:text-slate-400">
                    No calibrations yet — run one and its per-pod capacity appears here.
                  </p>
                )}
              </div>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
