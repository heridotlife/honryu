import { describe, expect, it, vi, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import newTestSource from './NewTest.tsx?raw';
import NewTest from './NewTest';
import { SessionProvider } from '../hooks/useSession';
import { buildConfig, type NewTestForm } from '../lib/newTestFlow';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

// Wiring test, App.test.ts's ?raw pattern: mounting the whole create flow
// drags five API calls with it, and the gate itself is one line. What this
// pins is that the create control CANNOT render for a caller the server
// would 403 -- AC14's "no Deploy control on any page" includes this one.
describe('NewTest gating (phase 20)', () => {
  it('gates the create control on the session permission map', () => {
    expect(newTestSource).toContain("can('execution', 'create')");
    // The honest alternative rendered instead of the button.
    expect(newTestSource).toContain('no-create-permission');
  });
});

// The mounted flow (Execution.live.test.tsx's createRoot + act pattern):
// every fetch is stubbed per URL, the PUT to /executions/{id}/config is
// captured, and the payload is compared against buildConfig's output --
// the R9 wire contract the stage editor must keep byte-identical.
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

let puts: Array<{ url: string; body: string }> = [];
/** Phase 88: the POST /api/executions form bodies -- where the
 * fanout_targets field rides. */
let execPosts: string[] = [];
let container: HTMLDivElement | null = null;
let root: Root | null = null;

/** The flow's default API stub (project-if-absent -> scenario ->
 * execution -> fragment -> config), capturing the config PUT. */
function stubFlowApi() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? 'GET';
      if (url.endsWith('/api/me')) {
        return json({
          subject: 'demo:a',
          name: 'a',
          email: '',
          global_roles: [],
          tenants: {},
          permissions: { '*': ['*'] },
          demo: true,
        });
      }
      if (method === 'GET' && url.endsWith('/api/projects')) {
        return json([]);
      }
      if (method === 'POST' && url.endsWith('/api/projects')) {
        return json({ id: 1, name: 'tests-checkout-smoke' });
      }
      if (method === 'POST' && url.endsWith('/api/scenarios')) {
        return json({ id: 42 });
      }
      if (method === 'POST' && url.endsWith('/api/executions')) {
        return json({ id: 9 });
      }
      if (method === 'PUT' && url.endsWith('/api/scenarios/42/requests')) {
        return json({});
      }
      if (method === 'PUT' && url.endsWith('/api/executions/9/config')) {
        puts.push({ url, body: String(init?.body) });
        return json({});
      }
      return json({ message: `no stub for ${method} ${url}` }, 500);
    })
  );
}

async function renderNewTest() {
  container = document.createElement('div');
  document.body.appendChild(container);
  stubFlowApi();
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter initialEntries={['/executions/new']}>
        <SessionProvider>
          <Routes>
            <Route path="/executions/new" element={<NewTest />} />
          </Routes>
        </SessionProvider>
      </MemoryRouter>
    );
  });
  await act(async () => {});
}

afterEach(() => {
  vi.unstubAllGlobals();
  const r = root;
  if (r !== null && container !== null) {
    act(() => {
      r.unmount();
    });
  }
  container?.remove();
  container = null;
  root = null;
  puts = [];
  execPosts = [];
});

/** Native value setter + input event (React's tracker ignores plain writes). */
async function type(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  const setter = Object.getOwnPropertyDescriptor(proto, 'value')!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
}

