// Row-label helpers for the execution list surfaces (Executions page rows
// and the Reports page's execution picker). The calibrate flow
// (CalibrateScenarioModal) mints execution names as
// "calibrate <scenario> <ISO timestamp>" for uniqueness, and rows used to
// glue that raw ISO string onto the name next to a second, localized
// rendering of the same instant: "#16 calibrate checkout
// 2026-09-10T16:47:22.442Z jmeter · 9/11/2026, 1:47:22 AM". Rows now show
// the name without the raw ISO and exactly one short formatted timestamp,
// which also keeps the accessible label coherent for screen readers.

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

/**
 * The calibrate flow's uniqueness suffix: a whitespace + ISO-8601 instant
 * (optional fractional seconds, Z or numeric offset) at the end of a name.
 */
const ISO_SUFFIX = /\s\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})$/;

/**
 * The name a row should show: execution names with a trailing ISO
 * timestamp (the calibrate flow's mint) lose it -- the row's formatted
 * creation time carries the same instant. Names without one come back
 * untouched.
 */
export function executionDisplayName(name: string): string {
  return name.replace(ISO_SUFFIX, '');
}

/**
 * The one timestamp a row shows: "Sep 10, 16:47" short form, local time.
 * Input that fails to parse (an old row with junk data) comes back
 * verbatim, the same fallback the previous toLocaleString rendering had.
 */
export function formatRowTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return iso;
  }
  const pad = (n: number): string => String(n).padStart(2, '0');
  return `${MONTHS[d.getMonth()]} ${d.getDate()}, ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
