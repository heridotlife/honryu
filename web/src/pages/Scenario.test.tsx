import { describe, expect, it, vi, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import Scenario from './Scenario';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

// The instantiation landing page (phase 65): name from GET /api/scenarios/{id},
// the template badge only when the row is one, and the TaurusEditor wired to
// the same fragment endpoints the execution page's editor uses. Mounted with
// stubbed fetch (NewTest.test.tsx's createRoot + act pattern) -- the
// "Up to date" editor status doubles as proof the fragment loaded and the
// CodeMirror view mounted without throwing.
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

let container: HTMLDivElement | null = null;
let root: Root | null = null;

async function renderScenario(scenarioId: number, scenario: unknown, status = 200) {
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith(`/api/scenarios/${scenarioId}`)) {
        return json(scenario, status);
      }
      if (url.endsWith(`/api/scenarios/${scenarioId}/requests`)) {
        return new Response('default-address: https://httpbin.org\nrequests:\n', {
          status: 200,
          headers: { 'Content-Type': 'text/yaml' },
        });
      }
      return json({ message: `no stub for ${url}` }, 500);
    }),
  );
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <MemoryRouter initialEntries={[`/scenarios/${scenarioId}`]}>
        <Routes>
          <Route path="/scenarios/:id" element={<Scenario />} />
        </Routes>
      </MemoryRouter>,
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
});

describe('Scenario page (phase 65)', () => {
  it('shows the scenario name, the template badge, and a loaded editor', async () => {
    await renderScenario(42, {
      id: 42,
      name: 'from-baseline',
      project_id: 1,
      created_time: '2026-09-17T00:00:00Z',
      is_template: true,
      template_name: 'httpbin-baseline',
    });

    expect(container!.querySelector('[data-testid="scenario-name"]')?.textContent).toBe('from-baseline');
    const badge = container!.querySelector('[data-testid="template-badge"]');
    expect(badge?.textContent).toContain('httpbin-baseline');
    // The editor finished loading its fragment (no error, nothing dirty).
    expect(container!.textContent).toContain('Up to date');
    expect(container!.querySelector('.cm-editor')).not.toBeNull();
  });

  it('badges nothing for an ordinary scenario', async () => {
    await renderScenario(43, {
      id: 43,
      name: 'plain',
      project_id: 1,
      created_time: '2026-09-17T00:00:00Z',
      is_template: false,
    });

    expect(container!.querySelector('[data-testid="scenario-name"]')?.textContent).toBe('plain');
    expect(container!.querySelector('[data-testid="template-badge"]')).toBeNull();
    expect(container!.textContent).toContain('Up to date');
  });

  it('surfaces the error when the scenario does not exist', async () => {
    await renderScenario(44, { message: 'ports: not found' }, 404);
    expect(container!.querySelector('[role="alert"]')?.textContent).toContain('ports: not found');
    // And no editor was wired up for a scenario that never loaded.
    expect(container!.querySelector('.cm-editor')).toBeNull();
  });
});