async function click(el: Element) {
  await act(async () => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
}

/** The page's own defaults (NewTest's initial form), with the typed identity fields. */
function submittedForm(name: string, targetUrl: string): NewTestForm {
  return {
    name,
    targetUrl,
    method: 'GET',
    headers: [],
    concurrency: 50,
    engines: 2,
    rampup: 30,
    duration: 300,
    engine: 'jmeter',
  };
}

/** Fills name + target URL and clicks Create, flushing the five-call flow. */
async function fillAndSubmit() {
  await type(container!.querySelector('input[placeholder="checkout-smoke"]') as HTMLInputElement, 'checkout-smoke');
  await type(
    container!.querySelector('input[placeholder="http://checkout.svc"]') as HTMLInputElement,
    'http://checkout.svc'
  );
  await click(container!.querySelector('[data-testid="create-test"]')!);
}

/** Phase 90: flip the Load card to Advanced (the pre-phase-90 surface the
 * byte-compat pins exercise; Simple is the new default). */
async function switchToAdvanced() {
  await click(container!.querySelector('[data-testid="load-tab-advanced"]')!);
}

describe('NewTest step 5 via StageEditor (mounted flow)', () => {
  it('defaults to the Simple mode form; Advanced still seeds the stage editor from the form defaults', async () => {
    await renderNewTest();
    // Phase 90: Simple is the default Load surface.
    expect(container!.querySelector('[data-testid="mode-form"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="stage-editor"]')).toBeNull();
    await switchToAdvanced();
    expect(container!.querySelector('[data-testid="stage-editor"]')).not.toBeNull();
    const concurrency = container!.querySelector('[aria-label="stage 1 concurrency"]') as HTMLInputElement;
    expect(concurrency.value).toBe('50');
    expect((container!.querySelector('[aria-label="stage 1 duration"]') as HTMLInputElement).value).toBe('300');
  });

  it('reaches PUT /executions/{id}/config with a payload byte-equal to buildConfig', async () => {
    await renderNewTest();
    await switchToAdvanced();
    await fillAndSubmit();
    await act(async () => {});

    expect(puts).toHaveLength(1);
    expect(puts[0].url).toContain('/api/executions/9/config');
    const expected = buildConfig(submittedForm('checkout-smoke', 'http://checkout.svc'), 42);
    expected.project_id = 1;
    expected.execution_id = 9;
    // Byte equality: key order and omitted keys (no throughput, no
    // csv_split) are the contract, not just the parsed shape.
    expect(puts[0].body).toBe(JSON.stringify(expected));
  });

  it('submits every edited stage row, each bound to the created scenario', async () => {
    await renderNewTest();
    await switchToAdvanced();
    await type(container!.querySelector('input[placeholder="checkout-smoke"]') as HTMLInputElement, 'checkout-smoke');
    await type(
      container!.querySelector('input[placeholder="http://checkout.svc"]') as HTMLInputElement,
      'http://checkout.svc'
    );
    await click(container!.querySelector('[aria-label="add stage"]')!);
    await type(container!.querySelector('[aria-label="stage 2 concurrency"]') as HTMLInputElement, '25');
    await type(container!.querySelector('[aria-label="stage 2 throughput"]') as HTMLInputElement, '120');
    await click(container!.querySelector('[data-testid="create-test"]')!);
    await act(async () => {});

    const sent = JSON.parse(puts[0].body) as {
      tests: Array<{ name: string; scenario_id: number; concurrency: number; throughput?: number }>;
    };
    expect(sent.tests).toHaveLength(2);
    expect(sent.tests[0]).toEqual({
      name: 'checkout-smoke',
      scenario_id: 42,
      concurrency: 50,
      rampup: 30,
      engines: 2,
      duration: 300,
    });
    expect(sent.tests[1].concurrency).toBe(25);
    expect(sent.tests[1].throughput).toBe(120);
    expect(sent.tests[1].scenario_id).toBe(42);
  });
});

// Phase 24: a step failing with the structured details envelope surfaces
// the hint/numbers under the message line; a message-only failure renders
// exactly as before. renderNewTest's stub is swapped before submit because
// the flow fetches at click time. Since phase 90 the config step (step 5)
// carries its failure to the execution page instead (see the Simple-mode
// tests below), so these pins fail an earlier step.
describe('NewTest action-error details (mounted)', () => {
  it('surfaces the hint code line and numbers when the failing step carried details', async () => {
    await renderNewTest();
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? 'GET';
        if (method === 'GET' && url.endsWith('/api/projects')) {
          return json([]);
        }
        if (method === 'POST' && url.endsWith('/api/projects')) {
          return json({ id: 1, name: 'tests-checkout-smoke' });
        }
        if (method === 'POST' && url.endsWith('/api/scenarios')) {
          return json({ id: 42 });
        }
        if (method === 'POST' && url.endsWith('/api/executions')) {
          return json({ id: 9 });
        }
        if (method === 'PUT' && url.endsWith('/api/scenarios/42/requests')) {
          return json(
            {
              message: 'reservation would exceed tenant quota',
              details: { requested: 2, used: 0, ceiling: 1, hint: 'PUT /api/tenants/{tenant_id}/quota ceiling=1' },
            },
            429
          );
        }
        if (method === 'PUT' && url.endsWith('/api/executions/9/config')) {
          puts.push({ url, body: String(init?.body) });
          return json({});
        }
        return json({ message: `no stub for ${method} ${url}` }, 500);
      })
    );

    await fillAndSubmit();
    await act(async () => {});

    expect(container!.querySelector('[role="alert"]')?.textContent).toContain('reservation would exceed tenant quota');
    const details = container!.querySelector('[data-testid="action-error-details"]');
    expect(details?.querySelector('code')?.textContent).toBe('PUT /api/tenants/{tenant_id}/quota ceiling=1');
    expect(details?.textContent).toContain('used 0 / ceiling 1 — requested 2');
    // The flow stopped at the failing step: no config PUT, no navigation.
    expect(puts).toHaveLength(0);
  });

  it('message-only failures keep the single alert line (no details node)', async () => {
    await renderNewTest();
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? 'GET';
        if (method === 'GET' && url.endsWith('/api/projects')) {
          return json([]);
        }
        if (method === 'POST' && url.endsWith('/api/projects')) {
          return json({ id: 1, name: 'tests-checkout-smoke' });
        }
        if (method === 'POST' && url.endsWith('/api/scenarios')) {
          return json({ message: 'scenario name already in use' }, 409);
        }
        return json({ message: `no stub for ${method} ${url}` }, 500);
      })
    );

    await fillAndSubmit();
    await act(async () => {});

    expect(container!.querySelector('[role="alert"]')?.textContent).toContain('scenario name already in use');
    expect(container!.querySelector('[data-testid="action-error-details"]')).toBeNull();
  });
});

