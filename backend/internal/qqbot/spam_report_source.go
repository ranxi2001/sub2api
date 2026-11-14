package qqbot

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	spamReportWindow              = 30 * time.Minute
	spamReportProcessingWindow    = 2 * time.Minute
	spamReportSourceRetention     = spamReportWindow + spamReportProcessingWindow
	spamReportRecallWindow        = 115 * time.Second
	spamReportRecallSkippedReason = "原消息已超过安全撤回时限，按举报规则仅禁言"
)

func (c Config) reportsSpam(group string) bool {
	if !c.Enabled || !c.Moderation.ReportEnabled || !c.allows(group) {
		return false
	}
	for _, g := range c.Moderation.Groups {
		if group == g {
			return true
		}
	}
	return false
}

// Model and credential changes invalidate a verdict as well as policy changes.
func spamReportPolicy(c Config) string {
	raw, _ := json.Marshal([]any{"spam-report-v2-30m", c.Enabled, c.AppID, c.Sandbox, c.AllowedGroups,
		c.Moderation, c.GroupAdmins, c.BaseURL, c.APIKey, c.ChatModel, c.ChatProtocol})
	mac := hmac.New(sha256.New, []byte(c.AppSecret))
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

type spamReportSource struct {
	MessageID string
	MemberID  string
	Content   string
	SentAt    time.Time
}

func spamReportTextEligible(e Event) bool {
	return !e.Author.Bot && e.Author.MemberRole == "member" && e.MessageType == 0 &&
		e.MessageReference.MessageID == "" && e.MessageScene.value("ref_msg_idx") == "" &&
		len(e.Attachments) == 0 && len(e.MsgElements) == 0 &&
		strings.TrimSpace(e.Content) != "" && utf8.ValidString(e.Content) &&
		len(e.Content) <= 24000 && utf8.RuneCountInString(e.Content) <= 6000 && !containsRewardCode(e.Content)
}

// The existing TEXT hash column also records callback provenance. An @ callback
// can establish a consistency observation, but cannot supply a complete source.
// Keeping an explicit invalid state prevents a later callback from reviving a
// rejected observation; an empty ciphertext never implies trusted provenance.
type spamReportCallbackHash struct {
	Version        int    `json:"v"`
	Kind           string `json:"kind"`
	Reason         string `json:"reason,omitempty"`
	Content        string `json:"content,omitempty"`
	TrimmedContent string `json:"trimmed_content,omitempty"`
	WithoutMention string `json:"without_mention,omitempty"`
	AtContent      string `json:"at_content,omitempty"`
}

func spamReportContentHash(secret, content string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(content))
	return hex.EncodeToString(mac.Sum(nil))
}

func spamReportCallbackFingerprint(c Config, e Event, eventType string, eligible bool) spamReportCallbackHash {
	h := spamReportCallbackHash{Version: 1, Kind: "invalid", Reason: "ineligible_content"}
	if !eligible {
		return h
	}
	if eventType == groupMessageCreate {
		h.Kind = "full"
	} else {
		h.Kind = "at"
	}
	h.Reason = ""
	h.Content = spamReportContentHash(c.AppSecret, e.Content)
	h.TrimmedContent = spamReportContentHash(c.AppSecret, strings.TrimSpace(e.Content))
	if h.Kind == "full" {
		// QQ removes only the leading bot mention in GROUP_AT_MESSAGE_CREATE.
		// The matching signed @ callback with the same immutable identity is the
		// evidence for that transformation; never strip interior or @all tags.
		text := strings.TrimSpace(e.Content)
		if strings.HasPrefix(text, "<@") {
			mention, remainder, ok := strings.Cut(text[2:], ">")
			member := strings.TrimPrefix(mention, "!")
			valid := ok && member != "" && member != "all" && len(member) <= 128
			for _, r := range member {
				if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
					valid = false
				}
			}
			if valid && strings.TrimSpace(remainder) != "" {
				h.WithoutMention = spamReportContentHash(c.AppSecret, strings.TrimSpace(remainder))
			}
		}
	}
	return h
}

func spamReportCallbacksMatch(previous, current spamReportCallbackHash) bool {
	valid := func(h spamReportCallbackHash) bool {
		return h.Version == 1 && (h.Kind == "full" || h.Kind == "at") && len(h.Content) == 64
	}
	if !valid(previous) || !valid(current) {
		return false
	}
	if previous.Content == current.Content {
		return true
	}
	if previous.Kind == current.Kind {
		return false // Same-event duplicates must retain the exact original body.
	}
	if previous.Kind == "at" {
		previous, current = current, previous
	}
	return previous.WithoutMention != "" && previous.WithoutMention == current.TrimmedContent
}

