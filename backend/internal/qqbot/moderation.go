package qqbot

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/lib/pq"
)

type ModerationExemption struct {
	GroupID  string `json:"group_id"`
	MemberID string `json:"member_id"`
	Nickname string `json:"nickname,omitempty"`
}

// Display labels never participate in member identity or exemption checks.
func normalizeModerationExemptions(exemptions []ModerationExemption) []ModerationExemption {
	out := make([]ModerationExemption, 0, len(exemptions))
	for _, exemption := range exemptions {
		exemption.GroupID = strings.TrimSpace(exemption.GroupID)
		exemption.MemberID = strings.TrimSpace(exemption.MemberID)
		exemption.Nickname = strings.TrimSpace(exemption.Nickname)
		out = append(out, exemption)
	}
	return out
}

type ModerationConfig struct {
	InheritGroups        bool                  `json:"inherit_groups"`
	Enabled              bool                  `json:"enabled"`
	ReportEnabled        bool                  `json:"report_enabled"`      // Legacy field; ignored when loading or saving.
	ReportMuteMinutes    int                   `json:"report_mute_minutes"` // Retained for historical configuration compatibility.
	Groups               []string              `json:"groups"`
	RepeatWindowSeconds  int                   `json:"repeat_window_seconds"`
	RepeatCount          int                   `json:"repeat_count"`
	BurstWindowSeconds   int                   `json:"burst_window_seconds"`
	BurstCount           int                   `json:"burst_count"`
	MuteMinutes          int                   `json:"mute_minutes"`
	RepeatOffenseMinutes int                   `json:"repeat_offense_minutes"`
	EscalatedMuteMinutes int                   `json:"escalated_mute_minutes"`
	Exemptions           []ModerationExemption `json:"exemptions"`
}

func DefaultModerationConfig() ModerationConfig {
	return ModerationConfig{Groups: []string{}, ReportMuteMinutes: 5, RepeatWindowSeconds: 15, RepeatCount: 4,
		BurstWindowSeconds: 10, BurstCount: 10, MuteMinutes: 5, RepeatOffenseMinutes: 30,
		EscalatedMuteMinutes: 30, Exemptions: []ModerationExemption{}}
}

func validateModeration(c Config) error {
	m := c.Moderation
	if m.RepeatWindowSeconds < 1 || m.RepeatWindowSeconds > 60 || m.BurstWindowSeconds < 1 || m.BurstWindowSeconds > 60 ||
		m.RepeatCount < 2 || m.RepeatCount > 100 || m.BurstCount < 2 || m.BurstCount > 100 {
		return errors.New("防刷屏统计窗口须为 1～60 秒，条数须为 2～100")
	}
	if m.MuteMinutes < 1 || m.MuteMinutes > 43200 || m.EscalatedMuteMinutes < m.MuteMinutes || m.EscalatedMuteMinutes > 43200 || m.RepeatOffenseMinutes < 1 || m.RepeatOffenseMinutes > 1440 {
		return errors.New("禁言须为 1～43200 分钟，再犯禁言不能短于首次禁言，再犯窗口须为 1～1440 分钟")
	}
	if len(m.Groups) > 100 || len(m.Exemptions) > 100 || (m.Enabled && (c.AppID == "" || (len(m.Groups) == 0 && !m.InheritGroups))) {
		return errors.New("请填写机器人 AppID 和消息治理群；群与白名单各最多 100 条")
	}
	groups := make(map[string]bool)
	if m.InheritGroups {
		for _, g := range c.AllowedGroups {
			groups[g] = true
		}
	}
	seenGroups := map[string]bool{}
	for _, g := range m.Groups {
		if !c.allows(g) || seenGroups[g] {
			return errors.New("防刷屏群须属于允许使用的群且不能重复")
		}
		groups[g] = true
		seenGroups[g] = true
	}
	seen := make(map[[2]string]bool)
	for _, e := range m.Exemptions {
		key := [2]string{e.GroupID, e.MemberID}
		if !groups[e.GroupID] || len(e.MemberID) == 0 || len(e.MemberID) > 128 || strings.ContainsFunc(e.MemberID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) || seen[key] {
			return errors.New("防刷屏白名单须填写已选择的群及有效成员 OpenID，且不能重复")
		}
		if len([]rune(e.Nickname)) > 100 || strings.ContainsFunc(e.Nickname, unicode.IsControl) {
			return errors.New("消息治理白名单备注最多 100 字且不能含控制字符")
		}
		seen[key] = true
	}
	return nil
}