// Phase 90: the Simple Load tab. Default on; its submit PUTs the
// mode-shaped statement (mode + rate + duration only) and -- when the
// config save is refused (the 409 "calibrate first" above all) -- still
// navigates to the created execution's page, carrying the error as
// router state for the banner there. The probe route observes both.
describe('NewTest Simple mode (phase 90)', () => {
  function LocationProbe() {
    const location = useLocation();
    return (
      <div data-testid="probe" data-path={location.pathname} data-state={JSON.stringify(location.state ?? null)} />
    );
  }

  async function renderWithProbe() {
    container = document.createElement('div');
    document.body.appendChild(container);
    stubFlowApi();
    root = createRoot(container);
    await act(async () => {
      root!.render(
        <MemoryRouter initialEntries={['/executions/new']}>
          <SessionProvider>
            <Routes>
              <Route path="/executions/new" element={<NewTest />} />
              <Route path="/executions/:id" element={<LocationProbe />} />
            </Routes>
          </SessionProvider>
        </MemoryRouter>
      );
    });
    await act(async () => {});
  }

  it('submits the mode-shaped config from the Simple tab', async () => {
    await renderWithProbe();
    await type(container!.querySelector('input[placeholder="checkout-smoke"]') as HTMLInputElement, 'checkout-smoke');
    await type(
      container!.querySelector('input[placeholder="http://checkout.svc"]') as HTMLInputElement,
      'http://checkout.svc'
    );
    // Simple defaults: burst, 100 rps, 10 minutes.
    await click(container!.querySelector('[data-testid="create-test"]')!);
    await act(async () => {});

    expect(puts).toHaveLength(1);
    const sent = JSON.parse(puts[0].body) as {
      tests: Array<{
        mode?: string;
        throughput: number;
        duration: number;
        concurrency: number;
        engines: number;
        rampup: number;
      }>;
    };
    expect(sent.tests).toHaveLength(1);
    expect(sent.tests[0].mode).toBe('burst');
    expect(sent.tests[0].throughput).toBe(100);
    expect(sent.tests[0].duration).toBe(600);
    // The resolved fields ride as zeros: the server owns them.
    expect(sent.tests[0].concurrency).toBe(0);
    expect(sent.tests[0].engines).toBe(0);
    // Landed on the execution page, no error banner state.
    const probe = container!.querySelector('[data-testid="probe"]');
    expect(probe?.getAttribute('data-path')).toBe('/executions/9');
    expect(probe?.getAttribute('data-state')).toBe('null');
  });

  it('navigates anyway when the config save is refused, carrying the error for the banner', async () => {
    await renderWithProbe();
    // Swap the config PUT stub to a 409 with the phase-90 envelope.
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? 'GET';
        if (url.endsWith('/api/me')) {
          return json({
            subject: 'demo:a',
            name: 'a',
            email: '',
            global_roles: [],
            tenants: {},
            permissions: { '*': ['*'] },
            demo: true,
          });
        }
        if (method === 'GET' && url.endsWith('/api/projects')) {
          return json([]);
        }
        if (method === 'POST' && url.endsWith('/api/projects')) {
          return json({ id: 1, name: 'tests-checkout-smoke' });
        }
        if (method === 'POST' && url.endsWith('/api/scenarios')) {
          return json({ id: 42 });
        }
        if (method === 'POST' && url.endsWith('/api/executions')) {
          return json({ id: 9 });
        }
        if (method === 'PUT' && url.endsWith('/api/scenarios/42/requests')) {
          return json({});
        }
        if (method === 'PUT' && url.endsWith('/api/executions/9/config')) {
          puts.push({ url, body: String(init?.body) });
          return json(
            {
              message:
                'executionapp: mode config refused: capacity profile status "no_profile" for scenario 42 on jmeter (500m CPU / 512Mi memory)',
              details: {
                fanout_status: 'no_profile',
                scenario_id: 42,
                engine: 'jmeter',
                cpu: '500m',
                memory: '512Mi',
                hint: 'calibrate this scenario first (Execution page → Calibrate scenario), or configure it in Advanced mode',
              },
            },
            409
          );
        }
        return json({ message: `no stub for ${method} ${url}` }, 500);
      })
    );

    await type(container!.querySelector('input[placeholder="checkout-smoke"]') as HTMLInputElement, 'checkout-smoke');
    await type(
      container!.querySelector('input[placeholder="http://checkout.svc"]') as HTMLInputElement,
      'http://checkout.svc'
    );
    await click(container!.querySelector('[data-testid="create-test"]')!);
    await act(async () => {});

    // The PUT was attempted and refused...
    expect(puts).toHaveLength(1);
    // ...and the flow still landed on the execution page, with the error
    // and its structured details in router state for the banner.
    const probe = container!.querySelector('[data-testid="probe"]');
    expect(probe?.getAttribute('data-path')).toBe('/executions/9');
    const state = JSON.parse(probe?.getAttribute('data-state') ?? 'null') as {
      configError?: string;
      configErrorDetail?: Record<string, unknown>;
    };
    expect(state.configError).toContain('no_profile');
    expect(state.configErrorDetail?.fanout_status).toBe('no_profile');
    expect(state.configErrorDetail?.hint).toContain('calibrate');
  });
});

