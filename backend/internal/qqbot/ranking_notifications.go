package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

const rankingNotificationEvent = "RANKING_REWARD_NOTIFICATION"

type rankingNotificationPayload struct {
	Text      string `json:"text"`
	Sandbox   bool   `json:"sandbox"`
	SourceKey string `json:"source_key"`
}

func formatRankingRewardNotification(date, kind string, rank int, reward GrowthReward) string {
	return fmt.Sprintf("🏆 **每日排行榜奖励**\n\n%s（北京时间）\n%s · 第 %d 名\n\n奖励：%s\n状态：%s\n\n发送 成长任务 查看奖励记录。",
		date, rankingTitle(kind), rank, reward.description(), reward.deliveryNote())
}

// Enqueue only newly granted rewards, in the grant transaction. Workers cannot
// announce an uncommitted award, and failed sends never cause a second grant.
func enqueueRankingRewardNotification(ctx context.Context, tx *sql.Tx, c Config, j Job, key, date, kind string, rank int, reward GrowthReward) error {
	j.EventType = rankingNotificationEvent
	if j.MemberID == "" || !allowsNotification(c, j) {
		return nil
	}
	body, err := json.Marshal(rankingNotificationPayload{
		Text: formatRankingRewardNotification(date, kind, rank, reward), Sandbox: c.Sandbox, SourceKey: key,
	})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,member_id,payload,kind)
 VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,'ranking_notification') ON CONFLICT(app_id,event_key) DO NOTHING`,
		randomID(), c.AppID, "ranking-reward:"+key, rankingNotificationEvent, j.GroupID, j.MemberID, string(body))
	return err
}

func (s *Service) deliverRankingNotification(ctx context.Context, c Config, j Job) {
	var body rankingNotificationPayload
	if json.Unmarshal(j.Payload, &body) != nil || body.SourceKey == "" || j.MemberID == "" {
		s.finish(j, "cancelled", "排行奖励通知内容无效")
		return
	}
	// A queued notification must not mention a previous owner after unbinding,
	// rebinding or changing groups. Known departures also suppress delivery.
	var valid bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM qq_bot_ranking_claims r
 JOIN users u ON u.id=r.user_id AND u.status='active' AND u.deleted_at IS NULL
 JOIN qq_bot_bindings b ON b.app_id=r.app_id AND b.user_id=r.user_id
 WHERE r.app_id=$1 AND r.source_key=$2 AND r.claimed_at IS NOT NULL AND b.group_id=$3 AND b.member_id=$4
 AND NOT EXISTS(SELECT 1 FROM qq_bot_members m WHERE m.app_id=b.app_id AND m.group_id=b.group_id AND m.member_id=b.member_id AND m.left_at IS NOT NULL)
 )`, c.AppID, body.SourceKey, j.GroupID, j.MemberID).Scan(&valid)
	if err != nil {
		s.finish(j, "failed", "排行奖励通知绑定状态检查失败")
		return
	}
	if !valid {
		s.finish(j, "cancelled", "获奖账号已不可用、QQ 绑定已变更或成员已退群")
		return
	}
	s.deliverPriceNotification(ctx, c, j)
}
