package report

import (
	"testing"
	"time"
)

func TestShareToken_ExpiredAt(t *testing.T) {
	t.Parallel()
	never := ShareToken{RunID: 1, Token: "abc"}
	if never.ExpiredAt(time.Unix(9999, 0)) {
		t.Errorf("nil Expires expired")
	}

	expires := time.Unix(1000, 0).UTC()
	s := ShareToken{RunID: 1, Expires: &expires}
	if s.ExpiredAt(time.Unix(999, 0)) {
		t.Errorf("link expired before its expiry")
	}
	// The expiry instant itself is dead: a link valid "until 12:00" does
	// not answer at 12:00.
	if !s.ExpiredAt(expires) {
		t.Errorf("link still live at the exact expiry instant")
	}
	if !s.ExpiredAt(time.Unix(1001, 0)) {
		t.Errorf("link still live after its expiry")
	}
}
