package qqbot

import (
	"context"
	"log/slog"
)

func isMembershipEvent(eventType string) bool {
	switch eventType {
	case "GROUP_JOIN_REQUEST", "GROUP_MEMBER_ADD", "GROUP_MEMBER_REMOVE":
		return true
	}
	return false
}

// Keep membership diagnostics after the job payload is erased, without logging
// callback bodies, account details, credentials or raw QQ identifiers. Reasons
// and outcomes must be static values supplied by the event processing code.
func logMembershipEvent(ctx context.Context, j Job, e *Event, stage, outcome, reason string) {
	if !isMembershipEvent(j.EventType) {
		return
	}
	attrs := []slog.Attr{
		slog.String("component", "qqbot.membership"),
		slog.String("event_type", j.EventType),
		slog.String("stage", stage),
		slog.String("outcome", outcome),
		slog.String("reason", reason),
	}
	if j.ID != "" {
		attrs = append(attrs, slog.String("job_id", j.ID))
	}
	for _, ref := range []struct{ name, value string }{
		{"app_ref", j.AppID}, {"group_ref", j.GroupID}, {"member_ref", j.MemberID},
	} {
		if ref.value != "" {
			attrs = append(attrs, slog.String(ref.name, codeHash(j.AppID + "\x00" + ref.value)[:12]))
		}
	}
	if e != nil && j.EventType == "GROUP_JOIN_REQUEST" {
		source := "other"
		switch e.ApplySource {
		case "":
			source = "missing"
		case "invited", "self_apply":
			source = e.ApplySource
		}
		attrs = append(attrs, slog.String("apply_source", source), slog.Bool("has_inviter", e.InvitedBy != ""))
	}
	level := slog.LevelInfo
	if outcome == "failed" {
		level = slog.LevelWarn
	}
	slog.LogAttrs(ctx, level, "QQ bot member event", attrs...)
}
