package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// These fixed codes are safe to persist and display without copying source text,
// callback fields, URLs, credentials, or member identifiers into the audit trail.
type spamReportSourceReason string

const (
	spamSourceOK                       spamReportSourceReason = ""
	spamSourceNotFound                 spamReportSourceReason = "spam_source_not_found"
	spamSourceExpired                  spamReportSourceReason = "spam_source_expired"
	spamSourceProcessingExpired        spamReportSourceReason = "spam_report_processing_expired"
	spamSourceProcessingInsufficient   spamReportSourceReason = "spam_report_processing_insufficient"
	spamSourceReportMissing            spamReportSourceReason = "spam_report_missing"
	spamSourceReportInvalid            spamReportSourceReason = "spam_report_inactive"
	spamSourceChanged                  spamReportSourceReason = "spam_source_changed"
	spamSourceAmbiguous                spamReportSourceReason = "spam_source_ambiguous"
	spamSourceWhitelist                spamReportSourceReason = "spam_source_whitelisted"
	spamSourceWhitelistChanged         spamReportSourceReason = "spam_report_whitelist_changed"
	spamSourceChatAuthorizationChanged spamReportSourceReason = "spam_report_chat_authorization_changed"
	spamSourceChatAuthorized           spamReportSourceReason = "spam_source_chat_authorized"
	spamSourceQQAdmin                  spamReportSourceReason = "spam_source_qq_admin"
	spamSourceBot                      spamReportSourceReason = "spam_source_bot"
	spamSourceRoleMissing              spamReportSourceReason = "spam_source_role_missing"
	spamSourceRoleChanged              spamReportSourceReason = "spam_source_role_changed"
	spamSourcePolicyInactive           spamReportSourceReason = "spam_source_policy_inactive"
	spamSourcePolicyDisabled           spamReportSourceReason = "spam_report_policy_disabled"
	spamSourcePolicyChanged            spamReportSourceReason = "spam_source_policy_changed"
	spamSourceIneligible               spamReportSourceReason = "spam_source_ineligible_content"
	spamSourceConflict                 spamReportSourceReason = "spam_source_callback_conflict"
	spamSourceMentionOnly              spamReportSourceReason = "spam_source_mention_only"
	spamSourceLegacy                   spamReportSourceReason = "spam_source_legacy_provenance"
	spamSourceInvalidTime              spamReportSourceReason = "spam_source_invalid_time"
	spamSourceInvalidRef               spamReportSourceReason = "spam_source_invalid_reference"
	spamSourceUnverified               spamReportSourceReason = "spam_source_unverified"
)

func (reason spamReportSourceReason) explanation() string {
	switch reason {
	case spamSourceNotFound:
		return "未找到与引用匹配的原消息记录。记录可能已清理、引用未匹配或回调未留存，不能据此认定权限不足；请引用刚发送的原始纯文本，仍失败请管理员检查回调记录。"
	case spamSourceExpired:
		return "原消息发送已超过 30 分钟，超出举报受理时限，无法再发起自动审核。请群管理员人工处理这条消息。"
	case spamSourceProcessingExpired:
		return "举报受理后的 2 分钟处理期限已到，本次处理已停止。请群管理员人工核查。"
	case spamSourceProcessingInsufficient:
		return "举报受理后的 2 分钟处理期限剩余不足 5 秒，无法安全提交处罚，本次处理已停止。请群管理员人工核查。"
	case spamSourceReportMissing:
		return "未找到匹配的举报受理记录，无法继续自动处理。请群管理员查看举报审核记录。"
	case spamSourceReportInvalid:
		return "举报受理记录已失效，无法继续自动处理。请群管理员查看举报审核记录中的状态及取消原因。"
	case spamSourceChanged:
		return "原消息的身份、正文或发送时间与受理时记录不一致，原证据已失效。请群管理员人工核查。"
	case spamSourceAmbiguous:
		return "引用匹配到多条原消息，无法唯一确认举报目标。请重新引用准确的原消息；仍失败请群管理员人工处理。"
	case spamSourceWhitelist:
		return "发送者在本群消息治理白名单内，自动处罚已跳过。请群管理员人工核查；如配置有误，请先核对并调整白名单。"
	case spamSourceWhitelistChanged:
		return "审核期间发送者被加入本群消息治理白名单，本次举报已取消；随后移出白名单也不会恢复这次处罚。请群管理员人工核查。"
	case spamSourceChatAuthorizationChanged:
		return "审核期间发送者被加入本群聊天授权成员，触发消息治理保护，本次举报已取消；随后移除授权也不会恢复这次处罚。请群管理员人工核查。"
	case spamSourceChatAuthorized:
		return "发送者是本群配置的聊天授权成员，当前也受消息治理保护。请群管理员人工核查，并按需核对聊天授权配置。"
	case spamSourceQQAdmin:
		return "原消息回调标记发送者为 QQ 群主或管理员，自动处罚已跳过。请群管理员人工核查。"
	case spamSourceBot:
		return "原消息回调标记发送者为机器人，自动处罚已跳过。请群管理员人工核查。"
	case spamSourceRoleMissing:
		return "原消息回调未提供可确认的普通成员角色。请管理员检查回调中的成员角色字段，并人工核查这条消息。"
	case spamSourceRoleChanged:
		return "后续回调显示发送者已不再具有可确认的普通成员身份，原证据不可用于自动处罚。请群管理员核对当前角色并人工处理。"
	case spamSourcePolicyInactive:
		return "本群当前未启用有效的举报策略。请管理员检查机器人开关、允许群和垃圾信息举报配置。"
	case spamSourcePolicyDisabled:
		return "审核期间本群举报功能被关闭或移出启用范围，本次举报已取消；重新启用也不会恢复这次处罚。请群管理员人工核查。"
	case spamSourcePolicyChanged:
		return "原消息留存后的举报配置已变化，原证据已失效。请管理员人工处理这条消息；新消息将按当前配置留存。"
	case spamSourceIneligible:
		return "原消息不符合可核验的原始纯文本条件，可能含引用、附件、卡片，或文本为空、过长或包含敏感兑换码。请群管理员直接核查原消息。"
	case spamSourceConflict:
		return "同一消息的重复回调存在内容或身份冲突，原证据已作废。请管理员核查回调差异并人工处理原消息。"
	case spamSourceMentionOnly:
		return "目前只留存了这条消息的 @ 回调，尚无完整的全量原消息记录。请稍后重试；仍失败请管理员核查该消息的全量回调。"
	case spamSourceLegacy:
		return "原消息记录缺少当前版本要求的回调来源标记，无法安全复用。请管理员人工处理这条消息；新消息将记录完整来源。"
	case spamSourceInvalidTime:
		return "原消息时间超前，无法确认安全处理窗口。请管理员核对服务器时间和回调时间，并人工核查。"
	case spamSourceInvalidRef:
		return "引用标识无效，无法定位原消息。请使用 QQ 的引用回复功能重新举报原消息。"
	default:
		return "原消息的安全校验未通过，现有记录不足以确定具体原因。请管理员核查回调记录并人工处理。"
	}
}