// Phase 65: the "From template" flow. Catalog from GET /api/templates drives
// the picker; the create button fires the project-if-absent resolve and one
// instantiate POST, then navigates to the created scenario's page. The
// from-scratch flow above is untouched by all of it -- its stubs 500 on
// /api/templates and the card degrades to a note.
const templateRow = {
  id: 7,
  name: 'HTTPbin baseline',
  project_id: 0,
  is_template: true,
  template_name: 'httpbin-baseline',
};

describe('NewTest from template (mounted flow)', () => {
  it('lists the catalog in the picker, instantiates with the override, and lands on the scenario page', async () => {
    const instantiates: Array<{ url: string; body: unknown }> = [];
    let landed: string | null = null;
    function LocationProbe() {
      const location = useLocation();
      // pathname + search: the instantiate landing carries ?tab=editor
      // (phase 67b -- the detail page's default tab is the run history).
      landed = location.pathname + location.search;
      return null;
    }

    container = document.createElement('div');
    document.body.appendChild(container);
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? 'GET';
        if (url.endsWith('/api/me')) {
          return json({
            subject: 'demo:a',
            name: 'a',
            email: '',
            global_roles: [],
            tenants: {},
            permissions: { '*': ['*'] },
            demo: true,
          });
        }
        if (method === 'GET' && url.endsWith('/api/templates')) {
          return json([templateRow]);
        }
        if (method === 'GET' && url.endsWith('/api/projects')) {
          return json([]);
        }
        if (method === 'POST' && url.endsWith('/api/projects')) {
          return json({ id: 1, name: 'tests-from-baseline' });
        }
        if (method === 'POST' && url.endsWith('/api/scenarios/7/instantiate')) {
          instantiates.push({ url, body: JSON.parse(String(init?.body)) });
          return json({ id: 42, name: 'from-baseline', project_id: 1, is_template: false }, 201);
        }
        return json({ message: `no stub for ${method} ${url}` }, 500);
      })
    );
    root = createRoot(container);
    await act(async () => {
      root!.render(
        <MemoryRouter initialEntries={['/executions/new']}>
          <SessionProvider>
            <Routes>
              <Route path="/executions/new" element={<NewTest />} />
              <Route path="/scenarios/:id" element={<LocationProbe />} />
            </Routes>
          </SessionProvider>
        </MemoryRouter>
      );
    });
    await act(async () => {});

    // The catalog drives the picker (server-driven state).
    const picker = container!.querySelector('[aria-label="template picker"]') as HTMLSelectElement;
    expect(picker).not.toBeNull();
    expect(picker.options).toHaveLength(2); // placeholder + the one template
    expect((picker.options[1] as HTMLOptionElement).textContent).toBe('HTTPbin baseline');

    // Create stays disabled until a template is picked.
    const createBtn = () => container!.querySelector('[data-testid="create-from-template"]') as HTMLButtonElement;
    expect(createBtn().disabled).toBe(true);

    // Pick, name, override, create.
    const selectSetter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')!.set!;
    await act(async () => {
      selectSetter.call(picker, '7');
      picker.dispatchEvent(new Event('change', { bubbles: true }));
    });
    expect(createBtn().disabled).toBe(false);
    await type(container!.querySelector('[aria-label="template test name"]') as HTMLInputElement, 'from-baseline');
    await type(
      container!.querySelector('[aria-label="template target URL override"]') as HTMLInputElement,
      'http://checkout.svc'
    );
    await click(createBtn());
    await act(async () => {});

    // One instantiate call, carrying the name, resolved project, override.
    expect(instantiates).toHaveLength(1);
    expect(instantiates[0].url).toContain('/api/scenarios/7/instantiate');
    expect(instantiates[0].body).toEqual({
      name: 'from-baseline',
      project_id: 1,
      overrides: { target_url: 'http://checkout.svc' },
    });
    // The instantiate POST is the scenario-creation step -- no POST
    // /api/scenarios happened (no client-side cloning).
    const calls = (fetch as ReturnType<typeof vi.fn>).mock.calls as Array<[RequestInfo | URL]>;
    expect(calls.filter(([u]) => String(u).endsWith('/api/scenarios'))).toHaveLength(0);
    // Landed on the created scenario's page, editor tab up (phase 67b:
    // the detail page defaults to the run history; instantiation ends in
    // the editor where the flow always ended).
    expect(landed).toBe('/scenarios/42?tab=editor');
  });

  it('keeps the page usable when the catalog is unavailable', async () => {
    await renderNewTest(); // its stub 500s GET /api/templates
    await act(async () => {});
    expect(container!.textContent).toContain('Template catalog unavailable.');
    // The from-scratch flow's own controls are untouched.
    expect(container!.querySelector('[data-testid="create-test"]')).not.toBeNull();
    expect(container!.querySelector('[aria-label="template picker"]')).toBeNull();
  });
});

