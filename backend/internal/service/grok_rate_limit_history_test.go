//go:build unit

package service

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

func TestGrokAdaptiveCooldownRequiresMatchingHistory(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	reset, generation := now.Add(-time.Second), now.Add(-5*time.Minute)
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		want time.Duration
	}{
		{name: "matching", want: time.Hour},
		{name: "old_generation", edit: func(h map[string]any) { h["generation_us"] = "1" }, want: 10 * time.Minute},
		{name: "old_reset", edit: func(h map[string]any) { h["reset_us"] = "1" }, want: 10 * time.Minute},
		{name: "invalid_duration", edit: func(h map[string]any) { h["cooldown_us"] = "invalid" }, want: 10 * time.Minute},
		{name: "overflow", edit: func(h map[string]any) { h["cooldown_us"] = "9223372036854775807" }, want: 10 * time.Minute},
		{name: "zero", edit: func(h map[string]any) { h["cooldown_us"] = "0" }, want: 10 * time.Minute},
		{name: "legacy", edit: func(h map[string]any) { clear(h) }, want: 10 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := map[string]any{"generation_us": strconv.FormatInt(generation.UnixMicro(), 10),
				"reset_us": strconv.FormatInt(reset.UnixMicro(), 10), "cooldown_us": strconv.FormatInt((40 * time.Minute).Microseconds(), 10)}
			if tc.edit != nil {
				tc.edit(history)
			}
			account := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, RateLimitedAt: &generation,
				RateLimitResetAt: &reset, Extra: map[string]any{GrokRateLimitHistoryExtraKey: history}}
			retryAfter := 60
			next, limited := grokRateLimitResetAtForAccount(account, &xai.QuotaSnapshot{
				StatusCode: http.StatusTooManyRequests, RetryAfterSeconds: &retryAfter, UpdatedAt: now.Format(time.RFC3339Nano),
			}, now)
			require.True(t, limited)
			require.True(t, now.Add(tc.want).Equal(next))
		})
	}
}
