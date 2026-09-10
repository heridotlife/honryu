package ports

import (
	"context"
	"time"

	"github.com/heridotlife/honryu/internal/domain/digest"
)

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
