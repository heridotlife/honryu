// The run-report threshold block (phase 72): rows with icon + text
// (met/missed/unknown), hidden when there is no threshold, skeleton while
// the report is pending. ThresholdEditor.test.tsx's mounted pattern, minus
// the network -- this component is pure props.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it } from 'vitest';
import ThresholdResultsCard from './ThresholdResultsCard';
import type { ThresholdResult } from '../api/reports';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | null = null;
let root: Root | null = null;

function render(results: ThresholdResult[], pending = false) {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  act(() => {
    root!.render(<ThresholdResultsCard results={results} pending={pending} />);
  });
}

afterEach(() => {
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

const met: ThresholdResult = {
  threshold_id: 1,
  metric: 'throughput_qps',
  comparison: 'gt',
  value: 50,
  observed_value: 80,
  satisfied: true,
};
const missed: ThresholdResult = {
  threshold_id: 2,
  metric: 'http_p95_ms',
  comparison: 'lt',
  value: 300,
  observed_value: 480.5,
  satisfied: false,
};
const unknown: ThresholdResult = {
  threshold_id: 3,
  metric: 'http_p99_ms',
  comparison: 'lt',
  value: 500,
  observed_value: null,
  satisfied: null,
  reason: 'no p99 latency in the report',
};

describe('ThresholdResultsCard', () => {
  it('hides entirely when the run has no threshold results', () => {
    render([]);
    expect(container!.querySelector('[data-testid="scenario-thresholds-card"]')).toBeNull();
    expect(container!.textContent).toBe('');
  });

  it('shows a skeleton while the run\'s report is pending', () => {
    render([], true);
    expect(container!.querySelector('[data-testid="scenario-thresholds-pending"]')).not.toBeNull();
    expect(container!.textContent).toContain("Results appear when the run's report lands.");
  });

  it('renders met rows with icon + word and the observed figure', () => {
    render([met]);
    expect(container!.querySelector('[data-testid="scenario-threshold-met-0"]')).not.toBeNull();
    const row = container!.querySelector('[data-testid="scenario-threshold-row-0"]')!;
    expect(row.textContent).toContain('met');
    expect(row.textContent).toContain('observed 80 req/s');
    expect(row.textContent).toContain('greater than 50');
  });

  it('renders missed rows with icon + word and the observed figure', () => {
    render([missed]);
    expect(container!.querySelector('[data-testid="scenario-threshold-missed-0"]')).not.toBeNull();
    const row = container!.querySelector('[data-testid="scenario-threshold-row-0"]')!;
    expect(row.textContent).toContain('missed');
    expect(row.textContent).toContain('observed 480.5 ms');
  });

  it('renders an unmeasurable metric as unknown with the reason, never as a fail', () => {
    render([unknown]);
    expect(container!.querySelector('[data-testid="scenario-threshold-unknown-0"]')).not.toBeNull();
    const row = container!.querySelector('[data-testid="scenario-threshold-row-0"]')!;
    expect(row.textContent).toContain('unknown');
    expect(row.textContent).toContain('no p99 latency in the report');
    expect(row.textContent).not.toContain('missed');
    expect(row.textContent).not.toContain('met');
  });

  it('renders all three verdicts in one report, definition order', () => {
    render([met, missed, unknown]);
    expect(container!.querySelectorAll('[data-testid^="scenario-threshold-row-"]').length).toBe(3);
    expect(container!.querySelector('[data-testid="scenario-threshold-met-0"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="scenario-threshold-missed-1"]')).not.toBeNull();
    expect(container!.querySelector('[data-testid="scenario-threshold-unknown-2"]')).not.toBeNull();
  });
});
