// Types and fetchers for projects (GET /api/projects). Field names mirror
// the project handlers' JSON in internal/adapters/httpapi -- the same wire
// shape NewTest's project resolution (pages/NewTest.tsx) already consumes
// inline; this module is the shared, typed front door for it.
import { apiClient } from './client';

/** One project row: the execution's owning scope the switcher filters by. */
export interface Project {
  id: number;
  name: string;
  owner: string;
  tenant_id: number;
  created_time: string;
}

/**
 * GET /api/projects -- every project the caller may see. Go marshals a nil
 * slice as null, so the empty tenant normalizes here to [] and callers can
 * always treat the result as an array (same precedent as getCampaignVerdict).
 */
export async function listProjects(): Promise<Project[]> {
  const got = await apiClient.get<Project[] | null>('/projects');
  return got ?? [];
}
