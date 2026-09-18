// Read-only view of the cluster registry (GET /api/clusters): where load
// can run and how each registered cluster routes. Writes -- registration,
// token rotation, deletion -- are deliberately not offered here; they stay
// API/CLI operations (phase 13 spec: the SPA must not become an auth
// surface). No health probing either: this shows stored registration
// state, not live connectivity.
import { useEffect, useState } from 'react';
import { Server } from 'lucide-react';
import Card, { CardContent } from '../components/ui/Card';
import CardTable, { type CardTableColumn } from '../components/CardTable';
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

// Phase 86: the registry table's column defs -- one source for the sm+
// table and the below-sm card list (the cluster name is the card title;
// the origin hint rides the row title in both branches). clusterCapacity
// and the code-cell renders stay exactly what they fed the old <td>s.
const registryColumns: CardTableColumn<Cluster>[] = [
  {
    key: 'name',
    header: 'Name',
    primary: true,
    thClassName: 'px-3 py-2 font-medium',
    tdClassName: 'px-3 py-2 font-medium whitespace-nowrap text-slate-900 dark:text-white',
    render: (c) => c.name,
  },
  {
    key: 'origin',
    header: 'Origin',
    thClassName: 'px-3 py-2 font-medium',
    tdClassName: 'px-3 py-2',
    render: (c) => <OriginBadge origin={c.origin} />,
  },
  {
    key: 'capacity',
    header: 'Capacity',
    thClassName: 'px-3 py-2 font-medium',
    tdClassName: 'px-3 py-2 whitespace-nowrap',
    render: (c) => <CapacityMeter label="engines" {...clusterCapacity(c)} />,
  },
  {
    // Phase 88: the fan-out executions mid-run on this cluster, served by
    // the registry list's fanout_running enrichment. The honest states are
    // all distinct: '—' (none mid-flight), the absent field (deployment
    // wired no run lookup), and the running names themselves.
    key: 'fanout_running',
    header: 'Fan-out in progress',
    thClassName: 'px-3 py-2 font-medium',
    tdClassName: 'px-3 py-2',
    render: (c) => {
      if (c.fanout_running === undefined) return <span className="text-slate-400">—</span>;
      if (c.fanout_running.length === 0) return <span className="text-slate-400">—</span>;
      return (
        <span className="text-caption" data-testid={`fanout-running-${c.name}`}>
          {c.fanout_running.map((e) => (
            <span key={e.execution_id} className="mr-2 inline-flex items-center gap-1">
              <span className="inline-flex items-center rounded-full bg-emerald-100 px-2 py-0.5 text-xs font-medium text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300">
                running
              </span>
              {e.name} (#{e.execution_id})
            </span>
          ))}
        </span>
      );
    },
  },
  {
    key: 'namespace',
    header: 'Engine namespace',
    thClassName: 'px-3 py-2 font-medium',
    tdClassName: 'px-3 py-2 whitespace-nowrap',
    render: (c) => c.namespace,
  },
  {
    key: 'sidecar_image',
    header: 'Sidecar image',
    thClassName: 'px-3 py-2 font-medium',
    tdClassName: 'px-3 py-2',
    render: (c) => <code className="text-caption break-all">{c.sidecar_image || '—'}</code>,
  },
  {
    key: 'ingest_url',
    header: 'Ingest URL',
    thClassName: 'px-3 py-2 font-medium',
    tdClassName: 'px-3 py-2',
    render: (c) => <code className="text-caption break-all">{c.ingest_url || '—'}</code>,
  },
  {
    key: 'api_url',
    header: 'API server',
    thClassName: 'px-3 py-2 font-medium',
    tdClassName: 'px-3 py-2',
    render: (c) => <code className="text-caption break-all">{c.api_url || '—'}</code>,
  },
  {
    key: 'secret_ref',
    header: 'Credential Secret',
    thClassName: 'px-3 py-2 font-medium',
    tdClassName: 'px-3 py-2',
    render: (c) => <code className="text-caption break-all">{c.secret_ref || '—'}</code>,
  },
  {
    key: 'registered',
    header: 'Registered',
    thClassName: 'px-3 py-2 font-medium',
    tdClassName: 'px-3 py-2 whitespace-nowrap',
    render: (c) => (
      <>
        <span className="text-slate-700 dark:text-slate-300">{formatClusterTime(c.created_time)}</span>
        {c.created_by && (
          <span className="text-caption block text-slate-500 dark:text-slate-400">by {c.created_by}</span>
        )}
      </>
    ),
  },
];

// Phase 86: the capacity matrix's column defs -- the pod size (the row's
// identity) is the card title below sm; rows arrive in the backend's
// order and the calibrated dates stay short dates.
const matrixColumns: CardTableColumn<CapacityProfileSummary>[] = [
  {
    key: 'pod',
    header: 'Pod size',
    primary: true,
    thClassName: 'px-3 py-2 font-medium',
    tdClassName: 'px-3 py-2 whitespace-nowrap',
    render: (p) => (
      <code className="text-caption">
        {p.cpu} / {p.memory}
      </code>
    ),
  },
  {
    key: 'qps',
    header: 'Per-pod QPS',
    thClassName: 'px-3 py-2 font-medium',
    tdClassName: 'px-3 py-2 whitespace-nowrap',
    render: (p) => p.per_pod_qps,
  },
  {
    key: 'saturated_by',
    header: 'Saturated by',
    thClassName: 'px-3 py-2 font-medium',
    tdClassName: 'px-3 py-2',
    render: (p) => p.saturated_by,
  },
  {
    key: 'calibrated',
    header: 'Calibrated',
    thClassName: 'px-3 py-2 font-medium',
    tdClassName: 'px-3 py-2 whitespace-nowrap text-slate-700 dark:text-slate-300',
    render: (p) => formatCalibratedShortDate(p.calibrated_at),
  },
];

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
            <CardTable
              tableTestId="clusters-table"
              columns={registryColumns}
              rows={clusters}
              rowKey={(c) => c.name}
              rowTitle={(c) => originDescription(c.origin)}
              headerRowClassName="text-caption border-b border-slate-200 text-slate-500 dark:border-slate-700 dark:text-slate-400"
              tbodyClassName="divide-y divide-slate-100 dark:divide-slate-800"
            />
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
                  /* Phase 86: shared CardTable -- the sm+ markup is the
                     matrix this card always had; below sm each pod size is
                     a card with its QPS/saturation/calibration pairs. */
                  <div className="mt-1">
                    <CardTable
                      tableTestId="capacity-matrix"
                      columns={matrixColumns}
                      rows={profiles}
                      rowKey={(p) => `${p.scenario_id}-${p.engine}-${p.cpu}-${p.memory}`}
                      headerRowClassName="text-caption border-b border-slate-200 text-slate-500 dark:border-slate-700 dark:text-slate-400"
                      tbodyClassName="divide-y divide-slate-100 dark:divide-slate-800"
                    />
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
