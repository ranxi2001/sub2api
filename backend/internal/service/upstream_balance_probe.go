package service

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const (
	// upstreamBalanceProbePath is the key-scoped usage endpoint (the CC Switch
	// usage contract) every Sub2API upstream serves. It authenticates with the
	// API key the account already stores; the panel's /api/v1/user/profile would
	// need an upstream login session the account does not hold.
	upstreamBalanceProbePath = "/v1/usage"
	// /v1/usage also returns per-model and per-day statistics next to the
	// balance, so it is allowed a larger body than the billing endpoint.
	upstreamBalanceProbeTimeout          = 30 * time.Second
	upstreamBalanceProbeMaxBodyBytes     = 256 * 1024
	upstreamBalanceProbeMaxWindows       = 8
	upstreamBalanceProbeMaxPlanNameRunes = 64
	upstreamBalanceProbeMaxTokenLength   = 32
)

// UpstreamBalanceSnapshot is the upstream balance read from GET /v1/usage. It
// is stored inside UpstreamBillingProbeSnapshot, so every path that drops the
// probe snapshot when the account's key, base URL or proxy changes drops the
// balance with it. Status reuses the probe status values and is tracked apart
// from the rate status because either endpoint can be missing on its own.
type UpstreamBalanceSnapshot struct {
	Status        string         `json:"status"`
	Data          map[string]any `json:"data,omitempty"`
	ReceivedAt    *time.Time     `json:"received_at,omitempty"`
	FreshUntil    *time.Time     `json:"fresh_until,omitempty"`
	LastAttemptAt time.Time      `json:"last_attempt_at"`
	HTTPStatus    int            `json:"http_status,omitempty"`
	LastError     string         `json:"last_error,omitempty"`
}

type upstreamUsageBalanceResponse struct {
	Mode         *string                           `json:"mode"`
	IsValid      *bool                             `json:"isValid"`
	Status       *string                           `json:"status"`
	PlanName     *string                           `json:"planName"`
	Remaining    *float64                          `json:"remaining"`
	Unit         *string                           `json:"unit"`
	Balance      *float64                          `json:"balance"`
	Quota        *upstreamUsageBalanceQuota        `json:"quota"`
	Subscription *upstreamUsageBalanceSubscription `json:"subscription"`
	RateLimits   []upstreamUsageBalanceRateLimit   `json:"rate_limits"`
	ExpiresAt    *string                           `json:"expires_at"`
}

type upstreamUsageBalanceQuota struct {
	Limit     *float64 `json:"limit"`
	Used      *float64 `json:"used"`
	Remaining *float64 `json:"remaining"`
}

type upstreamUsageBalanceSubscription struct {
	DailyUsageUSD   *float64 `json:"daily_usage_usd"`
	WeeklyUsageUSD  *float64 `json:"weekly_usage_usd"`
	MonthlyUsageUSD *float64 `json:"monthly_usage_usd"`
	DailyLimitUSD   *float64 `json:"daily_limit_usd"`
	WeeklyLimitUSD  *float64 `json:"weekly_limit_usd"`
	MonthlyLimitUSD *float64 `json:"monthly_limit_usd"`
	ExpiresAt       *string  `json:"expires_at"`
}

type upstreamUsageBalanceRateLimit struct {
	Window  *string  `json:"window"`
	Limit   *float64 `json:"limit"`
	Used    *float64 `json:"used"`
	ResetAt *string  `json:"reset_at"`
}

// A slow rate endpoint can time out while the separate key usage endpoint
// still answers. Other transport failures skip the second GET, as do key
// rejections and rate limits. Official APIs do not serve Sub2API key usage.
func upstreamBalanceProbeAllowed(baseURL string, rate upstreamBillingProbeHTTPResult) bool {
	if rate.statusCode == 0 && !rate.timedOut {
		return false
	}
	switch rate.statusCode {
	case http.StatusUnauthorized, http.StatusTooManyRequests:
		return false
	}
	return !upstreamBillingProbeTargetIsOfficialAPI(baseURL)
}

