import { useEffect, useState } from 'react';
import Button from '../components/ui/Button';
import Card, { CardContent, CardHeader, CardTitle } from '../components/ui/Card';
import Input from '../components/ui/Input';
import { ApiError } from '../api/client';
import { createCampaign, getCampaignVerdict, listTenantCampaigns } from '../api/campaigns';
import type { Campaign, CampaignVerdict, Outcome, ServiceVerdict } from '../api/campaigns';
import { getCampaignComparison } from '../api/comparison';
import type { CampaignComparison, ComparisonStatus } from '../api/comparison';
import { useSession } from '../hooks/useSession';

type CampaignStatus = 'upcoming' | 'active' | 'ended' | 'aborted';

const campaignStatusClasses: Record<CampaignStatus, string> = {
  upcoming: 'bg-sky-100 text-sky-800 dark:bg-sky-900/30 dark:text-sky-300',
  active: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300',
  ended: 'bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-300',
  aborted: 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300',
};

// Recomputed from the window and aborted_at, the same rule
// campaign.Campaign.IsActive applies server-side, rather than trusting the
// response's own `active` flag -- which is only a snapshot from whenever
// the list was last loaded and goes stale on a long-lived page.
function campaignStatus(c: Campaign, now: Date): CampaignStatus {
  if (c.aborted_at) return 'aborted';
  const start = new Date(c.window_start);
  const end = new Date(c.window_end);
  if (now < start) return 'upcoming';
  if (now >= end) return 'ended';
  return 'active';
}

function CampaignStatusBadge({ status }: { status: CampaignStatus }) {
  return (
    <span className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${campaignStatusClasses[status]}`}>
      {status}
    </span>
  );
}

type ServiceStatus = Outcome | 'pending';

const serviceStatusClasses: Record<ServiceStatus, string> = {
  passed: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300',
  failed: 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300',
  aborted: 'bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-300',
  error: 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300',
  pending: 'bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-300',
};

// A service with no report yet (never run, or still mid-run) is distinct
// from any of taurus.Outcome's own values -- has_report:false is the only
// case where outcome is absent, per serviceVerdictResponse's omitempty.
function serviceStatus(sv: ServiceVerdict): ServiceStatus {
  if (!sv.has_report || !sv.outcome) return 'pending';
  return sv.outcome;
}

function ServiceStatusBadge({ status }: { status: ServiceStatus }) {
  return (
    <span className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${serviceStatusClasses[status]}`}>
      {status}
    </span>
  );
}

/** Display labels for the wire's snake_case classifications (newly_at_risk -> "newly at risk"). */
export const comparisonStatusLabels: Record<ComparisonStatus, string> = {
  improved: 'improved',
  regressed: 'regressed',
  newly_at_risk: 'newly at risk',
  still_at_risk: 'still at risk',
  steady: 'steady',
  new: 'new',
  dropped: 'dropped',
};

// Distinct per classification: the go/no-go direction the chip encodes
// (emerald = go-positive, red = go-negative, amber/orange = risk, slate =
// unchanged/neutral, sky = participation without baseline, violet = left).
export const comparisonStatusClasses: Record<ComparisonStatus, string> = {
  improved: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300',
  regressed: 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300',
  newly_at_risk: 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300',
  still_at_risk: 'bg-orange-100 text-orange-800 dark:bg-orange-900/30 dark:text-orange-300',
  steady: 'bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-300',
  new: 'bg-sky-100 text-sky-800 dark:bg-sky-900/30 dark:text-sky-300',
  dropped: 'bg-violet-100 text-violet-800 dark:bg-violet-900/30 dark:text-violet-300',
};

/**
 * The go/no-go transition a classification summarizes, derived from the
 * classification itself (the server classified from the same booleans).
 * new/dropped are participation changes, not verdict transitions.
 */
export function comparisonTransition(status: ComparisonStatus): string {
  switch (status) {
    case 'improved':
      return 'no-go → go';
    case 'regressed':
      return 'go → no-go';
    case 'steady':
      return 'go → go';
    case 'still_at_risk':
      return 'no-go → no-go';
    case 'newly_at_risk':
      return 'new: no-go';
    case 'new':
      return 'new: go';
    case 'dropped':
      return 'left this campaign';
  }
}

