import { afterEach, describe, expect, it, vi } from 'vitest';
import { formatApmLink, loadApmTemplate, saveApmTemplate, APM_TEMPLATE_PLACEHOLDER } from './apm';

describe('formatApmLink', () => {
  // The link renders only when every ingredient is present: a template, a
  // correlation id, and the placeholder connecting them. Anything less
  // returns null so callers render no link at all (the raw id remains the
  // copy-paste affordance).
  it.each([
    ['https://apm.example.com/trace/{correlation_id}', 'abc123', 'https://apm.example.com/trace/abc123'],
    ['https://apm.example.com/trace/{correlation_id}?span={correlation_id}', 'abc', 'https://apm.example.com/trace/abc?span=abc'],
    ['  https://apm.example.com/{correlation_id}  ', 'abc', 'https://apm.example.com/abc'],
  ])('substitutes every occurrence of the placeholder (%s)', (template, id, expected) => {
    expect(formatApmLink(template, id)).toBe(expected);
  });

  it.each([
    ['no link: empty template', '', 'abc123'],
    ['no link: whitespace-only template', '   ', 'abc123'],
    ['no link: template missing the placeholder', 'https://apm.example.com/trace/', 'abc123'],
    ['no link: empty correlation id', 'https://apm.example.com/trace/{correlation_id}', ''],
  ])('%s', (_name, template, id) => {
    expect(formatApmLink(template, id)).toBeNull();
  });

  it('exposes the placeholder it substitutes', () => {
    expect(formatApmLink(`https://x/${APM_TEMPLATE_PLACEHOLDER}`, 'abc')).toBe('https://x/abc');
  });
});

describe('apm template storage', () => {
  afterEach(() => {
    localStorage.removeItem('honryu-apm-template');
  });

  it('loadApmTemplate returns "" when nothing is stored', () => {
    expect(loadApmTemplate()).toBe('');
  });

  it('saveApmTemplate round-trips, and an empty string clears back to ""', () => {
    saveApmTemplate('https://apm.example.com/trace/{correlation_id}');
    expect(loadApmTemplate()).toBe('https://apm.example.com/trace/{correlation_id}');

    saveApmTemplate('');
    expect(loadApmTemplate()).toBe('');
  });
});

import { getApmLinks, substituteApmLink } from './apm';

describe('substituteApmLink (phase 37)', () => {
  it('substitutes every supported placeholder, repeatedly', () => {
    const url = substituteApmLink('https://apm.example.com/t/{{correlation_id}}?e={{execution_id}}&r={{run_id}}&p={{project_id}}#{{run_id}}', {
      correlation_id: '4bf92f35',
      execution_id: 7,
      run_id: 42,
      project_id: 3,
    });
    expect(url).toBe('https://apm.example.com/t/4bf92f35?e=7&r=42&p=3#42');
  });

  it('returns null when a placeholder would remain unsubstituted (dead link beats broken link)', () => {
    // A run without a correlation id cannot fill {{correlation_id}}; a
    // half-substituted URL points nowhere, so no button renders at all.
    expect(
      substituteApmLink('https://apm.example.com/t/{{correlation_id}}', { execution_id: 7, run_id: 42 })
    ).toBeNull();
    expect(
      substituteApmLink('https://apm.example.com/p/{{project_id}}', { execution_id: 7, run_id: 42 })
    ).toBeNull();
    // Unknown placeholders survive server validation in theory only; the
    // client still refuses to render what it could not fill.
    expect(
      substituteApmLink('https://apm.example.com/x/{{trace_id}}', {
        correlation_id: 'abc',
        execution_id: 7,
        run_id: 42,
      })
    ).toBeNull();
  });

  it('leaves single braces alone (Grafana-style JSON query payloads are not placeholders)', () => {
    const url = substituteApmLink('https://g.example.com/explore?left={"query":"{{correlation_id}}"}', {
      correlation_id: 'abc',
      execution_id: 1,
      run_id: 2,
    });
    expect(url).toBe('https://g.example.com/explore?left={"query":"abc"}');
  });

  it('renders a static template with no placeholders as-is', () => {
    expect(substituteApmLink('https://wiki.example.com/runs', { execution_id: 1, run_id: 2 })).toBe(
      'https://wiki.example.com/runs'
    );
  });
});

describe('getApmLinks (phase 37)', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('GETs /api/apm-links and passes the templates through', async () => {
    let seenUrl = '';
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        seenUrl = String(input);
        return new Response(JSON.stringify([{ name: 'Grafana Tempo', url_template: 'https://g/{{correlation_id}}' }]), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      })
    );

    const got = await getApmLinks();

    expect(seenUrl).toBe('/api/apm-links');
    expect(got).toEqual([{ name: 'Grafana Tempo', url_template: 'https://g/{{correlation_id}}' }]);
  });

  it('normalizes a null wire body to []', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('null', { status: 200, headers: { 'Content-Type': 'application/json' } }))
    );

    expect(await getApmLinks()).toEqual([]);
  });
});
