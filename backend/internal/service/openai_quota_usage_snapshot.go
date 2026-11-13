package service

import (
	"encoding/json"
	"math"
	"time"
)

// Decode presence instead of the display DTO's zero values. Missing usage,
// unknown/duplicate windows and absent reset timestamps must not invent a reset.
func openAIQuotaSchedulingUpdates(body []byte, shadow bool, expectedAccountID string, now time.Time) map[string]any {
	type window struct {
		Used       *float64 `json:"used_percent"`
		Seconds    int64    `json:"limit_window_seconds"`
		ResetAfter *int64   `json:"reset_after_seconds"`
		ResetAt    *int64   `json:"reset_at"`
	}
	type limit struct {
		Primary   *window `json:"primary_window"`
		Secondary *window `json:"secondary_window"`
	}
	var payload struct {
		AccountID  string `json:"account_id"`
		RateLimit  *limit `json:"rate_limit"`
		Additional []struct {
			Feature   string `json:"metered_feature"`
			RateLimit *limit `json:"rate_limit"`
		} `json:"additional_rate_limits"`
	}
	if json.Unmarshal(body, &payload) != nil || identityValuesConflict(expectedAccountID, payload.AccountID) {
		return nil
	}
	selected := payload.RateLimit
	if shadow {
		selected = nil
		for _, item := range payload.Additional {
			if item.Feature == "codex_bengalfox" {
				selected = item.RateLimit
				break
			}
		}
	}
	if selected == nil {
		return nil
	}
	updates := make(map[string]any)
	for _, w := range []*window{selected.Primary, selected.Secondary} {
		if w == nil || w.Used == nil {
			continue
		}
		if math.IsNaN(*w.Used) || math.IsInf(*w.Used, 0) || *w.Used < 0 {
			return nil
		}
		var name string
		switch w.Seconds {
		case 5 * 60 * 60:
			name = "5h"
		case 7 * 24 * 60 * 60:
			name = "7d"
		default:
			return nil
		}
		prefix := "codex_" + name
		if _, exists := updates[prefix+"_used_percent"]; exists {
			return nil
		}
		updates[prefix+"_used_percent"] = *w.Used
		updates[prefix+"_window_minutes"] = int(w.Seconds / 60)
		if w.ResetAt != nil && *w.ResetAt > 0 {
			updates[prefix+"_reset_at"] = time.Unix(*w.ResetAt, 0).UTC().Format(time.RFC3339)
		} else if w.ResetAfter != nil && *w.ResetAfter >= 0 {
			updates[prefix+"_reset_at"] = now.Add(time.Duration(*w.ResetAfter) * time.Second).UTC().Format(time.RFC3339)
		}
		if w.ResetAfter != nil && *w.ResetAfter >= 0 {
			updates[prefix+"_reset_after_seconds"] = int(*w.ResetAfter)
		}
	}
	if len(updates) == 0 {
		return nil
	}
	updates["codex_usage_updated_at"] = now.UTC().Format(time.RFC3339Nano)
	if used, ok := resolveAccountExtraNumber(updates, "codex_7d_used_percent"); ok && validOpenAI429Percent(used) {
		evidence := openAI429WeeklyEvidence(used, now, nil)
		if reset, exists := updates["codex_7d_reset_at"]; exists {
			evidence["reset_at"] = reset
		}
		updates[openAI429WeeklyEvidenceKey] = evidence
	}
	return updates
}
