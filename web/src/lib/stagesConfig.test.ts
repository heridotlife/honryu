import { describe, expect, it } from 'vitest';
import {
  configToStages,
  editableRowsValid,
  stageRowsValid,
  stagesToConfig,
  validateStageRow,
  type StageRow,
  type StagesConfigJSON,
} from './stagesConfig';
import { buildConfig, type NewTestForm } from './newTestFlow';

// The R9 form fixture from newTestFlow.test.ts -- reused so the
// byte-equality claim compares against the same shape the live flow
// sends today.
const form: NewTestForm = {
  name: 'checkout-smoke',
  targetUrl: 'http://checkout.svc/',
  method: 'GET',
  headers: [{ name: 'X-Auth', value: 'tok' }],
  concurrency: 50,
  engines: 2,
  rampup: 30,
  duration: 300,
  engine: 'jmeter',
};

/** The single row the NewTest flow derives from its form, per buildConfig's mapping. */
function rowFromForm(f: NewTestForm, scenarioId: number): StageRow {
  return {
    name: f.name,
    scenarioId,
    concurrency: f.concurrency,
    engines: f.engines,
    rampup: f.rampup,
    duration: f.duration,
    // throughput: NewTestForm has no throughput field; unlimited = omitted.
  };
}

describe('stagesToConfig', () => {
  it('byte-equals buildConfig output for the single-row case', () => {
    const viaStages = stagesToConfig([rowFromForm(form, 42)], `${form.name}-load`, 0, 0);
    const viaBuild = buildConfig(form, 42);
    // Byte equality: key order and omitted keys are the contract, so
    // compare the serialized forms, not just deep equality.
    expect(JSON.stringify(viaStages)).toBe(JSON.stringify(viaBuild));
    expect(viaStages).toEqual(viaBuild);
  });

  it('byte-equals buildConfig for other form values too (names, zeros, big numbers)', () => {
    const forms: NewTestForm[] = [
      { ...form, name: 'a b c', concurrency: 1, engines: 1, rampup: 0, duration: 1 },
      { ...form, name: 'x', concurrency: 4000, engines: 99, rampup: 900, duration: 86400 },
    ];
    for (const f of forms) {
      expect(JSON.stringify(stagesToConfig([rowFromForm(f, 7)], `${f.name}-load`, 0, 0))).toBe(
        JSON.stringify(buildConfig(f, 7))
      );
    }
  });

  it('carries the wrapper ids it is given (the caller patches them after creation)', () => {
    const cfg = stagesToConfig([rowFromForm(form, 1)], 'n-load', 7, 9);
    expect(cfg.name).toBe('n-load');
    expect(cfg.project_id).toBe(7);
    expect(cfg.execution_id).toBe(9);
  });

  it('emits every row in order', () => {
    const rows: StageRow[] = [rowFromForm(form, 1), { ...rowFromForm(form, 2), concurrency: 10, duration: 60 }];
    const cfg = stagesToConfig(rows, 'n-load', 0, 0);
    expect(cfg.tests).toHaveLength(2);
    expect(cfg.tests[1].concurrency).toBe(10);
    expect(cfg.tests[1].duration).toBe(60);
  });

  it('omits throughput when undefined or zero (unlimited), keeps it positive-only', () => {
    const base = rowFromForm(form, 1);
    expect(JSON.stringify(stagesToConfig([base], 'n', 0, 0))).not.toContain('throughput');
    expect(JSON.stringify(stagesToConfig([{ ...base, throughput: 0 }], 'n', 0, 0))).not.toContain('throughput');
    const withTp = stagesToConfig([{ ...base, throughput: 125 }], 'n', 0, 0);
    expect(withTp.tests[0].throughput).toBe(125);
  });

  it('omits csv_split when false/undefined, emits it when true', () => {
    const base = rowFromForm(form, 1);
    expect(JSON.stringify(stagesToConfig([base], 'n', 0, 0))).not.toContain('csv_split');
    expect(JSON.stringify(stagesToConfig([{ ...base, csvSplit: false }], 'n', 0, 0))).not.toContain('csv_split');
    expect(stagesToConfig([{ ...base, csvSplit: true }], 'n', 0, 0).tests[0].csv_split).toBe(true);
  });

  it('slots throughput between engines and duration (Go json field order)', () => {
    const cfg = stagesToConfig([{ ...rowFromForm(form, 1), throughput: 10 }], 'n', 0, 0);
    expect(JSON.stringify(cfg.tests[0])).toContain('"engines":2,"throughput":10,"duration":300');
  });
});

