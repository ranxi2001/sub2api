package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"
)

const (
	OpenAIDisableSchedulingOn429ExtraKey                  = "openai_disable_scheduling_on_429"
	OpenAIDisableSchedulingOn4297dThresholdExtraKey       = "openai_disable_scheduling_on_429_7d_threshold_percent"
	DefaultOpenAIDisableSchedulingOn4297dThresholdPercent = 98.0
)

func openAI429WeeklyThreshold(account *Account) (float64, bool) {
	if _, exists := account.Extra[OpenAIDisableSchedulingOn4297dThresholdExtraKey]; !exists {
		return DefaultOpenAIDisableSchedulingOn4297dThresholdPercent, true
	}
	threshold, ok := resolveAccountExtraNumber(account.Extra, OpenAIDisableSchedulingOn4297dThresholdExtraKey)
	return threshold, ok && validOpenAI429Percent(threshold)
}

func validOpenAI429Percent(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 100
}

// Read this response directly: persistence happens later and may be throttled.
// A present but invalid/low weekly observation must never fall back to an older
// high snapshot. The five-hour window does not participate in this policy.
func openAI429WeeklyHeaderUsage(headers http.Header) (used float64, present, valid bool) {
	hasObservation := false
	for _, window := range []string{"primary", "secondary"} {
		for _, field := range []string{"used-percent", "window-minutes", "reset-after-seconds"} {
			_, exists := headers[http.CanonicalHeaderKey("x-codex-"+window+"-"+field)]
			hasObservation = hasObservation || exists
		}
	}
	if !hasObservation {
		return 0, false, false
	}
	snapshot := ParseCodexRateLimitHeaders(headers)
	if snapshot == nil {
		return 0, true, false
	}
	// A permanent pause must not rely on Normalize's legacy primary=7d
	// assumption. Require exactly one explicitly identified weekly window.
	prefix := ""
	for _, window := range []string{"primary", "secondary"} {
		if key := "x-codex-" + window + "-window-minutes"; len(headers.Values(key)) > 0 {
			minutes, err := strconv.Atoi(headers.Get(key))
			if err != nil || minutes <= 0 {
				return 0, true, false
			}
			if minutes == 7*24*60 {
				if prefix != "" {
					return 0, true, false
				}
				prefix = "x-codex-" + window + "-"
			}
		}
	}
	if prefix == "" {
		return 0, true, false
	}
	weekly := snapshot.PrimaryUsedPercent
	if prefix == "x-codex-secondary-" {
		weekly = snapshot.SecondaryUsedPercent
	}
	if weekly == nil || !validOpenAI429Percent(*weekly) {
		return 0, true, false
	}
	if key := prefix + "reset-after-seconds"; len(headers.Values(key)) > 0 {
		seconds, err := strconv.ParseInt(headers.Get(key), 10, 64)
		if err != nil || seconds <= 0 || seconds > int64((7*24*time.Hour)/time.Second) {
			return 0, true, false
		}
	}
	return *weekly, true, true
}

func openAI429WeeklyUsage(account *Account, headers http.Header, now time.Time) (float64, bool) {
	if used, present, valid := openAI429WeeklyHeaderUsage(headers); present {
		return used, valid
	}
	if !openAICodexSnapshotIdentityTrusted(account) {
		return 0, false
	}
	extra := account.Extra
	if _, exists := extra["codex_7d_window_minutes"]; exists {
		minutes, ok := resolveAccountExtraNumber(extra, "codex_7d_window_minutes")
		if !ok || minutes != 7*24*60 {
			return 0, false
		}
	}
	used, ok := resolveAccountExtraNumber(extra, "codex_7d_used_percent")
	if !ok || !validOpenAI429Percent(used) {
		return 0, false
	}
	observed := codexUpdatesTime(extra)
	if versions, exists := extra[CodexUsageFieldVersionsKey]; exists {
		fields, ok := versions.(map[string]any)
		if !ok {
			return 0, false
		}
		stamp, err := strconv.ParseInt(fmt.Sprint(fields["codex_7d_used_percent"]), 10, 64)
		if err != nil || stamp <= 0 {
			return 0, false
		}
		observed = time.Unix(0, stamp)
	}
	// Permanent pauses require a known, recent observation, unlike temporary
	// scheduling guards that may retain legacy snapshots without timestamps.
	if observed.IsZero() || observed.After(now) || now.Sub(observed) >= openAICodexAutoPauseStaleAfter {
		return 0, false
	}
	if raw, exists := extra["codex_7d_reset_at"]; exists {
		reset, err := parseTime(fmt.Sprint(raw))
		if err != nil || !reset.After(now) {
			return 0, false
		}
	} else if raw, exists := extra["codex_7d_reset_after_seconds"]; exists {
		seconds, err := strconv.ParseInt(fmt.Sprint(raw), 10, 64)
		if err != nil || seconds <= 0 || seconds > int64((7*24*time.Hour)/time.Second) {
			return 0, false
		}
		resetObserved := codexUpdatesTime(extra)
		if fields, ok := extra[CodexUsageFieldVersionsKey].(map[string]any); ok {
			stamp, err := strconv.ParseInt(fmt.Sprint(fields["codex_7d_reset_after_seconds"]), 10, 64)
			if err != nil || stamp <= 0 {
				return 0, false
			}
			resetObserved = time.Unix(0, stamp)
		}
		if resetObserved.IsZero() || resetObserved.After(now) || !resetObserved.Add(time.Duration(seconds)*time.Second).After(now) {
			return 0, false
		}
	}
	return used, true
}

