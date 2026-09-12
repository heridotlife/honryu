import { useState } from 'react';
import Button from '../components/ui/Button';
import Card from '../components/ui/Card';
import Input from '../components/ui/Input';
import { ApiError } from '../api/client';
import { listTenantReservations } from '../api/reservations';
import type { Reservation } from '../api/reservations';

type Status = 'upcoming' | 'active' | 'past';

const statusClasses: Record<Status, string> = {
  upcoming: 'bg-sky-100 text-sky-800 dark:bg-sky-900/30 dark:text-sky-300',
  active: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300',
  past: 'bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-300',
};

function reservationStatus(r: Reservation, now: Date): Status {
  const start = new Date(r.start);
  const end = new Date(r.end);
  if (now < start) return 'upcoming';
  if (now >= end) return 'past';
  return 'active';
}

function StatusBadge({ status }: { status: Status }) {
  return (
    <span className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${statusClasses[status]}`}>
      {status}
    </span>
  );
}

function formatTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function dayKey(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString([], { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric' });
}

function groupByDay(reservations: Reservation[]): [string, Reservation[]][] {
  const groups = new Map<string, Reservation[]>();
  for (const r of reservations) {
    const key = dayKey(r.start);
    const existing = groups.get(key);
    if (existing) {
      existing.push(r);
    } else {
      groups.set(key, [r]);
    }
  }
  return Array.from(groups.entries());
}

/** A timeline of a tenant's engine reservations, grouped by day. */
export default function Reservations() {
  const [tenantId, setTenantId] = useState('');
  const [cluster, setCluster] = useState('');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [reservations, setReservations] = useState<Reservation[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const load = async () => {
    const tenantIdNum = Number(tenantId);
    if (!tenantId || !Number.isInteger(tenantIdNum) || tenantIdNum <= 0) {
      setError('Enter a valid tenant id.');
      return;
    }
    const fromDate = from ? new Date(from) : undefined;
    const toDate = to ? new Date(to) : undefined;
    if ((from && Number.isNaN(fromDate?.getTime())) || (to && Number.isNaN(toDate?.getTime()))) {
      setError('Enter valid dates.');
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const got = await listTenantReservations(tenantIdNum, { from: fromDate, to: toDate, cluster: cluster || undefined });
      setReservations(got);
    } catch (err) {
      setReservations(null);
      setError(err instanceof ApiError ? err.message : 'Failed to load reservations.');
    } finally {
      setLoading(false);
    }
  };

  const now = new Date();
  const groups = reservations ? groupByDay(reservations) : [];

  return (
    <div className="space-y-6" role="region" aria-label="Reservations panel">
      <div>
        <h1 className="text-display-sm text-slate-900 dark:text-white">Reservations</h1>
        <p className="text-body-sm mt-1 text-slate-500 dark:text-slate-400">
          A tenant's engine reservations over time, by cluster.
        </p>
      </div>

      <Card>
        <form
          className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4 lg:items-end"
          onSubmit={(e) => {
            e.preventDefault();
            void load();
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
          <Input
            label="Cluster"
            value={cluster}
            onChange={(e) => setCluster(e.target.value)}
            placeholder="all clusters"
            fullWidth
          />
          <Input
            label="From"
            type="datetime-local"
            value={from}
            onChange={(e) => setFrom(e.target.value)}
            helperText={!from ? 'defaults to now' : undefined}
            fullWidth
          />
          <Input
            label="To"
            type="datetime-local"
            value={to}
            onChange={(e) => setTo(e.target.value)}
            helperText={!to ? 'defaults to +7 days' : undefined}
            fullWidth
          />
          <div className="lg:col-span-4">
            <Button type="submit" disabled={loading}>
              {loading ? 'Loading…' : 'Load reservations'}
            </Button>
          </div>
        </form>
        {error && (
          <p className="mt-4 text-sm text-red-600 dark:text-red-400" role="alert">
            {error}
          </p>
        )}
      </Card>

      {reservations && (
        <Card padding="none">
          {reservations.length === 0 ? (
            <p className="text-body-sm p-6 text-slate-500 dark:text-slate-400">
              No reservations for this tenant in the selected window.
            </p>
          ) : (
            <ul className="divide-y divide-slate-200 dark:divide-slate-700">
              {groups.map(([day, items]) => (
                <li key={day} className="p-4">
                  <h3 className="text-caption mb-3 font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400">
                    {day}
                  </h3>
                  <ul className="space-y-2">
                    {items
                      .slice()
                      .sort((a, b) => new Date(a.start).getTime() - new Date(b.start).getTime())
                      .map((r) => (
                        <li
                          key={r.id}
                          className="flex min-h-[44px] flex-col gap-2 rounded-lg border border-slate-200 p-3 dark:border-slate-700 sm:flex-row sm:items-center sm:justify-between"
                        >
                          <div className="flex items-center gap-3">
                            <StatusBadge status={reservationStatus(r, now)} />
                            <span className="text-body-sm font-medium text-slate-900 dark:text-white">
                              {formatTime(r.start)}–{formatTime(r.end)}
                            </span>
                            <span className="text-caption text-slate-500 dark:text-slate-400">
                              execution #{r.execution_id}
                            </span>
                          </div>
                          <div className="text-caption text-slate-500 dark:text-slate-400">
                            {r.cluster ? `${r.cluster} · ` : ''}
                            {r.engine_count} {r.engine_count === 1 ? 'engine' : 'engines'}
                          </div>
                        </li>
                      ))}
                  </ul>
                </li>
              ))}
            </ul>
          )}
        </Card>
      )}
    </div>
  );
}

export { reservationStatus, groupByDay };
