import { describe, expect, it } from 'vitest';
import appSource from './App.tsx?raw';

// R1's route contract, pinned against the router's source, updated by phase
// 67b's scenario-first inversion: the /status bookmark and the old flat
// /executions list URL both redirect to /scenarios (one hop each -- the
// /executions redirect must not chain), while the run hub stays mounted at
// /executions/:id. Reading the mounted component through the router in jsdom
// drags every page's data fetching with it; the ?raw import keeps this test
// to the wiring itself (App.test.tsx mounts the redirect for real).
describe('App routes (R1, phase 67b)', () => {
  it('redirects /status to /scenarios', () => {
    const statusRoute = appSource.split('\n').find((l) => l.includes('path="/status"'));
    expect(statusRoute).toBeDefined();
    expect(statusRoute).toContain('Navigate');
    expect(statusRoute).toContain('to="/scenarios"');
  });

  it('redirects the old flat list at /executions to /scenarios', () => {
    const execRoute = appSource.split('\n').find((l) => l.includes('path="/executions"'));
    expect(execRoute).toBeDefined();
    expect(execRoute).toContain('Navigate');
    expect(execRoute).toContain('to="/scenarios"');
  });

  it('keeps the run hub mounted at /executions/:id', () => {
    const hubRoute = appSource.split('\n').find((l) => l.includes('path="/executions/:id"'));
    expect(hubRoute).toBeDefined();
    expect(hubRoute).toContain('Execution');
    expect(hubRoute).not.toContain('Navigate');
  });

  it('mounts the scenarios list at /scenarios', () => {
    const listRoute = appSource.split('\n').find((l) => l.includes('path="/scenarios"'));
    expect(listRoute).toBeDefined();
    expect(listRoute).toContain('Scenarios');
  });

  it('does not mount LiveStatus at /status anymore', () => {
    const statusRoute = appSource.split('\n').find((l) => l.includes('path="/status"'));
    expect(statusRoute).not.toContain('LiveStatus');
  });
});

// Phase 20's route contract: / is the profile picker (its own redirect to
// /reports happens only once a session exists), and the whole app sits
// inside SessionProvider so the picker, nav, and action buttons read one
// /api/me. Same ?raw approach as R1 above.
describe('App routes (phase 20)', () => {
  it('mounts the profile picker at / instead of an unconditional redirect', () => {
    const rootRoute = appSource.split('\n').find((l) => l.includes('path="/"'));
    expect(rootRoute).toBeDefined();
    expect(rootRoute).toContain('ProfilePicker');
    expect(rootRoute).not.toContain('Navigate');
  });

  it('wraps the routed app in SessionProvider', () => {
    expect(appSource).toContain('<SessionProvider>');
    // Inside the router (the picker uses Navigate), around the layout
    // (DashboardLayout reads the session too, task 23).
    expect(appSource.indexOf('<SessionProvider>')).toBeLessThan(appSource.indexOf('<DashboardLayout'));
  });
});
