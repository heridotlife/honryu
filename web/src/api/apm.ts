// APM deep-link support for correlation ids (phase 10): the raw id is
// always the copy-paste affordance; a deployment-specific URL template,
// stored client-side in localStorage (honryu-apm-template), optionally
// turns it into a link. No server involvement -- same precedent as
// DashboardLayout's honryu-theme key.
//
// Phase 37 adds the deployment-wide successor: templates configured in
// Helm (apm.linkTemplates), served by GET /api/apm-links with four
// placeholders, substituted per run on the report page. The per-browser
// template above stays as-is -- a personal overlay that works even where
// the operator cannot change Helm values.

import { apiClient } from './client';

/** The placeholder formatApmLink substitutes. */
export const APM_TEMPLATE_PLACEHOLDER = '{correlation_id}';

const STORAGE_KEY = 'honryu-apm-template';

/**
 * Builds an APM deep link from a template, or returns null when no link
 * should render: empty/whitespace template, template missing the
 * {correlation_id} placeholder, or empty correlation id. Every occurrence
 * of the placeholder is substituted.
 */
export function formatApmLink(template: string, correlationId: string): string | null {
  const trimmed = template.trim();
  if (!trimmed || !correlationId || !trimmed.includes(APM_TEMPLATE_PLACEHOLDER)) {
    return null;
  }
  return trimmed.replaceAll(APM_TEMPLATE_PLACEHOLDER, correlationId);
}

/** The saved template, or '' when none is stored. */
export function loadApmTemplate(): string {
  return localStorage.getItem(STORAGE_KEY) ?? '';
}

/** Stores the template as given (empty string clears it). */
export function saveApmTemplate(template: string): void {
  localStorage.setItem(STORAGE_KEY, template);
}

/** One deployment-configured APM link-out (phase 37): GET /api/apm-links.
 * url_template keeps its {{placeholders}} intact; the report page
 * substitutes per-run values. */
export interface ApmLinkTemplate {
  name: string;
  url_template: string;
}

/** GET /api/apm-links -- the deployment's templates, or [] when none are
 * configured (a null wire body normalizes to empty, the house rule). */
export async function getApmLinks(): Promise<ApmLinkTemplate[]> {
  const got = await apiClient.get<ApmLinkTemplate[] | null>('/apm-links');
  return got ?? [];
}

/** The per-run values a template's placeholders substitute. The ids are
 * always on the report; correlation_id and project_id are optional because
 * runs predate telemetry and the execution read can fail server-side. */
export interface ApmLinkValues {
  correlation_id?: string;
  execution_id: number;
  run_id: number;
  project_id?: number;
}
/** Any remaining {{placeholder}} after substitution -- a value the report
 * did not carry, or a placeholder the server never validated. */
const LEFTOVER_PLACEHOLDER = /\{\{[a-z_]+\}\}/;

/** Substitutes a template's {{placeholders}} with the run's values and
 * returns the URL, or null when a placeholder would remain unsubstituted:
 * a half-substituted URL is a dead link, and no button beats a broken one.
 * Every occurrence is replaced; single braces (JSON query payloads) are
 * never placeholders. */
export function substituteApmLink(urlTemplate: string, values: ApmLinkValues): string | null {
  let out = urlTemplate;
  for (const [key, value] of Object.entries(values)) {
    if (value !== undefined) {
      out = out.replaceAll(`{{${key}}}`, String(value));
    }
  }
  return LEFTOVER_PLACEHOLDER.test(out) ? null : out;
}