// Capture only signed original callbacks. Quote bodies never establish authorship.
// This path runs before history, AI chat and ambient-message routing gates.
func (s *Service) rememberSpamReportSource(ctx context.Context, snapshot Config, e Event, eventTypes ...string) error {
	eventType := groupMessageCreate
	if len(eventTypes) > 0 {
		eventType = eventTypes[0]
	}
	if !isGroupMessageEvent(eventType) {
		return nil
	}
	if !snapshot.reportsSpam(e.GroupID) {
		return nil
	}
	receptionReason := ""
	defer func() { s.logSpamSourceReception(ctx, snapshot, e, eventType, receptionReason) }()
	switch {
	case e.ID == "":
		receptionReason = "message_id_missing"
	case len(e.ID) > 1024:
		receptionReason = "message_id_too_long"
	case e.Author.MemberID == "":
		receptionReason = "member_id_missing"
	case len(e.Author.MemberID) > 128:
		receptionReason = "member_id_too_long"
	case len(e.Author.MemberRole) > 32:
		receptionReason = "member_role_too_long"
	case len(e.Timestamp) == 0:
		receptionReason = "timestamp_missing"
	}
	if receptionReason != "" {
		return nil
	}
	var stamp string
	if json.Unmarshal(e.Timestamp, &stamp) != nil {
		receptionReason = "timestamp_type_invalid"
		return nil
	}
	if stamp == "" {
		receptionReason = "timestamp_empty"
		return nil
	}
	sent, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		receptionReason = "timestamp_invalid"
		return nil
	}
	if time.Since(sent) > spamReportSourceRetention {
		receptionReason = "timestamp_expired"
		return nil
	}
	if sent.After(time.Now().Add(5 * time.Second)) {
		receptionReason = "timestamp_future"
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		receptionReason = "transaction_begin_failed"
		return err
	}
	defer func() { _ = tx.Rollback() }()
	c, err := s.lockConfig(ctx, tx)
	if err != nil {
		receptionReason = "config_read_failed"
		return err
	}
	if c.AppID != snapshot.AppID || c.Sandbox != snapshot.Sandbox {
		receptionReason = "scope_changed"
		return nil
	}
	if !c.reportsSpam(e.GroupID) {
		receptionReason = "report_disabled"
		return nil
	}
	// Bound this evidence buffer even for unmentioned, high-volume group traffic.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,306))`, c.AppID+":"+e.GroupID); err != nil {
		receptionReason = "group_lock_failed"
		return err
	}
	observationEligible := spamReportTextEligible(e) && !c.moderationExempt(e.GroupID, e.Author.MemberID)
	fingerprint := spamReportCallbackFingerprint(c, e, eventType, observationEligible)
	eligible := observationEligible && eventType == groupMessageCreate
	ciphertext := ""
	if eligible {
		if !s.encryptionReady || s.encryptor == nil {
			receptionReason = "encryption_unavailable"
			return errors.New("举报原消息证据加密不可用")
		}
		ciphertext, err = s.encryptor.Encrypt(e.Content)
		if err != nil {
			receptionReason = "encryption_failed"
			return errors.New("举报原消息证据加密失败")
		}
	}
	// The group advisory lock serializes source writes. Compare immutable fields
	// in PostgreSQL so timestamp precision has exactly the same semantics as the
	// stored value, and retain the first timestamp and sender on every duplicate.
	var previousHash, previousCiphertext, previousRole string
	var previousBot, previousEligible, unchanged bool
	err = tx.QueryRowContext(ctx, `SELECT content_hash,content_encrypted,member_role,is_bot,eligible,
 member_id=$5 AND member_role=$6 AND ref_idx=$7 AND sent_at=$8 AND policy_hash=$9
 FROM qq_bot_spam_report_sources WHERE app_id=$1 AND sandbox=$2 AND group_id=$3 AND message_id=$4`,
		c.AppID, c.Sandbox, e.GroupID, e.ID, e.Author.MemberID, e.Author.MemberRole,
		e.MessageScene.value("msg_idx"), sent, spamReportPolicy(c)).Scan(&previousHash, &previousCiphertext, &previousRole, &previousBot, &previousEligible, &unchanged)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		receptionReason = "previous_source_read_failed"
		return err
	}
	role, bot := e.Author.MemberRole, e.Author.Bot
	if err == nil {
		var previous spamReportCallbackHash
		parsed := json.Unmarshal([]byte(previousHash), &previous) == nil
		matches := parsed && unchanged && observationEligible && !previousBot && spamReportCallbacksMatch(previous, fingerprint)
		if previous.Kind == "full" && !previousEligible {
			matches = false
		}
		if previous.Kind == "full" && fingerprint.Kind == "at" && previous.AtContent != "" && previous.AtContent != fingerprint.Content {
			matches = false
		}
		if role == "member" {
			role = previousRole
		}
		bot = bot || previousBot
		if !matches {
			// Legacy hashes have no provenance and cannot safely be upgraded.
			reason := "callback_conflict"
			if !parsed || previous.Version != 1 {
				reason = "legacy_provenance"
			} else if previous.Kind == "invalid" && previous.Reason != "" {
				reason = previous.Reason
			}
			fingerprint = spamReportCallbackHash{Version: 1, Kind: "invalid", Reason: reason}
			eligible, ciphertext = false, previousCiphertext
		} else if previous.Kind == "full" {
			if fingerprint.Kind == "at" {
				previous.AtContent = fingerprint.Content
			}
			fingerprint, eligible, ciphertext = previous, true, previousCiphertext
		} else if fingerprint.Kind == "full" {
			fingerprint.AtContent = previous.Content
		}
	}
	encodedHash, err := json.Marshal(fingerprint)
	if err != nil {
		receptionReason = "fingerprint_encode_failed"
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_spam_report_sources
 (app_id,sandbox,group_id,message_id,ref_idx,member_id,member_role,is_bot,eligible,content_encrypted,policy_hash,sent_at,content_hash)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
 ON CONFLICT(app_id,sandbox,group_id,message_id) DO UPDATE SET
 member_role=EXCLUDED.member_role,is_bot=EXCLUDED.is_bot,eligible=EXCLUDED.eligible,
 content_encrypted=EXCLUDED.content_encrypted,content_hash=EXCLUDED.content_hash`, c.AppID, c.Sandbox, e.GroupID, e.ID,
		e.MessageScene.value("msg_idx"), e.Author.MemberID, role, bot, eligible, ciphertext, spamReportPolicy(c), sent, string(encodedHash))
	if err != nil {
		receptionReason = "source_write_failed"
		return err
	}
	// QQ may timestamp multiple events to the same second. Retain protective
	// observations before ordinary sources at that timestamp, so capacity
	// eviction cannot remove a protection while leaving its affected source.
	_, err = tx.ExecContext(ctx, `DELETE FROM qq_bot_spam_report_sources WHERE app_id=$1 AND sandbox=$2 AND group_id=$3
 AND (sent_at<clock_timestamp()-($4::double precision * INTERVAL '1 second') OR message_id IN
 (SELECT message_id FROM qq_bot_spam_report_sources WHERE app_id=$1 AND sandbox=$2 AND group_id=$3
 ORDER BY sent_at DESC,(member_role<>'member' OR is_bot) DESC,message_id DESC OFFSET 2000))`, c.AppID, c.Sandbox, e.GroupID, spamReportSourceRetention.Seconds())
	if err != nil {
		receptionReason = "source_prune_failed"
		return err
	}
	if err = tx.Commit(); err != nil {
		receptionReason = "transaction_commit_failed"
		return err
	}
	receptionReason = spamSourceReceptionStoredReason(c, e, fingerprint, eligible)
	return nil
}

type spamReportQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Service) loadSpamReportEvidence(ctx context.Context, db spamReportQueryer, c Config, group, reference, reportID string) (*spamReportSource, error) {
	if reportID == "" {
		return nil, nil
	}
	source, _, err := s.inspectSpamReportSourceForReport(ctx, db, c, group, reference, reportID)
	return source, err
}

func (s *Service) maintainSpamReports(ctx context.Context) {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_spam_report_sources WHERE sent_at<clock_timestamp()-($1::double precision * INTERVAL '1 second')`, spamReportSourceRetention.Seconds())
	_, _ = s.db.ExecContext(ctx, `UPDATE qq_bot_spam_reports SET status='failed',reason=$1,finished_at=NOW()
 WHERE status='evaluating' AND created_at<clock_timestamp()-INTERVAL '2 minutes'`, spamSourceProcessingExpired.audit())
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_spam_reports WHERE created_at<NOW()-INTERVAL '30 days'`)
}

// Reverting a configuration must not revive a previously invalidated verdict.
// This runs under SaveConfig's exclusive lock, including sandbox changes when
// automatic anti-flood enforcement itself is disabled.
func resetSpamReports(ctx context.Context, tx *sql.Tx, old, current Config) error {
	if (!old.Moderation.ReportEnabled && !current.Moderation.ReportEnabled) || spamReportPolicy(old) == spamReportPolicy(current) {
		return nil
	}
	// Store the first verified cancellation cause under the config lock. Later
	// configuration restoration cannot replace it or revive the old admission.
	exemptions, err := json.Marshal(current.Moderation.Exemptions)
	if err != nil {
		return err
	}
	admins, err := json.Marshal(current.GroupAdmins)
	if err != nil {
		return err
	}
	groups := []string{}
	for _, group := range current.Moderation.Groups {
		if current.reportsSpam(group) {
			groups = append(groups, group)
		}
	}
	activeGroups, err := json.Marshal(groups)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE qq_bot_spam_reports r SET status='cancelled',reason=CASE
 WHEN r.app_id<>$2 OR r.sandbox<>$3 THEN $7
 WHEN EXISTS(SELECT 1 FROM jsonb_to_recordset(COALESCE(NULLIF($4::jsonb,'null'::jsonb),'[]'::jsonb)) AS e(group_id text,member_id text)
 WHERE e.group_id=r.group_id AND e.member_id=r.member_id) THEN $8
 WHEN EXISTS(SELECT 1 FROM jsonb_to_recordset(COALESCE(NULLIF($5::jsonb,'null'::jsonb),'[]'::jsonb)) AS a(app_id text,group_id text,member_id text)
 WHERE a.app_id=r.app_id AND a.group_id=r.group_id AND a.member_id=r.member_id) THEN $9
 WHEN NOT ($6::jsonb ? r.group_id) THEN $10 ELSE $7 END,finished_at=NOW()
 WHERE app_id=$1 AND (status='evaluating' OR (status='queued' AND EXISTS
 (SELECT 1 FROM qq_bot_moderation_operations o WHERE o.incident_id=r.incident_id AND o.status IN ('queued','started'))))`, old.AppID,
		current.AppID, current.Sandbox, string(exemptions), string(admins), string(activeGroups),
		spamSourcePolicyChanged.audit(), spamSourceWhitelistChanged.audit(), spamSourceChatAuthorizationChanged.audit(), spamSourcePolicyDisabled.audit()); err != nil {
		return err
	}
	// Never copy arbitrary persisted reason text into an operation's public
	// audit. Select a fixed, operation-specific explanation by known code only.
	operationReasons := map[string]map[string]string{}
	for _, reason := range []spamReportSourceReason{spamSourcePolicyChanged, spamSourceWhitelistChanged, spamSourceChatAuthorizationChanged, spamSourcePolicyDisabled} {
		operationReasons[string(reason)] = map[string]string{
			"recall": spamReportOperationCancellation(reason, "recall"),
			"mute":   spamReportOperationCancellation(reason, "mute"),
		}
	}
	operationReasonsJSON, err := json.Marshal(operationReasons)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE qq_bot_moderation_operations o SET status='cancelled',
 error=COALESCE($2::jsonb->split_part(r.reason,'：',1)->>o.kind,CASE WHEN o.kind='recall' THEN $3 ELSE $4 END),finished_at=NOW()
 FROM qq_bot_moderation_incidents i JOIN qq_bot_spam_reports r ON r.incident_id=i.id
 AND r.app_id=i.app_id AND r.sandbox=i.sandbox AND r.group_id=i.group_id AND r.member_id=i.member_id
 WHERE i.id=o.incident_id AND i.app_id=$1 AND r.status='cancelled'
 AND i.reason IN ('spam_advertising','spam_phishing','spam_scam') AND o.status='queued'`, old.AppID, string(operationReasonsJSON),
		spamReportOperationCancellation(spamSourceReportInvalid, "recall"), spamReportOperationCancellation(spamSourceReportInvalid, "mute")); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM qq_bot_spam_report_sources WHERE app_id=$1`, old.AppID)
	return err
}
