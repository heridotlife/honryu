// Types and fetchers for the campaigns feature: POST/GET
// /api/tenants/{tenant_id}/campaigns, GET /api/campaigns/{campaign_id}, and
// GET /api/campaigns/{campaign_id}/verdict. Field names mirror
// internal/adapters/httpapi/campaign_handlers.go's response types exactly.
import { apiClient } from './client';

export interface CampaignService {
  project_id: number;
  execution_id: number;
}

export interface Campaign {
  id: number;
  name: string;
  tenant_id: number;
  window_start: string;
  window_end: string;
  services: CampaignService[];
  active: boolean;
  aborted_at?: string;
}

export interface CreateCampaignInput {
  name: string;
  windowStart: Date;
  windowEnd: Date;
  services: CampaignService[];
}

export function createCampaign(tenantId: number, input: CreateCampaignInput): Promise<Campaign> {
  const form = new URLSearchParams();
  form.set('name', input.name);
  form.set('window_start', input.windowStart.toISOString());
  form.set('window_end', input.windowEnd.toISOString());
  for (const svc of input.services) {
    form.append('service_project_id', String(svc.project_id));
    form.append('service_execution_id', String(svc.execution_id));
  }
  return apiClient.post<Campaign>(`/tenants/${tenantId}/campaigns`, form);
}

export function listTenantCampaigns(tenantId: number): Promise<Campaign[]> {
  return apiClient.get<Campaign[]>(`/tenants/${tenantId}/campaigns`);
}

export function getCampaign(campaignId: number): Promise<Campaign> {
  return apiClient.get<Campaign>(`/campaigns/${campaignId}`);
}

/**
 * GET /api/campaigns -- every campaign across the caller's visible tenants
 * (RBAC-scoped server-side; heri-mp sees marketplace only, ops-admin sees
 * all). The list panel loads this on mount so no one types a tenant id
 * just to see what exists. Nil-normalized for the same reason as
 * listProjects.
 */
export async function listAllCampaigns(): Promise<Campaign[]> {
  const got = await apiClient.get<Campaign[] | null>('/campaigns');
  return got ?? [];
}

export type Outcome = 'passed' | 'failed' | 'aborted' | 'error';

export interface FailingCriterion {
  criterion: string;
  unparsed?: boolean;
}

export interface ServiceVerdict {
  project_id: number;
  execution_id: number;
  has_report: boolean;
  outcome?: Outcome;
  failing_criteria?: FailingCriterion[];
}

export interface OtherLoad {
  execution_id: number;
  start: string;
  end: string;
  engine_count: number;
}

export interface CampaignVerdict {
  campaign_id: number;
  services: ServiceVerdict[];
  go: boolean;
  other_load?: OtherLoad[];
}

/**
 * failing_criteria and other_load are omitempty on the wire -- absent, not
 * [], when there's nothing to report (see internal/adapters/httpapi's
 * serviceVerdictResponse/campaignVerdictResponse) -- normalized here so
 * callers can always treat them as arrays.
 */
export async function getCampaignVerdict(campaignId: number): Promise<CampaignVerdict> {
  const got = await apiClient.get<CampaignVerdict>(`/campaigns/${campaignId}/verdict`);
  return {
    ...got,
    services: got.services.map((sv) => ({ ...sv, failing_criteria: sv.failing_criteria ?? [] })),
    other_load: got.other_load ?? [],
  };
}
