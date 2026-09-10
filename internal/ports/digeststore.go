package ports

import (
	"context"
	"time"

	"github.com/heridotlife/honryu/internal/domain/digest"
)

// DigestScheduleStore persists the per-project digest firing schedules.
//
// One row per project is the whole configuration; there are no occurrences
// to reserve because a digest claims no capacity -- it reads stored reports
// and delivers best-effort. The store's one concurrency-critical operation
// is the due claim, which must be atomic in the ClaimDueOccurrence sense:
// two scheduler replicas polling concurrently may not double-fire the same
// project's window.

// ReportDigestStore persists the periodic report digests fired per project.
//
// A digest row is a projection of a past aggregation, written once and never
// mutated: the serialised payload is stored verbatim so the in-app feed and
// every webhook receiver see the same numbers for the same window forever.
// Scoping is by project everywhere, and the latest row's window_end per
// (project, period) doubles as the bookmark the next digest of that period
// continues from -- which is why it is read here rather than recomputed.
type ReportDigestStore interface {
	// SaveDigest stores d and returns its storage-assigned id. The caller
	// has already built the payload; created_time is the store's to stamp.
	SaveDigest(ctx context.Context, d digest.Digest) (int64, error)
	// ListDigestsByProject returns the project's digests, newest first,
	// so the operator's feed reads like a history. A limit of zero or less
	// means no limit (ListReports' convention, not SQL LIMIT 0).
	ListDigestsByProject(ctx context.Context, projectID int64, limit int) ([]digest.Digest, error)
	// LastDigestWindowEnd returns the window_end of the project's most
	// recent digest under period -- where the next one continues from --
	// or found=false when none has ever been fired for that pairing. Only
	// digests of the same period count: switching daily to weekly starts
	// the weekly history fresh rather than tiling off a daily edge.
	LastDigestWindowEnd(ctx context.Context, projectID int64, period digest.Period) (t time.Time, found bool, err error)
}

// DigestScheduleStore persists the per-project digest firing schedules.
type DigestScheduleStore interface {
	// UpsertDigestSchedule stores the project's schedule configuration,
	// creating the row or replacing its period/enabled state. last_fired
	// is NOT part of the upsert: re-enabling or switching period keeps the
	// bookmark, so fire times tile from where they left off.
	UpsertDigestSchedule(ctx context.Context, projectID int64, period digest.Period, enabled bool) error
	// GetDigestSchedule returns the project's schedule, or ErrNotFound
	// when none is configured.
	GetDigestSchedule(ctx context.Context, projectID int64) (digest.Schedule, error)
	// DeleteDigestSchedule removes the project's schedule, or ErrNotFound
	// when none exists. Past digest rows are unaffected -- history is
	// history.
	DeleteDigestSchedule(ctx context.Context, projectID int64) error
	// ClaimDueDigestSchedule atomically claims one due, enabled schedule:
	// it stamps last_fired=now on the row and returns it. Dueness is
	// last_fired IS NULL OR last_fired <= now-period (per-row: the period
	// is a word, not a duration, so the SQL guard takes the cutoff as a
	// parameter). found is false when nothing is due. Implementations must
	// make the stamp conditional on still being due (affected-rows, the
	// ClaimDueOccurrence pattern) so two replicas cannot double-fire.
	ClaimDueDigestSchedule(ctx context.Context, now time.Time) (s digest.Schedule, found bool, err error)
}
