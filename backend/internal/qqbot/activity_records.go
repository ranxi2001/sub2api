package qqbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ActivityParticipant struct {
	MemberID    string `json:"member_id"`
	UserID      int64  `json:"user_id"`
	Email       string `json:"email"`
	Nickname    string `json:"nickname"`
	Bound       bool   `json:"bound"`
	UserStatus  string `json:"user_status"`
	UserDeleted bool   `json:"user_deleted"`
}

type ActivityRecord struct {
	CouponTemplateID int64                `json:"coupon_template_id,omitempty"`
	CouponQuantity   int                  `json:"coupon_quantity,omitempty"`
	ID               string               `json:"id"`
	GroupID          string               `json:"group_id"`
	Participant      ActivityParticipant  `json:"participant"`
	Inviter          *ActivityParticipant `json:"inviter,omitempty"`
	Status           string               `json:"status"`
	Amount           float64              `json:"amount"`
	Chances          int                  `json:"chances"`
	PrizeName        string               `json:"prize_name"`
	IsGuaranteed     bool                 `json:"is_guaranteed"`
	Remaining        *int64               `json:"remaining,omitempty"`
	CreatedAt        time.Time            `json:"created_at"`
	InvitedAt        *time.Time           `json:"invited_at,omitempty"`
	JoinedAt         *time.Time           `json:"joined_at,omitempty"`
	LeftAt           *time.Time           `json:"left_at,omitempty"`
	RewardedAt       *time.Time           `json:"rewarded_at,omitempty"`
	RewardUserID     int64                `json:"reward_user_id"`
	RewardEmail      string               `json:"reward_email"`
}