// Phase 77: the identity fields validate on blur (earlier feedback than
// the submit guard, which stays as the backstop), and a failed submit
// moves focus to a summary whose entries link to their fields.
describe('NewTest blur validation + error summary (phase 77)', () => {
  it('shows a field error when a required field is left empty and blurred; the untouched field stays quiet', async () => {
    await renderNewTest();
    const name = container!.querySelector('#newtest-name') as HTMLInputElement;
    await type(name, 'x');
    await type(name, ''); // leave it empty, then blur it
    await act(async () => {
      name.dispatchEvent(new FocusEvent('focusout', { bubbles: true }));
    });
    expect(container!.querySelector('[data-testid="newtest-name-error"]')?.textContent).toContain(
      'Test name is required.'
    );
    expect(name.getAttribute('aria-invalid')).toBe('true');
    // The target URL was never touched: no error next to it yet.
    expect(container!.querySelector('[data-testid="newtest-target-url-error"]')).toBeNull();
  });

  it('on a failed submit focus moves to the summary, whose links focus their fields', async () => {
    await renderNewTest();
    await click(container!.querySelector('[data-testid="create-test"]')!);
    const summary = container!.querySelector('[data-testid="newtest-error-summary"]');
    expect(summary).not.toBeNull();
    expect(summary!.getAttribute('role')).toBe('alert');
    // Focus moved to the summary, not left wherever the operator was.
    expect(document.activeElement).toBe(summary);
    // One entry per missing field, each a link to that field.
    const links = Array.from(summary!.querySelectorAll('a'));
    expect(links.map(l => l.getAttribute('href'))).toEqual(['#newtest-name', '#newtest-target-url']);
    expect(links[0].textContent).toContain('Test name is required.');
    await act(async () => {
      links[0].dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    });
    expect(document.activeElement).toBe(container!.querySelector('#newtest-name'));
    // The failed submit never started the flow: no calls, no steps.
    expect(puts).toHaveLength(0);
    expect(container!.textContent).not.toContain('step:');
  });
});

