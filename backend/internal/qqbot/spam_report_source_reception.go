package qqbot

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"
)

const spamSourceReceptionLogInterval = 30 * time.Second
const spamSourceReceptionLogLimit = 512

type spamSourceReceptionDiagnostics struct {
	mu      sync.Mutex
	entries map[string]spamSourceReceptionLogEntry
}

type spamSourceReceptionLogEntry struct {
	last       time.Time
	suppressed uint64
}

// Keys contain only a scoped hash, a known event type and a fixed reason. Neither
// traffic volume nor arbitrary message identifiers can grow this map unbounded.
func (d *spamSourceReceptionDiagnostics) allow(key string, now time.Time) (uint64, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if entry, ok := d.entries[key]; ok {
		if now.Before(entry.last.Add(spamSourceReceptionLogInterval)) {
			entry.suppressed++
			d.entries[key] = entry
			return 0, false
		}
		d.entries[key] = spamSourceReceptionLogEntry{last: now}
		return entry.suppressed, true
	}
	if len(d.entries) >= spamSourceReceptionLogLimit {
		for key, entry := range d.entries {
			if !now.Before(entry.last.Add(spamSourceReceptionLogInterval)) {
				delete(d.entries, key)
			}
		}
		if len(d.entries) >= spamSourceReceptionLogLimit {
			return 0, false
		}
	}
	if d.entries == nil {
		d.entries = make(map[string]spamSourceReceptionLogEntry)
	}
	d.entries[key] = spamSourceReceptionLogEntry{last: now}
	return 0, true
}

// Use a secret-keyed hash with an independent diagnostic domain. Message IDs and
// indexes are group scoped; msg_idx and ref_msg_idx deliberately share the index
// domain so operators can compare a source callback with a later report.
func spamSourceReceptionRef(c Config, group, field, value string) string {
	if c.AppSecret == "" || value == "" {
		return ""
	}
	raw, _ := json.Marshal([]any{"qqbot-spam-source-reception-v1", c.AppID, c.Sandbox, group, field, value})
	mac := hmac.New(sha256.New, []byte(c.AppSecret))
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))[:16]
}

// Accept only fixed reason codes, never error text or callback-controlled values.
func spamSourceReceptionDetails(reason string) (string, slog.Level) {
	switch reason {
	case "message_id_missing", "message_id_too_long", "member_id_missing",
		"member_id_too_long", "member_role_too_long", "timestamp_missing",
		"timestamp_type_invalid", "timestamp_empty", "timestamp_invalid",
		"timestamp_expired", "timestamp_future", "scope_changed", "report_disabled":
		return "dropped", slog.LevelInfo
	case "transaction_begin_failed", "config_read_failed", "group_lock_failed",
		"encryption_unavailable", "encryption_failed", "previous_source_read_failed",
		"fingerprint_encode_failed", "source_write_failed", "source_prune_failed", "transaction_commit_failed":
		return "failed", slog.LevelWarn
	case "original_stored", "mention_observed", "callback_conflict_stored",
		"legacy_provenance_stored", "bot_source_stored", "member_role_missing_stored",
		"protected_role_stored", "member_role_unknown_stored", "exempt_source_stored", "ineligible_source_stored":
		return "stored", slog.LevelDebug
	default:
		return "", slog.LevelInfo
	}
}

func spamSourceReceptionStoredReason(c Config, e Event, fingerprint spamReportCallbackHash, eligible bool) string {
	if eligible {
		return "original_stored"
	}
	if fingerprint.Kind == "at" {
		return "mention_observed"
	}
	switch fingerprint.Reason {
	case "callback_conflict":
		return "callback_conflict_stored"
	case "legacy_provenance":
		return "legacy_provenance_stored"
	}
	switch {
	case e.Author.Bot:
		return "bot_source_stored"
	case e.Author.MemberRole == "":
		return "member_role_missing_stored"
	case e.Author.MemberRole == "admin" || e.Author.MemberRole == "owner":
		return "protected_role_stored"
	case e.Author.MemberRole != "member":
		return "member_role_unknown_stored"
	case c.moderationExempt(e.GroupID, e.Author.MemberID):
		return "exempt_source_stored"
	default:
		return "ineligible_source_stored"
	}
}

func (s *Service) logSpamSourceReception(ctx context.Context, c Config, e Event, eventType, reason string) {
	if !isGroupMessageEvent(eventType) || !c.reportsSpam(e.GroupID) {
		return
	}
	outcome, level := spamSourceReceptionDetails(reason)
	if outcome == "" || !slog.Default().Enabled(ctx, level) {
		return
	}
	groupRef := spamSourceReceptionRef(c, "", "group", e.GroupID)
	suppressed, ok := s.spamSourceDiagnostics.allow(groupRef+"\x00"+eventType+"\x00"+reason, time.Now())
	if !ok {
		return
	}
	attrs := []slog.Attr{
		slog.String("component", "qqbot.spam_source"),
		slog.String("event_type", eventType),
		slog.String("stage", "receive"),
		slog.String("outcome", outcome),
		slog.String("reason", reason),
		slog.Uint64("suppressed", suppressed),
		slog.Bool("refs_available", c.AppSecret != ""),
	}
	for _, ref := range []struct{ name, group, field, value string }{
		{"app_ref", "", "app", c.AppID},
		{"env_ref", "", "env", strconv.FormatBool(c.Sandbox)},
		{"group_ref", "", "group", e.GroupID},
		{"message_ref", e.GroupID, "message", e.ID},
		{"msg_idx_ref", e.GroupID, "index", e.MessageScene.value("msg_idx")},
		{"ref_msg_idx_ref", e.GroupID, "index", e.MessageScene.value("ref_msg_idx")},
		{"reference_message_ref", e.GroupID, "message", e.MessageReference.MessageID},
	} {
		if value := spamSourceReceptionRef(c, ref.group, ref.field, ref.value); value != "" {
			attrs = append(attrs, slog.String(ref.name, value))
		}
	}
	slog.LogAttrs(ctx, level, "QQ bot spam source reception", attrs...)
}
