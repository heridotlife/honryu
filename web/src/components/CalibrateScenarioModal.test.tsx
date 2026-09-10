// The create-calibration dialog's mounted interaction (phase 39), the
// ShareRunModal/ExecutionConfigCard tests' style: createRoot + act with
// fetch stubbed. Under test: the form's defaults, the exact POST body
// createCalibration sends (project_id, auto-generated name, engine,
// criterion, cpu, memory, the phase 41 scenario binding -- scenario_id +
// source_execution_id -- and the optional bounds ONLY when filled in),
// the required-target-QPS guard, and the onCreated handoff the parent
// turns into navigation to the fresh execution's page.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import CalibrateScenarioModal from './CalibrateScenarioModal';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

let container: HTMLDivElement | null = null;
let root: Root | null = null;
/** Every (url, body) the stubbed fetch saw -- one entry per POST. */
let posts: Array<{ url: string; body: string }> = [];
const created = vi.fn();
const closed = vi.fn();

async function renderModal(): Promise<void> {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root!.render(
      <CalibrateScenarioModal
        scenarioId={7}
        scenarioName="smoke"
        projectId={3}
        sourceExecutionId={12}
        engine="jmeter"
        onClose={closed}
        onCreated={created}
      />
    );
  });
}

const input = (testid: string): HTMLInputElement =>
  container!.querySelector(`[data-testid="${testid}"]`) as HTMLInputElement;

const fill = async (testid: string, value: string): Promise<void> => {
  const el = input(testid);
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
};

const clickSubmit = async (): Promise<void> => {
  const btn = container!.querySelector('[data-testid="calibrate-submit"]') as HTMLButtonElement;
  await act(async () => {
    btn.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
};

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
  posts = [];
  created.mockClear();
  closed.mockClear();
});

/** Stubs POST /api/calibrations with `status` and the given response body. */
const stubCreate = (status = 201, body: unknown = { execution_id: 42 }) => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      posts.push({ url: String(input), body: String(init?.body ?? '') });
      return json(body, status);
    })
  );
};

