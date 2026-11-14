package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/lib/pq"
)

type Service struct {
	siteAI                SiteAIProvider
	db                    *sql.DB
	encryptor             Encryptor
	encryptionReady       bool
	invalidate            func(context.Context, int64)
	bindingEmail          BindingEmailDelivery
	channelStatus         ChannelStatusProvider
	modelPrices           ModelPriceProvider
	healthSource          HealthSnapshotProvider
	priceSource           PriceSnapshotProvider
	priceWake             chan struct{}
	http                  *http.Client
	tokenURL              string
	qqURL                 string
	tokenGate             chan struct{}
	accessToken           string
	tokenKey              string
	tokenExpires          time.Time
	groupBotMu            sync.Mutex
	groupBots             map[string]groupBotIdentity
	groupInfo             groupInfoCache
	spamSourceDiagnostics spamSourceReceptionDiagnostics
	imageDownload         func(context.Context, string) ([]byte, error)
	cancel                context.CancelFunc
	wg                    sync.WaitGroup
}

func New(db *sql.DB, encryptor Encryptor, ready bool, invalidate func(context.Context, int64)) *Service {
	if invalidate == nil {
		invalidate = func(context.Context, int64) {}
	}
	return &Service{db: db, encryptor: encryptor, encryptionReady: ready, invalidate: invalidate, priceWake: make(chan struct{}, 1), tokenGate: make(chan struct{}, 1), http: &http.Client{Timeout: 210 * time.Second, CheckRedirect: noRedirect}, imageDownload: downloadImage, tokenURL: "https://bots.qq.com/app/getAppAccessToken", qqURL: "https://api.sgroup.qq.com"}
}
func (s *Service) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.startModeration(ctx)
	s.startPriceMonitor(ctx)
	s.startHealthMonitor(ctx)
	s.startRankingMonitor(ctx)
	s.startGrowthMonitor(ctx)
	for i := 0; i < 2; i++ {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.runOne(ctx)
				}
			}
		}()
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.maintenance(ctx)
			}
		}
	}()
}
func (s *Service) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}
func (s *Service) maintenance(ctx context.Context) {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_pending_image_plans WHERE expires_at<NOW() OR EXISTS(SELECT 1 FROM qq_bot_jobs j WHERE j.id=job_id AND j.status NOT IN ('running','completed'))`)
	s.maintainModeration(ctx)
	s.maintainSpamReports(ctx)
	// An interrupted model request may already have cost money. Preserve its
	// status for inspection and never replay it after a process crash.
	_, _ = s.db.ExecContext(ctx, `UPDATE qq_bot_jobs SET status='interrupted',error='处理进程中断，未自动重试；请检查调用用量',finished_at=NOW(),payload='{}' WHERE status='running' AND started_at<NOW()-INTERVAL '10 minutes'`)
	_, _ = s.db.ExecContext(ctx, `UPDATE qq_bot_jobs SET status='expired',error='排队超过回复时限，请重新发送',finished_at=NOW(),payload='{}' WHERE status='queued' AND created_at<NOW()-INTERVAL '5 minutes' AND event_type IN ('GROUP_AT_MESSAGE_CREATE','GROUP_MESSAGE_CREATE','INTERACTION_CREATE')`)
	_, _ = s.db.ExecContext(ctx, `UPDATE qq_bot_jobs SET result_image=NULL,result_text='',payload='{}',member_nickname='',message_preview='' WHERE created_at<NOW()-INTERVAL '24 hours' AND (result_image IS NOT NULL OR result_text<>'' OR payload<>'{}'::jsonb OR member_nickname<>'' OR message_preview<>'')`)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_bind_requests WHERE expires_at<NOW()`)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_email_bind_requests WHERE expires_at<NOW()`)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_transfer_requests WHERE expires_at<NOW()`)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_email_limits WHERE day<CURRENT_DATE-2`)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_reward_limits WHERE last_attempt_at<NOW()-INTERVAL '2 days' AND blocked_until<NOW()`)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_reward_attempts WHERE failed AND created_at<NOW()-INTERVAL '30 days'`)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_conversations WHERE updated_at<NOW()-INTERVAL '1 day'`)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_group_messages h WHERE NOT EXISTS (SELECT 1 FROM qq_bot_group_messages recent WHERE recent.app_id=h.app_id AND recent.group_id=h.group_id AND recent.created_at>NOW()-INTERVAL '30 minutes')`)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_message_references WHERE created_at<NOW()-INTERVAL '24 hours'`)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_incoming_images WHERE created_at<NOW()-INTERVAL '24 hours'`)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_daily_usage WHERE day<CURRENT_DATE-32`)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_autonomous_seen WHERE created_at<NOW()-INTERVAL '1 day'`)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_autonomous_decisions WHERE day<CURRENT_DATE-32`)
	// Keep small deduplication tombstones for the signed event replay lifetime.
	_, _ = s.db.ExecContext(ctx, `DELETE FROM qq_bot_jobs WHERE created_at<NOW()-INTERVAL '30 days' AND status NOT IN ('queued','running')`)
}
func (s *Service) runOne(parent context.Context) {
	j, err := s.claimJob(parent)
	if err != nil {
		return
	}
	ctx, cancel := jobContext(parent, j)
	defer cancel()
	if (isGroupMessageEvent(j.EventType) || j.EventType == interactionCreate) && checkModelBudget(ctx, minimumModelBudget) != nil {
		s.finish(j, "expired", "排队超过回复时限，请重新发送")
		return
	}
	c, err := s.Config(ctx)
	if err != nil {
		logMembershipEvent(ctx, j, nil, "process", "failed", "config_unavailable")
		s.finish(j, "failed", err.Error())
		return
	}
	if !c.Enabled || c.AppID != j.AppID || !c.allows(j.GroupID) {
		reason := "bot_disabled"
		if c.AppID != j.AppID {
			reason = "app_changed"
		} else if c.Enabled && !c.allows(j.GroupID) {
			reason = "group_not_allowed"
		}
		logMembershipEvent(ctx, j, nil, "process", "ignored", reason)
		s.finish(j, "failed", ErrDisabled.Error())
		return
	}
	if j.EventType == announcementEvent {
		s.deliverAnnouncement(ctx, c, j)
		return
	}
	if j.EventType == rankingNotificationEvent {
		s.deliverRankingNotification(ctx, c, j)
		return
	}
	if j.EventType == priceNotificationEvent || j.EventType == healthNotificationEvent {
		s.deliverPriceNotification(ctx, c, j)
		return
	}
	var e Event
	if err = json.Unmarshal(j.Payload, &e); err != nil {
		logMembershipEvent(ctx, j, nil, "process", "failed", "invalid_payload")
		s.finish(j, "failed", "事件格式不正确")
		return
	}
	if !isGroupMessageEvent(j.EventType) && j.EventType != interactionCreate {
		err = s.memberEvent(ctx, c, j, e)
		if err != nil {
			s.finish(j, "failed", "群成员事件处理失败")
		} else {
			s.finish(j, "completed", "")
		}
		return
	}
	if checkModelBudget(ctx, minimumModelBudget) != nil {
		s.finish(j, "expired", "排队超过回复时限，请重新发送")
		return
	}
	if isGroupMessageEvent(j.EventType) {
		blocked, checkErr := s.moderationBlocked(ctx, c, j.GroupID, j.MessageID)
		if checkErr != nil {
			s.finish(j, "failed", "防刷屏状态读取失败")
			return
		}
		if blocked {
			s.finish(j, "skipped", "防刷屏拦截")
			return
		}
	}
	if j.EventType == interactionCreate {
		cmd, valid := helpActionCommand(c, e.HelpAction)
		if !valid {
			s.finish(j, "skipped", "帮助按钮已失效，请重新发送 帮助")
			return
		}
		e.Content = cmd
		e.Author.MemberID = j.MemberID
	}
	if j.Kind == "ambient_chat" {
		err = s.autonomousMessage(ctx, c, &j, e)
	} else {
		err = s.message(ctx, c, &j, e)
	}
	if err == nil && j.Status == "deferred" {
		return
	}
	if err != nil {
		status := "delivery_failed"
		if j.Error != "" {
			status = "failed"
		}
		if errors.Is(err, errReplyWindow) || errors.Is(err, context.DeadlineExceeded) {
			status = "expired"
		}
		s.finish(j, status, err.Error())
	} else {
		status := "completed"
		if j.Status == "skipped" {
			status = "skipped"
		}
		s.finish(j, status, "")
	}
}
func (s *Service) finish(j Job, status, detail string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `UPDATE qq_bot_jobs SET status=$2,error=$3,finished_at=NOW(),payload='{}' WHERE id=$1`, j.ID, status, limitText(detail, 250)); err != nil {
		logMembershipEvent(ctx, j, nil, "finish", "failed", "job_status_write_failed")
	}
}
func (s *Service) memberEvent(ctx context.Context, c Config, j Job, e Event) error {
	outcome, reason := "failed", "database_error"
	defer func() { logMembershipEvent(ctx, j, &e, "process", outcome, reason) }()
	switch j.EventType {
	case "GROUP_JOIN_REQUEST":
		if e.ApplySource != "invited" {
			outcome, reason = "ignored", "not_an_invitation"
			return nil
		}
		if e.InvitedBy == "" {
			outcome, reason = "ignored", "missing_inviter"
			return nil
		}
		if e.InvitedBy == e.MemberID {
			outcome, reason = "ignored", "self_invitation"
			return nil
		}
		applied, err := time.Parse(time.RFC3339, e.ApplyAt)
		if err != nil {
			reason = "invalid_invitation_time"
			return errors.New("invalid invitation time")
		}
		// Signed events can arrive out of order. A delayed invitation may
		// complete an earlier join, but cannot be attached after that join.
		res, err := s.db.ExecContext(ctx, `INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at) VALUES($1,$2,$3,$4,$5)
        ON CONFLICT(app_id,group_id,member_id) DO UPDATE SET inviter_id=EXCLUDED.inviter_id,invited_at=EXCLUDED.invited_at
        WHERE qq_bot_members.inviter_id='' AND (qq_bot_members.joined_at IS NULL OR EXCLUDED.invited_at<=qq_bot_members.joined_at)`, c.AppID, e.GroupID, e.MemberID, e.InvitedBy, applied)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		_, err = s.reward(ctx, c, j, "invite")
		if err != nil {
			reason = "reward_settlement_failed"
			return err
		}
		if n == 0 {
			outcome, reason = "ignored", "existing_inviter_or_late_invitation"
		} else {
			outcome, reason = "recorded", "invitation_recorded"
		}
		return err
	case "GROUP_MEMBER_ADD", "GROUP_MEMBER_REMOVE":
		var ts int64
		if json.Unmarshal(e.Timestamp, &ts) != nil || ts <= 0 {
			reason = "invalid_membership_time"
			return errors.New("invalid membership time")
		}
		at := time.Unix(ts, 0)
		if j.EventType == "GROUP_MEMBER_ADD" {
			res, err := s.db.ExecContext(ctx, `INSERT INTO qq_bot_members(app_id,group_id,member_id,joined_at,last_membership_at) VALUES($1,$2,$3,$4,$4)
            ON CONFLICT(app_id,group_id,member_id) DO UPDATE SET joined_at=COALESCE(qq_bot_members.joined_at,EXCLUDED.joined_at),left_at=NULL,last_membership_at=EXCLUDED.last_membership_at
            WHERE qq_bot_members.last_membership_at IS NULL OR EXCLUDED.last_membership_at>qq_bot_members.last_membership_at`, c.AppID, e.GroupID, e.MemberID, at)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			_, err = s.reward(ctx, c, j, "invite")
			if err != nil {
				reason = "reward_settlement_failed"
				return err
			}
			outcome, reason = "recorded", "member_join_recorded"
			if n == 0 {
				outcome, reason = "ignored", "stale_membership_event"
			}
			return err
		}
		res, err := s.db.ExecContext(ctx, `INSERT INTO qq_bot_members(app_id,group_id,member_id,left_at,last_membership_at) VALUES($1,$2,$3,$4,$4)
        ON CONFLICT(app_id,group_id,member_id) DO UPDATE SET left_at=EXCLUDED.left_at,last_membership_at=EXCLUDED.last_membership_at
        WHERE qq_bot_members.last_membership_at IS NULL OR EXCLUDED.last_membership_at>=qq_bot_members.last_membership_at`, c.AppID, e.GroupID, e.MemberID, at)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		outcome, reason = "recorded", "member_leave_recorded"
		if n == 0 {
			outcome, reason = "ignored", "stale_membership_event"
		}
		return nil
	}
	outcome, reason = "ignored", "unsupported_event"
	return nil
}

