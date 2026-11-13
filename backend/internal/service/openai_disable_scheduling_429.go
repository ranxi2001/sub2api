package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	OpenAIDisableSchedulingOn429ExtraKey                  = "openai_disable_scheduling_on_429"
	OpenAIDisableSchedulingOn4297dThresholdExtraKey       = "openai_disable_scheduling_on_429_7d_threshold_percent"
	DefaultOpenAIDisableSchedulingOn4297dThresholdPercent = 98.0
	openAI429WeeklyInvalidAtKey                           = "codex_7d_permanent_disable_invalid_at"
	openAI429WeeklyEvidenceKey                            = "codex_7d_permanent_disable_evidence"
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
	return openAI429WeeklyParsedHeaderUsage(headers, ParseCodexRateLimitHeaders(headers))
}

func openAI429WeeklyParsedHeaderUsage(headers http.Header, snapshot *OpenAICodexUsageSnapshot) (used float64, present, valid bool) {
	// A permanent pause must not rely on Normalize's legacy primary=7d
	// assumption. Require exactly one explicitly identified weekly window.
	prefix := ""
	for _, window := range []string{"primary", "secondary"} {
		values := headers.Values("x-codex-" + window + "-window-minutes")
		claimsWeekly := false
		// Inspect every value so a conflicting duplicate cannot hide a weekly
		// claim behind Header.Get's first value (including comma-joined fields).
		for _, value := range values {
			for _, part := range strings.Split(value, ",") {
				if minutes, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && minutes == 7*24*60 {
					claimsWeekly = true
				}
			}
		}
		if !claimsWeekly {
			// An unused, malformed or shorter neighbor says nothing about an
			// independently identified weekly window.
			continue
		}
		if prefix != "" || len(values) != 1 {
			return 0, true, false
		}
		if minutes, err := strconv.Atoi(values[0]); err != nil || minutes != 7*24*60 {
			return 0, true, false
		}
		prefix = "x-codex-" + window + "-"
	}
	if prefix == "" {
		// Nonweekly/unidentified data is not a weekly observation. It neither
		// supplies a weekly percentage nor vetoes independently valid cache.
		return 0, false, false
	}
	if snapshot == nil {
		return 0, true, false
	}
	weekly := snapshot.PrimaryUsedPercent
	if prefix == "x-codex-secondary-" {
		weekly = snapshot.SecondaryUsedPercent
	}
	if len(headers.Values(prefix+"used-percent")) != 1 || weekly == nil || !validOpenAI429Percent(*weekly) {
		return 0, true, false
	}
	if values, exists := headers[http.CanonicalHeaderKey(prefix+"reset-after-seconds")]; exists {
		if len(values) != 1 {
			return 0, true, false
		}
		seconds, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || seconds <= 0 || seconds > int64((7*24*time.Hour)/time.Second) {
			return 0, true, false
		}
	}
	return *weekly, true, true
}

