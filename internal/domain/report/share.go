package report

import "time"

// ShareToken is one issued share link: a secret that reads exactly one
// run's report, no session required. It is a bare capability -- knowing the
// token is the entire authorization -- which is why tokens are 256-bit
// random values minted by the HTTP layer, never derived from anything.
type ShareToken struct {
	// ID is the storage-assigned row identity; links themselves address
	// runs by token only.
	ID int64
	// RunID is the single run this link can read.
	RunID int64
	// Token is the secret (64 lowercase hex chars).
	Token string
	// CreatedBy names the account that issued the link; empty in no-auth
	// mode.
	CreatedBy string
	// CreatedTime is when the link was issued.
	CreatedTime time.Time
	// Expires is when the link stops working, or nil for a link that never
	// expires.
	Expires *time.Time
}

// ExpiredAt reports whether the link is dead at now. A nil Expires never
// expires. Expiry is deliberately evaluated here, at fetch time, rather
// than filtered in storage: the issuing UI keeps listing expired links
// (their revoke buttons still work) while the public fetch answers 404 --
// the same row, two different answers, decided by policy at the edge.
func (s ShareToken) ExpiredAt(now time.Time) bool {
	return s.Expires != nil && !now.Before(*s.Expires)
}
