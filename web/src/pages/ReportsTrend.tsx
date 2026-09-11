// Trend + error-signature analytics for an execution (phase 9 endpoints,
// surfaced in phase 13): the run-over-run trend as a table with a plain-SVG
// sparkline over achieved QPS, and the error-signature history with a
// by-label/by-code toggle. Sections mount on the Reports list once an
// execution is loaded (same execution-id form) and own their fetching and
// error state.
import { useEffect, useState } from 'react';
import Card, { CardContent, CardHeader, CardTitle } from '../components/ui/Card';
import OutcomeBadge from '../components/ui/OutcomeBadge';
import Sparkline from '../components/Sparkline';
import { ApiError } from '../api/client';
import type { Outcome } from '../api/reports';
import { getErrorSignatures, getExecutionTrend } from '../api/trends';
import type { ErrorSignatureHistory, SignatureBreakdown, SignatureGroupBy, Trend } from '../api/trends';

/**
 * Groups and rows biggest-first (total_count desc), ties broken by key so
 * rendering is deterministic. Returns copies; the input is not mutated.
 */
export function sortSignatureGroups(groups: SignatureBreakdown[]): SignatureBreakdown[] {
  return groups
    .map((g) => ({ ...g, rows: [...g.rows].sort((a, b) => b.total_count - a.total_count) }))
    .sort((a, b) => b.total_count - a.total_count || a.key.localeCompare(b.key));
}

