// Base fetch wrapper for the honryu API: attaches the auth header, and
// surfaces the backend's error envelope ({"message": "..."}, see
// internal/adapters/httpapi/response.go's writeError) as a typed ApiError
// instead of a generic fetch failure.

export class ApiError extends Error {
  status: number;
  /** Parsed JSON body when the response carried one (e.g. DiagnosticsError). */
  data?: unknown;

  constructor(status: number, message: string, data?: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.data = data;
  }
}

export interface ApiClientOptions {
  /** Defaults to "/api", proxied by Vite in dev and same-origin in prod (cmd/api serves both). */
  baseUrl?: string;
  /** Defaults to reading "honryu_token" from localStorage; returns null when unauthenticated (no-auth mode). */
  getToken?: () => string | null;
  /** Called once when an API response comes back 401 and the session is dead. Defaults to
   * hard-redirecting to "/" so the profile picker boots (session-surface calls exempt). */
  onUnauthorized?: () => void;
}

/** The paths whose 401s are the picker's normal unauthenticated state, not a
 * dead session mid-use: identity (GET /me), the session itself (POST/DELETE
 * /session -- the picker's createSession MUST NOT bounce), and the persona
 * list (GET /session/profiles). Everything else 401ing means the cookie
 * expired while the operator was working. */
function isSessionSurface(path: string): boolean {
  return path === '/me' || path === '/session' || path.startsWith('/session/');
}

/** The default dead-session reaction: drop any local bearer token, then let
 * a full page load at "/" rebuild the app around the profile picker. A hard
 * redirect (not router state) because React context, polls, and SSE streams
 * all still believe the session is alive. */
function defaultOnUnauthorized(): void {
  localStorage.removeItem('honryu_token');
  window.location.assign('/');
}

export class ApiClient {
  /** Exposed read-only so callers needing a raw URL (e.g. EventSource, which can't use fetch) can build one. */
  readonly baseUrl: string;
  private readonly getToken: () => string | null;
  private readonly onUnauthorized: () => void;
  /** Once-per-instance latch: a burst of 401s (a page firing several polls
   * at once) must trigger exactly one redirect, never a loop. The hard
   * navigation rebuilds every module, so no reset is needed in practice. */
  private unauthorizedHandled = false;

  constructor(options: ApiClientOptions = {}) {
    this.baseUrl = options.baseUrl ?? '/api';
    this.getToken = options.getToken ?? (() => localStorage.getItem('honryu_token'));
    this.onUnauthorized = options.onUnauthorized ?? defaultOnUnauthorized;
  }

  /** Fires the dead-session reaction at most once per client instance. */
  private handleUnauthorized(path: string): void {
    if (this.unauthorizedHandled || isSessionSurface(path)) {
      return;
    }
    this.unauthorizedHandled = true;
    this.onUnauthorized();
  }

  /** Sends a request with the usual auth/Accept headers, returning the checked response. */
  private async send(path: string, init: RequestInit): Promise<Response> {
    const headers = new Headers(init.headers);
    headers.set('Accept', 'application/json');
    const token = this.getToken();
    if (token) {
      headers.set('Authorization', `Bearer ${token}`);
    }

    const res = await fetch(`${this.baseUrl}${path}`, { ...init, headers });
    if (res.status === 401) {
      this.handleUnauthorized(path);
    }
    if (!res.ok) {
      const bodyText = await res.text();
      let data: unknown;
      try {
        data = JSON.parse(bodyText);
      } catch {
        data = undefined;
      }
      throw new ApiError(res.status, extractErrorMessageFromText(bodyText, res.statusText, res.status), data);
    }
    return res;
  }

  async request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const res = await this.send(path, init);
    if (res.status === 204) {
      return undefined as T;
    }
    return (await res.json()) as T;
  }

  /** GETs a text/plain body (e.g. shard config/log objects) with the same auth and error handling as request. */
  text(path: string): Promise<string> {
    return this.send(path, { method: 'GET' }).then((res) => res.text());
  }

  get<T>(path: string): Promise<T> {
    return this.request<T>(path, { method: 'GET' });
  }

  /** Every mutating honryu route takes a form-encoded body (see e.g. campaign_handlers.go's r.ParseForm), not JSON. */
  /**
   * PUT with a caller-supplied content type and body string, no JSON
   * wrapping (the G3 fragment endpoint stores text/yaml verbatim).
   */
  async putRaw(path: string, contentType: string, body: string): Promise<void> {
    const headers = new Headers({ 'Content-Type': contentType });
    const token = this.getToken();
    if (token) {
      headers.set('Authorization', `Bearer ${token}`);
    }
    const res = await fetch(`${this.baseUrl}${path}`, { method: 'PUT', headers, body });
    if (res.status === 401) {
      this.handleUnauthorized(path);
    }
    if (!res.ok) {
      const bodyText = await res.text();
      let data: unknown;
      try {
        data = JSON.parse(bodyText);
      } catch {
        data = undefined;
      }
      throw new ApiError(res.status, extractErrorMessageFromText(bodyText, res.statusText, res.status), data);
    }
  }
  post<T>(path: string, form: URLSearchParams): Promise<T> {
    return this.request<T>(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: form.toString(),
    });
  }
}

function extractErrorMessageFromText(bodyText: string, statusText: string, status: number): string {
  try {
    const body = JSON.parse(bodyText) as { message?: unknown };
    if (typeof body.message === 'string' && body.message !== '') {
      return body.message;
    }
  } catch {
    // Non-JSON or empty error body; fall through to statusText.
  }
  return statusText || `request failed with status ${status}`;
}

/**
 * The structured details payload of the backend's error envelope (phase 24:
 * {"message": ..., "details": {...}} on select errors like the 429 quota
 * refusal and the 409 engines-finished conflict), when the failing response
 * carried one -- otherwise null. Callers treat null as "render the message
 * only". Walks the error's cause chain so a wrapper (NewTest's stepError)
 * does not hide the envelope.
 */
export function errorDetails(err: unknown): Record<string, unknown> | null {
  let e: unknown = err;
  for (let depth = 0; e instanceof Error && depth < 5; depth++) {
    if (e instanceof ApiError && e.data !== null && typeof e.data === 'object') {
      const details = (e.data as { details?: unknown }).details;
      if (details !== null && typeof details === 'object') {
        return details as Record<string, unknown>;
      }
    }
    e = (e as { cause?: unknown }).cause;
  }
  return null;
}

/** The client every page uses; a fresh ApiClient() with custom options is only needed in tests. */
export const apiClient = new ApiClient();
