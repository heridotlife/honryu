// Types and fetchers for the run-completion webhook registry (phase 40):
// POST/GET /api/projects/{project_id}/webhooks, DELETE
// /api/projects/{project_id}/webhooks/{webhook_id}, and PUT
// .../{webhook_id}/enabled. Field names mirror webhookResponse in
// internal/adapters/httpapi/webhook_handlers.go exactly. The secret is
// write-only: it goes in the create form and never comes back, only the
// has_secret bool does.
import { apiClient } from './client';

/** One registered receiver; deliveries are phase 38's background fan-out. */
export interface Webhook {
  id: number;
  url: string;
  /** True when a secret was set at registration; the value never returns. */
  has_secret: boolean;
  enabled: boolean;
  created_by?: string;
  created_time: string;
}

/** GET /api/projects/{project_id}/webhooks -- registration order, paused rows included. */
export function listWebhooks(projectId: number): Promise<Webhook[]> {
  return apiClient.get<Webhook[]>(`/projects/${projectId}/webhooks`);
}

/** POST /api/projects/{project_id}/webhooks -- the URL must be https or the backend rejects it (400). */
export function createWebhook(projectId: number, url: string, secret?: string): Promise<Webhook> {
  const form = new URLSearchParams();
  form.set('url', url);
  if (secret !== undefined) {
    form.set('secret', secret);
  }
  return apiClient.post<Webhook>(`/projects/${projectId}/webhooks`, form);
}

/** DELETE /api/projects/{project_id}/webhooks/{webhook_id} -- 204, scoped by project. */
export function deleteWebhook(projectId: number, webhookId: number): Promise<void> {
  return apiClient.request<void>(`/projects/${projectId}/webhooks/${webhookId}`, { method: 'DELETE' });
}

/** PUT /api/projects/{project_id}/webhooks/{webhook_id}/enabled -- pause (false) or resume (true). */
export function setWebhookEnabled(projectId: number, webhookId: number, enabled: boolean): Promise<void> {
  const form = new URLSearchParams();
  form.set('enabled', String(enabled));
  return apiClient.request<void>(`/projects/${projectId}/webhooks/${webhookId}/enabled`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: form.toString(),
  });
}