func (c Config) moderates(group string) bool {
	if !c.Enabled || !c.Moderation.Enabled || !c.allows(group) || c.groupPolicy(group).DisableModeration {
		return false
	}
	if c.Moderation.InheritGroups {
		return true
	}
	for _, g := range c.Moderation.Groups {
		if g == group {
			return true
		}
	}
	return false
}

func (c Config) moderationExempt(group, member string) bool {
	for _, e := range c.Moderation.Exemptions {
		if e.GroupID == group && e.MemberID == member {
			return true
		}
	}
	for _, a := range c.GroupAdmins {
		if a.AppID == c.AppID && a.GroupID == group && a.MemberID == member {
			return true
		}
	}
	return false
}

func moderationPolicy(c Config) string {
	raw, _ := json.Marshal([]any{c.Enabled, c.AppID, c.Sandbox, c.AllowedGroups, c.Moderation, c.GroupAdmins, c.GroupPolicies})
	mac := hmac.New(sha256.New, []byte(c.AppSecret))
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

func moderationTextHash(c Config, e Event) string {
	// Attachment-only messages and forwarded/quoted message bodies must not all
	// collapse to the same empty fingerprint. They still count toward burst limits.
	if len(e.Attachments) != 0 || len(e.MsgElements) != 0 || e.MessageType != 0 {
		return ""
	}
	text := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r == '\u200b' || r == '\u200c' || r == '\u200d' || r == '\ufeff' {
			return -1
		}
		return r
	}, e.Content)), " ")
	if text == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(c.AppSecret))
	_, _ = mac.Write([]byte(c.AppID + "\x00" + e.GroupID + "\x00" + text))
	return hex.EncodeToString(mac.Sum(nil))
}

type moderationMessage struct {
	ID     string
	Hash   string
	SentAt time.Time
}

func moderationViolation(m ModerationConfig, messages []moderationMessage, hash string, now time.Time) (string, []moderationMessage) {
	var repeated, burst []moderationMessage
	for _, message := range messages {
		if message.SentAt.After(now.Add(-time.Duration(m.BurstWindowSeconds) * time.Second)) {
			burst = append(burst, message)
		}
		if hash != "" && message.Hash == hash && message.SentAt.After(now.Add(-time.Duration(m.RepeatWindowSeconds)*time.Second)) {
			repeated = append(repeated, message)
		}
	}
	// A burst may contain many different messages, so recall its entire window.
	if len(burst) >= m.BurstCount {
		return "burst", burst
	}
	if len(repeated) >= m.RepeatCount {
		return "repeat", repeated[1:]
	}
	return "", nil
}

