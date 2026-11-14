package qqbot

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"
)

var errSpamReportRevoked = errors.New("spam report authorization revoked")

const (
	spamSourceRecallExpired     spamReportSourceReason = "spam_report_recall_expired"
	spamSourceRecallUnconfirmed spamReportSourceReason = "spam_report_recall_unconfirmed"
)

// Only fixed rejection codes may cross the authorization boundary. Keep the
// sentinel in the error chain so revocation still cannot become a retry.
type spamReportRevokedError struct {
	reason spamReportSourceReason
}

func (e *spamReportRevokedError) Error() string {
	return errSpamReportRevoked.Error() + ": " + string(e.reason)
}

func (e *spamReportRevokedError) Unwrap() error { return errSpamReportRevoked }

func spamReportRevoked(reason spamReportSourceReason) error {
	if reason == spamSourceOK {
		reason = spamSourceUnverified
	}
	return &spamReportRevokedError{reason: reason}
}

func spamReportRevocationReason(err error) spamReportSourceReason {
	var revoked *spamReportRevokedError
	if errors.As(err, &revoked) {
		return revoked.reason
	}
	return spamSourceUnverified
}

func spamReportOperationExpiryReason(kind string) spamReportSourceReason {
	if kind == "recall" {
		return spamSourceRecallExpired
	}
	return spamSourceProcessingExpired
}

// A mute can be cancelled after recall already succeeded. Describe only the
// current operation; never reuse the pre-queue reply claiming no punishment.
func spamReportOperationCancellation(reason spamReportSourceReason, kind string) string {
	if reason == spamSourceOK {
		reason = spamSourceUnverified
	}
	detail := reason.explanation()
	switch reason {
	case spamSourceWhitelist:
		detail = "发送者受本群消息治理白名单保护。"
	case spamSourceChatAuthorized:
		detail = "发送者受本群聊天授权成员保护。"
	case spamSourceQQAdmin:
		detail = "原消息回调标记发送者为 QQ 群主或管理员。"
	case spamSourceBot:
		detail = "原消息回调标记发送者为机器人。"
	case spamSourceRecallExpired:
		detail = "原消息已超过安全撤回时限。"
	case spamSourceRecallUnconfirmed:
		detail = "原消息撤回未确认成功，且不符合超过安全撤回时限后仅禁言的条件。"
	}
	operation := "当前操作"
	if kind == "mute" {
		operation = "当前禁言操作"
	} else if kind == "recall" {
		operation = "当前撤回操作"
	}
	return string(reason) + "：" + detail + operation + "已取消；此前已完成的操作结果不受影响。"
}

