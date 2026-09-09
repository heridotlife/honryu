// Types and fetchers for the demo-session endpoints (phase 20): the
// picker's persona list, select/logout, and GET /api/me -- the SPA's single
// source of identity. The session cookie is HttpOnly, so nothing here ever
// touches it: cookies ride along on same-origin fetch by default, which is
// exactly why the session is a cookie and not a bearer token (EventSource
// cannot set headers, and Live Status depends on it).
import { apiClient } from './client';

/** One picker entry from GET /api/session/profiles. */
export interface SessionProfile {
  id: string;
  name: string;
}

/**
 * GET /api/me: who the caller is and what they may do. The permission map
 * mirrors authapp.Service.Permissions -- resource name -> sorted actions --
 * and is the single source the nav and every action button shape from.
 */
export interface SessionInfo {
  subject: string;
  name: string;
  email: string;
  global_roles: string[];
  /** tenant id -> role names held there; JSON object keys are strings. */
  tenants: Record<string, string[]>;
  /** resource -> actions; `{"*": ["*"]}` is the admin wildcard grant. */
  permissions: Record<string, string[]>;
  /** True whenever the demo session provider backs this deployment. */
  demo: boolean;
}

/** The picker's persona list; 404 (ApiError) when demo mode is off. */
export async function listSessionProfiles(): Promise<SessionProfile[]> {
  const got = await apiClient.get<{ profiles: SessionProfile[] | null }>('/session/profiles');
  return got.profiles ?? [];
}

/**
 * Selecting a persona IS the authentication (spec Approach E). The ONE
 * honryu mutation with a JSON body: every other mutating route is
 * form-encoded (ApiClient.post), but createSession parses JSON itself.
 */
export function createSession(profile: string): Promise<void> {
  return apiClient.request<void>('/session', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ profile }),
  });
}

/** Logout: the server expires the HttpOnly cookie. */
export function deleteSession(): Promise<void> {
  return apiClient.request<void>('/session', { method: 'DELETE' });
}

/** Who am I; throws ApiError with status 401 when unauthenticated. */
export function getMe(): Promise<SessionInfo> {
  return apiClient.get<SessionInfo>('/me');
}

/**
 * Whether a permission map grants resource:action, honouring both wildcard
 * shapes the backend can emit: the admin role's `{"*": ["*"]}` and a
 * per-resource "*". Must agree with rbac.Permission.Allows on the server.
 */
export function can(
  permissions: Record<string, string[]> | null | undefined,
  resource: string,
  action: string
): boolean {
  if (!permissions) {
    return false;
  }
  if (permissions['*']?.includes('*')) {
    return true;
  }
  const actions = permissions[resource];
  if (!actions) {
    return false;
  }
  return actions.includes(action) || actions.includes('*');
}

/**
 * Whether the session may administer tenants (phase 35's Tenants page and
 * nav link). True for the service-provider admin's wildcard and for any
 * holder of tenant:admin -- which the permission map grants a tenant's own
 * admins. Mirrors the backend's tenantAdminGate exactly: what the nav
 * shows, the routes behind it would let through.
 */
export function canAdminTenants(permissions: Record<string, string[]> | null | undefined): boolean {
  return can(permissions, 'tenant', 'admin');
}