// Called before the ambient/chat routing gates. A separate durable queue lets
// moderation run even with history and autonomous replies disabled.
func (s *Service) moderateIncoming(ctx context.Context, snapshot Config, e Event) (bool, error) {
	if !snapshot.moderates(e.GroupID) || e.Author.Bot || e.Author.MemberRole != "member" || snapshot.moderationExempt(e.GroupID, e.Author.MemberID) {
		return false, nil
	}
	if e.ID == "" || len(e.ID) > 1024 || e.Author.MemberID == "" || len(e.Author.MemberID) > 128 {
		return false, nil
	}
	var stamp string
	if json.Unmarshal(e.Timestamp, &stamp) != nil {
		return false, nil
	}
	sent, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	c, err := s.lockConfig(ctx, tx)
	if err != nil {
		return false, err
	}
	if c.AppID != snapshot.AppID || c.Sandbox != snapshot.Sandbox || !c.moderates(e.GroupID) || c.moderationExempt(e.GroupID, e.Author.MemberID) {
		return false, nil
	}
	args := []any{c.AppID, c.Sandbox, e.GroupID, e.Author.MemberID}
	if _, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_moderation_members(app_id,sandbox,group_id,member_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, args...); err != nil {
		return false, err
	}
	var cooldown, lastMuted, now time.Time
	var activeIncident string
	if err = tx.QueryRowContext(ctx, `SELECT cooldown_until,last_muted_at,active_incident_id FROM qq_bot_moderation_members WHERE app_id=$1 AND sandbox=$2 AND group_id=$3 AND member_id=$4 FOR UPDATE`, args...).Scan(&cooldown, &lastMuted, &activeIncident); err != nil {
		return false, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return false, err
	}
	var seen, alreadyBlocked bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM qq_bot_moderation_events WHERE app_id=$1 AND sandbox=$2 AND group_id=$3 AND message_id=$4), COALESCE((SELECT blocked FROM qq_bot_moderation_events WHERE app_id=$1 AND sandbox=$2 AND group_id=$3 AND message_id=$4),FALSE)`, c.AppID, c.Sandbox, e.GroupID, e.ID).Scan(&seen, &alreadyBlocked); err != nil {
		return false, err
	}
	if seen {
		return alreadyBlocked, nil
	}
	window := max(c.Moderation.RepeatWindowSeconds, c.Moderation.BurstWindowSeconds)
	if sent.Before(now.Add(-time.Duration(window)*time.Second)) || sent.After(now.Add(5*time.Second)) {
		return false, nil
	}
	if sent.After(now) {
		sent = now
	}
	policy, hash := moderationPolicy(c), moderationTextHash(c, e)
	res, err := tx.ExecContext(ctx, `INSERT INTO qq_bot_moderation_events(app_id,sandbox,group_id,member_id,message_id,policy_hash,content_hash,sent_at,blocked) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`, c.AppID, c.Sandbox, e.GroupID, e.Author.MemberID, e.ID, policy, hash, sent, cooldown.After(now))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		var blocked bool
		err = tx.QueryRowContext(ctx, `SELECT blocked FROM qq_bot_moderation_events WHERE app_id=$1 AND sandbox=$2 AND group_id=$3 AND message_id=$4`, c.AppID, c.Sandbox, e.GroupID, e.ID).Scan(&blocked)
		return blocked, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE qq_bot_moderation_members SET updated_at=$5 WHERE app_id=$1 AND sandbox=$2 AND group_id=$3 AND member_id=$4`, append(args, now)...); err != nil {
		return false, err
	}
	if cooldown.After(now) {
		// Remove new spam while the first mute is pending or failed. The member
		// lock bounds each incident to 100 recalls across service instances.
		_, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_moderation_operations(incident_id,kind,message_id,expires_at)
 SELECT id,'recall',$2,$3 FROM qq_bot_moderation_incidents WHERE id=$1 AND policy_hash=$4
 AND (SELECT COUNT(*) FROM qq_bot_moderation_operations WHERE incident_id=$1 AND kind='recall')<100
 ON CONFLICT DO NOTHING`, activeIncident, e.ID, sent.Add(115*time.Second), policy)
		if err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	// Bound each rule independently: unrelated messages in a longer repeat
	// window must not hide a new burst or a repeated text beyond the first 100.
	rows, err := tx.QueryContext(ctx, `WITH recent AS NOT MATERIALIZED (
 SELECT message_id,content_hash,sent_at,created_at FROM qq_bot_moderation_events
 WHERE app_id=$1 AND sandbox=$2 AND group_id=$3 AND member_id=$4 AND policy_hash=$5 AND NOT blocked
 ) SELECT message_id,content_hash,sent_at FROM (
 (SELECT * FROM recent WHERE sent_at>$6 ORDER BY sent_at,created_at,message_id LIMIT $7)
 UNION
 (SELECT * FROM recent WHERE content_hash=$8 AND $8<>'' AND sent_at>$9 ORDER BY sent_at,created_at,message_id LIMIT $10)
 ) candidates ORDER BY sent_at,created_at,message_id`, c.AppID, c.Sandbox, e.GroupID, e.Author.MemberID, policy,
		now.Add(-time.Duration(c.Moderation.BurstWindowSeconds)*time.Second), c.Moderation.BurstCount, hash,
		now.Add(-time.Duration(c.Moderation.RepeatWindowSeconds)*time.Second), c.Moderation.RepeatCount)
	if err != nil {
		return false, err
	}
	var messages []moderationMessage
	for rows.Next() {
		var message moderationMessage
		if err = rows.Scan(&message.ID, &message.Hash, &message.SentAt); err != nil {
			_ = rows.Close()
			return false, err
		}
		messages = append(messages, message)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return false, err
	}
	reason, recalls := moderationViolation(c.Moderation, messages, hash, now)
	if reason == "" {
		return false, tx.Commit()
	}
	minutes := c.Moderation.MuteMinutes
	if lastMuted.After(now.Add(-time.Duration(c.Moderation.RepeatOffenseMinutes) * time.Minute)) {
		minutes = c.Moderation.EscalatedMuteMinutes
	}
	id := randomID()
	if _, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_moderation_incidents(id,app_id,sandbox,group_id,member_id,policy_hash,reason,mute_seconds) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, c.AppID, c.Sandbox, e.GroupID, e.Author.MemberID, policy, reason, minutes*60); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_moderation_operations(incident_id,kind,expires_at) VALUES($1,'mute',$2)`, id, now.Add(90*time.Second)); err != nil {
		return false, err
	}
	ids := make([]string, 0, len(recalls))
	for _, message := range recalls {
		ids = append(ids, message.ID)
		if _, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_moderation_operations(incident_id,kind,message_id,expires_at) VALUES($1,'recall',$2,$3)`, id, message.ID, message.SentAt.Add(115*time.Second)); err != nil {
			return false, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE qq_bot_moderation_events SET blocked=TRUE WHERE app_id=$1 AND sandbox=$2 AND group_id=$3 AND message_id=ANY($4)`, c.AppID, c.Sandbox, e.GroupID, pq.Array(ids)); err != nil {
		return false, err
	}
	// Cancel model/command work that has not started for the recalled messages.
	if _, err = tx.ExecContext(ctx, `UPDATE qq_bot_jobs SET status='skipped',error='防刷屏拦截',finished_at=NOW(),payload='{}' WHERE app_id=$1 AND group_id=$2 AND message_id=ANY($3) AND status='queued'`, c.AppID, e.GroupID, pq.Array(ids)); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE qq_bot_moderation_members SET cooldown_until=$5,updated_at=$6,active_incident_id=$7 WHERE app_id=$1 AND sandbox=$2 AND group_id=$3 AND member_id=$4`, append(args, now.Add(time.Minute), now, id)...); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

type ModerationRecord struct {
	ID              string    `json:"id"`
	GroupID         string    `json:"group_id"`
	MemberID        string    `json:"member_id"`
	Reason          string    `json:"reason"`
	MuteSeconds     int       `json:"mute_seconds"`
	CreatedAt       time.Time `json:"created_at"`
	MuteStatus      string    `json:"mute_status"`
	RecallTotal     int       `json:"recall_total"`
	RecallSucceeded int       `json:"recall_succeeded"`
	Pending         int       `json:"pending"`
	Error           string    `json:"error"`
}

func (s *Service) moderationBlocked(ctx context.Context, c Config, group, message string) (bool, error) {
	if !c.moderates(group) || message == "" {
		return false, nil
	}
	var blocked bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM qq_bot_moderation_events WHERE app_id=$1 AND sandbox=$2 AND group_id=$3 AND message_id=$4 AND blocked)`, c.AppID, c.Sandbox, group, message).Scan(&blocked)
	return blocked, err
}

