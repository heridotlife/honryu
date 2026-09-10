package digest

import "time"

// Schedule is a project's digest firing configuration: one row per project.
// A digest is a read of stored reports plus a best-effort delivery, so
// unlike an execution schedule there are no reserved occurrences and no
// horizon -- only the period, the on/off state, and when a fire was last
// claimed.
type Schedule struct {
	// ProjectID is the project whose runs the schedule digests; it is also
	// the row identity.
	ProjectID int64
	// Period is how often the digest fires.
	Period Period
	// Enabled gates the scheduler's claim: a disabled row is never due,
	// but keeps its place and its last_fired bookmark.
	Enabled bool
	// LastFired is when the scheduler last claimed a fire for this row;
	// nil means never. It is the dueness bookmark (next due at
	// LastFired+Period) and the concurrency guard, not a promise that a
	// digest row exists for it -- a claimed fire that failed to build is
	// skipped, the same way a claimed occurrence whose deploy failed is.
	LastFired *time.Time
}
