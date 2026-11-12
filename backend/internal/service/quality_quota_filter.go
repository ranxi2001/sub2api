package service

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"
)

// QualityTemplateQuotaAvailable refines the normal-only template selection with
// known OAuth quota availability. Explicit other statuses (and all statuses)
// preserve their existing probe/recovery intent. It never probes the provider.
func QualityTemplateQuotaAvailable(filter QualityRuleAccountFilter, account *Account, now time.Time) bool {
	if len(filter.Statuses) != 1 || filter.Statuses[0] != StatusActive || account == nil || !account.IsOpenAIOAuth() {
		return true
	}
	if account.RateLimitResetAt != nil && now.Before(*account.RateLimitResetAt) {
		return false
	}
	if !openAICodexSnapshotIdentityTrusted(account) {
		return true
	}
	// Unlike a persisted cooldown, an undated/invalid observation cannot prove
	// exhaustion. Reuse scheduling's reset and stale-window handling below.
	for _, window := range []string{"5h", "7d"} {
		extra := qualityQuotaWindowObservation(account.Extra, window, now)
		used, known := resolveOpenAIQuotaUtilization(extra, window, now)
		if known && !math.IsNaN(used) && !math.IsInf(used, 0) && used >= 1 {
			return false
		}
	}
	return true
}

// Coalesced snapshots retain each field's original time. A newer 5h packet
// must not refresh old weekly exhaustion or slide its relative reset forward.
func qualityQuotaWindowObservation(extra map[string]any, window string, now time.Time) map[string]any {
	prefix := "codex_" + window + "_"
	observed := func(key string) (time.Time, bool) {
		if raw, exists := extra[CodexUsageFieldVersionsKey]; exists {
			fields, ok := raw.(map[string]any)
			if !ok {
				return time.Time{}, false
			}
			stamp, err := strconv.ParseInt(fmt.Sprint(fields[key]), 10, 64)
			if err != nil || stamp <= 0 {
				return time.Time{}, false
			}
			at := time.Unix(0, stamp)
			return at, !at.After(now)
		}
		at, err := parseTime(fmt.Sprint(extra["codex_usage_updated_at"]))
		return at, err == nil && !at.After(now)
	}
	at, ok := observed(prefix + "used_percent")
	if !ok {
		return nil
	}
	out := map[string]any{prefix + "used_percent": extra[prefix+"used_percent"], "codex_usage_updated_at": at.Format(time.RFC3339Nano)}
	if raw, exists := extra[prefix+"reset_at"]; exists {
		reset, err := parseTime(fmt.Sprint(raw))
		if err != nil {
			return nil
		}
		out[prefix+"reset_at"] = reset.Format(time.RFC3339Nano)
	} else if raw, exists := extra[prefix+"reset_after_seconds"]; exists {
		resetObserved, valid := observed(prefix + "reset_after_seconds")
		seconds, err := strconv.ParseInt(fmt.Sprint(raw), 10, 64)
		if !valid || err != nil || seconds < 0 || seconds > int64((7*24*time.Hour)/time.Second) {
			return nil
		}
		out[prefix+"reset_at"] = resetObserved.Add(time.Duration(seconds) * time.Second).Format(time.RFC3339Nano)
	}
	return out
}

func (s *ScheduledTestRunnerService) qualityTemplateQuotaAvailable(ctx context.Context, plan *ScheduledTestPlan) (bool, error) {
	// Explicit runs and upstream-error protection retain their existing semantics.
	if plan.PelicanConfig == nil || plan.PelicanConfig.Quality == nil || plan.ManualQualityRun || plan.TriggerSource != "" || s.scheduledSvc == nil || s.scheduledSvc.templateRepo == nil {
		return true, nil
	}
	template, err := s.scheduledSvc.templateRepo.ForPlan(ctx, plan.ID)
	if err != nil || template == nil {
		return err == nil, err
	}
	if len(template.AccountFilter.Statuses) != 1 || template.AccountFilter.Statuses[0] != StatusActive {
		return true, nil
	}
	if s.accountTestSvc == nil || s.accountTestSvc.accountRepo == nil {
		return false, fmt.Errorf("quality template account lookup unavailable")
	}
	account, err := s.accountTestSvc.accountRepo.GetByID(ctx, plan.AccountID)
	if err != nil {
		return false, err
	}
	return account != nil && QualityTemplateQuotaAvailable(template.AccountFilter, account, time.Now()), nil
}

// A quota skip is not an interrupted quality round: preserve the 401 streak
// captured by ClaimPelican while releasing exactly the lease we acquired.
type QualityQuotaSkipRepository interface {
	FinishQuotaSkippedPelican(context.Context, *ScheduledTestPlan, time.Time, time.Time) error
}
