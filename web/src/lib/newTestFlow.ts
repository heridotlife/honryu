// The R9 flow's pure decision logic: project-if-absent resolution and the
// concurrency/engines warning. The page wires the API calls around these.

export interface NewTestForm {
  name: string;
  targetUrl: string;
  method: string;
  headers: Array<{ name: string; value: string }>;
  concurrency: number;
  engines: number;
  rampup: number;
  duration: number;
  engine: string;
}

/** R9's guard: shard.Plan clamps engines to concurrency SILENTLY. */
export function concurrencyEnginesWarning(concurrency: number, engines: number): string | null {
  if (concurrency > 0 && engines > concurrency) {
    return (
      `${engines} engines requested but concurrency is only ${concurrency} — ` +
      `the scheduler will silently clamp to ${concurrency} engines (one request each). ` +
      `Raise concurrency to at least ${engines}.`
    );
  }
  return null;
}

/**
 * The fragment the flow writes via G3 (PUT /scenarios/{id}/requests): a
 * bare taurus.Scenario with root-level default-address/requests — NOT
 * wrapped in a scenarios: map. G3 unmarshals one Scenario; the wrapped
 * shape leaves Requests empty and 400s with "at least one request is
 * required" (phase 22 finding 1, verified live).
 */
export function buildFragment(form: NewTestForm): string {
  const lines: string[] = [];
  lines.push(`default-address: ${form.targetUrl.replace(/\/$/, '')}`);
  if (form.headers.length > 0) {
    lines.push('headers:');
    for (const h of form.headers) {
      lines.push(`    ${h.name}: ${h.value}`);
    }
  }
  lines.push('requests:');
  lines.push('    - method: ' + form.method);
  lines.push('      url: /');
  return lines.join('\n') + '\n';
}

/** The execution config the flow writes via G7 (PUT /executions/{id}/config, JSON). */
export function buildConfig(form: NewTestForm, scenarioId: number) {
  return {
    name: `${form.name}-load`,
    project_id: 0, // patched by the caller after creation
    execution_id: 0, // patched by the caller
    tests: [
      {
        name: form.name,
        scenario_id: scenarioId,
        concurrency: form.concurrency,
        rampup: form.rampup,
        engines: form.engines,
        duration: form.duration,
      },
    ],
  };
}

/** The ordered steps; a failure names the step, never a bare error. */
export const flowSteps = [
  'resolve project',
  'create scenario',
  'create execution',
  'save requests fragment',
  'save load config',
] as const;
export type FlowStep = (typeof flowSteps)[number];

/** Wraps an error with the step that failed. */
export function stepError(step: FlowStep, err: unknown): Error {
  const msg = err instanceof Error ? err.message : String(err);
  return new Error(`Step "${step}" failed: ${msg}`);
}
