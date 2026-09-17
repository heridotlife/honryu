package account

import "context"

// ctxKey is the private type for principal-carrying request contexts.
type ctxKey struct{}

// WithContext returns a context carrying the authenticated account. The
// inbound adapter stashes the principal it authenticated here; the use-case
// layer reads it back with FromContext when a write needs to name its actor
// (e.g. a scenario version's created_by). No principal is the zero Account,
// and FromContext answers that honestly rather than with a fabricated one.
func WithContext(ctx context.Context, acct Account) context.Context {
	return context.WithValue(ctx, ctxKey{}, acct)
}

// FromContext returns the authenticated account stored by WithContext, or
// the anonymous (zero) account when none is present.
func FromContext(ctx context.Context) Account {
	acct, _ := ctx.Value(ctxKey{}).(Account)
	return acct
}