/** Baseline-override input: a positive campaign id, 'empty' (use default resolution), or 'invalid'. */
export function parseBaselineId(raw: string): number | 'empty' | 'invalid' {
  if (raw.trim() === '') {
    return 'empty';
  }
  const n = Number(raw);
  return Number.isInteger(n) && n > 0 ? n : 'invalid';
}

function ComparisonStatusBadge({ status }: { status: ComparisonStatus }) {
  return (
    <span className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${comparisonStatusClasses[status]}`}>
      {comparisonStatusLabels[status]}
    </span>
  );
}

function formatTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

interface ServiceRow {
  projectId: string;
  executionId: string;
}

function emptyRow(): ServiceRow {
  return { projectId: '', executionId: '' };
}

/**
 * Per-service go/no-go comparison against a baseline campaign (phase 9's
 * comparison endpoint): the baseline defaults to the tenant's most-recent
 * prior ended campaign; an override input passes ?baseline=. No baseline
 * resolvable (the tenant's first campaign) renders as information, never
 * an error.
 */
function ComparisonPanel({ campaignId }: { campaignId: number }) {
  const [comparison, setComparison] = useState<CampaignComparison | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [baselineInput, setBaselineInput] = useState('');
  const [baselineError, setBaselineError] = useState<string | null>(null);
  // null = default resolution (most recent prior ended campaign).
  const [baselineOverride, setBaselineOverride] = useState<number | null>(null);

  useEffect(() => {
    setLoading(true);
    setError(null);
    getCampaignComparison(campaignId, baselineOverride ?? undefined)
      .then(setComparison)
      .catch((err: unknown) => setError(err instanceof ApiError ? err.message : 'Failed to load comparison.'))
      .finally(() => setLoading(false));
  }, [campaignId, baselineOverride]);

  const applyBaseline = () => {
    const parsed = parseBaselineId(baselineInput);
    if (parsed === 'invalid') {
      setBaselineError('Enter a positive campaign id, or leave empty for the default baseline.');
      return;
    }
    setBaselineError(null);
    setBaselineOverride(parsed === 'empty' ? null : parsed);
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>Comparison against baseline</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <form
          className="flex flex-col gap-3 sm:flex-row sm:items-end"
          onSubmit={(e) => {
            e.preventDefault();
            applyBaseline();
          }}
        >
          <div className="w-64">
            <Input
              label="Baseline campaign ID"
              type="number"
              min={1}
              value={baselineInput}
              onChange={(e) => setBaselineInput(e.target.value)}
              placeholder="default: latest ended"
            />
          </div>
          <Button type="submit" variant="secondary" disabled={loading}>
            Compare
          </Button>
        </form>
        {baselineError && (
          <p className="text-sm text-red-600 dark:text-red-400" role="alert">
            {baselineError}
          </p>
        )}

        {loading && <p className="text-body-sm text-slate-500 dark:text-slate-400">Loading comparison…</p>}
        {error && (
          <p className="text-sm text-red-600 dark:text-red-400" role="alert">
            {error}
          </p>
        )}
        {!loading && !error && comparison && (
          <>
            {!comparison.has_baseline ? (
              // First campaign (no prior ended campaign, none given): the
              // endpoint returns has_baseline:false with no services -- an
              // expected state, rendered as such.
              <p className="text-body-sm text-slate-500 dark:text-slate-400">
                First campaign — no baseline to compare against yet. The next campaign in this tenant will compare
                against this one.
              </p>
            ) : (
              <>
                <p className="text-caption text-slate-500 dark:text-slate-400">
                  {baselineOverride
                    ? `vs campaign #${comparison.baseline_campaign_id ?? baselineOverride} (explicit baseline)`
                    : `vs campaign #${comparison.baseline_campaign_id} (most recent ended)`}
                </p>
                {comparison.services.length === 0 ? (
                  <p className="text-body-sm text-slate-500 dark:text-slate-400">
                    No per-service changes against this baseline.
                  </p>
                ) : (
                  <ul className="space-y-2">
                    {comparison.services.map((sc) => (
                      <li key={sc.project_id} className="rounded-lg border border-slate-200 p-3 dark:border-slate-700">
                        <div className="flex flex-wrap items-center gap-3">
                          <ComparisonStatusBadge status={sc.status} />
                          <span className="text-body-sm font-medium text-slate-900 dark:text-white">
                            project #{sc.project_id}
                          </span>
                          <span className="text-caption text-slate-500 dark:text-slate-400">
                            {comparisonTransition(sc.status)}
                          </span>
                        </div>
                      </li>
                    ))}
                  </ul>
                )}
              </>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}

