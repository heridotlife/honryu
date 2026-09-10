// The Webhooks card's mounted interactions (phase 40), NewTest.test.tsx's
// createRoot + act style with fetch stubbed per-URL: the registry lists,
// the add form calls the create route (and shows the backend's https-only
// rejection), the toggle switch PUTs the new state, and delete takes a
// second confirming click.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import WebhooksCard from './WebhooksCard';
import type { Webhook } from '../api/webhooks';

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const twoHooks: Webhook[] = [
  {
    id: 3,
    url: 'https://hooks.example.com/a-very-long-receiver-url-that-should-be-truncated-in-the-row',
    has_secret: true,
    enabled: true,
    created_by: 'op@acme',
    created_time: '2026-09-01T00:00:00Z',
  },
  {
    id: 5,
    url: 'https://hooks.example.com/short',
    has_secret: false,
    enabled: false,
    created_time: '2026-09-02T00:00:00Z',
  },
];

interface Calls {
  urls: string[];
  methods: string[];
  bodies: string[];
}

let container: HTMLDivElement | null = null;
let root: Root | null = null;
let calls: Calls = { urls: [], methods: [], bodies: [] };
let createStatus = 201;

async function renderCard(): Promise<void> {
  container = document.createElement('div');
  document.body.appendChild(container);
  calls = { urls: [], methods: [], bodies: [] };
  createStatus = 201;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.urls.push(url);
      calls.methods.push((init?.method as string) ?? 'GET');
      calls.bodies.push(String(init?.body ?? ''));
      if (url === '/api/projects/1/webhooks') {
        if ((init?.method as string) === 'POST') {
          if (createStatus !== 201) {
            return json({ message: 'url must be https' }, createStatus);
          }
          return json(
            {
              id: 9,
              url: 'https://hooks.example.com/new',
              has_secret: true,
              enabled: true,
              created_time: '2026-09-03T00:00:00Z',
            },
            201
          );
        }
        return json(twoHooks);
      }
      if (url === '/api/projects/1/webhooks/3/enabled') {
        return json({ message: 'updated' });
      }
      if (url === '/api/projects/1/webhooks/3') {
        return new Response(null, { status: 204 });
      }
      return json({ message: `no stub for ${url}` }, 500);
    })
  );
  root = createRoot(container);
  await act(async () => {
    root!.render(<WebhooksCard projectId={1} />);
  });
  // Let the mount-time list land.
  await act(async () => {});
}

afterEach(() => {
  vi.unstubAllGlobals();
  root?.unmount();
  container?.remove();
  container = null;
  root = null;
});

const tid = (id: string): HTMLElement => {
  const el = container!.querySelector(`[data-testid="${id}"]`);
  if (!el) {
    throw new Error(`missing ${id}`);
  }
  return el as HTMLElement;
};

/** Native value setter + input event (React's tracker ignores plain writes). */
async function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
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

describe('WebhooksCard', () => {
  it('lists the project webhooks with toggle and delete controls', async () => {
    await renderCard();

    expect(tid('webhook-row-3')).toBeTruthy();
    expect(tid('webhook-row-5')).toBeTruthy();
    expect(tid('webhook-toggle-3')).toBeTruthy();
    expect(tid('webhook-delete-3')).toBeTruthy();
    // The registered URL is shown, truncated for the long one.
    expect(tid('webhook-row-3').textContent).toContain('hooks.example.com');
  });

  it('adds a webhook through the form', async () => {
    await renderCard();

    await type(tid('webhook-url-input') as HTMLInputElement, 'https://hooks.example.com/new');
    await type(tid('webhook-secret-input') as HTMLInputElement, 's3cr3t');
    await click(tid('add-webhook-btn'));
    await act(async () => {});

    const addCall = calls.urls.findIndex((u, i) => u === '/api/projects/1/webhooks' && calls.methods[i] === 'POST');
    expect(addCall).toBeGreaterThanOrEqual(0);
    const body = new URLSearchParams(calls.bodies[addCall]);
    expect(body.get('url')).toBe('https://hooks.example.com/new');
    expect(body.get('secret')).toBe('s3cr3t');
    expect(tid('webhook-row-9')).toBeTruthy();
  });

  it('shows the backend rejection when the URL is not https', async () => {
    await renderCard();
    createStatus = 400;

    await type(tid('webhook-url-input') as HTMLInputElement, 'http://cleartext.example.com/hook');
    await click(tid('add-webhook-btn'));
    await act(async () => {});

    expect(container!.textContent).toContain('url must be https');
  });

  it('toggles a webhook through the enabled switch', async () => {
    await renderCard();

    await click(tid('webhook-toggle-3'));
    await act(async () => {});

    const toggleCall = calls.urls.findIndex(u => u === '/api/projects/1/webhooks/3/enabled');
    expect(toggleCall).toBeGreaterThanOrEqual(0);
    expect(calls.methods[toggleCall]).toBe('PUT');
    expect(new URLSearchParams(calls.bodies[toggleCall]).get('enabled')).toBe('false');
  });

  it('deletes a webhook only on the second, confirming click', async () => {
    await renderCard();

    await click(tid('webhook-delete-3'));
    expect(calls.urls).not.toContain('/api/projects/1/webhooks/3');

    await click(tid('webhook-delete-3'));
    await act(async () => {});
    const delCall = calls.urls.findIndex((u, i) => u === '/api/projects/1/webhooks/3' && calls.methods[i] === 'DELETE');
    expect(delCall).toBeGreaterThanOrEqual(0);
    expect(container!.querySelector('[data-testid="webhook-row-3"]')).toBeNull();
  });
});
