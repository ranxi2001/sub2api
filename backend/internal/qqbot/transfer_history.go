package qqbot

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type TransferReceipt struct {
	ID         int64     `json:"id"`
	FromUserID int64     `json:"from_user_id"`
	ToUserID   int64     `json:"to_user_id"`
	FromEmail  string    `json:"from_email"`
	ToEmail    string    `json:"to_email"`
	Amount     string    `json:"amount"`
	Fee        string    `json:"fee"`
	NetAmount  string    `json:"net_amount"`
	CreatedAt  time.Time `json:"created_at"`
}
type TransferHistory struct {
	Items    []TransferReceipt `json:"items"`
	Total    int               `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

func (s *Service) TransferHistory(ctx context.Context, search string, page int) (TransferHistory, error) {
	out := TransferHistory{Items: []TransferReceipt{}, Page: page, PageSize: 20}
	if page < 1 || page > 100000 || len(search) > 254 {
		return out, errors.New("查询参数不正确")
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
	where := ` FROM qq_bot_transfer_history WHERE app_id=$1 AND ($2='' OR from_email ILIKE '%'||$2||'%' OR to_email ILIKE '%'||$2||'%' OR from_user_id::text=$2 OR to_user_id::text=$2)`
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*)`+where, c.AppID, search).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,from_user_id,to_user_id,from_email,to_email,amount::text,fee::text,net_amount::text,created_at`+where+` ORDER BY created_at DESC,id DESC LIMIT 20 OFFSET $3`, c.AppID, search, (page-1)*20)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var r TransferReceipt
		if err = rows.Scan(&r.ID, &r.FromUserID, &r.ToUserID, &r.FromEmail, &r.ToEmail, &r.Amount, &r.Fee, &r.NetAmount, &r.CreatedAt); err != nil {
			return out, err
		}
		out.Items = append(out.Items, r)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