func (reason spamReportSourceReason) decision() string {
	return reason.decisionWithModel(false)
}

func (reason spamReportSourceReason) decisionWithModel(called bool) string {
	if reason == spamSourceOK {
		reason = spamSourceUnverified
	}
	if called {
		return string(reason) + "：" + reason.explanation() + "已调用模型，未提交处罚。"
	}
	return string(reason) + "：" + reason.explanation() + "未调用模型、未执行处罚。"
}

func (reason spamReportSourceReason) reply() string {
	return reason.explanation() + "本次不撤回、不禁言。"
}

func (reason spamReportSourceReason) audit() string {
	return string(reason) + "：" + reason.explanation()
}

func spamReportProtectionReason(c Config, group, member string) spamReportSourceReason {
	for _, exemption := range c.Moderation.Exemptions {
		if exemption.GroupID == group && exemption.MemberID == member {
			return spamSourceWhitelist
		}
	}
	for _, admin := range c.GroupAdmins {
		if admin.AppID == c.AppID && admin.GroupID == group && admin.MemberID == member {
			return spamSourceChatAuthorized
		}
	}
	return spamSourceOK
}

func spamReportConfigReason(c, snapshot Config, group, member string) spamReportSourceReason {
	if c.AppID != snapshot.AppID || c.Sandbox != snapshot.Sandbox {
		return spamSourcePolicyChanged
	}
	if reason := spamReportProtectionReason(c, group, member); reason != spamSourceOK {
		return reason
	}
	if !c.reportsSpam(group) {
		return spamSourcePolicyInactive
	}
	if spamReportPolicy(c) != spamReportPolicy(snapshot) {
		return spamSourcePolicyChanged
	}
	return spamSourceOK
}

// Only fixed codes written while cancelling are displayable; never echo stored
// free-form reasons or infer that a former protection is still configured.
func spamReportCancellationReason(reason string) spamReportSourceReason {
	switch code := spamReportSourceReason(strings.SplitN(reason, "：", 2)[0]); code {
	case spamSourceWhitelistChanged, spamSourceChatAuthorizationChanged, spamSourcePolicyChanged, spamSourcePolicyDisabled, spamSourceProcessingExpired:
		return code
	default:
		return spamSourceReportInvalid
	}
}

