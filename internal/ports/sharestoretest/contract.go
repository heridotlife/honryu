// Package sharestoretest is the shared conformance suite every ShareStore
// must pass, fake and real alike.
package sharestoretest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/ports"
)

// tok builds a 64-char token from a repeated letter, so fixtures read as
// distinct tokens without 64-character literals on every line.
func tok(letter string) string { return strings.Repeat(letter, 64) }

// NewStore builds a store with no share links in it.
type NewStore func(t *testing.T) ports.ShareStore

// Run exercises ShareStore behaviour.
func Run(t *testing.T, newStore NewStore) {
	t.Helper()
	ctx := context.Background()

	t.Run("CreateAndGet", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateShare(ctx, 7, tok("a"), "dave", nil); err != nil {
			t.Fatalf("CreateShare: %v", err)
		}
		got, err := s.GetShareByToken(ctx, tok("a"))
		if err != nil {
			t.Fatalf("GetShareByToken: %v", err)
		}
		if got.RunID != 7 || got.Token != tok("a") {
			t.Errorf("share = run %d token %q, want run 7", got.RunID, got.Token)
		}
		if got.CreatedBy != "dave" {
			t.Errorf("created_by = %q, want %q", got.CreatedBy, "dave")
		}
		if got.Expires != nil {
			t.Errorf("expires = %v, want nil for a never-expiring link", got.Expires)
		}
		if got.CreatedTime.IsZero() {
			t.Errorf("created_time is zero; the issuing UI shows it")
		}
	})

	t.Run("MultipleTokensPerRun", func(t *testing.T) {
		// Two customers, two links, one run: revoking one must never break
		// the other, so both resolve independently.
		s := newStore(t)
		for _, tok := range []string{tok("a"), tok("b")} {
			if err := s.CreateShare(ctx, 7, tok, "dave", nil); err != nil {
				t.Fatalf("CreateShare(%q): %v", tok, err)
			}
		}
		for _, tok := range []string{tok("a"), tok("b")} {
			got, err := s.GetShareByToken(ctx, tok)
			if err != nil {
				t.Fatalf("GetShareByToken(%q): %v", tok, err)
			}
			if got.RunID != 7 {
				t.Errorf("token %q -> run %d, want 7", tok, got.RunID)
			}
		}
	})

	t.Run("GetUnknownTokenIsNotFound", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.GetShareByToken(ctx, "deadbeef"); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("GetShareByToken(unknown) = %v, want ErrNotFound", err)
		}
	})

	t.Run("ExpiryRoundTrips", func(t *testing.T) {
		// Whole-second times: a DATETIME column without fractional seconds
		// must round-trip what the contract asserts, and sub-second
		// precision is not part of the port.
		s := newStore(t)
		expires := time.Unix(2_000_000_000, 0).UTC()
		if err := s.CreateShare(ctx, 7, tok("c"), "dave", &expires); err != nil {
			t.Fatalf("CreateShare: %v", err)
		}
		got, err := s.GetShareByToken(ctx, tok("c"))
		if err != nil {
			t.Fatalf("GetShareByToken: %v", err)
		}
		if got.Expires == nil || !got.Expires.Equal(expires) {
			t.Errorf("expires = %v, want %v", got.Expires, expires)
		}
	})

	t.Run("ListByRunInIssueOrder", func(t *testing.T) {
		s := newStore(t)
		// Run 7 gets two links; run 8 gets one, which must not leak in.
		for _, row := range []struct {
			run int64
			tok string
		}{
			{7, tok("a")},
			{7, tok("b")},
			{8, tok("c")},
		} {
			if err := s.CreateShare(ctx, row.run, row.tok, "dave", nil); err != nil {
				t.Fatalf("CreateShare(run %d): %v", row.run, err)
			}
		}
		got, err := s.ListSharesByRun(ctx, 7)
		if err != nil {
			t.Fatalf("ListSharesByRun: %v", err)
		}
		if len(got) != 2 || got[0].Token != tok("a") || got[1].Token != tok("b") {
			t.Errorf("ListSharesByRun(7) = %d links, want 2 oldest-first", len(got))
		}
	})

	t.Run("DeleteRemovesOnlyTheNamedToken", func(t *testing.T) {
		s := newStore(t)
		for _, tok := range []string{tok("a"), tok("b")} {
			if err := s.CreateShare(ctx, 7, tok, "dave", nil); err != nil {
				t.Fatalf("CreateShare(%q): %v", tok, err)
			}
		}
		if err := s.DeleteShare(ctx, 7, tok("a")); err != nil {
			t.Fatalf("DeleteShare: %v", err)
		}
		if _, err := s.GetShareByToken(ctx, tok("a")); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("deleted token resolves: %v, want ErrNotFound", err)
		}
		// The other customer's link is untouched.
		if _, err := s.GetShareByToken(ctx, tok("b")); err != nil {
			t.Errorf("sibling token broken by revoke: %v", err)
		}
		if err := s.DeleteShare(ctx, 7, tok("a")); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("deleting an unknown token = %v, want ErrNotFound", err)
		}
		// A token exists but was issued for another run: not this run's to
		// delete.
		if err := s.DeleteShare(ctx, 8, tok("b")); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("deleting another run's token = %v, want ErrNotFound", err)
		}
	})
}