type ActivityRecordsPage struct {
	Items    []ActivityRecord `json:"items"`
	Total    int              `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
}

func participantTargets(p *ActivityParticipant) []any {
	return []any{&p.MemberID, &p.UserID, &p.Email, &p.Nickname, &p.Bound, &p.UserStatus, &p.UserDeleted}
}

// Record reads never settle rewards. Historical credits retain their original
// account, while invitation relationships show both parties' current bindings.
func (s *Service) ListActivityRecords(ctx context.Context, kind, search string, page, pageSize int) (ActivityRecordsPage, error) {
	out := ActivityRecordsPage{Items: []ActivityRecord{}, Page: page, PageSize: pageSize}
	if kind != "checkin" && kind != "lottery" && kind != "invite" {
		return out, errors.New("活动类型不正确")
	}
	if page < 1 || page > 1000000 || pageSize < 1 || pageSize > 100 {
		return out, errors.New("分页参数不正确")
	}
	c, err := s.Config(ctx)
	if err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	search = strings.TrimSpace(search)
	if kind == "invite" {
		err = listInviteRecords(ctx, tx, c, search, &out)
	} else {
		err = listParticipationRecords(ctx, tx, c.AppID, kind, search, &out)
	}
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func listParticipationRecords(ctx context.Context, tx *sql.Tx, appID, kind, search string, out *ActivityRecordsPage) error {
	table, extraFilter, prize, remaining := "qq_bot_rewards", " AND r.kind='checkin'", "''", "NULL::bigint"
	guaranteed := "false"
	chances := "0"
	couponID := "COALESCE((SELECT c.coupon_template_id FROM qq_bot_reward_codes c WHERE c.source_key=r.reward_key||':coupon' AND c.reward_type='coupon' AND c.user_id=r.user_id),0)"
	couponQuantity := "COALESCE((SELECT c.quantity FROM qq_bot_reward_codes c WHERE c.source_key=r.reward_key||':coupon' AND c.reward_type='coupon' AND c.user_id=r.user_id),0)"
	if kind == "lottery" {
		table, extraFilter, prize, remaining = "qq_bot_lottery_draws", "", "r.prize_name", "r.remaining"
		guaranteed = "COALESCE(r.config_snapshot->'is_guaranteed'='true'::jsonb,false)"
		chances = "COALESCE((r.config_snapshot->>'awarded_chances')::integer,0)"
		couponID = "COALESCE((r.config_snapshot->>'coupon_template_id')::bigint,0)"
		couponQuantity = "COALESCE((r.config_snapshot->>'coupon_quantity')::integer,0)"
	}
	from := ` FROM ` + table + ` r JOIN users u ON u.id=r.user_id
 LEFT JOIN qq_bot_bindings b ON b.app_id=r.app_id AND b.member_id=r.member_id AND b.user_id=r.user_id`
	filter := ` WHERE r.app_id=$1` + extraFilter + ` AND ($2='' OR r.user_id::text=$2
 OR strpos(lower(u.email),lower($2))>0 OR strpos(lower(r.member_id),lower($2))>0
 OR strpos(lower(r.group_id),lower($2))>0 OR strpos(lower(COALESCE(b.nickname,'')),lower($2))>0)`
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)`+from+filter, appID, search).Scan(&out.Total); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.id::text,r.group_id,r.member_id,r.user_id,u.email,
 COALESCE(b.nickname,''),b.user_id IS NOT NULL,u.status,u.deleted_at IS NOT NULL,
 r.amount,`+prize+`,`+remaining+`,r.created_at,`+guaranteed+`,`+chances+","+couponID+","+couponQuantity+from+filter+` ORDER BY r.created_at DESC,r.id DESC LIMIT $3 OFFSET $4`, appID, search, out.PageSize, (out.Page-1)*out.PageSize)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		r := ActivityRecord{Status: "credited"}
		targets := append([]any{&r.ID, &r.GroupID}, participantTargets(&r.Participant)...)
		targets = append(targets, &r.Amount, &r.PrizeName, &r.Remaining, &r.CreatedAt, &r.IsGuaranteed, &r.Chances, &r.CouponTemplateID, &r.CouponQuantity)
		if err = rows.Scan(targets...); err != nil {
			return err
		}
		if kind == "lottery" && r.Amount == 0 && r.Chances == 0 && r.CouponQuantity == 0 {
			r.Status = "no_prize"
		}
		out.Items = append(out.Items, r)
	}
	return rows.Err()
}

const inviteRecordFrom = ` FROM qq_bot_members m
 LEFT JOIN qq_bot_bindings b ON b.app_id=m.app_id AND b.member_id=m.member_id
 LEFT JOIN users u ON u.id=b.user_id
 LEFT JOIN qq_bot_bindings ib ON ib.app_id=m.app_id AND ib.member_id=m.inviter_id
 LEFT JOIN users iu ON iu.id=ib.user_id`

const inviteRecordFilter = ` WHERE m.app_id=$1 AND m.inviter_id<>'' AND ($2=''
 OR strpos(lower(m.member_id),lower($2))>0 OR strpos(lower(m.inviter_id),lower($2))>0
 OR strpos(lower(m.group_id),lower($2))>0 OR strpos(lower(COALESCE(b.nickname,'')),lower($2))>0
 OR strpos(lower(COALESCE(ib.nickname,'')),lower($2))>0 OR b.user_id::text=$2 OR ib.user_id::text=$2
 OR strpos(lower(COALESCE(u.email,'')),lower($2))>0 OR strpos(lower(COALESCE(iu.email,'')),lower($2))>0)`

const inviteRelationshipSelect = `SELECT m.*,COALESCE(b.user_id,0) AS uid,COALESCE(u.email,'') AS email,
 COALESCE(b.nickname,'') AS nickname,b.user_id IS NOT NULL AS bound,
 COALESCE(u.status,'') AS user_status,u.deleted_at IS NOT NULL AS user_deleted,
 COALESCE(ib.user_id,0) AS inviter_uid,COALESCE(iu.email,'') AS inviter_email,
 COALESCE(ib.nickname,'') AS inviter_nickname,ib.user_id IS NOT NULL AS inviter_bound,
 COALESCE(iu.status,'') AS inviter_status,iu.deleted_at IS NOT NULL AS inviter_deleted` + inviteRecordFrom

func listInviteRecords(ctx context.Context, tx *sql.Tx, c Config, search string, out *ActivityRecordsPage) error {
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)`+inviteRecordFrom+inviteRecordFilter, c.AppID, search).Scan(&out.Total); err != nil {
		return err
	}
	// Page relationships before looking up financial history. A reward paid in
	// another group/account relationship must not appear as a second payout here.
	return visitInviteRecords(ctx, tx, c, inviteRelationshipSelect+inviteRecordFilter+`
 ORDER BY COALESCE(m.invited_at,m.first_seen_at) DESC,m.group_id,m.member_id LIMIT $3 OFFSET $4`,
		[]any{c.AppID, search, out.PageSize, (out.Page - 1) * out.PageSize}, true, func(r ActivityRecord) {
			out.Items = append(out.Items, r)
		})
}