// Only a matching, durably admitted report may use the processing grace. New
// reports cannot use the longer encrypted-source retention as an admission window.
func (s *Service) inspectSpamReportSourceForReport(ctx context.Context, db spamReportQueryer, c Config, group, reference, reportID string) (*spamReportSource, spamReportSourceReason, error) {
	if !c.reportsSpam(group) {
		return nil, spamSourcePolicyInactive, nil
	}
	if reference == "" || len(reference) > 1024 {
		return nil, spamSourceInvalidRef, nil
	}
	// Count all retained matches before eligibility checks. A rejected or expired
	// collision must not select a different sender as an apparently unique match.
	rows, err := db.QueryContext(ctx, `SELECT s.message_id,s.member_id,s.content_encrypted,s.sent_at,
 s.eligible,s.is_bot,s.member_role,s.policy_hash,s.content_hash,
 s.sent_at<clock_timestamp()-($6::double precision * INTERVAL '1 second'),
 r.id IS NOT NULL,
 COALESCE(r.message_id=s.message_id AND r.member_id=s.member_id AND r.policy_hash=s.policy_hash AND r.status IN ('evaluating','queued')
 AND r.created_at>=s.sent_at-INTERVAL '5 seconds'
 AND r.created_at<=s.sent_at+($6::double precision * INTERVAL '1 second'),false),
 COALESCE(r.created_at<=clock_timestamp()-($7::double precision * INTERVAL '1 second')
 OR s.sent_at<=clock_timestamp()-($8::double precision * INTERVAL '1 second'),false),
 COALESCE(r.status,''),COALESCE(r.reason,''),
 s.sent_at>clock_timestamp()+INTERVAL '5 seconds',
 EXISTS(SELECT 1 FROM qq_bot_spam_report_sources newer WHERE newer.app_id=s.app_id
 AND newer.sandbox=s.sandbox AND newer.group_id=s.group_id AND newer.member_id=s.member_id
 AND newer.sent_at>=s.sent_at AND (newer.member_role<>'member' OR newer.is_bot))
 FROM qq_bot_spam_report_sources s LEFT JOIN qq_bot_spam_reports r
 ON r.id=$5 AND r.app_id=s.app_id AND r.sandbox=s.sandbox AND r.group_id=s.group_id
 WHERE s.app_id=$1 AND s.sandbox=$2 AND s.group_id=$3
 AND (s.message_id=$4 OR s.ref_idx=$4) LIMIT 2`, c.AppID, c.Sandbox, group, reference, reportID,
		spamReportWindow.Seconds(), spamReportProcessingWindow.Seconds(), spamReportSourceRetention.Seconds())
	if err != nil {
		return nil, spamSourceUnverified, err
	}
	defer func() { _ = rows.Close() }()
	var source spamReportSource
	var encrypted, role, policy, hash, reportStatus, reportReason string
	var eligible, bot, expired, reportFound, validReport, processingExpired, future, roleChanged bool
	count := 0
	for rows.Next() {
		if err = rows.Scan(&source.MessageID, &source.MemberID, &encrypted, &source.SentAt,
			&eligible, &bot, &role, &policy, &hash, &expired, &reportFound, &validReport, &processingExpired, &reportStatus, &reportReason, &future, &roleChanged); err != nil {
			return nil, spamSourceUnverified, err
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return nil, spamSourceUnverified, err
	}
	if count == 0 {
		return nil, spamSourceNotFound, nil
	}
	if count != 1 {
		return nil, spamSourceAmbiguous, nil
	}
	if reportID == "" && expired {
		return nil, spamSourceExpired, nil
	}
	if reportID != "" {
		if !reportFound {
			return nil, spamSourceReportMissing, nil
		}
		if reportStatus == "cancelled" {
			return nil, spamReportCancellationReason(reportReason), nil
		}
		if processingExpired {
			return nil, spamSourceProcessingExpired, nil
		}
		if !validReport {
			return nil, spamSourceReportInvalid, nil
		}
	}
	if future {
		return nil, spamSourceInvalidTime, nil
	}
	if reason := spamReportProtectionReason(c, group, source.MemberID); reason != spamSourceOK {
		return nil, reason, nil
	}
	if bot {
		return nil, spamSourceBot, nil
	}
	if role == "admin" || role == "owner" {
		return nil, spamSourceQQAdmin, nil
	}
	if role != "member" {
		return nil, spamSourceRoleMissing, nil
	}
	if roleChanged {
		return nil, spamSourceRoleChanged, nil
	}
	if policy != spamReportPolicy(c) {
		return nil, spamSourcePolicyChanged, nil
	}
	// The eligibility flag predates callback provenance. It cannot establish a
	// full original callback on its own, including for previously stored rows.
	var fingerprint spamReportCallbackHash
	if json.Unmarshal([]byte(hash), &fingerprint) != nil || fingerprint.Version != 1 {
		return nil, spamSourceLegacy, nil
	}
	if fingerprint.Kind == "at" {
		return nil, spamSourceMentionOnly, nil
	}
	if !eligible || fingerprint.Kind != "full" || fingerprint.Reason != "" || len(fingerprint.Content) != 64 {
		switch fingerprint.Reason {
		case "ineligible_content":
			return nil, spamSourceIneligible, nil
		case "callback_conflict":
			return nil, spamSourceConflict, nil
		case "legacy_provenance":
			return nil, spamSourceLegacy, nil
		default:
			return nil, spamSourceUnverified, nil
		}
	}
	if s.encryptor == nil {
		return nil, spamSourceUnverified, errors.New("举报证据解密不可用")
	}
	source.Content, err = s.encryptor.Decrypt(encrypted)
	if err != nil {
		return nil, spamSourceUnverified, errors.New("举报证据解密失败")
	}
	if strings.TrimSpace(source.Content) == "" {
		return nil, spamSourceIneligible, nil
	}
	return &source, spamSourceOK, nil
}
