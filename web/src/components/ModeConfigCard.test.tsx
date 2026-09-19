// The mode-aware config card (phase 90): summary + derivation note +
// Simple re-apply. Mounted with a stubbed config API; the re-apply PUT is
// captured and its shape pinned (mode + rate + duration, zeros for the
// server-owned fields), and a 409 surfaces the structured remediation
// inline -- the calibrate-then-configure loop's closing half.
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import ModeConfigCard from './ModeConfigCard';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | null = null;
let root: ReturnType<typeof createRoot> | null = null;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });

const modeCfg = {
  'multi-test': {
    name: 'p90-1',
    project_id: 3,
    execution_id: 7,
    tests: [
      // The spec's worked example, resolved: burst 500 rps for 10 minutes.
      {
        name: 'checkout',
        scenario_id: 5,
        concurrency: 375,
        rampup: 0,
        engines: 4,
        throughput: 500,
        duration: 600,
        mode: 'burst',
      },
    ],
  },
};

const advancedCfg = {
  'multi-test': {
    name: 'adv',
    project_id: 3,
    execution_id: 7,
    tests: [{ name: 'a', scenario_id: 5, concurrency: 2, rampup: 5, engines: 1, duration: 120 }],
  },
};

const capacityKey = { engine: 'jmeter', cpu: '500m', memory: '512Mi' };

async function render(canUpdate = true) {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter>
        <ModeConfigCard executionId={7} canUpdate={canUpdate} capacityKey={capacityKey} />
      </MemoryRouter>
    );
  });
  await act(async () => {});
}

beforeEach(() => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/api/executions/7/config')) {
        return json(modeCfg);
      }
      // The derivation note's optional per-pod rate: a profile exists.
      if (url.includes('/api/scenarios/5/capacity-profile')) {
        return json({
          scenario_id: 5,
          engine: 'jmeter',
          cpu: '500m',
          memory: '512Mi',
          per_pod_qps: 125,
          saturated_by: 'engine',
        });
      }
      return json({ message: `no stub for ${url}` }, 500);
    })
  );
});
afterEach(() => {
  act(() => root?.unmount());
  container?.remove();
  container = null;
  root = null;
  vi.unstubAllGlobals();
});

