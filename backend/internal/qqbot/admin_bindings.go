package qqbot

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type AdminBinding struct {
	Binding
	Email       string `json:"email"`
	UserStatus  string `json:"user_status"`
	UserDeleted bool   `json:"user_deleted"`
}

type AdminBindingsPage struct {
	Items    []AdminBinding `json:"items"`
	Total    int            `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
}

func (s *Service) ListBindings(ctx context.Context, search string, page, pageSize int) (AdminBindingsPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	out := AdminBindingsPage{Items: []AdminBinding{}, Page: page, PageSize: pageSize}
	c, err := s.Config(ctx)
	if err != nil {
		return out, err
	}
	search = strings.TrimSpace(search)
	// Literal substring matching keeps wildcard characters in email addresses literal.
	const filter = ` FROM qq_bot_bindings b JOIN users u ON u.id=b.user_id WHERE b.app_id=$1
AND ($2='' OR strpos(lower(u.email),lower($2))>0 OR strpos(lower(b.nickname),lower($2))>0
OR strpos(lower(b.member_id),lower($2))>0 OR strpos(lower(b.group_id),lower($2))>0 OR b.user_id::text=$2)`
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*)`+filter, c.AppID, search).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT b.app_id,b.member_id,b.user_id,b.nickname,b.group_id,b.created_at,u.email,u.status,u.deleted_at IS NOT NULL`+filter+` ORDER BY b.created_at DESC,b.member_id LIMIT $3 OFFSET $4`, c.AppID, search, pageSize, (page-1)*pageSize)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var b AdminBinding
		if err = rows.Scan(&b.AppID, &b.MemberID, &b.UserID, &b.Nickname, &b.GroupID, &b.CreatedAt, &b.Email, &b.UserStatus, &b.UserDeleted); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Items = append(out.Items, b)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

var ErrBindingChanged = errors.New("绑定已解除或已发生变化，请刷新列表后重试")

// The complete observed identity prevents a stale admin page from deleting a
// newer binding, including an identical account/member pair bound again later.
type UnbindRequest struct {
	AppID     string    `json:"app_id"`
	MemberID  string    `json:"member_id"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Service) Unbind(ctx context.Context, user int64, req UnbindRequest) error {
	if user <= 0 || req.AppID == "" || req.MemberID == "" || req.CreatedAt.IsZero() {
		return ErrBindingChanged
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Match binding confirmations and reward settlement locks. Rewards that
	// started first finish before the unbind; later ones see no binding.
	for _, seed := range []int{273, 272} {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,$2))`, req.AppID, seed); err != nil {
			return err
		}
	}
	var member string
	err = tx.QueryRowContext(ctx, `DELETE FROM qq_bot_bindings WHERE app_id=$1 AND user_id=$2 AND member_id=$3 AND created_at=$4 RETURNING member_id`, req.AppID, user, req.MemberID, req.CreatedAt).Scan(&member)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBindingChanged
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM qq_bot_bind_requests WHERE app_id=$1 AND (user_id=$2 OR member_id=$3)`, req.AppID, user, member); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM qq_bot_email_bind_requests WHERE app_id=$1 AND (user_id=$2 OR member_id=$3)`, req.AppID, user, member); err != nil {
		return err
	}
	// Keep reward eligibility, invitation events, and financial history intact.
	return tx.Commit()
}
