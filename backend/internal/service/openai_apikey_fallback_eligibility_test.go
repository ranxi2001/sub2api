//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIAPIKeyFallbackOAuthAvailability(t *testing.T) {
	now := time.Now()
	future, past := now.Add(time.Hour), now.Add(-time.Hour)
	for _, tc := range []struct {
		name   string
		mutate func(*Account)
		want   bool
	}{
		{name: "active without quota evidence", want: true},
		{name: "error", mutate: func(a *Account) { a.Status = StatusError }},
		{name: "inactive", mutate: func(a *Account) { a.Status = StatusDisabled }},
		{name: "manual pause", mutate: func(a *Account) { a.Schedulable = false }},
		{name: "performance pause", mutate: func(a *Account) { a.PerformancePaused = true }},
		{name: "expiry", mutate: func(a *Account) { a.AutoPauseOnExpired = true; a.ExpiresAt = &past }},
		{name: "overload", mutate: func(a *Account) { a.OverloadUntil = &future }},
		{name: "rate limited", mutate: func(a *Account) { a.RateLimitResetAt = &future }},
		{name: "temporary cooldown", mutate: func(a *Account) { a.TempUnschedulableUntil = &future }},
		{name: "elapsed cooldown", mutate: func(a *Account) { a.TempUnschedulableUntil = &past }, want: true},
		{name: "API key", mutate: func(a *Account) { a.Type = AccountTypeAPIKey }},
		{name: "shadow", mutate: func(a *Account) { id := int64(2); a.ParentAccountID = &id }},
		{name: "weekly 100 percent alone is not a scheduling block", mutate: func(a *Account) { a.Extra["codex_7d_used_percent"] = 100.0 }, want: true},
		{name: "five hour 100 percent alone is not a scheduling block", mutate: func(a *Account) { a.Extra["codex_5h_used_percent"] = 100.0 }, want: true},
		{name: "weekly window reset", mutate: func(a *Account) {
			a.Extra["codex_7d_used_percent"] = 100.0
			a.Extra["codex_7d_reset_at"] = past.Format(time.RFC3339Nano)
		}, want: true},
		{name: "available quota", mutate: func(a *Account) { a.Extra["codex_7d_used_percent"] = 50.0 }, want: true},
		{name: "configured weekly auto pause", mutate: func(a *Account) { a.Extra["codex_7d_used_percent"] = 95.0; a.Extra["auto_pause_7d_threshold"] = 0.9 }},
		{name: "disabled weekly auto pause", mutate: func(a *Account) {
			a.Extra["codex_7d_used_percent"] = 95.0
			a.Extra["auto_pause_7d_threshold"] = 0.9
			a.Extra["auto_pause_7d_disabled"] = true
		}, want: true},
		{name: "account scheduling threshold", mutate: func(a *Account) {
			a.Extra["codex_7d_used_percent"] = 95.0
			a.Extra["codex_7d_reset_at"] = future.Format(time.RFC3339Nano)
			a.Credentials = map[string]any{accountSchedulingThresholdCredentialKey: 90}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{"codex_usage_updated_at": now.Format(time.RFC3339Nano)}}
			if tc.mutate != nil {
				tc.mutate(account)
			}
			require.Equal(t, tc.want, openAIAPIKeyFallbackOAuthAvailable(t.Context(), account, nil, now))
		})
	}
}