func (s *Service) moderationRecords(ctx context.Context, c Config) ([]ModerationRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT i.id,i.group_id,i.member_id,i.reason,i.mute_seconds,i.created_at,
 COALESCE(MAX(o.status) FILTER(WHERE o.kind='mute'),''),COUNT(*) FILTER(WHERE o.kind='recall'),
 COUNT(*) FILTER(WHERE o.kind='recall' AND o.status='succeeded'),COUNT(*) FILTER(WHERE o.status IN ('queued','started')),
 COALESCE(string_agg(DISTINCT NULLIF(o.error,''),'; '),'')
 FROM (SELECT * FROM qq_bot_moderation_incidents WHERE app_id=$1 AND sandbox=$2 ORDER BY created_at DESC LIMIT 50) i
 JOIN qq_bot_moderation_operations o ON o.incident_id=i.id GROUP BY i.id,i.group_id,i.member_id,i.reason,i.mute_seconds,i.created_at ORDER BY i.created_at DESC`, c.AppID, c.Sandbox)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ModerationRecord{}
	for rows.Next() {
		var r ModerationRecord
		if err = rows.Scan(&r.ID, &r.GroupID, &r.MemberID, &r.Reason, &r.MuteSeconds, &r.CreatedAt, &r.MuteStatus, &r.RecallTotal, &r.RecallSucceeded, &r.Pending, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) startModeration(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runModeration(ctx)
			}
		}
	}()
}

// Each operation is reserved durably before making an HTTP request. Unknown
// outcomes and process crashes are never replayed, including after restart.
func (s *Service) runModeration(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	c, err := s.lockConfig(ctx, tx)
	if err != nil {
		return
	}
	var id int64
	var incident, group, member, policy, kind, message, reason string
	var seconds int
	var expires time.Time
	err = tx.QueryRowContext(ctx, `SELECT o.id,o.incident_id,i.group_id,i.member_id,i.policy_hash,o.kind,o.message_id,i.mute_seconds,o.expires_at,i.reason
 FROM qq_bot_moderation_operations o JOIN qq_bot_moderation_incidents i ON i.id=o.incident_id
 LEFT JOIN qq_bot_moderation_rates r ON r.app_id=i.app_id AND r.sandbox=i.sandbox AND r.kind=o.kind
 WHERE o.status='queued' AND i.app_id=$1 AND i.sandbox=$2 AND (r.next_at IS NULL OR r.next_at<=clock_timestamp())
 AND (i.reason NOT IN ('spam_advertising','spam_phishing','spam_scam') OR o.kind<>'mute'
 OR NOT EXISTS(SELECT 1 FROM qq_bot_moderation_operations recall WHERE recall.incident_id=i.id
 AND recall.kind='recall' AND recall.status IN ('queued','started')))
 ORDER BY CASE o.kind WHEN 'mute' THEN 0 ELSE 1 END,o.id LIMIT 1 FOR UPDATE OF o SKIP LOCKED`, c.AppID, c.Sandbox).Scan(&id, &incident, &group, &member, &policy, &kind, &message, &seconds, &expires, &reason)
	if err != nil {
		return
	}
	status, detail := "started", ""
	report := reason == "spam_advertising" || reason == "spam_phishing" || reason == "spam_scam"
	allowed, expectedPolicy := c.moderates(group), moderationPolicy(c)
	if report {
		allowed, expectedPolicy = c.reportsSpam(group), spamReportPolicy(c)
	}
	if report {
		if rejection := spamReportConfigReason(c, c, group, member); rejection != spamSourceOK {
			status, detail = "cancelled", spamReportOperationCancellation(rejection, kind)
		} else if policy != expectedPolicy {
			status, detail = "cancelled", spamReportOperationCancellation(spamSourcePolicyChanged, kind)
		} else if !expires.After(time.Now()) {
			status, detail = "expired", spamReportOperationCancellation(spamReportOperationExpiryReason(kind), kind)
		}
	} else if !allowed || c.moderationExempt(group, member) || policy != expectedPolicy {
		status, detail = "cancelled", "配置或白名单已变更，取消处罚"
	} else if !expires.After(time.Now()) {
		status, detail = "expired", "已超过处罚时限"
	}
	if report && status == "started" {
		var target, targetMember, reportPolicy, reportID, reportStatus, reportReason string
		var recallSucceeded, recallSkipped bool
		err = tx.QueryRowContext(ctx, `SELECT r.message_id,r.member_id,r.policy_hash,r.id,r.status,r.reason,
 EXISTS(SELECT 1 FROM qq_bot_moderation_operations o WHERE o.incident_id=r.incident_id AND o.kind='recall' AND o.message_id=r.message_id AND o.status='succeeded'),
 EXISTS(SELECT 1 FROM qq_bot_moderation_operations o WHERE o.incident_id=r.incident_id AND o.kind='recall' AND o.message_id=r.message_id
 AND o.status='skipped' AND o.error=$5 AND o.started_at IS NULL AND o.finished_at IS NOT NULL
 AND o.expires_at<o.finished_at+INTERVAL '5 seconds')
 FROM qq_bot_spam_reports r WHERE r.incident_id=$1 AND r.app_id=$2 AND r.sandbox=$3 AND r.group_id=$4`, incident, c.AppID, c.Sandbox, group, spamReportRecallSkippedReason).Scan(&target, &targetMember, &reportPolicy, &reportID, &reportStatus, &reportReason, &recallSucceeded, &recallSkipped)
		if errors.Is(err, sql.ErrNoRows) {
			status, detail = "cancelled", spamReportOperationCancellation(spamSourceReportInvalid, kind)
		} else if err != nil {
			return // Database failures are not evidence of a revoked report.
		} else if reportStatus != "queued" {
			status, detail = "cancelled", spamReportOperationCancellation(spamReportCancellationReason(reportReason), kind)
		} else {
			source, sourceReason, sourceErr := s.inspectSpamReportSourceForReport(ctx, tx, c, group, target, reportID)
			if sourceErr != nil {
				return // Database errors must never authorize an external operation.
			}
			if source == nil {
				status, detail = "cancelled", spamReportOperationCancellation(sourceReason, kind)
			} else if source.MemberID != member || targetMember != member || (kind == "recall" && message != source.MessageID) {
				status, detail = "cancelled", spamReportOperationCancellation(spamSourceChanged, kind)
			} else if reportPolicy != policy {
				status, detail = "cancelled", spamReportOperationCancellation(spamSourcePolicyChanged, kind)
			} else if kind == "recall" && !source.SentAt.Add(spamReportRecallWindow).After(time.Now()) {
				status, detail = "expired", spamReportOperationCancellation(spamSourceRecallExpired, kind)
			} else if kind == "mute" && !recallSucceeded && !(recallSkipped && time.Until(source.SentAt.Add(spamReportRecallWindow)) < 5*time.Second) {
				status, detail = "cancelled", spamReportOperationCancellation(spamSourceRecallUnconfirmed, kind)
			}
		}
	}
	if status != "started" {
		if _, err = tx.ExecContext(ctx, `UPDATE qq_bot_moderation_operations SET status=$2,error=$3,finished_at=NOW() WHERE id=$1`, id, status, detail); err == nil {
			_ = tx.Commit()
		}
		return
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_moderation_rates(app_id,sandbox,kind) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, c.AppID, c.Sandbox, kind); err != nil {
		return
	}
	var next, now time.Time
	if err = tx.QueryRowContext(ctx, `SELECT next_at FROM qq_bot_moderation_rates WHERE app_id=$1 AND sandbox=$2 AND kind=$3 FOR UPDATE`, c.AppID, c.Sandbox, kind).Scan(&next); err != nil {
		return
	}
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil || next.After(now) {
		return
	}
	spacing := 200 * time.Millisecond
	if kind == "mute" {
		spacing = 1100 * time.Millisecond
	}
	if _, err = tx.ExecContext(ctx, `UPDATE qq_bot_moderation_rates SET next_at=$4 WHERE app_id=$1 AND sandbox=$2 AND kind=$3`, c.AppID, c.Sandbox, kind, now.Add(spacing)); err != nil {
		return
	}
	if _, err = tx.ExecContext(ctx, `UPDATE qq_bot_moderation_operations SET status='started',started_at=$2 WHERE id=$1`, id, now); err != nil {
		return
	}
	if err = tx.Commit(); err != nil {
		return
	}
	requestCtx, requestCancel := context.WithDeadline(ctx, expires)
	defer requestCancel()
	base := "/v2/groups/" + url.PathEscape(group)
	muteUntil := now.Add(time.Duration(seconds) * time.Second)
	if report {
		err = s.executeSpamReportOperation(requestCtx, c, id)
	} else if kind == "mute" {
		_, err = s.qqRequestMethod(requestCtx, c, http.MethodPost, base+"/restrict_chat_setting", map[string]any{"members": []any{map[string]string{"op": "add", "member_openid": member, "mute_expire_at": muteUntil.Format(time.RFC3339)}}})
	} else {
		_, err = s.qqRequestMethod(requestCtx, c, http.MethodDelete, base+"/messages/"+url.PathEscape(message), nil)
	}
	status, detail = "succeeded", ""
	if err != nil {
		if report {
			status, detail = "unconfirmed", "spam_report_operation_unconfirmed：举报处罚请求或最终校验未能确认完成，结果未知，未自动重试；请管理员核查执行记录。"
		} else {
			status, detail = "unconfirmed", limitText(err.Error()+"；操作未确认成功，未自动重试", 250)
		}
	}
	if errors.Is(err, errSpamReportRevoked) {
		status, detail = "cancelled", spamReportOperationCancellation(spamReportRevocationReason(err), kind)
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	finishTx, saveErr := s.db.BeginTx(finishCtx, nil)
	if saveErr != nil {
		return
	}
	defer func() { _ = finishTx.Rollback() }()
	if kind == "mute" && err == nil && !report {
		if _, saveErr = finishTx.ExecContext(finishCtx, `UPDATE qq_bot_moderation_members SET last_muted_at=GREATEST(last_muted_at,$5),cooldown_until=GREATEST(cooldown_until,$6),updated_at=$5 WHERE app_id=$1 AND sandbox=$2 AND group_id=$3 AND member_id=$4`, c.AppID, c.Sandbox, group, member, now, muteUntil); saveErr != nil {
			return
		}
	}
	if _, saveErr = finishTx.ExecContext(finishCtx, `UPDATE qq_bot_moderation_operations SET status=$2,error=$3,finished_at=NOW() WHERE id=$1 AND status='started'`, id, status, detail); saveErr != nil {
		return
	}
	_ = finishTx.Commit()
}

func (s *Service) maintainModeration(ctx context.Context) {
	_, _ = s.db.ExecContext(ctx, `UPDATE qq_bot_moderation_operations SET status='unconfirmed',error='处理进程中断，操作结果未知，未自动重试',finished_at=NOW() WHERE status='started' AND started_at<NOW()-INTERVAL '1 minute'`)
	_, _ = s.db.ExecContext(ctx, `UPDATE qq_bot_moderation_operations o SET status='expired',
 error=CASE WHEN i.reason IN ('spam_advertising','spam_phishing','spam_scam')
 THEN CASE WHEN o.kind='recall' THEN $1 ELSE $2 END ELSE '已超过处罚时限' END,finished_at=NOW()
 FROM qq_bot_moderation_incidents i WHERE i.id=o.incident_id AND o.status='queued' AND o.expires_at<NOW()`,
		spamReportOperationCancellation(spamSourceRecallExpired, "recall"), spamReportOperationCancellation(spamSourceProcessingExpired, "mute"))
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_moderation_events WHERE created_at<NOW()-INTERVAL '10 minutes'`)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_moderation_members WHERE updated_at<NOW()-INTERVAL '2 days' AND cooldown_until<NOW()`)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_moderation_incidents WHERE created_at<NOW()-INTERVAL '30 days'`)
}
