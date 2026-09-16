// The scenario-threshold editor (phase 72): rows of (metric, comparison,
// value) bound to the scenario, saved as a whole via PUT replace-all — the
// editor-save semantics the endpoint implements, so Save is idempotent and
// an emptied list clears. Loaded once per scenario; each row validates
// client-side against the metric's own legal range (the same rule the
// backend's domain Validate applies), with helper text stating what's legal
// rather than leaving zero-semantics to be guessed.
import { useEffect, useState } from 'react';
import Button from './ui/Button';
import { ApiError } from '../api/client';
import { listThresholds, saveThresholds, type StoredThreshold, type ThresholdInput } from '../api/scenarios';

/** The metric options, in the stable order the enum lists them. Labels name
 * the unit — the wire spelling stays in the value. */
const METRIC_OPTIONS = [
  { value: 'http_p95_ms', label: 'p95 response time (ms)' },
  { value: 'http_p99_ms', label: 'p99 response time (ms)' },
  { value: 'error_rate', label: 'Error rate (0–1)' },
  { value: 'throughput_qps', label: 'Throughput (req/s)' },
] as const;

/** The comparison options: lt is the ceiling form, gt the floor. */
const COMPARISON_OPTIONS = [
  { value: 'lt', label: 'less than' },
  { value: 'gt', label: 'greater than' },
] as const;

/** The input row: what the operator is editing, keyed client-side. */
interface Row extends ThresholdInput {
  key: number;
}

/** Is the metric one whose bound is a latency/throughput figure — anything
 * but the fractional error rate? */
function isPositiveMetric(metric: string): boolean {
  return metric === 'http_p95_ms' || metric === 'http_p99_ms' || metric === 'throughput_qps';
}

/** Zero-semantics helper text per row: what a legal value IS, not just that
 * this one isn't. */
function rangeHint(metric: string): string {
  return isPositiveMetric(metric) ? 'Must be a number greater than 0' : 'Between 0 and 1';
}

/** The row's validation verdict, or null while legal. Empty value counts as
 * unset (Save blocks); a wrong-range value shows the same hint as the
 * helper text, louder. */
function rowError(row: Row): string | null {
  if (Number.isNaN(row.value)) {
    return 'Enter a value';
  }
  if (isPositiveMetric(row.metric) && row.value <= 0) {
    return rangeHint(row.metric);
  }
  if (!isPositiveMetric(row.metric) && (row.value < 0 || row.value > 1)) {
    return rangeHint(row.metric);
  }
  return null;
}

/** "p95 response time (ms) less than 300" — the criterion as a reader meets
 * it, also what the test pins. */
export function describeThreshold(t: ThresholdInput): string {
  const metric = METRIC_OPTIONS.find((m) => m.value === t.metric);
  const cmp = COMPARISON_OPTIONS.find((c) => c.value === t.comparison);
  return `${metric?.label ?? t.metric} ${cmp?.label ?? t.comparison} ${t.value}`;
}

interface ThresholdEditorProps {
  scenarioId: number;
}

/**
 * Thresholds section of the scenario Editor tab. One editor, whole-list
 * saves: Add row / remove rework a local draft, Save PUTs the draft as the
 * scenario's entire set and adopts the stored rows (ids included) back.
 */
