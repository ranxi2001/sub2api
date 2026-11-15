package qqbot

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var ErrRewardCodeRequest = errors.New("奖励记录参数不正确或记录状态已变化，请刷新后重试")

type RewardCode struct {
	CouponTemplateID  int64      `json:"coupon_template_id,omitempty"`
	SourceKind        string     `json:"source_kind"`
	ID                int64      `json:"id"`
	AppID             string     `json:"app_id"`
	CodeHint          string     `json:"code_hint"`
	BatchName         string     `json:"batch_name"`
	RewardType        string     `json:"reward_type"`
	Amount            float64    `json:"amount"`
	Quantity          int        `json:"quantity"`
	PrizeName         string     `json:"prize_name"`
	ClaimInstructions string     `json:"claim_instructions"`
	TargetUserID      *int64     `json:"target_user_id"`
	ExpiresAt         *time.Time `json:"expires_at"`
	Status            string     `json:"status"`
	UserID            *int64     `json:"user_id"`
	Email             string     `json:"email"`
	MemberID          string     `json:"member_id"`
	RedeemedAt        *time.Time `json:"redeemed_at"`
	FulfilledAt       *time.Time `json:"fulfilled_at"`
	FulfillmentNote   string     `json:"fulfillment_note"`
	CreatedAt         time.Time  `json:"created_at"`
}
type RewardCodesPage struct {
	Items    []RewardCode `json:"items"`
	Total    int          `json:"total"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
}

// Retired redemption messages may still be sent from old cards. Remove every
// copy of the code before persistence even though the command cannot execute.
func redactRewardCommand(e Event) Event {
	cmd, _ := command(e.Content)
	cleanCommand := "/奖励兑换"
	if cmd == "福利" {
		cleanCommand = "/福利 兑换"
	}
	return Event{ID: e.ID, GroupID: e.GroupID, Author: e.Author, Mentions: e.Mentions,
		Content: mentionPrefix.FindString(strings.TrimSpace(e.Content)) + cleanCommand}
}

func containsRewardCode(content string) bool {
	content = strings.TrimSpace(mentionPrefix.ReplaceAllString(strings.TrimSpace(content), ""))
	cmd, arg := command("/" + strings.TrimPrefix(content, "/"))
	action, _ := command("/" + arg)
	return cmd == "奖励兑换" || (cmd == "福利" && action == "兑换")
}

func (s *Service) ListRewardCodes(ctx context.Context, search, status string, page int) (RewardCodesPage, error) {
	out := RewardCodesPage{Items: []RewardCode{}, Page: page, PageSize: 20}
	if page < 1 || page > 1000000 || len(search) > 254 {
		return out, ErrRewardCodeRequest
	}
	switch status {
	case "", "unused", "disabled", "pending", "fulfilled", "expired":
	default:
		return out, ErrRewardCodeRequest
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	const from = ` FROM qq_bot_reward_codes r LEFT JOIN users u ON u.id=r.user_id WHERE
 ($1='' OR strpos(lower(r.batch_name),lower($1))>0 OR strpos(lower(r.code_hint),lower($1))>0 OR r.id::text=$1 OR r.user_id::text=$1 OR r.target_user_id::text=$1 OR strpos(lower(COALESCE(u.email,'')),lower($1))>0)
 AND ($2='' OR CASE WHEN r.status='unused' AND r.expires_at<=NOW() THEN 'expired' ELSE r.status END=$2)`
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*)`+from, search, status).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.app_id,r.code_hint,r.batch_name,r.reward_type,r.amount,r.quantity,r.prize_name,r.claim_instructions,r.target_user_id,r.expires_at,
 CASE WHEN r.status='unused' AND r.expires_at<=NOW() THEN 'expired' ELSE r.status END,r.user_id,COALESCE(u.email,''),r.member_id,r.redeemed_at,r.fulfilled_at,r.fulfillment_note,r.created_at,r.source_kind,COALESCE(r.coupon_template_id,0)`+from+` ORDER BY r.id DESC LIMIT 20 OFFSET $3`, search, status, (page-1)*20)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var r RewardCode
		if err = rows.Scan(&r.ID, &r.AppID, &r.CodeHint, &r.BatchName, &r.RewardType, &r.Amount, &r.Quantity, &r.PrizeName, &r.ClaimInstructions, &r.TargetUserID, &r.ExpiresAt, &r.Status, &r.UserID, &r.Email, &r.MemberID, &r.RedeemedAt, &r.FulfilledAt, &r.FulfillmentNote, &r.CreatedAt, &r.SourceKind, &r.CouponTemplateID); err != nil {
			return out, err
		}
		out.Items = append(out.Items, r)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (s *Service) UpdateRewardCode(ctx context.Context, id int64, action, note string) error {
	if id < 1 || len([]rune(note)) > 500 {
		return ErrRewardCodeRequest
	}
	var query string
	switch action {
	case "disable":
		query = `UPDATE qq_bot_reward_codes SET status='disabled' WHERE id=$1 AND status='unused'`
	case "fulfill":
		query = `UPDATE qq_bot_reward_codes SET status='fulfilled',fulfilled_at=NOW(),fulfillment_note=$2 WHERE id=$1 AND status='pending' AND reward_type IN ('physical','other')`
	default:
		return ErrRewardCodeRequest
	}
	args := []any{id}
	if action == "fulfill" {
		args = append(args, strings.TrimSpace(note))
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrRewardCodeRequest
	}
	return nil
}
