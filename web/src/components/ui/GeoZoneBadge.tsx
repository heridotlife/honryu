// GeoZoneBadge (phase 109): a cluster's display-only geo/zone labels, as
// one chip wherever a cluster is named. Unlabeled (both empty) renders
// nothing at all, so legacy rows stay quiet -- the same stance
// ClusterBadge takes for an absent cluster. Never a scheduling input.
export default function GeoZoneBadge({ geo, zone }: { geo?: string; zone?: string }) {
  if (!geo && !zone) return null;
  return (
    <span
      data-testid="geolocation-badge"
      className="inline-flex items-center rounded-full bg-teal-100 px-2.5 py-0.5 text-xs font-medium text-teal-800 dark:bg-teal-900/30 dark:text-teal-300"
    >
      {[geo, zone].filter(Boolean).join(' · ')}
    </span>
  );
}