// Phase 88: the fan-out target picker -- one checkbox per registered BYOC
// cluster; any checked makes the created execution fan-out (the full shard
// set runs on every checked cluster). The honest degraded states matter as
// much as the happy one: no BYOC cluster registered, or the registry read
// failing, disables the picker with the reason stated, never a broken form.
describe('NewTest fan-out targets (phase 88, mounted)', () => {
  /** renderNewTest's stub plus /api/clusters and a capture of the execution
   * POST body -- the one request the fanout_targets field rides. */
  async function renderNewTestWithClusters(clusters: unknown[] | null) {
    container = document.createElement('div');
    document.body.appendChild(container);
    execPosts = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? 'GET';
        if (url.endsWith('/api/me')) {
          return json({
            subject: 'demo:a',
            name: 'a',
            email: '',
            global_roles: [],
            tenants: {},
            permissions: { '*': ['*'] },
            demo: true,
          });
        }
        if (url.endsWith('/api/clusters')) {
          return clusters === null ? json({ message: 'registry down' }, 500) : json(clusters);
        }
        if (method === 'GET' && url.endsWith('/api/projects')) {
          return json([]);
        }
        if (method === 'POST' && url.endsWith('/api/projects')) {
          return json({ id: 1, name: 'tests-checkout-smoke' });
        }
        if (method === 'POST' && url.endsWith('/api/scenarios')) {
          return json({ id: 42 });
        }
        if (method === 'POST' && url.endsWith('/api/executions')) {
          execPosts.push(String(init?.body));
          return json({ id: 9 });
        }
        if (method === 'PUT' && url.endsWith('/api/scenarios/42/requests')) {
          return json({});
        }
        if (method === 'PUT' && url.endsWith('/api/executions/9/config')) {
          puts.push({ url, body: String(init?.body) });
          return json({});
        }
        return json({ message: `no stub for ${method} ${url}` }, 500);
      })
    );
    root = createRoot(container);
    await act(async () => {
      root!.render(
        <MemoryRouter initialEntries={['/executions/new']}>
          <SessionProvider>
            <Routes>
              <Route path="/executions/new" element={<NewTest />} />
            </Routes>
          </SessionProvider>
        </MemoryRouter>
      );
    });
    await act(async () => {});
  }

  const operatorCluster = {
    name: 'honryu',
    origin: 'operator',
    namespace: 'honryu',
    created_time: '2026-09-05T10:29:02Z',
  };
  const byoc = (name: string) => ({ ...operatorCluster, name, origin: 'byoc' });

  it('offers only BYOC clusters as targets, and sends fanout_targets only when one is checked', async () => {
    await renderNewTestWithClusters([operatorCluster, byoc('eu-1'), byoc('us-1')]);
    const fieldset = container!.querySelector('[data-testid="fanout-targets"]')!;
    expect(fieldset).not.toBeNull();
    // The operator-managed default is not a fan-out target.
    expect(container!.querySelector('[data-testid="fanout-target-honryu"]')).toBeNull();
    expect(container!.querySelector('[data-testid="fanout-target-eu-1"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="fanout-target-us-1"]')).not.toBeNull();

    // Unchecked: the request every pre-fan-out client sends stays identical.
    await type(container!.querySelector('input[placeholder="checkout-smoke"]') as HTMLInputElement, 'checkout-smoke');
    await type(
      container!.querySelector('input[placeholder="http://checkout.svc"]') as HTMLInputElement,
      'http://checkout.svc'
    );
    await click(container!.querySelector('[data-testid="create-test"]')!);
    await act(async () => {});
    expect(execPosts).toHaveLength(1);
    expect(execPosts[0]).not.toContain('fanout_targets');
  });

  it('sends the checked targets as a JSON array on the execution form', async () => {
    await renderNewTestWithClusters([byoc('eu-1'), byoc('us-1')]);
    await click(container!.querySelector('[data-testid="fanout-target-us-1"]')!);
    await click(container!.querySelector('[data-testid="fanout-target-eu-1"]')!);
    expect(container!.querySelector('[data-testid="fanout-targets-selected"]')!.textContent).toContain('2 targets');
    await type(container!.querySelector('input[placeholder="checkout-smoke"]') as HTMLInputElement, 'checkout-smoke');
    await type(
      container!.querySelector('input[placeholder="http://checkout.svc"]') as HTMLInputElement,
      'http://checkout.svc'
    );
    await click(container!.querySelector('[data-testid="create-test"]')!);
    await act(async () => {});

    expect(execPosts).toHaveLength(1);
    const sent = new URLSearchParams(execPosts[0]);
    // Sorted: the array is deterministic whatever the check order was.
    expect(sent.get('fanout_targets')).toBe('["eu-1","us-1"]');
  });

  it('states the reason when no BYOC cluster is registered', async () => {
    await renderNewTestWithClusters([operatorCluster]);
    expect(container!.querySelector('[data-testid="fanout-targets-empty"]')?.textContent).toContain(
      'No BYOC clusters registered'
    );
    expect(container!.querySelector('[data-testid="fanout-target-eu-1"]')).toBeNull();
  });

  it('degrades to the default-cluster note when the registry read fails, never a broken form', async () => {
    await renderNewTestWithClusters(null);
    const fieldset = container!.querySelector('[data-testid="fanout-targets"]')!;
    expect(fieldset.textContent).toContain('Cluster registry unavailable');
  });
});