func openAIDisableSchedulingOn429Enabled(account *Account) bool {
	// Evaluate the persisted owner, not a managed connection's projected type.
	if account == nil || account.ID <= 0 || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth ||
		account.IsShadow() {
		return false
	}
	enabled, _ := account.Extra[OpenAIDisableSchedulingOn429ExtraKey].(bool)
	return enabled
}

type openAIDisableScheduling429ObservedKey struct{}

type openAIDisableScheduling429Observation struct {
	accountID int64
	disabled  bool
}

// Persist the normal scheduling switch, rather than a temporary cooldown. A
// successful test or an elapsed 429 window must not reopen the account. The
// context marker shares the result across gateway and common error handlers.
func (s *RateLimitService) maybeDisableOpenAISchedulingOn429(ctx context.Context, account *Account, status int, headers http.Header) (context.Context, bool) {
	if s == nil || s.accountRepo == nil || status != http.StatusTooManyRequests || account == nil ||
		account.ID <= 0 || account.Platform != PlatformOpenAI || account.IsShadow() {
		return ctx, false
	}
	if observed, ok := ctx.Value(openAIDisableScheduling429ObservedKey{}).(openAIDisableScheduling429Observation); ok && observed.accountID == account.ID {
		return ctx, observed.disabled
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	// A projected api_key connection can belong to an OAuth account. Conversely,
	// a projected OAuth type/Extra must not opt an API Key owner into this policy.
	// Resolve the owning account authoritatively, retaining one observation even
	// on failure so the common handler does not repeat the read for this error.
	ctx = context.WithValue(ctx, openAIDisableScheduling429ObservedKey{}, openAIDisableScheduling429Observation{accountID: account.ID})
	owner := account
	if account.AuthConnectionID != "" {
		if !account.HasManagedOpenAIConnections() {
			slog.Warn("openai_429_disable_scheduling_owner_unverified", "account_id", account.ID)
			return ctx, false
		}
		var err error
		owner, err = s.accountRepo.GetByID(stateCtx, account.ID)
		if err != nil || owner == nil || owner.ID != account.ID {
			slog.Warn("openai_429_disable_scheduling_owner_read_failed", "account_id", account.ID)
			return ctx, false
		}
	}
	if !openAIDisableSchedulingOn429Enabled(owner) {
		return ctx, false
	}
	threshold, thresholdValid := openAI429WeeklyThreshold(owner)
	used, usageValid := openAI429WeeklyUsage(owner, headers, time.Now())
	if !thresholdValid || !usageValid || used <= threshold {
		return ctx, false
	}
	err := s.accountRepo.SetSchedulable(stateCtx, owner.ID, false)
	if err != nil {
		// Never log an upstream response, request body or credential here.
		slog.Warn("openai_429_disable_scheduling_failed", "account_id", account.ID)
	} else {
		// SetSchedulable refreshes the shared scheduler snapshot; bridge stale
		// in-flight candidates immediately and stop same-account 429 retries.
		s.notifyAccountSchedulingBlocked(owner, time.Time{}, "openai_429_scheduling_disabled")
		slog.Info("openai_429_scheduling_disabled", "account_id", account.ID, "weekly_used_percent", used, "threshold_percent", threshold)
	}
	ctx = context.WithValue(ctx, openAIDisableScheduling429ObservedKey{}, openAIDisableScheduling429Observation{accountID: account.ID, disabled: err == nil})
	return ctx, err == nil
}
