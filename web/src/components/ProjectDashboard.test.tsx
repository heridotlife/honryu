// The project dashboard's render contract (phase 66), DigestCard.test's
// createRoot + act style with fetch stubbed per-URL: the summary fetch
// powers the KPI row, the regressed card takes its danger state when the
// count is positive, and an execution-less project renders the empty state
// -- never a chart of nothing.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import ProjectDashboard from './ProjectDashboard';
import type { ProjectSummary } from '../api/generated';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const baseSummary: ProjectSummary = {
  project_id: 1,
  total_executions: 3,
  last_execution_time: '2026-09-20T10:00:00Z',
  scenario_count: 5,
  template_count: 7,
  regressed_count: 0,
  last_run: { run_id: 9, outcome: 'passed', started_at: '2026-09-20T12:00:00Z' },
  throughput_series: [
    { run_id: 7, started_at: '2026-09-19T10:00:00Z', achieved_throughput: 90, requested_throughput: 100 },
    { run_id: 8, started_at: '2026-09-19T11:00:00Z', achieved_throughput: 100, requested_throughput: 100 },
    { run_id: 9, started_at: '2026-09-20T12:00:00Z', achieved_throughput: 120, requested_throughput: 100 },
  ],
};

let container: HTMLDivElement | null = null;
let root: Root | null = null;
let summaryBody: unknown = baseSummary;
let summaryStatus = 200;

async function renderDashboard(projectId = 1): Promise<void> {
  container = document.createElement('div');
  document.body.appendChild(container);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url === `/api/projects/${projectId}/summary`) {
        return json(summaryBody, summaryStatus);
      }
      return json({ message: 'unexpected ' + url }, 500);
    })
  );
  root = createRoot(container);
  await act(async () => {
    // MemoryRouter: the empty state's action is a Link into the template
    // picker, which needs a router ancestor.
    root!.render(
      <MemoryRouter>
        <ProjectDashboard projectId={projectId} />
      </MemoryRouter>,
    );
  });
  // Drain the fetch promise chain.
  await act(async () => {});
}

function teardown(): void {
  if (root) {
    act(() => root!.unmount());
  }
  container?.remove();
  container = null;
  root = null;
  vi.unstubAllGlobals();
  summaryBody = baseSummary;
  summaryStatus = 200;
}

afterEach(teardown);

describe('ProjectDashboard', () => {
  it('renders the KPI cards, sparkline, and last-run chip from one summary fetch', async () => {
    await renderDashboard();

    const dash = container!.querySelector('[data-testid="project-dashboard"]');
    expect(dash).not.toBeNull();

    expect(container!.querySelector('[data-testid="project-kpi-executions"]')?.textContent).toContain('3');
    expect(container!.querySelector('[data-testid="project-kpi-scenarios"]')?.textContent).toContain('5');
    expect(container!.querySelector('[data-testid="project-kpi-templates"]')?.textContent).toContain('7');

    const regressed = container!.querySelector('[data-testid="project-kpi-regressed"]');
    expect(regressed?.textContent).toContain('0');
    // Zero regressions: the card is calm, not danger-tinted.
    expect(regressed?.getAttribute('data-danger')).toBe('false');

    // The sparkline charts the series' achieved throughput, oldest first.
    const spark = container!.querySelector('[data-testid="project-sparkline-card"] svg[role="img"]');
    expect(spark).not.toBeNull();
    expect(spark?.getAttribute('aria-label')).toContain('3 most recent runs');

    const chip = container!.querySelector('[data-testid="project-last-run"]');
    expect(chip?.textContent).toContain('passed');
  });

  it('danger-tints the regressed card when the count is positive', async () => {
    summaryBody = {
      ...baseSummary,
      regressed_count: 2,
      throughput_series: [
        { run_id: 7, started_at: '2026-09-19T11:00:00Z', achieved_throughput: 40, requested_throughput: 100, regressed: true },
        { run_id: 8, started_at: '2026-09-19T11:00:00Z', achieved_throughput: 100, requested_throughput: 100 },
      ],
    };
    await renderDashboard();

    const regressed = container!.querySelector('[data-testid="project-kpi-regressed"]');
    expect(regressed?.getAttribute('data-danger')).toBe('true');
    expect(regressed?.className).toContain('red-');
    expect(regressed?.textContent).toContain('2');

    // The series caption names the regressed points too.
    expect(container!.querySelector('[data-testid="project-sparkline-card"]')?.textContent).toContain('1 regressed');
  });

  it('renders the empty state for a project with no executions', async () => {
    summaryBody = { ...baseSummary, total_executions: 0, last_run: null, throughput_series: [] };
    await renderDashboard();

    const empty = container!.querySelector('[data-testid="project-dashboard-empty"]');
    expect(empty).not.toBeNull();
    expect(empty?.textContent).toContain('No executions yet');
    // Phase 76 alignment: icon + title + the ONE action (the template
    // picker, the create flow this SPA has), SloPanel still mounted below
    // -- objectives are defined before the first run grades them.
    expect(empty!.querySelector('svg')).not.toBeNull();
    const action = empty!.querySelector<HTMLAnchorElement>('[data-testid="empty-state-action"]')!;
    expect(action?.getAttribute('href')).toBe('/executions/new');
    expect(container!.querySelector('[data-testid="slo-panel"]')).not.toBeNull();
    // No KPI grid, no chart of nothing.
    expect(container!.querySelector('[data-testid="project-kpis"]')).toBeNull();
    expect(container!.querySelector('svg[role="img"]')).toBeNull();
  });

  it('degrades to a note when the summary fetch fails', async () => {
    summaryStatus = 500;
    await renderDashboard();

    expect(container!.querySelector('[data-testid="project-dashboard-error"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="project-kpis"]')).toBeNull();
  });
});