describe('CalibrateScenarioModal', () => {
  it("renders the form with the spec's defaults (cpu 500m, memory 512Mi, criterion)", async () => {
    await renderModal();
    expect(container!.querySelector('[data-testid="calibrate-modal"]')).not.toBeNull();
    expect(input('calibrate-target-qps').value).toBe('');
    expect(input('calibrate-cpu').value).toBe('500m');
    expect(input('calibrate-memory').value).toBe('512Mi');
    expect(input('calibrate-criterion').value).toBe('failures>10%, p95>500ms');
    expect(input('calibrate-seed-qps').value).toBe('');
    expect(container!.textContent).toContain('Calibrate scenario 7');
  });

  it('refuses to submit without a target QPS: no POST, no handoff', async () => {
    stubCreate();
    await renderModal();
    await clickSubmit();
    expect(posts).toHaveLength(0);
    expect(created).not.toHaveBeenCalled();
    expect(container!.textContent).toContain('Target QPS is required');
  });

  it("submits POST /api/calibrations with exactly the spec's fields, then hands off the new execution id", async () => {
    stubCreate();
    await renderModal();
    await fill('calibrate-target-qps', '500');
    await clickSubmit();

    expect(posts).toHaveLength(1);
    expect(posts[0].url).toContain('/api/calibrations');
    const form = new URLSearchParams(posts[0].body);
    // The spec's fixed fields...
    expect(form.get('project_id')).toBe('3');
    expect(form.get('engine')).toBe('jmeter');
    expect(form.get('criterion')).toBe('failures>10%, p95>500ms');
    expect(form.get('cpu')).toBe('500m');
    expect(form.get('memory')).toBe('512Mi');
    // ...the phase 41 scenario binding: the scenario this row names and
    // the execution the dialog was launched from (its entry is what the
    // backend copies -- without these the created execution could never
    // run).
    expect(form.get('scenario_id')).toBe('7');
    expect(form.get('source_execution_id')).toBe('12');
    // ...the auto-generated name: "calibrate <scenario name> <timestamp>".
    expect(form.get('name')).toMatch(/^calibrate smoke \S+/);
    // The optional bounds were left empty: NOT sent, so the backend's own
    // defaults apply (the creation contract carries only what was set).
    expect(form.has('seed_qps')).toBe(false);
    expect(form.has('max_qps')).toBe(false);
    expect(form.has('max_steps')).toBe(false);
    expect(form.has('hold_seconds')).toBe(false);

    // Navigation handoff: the parent navigates to the created execution.
    expect(created).toHaveBeenCalledWith(42);
  });

  it('sends the optional bounds only when they are filled in', async () => {
    stubCreate();
    await renderModal();
    await fill('calibrate-target-qps', '1000');
    await fill('calibrate-seed-qps', '20');
    await fill('calibrate-max-qps', '2000');
    await fill('calibrate-max-steps', '12');
    await fill('calibrate-hold-seconds', '45');
    await clickSubmit();

    const form = new URLSearchParams(posts[0].body);
    expect(form.get('seed_qps')).toBe('20');
    expect(form.get('max_qps')).toBe('2000');
    expect(form.get('max_steps')).toBe('12');
    expect(form.get('hold_seconds')).toBe('45');
    expect(created).toHaveBeenCalledWith(42);
  });

  it('surfaces the server error and stays put on a 400', async () => {
    stubCreate(400, { message: 'calibration: pod CPU and memory are required' });
    await renderModal();
    await fill('calibrate-target-qps', '500');
    await clickSubmit();

    expect(posts).toHaveLength(1);
    expect(created).not.toHaveBeenCalled();
    expect(container!.textContent).toContain('pod CPU and memory are required');
  });
});

// Phase 42's hotfix: the criterion is validated client-side against the
// Taurus expression grammar -- the prose that used to pass through here
// was rejected by bzt only at run time ("Unsupported fail criteria
// subject: error_rate"), after the execution existed.
describe('CalibrateScenarioModal criterion validation (phase 42)', () => {
  it('rejects prose criteria with a visible error and no POST', async () => {
    stubCreate();
    await renderModal();
    await fill('calibrate-target-qps', '500');
    await fill('calibrate-criterion', 'error_rate < 0.01 AND p95 < 500ms');
    await clickSubmit();

    expect(posts).toHaveLength(0);
    expect(created).not.toHaveBeenCalled();
    expect(container!.textContent).toContain('is not a Taurus expression');
    // The message names the offending input so the operator knows what to fix.
    expect(container!.textContent).toContain('error_rate < 0.01 AND p95 < 500ms');
  });

  it('rejects an empty criterion with the stated grammar', async () => {
    stubCreate();
    await renderModal();
    await fill('calibrate-target-qps', '500');
    await fill('calibrate-criterion', ' , ');
    await clickSubmit();

    expect(posts).toHaveLength(0);
    expect(container!.textContent).toContain('Criterion is required');
  });

  it('names only the invalid expression in a mixed list', async () => {
    stubCreate();
    await renderModal();
    await fill('calibrate-target-qps', '500');
    await fill('calibrate-criterion', 'failures>10%, latency>500ms');
    await clickSubmit();

    expect(posts).toHaveLength(0);
    expect(container!.textContent).toContain('"latency>500ms" is not a Taurus expression');
  });

  it("accepts the default and posts the field unchanged", async () => {
    stubCreate();
    await renderModal();
    await fill('calibrate-target-qps', '500');
    await clickSubmit();

    expect(posts).toHaveLength(1);
    const form = new URLSearchParams(posts[0].body);
    expect(form.get('criterion')).toBe('failures>10%, p95>500ms');
    expect(created).toHaveBeenCalledWith(42);
  });
});