// upstreamBalanceProbeURL asks for the smallest statistics windows /v1/usage
// accepts, so reading a balance does not make the upstream aggregate its
// default 30 days of per-model and per-day logs. Upstreams that predate these
// parameters ignore them.
func upstreamBalanceProbeURL(baseURL string, now time.Time) string {
	endpoint := buildOpenAIEndpointURL(baseURL, upstreamBalanceProbePath)
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	day := now.UTC().Format("2006-01-02")
	query := parsed.Query()
	query.Set("days", "1")
	query.Set("start_date", day)
	query.Set("end_date", day)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// upstreamBalanceSnapshotFromResult turns one /v1/usage response into the
// balance to store. Failures keep the last successful data together with its
// original timestamps, like the rate snapshot, so readers can still tell when
// it expires.
func upstreamBalanceSnapshotFromResult(
	result upstreamBillingProbeHTTPResult,
	previous *UpstreamBalanceSnapshot,
	intervalMinutes int,
	now time.Time,
) *UpstreamBalanceSnapshot {
	reason := result.failure
	if reason == "" {
		switch {
		case result.statusCode == http.StatusNotFound || result.statusCode == http.StatusMethodNotAllowed:
			reason = "unsupported"
		case result.statusCode < 200 || result.statusCode >= 300:
			reason = "http_error"
		default:
			data, err := parseUpstreamBalanceResponse(result.body)
			if err == nil {
				return &UpstreamBalanceSnapshot{
					Status:        UpstreamBillingProbeStatusOK,
					Data:          data,
					ReceivedAt:    probeTimePtr(now),
					FreshUntil:    probeTimePtr(now.Add(2 * time.Duration(intervalMinutes) * time.Minute)),
					LastAttemptAt: now,
					HTTPStatus:    result.statusCode,
				}
			}
			reason = "invalid_response"
		}
	}
	status := UpstreamBillingProbeStatusFailed
	if reason == "unsupported" {
		status = UpstreamBillingProbeStatusUnsupported
	}
	snapshot := &UpstreamBalanceSnapshot{
		Status:        status,
		LastAttemptAt: now,
		HTTPStatus:    result.statusCode,
		LastError:     reason,
	}
	if previous != nil {
		snapshot.Data = previous.Data
		snapshot.ReceivedAt = previous.ReceivedAt
		snapshot.FreshUntil = previous.FreshUntil
	}
	return snapshot
}

// parseUpstreamBalanceResponse keeps only the balance fields of a /v1/usage
// response. Usage statistics, model lists and every unknown field are dropped,
// strings are length-limited and numbers must be finite.
//
// The response shape depends on how the upstream key is billed:
//   - wallet: remaining and balance hold the user's wallet balance;
//   - subscription: remaining is the smallest headroom left in the configured
//     daily/weekly/monthly limits, or -1 when none is configured;
//   - quota_limited: remaining is the key's own quota headroom, and rate
//     limits may add 5h/1d/7d windows.
func parseUpstreamBalanceResponse(body []byte) (map[string]any, error) {
	var response upstreamUsageBalanceResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	// isValid is part of the usage contract in every mode. Requiring it keeps an
	// arbitrary JSON object from a non-Sub2API upstream from reading as a balance.
	if response.IsValid == nil {
		return nil, fmt.Errorf("unexpected usage response schema")
	}
	data := map[string]any{"is_valid": *response.IsValid}
	if response.Mode != nil && (*response.Mode == "unrestricted" || *response.Mode == "quota_limited") {
		data["mode"] = *response.Mode
	}
	if token, ok := upstreamBalanceToken(response.Status); ok {
		data["key_status"] = token
	}
	if response.PlanName != nil {
		if planName := sanitizeUpstreamBalanceText(*response.PlanName, upstreamBalanceProbeMaxPlanNameRunes); planName != "" {
			data["plan_name"] = planName
		}
	}
	if unit, ok := upstreamBalanceToken(response.Unit); ok {
		data["unit"] = unit
	}

	remaining, hasRemaining := finiteUpstreamBalanceNumber(response.Remaining)
	if quota := response.Quota; quota != nil {
		if !hasRemaining {
			remaining, hasRemaining = finiteUpstreamBalanceNumber(quota.Remaining)
		}
		if limit, ok := finiteUpstreamBalanceNumber(quota.Limit); ok {
			data["quota_limit"] = limit
		}
		if used, ok := finiteUpstreamBalanceNumber(quota.Used); ok {
			data["quota_used"] = used
		}
	}
	if hasRemaining {
		if response.Subscription != nil && remaining == -1 {
			data["unlimited"] = true
		} else {
			data["remaining"] = remaining
		}
	}
	if balance, ok := finiteUpstreamBalanceNumber(response.Balance); ok {
		data["wallet_balance"] = balance
	}

	windows := make([]map[string]any, 0, upstreamBalanceProbeMaxWindows)
	if subscription := response.Subscription; subscription != nil {
		for _, period := range []struct {
			window string
			used   *float64
			limit  *float64
		}{
			{"daily", subscription.DailyUsageUSD, subscription.DailyLimitUSD},
			{"weekly", subscription.WeeklyUsageUSD, subscription.WeeklyLimitUSD},
			{"monthly", subscription.MonthlyUsageUSD, subscription.MonthlyLimitUSD},
		} {
			// Sub2API treats a missing or non-positive limit as not configured.
			limit, ok := finiteUpstreamBalanceNumber(period.limit)
			if !ok || limit <= 0 {
				continue
			}
			window := map[string]any{"window": period.window, "limit": limit}
			if used, ok := finiteUpstreamBalanceNumber(period.used); ok {
				window["used"] = used
			}
			windows = append(windows, window)
		}
	}
	for _, rateLimit := range response.RateLimits {
		if len(windows) == upstreamBalanceProbeMaxWindows {
			break
		}
		name, nameOK := upstreamBalanceToken(rateLimit.Window)
		limit, limitOK := finiteUpstreamBalanceNumber(rateLimit.Limit)
		if !nameOK || !limitOK || limit <= 0 {
			continue
		}
		window := map[string]any{"window": name, "limit": limit}
		if used, ok := finiteUpstreamBalanceNumber(rateLimit.Used); ok {
			window["used"] = used
		}
		if resetAt, ok := normalizeUpstreamBalanceTime(rateLimit.ResetAt); ok {
			window["reset_at"] = resetAt
		}
		windows = append(windows, window)
	}
	if len(windows) > 0 {
		data["windows"] = windows
	}

	// quota_limited reports the key's expiry at the top level; a subscription
	// reports its own expiry. The two never appear in the same response.
	if expiresAt, ok := normalizeUpstreamBalanceTime(response.ExpiresAt); ok {
		data["expires_at"] = expiresAt
	} else if response.Subscription != nil {
		if expiresAt, ok := normalizeUpstreamBalanceTime(response.Subscription.ExpiresAt); ok {
			data["expires_at"] = expiresAt
		}
	}
	return data, nil
}

func finiteUpstreamBalanceNumber(value *float64) (float64, bool) {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return 0, false
	}
	return *value, true
}

// upstreamBalanceToken accepts short identifiers such as a currency unit, a
// key status or a window name, and rejects anything else.
func upstreamBalanceToken(value *string) (string, bool) {
	if value == nil {
		return "", false
	}
	token := strings.TrimSpace(*value)
	if token == "" || len(token) > upstreamBalanceProbeMaxTokenLength {
		return "", false
	}
	for _, r := range token {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return "", false
		}
	}
	return token, true
}

// sanitizeUpstreamBalanceText keeps an upstream-chosen display name printable:
// control and format characters (including bidi overrides) are removed and the
// result is capped at maxRunes.
func sanitizeUpstreamBalanceText(value string, maxRunes int) string {
	kept := make([]rune, 0, maxRunes)
	for _, r := range strings.TrimSpace(value) {
		if r == unicode.ReplacementChar || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if len(kept) == maxRunes {
			break
		}
		kept = append(kept, r)
	}
	return strings.TrimSpace(string(kept))
}

func normalizeUpstreamBalanceTime(value *string) (string, bool) {
	if value == nil {
		return "", false
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*value))
	if err != nil || parsed.IsZero() {
		return "", false
	}
	return parsed.UTC().Format(time.RFC3339), true
}
