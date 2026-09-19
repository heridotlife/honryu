import { describe, expect, it } from 'vitest';
import { calibrationSpecLines, engineShortfall, formatSpecQps, gateControls, outcomeBadge, phaseControls, shortTime } from './Execution';

// Phase 62: the calibration spec card's number/line formatting. The
// criterion is rendered as code by the card itself; these lines are the
// rest of the compact spec.
describe('calibrationSpecLines', () => {
  const spec = {
    criterion: 'failures>5%',
    seed_qps: 10,
    max_qps: 10000,
    max_steps: 20,
    hold_seconds: 30,
    cpu: '1',
    memory: '512Mi',
  };

  it('renders range, budget, and pod size as three caption lines', () => {
    expect(calibrationSpecLines(spec)).toEqual([
      'seed 10 → max 10000 qps',
      'up to 20 steps · 30s hold per step',
      'pod: 1 CPU · 512Mi memory',
    ]);
  });

  it('keeps fractional qps honest at one decimal', () => {
    expect(formatSpecQps(608.5)).toBe('608.5');
    expect(formatSpecQps(310)).toBe('310');
  });
});

// Phase 93's simplified control matrix: exactly two verbs. Start when
// idle or deployed (the composite picks deploy-first vs straight
// countdown by phase), Stop while running, nothing clickable before the
// status loads. Deploy/Trigger/Purge as separate hub buttons are gone.
// Asserted without a DOM.
describe('phaseControls', () => {
  it('idle offers Start only — no deploy/trigger/purge entries at all', () => {
    expect(phaseControls('idle')).toEqual([{ action: 'start', enabled: true }]);
  });

  it('deployed offers Start only (straight countdown → trigger; no purge)', () => {
    expect(phaseControls('deployed')).toEqual([{ action: 'start', enabled: true }]);
  });

  it('running offers Stop only', () => {
    expect(phaseControls('running')).toEqual([{ action: 'stop', enabled: true }]);
  });

  it('null phase (not loaded): both verbs present but nothing clickable', () => {
    expect(phaseControls(null)).toEqual([
      { action: 'start', enabled: false },
      { action: 'stop', enabled: false },
    ]);
  });
});

// Phase 20, AC14: the session decides whether a control exists at all. The
// permission maps are the personas' Permissions() output from DefaultCatalog.
describe('gateControls', () => {
  const mapCan = (permissions: Record<string, string[]>) => (resource: string, action: string) => {
    if (permissions['*']?.includes('*')) return true;
    return (permissions[resource] ?? []).includes(action);
  };

  const viewer = {
    project: ['list', 'read'],
    execution: ['list', 'read'],
    scenario: ['list', 'read'],
    run: ['list', 'read'],
    schedule: ['list', 'read'],
    report: ['list', 'read'],
  };
  const editor = {
    run: ['create', 'delete', 'list', 'read', 'update'],
  };
  const campaignManager = {
    campaign: ['admin', 'create', 'delete', 'list', 'read', 'update'],
    schedule: ['list', 'read'],
  };

  it('tenant_viewer renders no lifecycle control in any phase (AC14)', () => {
    for (const phase of ['idle', 'deployed', 'running', null] as const) {
      expect(gateControls(phaseControls(phase), mapCan(viewer))).toEqual([]);
    }
  });

  it('campaign_manager sees the plan but cannot change it (AC10)', () => {
    expect(gateControls(phaseControls('running'), mapCan(campaignManager))).toEqual([]);
  });

  it('tenant_editor and admin keep every offered control, with phase enablement preserved', () => {
    const gated = gateControls(phaseControls('idle'), mapCan(editor));
    expect(gated).toEqual([{ action: 'start', enabled: true }]);
    expect(gateControls(phaseControls('running'), mapCan({ '*': ['*'] }))).toEqual([
      { action: 'stop', enabled: true },
    ]);
  });

  it('partial grants keep only the controls they cover', () => {
    // Someone who may start but never stop: Start survives in the startable
    // phases, Stop drops out of running.
    const partialCan = (resource: string, action: string) => resource === 'run' && action === 'create';
    expect(gateControls(phaseControls('deployed'), partialCan)).toEqual([{ action: 'start', enabled: true }]);
    expect(gateControls(phaseControls('running'), partialCan)).toEqual([]);
  });
});

describe('engineShortfall', () => {
  it('floors at zero (terminating engine can briefly over-report)', () => {
    expect(engineShortfall({ engines: 3, engines_deployed: 3 })).toBe(0);
    expect(engineShortfall({ engines: 3, engines_deployed: 4 })).toBe(0);
    expect(engineShortfall({ engines: 3, engines_deployed: 1 })).toBe(2);
  });
});

// R3: report rows and log tails.
describe('outcomeBadge', () => {
  it('maps every outcome to a non-empty class string', () => {
    for (const outcome of ['passed', 'failed', 'aborted', 'error'] as const) {
      expect(outcomeBadge(outcome)).toMatch(/bg-/);
    }
    // passed and failed must color differently -- the whole point of a badge.
    expect(outcomeBadge('passed')).not.toBe(outcomeBadge('failed'));
    expect(outcomeBadge('aborted')).not.toBe(outcomeBadge('error'));
  });
});

describe('shortTime', () => {
  it('formats an ISO timestamp as MM-DD HH:mm', () => {
    expect(shortTime('2026-09-02T10:05:00Z')).toMatch(/^\d{2}-\d{2} \d{2}:\d{2}$/);
  });

  it('passes through garbage unchanged (no crash on odd server data)', () => {
    expect(shortTime('not-a-date')).toBe('not-a-date');
  });
});
