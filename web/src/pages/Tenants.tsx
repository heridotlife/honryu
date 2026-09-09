// The tenant admin console (phase 35): list tenants, inspect one, edit its
// per-cluster quota ceiling, manage its member role grants, and -- for the
// service-provider admin -- create tenants. The backend's tenantAdminGate
// is the authority: an unscoped route (list, create) admits only the
// service provider's admin, while the {tenant_id}-scoped routes also admit
// that tenant's own admins. So a tenant admin lands here with a 403 on the
// list and the page falls back to the session's own tenant ids -- they see
// and manage exactly their own tenant, which is the spec's rule.
import { useCallback, useEffect, useState } from 'react';
import Card, { CardContent } from '../components/ui/Card';
import Button from '../components/ui/Button';
import Input from '../components/ui/Input';
import { ApiError } from '../api/client';
import { listClusters } from '../api/clusters';
import type { Cluster } from '../api/clusters';
import { listTenantReservations } from '../api/reservations';
import type { Reservation } from '../api/reservations';
import {
  TENANT_ROLES,
  assignTenantRole,
  createTenant,
  getTenant,
  getTenantQuota,
  listTenantRoles,
  listTenants,
  revokeTenantRole,
  setTenantQuota,
} from '../api/tenants';
import type { Tenant, TenantRole, TenantRoleGrant } from '../api/tenants';
import type { SessionInfo } from '../api/session';
import { useSession } from '../hooks/useSession';

/** NaN-safe timestamp formatting, matching the other pages' formatTime behavior. */
export function formatTenantTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

/** The tenant ids a session holds tenant_admin in -- a tenant admin's own
 *  tenants, from the same /api/me map the nav shapes from. Pure so the
 *  fallback list is testable without mounting React. */
export function ownTenantIds(session: SessionInfo | null): number[] {
  if (!session) {
    return [];
  }
  return Object.entries(session.tenants)
    .filter(([, roles]) => roles.includes('tenant_admin'))
    .map(([id]) => Number(id))
    .filter((id) => Number.isInteger(id) && id > 0);
}

/** The next `count` reservations that have not ended yet, soonest first --
 *  the "what's coming" tail of the reservations summary. Pure for tests. */
export function nextUpcoming(reservations: Reservation[], count: number, now: Date): Reservation[] {
  return [...reservations]
    .filter((r) => new Date(r.end).getTime() >= now.getTime())
    .sort((a, b) => new Date(a.start).getTime() - new Date(b.start).getTime())
    .slice(0, count);
}

const inputCls =
  'block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 min-h-[44px] focus:outline-none focus:ring-2 focus:ring-sky-500 dark:border-slate-700 dark:bg-slate-900 dark:text-white';

const errMsg = (err: unknown, fallback: string) => (err instanceof ApiError ? err.message : fallback);

