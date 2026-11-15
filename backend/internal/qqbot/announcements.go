package qqbot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const announcementEvent = "MANUAL_ANNOUNCEMENT"

var (
	ErrAnnouncementInvalid  = errors.New("公告参数不正确")
	ErrAnnouncementConflict = errors.New("公告请求已变更，请重新加载配置后再发送")
	announcementRequestID   = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)
	announcementMentionTag  = regexp.MustCompile(`(?i)<\s*/?\s*qqbot[-_]|<\s*@|@everyone\b`)
)

type AnnouncementRequest struct {
	AppID      string   `json:"app_id"`
	Sandbox    bool     `json:"sandbox"`
	Groups     []string `json:"groups"`
	Text       string   `json:"text"`
	MentionAll bool     `json:"mention_all"`
	RequestID  string   `json:"request_id"`
}

type AnnouncementJob struct {
	ID      string `json:"id"`
	GroupID string `json:"group_id"`
}

type AnnouncementResult struct {
	BatchID   string            `json:"batch_id"`
	Jobs      []AnnouncementJob `json:"jobs"`
	Duplicate bool              `json:"duplicate"`
}

func normalizeAnnouncement(req AnnouncementRequest) (AnnouncementRequest, error) {
	invalid := func(message string) (AnnouncementRequest, error) {
		return req, fmt.Errorf("%w：%s", ErrAnnouncementInvalid, message)
	}
	if !announcementRequestID.MatchString(req.RequestID) {
		return invalid("发送标识无效，请刷新页面后重试")
	}
	req.RequestID = strings.ToLower(req.RequestID)
	if req.AppID == "" || len(req.AppID) > 128 || strings.ContainsFunc(req.AppID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return invalid("机器人 AppID 无效")
	}
	req.Text = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(req.Text, "\r\n", "\n"), "\r", "\n"))
	if !utf8.ValidString(req.Text) || utf8.RuneCountInString(req.Text) < 1 || utf8.RuneCountInString(req.Text) > 1600 {
		return invalid("公告正文须为 1～1600 字")
	}
	if announcementMentionTag.MatchString(req.Text) {
		return invalid("请使用“@所有人”开关，不要在正文填写 QQ 提及标签")
	}
	if len(req.Groups) < 1 || len(req.Groups) > 100 {
		return invalid("请明确选择 1～100 个接收公告的群")
	}
	req.Groups = slices.Clone(req.Groups)
	for _, group := range req.Groups {
		if group == "" || len(group) > 128 || strings.ContainsFunc(group, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return invalid("接收群 OpenID 无效")
		}
	}
	slices.Sort(req.Groups)
	req.Groups = slices.Compact(req.Groups)
	return req, nil
}