describe('configToStages', () => {
  it('reads a backend-shaped config back into rows', () => {
    const cfg: StagesConfigJSON = {
      name: 'checkout-smoke-load',
      project_id: 3,
      execution_id: 8,
      tests: [
        {
          name: 'checkout-smoke',
          scenario_id: 42,
          concurrency: 50,
          rampup: 30,
          engines: 2,
          throughput: 125,
          duration: 300,
          csv_split: true,
        },
      ],
    };
    expect(configToStages(cfg)).toEqual([
      {
        name: 'checkout-smoke',
        scenarioId: 42,
        concurrency: 50,
        rampup: 30,
        engines: 2,
        throughput: 125,
        duration: 300,
        csvSplit: true,
      },
    ]);
  });

  it('canonicalizes absent/zero throughput and false csv_split to their omitted forms', () => {
    const cfg: StagesConfigJSON = {
      name: 'n',
      project_id: 0,
      execution_id: 0,
      tests: [
        { name: 'a', scenario_id: 1, concurrency: 1, rampup: 0, engines: 1, duration: 1 },
        {
          name: 'b',
          scenario_id: 2,
          concurrency: 1,
          rampup: 0,
          engines: 1,
          throughput: 0,
          duration: 1,
          csv_split: false,
        },
      ],
    };
    const rows = configToStages(cfg);
    expect(rows[0].throughput).toBeUndefined();
    expect(rows[0].csvSplit).toBeUndefined();
    expect(rows[1].throughput).toBeUndefined();
    expect(rows[1].csvSplit).toBeUndefined();
  });

  it('returns [] for a config without a tests array', () => {
    expect(configToStages({ name: 'n', project_id: 0, execution_id: 0, tests: [] })).toEqual([]);
    // Defensive: a malformed object never throws, it reads as no rows.
    expect(configToStages({ name: 'n', project_id: 0, execution_id: 0 } as StagesConfigJSON)).toEqual([]);
  });
});

describe('validateStageRow (loadprofile.Entry.Validate mirror)', () => {
  const valid: StageRow = { name: 'a', scenarioId: 1, concurrency: 10, engines: 2, rampup: 5, duration: 60 };

  it('passes a valid row with no errors', () => {
    expect(validateStageRow(valid)).toEqual({});
  });

  it('flags scenario id <= 0 as "scenario required"', () => {
    expect(validateStageRow({ ...valid, scenarioId: 0 }).scenarioId).toBe('scenario required');
  });

  it('flags engines <= 0 as "engines must be positive"', () => {
    expect(validateStageRow({ ...valid, engines: 0 }).engines).toBe('engines must be positive');
  });

  it('flags concurrency <= 0 as "concurrency must be positive"', () => {
    expect(validateStageRow({ ...valid, concurrency: 0 }).concurrency).toBe('concurrency must be positive');
  });

  it('flags duration <= 0 as "duration must be positive"', () => {
    expect(validateStageRow({ ...valid, duration: 0 }).duration).toBe('duration must be positive');
  });

  it('flags negative throughput (ErrThroughputInvalid mirror); unlimited is fine', () => {
    expect(validateStageRow({ ...valid, throughput: -1 }).throughput).toBe('throughput cannot be negative');
    expect(validateStageRow({ ...valid, throughput: undefined }).throughput).toBeUndefined();
    expect(validateStageRow({ ...valid, throughput: 200 }).throughput).toBeUndefined();
  });

  it('collects every offending field at once (inline display, not first-error)', () => {
    const errs = validateStageRow({ ...valid, scenarioId: 0, engines: 0, concurrency: 0, duration: 0 });
    expect(Object.keys(errs).sort()).toEqual(['concurrency', 'duration', 'engines', 'scenarioId']);
  });
});

describe('row-set validity helpers', () => {
  const good: StageRow = { name: 'a', scenarioId: 1, concurrency: 10, engines: 2, rampup: 5, duration: 60 };

  it('stageRowsValid: full mirror -- needs rows, all clean including scenario', () => {
    expect(stageRowsValid([])).toBe(false);
    expect(stageRowsValid([good])).toBe(true);
    expect(stageRowsValid([{ ...good, scenarioId: 0 }])).toBe(false);
    expect(stageRowsValid([good, { ...good, duration: 0 }])).toBe(false);
  });

  it('editableRowsValid: same minus the scenario id (the flow assigns it at submit)', () => {
    expect(editableRowsValid([])).toBe(false);
    expect(editableRowsValid([{ ...good, scenarioId: 0 }])).toBe(true);
    expect(editableRowsValid([{ ...good, scenarioId: 0, engines: 0 }])).toBe(false);
  });
});

