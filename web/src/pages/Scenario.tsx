import { useEffect, useState } from 'react';
import { useParams } from 'react-router-dom';
import Card, { CardContent, CardHeader, CardTitle } from '../components/ui/Card';
import TaurusEditor from '../components/TaurusEditor';
import { getScenariosByScenarioId, type Scenario } from '../api/generated';

/**
 * The minimal scenario page (/scenarios/:id): the scenario's name, its
 * template badge when it was instantiated from one, and the Taurus fragment
 * editor. It exists because instantiate needs somewhere real to land; there
 * is deliberately no scenario list page -- the NewTest "from template"
 * picker is the catalog's only browser.
 */
export default function Scenario() {
  const { id } = useParams();
  const scenarioId = Number(id);
  const [scenario, setScenario] = useState<Scenario | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setError(null);
    getScenariosByScenarioId(scenarioId)
      .then((s) => {
        if (alive) setScenario(s);
      })
      .catch((e: unknown) => {
        if (alive) setError(e instanceof Error ? e.message : 'Failed to load scenario.');
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [scenarioId]);

  if (loading) {
    return <p className="text-body-sm text-slate-500 dark:text-slate-400">Loading scenario…</p>;
  }
  if (error || !scenario) {
    return (
      <p className="text-sm text-red-600 dark:text-red-400" role="alert">
        {error ?? 'Scenario not found.'}
      </p>
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-display-sm text-slate-900 dark:text-white" data-testid="scenario-name">
          {scenario.name}
        </h1>
        {scenario.is_template && (
          <span
            className="mt-1 inline-block rounded-full bg-sky-100 px-2 py-0.5 text-caption text-sky-800 dark:bg-sky-900/40 dark:text-sky-200"
            data-testid="template-badge"
          >
            template · {scenario.template_name}
          </span>
        )}
      </div>
      <Card>
        <CardHeader>
          <CardTitle>Requests</CardTitle>
        </CardHeader>
        <CardContent>
          <TaurusEditor scenarioId={scenarioId} />
        </CardContent>
      </Card>
    </div>
  );
}