// Queue the entire batch atomically. The request hash lives in event_key so
// finishing a job (which erases its payload) cannot invalidate deduplication.
// Existing job tombstones retain this protection for 30 days.
func (s *Service) SendAnnouncement(ctx context.Context, req AnnouncementRequest) (AnnouncementResult, error) {
	out := AnnouncementResult{Jobs: []AnnouncementJob{}}
	req, err := normalizeAnnouncement(req)
	if err != nil {
		return out, err
	}
	out.BatchID = req.RequestID
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	c, err := s.lockConfig(ctx, tx)
	if err != nil {
		return out, err
	}
	if c.AppID != req.AppID || c.Sandbox != req.Sandbox {
		return out, ErrAnnouncementConflict
	}
	if !c.Enabled || c.AppSecret == "" {
		return out, ErrDisabled
	}
	for _, group := range req.Groups {
		if !c.allows(group) || c.groupPolicy(group).DisableNotifications {
			return out, fmt.Errorf("%w：接收群不在允许名单中或已关闭通知，请重新选择", ErrAnnouncementInvalid)
		}
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,71427110))`, c.AppID+":"+req.RequestID); err != nil {
		return out, err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	hash := sha256.Sum256(raw)
	prefix := "announcement:" + req.RequestID + ":"
	batchKey := prefix + hex.EncodeToString(hash[:]) + ":"
	rows, err := tx.QueryContext(ctx, `SELECT id,group_id,event_key FROM qq_bot_jobs WHERE app_id=$1 AND event_type=$2 AND event_key LIKE $3 ORDER BY event_key`, c.AppID, announcementEvent, prefix+"%")
	if err != nil {
		return out, err
	}
	conflict := false
	for rows.Next() {
		var job AnnouncementJob
		var key string
		if err = rows.Scan(&job.ID, &job.GroupID, &key); err != nil {
			break
		}
		if key != fmt.Sprintf("%s%03d", batchKey, len(out.Jobs)) {
			conflict = true
		}
		out.Jobs = append(out.Jobs, job)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Jobs) > 0 {
		groups := make([]string, 0, len(out.Jobs))
		for _, job := range out.Jobs {
			groups = append(groups, job.GroupID)
		}
		if conflict || !slices.Equal(groups, req.Groups) {
			return out, ErrAnnouncementConflict
		}
		out.Duplicate = true
		return out, tx.Commit()
	}
	for i, group := range req.Groups {
		job := AnnouncementJob{ID: randomID(), GroupID: group}
		_, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,payload,kind) VALUES($1,$2,$3,$4,$5,$6,'announcement')`, job.ID, c.AppID, fmt.Sprintf("%s%03d", batchKey, i), announcementEvent, group, string(raw))
		if err != nil {
			return out, err
		}
		out.Jobs = append(out.Jobs, job)
	}
	return out, tx.Commit()
}

func (s *Service) deliverAnnouncement(parent context.Context, c Config, j Job) {
	if !c.Enabled || c.AppID != j.AppID || !c.allows(j.GroupID) || c.groupPolicy(j.GroupID).DisableNotifications {
		s.finish(j, "cancelled", "机器人已关闭或接收群通知权限已变更")
		return
	}
	if time.Since(j.CreatedAt) > time.Hour {
		s.finish(j, "expired", "公告排队超过 1 小时，未发送过期公告")
		return
	}
	var req AnnouncementRequest
	if json.Unmarshal(j.Payload, &req) != nil {
		s.finish(j, "cancelled", "公告内容无效")
		return
	}
	req, err := normalizeAnnouncement(req)
	if err != nil || req.AppID != c.AppID || req.Sandbox != c.Sandbox || !slices.Contains(req.Groups, j.GroupID) {
		s.finish(j, "cancelled", "公告内容无效或 QQ 环境已变更")
		return
	}
	text := req.Text
	if req.MentionAll {
		text = "<qqbot-at-everyone />\n" + text
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	token, err := s.token(ctx, c)
	if err == nil {
		err = s.sendAnnouncementWithToken(ctx, c, j, req, token, text)
	}
	if errors.Is(err, ErrAnnouncementConflict) {
		s.finish(j, "cancelled", "机器人或接收群通知配置已变更")
		return
	}
	if err != nil {
		s.finish(j, "delivery_failed", "公告发送失败："+err.Error())
		return
	}
	s.finish(j, "completed", "")
}

func (s *Service) sendAnnouncementWithToken(ctx context.Context, previous Config, j Job, req AnnouncementRequest, token, text string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.lockConfig(ctx, tx)
	if err != nil {
		return err
	}
	// Token refresh can wait on the network. Authorize again afterwards and keep
	// the configuration stable until the actual send returns (at most 20s).
	if !current.Enabled || current.AppID != req.AppID || current.Sandbox != req.Sandbox || current.AppSecret != previous.AppSecret || !current.allows(j.GroupID) || current.groupPolicy(j.GroupID).DisableNotifications {
		return ErrAnnouncementConflict
	}
	// Manual sends intentionally bypass automatic-notification quiet hours.
	// No passive message IDs, AI calls, or automatic retries are involved.
	_, err = s.qqRequestMethodWithToken(ctx, current, token, http.MethodPost, "/v2/groups/"+url.PathEscape(j.GroupID)+"/messages", map[string]any{
		"msg_type": 2, "markdown": map[string]string{"content": text},
	})
	return err
}