// Token refresh can wait on the network or another worker. Only after it
// finishes do we acquire the final authorization locks. Configuration saves
// and incoming role changes cannot pass between this check and the HTTP call.
// The operation was already durably marked started, so even a crash after the
// HTTP call will never cause an automatic replay.
func (s *Service) executeSpamReportOperation(ctx context.Context, snapshot Config, operationID int64) error {
	token, err := s.token(ctx, snapshot)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	c, err := s.lockConfig(ctx, tx)
	if err != nil {
		return err
	}
	if c.AppID != snapshot.AppID || c.Sandbox != snapshot.Sandbox {
		return spamReportRevoked(spamSourcePolicyChanged)
	}
	var group, member, message, kind, policy, reportID, reportStatus, reportReason string
	var seconds int
	var expires time.Time
	var recallSucceeded, recallSkipped bool
	err = tx.QueryRowContext(ctx, `SELECT i.group_id,i.member_id,r.message_id,o.kind,i.policy_hash,i.mute_seconds,o.expires_at,r.id,r.status,r.reason,
 EXISTS(SELECT 1 FROM qq_bot_moderation_operations recall WHERE recall.incident_id=i.id AND recall.kind='recall' AND recall.message_id=r.message_id AND recall.status='succeeded'),
 EXISTS(SELECT 1 FROM qq_bot_moderation_operations recall WHERE recall.incident_id=i.id AND recall.kind='recall' AND recall.message_id=r.message_id
 AND recall.status='skipped' AND recall.error=$4 AND recall.started_at IS NULL AND recall.finished_at IS NOT NULL
 AND recall.expires_at<recall.finished_at+INTERVAL '5 seconds')
 FROM qq_bot_moderation_operations o JOIN qq_bot_moderation_incidents i ON i.id=o.incident_id
 JOIN qq_bot_spam_reports r ON r.incident_id=i.id AND r.app_id=i.app_id AND r.sandbox=i.sandbox AND r.group_id=i.group_id
 AND r.member_id=i.member_id AND r.policy_hash=i.policy_hash
 WHERE o.id=$1 AND o.status='started' AND i.app_id=$2 AND i.sandbox=$3
 AND (o.kind='mute' OR (o.kind='recall' AND o.message_id=r.message_id))
 AND i.reason IN ('spam_advertising','spam_phishing','spam_scam')`, operationID, c.AppID, c.Sandbox, spamReportRecallSkippedReason).
		Scan(&group, &member, &message, &kind, &policy, &seconds, &expires, &reportID, &reportStatus, &reportReason, &recallSucceeded, &recallSkipped)
	if errors.Is(err, sql.ErrNoRows) {
		return spamReportRevoked(spamSourceReportInvalid)
	}
	if err != nil {
		return err
	}
	if reason := spamReportConfigReason(c, snapshot, group, member); reason != spamSourceOK {
		return spamReportRevoked(reason)
	}
	if reportStatus != "queued" {
		return spamReportRevoked(spamReportCancellationReason(reportReason))
	}
	if policy != spamReportPolicy(c) {
		return spamReportRevoked(spamSourcePolicyChanged)
	}
	if !expires.After(time.Now()) {
		return spamReportRevoked(spamReportOperationExpiryReason(kind))
	}
	if kind == "mute" && !recallSucceeded && !recallSkipped {
		return spamReportRevoked(spamSourceRecallUnconfirmed)
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,306))`, c.AppID+":"+group); err != nil {
		return err
	}
	source, sourceReason, err := s.inspectSpamReportSourceForReport(ctx, tx, c, group, message, reportID)
	if err != nil {
		return err
	}
	if source == nil {
		return spamReportRevoked(sourceReason)
	}
	if source.MemberID != member {
		return spamReportRevoked(spamSourceChanged)
	}
	if !expires.After(time.Now()) {
		return spamReportRevoked(spamReportOperationExpiryReason(kind))
	}
	if kind == "recall" && !source.SentAt.Add(spamReportRecallWindow).After(time.Now()) {
		return spamReportRevoked(spamSourceRecallExpired)
	}
	if kind == "mute" && !recallSucceeded && time.Until(source.SentAt.Add(spamReportRecallWindow)) >= 5*time.Second {
		return spamReportRevoked(spamSourceRecallUnconfirmed)
	}
	base := "/v2/groups/" + url.PathEscape(group)
	var raw []byte
	if kind == "mute" {
		until := time.Now().Add(time.Duration(seconds) * time.Second).Format(time.RFC3339)
		raw, err = s.qqRequestMethodWithToken(ctx, c, token, http.MethodPost, base+"/restrict_chat_setting",
			map[string]any{"members": []any{map[string]string{"op": "add", "member_openid": member, "mute_expire_at": until}}})
	} else {
		raw, err = s.qqRequestMethodWithToken(ctx, c, token, http.MethodDelete, base+"/messages/"+url.PathEscape(message), nil)
	}
	if err != nil {
		return err
	}
	return validateSpamReportQQResponse(raw)
}

// QQ documents no response body (or {}) for these operations. An undocumented
// business-error or partial-result payload must not authorize a subsequent mute.
func validateSpamReportQQResponse(raw []byte) error {
	if len(raw) <= 1024 {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			return nil
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(trimmed, &object) == nil && object != nil && len(object) == 0 {
			return nil
		}
	}
	return errors.New("QQ 处罚响应不符合已公布成功格式，结果未确认")
}
