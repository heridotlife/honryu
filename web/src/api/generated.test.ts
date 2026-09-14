// Wire-behavior tests for the generated client (web/scripts/gen-client.mjs
// output). The wrappers delegate to apiClient, so — like every api/* test —
// they stub global fetch and assert on the outgoing request: path builders
// interpolate ids, query params serialize the backend's spellings (repeated
// keys for arrays), and the form-encoded calibration POST coerces numbers the
// way the handler's ParseForm expects. The Reports page's list fetch already
// exercises the generated getExecutionsByExecutionIdReports through
// reports.test.ts; these cover the generator's other shapes.
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  paths,
  getRunsCompare,
  postCalibrations,
  getScenariosByScenarioIdRequests,
  putScenariosByScenarioIdRequests,
} from './generated';

function fetchRecorder() {
  const requests: { url: string; init?: RequestInit }[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    requests.push({ url: String(input), init });
    return new Response('null', { status: 200, headers: { 'Content-Type': 'application/json' } });
  });
  vi.stubGlobal('fetch', fetchMock);
  return requests;
}

describe('generated client', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('path builders interpolate ids after the /api base', () => {
    expect(paths.getExecutionsByExecutionIdReports(7)).toBe('/executions/7/reports');
    expect(paths.getRunsByRunIdScenariosByScenarioIdShardsByShardLog(3, 4, 2)).toBe(
      '/runs/3/scenarios/4/shards/2/log'
    );
  });

  it('GET with scalar query params appends them', async () => {
    const requests = fetchRecorder();

    await getRunsCompare({ query: { run_a: 1, run_b: 2 } });

    expect(requests[0].url).toBe('/api/runs/compare?run_a=1&run_b=2');
  });

  it('array query params repeat the key in request order', async () => {
    const requests = fetchRecorder();

    await getRunsCompare({ query: { 'run_ids[]': [9, 4, 5] } });

    expect(requests[0].url).toBe('/api/runs/compare?run_ids%5B%5D=9&run_ids%5B%5D=4&run_ids%5B%5D=5');
  });

  it('form-encoded POST coerces numbers to strings and sets the form content type', async () => {
    const requests = fetchRecorder();

    await postCalibrations({
      project_id: 2,
      name: 'search',
      engine: 'jmeter',
      criterion: 'failures>5%',
      cpu: '1',
      memory: '512Mi',
      scenario_id: 11,
      source_execution_id: 5,
    });

    expect(requests[0].url).toBe('/api/calibrations');
    expect(requests[0].init?.method).toBe('POST');
    const headers = new Headers(requests[0].init?.headers);
    expect(headers.get('Content-Type')).toBe('application/x-www-form-urlencoded');
    const body = new URLSearchParams(String(requests[0].init?.body));
    expect(body.get('project_id')).toBe('2');
    expect(body.get('scenario_id')).toBe('11');
    expect(body.get('criterion')).toBe('failures>5%');
  });

  // Phase 64: text/* operations. A text response is raw text read with
  // apiClient.text -- never res.json(), which would throw on YAML.
  it('text/yaml GET is fetched as raw text, not parsed as JSON', async () => {
    const yaml = 'multi-test:\n  scenario: false\n';
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(yaml, { status: 200, headers: { 'Content-Type': 'text/yaml' } }))
    );

    await expect(getScenariosByScenarioIdRequests(5)).resolves.toBe(yaml);
  });

  it('text/yaml PUT sends the body verbatim under the spec media type', async () => {
    const requests = fetchRecorder();
    const fragment = 'execution:\n  ramp-up: 30s # keep\n';

    await putScenariosByScenarioIdRequests(5, fragment);

    expect(requests[0].url).toBe('/api/scenarios/5/requests');
    expect(requests[0].init?.method).toBe('PUT');
    expect(new Headers(requests[0].init?.headers).get('Content-Type')).toBe('text/yaml');
    expect(requests[0].init?.body).toBe(fragment);
  });
});
