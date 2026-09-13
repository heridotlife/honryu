// Package digest models a periodic report digest: the aggregation of every
// completed run a project's executions produced in one window, stored once
// and delivered as a report.digest event. Pure domain: no I/O, no
// aggregation -- that is digestapp's job; this package owns the shape and
// the period grammar both sides of the wire share.
package digest

import (
	"errors"
	"time"
)

// ErrPeriodInvalid rejects a period outside the supported grammar. Callers
// compare with errors.Is.
var ErrPeriodInvalid = errors.New("digest: period must be daily or weekly")

// DeliveryStatus is what became of a digest's outbound delivery, recorded
// on the stored row at the finalize point. Three words, the whole story:
type DeliveryStatus string

const (
	// DeliveryPending means no confirmed delivery has been recorded: the
	// row was fired with no deliverer wired, nothing was configured to
	// notify, or it predates delivery tracking. It is the zero state a
	// row is born in, not an error.
	DeliveryPending DeliveryStatus = "pending"
	// DeliveryDelivered means at least one configured receiver confirmed
	// the digest (2xx) at DeliveredAt. A sibling receiver's failure does
	// not demote this: the digest got out.
	DeliveryDelivered DeliveryStatus = "delivered"
	// DeliveryFailed means delivery was attempted and no receiver
	// confirmed -- the window is otherwise lost to receivers, since
	// digests tile the timeline and are never re-fired.
	DeliveryFailed DeliveryStatus = "failed"
)

// Period is how often a project's digest fires. The stored value is the
// word, not its duration, so a schedule row stays legible in the database
// and the grammar can only grow by adding a word here.
type Period string

const (
	// PeriodDaily fires one digest per 24 hours.
	PeriodDaily Period = "daily"
	// PeriodWeekly fires one digest per 7 days.
	PeriodWeekly Period = "weekly"
)

// ParsePeriod validates raw as a supported period, returning it typed or
// ErrPeriodInvalid. The boundary where operator input (API form values,
// schedule rows) becomes a Period -- everything downstream may assume the
// word is one of the two above.
func ParsePeriod(raw string) (Period, error) {
	switch Period(raw) {
	case PeriodDaily, PeriodWeekly:
		return Period(raw), nil
	default:
		return "", ErrPeriodInvalid
	}
}

// Duration is the period's window length: how far back a first-ever digest
// reaches, and the minimum spacing between two fires of the same schedule.
func (p Period) Duration() time.Duration {
	switch p {
	case PeriodWeekly:
		return 7 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// Digest is one stored digest row: the window it covers and the exact
// serialised payload that was delivered for it. A projection of a past
// aggregation, not an aggregate with invariants -- the row is written once
// and never mutated, so there is nothing here to validate beyond what
// BuildDigest already guaranteed when it minted the bytes.
type Digest struct {
	// ID is the storage-assigned row identity; zero before Save.
	ID int64
	// ProjectID is the project whose runs the digest aggregated.
	ProjectID int64
	// Period is the digest period the row was fired under.
	Period Period
	// WindowStart and WindowEnd bound the aggregation: a run counts when
	// its report started in [WindowStart, WindowEnd). The next digest of
	// the same period starts at WindowEnd, so windows tile without gaps.
	WindowStart time.Time
	WindowEnd   time.Time
	// Payload is the delivered report.digest event body, verbatim.
	Payload []byte
	// DeliveryStatus records what became of the outbound delivery at the
	// finalize point: pending (no confirmed delivery yet), delivered, or
	// failed. Rows are born pending; only MarkDigestDelivery moves them.
	DeliveryStatus DeliveryStatus
	// DeliveredAt is when a receiver confirmed the delivery; nil unless
	// DeliveryStatus is delivered.
	DeliveredAt *time.Time
	// CreatedTime is when the digest was fired and stored.
	CreatedTime time.Time
}
