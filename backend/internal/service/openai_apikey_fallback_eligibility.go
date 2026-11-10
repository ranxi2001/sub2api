package service

import (
	"context"
	"time"
)

// Inspect the same persisted account and quota signals used by scheduling without
// enqueueing quota resets, changing account state or making provider requests.
func openAIAPIKeyFallbackOAuthAvailable(ctx context.Context, account *Account, settings *SettingService, now time.Time) bool {
	if account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth || account.IsShadow() || !account.IsSchedulable() {
		return false
	}
	var schedulingThresholds map[string]int
	if settings != nil {
		ctx = withOpenAIQuotaAutoPauseSettings(ctx, settings.GetOpenAIQuotaAutoPauseSettings(ctx))
		schedulingThresholds = settings.GetAccountSchedulingThresholds(ctx)
	}
	if EvaluateAccountSchedulingThreshold(account, schedulingThresholds, now).ShouldPause {
		return false
	}
	// Imported quota observations for a different identity cannot establish this
	// account's exhaustion. The existing threshold evaluator has the same guard.
	if !openAICodexSnapshotIdentityTrusted(account) {
		return true
	}
	used5h, has5h := resolveOpenAIQuotaUtilization(account.Extra, "5h", now)
	used7d, has7d := resolveOpenAIQuotaUtilization(account.Extra, "7d", now)
	pause5h, pause7d := resolveOpenAIQuotaAutoPauseThresholds(ctx, account)
	reached5h := !resolveAccountExtraBool(account.Extra, "auto_pause_5h_disabled") && pause5h > 0 && has5h && used5h >= pause5h
	reached7d := !resolveAccountExtraBool(account.Extra, "auto_pause_7d_disabled") && pause7d > 0 && has7d && used7d >= pause7d
	if config := ResolveOpenAIAutoResetCreditConfig(account); config.Enabled {
		if config.Threshold5h > 0 && has5h && used5h >= config.Threshold5h || config.Threshold7d > 0 && has7d && used7d >= config.Threshold7d {
			return false
		}
		if reached5h && config.Threshold5h == 0 || reached7d && config.Threshold7d == 0 {
			return false
		}
		if reached5h || reached7d {
			state := openAIAutoResetStateFromExtra(account.Extra)
			return state != nil && state.Status == OpenAIAutoResetStatusAvailable && state.AvailableCount > 0 && !openAIAutoResetStateStale(state, now)
		}
		return true
	}
	return !reached5h && !reached7d
}
