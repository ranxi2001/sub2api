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