describe('round-trip property: configToStages(stagesToConfig(rows)) === rows', () => {
  // No property-test dependency in the house set, so a seeded LCG
  // generates the row sets deterministically: same failure reproduces.
  function lcg(seed: number): () => number {
    let s = seed >>> 0;
    return () => {
      s = (1664525 * s + 1013904223) >>> 0;
      return s / 2 ** 32;
    };
  }

  it('holds for 200 generated valid row sets', () => {
    const rand = lcg(20260905);
    const int = (min: number, max: number) => min + Math.floor(rand() * (max - min + 1));
    for (let iter = 0; iter < 200; iter++) {
      const rowCount = int(1, 5);
      const rows: StageRow[] = Array.from({ length: rowCount }, () => ({
        name: `stage-${int(0, 99)}`,
        scenarioId: int(1, 1000),
        concurrency: int(1, 500),
        engines: int(1, 20),
        rampup: int(0, 600),
        // Valid domain only: throughput is unlimited (omitted) or positive;
        // csvSplit is off (omitted) or true. Zero/false ARE the omitted
        // defaults, and the round-trip canonicalizes to omission.
        throughput: rand() < 0.5 ? undefined : int(1, 2000),
        duration: int(1, 3600),
        csvSplit: rand() < 0.5 ? undefined : true,
      }));
      const back = configToStages(stagesToConfig(rows, 'prop-load', int(0, 99), int(0, 99)));
      // toEqual ignores undefined-keyed properties, which is exactly the
      // "modulo omitted defaults" equivalence the contract asks for.
      expect(back).toEqual(rows);
    }
  });
});

// Phase 90: mode provenance must survive every rows -> wire -> rows
// round trip. Dropping it would silently launder a mode config into an
// advanced one the moment an editor re-serialized it; writing it in the
// wrong position would break the Go-marshal-order mirror.
describe('mode provenance (phase 90)', () => {
  it('round-trips mode through configToStages/stagesToConfig', () => {
    const rows: StageRow[] = [
      {
        name: 'a',
        scenarioId: 1,
        concurrency: 375,
        rampup: 120,
        engines: 4,
        throughput: 500,
        duration: 600,
        mode: 'ramp',
      },
      { name: 'b', scenarioId: 2, concurrency: 10, rampup: 0, engines: 1, duration: 60 },
    ];
    const back = configToStages(stagesToConfig(rows, 'p90', 7, 9));
    expect(back[0].mode).toBe('ramp');
    expect(back[1].mode).toBeUndefined();
  });

  it('writes mode last on the wire (Go marshal order, after csv_split)', () => {
    const cfg = stagesToConfig(
      [
        {
          name: 'a',
          scenarioId: 1,
          concurrency: 375,
          rampup: 120,
          engines: 4,
          throughput: 500,
          duration: 600,
          csvSplit: true,
          mode: 'soak',
        },
      ],
      'p90',
      7,
      9
    );
    expect(Object.keys(cfg.tests[0])).toEqual([
      'name',
      'scenario_id',
      'concurrency',
      'rampup',
      'engines',
      'throughput',
      'duration',
      'csv_split',
      'mode',
    ]);
    expect(JSON.parse(JSON.stringify(cfg.tests[0])).mode).toBe('soak');
  });

  it('omits the mode key entirely for advanced rows (byte-compat)', () => {
    const cfg = stagesToConfig(
      [{ name: 'a', scenarioId: 1, concurrency: 5, rampup: 10, engines: 2, duration: 30 }],
      'p90',
      7,
      9
    );
    expect(JSON.stringify(cfg.tests[0])).not.toContain('mode');
  });
});

// Phase 98: a staircase row's step count survives the rows <-> wire round
// trip beside its mode, and never leaks onto a non-staircase entry.
describe('stagesConfig steps round trip (phase 98)', () => {
  it('carries steps only on staircase rows', () => {
    const rows: StageRow[] = [
      { name: 'probe', scenarioId: 7, concurrency: 30, rampup: 0, engines: 2, duration: 120, mode: 'staircase', steps: 4 },
      { name: 'blast', scenarioId: 8, concurrency: 5, rampup: 10, engines: 1, duration: 60, mode: 'burst', steps: 4 },
    ];
    const cfg = stagesToConfig(rows, 't', 1, 2);
    expect(cfg.tests[0].mode).toBe('staircase');
    expect(cfg.tests[0].steps).toBe(4);
    // steps is the last key (Go marshal order), and a non-staircase row
    // never emits it even if the value rides the row.
    expect(Object.keys(cfg.tests[0]).pop()).toBe('steps');
    expect(cfg.tests[1].steps).toBeUndefined();
    const back = configToStages({
      ...cfg,
      tests: [
        cfg.tests[0],
        { ...cfg.tests[1], steps: undefined },
      ],
    });
    expect(back[0].mode).toBe('staircase');
    expect(back[0].steps).toBe(4);
  });
});
