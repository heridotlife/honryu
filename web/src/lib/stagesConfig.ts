// The stage table's pure model: rows in the visual editor <-> the config
// JSON the backend's loadprofile.Profile/Entry decode. The JSON contract
// is newTestFlow.buildConfig's output -- the single-row case must be
// byte-equal to it -- and the field order mirrors Go's json marshal order
// (name, scenario_id, concurrency, rampup, engines, throughput, duration,
// csv_split), which is why throughput is inserted between engines and
// duration rather than appended.

/** One visual stage row. `throughput`/`csvSplit`/`mode`/`steps` omitted = their defaults. */
export interface StageRow {
  name: string;
  scenarioId: number;
  concurrency: number;
  rampup: number;
  engines: number;
  /** Target req/s shared across the row's engines. Omitted/0 = unlimited. */
  throughput?: number;
  duration: number;
  /** Split a CSV dataset across engines. Omitted/false = off. */
  csvSplit?: boolean;
  /** Phase 90 mode provenance ("burst"/"ramp"/"soak"/"staircase"). Omitted = advanced. */
  mode?: string;
  /** Phase 98 staircase step count (2–10); only meaningful on a
   * staircase row -- never emitted anywhere else. */
  steps?: number;
}

/** One tests[] entry on the wire (loadprofile.Entry's JSON shape). */
export interface StageTestJSON {
  name: string;
  scenario_id: number;
  concurrency: number;
  rampup: number;
  engines: number;
  throughput?: number;
  duration: number;
  csv_split?: boolean;
  /** Phase 90 mode provenance; last field, mirroring Go's marshal order. */
  mode?: string;
  /** Phase 98 staircase step count; after mode, mirroring Go's marshal order. */
  steps?: number;
}

/** The wrapper the flow PUTs to /executions/{id}/config (buildConfig's shape). */
export interface StagesConfigJSON {
  name: string;
  project_id: number;
  execution_id: number;
  tests: StageTestJSON[];
}

/**
 * Rows -> config JSON. Omission rules match the backend's omitempty
 * intent: throughput 0/undefined is unlimited and the key is omitted
 * (loadprofile.Throughput); csvSplit false is omitted. Note Go's Entry
 * tag for csv_split lacks omitempty so the backend may echo an explicit
 * false -- configToStages accepts both, we simply never write false.
 */
export function stagesToConfig(
  rows: StageRow[],
  name: string,
  projectId: number,
  executionId: number
): StagesConfigJSON {
  return {
    name,
    project_id: projectId,
    execution_id: executionId,
    tests: rows.map((r): StageTestJSON => {
      const omitTp = r.throughput === undefined || r.throughput === 0;
      return {
        name: r.name,
        scenario_id: r.scenarioId,
        concurrency: r.concurrency,
        rampup: r.rampup,
        engines: r.engines,
        // Conditional spreads keep the key at this position whether or
        // not it is present -- Go marshal order, and byte-equality with
        // buildConfig when both optional keys are absent.
        ...(omitTp ? {} : { throughput: r.throughput }),
        duration: r.duration,
        ...(r.csvSplit ? { csv_split: true as const } : {}),
        ...(r.mode ? { mode: r.mode } : {}),
        // A staircase row's step count, after mode (Go's marshal order);
        // never anywhere else -- the server refuses the field otherwise.
        ...(r.mode === 'staircase' && r.steps ? { steps: r.steps } : {}),
      };
    }),
  };
}

/**
 * Per-field validation of one row, mirroring loadprofile.Entry.Validate's
 * semantics and message copy (the UI's short forms): scenario required /
 * engines / concurrency / duration must be positive, throughput cannot be
 * negative. Collects all offenders (for inline display) rather than
 * stopping at the first like the Go switch does.
 */
export interface StageRowErrors {
  scenarioId?: string;
  engines?: string;
  concurrency?: string;
  duration?: string;
  throughput?: string;
}

export function validateStageRow(row: StageRow): StageRowErrors {
  const e: StageRowErrors = {};
  if (row.scenarioId <= 0) {
    e.scenarioId = 'scenario required';
  }
  if (row.engines <= 0) {
    e.engines = 'engines must be positive';
  }
  if (row.concurrency <= 0) {
    e.concurrency = 'concurrency must be positive';
  }
  if (row.duration <= 0) {
    e.duration = 'duration must be positive';
  }
  if (row.throughput !== undefined && row.throughput < 0) {
    e.throughput = 'throughput cannot be negative';
  }
  return e;
}

const hasErrors = (e: StageRowErrors) => Object.keys(e).length > 0;

/** Full Validate mirror over a row set: at least one row, every row clean. */
export function stageRowsValid(rows: StageRow[]): boolean {
  return rows.length > 0 && rows.every(r => !hasErrors(validateStageRow(r)));
}

/**
 * Table-mode validity: the fields the stage table actually edits. The
 * scenario id is assigned by the host flow at submit (NewTest creates the
 * scenario first), so a pending scenario does not block the table -- raw
 * JSON mode owns the full check (see stageRowsValid).
 */
export function editableRowsValid(rows: StageRow[]): boolean {
  return (
    rows.length > 0 &&
    rows.every(r => {
      const { scenarioId: _scenario, ...editable } = validateStageRow(r);
      return !hasErrors(editable);
    })
  );
}
/**
 * Config JSON -> rows. Mechanical, never validating
 * (see validateStageRow
 * for that); tolerant of absent keys and canonicalizes the omitted
 * defaults: throughput 0/absent -> undefined (unlimited), csv_split
 * false/absent -> undefined (off).
 */
export function configToStages(cfg: StagesConfigJSON): StageRow[] {
  const tests = Array.isArray(cfg?.tests) ? cfg.tests : [];
  return tests.map(t => ({
    name: t.name,
    scenarioId: t.scenario_id,
    concurrency: t.concurrency,
    rampup: t.rampup,
    engines: t.engines,
    throughput: t.throughput === 0 || t.throughput === undefined ? undefined : t.throughput,
    duration: t.duration,
    csvSplit: t.csv_split ? true : undefined,
    // Phase 90: mode provenance MUST survive the round trip -- dropping
    // it here would silently launder a mode config into an advanced one
    // the moment an editor re-serialized it. Phase 98: the staircase's
    // step count rides the same rule.
    mode: t.mode || undefined,
    steps: t.steps || undefined,
  }));
}
