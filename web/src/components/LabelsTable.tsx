// Per-label results table for a run report: which request label carried
// the load, which degraded, and what each one's percentiles looked like.
// Sortable on every measured column (click toggles asc/desc), with an em-dash
// where a label recorded no sample for that percentile -- and in the status
// column for reports from before statuses were counted. The whole card hides
// when the report carries no labels -- an empty card is noise, not
// information (same hide rule as the requested-vs-achieved overlay).
import { useState } from 'react';
import Card, { CardContent, CardHeader, CardTitle } from './ui/Card';
import type { LabelSummary, StatusBadge } from '../api/reports';

/** The sortable axes; the latency columns read latency['50']/['95']/['99']. */
export type LabelSortKey = 'label' | 'samples' | 'errorRate' | 'p50' | 'p95' | 'p99';

/** 1 = ascending, -1 = descending. */
export type SortDir = 1 | -1;

const COLUMNS: Array<{ key: LabelSortKey; title: string; numeric: boolean }> = [
  { key: 'label', title: 'Label', numeric: false },
  { key: 'samples', title: 'Samples', numeric: true },
  { key: 'errorRate', title: 'Error rate', numeric: true },
  { key: 'p50', title: 'p50', numeric: true },
  { key: 'p95', title: 'p95', numeric: true },
  { key: 'p99', title: 'p99', numeric: true },
];

/** The latency record's key each percentile column reads (p50 -> '50'). */
function pctKey(key: LabelSortKey): string | null {
  if (key === 'p50' || key === 'p95' || key === 'p99') {
    return key.slice(1);
  }
  return null;
}

/** Raw comparison value for a column; undefined = no sample for that cell. */
function sortValue(row: LabelSummary, key: LabelSortKey): number | string | undefined {
  const pct = pctKey(key);
  if (pct !== null) {
    return row.latency?.[pct];
  }
  switch (key) {
    case 'label':
      return row.label;
    case 'samples':
      return row.samples;
    case 'errorRate':
      return row.error_rate;
  }
}

/**
 * A sorted copy (the input is never mutated). Alpha columns compare with
 * localeCompare; numeric columns numerically. A label with no sample for
 * the sorted percentile sinks to the bottom in BOTH directions -- an
 * em-dash is "not measured", not "infinitely fast" or "infinitely slow".
 */
export function sortLabels(labels: LabelSummary[], key: LabelSortKey, dir: SortDir): LabelSummary[] {
  return [...labels].sort((a, b) => {
    const va = sortValue(a, key);
    const vb = sortValue(b, key);
    if (typeof va === 'string' || typeof vb === 'string') {
      return String(va).localeCompare(String(vb)) * dir;
    }
    if (va === undefined && vb === undefined) {
      return 0;
    }
    if (va === undefined) {
      return 1;
    }
    if (vb === undefined) {
      return -1;
    }
    return (va - vb) * dir;
  });
}

/** Fraction (the wire's 0..1 convention) -> percent with two decimals, e.g. 1.24%. */
export function formatErrorRate(fraction: number): string {
  return `${(fraction * 100).toFixed(2)}%`;
}

/** The wire carries seconds; the table reads milliseconds. */
export function formatMs(seconds: number): string {
  return `${(seconds * 1000).toFixed(1)} ms`;
}

/**
 * Badge colour by status class: 2xx green, 4xx amber, 5xx red, anything
 * else gray -- a class a reader can scan without reading the numbers.
 */
export function statusBadgeClasses(code: string): string {
  const base = 'inline-flex items-center rounded-full px-2 py-0.5 text-caption font-medium';
  switch (code[0]) {
    case '2':
      return `${base} bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300`;
    case '4':
      return `${base} bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300`;
    case '5':
      return `${base} bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300`;
    default:
      return `${base} bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-300`;
  }
}

/** The status column's badges for one label, in the wire's own order --
 * dominant first, already sorted server-side. */
function StatusBadges({ row, rowIdx }: { row: LabelSummary; rowIdx: number }) {
  if (!row.statuses || row.statuses.length === 0) {
    return <>—</>;
  }
  return (
    <span className="inline-flex flex-wrap gap-1">
      {row.statuses.map((s: StatusBadge) => (
        <span
          key={s.code}
          data-testid={`status-badge-${rowIdx}-${s.code}`}
          className={statusBadgeClasses(s.code)}
        >
          {s.code}×{s.count}
        </span>
      ))}
    </span>
  );
}

export default function LabelsTable({ labels }: { labels?: LabelSummary[] }) {
  const [sort, setSort] = useState<{ key: LabelSortKey; dir: SortDir }>({ key: 'label', dir: 1 });

  // The hide rule: no labels, no card.
  if (!labels || labels.length === 0) {
    return null;
  }

  const rows = sortLabels(labels, sort.key, sort.dir);
  const toggle = (key: LabelSortKey) =>
    setSort((s) => (s.key === key ? { key, dir: s.dir === 1 ? -1 : 1 } : { key, dir: 1 }));

  return (
    <Card data-testid="labels-card">
      <CardHeader>
        <CardTitle>Per-label results</CardTitle>
      </CardHeader>
      <CardContent>
        <div className="overflow-x-auto">
          <table className="w-full text-left text-body-sm" data-testid="labels-table">
            <thead>
              <tr className="text-caption border-b border-slate-200 text-slate-500 dark:border-slate-700 dark:text-slate-400">
                {COLUMNS.map((col) => (
                  <th
                    key={col.key}
                    scope="col"
                    className="px-3 py-2 font-medium"
                    aria-sort={sort.key === col.key ? (sort.dir === 1 ? 'ascending' : 'descending') : 'none'}
                  >
                    <button
                      type="button"
                      data-testid={`sort-${col.key}`}
                      onClick={() => toggle(col.key)}
                      className="inline-flex items-center gap-1 rounded transition-colors hover:text-sky-600 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 dark:hover:text-sky-400"
                    >
                      {col.title}
                      {sort.key === col.key && <span aria-hidden="true">{sort.dir === 1 ? '↑' : '↓'}</span>}
                    </button>
                  </th>
                ))}
                {/* Not sortable: the statuses are a breakdown, not a measured
                    axis -- their order is the wire's (dominant first). */}
                <th scope="col" className="px-3 py-2 font-medium">
                  Statuses
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {rows.map((row, rowIdx) => (
                <tr key={row.label}>
                  <td className="px-3 py-2 font-medium whitespace-nowrap text-slate-900 dark:text-white">{row.label}</td>
                  <td className="px-3 py-2 whitespace-nowrap">{row.samples}</td>
                  <td className="px-3 py-2 whitespace-nowrap">{formatErrorRate(row.error_rate)}</td>
                  {(['50', '95', '99'] as const).map((pct) => {
                    const seconds = row.latency?.[pct];
                    return (
                      <td key={pct} className="px-3 py-2 whitespace-nowrap" data-label={`p${pct}`}>
                        {seconds !== undefined ? formatMs(seconds) : '—'}
                      </td>
                    );
                  })}
                  <td className="px-3 py-2 whitespace-nowrap" data-label="statuses">
                    <StatusBadges row={row} rowIdx={rowIdx} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </CardContent>
    </Card>
  );
}
