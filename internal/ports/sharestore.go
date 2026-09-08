package ports

import (
	"context"
	"time"

	"github.com/heridotlife/honryu/internal/domain/report"
)

// ShareStore persists the share links issued for run reports.
//
// A link is a bare capability (see report.ShareToken), so this store is a
// plain record of what was issued: the public fetch resolves the token,
// checks expiry against the row, and serves the run report. Multiple live
// tokens per run are allowed -- sharing a report with two customers and
// revoking one must not break the other.
type ShareStore interface {
	// CreateShare records a newly minted token for runID. createdBy may be
	// empty (no-auth mode); expires may be nil (never expires).
	CreateShare(ctx context.Context, runID int64, token, createdBy string, expires *time.Time) error
	// GetShareByToken resolves a token to its share, or ErrNotFound. An
	// expired row is still returned: expiry is fetch-time policy, not
	// storage state, so the issuing UI can keep listing the link.
	GetShareByToken(ctx context.Context, token string) (report.ShareToken, error)
	// ListSharesByRun returns a run's links in issue order (oldest first).
	ListSharesByRun(ctx context.Context, runID int64) ([]report.ShareToken, error)
	// DeleteShare removes one token. An unknown token (or one issued for a
	// different run) is ErrNotFound.
	DeleteShare(ctx context.Context, runID int64, token string) error
}
