package httpapi

import (
	"context"

	"github.com/heridotlife/honryu/internal/domain/account"
)

// withAccount returns a context carrying the authenticated account. The
// carrier itself lives in the account domain (account.WithContext) so the
// use-case layer can read the principal for actor attribution (e.g. a
// scenario version's created_by) without importing this adapter.
func withAccount(ctx context.Context, acct account.Account) context.Context {
	return account.WithContext(ctx, acct)
}

// accountFrom returns the authenticated account stored by the auth middleware,
// or the anonymous (zero) account when none is present.
func accountFrom(ctx context.Context) account.Account {
	return account.FromContext(ctx)
}
