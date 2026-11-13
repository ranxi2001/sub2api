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
		Used       *float64
		Seconds    int64
		ResetAfter *int64
		ResetAt    *int64
		malformed  bool
	}
	type limit struct {
		Primary   json.RawMessage `json:"primary_window"`
		Secondary json.RawMessage `json:"secondary_window"`
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
	decodeWindow := func(raw json.RawMessage) *window {
		var fields map[string]json.RawMessage
		if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &fields) != nil {
			return nil
		}
		w := &window{}
		// The identity is decoded separately, so malformed usage/reset fields
		// in a known weekly slot remain an invalidating weekly observation.
		_ = json.Unmarshal(fields["limit_window_seconds"], &w.Seconds)
		if value, exists := fields["used_percent"]; exists {
			if json.Unmarshal(value, &w.Used) != nil {
				w.Used = nil
				w.malformed = true
			}
		}
		for key, target := range map[string]**int64{"reset_after_seconds": &w.ResetAfter, "reset_at": &w.ResetAt} {
			if value, exists := fields[key]; exists {
				if string(value) == "null" || json.Unmarshal(value, target) != nil {
					w.malformed = true
				}
			}
		}
		return w
	}
	windows := []*window{decodeWindow(selected.Primary), decodeWindow(selected.Secondary)}
	// Keep permanent-policy evidence independent of unrelated window parsing.
	// It is still persisted atomically with the normal scheduling snapshot.
	weekly := make(map[string]any)
	weeklyCount := 0
	for _, w := range windows {
		if w == nil || w.Seconds != 7*24*60*60 {
			continue
		}
		weeklyCount++
		if weeklyCount > 1 || w.malformed || w.Used == nil || !validOpenAI429Percent(*w.Used) ||
			(w.ResetAfter != nil && (*w.ResetAfter <= 0 || *w.ResetAfter > 7*24*60*60)) ||
			(w.ResetAt != nil && !time.Unix(*w.ResetAt, 0).After(now)) {
			weekly = map[string]any{openAI429WeeklyInvalidAtKey: now.UTC().Format(time.RFC3339Nano)}
			break
		}
		evidence := openAI429WeeklyEvidence(*w.Used, now, nil)
		if w.ResetAt != nil {
			evidence["reset_at"] = time.Unix(*w.ResetAt, 0).UTC().Format(time.RFC3339Nano)
		} else if w.ResetAfter != nil {
			evidence["reset_at"] = now.Add(time.Duration(*w.ResetAfter) * time.Second).UTC().Format(time.RFC3339Nano)
		}
		weekly[openAI429WeeklyEvidenceKey] = evidence
	}
	finish := func(updates map[string]any) map[string]any {
		if updates == nil {
			updates = make(map[string]any)
		}
		for key, value := range weekly {
			updates[key] = value
		}
		if len(updates) == 0 {
			return nil
		}
		updates["codex_usage_updated_at"] = now.UTC().Format(time.RFC3339Nano)
		return updates
	}
	updates := make(map[string]any)
	for _, w := range windows {
		if w == nil || w.Used == nil {
			continue
		}
		if w.malformed || math.IsNaN(*w.Used) || math.IsInf(*w.Used, 0) || *w.Used < 0 {
			return finish(nil)
		}
		var name string
		switch w.Seconds {
		case 5 * 60 * 60:
			name = "5h"
		case 7 * 24 * 60 * 60:
			name = "7d"
		default:
			return finish(nil)
		}
		prefix := "codex_" + name
		if _, exists := updates[prefix+"_used_percent"]; exists {
			return finish(nil)
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
	return finish(updates)
}
