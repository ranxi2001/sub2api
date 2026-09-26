package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// Capture the generation before the upstream request. A later read would allow
// an older successful probe to clear a newer 429. Never recover unrelated state.
type openAIQuotaRecoveryObservation struct {
	accountID int64
	limitedAt time.Time
	resetAt   time.Time
}

type openAIQuotaRecoveryRepository interface {
	ClearOpenAIRateLimitIfObserved(context.Context, int64, time.Time, time.Time) (bool, error)
}

func observeOpenAIQuotaRecovery(account *Account, now time.Time) *openAIQuotaRecoveryObservation {
	if account == nil || !account.IsOpenAIOAuth() || account.IsShadow() || account.ID <= 0 ||
		account.RateLimitedAt == nil || account.RateLimitResetAt == nil ||
		account.RateLimitedAt.After(now) || !account.RateLimitResetAt.After(now) {
		return nil
	}
	return &openAIQuotaRecoveryObservation{account.ID, *account.RateLimitedAt, *account.RateLimitResetAt}
}

// Only a complete, newly observed zero-usage snapshot is evidence of a reset.
// Do not combine partial responses with cached Extra or UI-derived zeroes.
func recoverOpenAIQuotaRateLimit(ctx context.Context, repo AccountRepository, observed *openAIQuotaRecoveryObservation, updates map[string]any) {
	if observed == nil {
		return
	}
	for _, key := range []string{"codex_5h_used_percent", "codex_7d_used_percent"} {
		used, ok := updates[key].(float64)
		if !ok || used != 0 {
			return
		}
	}
	updated, ok := updates["codex_usage_updated_at"].(string)
	if !ok {
		return
	}
	updatedAt, err := time.Parse(time.RFC3339, updated)
	now := time.Now()
	if err != nil || !updatedAt.After(observed.limitedAt) || updatedAt.After(now) || now.Sub(updatedAt) > openAIProbeCacheTTL {
		return
	}
	recoveryRepo, ok := repo.(openAIQuotaRecoveryRepository)
	if !ok {
		return
	}
	cleared, err := recoveryRepo.ClearOpenAIRateLimitIfObserved(ctx, observed.accountID, observed.limitedAt, observed.resetAt)
	if err != nil {
		slog.Warn("openai_quota_recovery_failed", "account_id", observed.accountID, "error", err)
	} else if cleared {
		slog.Info("openai_quota_rate_limit_recovered", "account_id", observed.accountID)
	}
}

// Decode presence explicitly: the display DTO's zero values cannot distinguish
// an upstream zero from an omitted/null used_percent or limit_reached field.
func openAIQuotaRecoveryUsageUpdates(body []byte, now time.Time) map[string]any {
	type window struct {
		UsedPercent *float64 `json:"used_percent"`
		Seconds     int64    `json:"limit_window_seconds"`
	}
	var payload struct {
		RateLimit *struct {
			Allowed      *bool   `json:"allowed"`
			LimitReached *bool   `json:"limit_reached"`
			Primary      *window `json:"primary_window"`
			Secondary    *window `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.RateLimit == nil {
		return nil
	}
	limit := payload.RateLimit
	if limit.Allowed == nil || !*limit.Allowed || limit.LimitReached == nil || *limit.LimitReached {
		return nil
	}
	updates := map[string]any{"codex_usage_updated_at": now.UTC().Format(time.RFC3339)}
	for _, w := range []*window{limit.Primary, limit.Secondary} {
		if w == nil || w.UsedPercent == nil || *w.UsedPercent != 0 {
			return nil
		}
		switch w.Seconds {
		case 5 * 60 * 60:
			updates["codex_5h_used_percent"] = *w.UsedPercent
		case 7 * 24 * 60 * 60:
			updates["codex_7d_used_percent"] = *w.UsedPercent
		default:
			return nil
		}
	}
	if len(updates) != 3 {
		return nil
	}
	return updates
}