// Share the status and historical payout rules between admin records and the
// member's invitation summary. The caller supplies only an internal SQL query.
func visitInviteRecords(ctx context.Context, tx *sql.Tx, c Config, relationships string, args []any, includeRechargeEligibility bool, visit func(ActivityRecord)) error {
	dateParam := fmt.Sprintf("$%d::date", len(args)+1)
	args = append(args, day(time.Now()))
	qualificationSelect := "true"
	qualificationJoins := ""
	requirement := c.inviteRechargeRequirement()
	if includeRechargeEligibility && requirement.enabled() {
		if err := validateRechargeRequirement(requirement); err != nil {
			return err
		}
		windowParam := fmt.Sprintf("$%d::timestamptz", len(args)+1)
		args = append(args, time.Now().Add(-time.Duration(requirement.windowDays())*24*time.Hour))
		lifetimeParam := fmt.Sprintf("$%d::numeric", len(args)+1)
		recentParam := fmt.Sprintf("$%d::numeric", len(args)+2)
		args = append(args, requirement.MinRechargeTotal, requirement.MinRecharge30d)
		qualificationJoins = ` LEFT JOIN LATERAL (
 SELECT COALESCE(SUM(CASE
   WHEN p.status IN ('COMPLETED','REFUND_FAILED') THEN GREATEST(p.amount-p.bonus_amount,0)
   WHEN p.status='PARTIALLY_REFUNDED' AND p.amount>0 THEN ROUND(GREATEST(p.amount-p.bonus_amount,0)*GREATEST(p.amount-p.refund_amount,0)/p.amount,8)
   ELSE 0 END),0) AS total,
 COALESCE(SUM(CASE WHEN p.completed_at>=` + windowParam + ` THEN CASE
   WHEN p.status IN ('COMPLETED','REFUND_FAILED') THEN GREATEST(p.amount-p.bonus_amount,0)
   WHEN p.status='PARTIALLY_REFUNDED' AND p.amount>0 THEN ROUND(GREATEST(p.amount-p.bonus_amount,0)*GREATEST(p.amount-p.refund_amount,0)/p.amount,8)
   ELSE 0 END ELSE 0 END),0) AS recent
 FROM payment_orders p WHERE p.user_id=m.inviter_uid AND p.order_type='balance' AND p.paid_at IS NOT NULL AND p.completed_at IS NOT NULL
) recharge_orders ON true
LEFT JOIN LATERAL (
 SELECT COALESCE(SUM(r.value),0) AS total,
 COALESCE(SUM(r.value) FILTER (WHERE r.used_at>=` + windowParam + `),0) AS recent
 FROM redeem_codes r WHERE r.used_by=m.inviter_uid AND r.type='balance' AND r.status='used' AND r.used_at IS NOT NULL AND r.value>0
 AND NOT EXISTS(SELECT 1 FROM payment_orders p WHERE p.recharge_code=r.code)
) recharge_codes ON true`
		qualificationSelect = `((` + lifetimeParam + `>0 AND recharge_orders.total+recharge_codes.total>=` + lifetimeParam + `)
 OR (` + recentParam + `>0 AND recharge_orders.recent+recharge_codes.recent>=` + recentParam + `))`
	}
	rows, err := tx.QueryContext(ctx, `WITH relationships AS (`+relationships+`)
 SELECT m.group_id||':'||m.member_id,m.group_id,
 m.member_id,m.uid,m.email,m.nickname,m.bound,m.user_status,m.user_deleted,
 m.inviter_id,m.inviter_uid,m.inviter_email,m.inviter_nickname,m.inviter_bound,m.inviter_status,m.inviter_deleted,
 COALESCE(m.invited_at,m.first_seen_at),m.invited_at,m.joined_at,m.left_at,
 COALESCE(r.id,0),COALESCE(r.app_id=m.app_id AND r.group_id=m.group_id AND r.member_id=m.inviter_id,false),
 COALESCE(r.amount,0),r.created_at,COALESCE(r.user_id,0),COALESCE(ru.email,''),
 COALESCE(rc.coupon_template_id,0),COALESCE(rc.quantity,0),
 (SELECT COUNT(*) FROM qq_bot_rewards daily WHERE daily.kind='invite' AND daily.user_id=m.inviter_uid
 AND (daily.created_at AT TIME ZONE 'Asia/Shanghai')::date=`+dateParam+`),`+qualificationSelect+`
 FROM relationships m`+qualificationJoins+` LEFT JOIN LATERAL (
 SELECT r.* FROM qq_bot_rewards r WHERE r.kind='invite' AND
 ((r.app_id=m.app_id AND r.source_member_id=m.member_id) OR r.source_user_id=NULLIF(m.uid,0))
 ORDER BY r.id LIMIT 1) r ON true LEFT JOIN users ru ON ru.id=r.user_id
 LEFT JOIN qq_bot_reward_codes rc ON rc.source_key=r.reward_key||':coupon' AND rc.reward_type='coupon' AND rc.user_id=r.user_id
 ORDER BY COALESCE(m.invited_at,m.first_seen_at) DESC,m.group_id,m.member_id`, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		r := ActivityRecord{Inviter: &ActivityParticipant{}}
		var rewardID int64
		var ownReward bool
		var dailyCount int
		var rechargeEligible bool
		targets := append([]any{&r.ID, &r.GroupID}, participantTargets(&r.Participant)...)
		targets = append(targets, participantTargets(r.Inviter)...)
		targets = append(targets, &r.CreatedAt, &r.InvitedAt, &r.JoinedAt, &r.LeftAt, &rewardID, &ownReward,
			&r.Amount, &r.RewardedAt, &r.RewardUserID, &r.RewardEmail, &r.CouponTemplateID, &r.CouponQuantity, &dailyCount, &rechargeEligible)
		if err = rows.Scan(targets...); err != nil {
			return err
		}
		r.Status = invitationRecordStatus(c, r, rewardID, ownReward, dailyCount, rechargeEligible)
		if rewardID != 0 && !ownReward {
			r.Amount, r.RewardedAt, r.RewardUserID, r.RewardEmail = 0, nil, 0, ""
			r.CouponTemplateID, r.CouponQuantity = 0, 0
		}
		visit(r)
	}
	return rows.Err()
}

