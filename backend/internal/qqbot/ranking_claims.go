package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

func rankingClaimName(date, kind string, rank int) string {
	return fmt.Sprintf("%s %s 第 %d 名", date, rankingTitle(kind), rank)
}

func rankingGrowthTasks(ctx context.Context, tx *sql.Tx, appID string, uid int64) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.ranking_day::text,r.ranking_kind,r.rank,r.reward,r.claimed_at,COALESCE(a.status,'') FROM qq_bot_ranking_claims r LEFT JOIN qq_bot_reward_codes a ON a.source_key=r.source_key WHERE r.app_id=$1 AND r.user_id=$2 ORDER BY r.ranking_day DESC,r.id DESC LIMIT 10`, appID, uid)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	text := "**排行奖励（最近 10 项）**"
	count := 0
	for rows.Next() {
		var date, kind, fulfillment string
		var rank int
		var raw []byte
		var claimed sql.NullTime
		if err = rows.Scan(&date, &kind, &rank, &raw, &claimed, &fulfillment); err != nil {
			return "", err
		}
		var reward GrowthReward
		if err = json.Unmarshal(raw, &reward); err != nil {
			return "", err
		}
		status := "已自动发放"
		if !claimed.Valid {
			status = "待结算"
		} else if reward.Type == "physical" || reward.Type == "other" {
			status = "已发放"
			if fulfillment == "pending" {
				status = "待管理员发放"
			}
		}
		text += fmt.Sprintf("\n\n**%s**\n奖励：%s\n状态：%s", rankingClaimName(date, kind, rank), reward.description(), status)
		count++
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if count == 0 {
		return "排行奖励：暂无记录\n查看榜单：今日排行榜", nil
	}
	return text, nil
}

func rankingRewardJob(ctx context.Context, tx *sql.Tx, appID string, uid int64) (Job, error) {
	j := Job{}
	err := tx.QueryRowContext(ctx, `SELECT member_id,group_id FROM qq_bot_bindings WHERE app_id=$1 AND user_id=$2`, appID, uid).Scan(&j.MemberID, &j.GroupID)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return j, err
}

// Drain frozen rewards created by the former manual-claim workflow. The old
// source key also detects rewards already paid before the claim table existed.
// Unbound winners receive their reward on their website account as well.
func (s *Service) settlePendingRankingRewards(ctx context.Context, c Config, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.lockConfig(ctx, tx)
	if err != nil {
		return err
	}
	if !current.Enabled || current.AppID != c.AppID {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,272))`, c.AppID); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.user_id,r.ranking_day::text,r.ranking_kind,r.rank,r.source_key,r.reward
 FROM qq_bot_ranking_claims r JOIN users u ON u.id=r.user_id AND u.status='active' AND u.deleted_at IS NULL
 WHERE r.app_id=$1 AND r.claimed_at IS NULL ORDER BY r.user_id,r.id LIMIT 100`, c.AppID)
	if err != nil {
		return err
	}
	type pendingReward struct {
		id, uid         int64
		date, kind, key string
		rank            int
		raw             []byte
	}
	var pending []pendingReward
	for rows.Next() {
		var r pendingReward
		if err = rows.Scan(&r.id, &r.uid, &r.date, &r.kind, &r.rank, &r.key, &r.raw); err != nil {
			break
		}
		pending = append(pending, r)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	// Lock the full, already sorted recipient set before coupon template locks.
	// Admin batch issuance uses the same order, preventing user/template deadlocks.
	for _, entry := range pending {
		if _, err = tx.ExecContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, entry.uid); err != nil {
			return err
		}
	}
	var invalidations []int64
	for _, r := range pending {
		var uid int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND status='active' AND deleted_at IS NULL FOR UPDATE`, r.uid).Scan(&uid)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		var reward GrowthReward
		if err = json.Unmarshal(r.raw, &reward); err != nil {
			return err
		}
		j, e := rankingRewardJob(ctx, tx, c.AppID, uid)
		if e != nil {
			return e
		}
		granted, e := grantActivityReward(ctx, tx, current, j, uid, r.key, "ranking", rankingClaimName(r.date, r.kind, r.rank), reward, at)
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, `UPDATE qq_bot_ranking_claims SET claimed_at=$2 WHERE id=$1`, r.id, at); err != nil {
			return err
		}
		if granted {
			if err = enqueueRankingRewardNotification(ctx, tx, current, j, r.key, r.date, r.kind, r.rank, reward); err != nil {
				return err
			}
		}
		if granted && reward.Type == "balance" {
			invalidations = append(invalidations, uid)
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	for _, uid := range invalidations {
		s.invalidate(ctx, uid)
	}
	return nil
}