func (s *Service) SettleInvites(ctx context.Context, member string) error {
	c, err := s.Config(ctx)
	if err != nil {
		return err
	}
	return s.settleInvites(ctx, c, member)
}

func (s *Service) settleInvites(ctx context.Context, c Config, member string) error {
	if !c.Enabled || !c.InviteEnabled || !c.inviteHasReward() || c.InviteDailyLimit <= 0 {
		return nil
	}
	eligibleAsInviter := true
	requirement := c.inviteRechargeRequirement()
	if requirement.enabled() {
		var userID int64
		err := s.db.QueryRowContext(ctx, `SELECT b.user_id FROM qq_bot_bindings b JOIN users u ON u.id=b.user_id
 WHERE b.app_id=$1 AND b.member_id=$2 AND u.status='active' AND u.deleted_at IS NULL`, c.AppID, member).Scan(&userID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		eligibility, err := requirement.eligibility(ctx, s.db, userID, false)
		if err != nil {
			return err
		}
		eligibleAsInviter = requirement.satisfied(eligibility)
	}
	// Filter eligibility before bounding the batch. Otherwise old unbound or
	// already-paid relationships can permanently hide later valid invitations.
	rows, err := s.db.QueryContext(ctx, `SELECT m.group_id,m.member_id FROM qq_bot_members m
 JOIN qq_bot_bindings b ON b.app_id=m.app_id AND b.member_id=m.member_id
 JOIN users u ON u.id=b.user_id AND u.status='active' AND u.deleted_at IS NULL
 JOIN qq_bot_bindings ib ON ib.app_id=m.app_id AND ib.member_id=m.inviter_id
 JOIN users iu ON iu.id=ib.user_id AND iu.status='active' AND iu.deleted_at IS NULL
 WHERE m.app_id=$1 AND (m.member_id=$2 OR (m.inviter_id=$2 AND $6)) AND m.group_id=ANY($3)
 AND m.joined_at IS NOT NULL AND m.left_at IS NULL AND m.inviter_id<>'' AND m.member_id<>m.inviter_id
 AND m.invited_at<=m.joined_at AND m.invited_at>m.joined_at-INTERVAL '7 days' AND b.user_id<>ib.user_id
 AND NOT EXISTS(SELECT 1 FROM qq_bot_rewards r WHERE r.kind='invite' AND r.app_id=m.app_id AND r.source_member_id=m.member_id)
 AND NOT EXISTS(SELECT 1 FROM qq_bot_rewards r WHERE r.kind='invite' AND r.source_user_id=b.user_id)
 AND (SELECT COUNT(*) FROM qq_bot_rewards r WHERE r.kind='invite' AND r.user_id=ib.user_id
      AND (r.created_at AT TIME ZONE 'Asia/Shanghai')::date=$5::date)<$4
 ORDER BY m.joined_at,m.group_id,m.member_id LIMIT 100`, c.AppID, member, pq.Array(c.AllowedGroups), c.InviteDailyLimit, day(time.Now()), eligibleAsInviter)
	if err != nil {
		return err
	}
	var jobs []Job
	for rows.Next() {
		j := Job{AppID: c.AppID}
		if err = rows.Scan(&j.GroupID, &j.MemberID); err != nil {
			_ = rows.Close()
			return err
		}
		jobs = append(jobs, j)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if c.allows(j.GroupID) {
			if _, err = s.reward(ctx, c, j, "invite"); err != nil {
				return err
			}
		}
	}
	return nil
}

var mentionPrefix = regexp.MustCompile(`^<@!?[^>]+>\s*`)

func command(content string, customCommands ...CustomCommand) (string, string) {
	content = strings.TrimSpace(mentionPrefix.ReplaceAllString(strings.TrimSpace(content), ""))
	slash := strings.HasPrefix(content, "/")
	content = strings.TrimPrefix(content, "/")
	pos := strings.IndexFunc(content, unicode.IsSpace)
	fields := []string{content}
	if pos >= 0 {
		fields = []string{content[:pos], content[pos:]}
	}
	arg := ""
	if len(fields) == 2 {
		arg = strings.TrimSpace(fields[1])
	}
	name := fields[0]
	if name == "余额" {
		name = "我的余额"
	}
	if !slash && !isBuiltinCommand(name) {
		known := false
		for _, custom := range customCommands {
			if !isRetiredCommand(name) && strings.TrimPrefix(custom.Command, "/") == name {
				known = true
				break
			}
		}
		if !known {
			return "", ""
		}
	}
	return name, arg
}
func (s *Service) message(ctx context.Context, c Config, j *Job, e Event) error {
	ref, err := s.referencedMessage(ctx, c, j.GroupID, e.referenceID())
	if err != nil {
		return err
	}
	named := j.TriggerReason == "name" && c.autonomousGroup(j.GroupID) && botNameCall(c.Autonomous, e)
	addressed := j.EventType == groupAtMessageCreate
	if j.EventType == groupMessageCreate && !named && !unmentionedGroupCommand(e, c.CustomCommands...) {
		addressed, err = s.groupMessageMentionsBot(ctx, c, e)
		if err != nil {
			return err
		}
		// When the bot is @-mentioned and the content is a known command, treat it
		// as a command rather than routing to the AI chat path.
		if addressed && isBuiltinOrCustomCommand(e, c.CustomCommands) {
			// Fall through to command handling below.
		} else if !addressed && (ref == nil || len(groupMentionIDs(e)) > 0) {
			return nil
		}
	}
	j.ReplyReference = e.MessageScene.value("msg_idx")
	if j.Kind == "image" && len(j.Image) > 0 {
		if !c.ImageEnabled || c.groupPolicy(j.GroupID).DisableImages {
			return ErrDisabled
		}
		// A delivery retry must never enter quota reservation or a paid model
		// call again. Reuse the original media sequence for QQ idempotency.
		return s.send(ctx, c, *j, j.ResultText, j.Image, 3)
	}
	parts, attachments := eventParts(e)
	hasImages := len(imageAttachments(attachments)) > 0
	cmd, arg := command(e.Content, c.CustomCommands...)
	if cmd == "" && (addressed || named || ref != nil) && !hasImages && parts == "" && len(attachments) == 0 && naturalHelpRequest(e.Content) {
		cmd = "帮助"
	}
	// Common personal queries are read-only and need neither a model call nor
	// chat quota. Match only this member's current, explicitly addressed text.
	if cmd == "" && addressed && !hasImages && parts == "" && len(attachments) == 0 && e.referenceID() == "" {
		cmd = naturalAccountCommand(e.Content)
	}
	if strings.TrimSpace(mentionPrefix.ReplaceAllString(strings.TrimSpace(e.Content), "")) == "" && !hasImages && parts == "" && e.referenceID() == "" {
		cmd = "帮助"
	}
	if arg != "" {
		switch cmd {
		case "签到", "我的余额", "今日用量", "我的奖励", "抽奖次数", "我的抽奖", "邀请奖励", "邀请活动", "我的邀请":
			return s.send(ctx, c, *j, "用法："+cmd+"。", nil, 1)
		}
	}
	if cmd == "成长任务" || cmd == "抽奖活动" {
		if _, ok := commandPageNumber(arg); !ok {
			return s.send(ctx, c, *j, "用法："+cmd+"；翻页："+cmd+" 页码。", nil, 1)
		}
	}
	text := ""
	var img []byte
	imageEdited := false
	seq := 1
	progressAttempted := false
	sendImageProgress := func(notice string) {
		if progressAttempted {
			return
		}
		progressAttempted = true
		progressJob := *j
		progressJob.TransientReply = true
		progressCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		// A failed/uncertain notice must neither stop paid work nor be retried.
		// Consume its sequence even on failure to avoid reusing a QQ message ID.
		_ = s.send(progressCtx, c, progressJob, notice, nil, seq)
		seq++
	}
	switch cmd {
	case "帮助":
		return s.sendHelp(ctx, c, *j, arg)
	case "价格":
		return s.sendModelPrice(ctx, c, *j, arg)
	case "我的优惠券":
		{
			var err error
			text, err = s.myCoupons(ctx, c, *j, arg)
			if err != nil {
				text = "优惠券查询失败，请稍后重试。"
			}
			break
		}
	case "我的余额":
		var balance float64
		err := s.db.QueryRowContext(ctx, `SELECT u.balance FROM qq_bot_bindings b JOIN users u ON u.id=b.user_id WHERE b.app_id=$1 AND b.member_id=$2 AND u.deleted_at IS NULL AND u.status='active'`, c.AppID, j.MemberID).Scan(&balance)
		if errors.Is(err, sql.ErrNoRows) {
			text = bindFirst
		} else if err != nil {
			text = "账户信息查询失败，请稍后重试。"
		} else {
			text = fmt.Sprintf("💳 **我的余额**\n余额：USD %.2f", balance)
		}
	case "今日用量":
		var err error
		text, err = s.todayUsage(ctx, c, *j, time.Now())
		if err != nil {
			text = "用量查询失败，请稍后重试。"
		}
	case "我的奖励":
		var err error
		text, err = s.myRewards(ctx, c, *j)
		if err != nil {
			text = "奖励信息查询失败，请稍后重试。"
		}
	case "成长任务":
		var err error
		text, err = s.growthTasks(ctx, c, *j)
		if err != nil {
			text = "成长任务查询失败，请稍后重试。"
		}
		return s.sendCommandPage(ctx, c, *j, cmd, text, arg)
	case "抽奖":
		count, ok := lotteryDrawCount(arg)
		if !ok {
			return s.send(ctx, c, *j, lotteryDrawUsage, nil, 1)
		}
		var err error
		if count == 1 {
			text, err = s.drawLottery(ctx, c, *j)
		} else {
			text, err = s.drawLotteryCount(ctx, c, *j, count)
		}
		if err != nil {
			text = "抽奖处理异常，请发送 抽奖次数 查询剩余次数。"
		}
	case "抽奖次数", "我的抽奖", "抽奖活动":
		var err error
		text, err = s.lotteryInfo(ctx, c, *j, cmd == "抽奖活动")
		if err != nil {
			text = "抽奖信息查询失败，请稍后重试。"
		}
		if cmd == "抽奖活动" {
			return s.sendCommandPage(ctx, c, *j, cmd, text, arg)
		}
	case "邀请奖励", "邀请活动":
		var err error
		text, err = s.inviteInfo(ctx, c, *j)
		if err != nil {
			text = "邀请活动查询失败，请稍后重试。"
		}
	case "我的邀请":
		var err error
		text, err = s.myInvites(ctx, c, *j)
		if err != nil {
			text = "邀请信息查询失败，请稍后重试。"
		}
	case "今日排行榜", "今日排行版":
		if arg != "" {
			text = "用法：今日排行榜"
			break
		}
		var err error
		text, err = s.todayRankings(ctx, c, time.Now())
		if err != nil {
			text = "排行榜查询失败，请稍后重试。"
		}
	case "渠道状态":
		return s.sendChannelStatus(ctx, c, *j, arg)
	case "绑定":
		if arg == "" {
			text = "用法：绑定 邮箱或绑定码"
			break
		}
		var bound bool
		err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM qq_bot_bindings WHERE app_id=$1 AND member_id=$2)`, c.AppID, e.Author.MemberID).Scan(&bound)
		if err != nil {
			text = "绑定状态查询失败，请稍后重试。"
		} else if bound {
			text = "当前 QQ 已绑定账号。"
		} else if strings.Contains(arg, "@") {
			var pending *preparedBindingEmail
			text, pending = s.prepareEmailBinding(ctx, c, e, arg)
			if pending != nil {
				// A slow SMTP server must not delay the user's QQ acknowledgement.
				if err := s.send(ctx, c, *j, text, nil, seq); err != nil {
					return err
				}
				s.deliverBindingEmail(ctx, e, pending)
				return nil
			}
		} else {
			text = s.claimBinding(ctx, c, e, arg)
		}
	case "签到":
		var err error
		text, err = s.reward(ctx, c, *j, "checkin")
		if err != nil {
			text = "签到失败，请稍后重试。"
		}
	case "转账":
		if arg == "" {
			text = transferUsage
			if c.TransferFeePercent > 0 {
				text += fmt.Sprintf("\n手续费：%.2f%%", c.TransferFeePercent)
			}
			break
		}
		var pending *preparedTransferEmail
		text, pending = s.prepareTransfer(ctx, c, e, arg)
		if pending != nil {
			if err := s.send(ctx, c, *j, text, nil, seq); err != nil {
				return err
			}
			s.deliverTransferEmail(ctx, pending)
			return nil
		}
	default:
		if reply, ok := customCommandReply(c.CustomCommands, cmd, arg); ok {
			text = reply
			break
		}
		if cmd != "" && cmd != "生图" {
			text = "未知指令，请发送 帮助。"
			break
		}
		kind := "chat"
		imageConfig := c
		prompt := strings.TrimSpace(mentionPrefix.ReplaceAllString(strings.TrimSpace(e.Content), ""))
		if prompt == "" && (hasImages || parts != "" || e.referenceID() != "") {
			prompt = "请分析这条消息的内容。"
		}
		if cmd == "生图" {
			kind = "image"
			imageConfig, prompt = imageCommandOptions(c, arg)
			if !c.ImageEnabled {
				text = "生图功能未启用。"
			}
			if prompt == "" {
				text = "用法：生图 图片描述；GIF：生图 --gif 动画描述"
			}
		}
		if text != "" {
			break
		}
		if prompt == "" || len([]rune(prompt)) > 6000 {
			text = "内容长度须为 1～6000 字。"
			break
		}
		if err := s.reserveUsage(ctx, c, *j, kind); err != nil {
			if errors.Is(err, errReplyWindow) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			text = "服务暂时不可用，请稍后重试。"
			if errors.Is(err, errFeatureUnavailable) || errors.Is(err, errUsageCooldown) || errors.Is(err, errUsageLimit) {
				text = err.Error()
			}
			break
		}
		j.Kind = kind
		var err error
		if kind == "image" {
			err = s.clearPendingImagePlan(ctx, c, *j)
			if err == nil {
				err = s.saveImageDecision(ctx, j, imageDecision{Tool: chatImageToolName, Review: "command", Basis: "explicit", Reason: "slash_command", Execution: "started", MessageRef: groupHistoryID(c.AppID, j.GroupID, e.ID)})
			}
			if err == nil {
				sendImageProgress("正在生成图片，请稍候。")
				img, err = s.generateImage(ctx, imageConfig, prompt)
				status := "succeeded"
				if err != nil {
					status = "failed"
				}
				if recordErr := s.imageExecution(ctx, j, status); err == nil {
					err = recordErr
				}
			}
		} else {
			messages := s.historyForQuestion(ctx, c, *j, prompt)
			messages = withGroupAdminChatInstructions(messages, c, *j, addressed)
			messages = withChatCapabilities(messages, c, *j)
			var input Message
			input, err = s.chatMessageWithImageProgress(ctx, c, e, prompt, ref, func() {
				sendImageProgress("正在处理图片，请稍候。")
			})
			if err == nil {
				var pending *pendingImagePlan
				if c.ImageEnabled && !c.groupPolicy(j.GroupID).DisableImages {
					pending, err = s.pendingImagePlan(ctx, c, *j)
				}
				messages = withPendingImagePlan(messages, pending)
				messages = append(messages, groupUserMessage(c, e, input))
				j.ShareHistory = shareableGroupEvent(e, ref, c.CustomCommands...)
				var turn chatTurnResult
				turnConfig := c
				turnConfig.ImageEnabled = c.ImageEnabled && !c.groupPolicy(j.GroupID).DisableImages
				if err == nil {
					var followup *siteQuery
					if ref == nil && e.referenceID() == "" && !hasImages && parts == "" && len(attachments) == 0 {
						followup, err = s.resolveSiteQueryFollowup(ctx, c, *j, prompt)
					}
					if err == nil && followup != nil {
						turn.SiteQuery = followup
					} else if err == nil {
						turn, err = s.siteChatTurn(ctx, turnConfig, messages)
					}
				}
				if err == nil && turn.SiteQuery != nil {
					// Return the read-only backend result directly. Personal quotes
					// must not be copied into conversational or shared history.
					j.ShareHistory = false
					var result SiteQueryResult
					result, err = s.querySiteInfoDetailed(ctx, c, *j, *turn.SiteQuery)
					if err == nil {
						turn.Text = result.Text
						err = s.recordSiteQuery(ctx, j, result.Decision)
					}
				}
				if err == nil && (turn.ImagePrompt != "" || turn.ImagePlan != nil) {
					turn, err = s.reviewImageTurn(ctx, turnConfig, j, e, prompt, input, ref, pending, turn)
				} else if err == nil {
					// Ordinary chat/cancellation invalidates an old plan even while
					// images are disabled and the plan was not loaded into context.
					err = s.clearPendingImagePlan(ctx, c, *j)
				}
				text, j.InputTokens, j.OutputTokens = turn.Text, turn.InputTokens, turn.OutputTokens
				if turn.ChatExecution.Attempts > 0 {
					j.ChatExecution, _ = json.Marshal(turn.ChatExecution)
				}
				if err == nil && turn.ImagePrompt != "" {
					// The independent review approved; recheck policy and reserve
					// image usage separately from chat and review token usage.
					if usageErr := s.reserveUsage(ctx, c, *j, "image"); usageErr != nil {
						if errors.Is(usageErr, errReplyWindow) || errors.Is(usageErr, context.DeadlineExceeded) {
							return usageErr
						}
						err = s.imageExecution(ctx, j, "quota_blocked")
						text = "生图服务暂时不可用，请稍后重试。"
						if errors.Is(usageErr, errFeatureUnavailable) || errors.Is(usageErr, errUsageCooldown) || errors.Is(usageErr, errUsageLimit) {
							text = "无法生成图片：" + usageErr.Error()
						}
					} else {
						kind, j.Kind = "image", "image"
						err = s.imageExecution(ctx, j, "started")
						if err == nil && turn.EditImageIndex > 0 {
							imageEdited = true
							sendImageProgress("正在修改图片，请稍候。")
							img, err = s.editImage(ctx, imageOutputConfig(c, turn.ImageOutputFormat), turn.ImagePrompt, input.Images[turn.EditImageIndex-1])
						} else if err == nil {
							sendImageProgress("正在生成图片，请稍候。")
							img, err = s.generateImage(ctx, imageOutputConfig(c, turn.ImageOutputFormat), turn.ImagePrompt)
						}
						status := "succeeded"
						if err != nil {
							status = "failed"
						}
						if recordErr := s.imageExecution(ctx, j, status); err == nil {
							err = recordErr
						}
						if err == nil {
							text = "图片已生成。"
							if imageEdited {
								text = "图片已修改。"
							}
						}
					}
				}
			} else {
				text = err.Error()
			}
		}
		if err != nil {
			// Record upstream failure distinctly from delivery failures. Never expose raw upstream bodies.
			if text == "" {
				text = "回复生成失败，请稍后重试。"
			}
			if kind == "image" {
				text = "图片生成失败，请稍后重试。"
				if imageEdited {
					text = "图片编辑失败，请稍后重试。"
				}
			}
			if outputMessage := imageOutputMessage(err); outputMessage != "" {
				text = outputMessage
			}
			j.Error = err.Error()
			_, _ = s.db.ExecContext(ctx, `UPDATE qq_bot_jobs SET error=$2,input_tokens=$3,output_tokens=$4,chat_execution=$5 WHERE id=$1`, j.ID, j.Error, j.InputTokens, j.OutputTokens, nullableChatExecution(j.ChatExecution))
			if sendErr := s.send(ctx, c, *j, text, nil, seq); sendErr != nil {
				return sendErr
			}
			return fmt.Errorf("模型调用失败：%w", err)
		}
		// Persist before sending to QQ. Delivery retries only reuse this saved result.
		if _, err = s.db.ExecContext(ctx, `UPDATE qq_bot_jobs SET result_text=$2,result_image=$3,image_type=$4,input_tokens=$5,output_tokens=$6,chat_execution=$7 WHERE id=$1`, j.ID, text, img, generatedImageType(img), j.InputTokens, j.OutputTokens, nullableChatExecution(j.ChatExecution)); err != nil {
			return errors.New("生成成功但结果保存失败；未自动重新生成")
		}
	}
	if len(img) > 0 {
		notice := "图片已生成。"
		if imageEdited {
			notice = "图片已修改。"
		}
		if err := s.send(ctx, c, *j, notice, nil, seq); err != nil {
			return err
		}
		return s.send(ctx, c, *j, text, img, seq+1)
	}
	chunks := splitReply(normalizeReplyLineBreaks(text), 5-seq+1)
	for _, chunk := range chunks {
		if err := s.sendFormatted(ctx, c, *j, chunk, nil, seq); err != nil {
			return err
		}
		seq++
	}
	return nil
}