export function TrendSection({ executionId }: { executionId: number }) {
  const [trend, setTrend] = useState<Trend | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setTrend(null);
    setError(null);
    getExecutionTrend(executionId)
      .then(setTrend)
      .catch((err: unknown) => setError(err instanceof ApiError ? err.message : 'Failed to load trend.'));
  }, [executionId]);

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-4">
        <CardTitle>Run-over-run trend</CardTitle>
        {trend && trend.points.length > 0 && (
          <div className="flex items-center gap-2 text-sky-600 dark:text-sky-400">
            <span className="text-caption text-slate-500 dark:text-slate-400">achieved QPS, oldest → newest</span>
            {/* Trend points arrive most-recent-first; the sparkline reads left = oldest. */}
            <Sparkline
              values={[...trend.points].reverse().map((p) => p.achieved_throughput)}
              ariaLabel="Achieved QPS across runs, oldest to newest"
            />
          </div>
        )}
      </CardHeader>
      <CardContent>
        {error && (
          <p className="text-sm text-red-600 dark:text-red-400" role="alert">
            {error}
          </p>
        )}
        {!error && trend && (
          <>
            {trend.points.length === 0 ? (
              <p className="text-body-sm">No runs yet — the trend fills in as runs complete.</p>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-left text-body-sm">
                  <thead>
                    <tr className="text-caption border-b border-slate-200 text-slate-500 dark:border-slate-700 dark:text-slate-400">
                      <th scope="col" className="px-3 py-2 font-medium">Run</th>
                      <th scope="col" className="px-3 py-2 font-medium">Outcome</th>
                      <th scope="col" className="px-3 py-2 font-medium">Achieved (req/s)</th>
                      <th scope="col" className="px-3 py-2 font-medium">Requested (req/s)</th>
                      <th scope="col" className="px-3 py-2 font-medium">p95</th>
                      <th scope="col" className="px-3 py-2 font-medium">Errors</th>
                      <th scope="col" className="px-3 py-2 font-medium">Signals</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                    {trend.points.map((p) => (
                      <tr key={p.run_id}>
                        <td className="px-3 py-2 font-medium whitespace-nowrap text-slate-900 dark:text-white">#{p.run_id}</td>
                        <td className="px-3 py-2">
                          {/* outcome comes off the same backend enum as Report.Outcome */}
                          <OutcomeBadge outcome={p.outcome as Outcome} />
                        </td>
                        <td className="px-3 py-2 whitespace-nowrap">{p.achieved_throughput.toFixed(1)}</td>
                        <td className="px-3 py-2 whitespace-nowrap">
                          {/* Phase 49 audit: this column read as a broken
                              all-zeros table because an unset stage
                              throughput means "unlimited" (the load profile
                              omits the key), which persists as 0 and is the
                              common case -- not a mapping bug and not a
                              missing field. The zero honestly means "no
                              target", so say that instead of 0.0. */}
                          {p.requested_throughput > 0 ? p.requested_throughput.toFixed(1) : 'unlimited'}
                        </td>
                        <td className="px-3 py-2 whitespace-nowrap">{p.p95.toFixed(3)}s</td>
                        <td className="px-3 py-2 whitespace-nowrap">{(p.error_rate * 100).toFixed(1)}%</td>
                        <td className="px-3 py-2">
                          <span className="flex flex-wrap items-center gap-1.5">
                            {p.regressed && (
                              <span className="inline-flex items-center rounded-full bg-red-100 px-2.5 py-0.5 text-xs font-medium text-red-800 dark:bg-red-900/30 dark:text-red-300">
                                regressed
                              </span>
                            )}
                            {!p.has_comparable_predecessor && (
                              <span className="inline-flex items-center rounded-full bg-slate-200 px-2.5 py-0.5 text-xs font-medium text-slate-700 dark:bg-slate-700 dark:text-slate-300">
                                no baseline
                              </span>
                            )}
                            {p.has_comparable_predecessor && !p.regressed && (
                              <span className="text-caption text-slate-400 dark:text-slate-500">—</span>
                            )}
                          </span>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}

export function SignatureSection({ executionId }: { executionId: number }) {
  const [by, setBy] = useState<SignatureGroupBy>('label');
  const [history, setHistory] = useState<ErrorSignatureHistory | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setHistory(null);
    setError(null);
    getErrorSignatures(executionId, by)
      .then(setHistory)
      .catch((err: unknown) => setError(err instanceof ApiError ? err.message : 'Failed to load error signatures.'));
  }, [executionId, by]);

  const groups = history ? sortSignatureGroups(history.groups) : [];

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-4">
        <CardTitle>Error signature history</CardTitle>
        <div
          role="group"
          aria-label="Group signatures by"
          className="flex gap-1 rounded-lg border border-slate-200 p-1 dark:border-slate-700"
        >
          {(['label', 'code'] as const).map((axis) => (
            <button
              key={axis}
              type="button"
              aria-pressed={by === axis}
              onClick={() => setBy(axis)}
              className={`min-h-[32px] rounded-md px-3 py-1 text-caption font-medium transition-colors focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 ${
                by === axis
                  ? 'bg-sky-100 text-sky-800 dark:bg-sky-900/40 dark:text-sky-300'
                  : 'text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'
              }`}
            >
              {axis === 'label' ? 'by label' : 'by code'}
            </button>
          ))}
        </div>
      </CardHeader>
      <CardContent>
        {error && (
          <p className="text-sm text-red-600 dark:text-red-400" role="alert">
            {error}
          </p>
        )}
        {!error && history && (
          <>
            {groups.length === 0 ? (
              <p className="text-body-sm">No failures recorded across this execution&apos;s runs.</p>
            ) : (
              <div className="space-y-4">
                {groups.map((g) => (
                  <div key={g.key}>
                    <div className="flex items-baseline justify-between gap-2 border-b border-slate-200 pb-1 dark:border-slate-700">
                      <span className="text-body-sm font-semibold text-slate-900 dark:text-white">{g.key}</span>
                      {/* Group total is a safe re-sum of its rows. Run coverage (run_count)
                          is per leaf row only -- a run hitting two response codes under
                          one label would otherwise be double counted, so the group shows
                          the total and no run count. */}
                      <span className="text-caption text-slate-500 dark:text-slate-400">
                        {g.total_count} {g.total_count === 1 ? 'failure' : 'failures'}
                      </span>
                    </div>
                    <ul className="mt-1">
                      {g.rows.map((row, i) => (
                        <li key={i} className="flex flex-wrap items-center justify-between gap-2 py-1 text-caption">
                          <span className="text-slate-700 dark:text-slate-300">
                            {row.label}
                            {row.response_code ? ` · ${row.response_code}` : ''} · {row.side}
                          </span>
                          <span className="text-slate-500 dark:text-slate-400">
                            {row.total_count} in {row.run_count} {row.run_count === 1 ? 'run' : 'runs'}
                          </span>
                        </li>
                      ))}
                    </ul>
                  </div>
                ))}
              </div>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}