export default function Tenants() {
  const { session } = useSession();

  // Tenant list: everyone's for the service admin, own-only for tenant
  // admins (the 403 fallback). canCreate tracks which landed.
  const [tenants, setTenants] = useState<Tenant[] | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  const [canCreate, setCanCreate] = useState(false);
  const [selectedId, setSelectedId] = useState<number | null>(null);

  // Create form.
  const [newName, setNewName] = useState('');
  const [newDisplay, setNewDisplay] = useState('');
  const [createBusy, setCreateBusy] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);

  // Detail panel: quota card.
  const [clusterNames, setClusterNames] = useState<string[] | null>(null);
  const [quotaCluster, setQuotaCluster] = useState('');
  const [ceiling, setCeiling] = useState('0');
  const [quotaBusy, setQuotaBusy] = useState(false);
  const [quotaMsg, setQuotaMsg] = useState<string | null>(null);
  const [quotaError, setQuotaError] = useState<string | null>(null);

  // Detail panel: members card.
  const [members, setMembers] = useState<TenantRoleGrant[] | null>(null);
  const [memberSubject, setMemberSubject] = useState('');
  const [memberEmail, setMemberEmail] = useState('');
  const [memberRole, setMemberRole] = useState<TenantRole>('tenant_editor');
  const [memberBusy, setMemberBusy] = useState(false);
  const [memberError, setMemberError] = useState<string | null>(null);

  // Detail panel: reservations summary.
  const [reservations, setReservations] = useState<Reservation[] | null>(null);

  const loadTenants = useCallback(async () => {
    setListError(null);
    try {
      setTenants(await listTenants());
      setCanCreate(true);
      return;
    } catch (err) {
      if (!(err instanceof ApiError) || err.status !== 403) {
        setTenants([]);
        setListError(errMsg(err, 'Failed to load tenants.'));
        return;
      }
    }
    // 403 on the unscoped list: a tenant admin. The scoped per-tenant GET
    // admits their own tenants, so that is the roster they get.
    try {
      const own = await Promise.all(ownTenantIds(session).map((id) => getTenant(id)));
      setTenants(own);
      setCanCreate(false);
    } catch (err) {
      setTenants([]);
      setListError(errMsg(err, 'Failed to load tenants.'));
    }
  }, [session]);

  useEffect(() => {
    void loadTenants();
  }, [loadTenants]);

  // The quota card's cluster options: the registry's names plus "" (the
  // implicit default cluster). A tenant admin cannot list clusters
  // (system:admin gate), which degrades the select to just the default --
  // the honest set for someone whose write surface is one default cluster.
  useEffect(() => {
    listClusters()
      .then((cs: Cluster[]) => setClusterNames(cs.map((c) => c.name)))
      .catch(() => setClusterNames([]));
  }, []);

  const loadQuota = useCallback((tenantId: number, cluster: string) => {
    getTenantQuota(tenantId, cluster)
      .then((q) => {
        setCeiling(String(q.ceiling));
        setQuotaError(null);
      })
      .catch((err: unknown) => setQuotaError(errMsg(err, 'Failed to load quota.')));
  }, []);

  const loadMembers = useCallback((tenantId: number) => {
    listTenantRoles(tenantId)
      .then((m) => {
        setMembers(m);
        setMemberError(null);
      })
      .catch((err: unknown) => setMemberError(errMsg(err, 'Failed to load members.')));
  }, []);

  const loadReservations = useCallback((tenantId: number) => {
    listTenantReservations(tenantId)
      .then((r) => setReservations(r))
      .catch(() => setReservations([]));
  }, []);

  // Selecting a tenant loads its detail panels and resets the per-tenant
  // transient state (quota message, add-member form).
  const select = (id: number) => {
    setSelectedId(id);
    setQuotaMsg(null);
    setQuotaError(null);
    setMembers(null);
    setMemberError(null);
    setReservations(null);
    loadQuota(id, quotaCluster);
    loadMembers(id);
    loadReservations(id);
  };

  const changeQuotaCluster = (cluster: string) => {
    setQuotaCluster(cluster);
    if (selectedId !== null) {
      setQuotaMsg(null);
      loadQuota(selectedId, cluster);
    }
  };

  const saveQuota = async () => {
    if (selectedId === null) {
      return;
    }
    const parsed = Number(ceiling);
    if (!Number.isInteger(parsed) || parsed < 0) {
      setQuotaMsg(null);
      setQuotaError('Ceiling must be a non-negative integer.');
      return;
    }
    setQuotaBusy(true);
    setQuotaError(null);
    try {
      await setTenantQuota(selectedId, quotaCluster, parsed);
      setQuotaMsg(`Quota saved: ${quotaCluster || 'default'} ceiling ${parsed}.`);
    } catch (err) {
      setQuotaMsg(null);
      setQuotaError(errMsg(err, 'Failed to save quota.'));
    } finally {
      setQuotaBusy(false);
    }
  };

  const addMember = async () => {
    if (selectedId === null || memberSubject.trim() === '') {
      return;
    }
    setMemberBusy(true);
    setMemberError(null);
    try {
      await assignTenantRole(selectedId, {
        subject: memberSubject.trim(),
        email: memberEmail.trim(),
        role: memberRole,
      });
      setMemberSubject('');
      setMemberEmail('');
      loadMembers(selectedId);
    } catch (err) {
      setMemberError(errMsg(err, 'Failed to assign role.'));
    } finally {
      setMemberBusy(false);
    }
  };

  const revokeMember = async (grant: TenantRoleGrant) => {
    if (selectedId === null) {
      return;
    }
    setMemberBusy(true);
    setMemberError(null);
    try {
      await revokeTenantRole(selectedId, grant.subject, grant.role);
      loadMembers(selectedId);
    } catch (err) {
      setMemberError(errMsg(err, 'Failed to revoke role.'));
    } finally {
      setMemberBusy(false);
    }
  };

  const submitCreate = async () => {
    if (newName.trim() === '') {
      return;
    }
    setCreateBusy(true);
    setCreateError(null);
    try {
      await createTenant(newName.trim(), newDisplay.trim());
      setNewName('');
      setNewDisplay('');
      await loadTenants();
    } catch (err) {
      setCreateError(errMsg(err, 'Failed to create tenant.'));
    } finally {
      setCreateBusy(false);
    }
  };

  const selected = tenants?.find((t) => t.id === selectedId) ?? null;
  const upcoming = reservations ? nextUpcoming(reservations, 3, new Date()) : [];

  return (
    <div className="space-y-6" data-testid="tenants-page">
      <div>
        <h1 className="text-display-sm text-slate-900 dark:text-white">Tenants</h1>
        <p className="text-body-sm mt-1 text-slate-500 dark:text-slate-400">
          Tenant administration: quotas, member roles, and reservations per tenant.
        </p>
      </div>

      {canCreate && (
        <Card>
          <CardContent className="space-y-3">
            <h2 className="text-body-sm font-semibold text-slate-900 dark:text-white">Create tenant</h2>
            <form
              className="flex flex-wrap items-start gap-3"
              onSubmit={(e) => {
                e.preventDefault();
                void submitCreate();
              }}
            >
              <div className="w-56">
                <Input
                  label="Name"
                  value={newName}
                  onChange={(e) => setNewName(e.target.value)}
                  placeholder="acme-corp"
                  aria-label="tenant name"
                />
              </div>
              <div className="w-56">
                <Input
                  label="Display name"
                  value={newDisplay}
                  onChange={(e) => setNewDisplay(e.target.value)}
                  placeholder="Acme Corporation"
                  aria-label="tenant display name"
                />
              </div>
              <Button type="submit" disabled={createBusy || newName.trim() === ''} data-testid="create-tenant-btn" className="mt-6">
                Create tenant
              </Button>
            </form>
            {createError && (
              <p className="text-sm text-red-600 dark:text-red-400" role="alert">
                {createError}
              </p>
            )}
          </CardContent>
        </Card>
      )}

      <Card>
        <CardContent className="space-y-4">
          <h2 className="text-body-sm font-semibold text-slate-900 dark:text-white">
            {canCreate ? 'All tenants' : 'Your tenants'}
          </h2>

          {listError && (
            <p className="text-sm text-red-600 dark:text-red-400" role="alert">
              {listError}
            </p>
          )}

          {tenants && tenants.length === 0 && !listError && (
            <p className="text-body-sm text-slate-500 dark:text-slate-400">
              No tenants{canCreate ? ' yet — create one above.' : ' you administer.'}
            </p>
          )}

          {tenants && tenants.length > 0 && (
            <div className="overflow-x-auto">
              <table className="w-full text-left text-body-sm">
                <thead>
                  <tr className="text-caption border-b border-slate-200 text-slate-500 dark:border-slate-700 dark:text-slate-400">
                    <th scope="col" className="px-3 py-2 font-medium">ID</th>
                    <th scope="col" className="px-3 py-2 font-medium">Name</th>
                    <th scope="col" className="px-3 py-2 font-medium">Display name</th>
                    <th scope="col" className="px-3 py-2 font-medium">Status</th>
                    <th scope="col" className="px-3 py-2 font-medium">Created</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {tenants.map((t) => (
                    <tr
                      key={t.id}
                      data-testid={`tenant-row-${t.id}`}
                      onClick={() => select(t.id)}
                      className={`cursor-pointer transition-colors duration-150 ${
                        t.id === selectedId
                          ? 'bg-sky-50 dark:bg-sky-900/30'
                          : 'hover:bg-slate-50 dark:hover:bg-slate-800/50'
                      }`}
                    >
                      <td className="px-3 py-2 whitespace-nowrap text-slate-500 dark:text-slate-400">{t.id}</td>
                      <td className="px-3 py-2 font-medium whitespace-nowrap text-slate-900 dark:text-white">
                        <button type="button" className="focus:outline-none focus:ring-2 focus:ring-sky-500 rounded" onClick={() => select(t.id)}>
                          {t.name}
                        </button>
                      </td>
                      <td className="px-3 py-2 whitespace-nowrap">{t.display_name || '—'}</td>
                      <td className="px-3 py-2 whitespace-nowrap">
                        <span
                          className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${
                            t.status === 'ACTIVE'
                              ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300'
                              : 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300'
                          }`}
                        >
                          {t.status}
                        </span>
                      </td>
                      <td className="px-3 py-2 whitespace-nowrap">{formatTenantTime(t.created_time)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>

      {selected && (
        <div className="grid gap-6 lg:grid-cols-2">
          <Card>
            <CardContent className="space-y-4">
              <h2 className="text-body-sm font-semibold text-slate-900 dark:text-white">
                Quota — {selected.display_name || selected.name}
              </h2>
              <p className="text-caption text-slate-500 dark:text-slate-400">
                Engine ceiling per cluster. An unset ceiling reads 0 — nothing runs until one is configured.
              </p>
              <div className="flex flex-wrap items-end gap-3">
                <div className="w-48">
                  <label htmlFor="quota-cluster" className="mb-2 block text-sm font-medium text-slate-700 dark:text-slate-300">
                    Cluster
                  </label>
                  <select
                    id="quota-cluster"
                    className={inputCls}
                    value={quotaCluster}
                    onChange={(e) => changeQuotaCluster(e.target.value)}
                  >
                    <option value="">default</option>
                    {(clusterNames ?? []).map((name) => (
                      <option key={name} value={name}>
                        {name}
                      </option>
                    ))}
                  </select>
                </div>
                <div className="w-36">
                  <Input
                    label="Ceiling"
                    type="number"
                    min={0}
                    value={ceiling}
                    onChange={(e) => setCeiling(e.target.value)}
                    aria-label="quota ceiling"
                  />
                </div>
                <Button onClick={() => void saveQuota()} disabled={quotaBusy} data-testid="quota-save-btn">
                  Save quota
                </Button>
              </div>
              {quotaError && (
                <p className="text-sm text-red-600 dark:text-red-400" role="alert">
                  {quotaError}
                </p>
              )}
              {quotaMsg && (
                <p className="text-sm text-emerald-600 dark:text-emerald-400" role="status">
                  {quotaMsg}
                </p>
              )}
            </CardContent>
          </Card>

          <Card>
            <CardContent className="space-y-4">
              <h2 className="text-body-sm font-semibold text-slate-900 dark:text-white">
                Members — {selected.display_name || selected.name}
              </h2>

              {memberError && (
                <p className="text-sm text-red-600 dark:text-red-400" role="alert">
                  {memberError}
                </p>
              )}

              {/* The table is the members-table hook the tests (and future
                  tooling) key on, so it renders whenever the roster has
                  loaded -- empty rosters included -- rather than vanishing. */}
              {members !== null && (
                <div className="overflow-x-auto">
                  <table className="w-full text-left text-body-sm" data-testid="members-table">
                    <thead>
                      <tr className="text-caption border-b border-slate-200 text-slate-500 dark:border-slate-700 dark:text-slate-400">
                        <th scope="col" className="px-3 py-2 font-medium">Subject</th>
                        <th scope="col" className="px-3 py-2 font-medium">Email</th>
                        <th scope="col" className="px-3 py-2 font-medium">Role</th>
                        <th scope="col" className="px-3 py-2 font-medium">Granted by</th>
                        <th scope="col" className="px-3 py-2 font-medium">Granted</th>
                        <th scope="col" className="px-3 py-2 font-medium" />
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                      {members.length === 0 && (
                        <tr>
                          <td colSpan={6} className="px-3 py-2 text-slate-500 dark:text-slate-400">
                            No role grants in this tenant yet.
                          </td>
                        </tr>
                      )}
                      {members.map((g) => (
                        <tr key={`${g.subject}-${g.role}`}>
                          <td className="px-3 py-2 font-medium whitespace-nowrap text-slate-900 dark:text-white">{g.subject}</td>
                          <td className="px-3 py-2 whitespace-nowrap">{g.email || '—'}</td>
                          <td className="px-3 py-2 whitespace-nowrap">
                            <code className="text-caption">{g.role}</code>
                          </td>
                          <td className="px-3 py-2 whitespace-nowrap">{g.granted_by || '—'}</td>
                          <td className="px-3 py-2 whitespace-nowrap">{formatTenantTime(g.granted_time)}</td>
                          <td className="px-3 py-2 text-right">
                            <Button
                              variant="outline"
                              size="sm"
                              onClick={() => void revokeMember(g)}
                              disabled={memberBusy}
                              data-testid={`revoke-btn-${g.subject}`}
                            >
                              Revoke
                            </Button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}

              <form
                className="flex flex-wrap items-start gap-3 border-t border-slate-200 pt-4 dark:border-slate-700"
                onSubmit={(e) => {
                  e.preventDefault();
                  void addMember();
                }}
              >
                <div className="w-44">
                  <Input
                    label="Subject"
                    value={memberSubject}
                    onChange={(e) => setMemberSubject(e.target.value)}
                    placeholder="demo:bob"
                    aria-label="member subject"
                  />
                </div>
                <div className="w-48">
                  <Input
                    label="Email"
                    value={memberEmail}
                    onChange={(e) => setMemberEmail(e.target.value)}
                    placeholder="bob@example.io"
                    aria-label="member email"
                  />
                </div>
                <div className="w-44">
                  <label htmlFor="member-role" className="mb-2 block text-sm font-medium text-slate-700 dark:text-slate-300">
                    Role
                  </label>
                  <select
                    id="member-role"
                    className={inputCls}
                    value={memberRole}
                    onChange={(e) => setMemberRole(e.target.value as TenantRole)}
                  >
                    {TENANT_ROLES.map((role) => (
                      <option key={role} value={role}>
                        {role}
                      </option>
                    ))}
                  </select>
                </div>
                <Button
                  type="submit"
                  disabled={memberBusy || memberSubject.trim() === ''}
                  data-testid="add-member-btn"
                  className="mt-6"
                >
                  Add member
                </Button>
              </form>
            </CardContent>
          </Card>

          <Card>
            <CardContent className="space-y-4">
              <h2 className="text-body-sm font-semibold text-slate-900 dark:text-white">
                Reservations — {selected.display_name || selected.name}
              </h2>
              <p className="text-body-sm text-slate-500 dark:text-slate-400">
                {reservations === null ? 'Loading…' : `${reservations.length} reservation${reservations.length === 1 ? '' : 's'} on the calendar.`}
              </p>
              {upcoming.length > 0 && (
                <div className="space-y-2">
                  <p className="text-caption text-slate-500 dark:text-slate-400">Next upcoming</p>
                  <ul className="space-y-1">
                    {upcoming.map((r) => (
                      <li key={r.id} className="text-body-sm text-slate-700 dark:text-slate-300">
                        <span className="font-medium">{r.engine_count}</span> engine{r.engine_count === 1 ? '' : 's'} on{' '}
                        {r.cluster || 'default'} — {formatTenantTime(r.start)}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
              {reservations !== null && upcoming.length === 0 && (
                <p className="text-body-sm text-slate-500 dark:text-slate-400">Nothing upcoming.</p>
              )}
            </CardContent>
          </Card>
        </div>
      )}
    </div>
  );
}
