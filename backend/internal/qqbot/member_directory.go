package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
)

type MemberDirectoryFilter struct {
	AppID    string `form:"app_id"`
	GroupID  string `form:"group_id"`
	MemberID string `form:"member_id"`
	Search   string `form:"search"`
	Page     int    `form:"page"`
	PageSize int    `form:"page_size"`
}

type DirectoryMember struct {
	GroupID        string `json:"group_id"`
	MemberID       string `json:"member_id"`
	Nickname       string `json:"nickname"`
	Email          string `json:"email"`
	MessagePreview string `json:"message_preview"`
	LastSeenAt     string `json:"last_seen_at"`
	GroupLabel     string `json:"group_label"`
}

type MemberDirectoryPage struct {
	AppID    string            `json:"app_id"`
	Items    []DirectoryMember `json:"items"`
	Total    int               `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

type memberDirectoryHint struct {
	GroupID        string     `json:"group_id"`
	MemberID       string     `json:"member_id"`
	Nickname       string     `json:"nickname"`
	MessagePreview string     `json:"message_preview"`
	SeenAt         *time.Time `json:"seen_at"`
}

func validateMemberDirectoryFilter(f MemberDirectoryFilter) error {
	if f.Page < 1 || f.Page > 100000 || f.PageSize < 1 || f.PageSize > 100 {
		return errors.New("分页参数不正确")
	}
	for _, id := range []string{f.AppID, f.GroupID, f.MemberID} {
		if len(id) > 128 || strings.ContainsFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return errors.New("成员目录筛选身份不正确")
		}
	}
	if f.MemberID != "" && f.GroupID == "" {
		return errors.New("查询已选成员时必须同时指定所属群")
	}
	if len(f.Search) > 512 || len([]rune(f.Search)) > 100 || strings.ContainsFunc(f.Search, unicode.IsControl) {
		return errors.New("成员搜索内容最多 100 字且不能含控制字符")
	}
	return nil
}

func memberDirectoryPreview(text string, customCommands ...CustomCommand) string {
	// Never reintroduce private command arguments through a legacy history row.
	if privateCommandText(text, customCommands...) {
		return ""
	}
	if containsRewardCode(text) {
		return ""
	}
	return memberHintText(text, 100)
}

// History is already bounded per group by HistoryMessages. Parse its JSON in Go
// so malformed/legacy rows cannot break a listing or invent member ownership.
func (s *Service) memberDirectoryHistory(ctx context.Context, tx *sql.Tx, c Config, f MemberDirectoryFilter) ([]memberDirectoryHint, error) {
	rows, err := tx.QueryContext(ctx, `SELECT group_id,member_id,content,created_at FROM qq_bot_group_messages
 WHERE app_id=$1 AND role='user' AND member_id<>'' AND created_at>NOW()-INTERVAL '24 hours'
 AND ($2='' OR group_id=$2) AND ($3='' OR member_id=$3)
 ORDER BY created_at DESC,id DESC`, c.AppID, f.GroupID, f.MemberID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	hints := []memberDirectoryHint{}
	seen := map[[2]string]bool{}
	for rows.Next() {
		var hint memberDirectoryHint
		var raw string
		var at time.Time
		if err := rows.Scan(&hint.GroupID, &hint.MemberID, &raw, &at); err != nil {
			return nil, err
		}
		key := [2]string{hint.GroupID, hint.MemberID}
		if seen[key] {
			continue
		}
		seen[key] = true
		hint.SeenAt = &at
		var entry groupHistoryEntry
		if json.Unmarshal([]byte(raw), &entry) == nil && entry.Speaker == groupSpeaker(c.AppID, hint.GroupID, hint.MemberID) {
			hint.Nickname = memberHintText(entry.Nickname, 100)
			if entry.ReplyTo == "" {
				hint.MessagePreview = memberDirectoryPreview(entry.Text, c.CustomCommands...)
			}
		}
		hints = append(hints, hint)
	}
	return hints, rows.Err()
}

func memberDirectoryConfigured(c Config, f MemberDirectoryFilter) []memberDirectoryHint {
	hints := []memberDirectoryHint{}
	add := func(group, member, nickname string) {
		if group != "" && member != "" && (f.GroupID == "" || f.GroupID == group) && (f.MemberID == "" || f.MemberID == member) {
			hints = append(hints, memberDirectoryHint{GroupID: group, MemberID: member, Nickname: memberHintText(nickname, 100)})
		}
	}
	for _, admin := range c.GroupAdmins {
		if admin.AppID == c.AppID {
			add(admin.GroupID, admin.MemberID, admin.Nickname)
		}
	}
	for _, exemption := range c.Moderation.Exemptions {
		add(exemption.GroupID, exemption.MemberID, exemption.Nickname)
	}
	return hints
}

// Every candidate carries its actual (app, group, member) identity. Numeric QQ
// numbers, labels and emails are never translated into an OpenID. Read access
// includes previously observed groups; write-side group authorization is separate.
func (s *Service) ListMembers(ctx context.Context, f MemberDirectoryFilter) (MemberDirectoryPage, error) {
	f.Search = strings.TrimSpace(f.Search)
	out := MemberDirectoryPage{Items: []DirectoryMember{}, Page: f.Page, PageSize: f.PageSize}
	if err := validateMemberDirectoryFilter(f); err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	// Keep the application identity stable while collecting and paging hints.
	c, err := s.lockConfig(ctx, tx)
	if err != nil {
		return out, err
	}
	out.AppID = c.AppID
	if f.AppID != "" && f.AppID != c.AppID {
		return out, errors.New("机器人 AppID 已变化，请重新加载配置后选择成员")
	}
	if c.AppID == "" {
		return out, tx.Commit()
	}
	history, err := s.memberDirectoryHistory(ctx, tx, c, f)
	if err != nil {
		return out, err
	}
	historyJSON, err := json.Marshal(history)
	if err != nil {
		return out, err
	}
	configuredJSON, err := json.Marshal(memberDirectoryConfigured(c, f))
	if err != nil {
		return out, err
	}
	// No global recent-row or member cap precedes the group/search predicates.
	// The final count and page share one statement snapshot, including empty pages.
	rows, err := tx.QueryContext(ctx, `WITH job_members AS (
 SELECT DISTINCT ON(group_id,member_id) group_id,member_id,
 CASE WHEN created_at>NOW()-INTERVAL '24 hours' THEN member_nickname ELSE '' END AS nickname,
 CASE WHEN created_at>NOW()-INTERVAL '24 hours' THEN message_preview ELSE '' END AS message_preview,
 created_at AS seen_at
 FROM qq_bot_jobs WHERE app_id=$1 AND member_id<>'' AND event_type IN ('GROUP_AT_MESSAGE_CREATE','GROUP_MESSAGE_CREATE')
 AND ($2='' OR group_id=$2) AND ($3='' OR member_id=$3)
 ORDER BY group_id,member_id,created_at DESC,id DESC
), candidates AS (
 SELECT group_id,member_id,nickname,''::text AS email,message_preview,seen_at,1 AS priority FROM job_members
 UNION ALL SELECT group_id,member_id,nickname,'',message_preview,seen_at,1
 FROM jsonb_to_recordset($4::jsonb) AS h(group_id text,member_id text,nickname text,message_preview text,seen_at timestamptz)
 UNION ALL SELECT b.group_id,b.member_id,b.nickname,COALESCE(u.email,''),'',NULL::timestamptz,2
 FROM qq_bot_bindings b LEFT JOIN users u ON u.id=b.user_id AND u.deleted_at IS NULL AND u.status='active'
 WHERE b.app_id=$1 AND b.member_id<>'' AND ($2='' OR b.group_id=$2) AND ($3='' OR b.member_id=$3)
 UNION ALL SELECT group_id,member_id,nickname,'','',NULL::timestamptz,3
 FROM jsonb_to_recordset($5::jsonb) AS h(group_id text,member_id text,nickname text)
 UNION ALL SELECT DISTINCT group_id,member_id,'','','',NULL::timestamptz,4
 FROM qq_bot_moderation_incidents WHERE app_id=$1 AND member_id<>'' AND ($2='' OR group_id=$2) AND ($3='' OR member_id=$3)
), members AS (
 SELECT group_id,member_id,
 COALESCE((array_agg(nickname ORDER BY priority,seen_at DESC NULLS LAST,nickname) FILTER(WHERE nickname<>''))[1],'') AS nickname,
 COALESCE(MAX(email),'') AS email,
 COALESCE((array_agg(message_preview ORDER BY seen_at DESC NULLS LAST,priority,message_preview) FILTER(WHERE message_preview<>''))[1],'') AS message_preview,
 MAX(seen_at) AS last_seen_at
 FROM candidates GROUP BY group_id,member_id
), filtered AS (
 SELECT * FROM members WHERE $6='' OR POSITION(LOWER($6) IN LOWER(nickname))>0
 OR POSITION(LOWER($6) IN LOWER(email))>0 OR POSITION(LOWER($6) IN LOWER(message_preview))>0
)
 SELECT counted.total,paged.group_id,paged.member_id,paged.nickname,paged.email,paged.message_preview,paged.last_seen_at
 FROM (SELECT COUNT(*) AS total FROM filtered) counted LEFT JOIN LATERAL
 (SELECT * FROM filtered ORDER BY last_seen_at DESC NULLS LAST,group_id,member_id LIMIT $7 OFFSET $8) paged ON TRUE`,
		c.AppID, f.GroupID, f.MemberID, string(historyJSON), string(configuredJSON), f.Search, f.PageSize, (f.Page-1)*f.PageSize)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var group, member, nickname, email, preview sql.NullString
		var seen sql.NullTime
		if err := rows.Scan(&out.Total, &group, &member, &nickname, &email, &preview, &seen); err != nil {
			_ = rows.Close()
			return out, err
		}
		if !member.Valid {
			continue
		}
		item := DirectoryMember{GroupID: group.String, MemberID: member.String,
			Nickname: memberHintText(nickname.String, 100), Email: email.String,
			MessagePreview: memberDirectoryPreview(preview.String, c.CustomCommands...), GroupLabel: memberHintText(c.groupPolicy(group.String).Label, 60)}
		if seen.Valid {
			item.LastSeenAt = seen.Time.UTC().Format(time.RFC3339Nano)
		}
		out.Items = append(out.Items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return out, err
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
