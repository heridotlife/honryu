// Status chip for run rows (phase 67b): OutcomeBadge's outcome palette with
// an icon riding beside the word, so a run's status is never color-only
// (design-review rule). The classes are imported -- not copied -- from
// OutcomeBadge so the two chips cannot drift.
import { AlertTriangle, Ban, CheckCircle2, XCircle } from 'lucide-react';
import type { LucideIcon } from 'lucide-react';
import type { Outcome } from '../api/reports';
import { outcomeClasses } from './ui/OutcomeBadge';

/** The never-color-only pairing: one glyph per outcome, always with the word. */
export const outcomeIcons: Record<Outcome, LucideIcon> = {
  passed: CheckCircle2,
  failed: XCircle,
  aborted: Ban,
  error: AlertTriangle,
};

export default function RunStatusBadge({ outcome }: { outcome: Outcome }) {
  const Icon = outcomeIcons[outcome];
  return (
    <span
      className={`inline-flex items-center gap-1 rounded-full px-2.5 py-0.5 text-xs font-medium ${outcomeClasses[outcome]}`}
    >
      <Icon aria-hidden className="h-3.5 w-3.5" />
      {outcome}
    </span>
  );
}