// Phase 91: multi-scenario Simple submits. The Load card's rows each
// create their own scenario (shared request shape), and the config PUT
// carries one mode-shaped entry per row -- no cross-row derivation
// sharing. Blank names derive checkout-smoke / checkout-smoke-2 at
// submit; the single-row pins above are the provenance round-trip.
describe('NewTest Simple multi-scenario (phase 91)', () => {
  function LocationProbe() {
    const location = useLocation();
    return (
      <div data-testid="probe" data-path={location.pathname} data-state={JSON.stringify(location.state ?? null)} />
    );
  }

  /** Mounts the page; pass false when the test installed its own fetch
   * stub first (stubFlowApi would replace it). */
  async function renderWithProbe(useDefaultStub = true) {
    container = document.createElement('div');
    document.body.appendChild(container);
    if (useDefaultStub) {
      stubFlowApi();
    }
    root = createRoot(container);
    await act(async () => {
      root!.render(
        <MemoryRouter initialEntries={['/executions/new']}>
          <SessionProvider>
            <Routes>
              <Route path="/executions/new" element={<NewTest />} />
              <Route path="/executions/:id" element={<LocationProbe />} />
            </Routes>
          </SessionProvider>
        </MemoryRouter>
      );
    });
    await act(async () => {});
  }

  it('creates one scenario per row and PUTs one mode entry per scenario', async () => {
    const scenarioPosts: string[] = [];
    const fragmentPuts: string[] = [];
    let nextScenarioID = 41;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = init?.method ?? 'GET';
        if (url.endsWith('/api/me')) {
          return json({
            subject: 'demo:a',
            name: 'a',
            email: '',
            global_roles: [],
            tenants: {},
            permissions: { '*': ['*'] },
            demo: true,
          });
        }
        if (method === 'GET' && url.endsWith('/api/projects')) {
          return json([]);
        }
        if (method === 'POST' && url.endsWith('/api/projects')) {
          return json({ id: 1, name: 'tests-checkout-smoke' });
        }
        if (method === 'POST' && url.endsWith('/api/scenarios')) {
          scenarioPosts.push(String(init?.body));
          nextScenarioID++;
          return json({ id: nextScenarioID });
        }
        if (method === 'POST' && url.endsWith('/api/executions')) {
          return json({ id: 9 });
        }
        if (method === 'PUT' && /\/api\/scenarios\/\d+\/requests$/.test(url)) {
          fragmentPuts.push(url);
          return json({});
        }
        if (method === 'PUT' && url.endsWith('/api/executions/9/config')) {
          puts.push({ url, body: String(init?.body) });
          return json({});
        }
        return json({ message: `no stub for ${method} ${url}` }, 500);
      })
    );
    await renderWithProbe(false);

    await type(container!.querySelector('input[placeholder="checkout-smoke"]') as HTMLInputElement, 'checkout-smoke');
    await type(
      container!.querySelector('input[placeholder="http://checkout.svc"]') as HTMLInputElement,
      'http://checkout.svc'
    );
    // A second row: soak at 60 rps for an hour. Row 1 keeps its defaults.
    await click(container!.querySelector('[data-testid="mode-add-row"]')!);
    await type(container!.querySelector('[data-testid="mode-row-1-qps"]') as HTMLInputElement, '60');
    const modeSelect = container!.querySelector('[data-testid="mode-row-1-mode"]') as HTMLSelectElement;
    const selectSetter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')!.set!;
    await act(async () => {
      selectSetter.call(modeSelect, 'soak');
      modeSelect.dispatchEvent(new Event('change', { bubbles: true }));
    });
    await type(container!.querySelector('[data-testid="mode-row-1-duration"]') as HTMLInputElement, '1');
    const unitSelect = container!.querySelector('[data-testid="mode-row-1-duration-unit"]') as HTMLSelectElement;
    await act(async () => {
      selectSetter.call(unitSelect, 'h');
      unitSelect.dispatchEvent(new Event('change', { bubbles: true }));
    });
    await click(container!.querySelector('[data-testid="create-test"]')!);
    await act(async () => {});

    // One scenario per row, named by the submit-time derivation.
    expect(scenarioPosts).toHaveLength(2);
    expect(new URLSearchParams(scenarioPosts[0]).get('name')).toBe('checkout-smoke');
    expect(new URLSearchParams(scenarioPosts[1]).get('name')).toBe('checkout-smoke-2');
    // Each scenario got the shared request fragment.
    expect(fragmentPuts).toEqual(['/api/scenarios/42/requests', '/api/scenarios/43/requests']);
    // One mode entry per scenario, each stating only its own row.
    expect(puts).toHaveLength(1);
    const sent = JSON.parse(puts[0].body) as {
      tests: Array<{
        name: string;
        scenario_id: number;
        mode: string;
        throughput: number;
        duration: number;
        concurrency: number;
        engines: number;
      }>;
    };
    expect(sent.tests).toHaveLength(2);
    expect(sent.tests[0]).toEqual({
      name: 'checkout-smoke',
      scenario_id: 42,
      mode: 'burst',
      throughput: 100,
      duration: 600,
      concurrency: 0,
      rampup: 0,
      engines: 0,
    });
    expect(sent.tests[1].mode).toBe('soak');
    expect(sent.tests[1].throughput).toBe(60);
    expect(sent.tests[1].duration).toBe(3600);
    expect(sent.tests[1].scenario_id).toBe(43);
    // Landed on the execution page like every Simple submit.
    expect(container!.querySelector('[data-testid="probe"]')?.getAttribute('data-path')).toBe('/executions/9');
  });

  it('keeps Create disabled while any row is invalid', async () => {
    await renderWithProbe();
    await type(container!.querySelector('input[placeholder="checkout-smoke"]') as HTMLInputElement, 'checkout-smoke');
    await type(
      container!.querySelector('input[placeholder="http://checkout.svc"]') as HTMLInputElement,
      'http://checkout.svc'
    );
    await click(container!.querySelector('[data-testid="mode-add-row"]')!);
    await type(container!.querySelector('[data-testid="mode-row-1-qps"]') as HTMLInputElement, '0');
    expect((container!.querySelector('[data-testid="create-test"]') as HTMLButtonElement).disabled).toBe(true);
    await type(container!.querySelector('[data-testid="mode-row-1-qps"]') as HTMLInputElement, '30');
    expect((container!.querySelector('[data-testid="create-test"]') as HTMLButtonElement).disabled).toBe(false);
  });
});
