package service

import (
	"strconv"
	"time"
)

const GrokRateLimitHistoryExtraKey = "grok_rate_limit_history"

// Recovery generations may advance while the cooldown boundary stays fixed.
// Use the separately retained duration only for that exact generation/boundary;
// legacy rows and external clears/re-arms retain their existing interpretation.
func grokHistoricalCooldown(account *Account) time.Duration {
	legacy := account.RateLimitResetAt.Sub(*account.RateLimitedAt)
	history, ok := account.Extra[GrokRateLimitHistoryExtraKey].(map[string]any)
	if !ok || history["generation_us"] != strconv.FormatInt(account.RateLimitedAt.UnixMicro(), 10) ||
		history["reset_us"] != strconv.FormatInt(account.RateLimitResetAt.UnixMicro(), 10) {
		return legacy
	}
	raw, ok := history["cooldown_us"].(string)
	if !ok {
		return legacy
	}
	micros, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || micros <= 0 || micros > int64((1<<63-1)/time.Microsecond) {
		return legacy
	}
	return time.Duration(micros) * time.Microsecond
}
