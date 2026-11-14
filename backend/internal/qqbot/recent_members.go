package qqbot

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/lib/pq"
)

type RecentMember struct {
	GroupID        string    `json:"group_id"`
	MemberID       string    `json:"member_id"`
	Nickname       string    `json:"nickname"`
	Email          string    `json:"email"`
	MessagePreview string    `json:"message_preview"`
	LastSeenAt     time.Time `json:"last_seen_at"`
}

func memberHintText(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len([]rune(text)) > max {
		return limitText(text, max-1)
	}
	return text
}

// Keep only the sender's short, plain chat text for operator identification.
// Commands, quotes and attachments must not copy codes or referenced content.
func groupMemberHint(e Event, customCommands ...CustomCommand) (nickname, preview string) {
	if e.Author.Bot || e.Author.MemberID == "" {
		return "", ""
	}
	nickname = memberHintText(e.Author.Username, 100)
	if e.referenceID() != "" || e.MessageType == 103 || !shareableGroupEvent(e, nil, customCommands...) {
		return nickname, ""
	}
	text := strings.TrimSpace(mentionPrefix.ReplaceAllString(strings.TrimSpace(e.Content), ""))
	return nickname, memberHintText(text, 100)
}

func (s *Service) recentMembers(ctx context.Context, c Config) ([]RecentMember, error) {
	out := []RecentMember{}
	// Bound the read before deduplication; notification/member events are not speakers.
	rows, err := s.db.QueryContext(ctx, `WITH recent AS (
 SELECT group_id,member_id,message_id,member_nickname,message_preview,created_at,id
 FROM qq_bot_jobs WHERE app_id=$1 AND group_id=ANY($2) AND member_id<>''
 AND event_type IN ('GROUP_AT_MESSAGE_CREATE','GROUP_MESSAGE_CREATE')
 AND created_at>NOW()-INTERVAL '24 hours' ORDER BY created_at DESC,id DESC LIMIT 200
), members AS (
 SELECT DISTINCT ON (group_id,member_id) * FROM recent ORDER BY group_id,member_id,created_at DESC,id DESC
)
SELECT m.group_id,m.member_id,m.member_nickname,m.message_preview,m.created_at,
 COALESCE(b.nickname,''),COALESCE(u.email,''),COALESCE(h.content,'')
FROM members m
LEFT JOIN qq_bot_bindings b ON b.app_id=$1 AND b.member_id=m.member_id
LEFT JOIN users u ON u.id=b.user_id AND u.deleted_at IS NULL AND u.status='active'
LEFT JOIN qq_bot_group_messages h ON h.app_id=$1 AND h.group_id=m.group_id AND h.message_key='user:'||m.message_id AND h.role='user'
ORDER BY m.created_at DESC,m.id DESC LIMIT 50`, c.AppID, pq.Array(c.AllowedGroups))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var member RecentMember
		var boundName, history string
		if err := rows.Scan(&member.GroupID, &member.MemberID, &member.Nickname, &member.MessagePreview, &member.LastSeenAt, &boundName, &member.Email, &history); err != nil {
			return nil, err
		}
		member.MessagePreview = memberDirectoryPreview(member.MessagePreview, c.CustomCommands...)
		// Already-retained history can identify pre-upgrade jobs without backfilling payloads.
		var entry groupHistoryEntry
		if json.Unmarshal([]byte(history), &entry) == nil && entry.Speaker == groupSpeaker(c.AppID, member.GroupID, member.MemberID) {
			if member.Nickname == "" {
				member.Nickname = memberHintText(entry.Nickname, 100)
			}
			if member.MessagePreview == "" && entry.ReplyTo == "" {
				member.MessagePreview = memberDirectoryPreview(entry.Text, c.CustomCommands...)
			}
		}
		if member.Nickname == "" {
			member.Nickname = memberHintText(boundName, 100)
		}
		out = append(out, member)
	}
	return out, rows.Err()
}
