package service

import (
	"context"
	"time"
)

type openAISameAccountRetryKey struct{}

type openAISameAccountRetry struct {
	accountID int64
	deadline  time.Time
}

// WithOpenAISameAccountRetry pins the actual sender for a same-account retry.
// Passing nil clears the constraint when an ordinary counted switch is allowed.
func WithOpenAISameAccountRetry(ctx context.Context, accountID int64, failure *UpstreamFailoverError) context.Context {
	retry := openAISameAccountRetry{}
	if failure != nil && failure.RetryableOnSameAccount {
		retry.accountID = accountID
		retry.deadline = failure.SameAccountRetryDeadline
	}
	return context.WithValue(ctx, openAISameAccountRetryKey{}, retry)
}

func openAISameAccountRetryFromContext(ctx context.Context) openAISameAccountRetry {
	if ctx == nil {
		return openAISameAccountRetry{}
	}
	retry, _ := ctx.Value(openAISameAccountRetryKey{}).(openAISameAccountRetry)
	return retry
}

// OpenAISameAccountRetryAdmissible is rechecked after slot waits so an expired
// retry cannot send, and a changed selection cannot escape its actual owner.
func OpenAISameAccountRetryAdmissible(ctx context.Context, accountID int64) bool {
	retry := openAISameAccountRetryFromContext(ctx)
	return (ctx == nil || ctx.Err() == nil) && (retry.accountID <= 0 ||
		(retry.accountID == accountID && (retry.deadline.IsZero() || time.Now().Before(retry.deadline))))
}