describe('ModeConfigCard (phase 90)', () => {
  it('renders the chip, resolved numbers, and the derivation note', async () => {
    await render();
    expect(container!.querySelector('[data-testid="mode-config-chip"]')?.textContent).toBe('burst · 500 rps · 10m');
    const table = container!.querySelector('[data-testid="mode-config-table"]');
    expect(table?.textContent).toContain('375');
    expect(table?.textContent).toContain('4');
    const note = container!.querySelector('[data-testid="mode-derivation"]');
    expect(note?.textContent).toContain("concurrency 375 ← Little's Law (500 rps × p95 ≈ 250ms × 3.0 headroom)");
    expect(note?.textContent).toContain('engines 4 ← capacity profile 125 rps/pod at 500 rps');
    expect(note?.textContent).toContain('cold start is the subject');
  });

  it('renders nothing for a config without mode provenance', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        if (String(input).includes('/api/executions/7/config')) {
          return json(advancedCfg);
        }
        return json({ message: 'no stub' }, 500);
      })
    );
    await render();
    expect(container!.querySelector('[data-testid="mode-config-card"]')).toBeNull();
  });

  it('hides the re-apply form from callers without execution:update', async () => {
    await render(false);
    expect(container!.querySelector('[data-testid="mode-reapply"]')).toBeNull();
    expect(container!.querySelector('[data-testid="mode-config-table"]')).not.toBeNull();
  });

  it('re-applies a fresh Simple statement, prefilled from the stored entry', async () => {
    const calls: Array<{ url: string; body: string }> = [];
    let puts = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (init?.method === 'PUT' && url.includes('/api/executions/7/config')) {
          puts++;
          calls.push({ url, body: String(init.body) });
          return json({});
        }
        if (url.includes('/api/executions/7/config')) {
          return json(modeCfg);
        }
        if (url.includes('/api/scenarios/5/capacity-profile')) {
          return json({ per_pod_qps: 125, saturated_by: 'engine' });
        }
        return json({ message: `no stub for ${url}` }, 500);
      })
    );
    await render();

    // Prefill came from the stored entry.
    const qps = container!.querySelector('[data-testid="mode-qps"]') as HTMLInputElement;
    expect(qps.value).toBe('500');
    expect((container!.querySelector('[data-testid="mode-duration"]') as HTMLInputElement).value).toBe('10');

    // Change the rate and re-apply.
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
    await act(async () => {
      setter.call(qps, '250');
      qps.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await act(async () => {
      root!.render(
        <MemoryRouter>
          <ModeConfigCard executionId={7} canUpdate={true} capacityKey={capacityKey} />
        </MemoryRouter>
      );
    });
    await act(async () => {
      container!
        .querySelector('[data-testid="mode-reapply"]')!
        .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await act(async () => {});

    expect(puts).toBe(1);
    const sent = JSON.parse(calls[0].body) as {
      project_id: number;
      execution_id: number;
      tests: Array<{
        mode?: string;
        throughput: number;
        duration: number;
        concurrency: number;
        engines: number;
        rampup: number;
        scenario_id: number;
      }>;
    };
    expect(sent.project_id).toBe(3);
    expect(sent.execution_id).toBe(7);
    expect(sent.tests[0].mode).toBe('burst');
    expect(sent.tests[0].throughput).toBe(250);
    expect(sent.tests[0].duration).toBe(600);
    expect(sent.tests[0].scenario_id).toBe(5);
    expect(sent.tests[0].concurrency).toBe(0);
    expect(sent.tests[0].engines).toBe(0);
  });

  it('surfaces a 409 refusal inline with the structured remediation', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (init?.method === 'PUT' && url.includes('/api/executions/7/config')) {
          return json(
            {
              message:
                'executionapp: mode config refused: capacity profile status "no_profile" for scenario 5 on jmeter (500m CPU / 512Mi memory)',
              details: {
                fanout_status: 'no_profile',
                hint: 'calibrate this scenario first (Execution page → Calibrate scenario), or configure it in Advanced mode',
              },
            },
            409
          );
        }
        if (url.includes('/api/executions/7/config')) {
          return json(modeCfg);
        }
        if (url.includes('/api/scenarios/5/capacity-profile')) {
          return json({ per_pod_qps: 125, saturated_by: 'engine' });
        }
        return json({ message: `no stub for ${url}` }, 500);
      })
    );
    await render();
    await act(async () => {
      container!
        .querySelector('[data-testid="mode-reapply"]')!
        .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await act(async () => {});

    const alert = container!.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain('no_profile');
    const details = container!.querySelector('[data-testid="action-error-details"]');
    expect(details?.textContent).toContain('calibrate this scenario first');
  });
});

