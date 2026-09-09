// Types and fetchers for the tenant admin surface (phase 35): GET/POST
// /api/tenants, GET /api/tenants/{id}, GET/PUT /api/tenants/{id}/quota,
// and GET/POST/DELETE /api/tenants/{id}/roles. Field names mirror
// internal/adapters/httpapi/tenant_handlers.go's response types exactly.
// Every mutation is form-encoded, matching the backend's ParseForm
// contract; the revoke is query-parameter-based like the handler reads.
import { apiClient } from './client';

export interface Tenant {
  id: number;
  name: string;
  display_name: string;
  status: string;
  created_time: string;
}

/** GET /api/tenants/{id}/quota: the ceiling for one cluster (0 = unset). */
export interface TenantQuota {
  cluster: string;
  ceiling: number;
}

/** One member-roster entry from GET /api/tenants/{id}/roles. */
export interface TenantRoleGrant {
  subject: string;
  email: string;
  role: string;
  granted_by: string;
  granted_time: string;
}

/** The tenant-scoped roles the backend catalog offers for granting
 *  (rbac.RoleTenantAdmin/Editor/Viewer). Global roles are deliberately
 *  absent: they are the service provider's, granted via POST /api/roles,
 *  which this page does not surface. */
export const TENANT_ROLES = ['tenant_admin', 'tenant_editor', 'tenant_viewer'] as const;

export type TenantRole = (typeof TENANT_ROLES)[number];

/** Lists every tenant. Service-provider admins only: the route's gate is
 *  unscoped, so a tenant admin gets 403 and must use their session's
 *  tenant ids with getTenant instead. */
export function listTenants(): Promise<Tenant[]> {
  return apiClient.get<Tenant[]>('/tenants');
}

/** Fetches one tenant by id. Tenant admins pass the scoped gate for their
 *  own tenant, so this works where listTenants 403s. */
export function getTenant(tenantId: number): Promise<Tenant> {
  return apiClient.get<Tenant>(`/tenants/${tenantId}`);
}

export function createTenant(name: string, displayName: string): Promise<Tenant> {
  const form = new URLSearchParams();
  form.set('name', name);
  form.set('display_name', displayName);
  return apiClient.post<Tenant>('/tenants', form);
}

/** Reads a tenant's quota ceiling for a cluster ("" is the default). */
export function getTenantQuota(tenantId: number, cluster = ''): Promise<TenantQuota> {
  const query = new URLSearchParams({ cluster });
  return apiClient.get<TenantQuota>(`/tenants/${tenantId}/quota?${query.toString()}`);
}

/** Sets a tenant's quota ceiling for a cluster. PUT + form-encoded, per the
 *  backend's setTenantQuota contract. */
export function setTenantQuota(tenantId: number, cluster: string, ceiling: number): Promise<void> {
  const form = new URLSearchParams();
  form.set('cluster', cluster);
  form.set('ceiling', String(ceiling));
  return apiClient.putRaw(`/tenants/${tenantId}/quota`, 'application/x-www-form-urlencoded', form.toString());
}

/** A tenant's member roster. */
export function listTenantRoles(tenantId: number): Promise<TenantRoleGrant[]> {
  return apiClient.get<TenantRoleGrant[]>(`/tenants/${tenantId}/roles`);
}

export interface AssignTenantRoleInput {
  subject: string;
  email: string;
  role: TenantRole;
}

export function assignTenantRole(tenantId: number, input: AssignTenantRoleInput): Promise<void> {
  const form = new URLSearchParams();
  form.set('subject', input.subject);
  form.set('email', input.email);
  form.set('role', input.role);
  return apiClient.post<void>(`/tenants/${tenantId}/roles`, form);
}

/** Revocation names the grant in the query string (subject + role), matching
 *  the DELETE handler's r.URL.Query reads. */
export function revokeTenantRole(tenantId: number, subject: string, role: string): Promise<void> {
  const query = new URLSearchParams({ subject, role });
  return apiClient.request<void>(`/tenants/${tenantId}/roles?${query.toString()}`, { method: 'DELETE' });
}