/** Create a campaign, browse a tenant's campaigns, and view a campaign's rolled-up verdict. */
export default function Campaigns() {
  const { can } = useSession();
  const canCreate = can('campaign', 'create');
  const [tenantId, setTenantId] = useState('');
  const [campaigns, setCampaigns] = useState<Campaign[] | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  const [listLoading, setListLoading] = useState(false);

  const [name, setName] = useState('');
  const [windowStart, setWindowStart] = useState('');
  const [windowEnd, setWindowEnd] = useState('');
  const [rows, setRows] = useState<ServiceRow[]>([emptyRow()]);
  const [createError, setCreateError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [verdict, setVerdict] = useState<CampaignVerdict | null>(null);
  const [verdictError, setVerdictError] = useState<string | null>(null);
  const [verdictLoading, setVerdictLoading] = useState(false);

  const loadCampaigns = async (tenantIdNum: number) => {
    setListLoading(true);
    setListError(null);
    try {
      setCampaigns(await listTenantCampaigns(tenantIdNum));
    } catch (err) {
      setCampaigns(null);
      setListError(err instanceof ApiError ? err.message : 'Failed to load campaigns.');
    } finally {
      setListLoading(false);
    }
  };

  const handleLoad = () => {
    const tenantIdNum = Number(tenantId);
    if (!tenantId || !Number.isInteger(tenantIdNum) || tenantIdNum <= 0) {
      setListError('Enter a valid tenant id.');
      return;
    }
    void loadCampaigns(tenantIdNum);
  };

  const selectCampaign = async (id: number) => {
    setSelectedId(id);
    setVerdict(null);
    setVerdictError(null);
    setVerdictLoading(true);
    try {
      setVerdict(await getCampaignVerdict(id));
    } catch (err) {
      setVerdictError(err instanceof ApiError ? err.message : 'Failed to load verdict.');
    } finally {
      setVerdictLoading(false);
    }
  };

  const handleCreate = async () => {
    const tenantIdNum = Number(tenantId);
    if (!tenantId || !Number.isInteger(tenantIdNum) || tenantIdNum <= 0) {
      setCreateError('Enter a valid tenant id above before creating a campaign.');
      return;
    }
    if (!name.trim()) {
      setCreateError('Name is required.');
      return;
    }
    const start = windowStart ? new Date(windowStart) : undefined;
    const end = windowEnd ? new Date(windowEnd) : undefined;
    if (!start || Number.isNaN(start.getTime()) || !end || Number.isNaN(end.getTime())) {
      setCreateError('Enter valid window start/end dates.');
      return;
    }
    const services = [];
    for (const row of rows) {
      if (!row.projectId && !row.executionId) continue;
      const projectId = Number(row.projectId);
      const executionId = Number(row.executionId);
      if (!Number.isInteger(projectId) || projectId <= 0 || !Number.isInteger(executionId) || executionId <= 0) {
        setCreateError('Each service row needs a valid project id and execution id.');
        return;
      }
      services.push({ project_id: projectId, execution_id: executionId });
    }
    if (services.length === 0) {
      setCreateError('Add at least one participating service.');
      return;
    }

    setCreating(true);
    setCreateError(null);
    try {
      const created = await createCampaign(tenantIdNum, { name, windowStart: start, windowEnd: end, services });
      setName('');
      setWindowStart('');
      setWindowEnd('');
      setRows([emptyRow()]);
      await loadCampaigns(tenantIdNum);
      await selectCampaign(created.id);
    } catch (err) {
      setCreateError(err instanceof ApiError ? err.message : 'Failed to create campaign.');
    } finally {
      setCreating(false);
    }
  };

  const updateRow = (index: number, patch: Partial<ServiceRow>) => {
    setRows((prev) => prev.map((r, i) => (i === index ? { ...r, ...patch } : r)));
  };

  const now = new Date();
  const selectedCampaign = campaigns?.find((c) => c.id === selectedId) ?? null;

  return (
    <div className="space-y-6" role="region" aria-label="Campaigns panel">
      <div>
        <h1 className="text-display-sm text-slate-900 dark:text-white">Campaigns</h1>
        <p className="text-body-sm mt-1 text-slate-500 dark:text-slate-400">
          PM-owned readiness events: a window, participating services, and a rolled-up go/no-go.
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Create a campaign</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <Input
            label="Tenant ID"
            type="number"
            min={1}
            value={tenantId}
            onChange={(e) => setTenantId(e.target.value)}
            placeholder="e.g. 1"
            fullWidth
          />
          <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Supersale 11.11" fullWidth />
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Input
              label="Window start"
              type="datetime-local"
              value={windowStart}
              onChange={(e) => setWindowStart(e.target.value)}
              fullWidth
            />
            <Input
              label="Window end"
              type="datetime-local"
              value={windowEnd}
              onChange={(e) => setWindowEnd(e.target.value)}
              fullWidth
            />
          </div>

          <div>
            <p className="mb-2 block text-sm font-medium text-slate-700 dark:text-slate-300">Participating services</p>
            <div className="space-y-3">
              {rows.map((row, i) => (
                <div key={i} className="flex flex-col gap-2 sm:flex-row sm:items-end">
                  <Input
                    label="Project ID"
                    type="number"
                    min={1}
                    value={row.projectId}
                    onChange={(e) => updateRow(i, { projectId: e.target.value })}
                    fullWidth
                  />
                  <Input
                    label="Designated execution ID"
                    type="number"
                    min={1}
                    value={row.executionId}
                    onChange={(e) => updateRow(i, { executionId: e.target.value })}
                    fullWidth
                  />
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() => setRows((prev) => prev.filter((_, idx) => idx !== i))}
                    disabled={rows.length === 1}
                  >
                    Remove
                  </Button>
                </div>
              ))}
            </div>
            <Button type="button" variant="secondary" size="sm" className="mt-3" onClick={() => setRows((prev) => [...prev, emptyRow()])}>
              Add service
            </Button>
          </div>

          {canCreate ? (
            <Button
              type="button"
              disabled={creating}
              onClick={() => {
                void handleCreate();
              }}
            >
              {creating ? 'Creating…' : 'Create campaign'}
            </Button>
          ) : (
            <p className="text-sm text-slate-500 dark:text-slate-400" data-testid="no-create-permission">
              Your role cannot create campaigns.
            </p>
          )}
          {createError && (
            <p className="text-sm text-red-600 dark:text-red-400" role="alert">
              {createError}
            </p>
          )}
        </CardContent>
      </Card>

      <Card>
        <form
          className="flex flex-col gap-4 sm:flex-row sm:items-end"
          onSubmit={(e) => {
            e.preventDefault();
            handleLoad();
          }}
        >
          <Input
            label="Tenant ID"
            type="number"
            min={1}
            value={tenantId}
            onChange={(e) => setTenantId(e.target.value)}
            placeholder="e.g. 1"
            fullWidth
          />
          <Button type="submit" disabled={listLoading}>
            {listLoading ? 'Loading…' : 'Load campaigns'}
          </Button>
        </form>
        {listError && (
          <p className="mt-4 text-sm text-red-600 dark:text-red-400" role="alert">
            {listError}
          </p>
        )}
      </Card>

      {campaigns && (
        <Card padding="none">
          {campaigns.length === 0 ? (
            <p className="text-body-sm p-6 text-slate-500 dark:text-slate-400">No campaigns for this tenant.</p>
          ) : (
            <ul className="divide-y divide-slate-200 dark:divide-slate-700">
              {campaigns.map((c) => (
                <li key={c.id}>
                  <button
                    type="button"
                    onClick={() => void selectCampaign(c.id)}
                    className={`flex min-h-[44px] w-full flex-col gap-2 rounded p-4 text-left transition-colors duration-200 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 sm:flex-row sm:items-center sm:justify-between ${
                      selectedId === c.id ? 'bg-sky-50 dark:bg-sky-900/20' : 'hover:bg-slate-50 dark:hover:bg-slate-800'
                    }`}
                  >
                    <div className="flex items-center gap-3">
                      <CampaignStatusBadge status={campaignStatus(c, now)} />
                      <span className="text-body-sm font-medium text-slate-900 dark:text-white">{c.name}</span>
                    </div>
                    <span className="text-caption text-slate-500 dark:text-slate-400">
                      {formatTime(c.window_start)} – {formatTime(c.window_end)}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Card>
      )}

      {selectedId !== null && (
        <Card>
          <CardHeader className="flex flex-row items-center justify-between">
            <CardTitle>{selectedCampaign ? selectedCampaign.name : `Campaign #${selectedId}`}</CardTitle>
            {verdict && (
              <span
                className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${
                  verdict.go
                    ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300'
                    : 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300'
                }`}
              >
                {verdict.go ? 'go' : 'no-go'}
              </span>
            )}
          </CardHeader>
          <CardContent className="space-y-4">
            {verdictLoading && <p className="text-body-sm text-slate-500 dark:text-slate-400">Loading verdict…</p>}
            {verdictError && (
              <p className="text-sm text-red-600 dark:text-red-400" role="alert">
                {verdictError}
              </p>
            )}
            {verdict && (
              <>
                <ul className="space-y-2">
                  {verdict.services.map((sv) => (
                    <li
                      key={sv.execution_id}
                      className="rounded-lg border border-slate-200 p-3 dark:border-slate-700"
                    >
                      <div className="flex flex-wrap items-center gap-3">
                        <ServiceStatusBadge status={serviceStatus(sv)} />
                        <span className="text-body-sm font-medium text-slate-900 dark:text-white">
                          project #{sv.project_id}
                        </span>
                        <span className="text-caption text-slate-500 dark:text-slate-400">
                          execution #{sv.execution_id}
                        </span>
                      </div>
                      {sv.failing_criteria && sv.failing_criteria.length > 0 && (
                        <ul className="mt-2 space-y-1">
                          {sv.failing_criteria.map((fc) => (
                            <li key={fc.criterion} className="text-caption text-red-600 dark:text-red-400">
                              {fc.criterion}
                              {fc.unparsed ? ' (could not be evaluated)' : ''}
                            </li>
                          ))}
                        </ul>
                      )}
                    </li>
                  ))}
                </ul>

                <div>
                  <h3 className="text-caption mb-2 font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400">
                    Other load active during this window
                  </h3>
                  {!verdict.other_load || verdict.other_load.length === 0 ? (
                    <p className="text-body-sm text-slate-500 dark:text-slate-400">
                      No other reservations or executions were active in this tenant during the window.
                    </p>
                  ) : (
                    <ul className="space-y-1">
                      {verdict.other_load.map((ol) => (
                        <li key={ol.execution_id} className="text-caption text-slate-500 dark:text-slate-400">
                          execution #{ol.execution_id} · {formatTime(ol.start)}–{formatTime(ol.end)} · {ol.engine_count}{' '}
                          {ol.engine_count === 1 ? 'engine' : 'engines'}
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              </>
            )}
          </CardContent>
        </Card>
      )}

      {selectedId !== null && <ComparisonPanel key={selectedId} campaignId={selectedId} />}
    </div>
  );
}

export { campaignStatus, serviceStatus };