// Phase 91: the Re-resolve action. The button confirms inline, POSTs
// /config/re-resolve, and the answer's per-entry old → new numbers render
// as the diff; a 409 surfaces the structured remediation; multi-entry
// configs render every mode row but keep the single-scenario re-apply
// form hidden (its PUT would drop the other entries).
describe('ModeConfigCard re-resolve (phase 91)', () => {
  const multiModeCfg = {
    'multi-test': {
      name: 'p91',
      project_id: 3,
      execution_id: 7,
      tests: [
        {
          name: 'checkout',
          scenario_id: 5,
          concurrency: 375,
          rampup: 0,
          engines: 4,
          throughput: 500,
          duration: 600,
          mode: 'burst',
        },
        {
          name: 'search',
          scenario_id: 6,
          concurrency: 30,
          rampup: 60,
          engines: 1,
          throughput: 50,
          duration: 3600,
          mode: 'soak',
        },
      ],
    },
  };

  const diffBody = {
    message: 'config re-resolved',
    entries: [
      {
        scenario_id: 5,
        name: 'checkout',
        mode: 'burst',
        changed: true,
        before: { engines: 4, concurrency: 375, rampup: 0, throughput: 500 },
        after: { engines: 5, concurrency: 750, rampup: 0, throughput: 500 },
      },
    ],
  };

  const refreshedCfg = {
    'multi-test': {
      ...modeCfg['multi-test'],
      tests: [{ ...modeCfg['multi-test'].tests[0], engines: 5, concurrency: 750 }],
    },
  };

  async function click(selector: string) {
    await act(async () => {
      container!.querySelector(selector)!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await act(async () => {});
  }

  it('confirms inline, POSTs the re-resolve, and renders the old → new diff', async () => {
    const posts: Array<{ url: string; method: string }> = [];
    let configServed = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? 'GET';
        if (method === 'POST' && url.includes('/api/executions/7/config/re-resolve')) {
          posts.push({ url, method });
          return json(diffBody);
        }
        if (url.includes('/api/executions/7/config')) {
          configServed++;
          return json(configServed > 2 ? refreshedCfg : modeCfg); // initial + post-action refetch
        }
        if (url.includes('/api/scenarios/5/capacity-profile')) {
          return json({ per_pod_qps: 125, saturated_by: 'engine' });
        }
        return json({ message: `no stub for ${url}` }, 500);
      })
    );
    await render();

    // Idle: the button, not the confirm bar.
    expect(container!.querySelector('[data-testid="mode-reresolve"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="mode-reresolve-confirm"]')).toBeNull();

    await click('[data-testid="mode-reresolve"]');
    expect(container!.querySelector('[data-testid="mode-reresolve-confirm"]')).not.toBeNull();
    // Cancel returns to idle without a POST.
    await click('[data-testid="mode-reresolve-cancel"]');
    expect(container!.querySelector('[data-testid="mode-reresolve-confirm"]')).toBeNull();

    await click('[data-testid="mode-reresolve"]');
    await click('[data-testid="mode-reresolve-confirm"]');

    expect(posts).toHaveLength(1);
    expect(posts[0].url).toContain('/api/executions/7/config/re-resolve');
    const diff = container!.querySelector('[data-testid="mode-reresolve-diff"]');
    expect(diff?.textContent).toContain('engines 4 → 5');
    expect(diff?.textContent).toContain('concurrency 375 → 750');
    expect(diff?.textContent).toContain('rampup 0 → 0');
    expect(diff?.textContent).toContain('throughput 500 → 500');
    // The table refreshed to the persisted numbers.
    expect((container!.querySelector('[data-testid="mode-config-table"]') as HTMLElement).textContent).toContain('750');
  });

  it('surfaces a 409 refusal inline with the structured remediation, nothing refreshed', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? 'GET';
        if (method === 'POST' && url.includes('/api/executions/7/config/re-resolve')) {
          return json(
            {
              message:
                'executionapp: mode config refused: capacity profile status "stale" for scenario 5 on jmeter (500m CPU / 512Mi memory)',
              details: {
                fanout_status: 'stale',
                scenario_id: 5,
                hint: 'the scenario changed since its calibration; recalibrate it (Execution page → Calibrate scenario), or configure it in Advanced mode',
              },
            },
            409
          );
        }
        if (url.includes('/api/executions/7/config')) {
          return json(modeCfg);
        }
        if (url.includes('/api/scenarios/5/capacity-profile')) {
          return json({ per_pod_qps: 125, saturated_by: 'engine' });
        }
        return json({ message: `no stub for ${url}` }, 500);
      })
    );
    await render();
    await click('[data-testid="mode-reresolve"]');
    await click('[data-testid="mode-reresolve-confirm"]');

    expect(container!.querySelector('[role="alert"]')?.textContent).toContain('stale');
    expect(container!.querySelector('[data-testid="action-error-details"]')?.textContent).toContain('recalibrate');
    expect(container!.querySelector('[data-testid="mode-reresolve-diff"]')).toBeNull();
  });

  it('renders every mode entry of a multi-scenario config and keeps the re-apply form away', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/api/executions/7/config')) {
          return json(multiModeCfg);
        }
        if (url.includes('/api/scenarios/5/capacity-profile') || url.includes('/api/scenarios/6/capacity-profile')) {
          return json({ per_pod_qps: 125, saturated_by: 'engine' });
        }
        return json({ message: `no stub for ${url}` }, 500);
      })
    );
    await render();

    expect(container!.querySelector('[data-testid="mode-config-row-5"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="mode-config-row-6"]')).not.toBeNull();
    expect((container!.querySelector('[data-testid="mode-config-table"]') as HTMLElement).textContent).toContain(
      'soak'
    );
    // The re-apply PUT replaces the whole profile: hidden for multi-entry
    // configs, with the reason stated. Re-resolve stays (it refreshes all).
    expect(container!.querySelector('[data-testid="mode-reapply"]')).toBeNull();
    expect(container!.querySelector('[data-testid="mode-reapply-unavailable"]')?.textContent).toContain(
      'Multi-scenario config'
    );
    expect(container!.querySelector('[data-testid="mode-reresolve"]')).not.toBeNull();
  });

  it('hides the re-resolve control from callers without execution:update', async () => {
    await render(false);
    expect(container!.querySelector('[data-testid="mode-reresolve"]')).toBeNull();
  });
});
