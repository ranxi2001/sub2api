//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyFallbackChecksSavedPolicyAndAccountEligibility(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		stale, disabled, unselected     bool
		errorAccount, expired, oauth    bool
		otherPlatform, shadow, wantOpen bool
	}{
		{name: "selected active API key", wantOpen: true},
		{name: "stale revision", stale: true},
		{name: "feature disabled", disabled: true},
		{name: "selection removed", unselected: true},
		{name: "account in error", errorAccount: true},
		{name: "expired account", expired: true},
		{name: "OAuth account", oauth: true},
		{name: "another platform", otherPlatform: true},
		{name: "shadow account", shadow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tx := testEntTx(t)
			client := tx.Client()
			repo := newAccountRepositoryWithSQL(client, tx, nil)
			account := &service.Account{
				Name: "apikey-fallback-policy", Platform: service.PlatformOpenAI,
				Type: service.AccountTypeAPIKey, Status: service.StatusActive,
				Credentials: map[string]any{"api_key": "fixture-only", "keep": "credential"},
				Extra:       map[string]any{"keep": "metadata"}, ErrorMessage: "retained diagnostic",
			}
			if tc.errorAccount {
				account.Status = service.StatusError
			}
			if tc.oauth {
				account.Type = service.AccountTypeOAuth
			}
			if tc.otherPlatform {
				account.Platform = service.PlatformAnthropic
			}
			if tc.shadow {
				parent := mustCreateAccount(t, client, &service.Account{Name: "fallback-parent"})
				account.ParentAccountID = &parent.ID
				account.QuotaDimension = service.QuotaDimensionSpark
			}
			account = mustCreateAccount(t, client, account)
			now := time.Now().UTC().Truncate(time.Microsecond)
			future := now.Add(time.Hour)
			expires := now.Add(2 * time.Hour)
			if tc.expired {
				expires = now.Add(-time.Hour)
			}
			require.NoError(t, client.Account.UpdateOneID(account.ID).
				SetSchedulable(false).SetExpiresAt(expires).
				SetRateLimitedAt(now).SetRateLimitResetAt(future).
				SetOverloadUntil(future).SetTempUnschedulableUntil(future).
				Exec(ctx))
			before, err := client.Account.Get(ctx, account.ID)
			require.NoError(t, err)
			cfg := service.DefaultOAuthAutoConfig()
			cfg.Revision = "current-fallback-revision"
			cfg.OpenAIAPIKeyFallback.Enabled = !tc.disabled
			cfg.OpenAIAPIKeyFallback.AccountIDs = []int64{account.ID}
			if tc.unselected {
				cfg.OpenAIAPIKeyFallback.AccountIDs = []int64{account.ID + 1}
			}
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			_, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES($1,$2)
			 ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, service.SettingKeyOAuthAutoConfig, string(raw))
			require.NoError(t, err)
			revision := cfg.Revision
			if tc.stale {
				revision = "outdated-fallback-revision"
			}
			changed, err := repo.EnableOpenAIAPIKeyFallback(ctx, account.ID, revision)
			require.NoError(t, err)
			require.Equal(t, tc.wantOpen, changed)
			if tc.wantOpen {
				changed, err = repo.EnableOpenAIAPIKeyFallback(ctx, account.ID, revision)
				require.NoError(t, err)
				require.False(t, changed, "repeated evaluation must be idempotent")
			}
			after, err := client.Account.Get(ctx, account.ID)
			require.NoError(t, err)
			require.Equal(t, tc.wantOpen, after.Schedulable)
			require.Equal(t, before.Status, after.Status)
			require.Equal(t, before.Credentials, after.Credentials)
			require.Equal(t, before.Extra, after.Extra)
			require.Equal(t, before.ErrorMessage, after.ErrorMessage)
			require.Equal(t, before.ExpiresAt, after.ExpiresAt)
			require.Equal(t, before.RateLimitedAt, after.RateLimitedAt)
			require.Equal(t, before.RateLimitResetAt, after.RateLimitResetAt)
			require.Equal(t, before.OverloadUntil, after.OverloadUntil)
			require.Equal(t, before.TempUnschedulableUntil, after.TempUnschedulableUntil)
			var events int
			require.NoError(t, scanSingleRow(ctx, tx,
				`SELECT COUNT(*) FROM scheduler_outbox WHERE event_type=$1 AND account_id=$2`,
				[]any{service.SchedulerOutboxEventAccountChanged, account.ID}, &events))
			wantEvents := 0
			if tc.wantOpen {
				wantEvents = 1
			}
			require.Equal(t, wantEvents, events)
		})
	}
}
