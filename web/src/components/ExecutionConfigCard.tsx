// The Configuration card (phase 27 T2): the execution's multi-test config,
// with target QPS (throughput) visible and editable. Loaded only while the
// execution is idle; PUT writes the full wrapper back. Unchanged fields
// round-trip verbatim -- only throughput is editable here (the full editor
// is NewTest's job at creation time).
import { useEffect, useState } from 'react';
import Card, { CardHeader, CardTitle, CardContent } from './ui/Card';
import Button from './ui/Button';
import {
  getExecutionConfig,
  putExecutionConfig,
  type ExecutionConfig,
} from '../api/executionsConfig';
import { ApiError } from '../api/client';

interface Props {
  executionId: number;
  canUpdate: boolean;
}

export default function ExecutionConfigCard({ executionId, canUpdate }: Props) {
  const [cfg, setCfg] = useState<ExecutionConfig | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [savedAt, setSavedAt] = useState<string | null>(null);
  // Draft throughput per test index: '' = unlimited (wire omits the key).
  const [drafts, setDrafts] = useState<Record<number, string>>({});
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    let alive = true;
    setCfg(null);
    setError(null);
    setDrafts({});
    setDirty(false);
    getExecutionConfig(executionId)
      .then((c) => {
        if (!alive) return;
        setCfg(c);
        const d: Record<number, string> = {};
        c.tests.forEach((t, i) => {
          d[i] = t.throughput == null ? '' : String(t.throughput);
        });
        setDrafts(d);
      })
      .catch((e: unknown) => {
        if (alive) setError(e instanceof ApiError ? e.message : 'failed to load config');
      });
    return () => {
      alive = false;
    };
  }, [executionId]);

  const setDraft = (i: number, v: string) => {
    setDrafts((prev) => ({ ...prev, [i]: v }));
    setDirty(true);
    setSavedAt(null);
  };

  const save = () => {
    if (!cfg) return;
    const next: ExecutionConfig = {
      ...cfg,
      tests: cfg.tests.map((t, i) => {
        const raw = drafts[i];
        const n = raw === '' ? undefined : Number(raw);
        // undefined = unlimited = omit the key; invalid text is rejected below
        return n !== undefined && !Number.isFinite(n) ? t : { ...t, throughput: n };
      }),
    };
    setBusy(true);
    putExecutionConfig(executionId, next)
      .then(() => {
        setCfg(next);
        setDirty(false);
        setSavedAt(new Date().toLocaleTimeString());
      })
      .catch((e: unknown) => setError(e instanceof ApiError ? e.message : 'failed to save config'))
      .finally(() => setBusy(false));
  };

  const invalid = Object.values(drafts).some((v) => v !== '' && !Number.isFinite(Number(v)));

  if (error) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>Configuration</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-slate-500 dark:text-slate-400" role="alert">
            {error}
          </p>
        </CardContent>
      </Card>
    );
  }
  if (!cfg) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>Configuration</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-body-sm text-slate-500 dark:text-slate-400" data-testid="config-loading">
            Loading configuration…
          </p>
        </CardContent>
      </Card>
    );
  }
  return (
    <Card data-testid="config-card">
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle>Configuration</CardTitle>
        {canUpdate && (
          <div className="flex items-center gap-2">
            {savedAt && <span className="text-caption text-slate-500">saved {savedAt}</span>}
            <Button onClick={save} disabled={busy || !dirty || invalid} data-testid="config-save">
              {busy ? 'Saving…' : 'Save'}
            </Button>
          </div>
        )}
      </CardHeader>
      <CardContent className="overflow-x-auto">
        <table className="w-full text-left text-body-sm" data-testid="config-table">
          <thead>
            <tr className="text-caption border-b border-slate-200 text-slate-500 dark:border-slate-700 dark:text-slate-400">
              <th scope="col" className="px-3 py-2 font-medium">Test</th>
              <th scope="col" className="px-3 py-2 font-medium">Scenario</th>
              <th scope="col" className="px-3 py-2 font-medium">Concurrency</th>
              <th scope="col" className="px-3 py-2 font-medium">Ramp-up (s)</th>
              <th scope="col" className="px-3 py-2 font-medium">Engines</th>
              <th scope="col" className="px-3 py-2 font-medium">Target QPS (req/s)</th>
              <th scope="col" className="px-3 py-2 font-medium">Duration (s)</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {cfg.tests.map((t, i) => (
              <tr key={i}>
                <td className="px-3 py-2">{t.name || `test ${i + 1}`}</td>
                <td className="px-3 py-2">{t.scenario_id}</td>
                <td className="px-3 py-2">{t.concurrency}</td>
                <td className="px-3 py-2">{t.rampup}</td>
                <td className="px-3 py-2">{t.engines}</td>
                <td className="px-3 py-2">
                  {canUpdate ? (
                    <input
                      type="number"
                      min={1}
                      className="w-28 rounded-md border border-slate-300 bg-white px-2 py-1 text-sm dark:border-slate-600 dark:bg-slate-800 dark:text-slate-100"
                      aria-label={`config test ${i + 1} throughput`}
                      placeholder="unlimited"
                      value={drafts[i] ?? ''}
                      onChange={(e) => setDraft(i, e.target.value)}
                      data-testid={`config-throughput-${i}`}
                    />
                  ) : (
                    <span>{t.throughput == null ? 'unlimited' : t.throughput}</span>
                  )}
                </td>
                <td className="px-3 py-2">{t.duration}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <p className="text-caption mt-2 text-slate-500 dark:text-slate-400">
          Empty target QPS = unlimited. {canUpdate ? 'Save writes the whole config back.' : 'Read-only for your role.'}
        </p>
      </CardContent>
    </Card>
  );
}
