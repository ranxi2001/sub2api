package service

import "context"

type preserveAccountStatusKey struct{}

// WithPreservedAccountStatus marks a settings save that does not own status or
// error_message. The repository must retain the current database values rather
// than write the editor's snapshot over a concurrent authentication transition.
func WithPreservedAccountStatus(ctx context.Context) context.Context {
	return withAccountStatusPreservation(ctx, true)
}

func withAccountStatusPreservation(ctx context.Context, preserve bool) context.Context {
	return context.WithValue(ctx, preserveAccountStatusKey{}, preserve)
}

// PreserveAccountStatus reports whether this save omitted an explicit status.
func PreserveAccountStatus(ctx context.Context) bool {
	preserve, _ := ctx.Value(preserveAccountStatusKey{}).(bool)
	return preserve
}

type accountExtraUpdateFieldsKey struct{}

// Capture request intent before a service fills omitted extra from its snapshot.
// Each request replaces inherited intent, including requests with nil extra.
func withAccountExtraUpdateFields(ctx context.Context, extra map[string]any) context.Context {
	provided := make(map[string]bool, 3)
	for _, key := range []string{AccountCostMultiplierExtraKey, AccountCostAutoSyncExtraKey, AccountProbePenaltyExtraKey} {
		_, provided[key] = extra[key]
	}
	return context.WithValue(ctx, accountExtraUpdateFieldsKey{}, provided)
}

// AccountExtraUpdateFieldProvided distinguishes explicit request fields from a
// materialized Account snapshot. Without request scope, internal full-Account
// writers retain their existing key-presence contract.
func AccountExtraUpdateFieldProvided(ctx context.Context, key string) (provided, scoped bool) {
	fields, scoped := ctx.Value(accountExtraUpdateFieldsKey{}).(map[string]bool)
	return fields[key], scoped
}