func openAI429WeeklyUsage(account *Account, headers http.Header, now time.Time, observedAt ...time.Time) (float64, bool) {
	if used, present, valid := openAI429WeeklyHeaderUsage(headers); present {
		if valid && len(observedAt) > 0 {
			observed := observedAt[0]
			if observed.IsZero() || observed.After(now) || now.Sub(observed) >= openAICodexAutoPauseStaleAfter {
				return 0, false
			}
			for _, window := range []string{"primary", "secondary"} {
				prefix := "x-codex-" + window + "-"
				minutes, _ := strconv.Atoi(headers.Get(prefix + "window-minutes"))
				if minutes == 7*24*60 && len(headers.Values(prefix+"reset-after-seconds")) > 0 {
					seconds, _ := strconv.ParseInt(headers.Get(prefix+"reset-after-seconds"), 10, 64)
					if !observed.Add(time.Duration(seconds) * time.Second).After(now) {
						return 0, false
					}
				}
			}
		}
		return used, valid
	}
	if !openAICodexSnapshotIdentityTrusted(account) {
		return 0, false
	}
	extra := account.Extra
	if raw, exists := extra[openAI429WeeklyEvidenceKey]; exists {
		evidence, ok := raw.(map[string]any)
		if !ok {
			return 0, false
		}
		// The complete validated observation is one versioned value. Legacy
		// normalization of nonweekly packets cannot overwrite its identity.
		proofExtra := map[string]any{
			"codex_7d_used_percent":   evidence["used_percent"],
			"codex_7d_window_minutes": evidence["window_minutes"],
			"codex_usage_updated_at":  evidence["observed_at"],
		}
		if reset, exists := evidence["reset_after_seconds"]; exists {
			proofExtra["codex_7d_reset_after_seconds"] = reset
		}
		if reset, exists := evidence["reset_at"]; exists {
			proofExtra["codex_7d_reset_at"] = reset
		}
		if invalidAt, exists := extra[openAI429WeeklyInvalidAtKey]; exists {
			proofExtra[openAI429WeeklyInvalidAtKey] = invalidAt
		}
		if rawVersions, exists := extra[CodexUsageFieldVersionsKey]; exists {
			versions, ok := rawVersions.(map[string]any)
			stamp, err := strconv.ParseInt(fmt.Sprint(versions[openAI429WeeklyEvidenceKey]), 10, 64)
			if !ok || err != nil || stamp <= 0 || !codexUpdatesTime(proofExtra).Equal(time.Unix(0, stamp)) {
				return 0, false
			}
			proofExtra[CodexUsageFieldVersionsKey] = map[string]any{
				"codex_7d_used_percent": stamp, "codex_7d_window_minutes": stamp,
				"codex_7d_reset_after_seconds": stamp,
			}
		}
		extra = proofExtra
	}
	// A normalized legacy primary snapshot may have no window identity. Its
	// "7d" field name alone is not evidence for a permanent scheduling change.
	minutes, ok := resolveAccountExtraNumber(extra, "codex_7d_window_minutes")
	if !ok || minutes != 7*24*60 {
		return 0, false
	}
	used, ok := resolveAccountExtraNumber(extra, "codex_7d_used_percent")
	if !ok || !validOpenAI429Percent(used) {
		return 0, false
	}
	observed := codexUpdatesTime(extra)
	windowObserved := observed
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
		stamp, err = strconv.ParseInt(fmt.Sprint(fields["codex_7d_window_minutes"]), 10, 64)
		if err != nil || stamp <= 0 {
			return 0, false
		}
		windowObserved = time.Unix(0, stamp)
		// Partial normalized packets can otherwise pair an unidentified
		// primary percentage with another observation's explicit weekly window.
		if !windowObserved.Equal(observed) {
			return 0, false
		}
	}
	// Permanent pauses require a known, recent observation, unlike temporary
	// scheduling guards that may retain legacy snapshots without timestamps.
	for _, observation := range []time.Time{observed, windowObserved} {
		if observation.IsZero() || observation.After(now) || now.Sub(observation) >= openAICodexAutoPauseStaleAfter {
			return 0, false
		}
	}
	// Header.Get/Normalize can retain a conflicting packet's first value for
	// ordinary quota display. That packet must never become permanent evidence.
	if raw, exists := extra[openAI429WeeklyInvalidAtKey]; exists {
		invalidAt, err := time.Parse(time.RFC3339Nano, fmt.Sprint(raw))
		if err != nil || !invalidAt.Before(observed) {
			return 0, false
		}
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

func openAI429WeeklyEvidence(used float64, observed time.Time, reset *int) map[string]any {
	evidence := map[string]any{"used_percent": used, "window_minutes": 7 * 24 * 60, "observed_at": observed.Format(time.RFC3339Nano)}
	if reset != nil {
		evidence["reset_after_seconds"] = *reset
	}
	return evidence
}

func openAI429SnapshotWeeklyEvidence(snapshot *OpenAICodexUsageSnapshot, observed time.Time) map[string]any {
	var evidence map[string]any
	for _, window := range []struct {
		minutes, reset *int
		used           *float64
	}{
		{snapshot.PrimaryWindowMinutes, snapshot.PrimaryResetAfterSeconds, snapshot.PrimaryUsedPercent},
		{snapshot.SecondaryWindowMinutes, snapshot.SecondaryResetAfterSeconds, snapshot.SecondaryUsedPercent},
	} {
		if window.minutes == nil || *window.minutes != 7*24*60 {
			continue
		}
		if evidence != nil || window.used == nil || !validOpenAI429Percent(*window.used) ||
			(window.reset != nil && (*window.reset <= 0 || *window.reset > 7*24*60*60)) {
			return nil
		}
		evidence = openAI429WeeklyEvidence(*window.used, observed, window.reset)
	}
	return evidence
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

// Stream cooldowns intentionally discard ordinary-model handshake quota. The
// opt-in permanent weekly policy still needs that observation, including lows
// and invalid evidence that must prevent fallback to an older high snapshot.
type openAI429WeeklyHeadersKey struct{}
type openAI429WeeklyObservedAtKey struct{}

// Bind the receipt time before any body buffering/streaming. Downstream
// failover helpers read the same request context as direct stream side effects.
func bindOpenAI429ResponseObservation(ctx context.Context, resp *http.Response, c *gin.Context) context.Context {
	observed := time.Now()
	if resp != nil && resp.Request != nil {
		if stamp, ok := resp.Request.Context().Value(openAI429WeeklyObservedAtKey{}).(time.Time); ok {
			observed = stamp
		}
	}
	ctx = context.WithValue(ctx, openAI429WeeklyObservedAtKey{}, observed)
	if c != nil && c.Request != nil {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), openAI429WeeklyObservedAtKey{}, observed))
	}
	return ctx
}

func openAI429LeaseObservation(ctx context.Context, lease *openAIWSConnLease) context.Context {
	var observed time.Time
	if lease != nil && lease.conn != nil {
		observed = lease.conn.createdAt()
	}
	return context.WithValue(ctx, openAI429WeeklyObservedAtKey{}, observed)
}

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
	if evidence, ok := ctx.Value(openAI429WeeklyHeadersKey{}).(http.Header); ok {
		headers = evidence
	}
	var observedAt []time.Time
	if observed, ok := ctx.Value(openAI429WeeklyObservedAtKey{}).(time.Time); ok {
		observedAt = []time.Time{observed}
	}
	used, usageValid := openAI429WeeklyUsage(owner, headers, time.Now(), observedAt...)
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
