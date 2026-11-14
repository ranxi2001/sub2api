package service

import "context"

type recoveredContextPrivacyKey struct{}

// Recovered text is evaluated by normal policies, but cannot be copied to
// audit bodies, excerpts or penalty evidence with a different retention period.
func WithRecoveredContextPrivacy(ctx context.Context) context.Context {
	return context.WithValue(ctx, recoveredContextPrivacyKey{}, true)
}

func HasRecoveredContextPrivacy(ctx context.Context) bool {
	value, _ := ctx.Value(recoveredContextPrivacyKey{}).(bool)
	return value
}