export default function ThresholdEditor({ scenarioId }: ThresholdEditorProps) {
  const [stored, setStored] = useState<StoredThreshold[] | null>(null);
  const [rows, setRows] = useState<Row[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setLoadError(null);
    listThresholds(scenarioId)
      .then((list) => {
        if (!alive) return;
        setStored(list);
        setRows(list.map((t, i) => ({ ...t, key: i })));
      })
      .catch((err: unknown) => {
        if (alive) setLoadError(err instanceof ApiError ? err.message : 'Failed to load thresholds.');
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [scenarioId]);

  const dirty =
    stored === null ||
    rows.length !== stored.length ||
    rows.some((r, i) => stored[i] === undefined || stored[i].metric !== r.metric || stored[i].comparison !== r.comparison || stored[i].value !== r.value);

  const invalid = rows.some((r) => rowError(r) !== null);

  const updateRow = (key: number, patch: Partial<Row>) => {
    setSaved(false);
    setRows((prev) => prev.map((r) => (r.key === key ? { ...r, ...patch } : r)));
  };

  const addRow = () => {
    setSaved(false);
    // NaN is the "no value typed yet" sentinel: the input renders empty and
    // the row reads as unset, not as a zero bound.
    setRows((prev) => [...prev, { key: Date.now() + prev.length, metric: 'http_p95_ms', comparison: 'lt', value: NaN }]);
  };

  const removeRow = (key: number) => {
    setSaved(false);
    setRows((prev) => prev.filter((r) => r.key !== key));
  };

  const save = async () => {
    setSaving(true);
    setSaveError(null);
    try {
      const list = await saveThresholds(
        scenarioId,
        rows.map(({ metric, comparison, value }) => ({ metric, comparison, value })),
      );
      setStored(list);
      setRows(list.map((t, i) => ({ ...t, key: Date.now() + i })));
      setSaved(true);
    } catch (err: unknown) {
      setSaveError(err instanceof ApiError ? err.message : 'Failed to save thresholds.');
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <div className="space-y-2" data-testid="thresholds-loading">
        <div className="h-9 animate-pulse rounded-lg bg-slate-100 dark:bg-slate-700/50" />
        <div className="h-9 w-2/3 animate-pulse rounded-lg bg-slate-100 dark:bg-slate-700/50" />
      </div>
    );
  }
  if (loadError !== null) {
    return (
      <p className="text-sm text-red-600 dark:text-red-400" role="alert" data-testid="thresholds-load-error">
        {loadError}
      </p>
    );
  }

  return (
    <div className="space-y-3" data-testid="threshold-editor">
      {rows.length === 0 ? (
        <p className="text-body-sm text-slate-500 dark:text-slate-400" data-testid="thresholds-empty">
          No thresholds defined — runs are not graded until you add one. A threshold is a bound a
          run&apos;s report is measured against: how the run did, never whether it passed.
        </p>
      ) : (
        <div className="space-y-2">
          {rows.map((row, i) => {
            const err = rowError(row);
            return (
              <div key={row.key} className="flex flex-wrap items-start gap-2" data-testid={`threshold-row-${i}`}>
                <label className="sr-only" htmlFor={`threshold-metric-${i}`}>
                  Metric
                </label>
                <select
                  id={`threshold-metric-${i}`}
                  className="block min-h-[44px] rounded-lg border border-slate-300 bg-white px-3 py-2 text-body-sm text-slate-900 focus:outline-none focus:ring-2 focus:ring-sky-500 dark:border-slate-700 dark:bg-slate-900 dark:text-white"
                  value={row.metric}
                  onChange={(e) => updateRow(row.key, { metric: e.target.value })}
                  data-testid={`threshold-metric-${i}`}
                >
                  {METRIC_OPTIONS.map((m) => (
                    <option key={m.value} value={m.value}>
                      {m.label}
                    </option>
                  ))}
                </select>
                <label className="sr-only" htmlFor={`threshold-comparison-${i}`}>
                  Comparison
                </label>
                <select
                  id={`threshold-comparison-${i}`}
                  className="block min-h-[44px] rounded-lg border border-slate-300 bg-white px-3 py-2 text-body-sm text-slate-900 focus:outline-none focus:ring-2 focus:ring-sky-500 dark:border-slate-700 dark:bg-slate-900 dark:text-white"
                  value={row.comparison}
                  onChange={(e) => updateRow(row.key, { comparison: e.target.value })}
                  data-testid={`threshold-comparison-${i}`}
                >
                  {COMPARISON_OPTIONS.map((c) => (
                    <option key={c.value} value={c.value}>
                      {c.label}
                    </option>
                  ))}
                </select>
                <label className="sr-only" htmlFor={`threshold-value-${i}`}>
                  Value
                </label>
                <div>
                  <input
                    id={`threshold-value-${i}`}
                    type="number"
                    step="any"
                    className="block w-32 min-h-[44px] rounded-lg border border-slate-300 bg-white px-3 py-2 text-body-sm text-slate-900 focus:outline-none focus:ring-2 focus:ring-sky-500 dark:border-slate-700 dark:bg-slate-900 dark:text-white"
                    value={Number.isNaN(row.value) ? '' : row.value}
                    onChange={(e) => updateRow(row.key, { value: e.target.value === '' ? NaN : Number(e.target.value) })}
                    aria-invalid={err !== null}
                    data-testid={`threshold-value-${i}`}
                  />
                  {err !== null && (
                    <p className="mt-1 text-caption text-red-600 dark:text-red-400" role="alert" data-testid={`threshold-error-${i}`}>
                      {err}
                    </p>
                  )}
                </div>
                <button
                  type="button"
                  onClick={() => removeRow(row.key)}
                  className="min-h-[44px] rounded-lg border border-slate-300 px-3 text-body-sm text-slate-600 hover:bg-slate-50 focus:outline-none focus:ring-2 focus:ring-sky-500 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
                  data-testid={`threshold-remove-${i}`}
                >
                  ✕ Remove
                </button>
                <p className="sr-only">{describeThreshold(row)}</p>
              </div>
            );
          })}
          <p className="text-caption text-slate-500 dark:text-slate-400" data-testid="thresholds-hint">
            Latency and throughput bounds must be greater than 0; error-rate bounds sit between 0 and 1.
          </p>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <Button variant="secondary" size="sm" onClick={addRow} data-testid="threshold-add">
          + Add threshold
        </Button>
        <Button variant="primary" size="sm" onClick={save} disabled={saving || invalid || !dirty} data-testid="threshold-save">
          {saving ? 'Saving…' : 'Save thresholds'}
        </Button>
        {saved && !dirty && (
          <span className="text-body-sm text-emerald-700 dark:text-emerald-400" data-testid="thresholds-saved" role="status">
            ✓ Saved
          </span>
        )}
        {saveError !== null && (
          <span className="text-sm text-red-600 dark:text-red-400" role="alert" data-testid="thresholds-save-error">
            {saveError}
          </span>
        )}
      </div>
    </div>
  );
}