func invitationRecordStatus(c Config, r ActivityRecord, rewardID int64, ownReward bool, dailyCount int, rechargeEligible bool) string {
	switch {
	case rewardID != 0 && ownReward:
		return "rewarded"
	case rewardID != 0:
		return "already_rewarded"
	case r.LeftAt != nil:
		return "left_group"
	case r.JoinedAt == nil:
		return "pending_join"
	case r.InvitedAt == nil || r.InvitedAt.After(*r.JoinedAt) || !r.InvitedAt.After(r.JoinedAt.Add(-7*24*time.Hour)):
		return "invalid_invitation"
	case !r.Participant.Bound && !r.Inviter.Bound:
		return "both_unbound"
	case !r.Inviter.Bound:
		return "inviter_unbound"
	case !r.Participant.Bound:
		return "invitee_unbound"
	case r.Participant.UserStatus != "active" || r.Participant.UserDeleted || r.Inviter.UserStatus != "active" || r.Inviter.UserDeleted:
		return "account_unavailable"
	case r.Participant.UserID == r.Inviter.UserID:
		return "self_invitation"
	case !c.Enabled || !c.InviteEnabled || !c.inviteHasReward() || c.InviteDailyLimit <= 0:
		return "disabled"
	case !c.allows(r.GroupID):
		return "group_disabled"
	case dailyCount >= c.InviteDailyLimit:
		return "daily_limit"
	case !rechargeEligible:
		return "recharge_required"
	default:
		return "pending_reward"
	}
}
