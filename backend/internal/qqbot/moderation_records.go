package qqbot

import (
	"context"
	"database/sql"
	"errors"
)

type ModerationPage struct {
	Items    []ModerationRecord `json:"items"`
	Total    int                `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
}

// Filter incidents before pagination; operation status matches any operation of an incident.
func (s *Service) ListModerationRecords(ctx context.Context, f JobFilter) (ModerationPage, error) {
	out := ModerationPage{Items: []ModerationRecord{}, Page: f.Page, PageSize: f.PageSize}
	if f.Page < 1 || f.Page > 100000 || f.PageSize < 1 || f.PageSize > 100 {
		return out, errors.New("分页参数不正确")
	}
	for _, v := range []string{f.GroupID, f.MemberID, f.Status} {
		if len(v) > 128 {
			return out, errors.New("筛选条件过长")
		}
	}
	from, to, err := recordTimeRange(f.From, f.To)
	if err != nil {
		return out, err
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
	filter := ` FROM qq_bot_moderation_incidents i WHERE app_id=$1 AND sandbox=$2
 AND ($3='' OR group_id=$3) AND ($4='' OR member_id=$4)
 AND ($5='' OR EXISTS(SELECT 1 FROM qq_bot_moderation_operations o WHERE o.incident_id=i.id AND o.status=$5))
 AND ($6::timestamptz IS NULL OR i.created_at >= $6) AND ($7::timestamptz IS NULL OR i.created_at <= $7)`
	args := []any{c.AppID, c.Sandbox, f.GroupID, f.MemberID, f.Status, from, to}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*)`+filter, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT i.id,i.group_id,i.member_id,i.reason,i.mute_seconds,i.created_at,
 COALESCE(MAX(o.status) FILTER(WHERE o.kind='mute'),''),COUNT(o.id) FILTER(WHERE o.kind='recall'),
 COUNT(o.id) FILTER(WHERE o.kind='recall' AND o.status='succeeded'),COUNT(o.id) FILTER(WHERE o.status IN ('queued','started')),
 COALESCE(string_agg(DISTINCT NULLIF(o.error,''),'; '),'')
 FROM (SELECT i.*`+filter+` ORDER BY created_at DESC,id DESC LIMIT $8 OFFSET $9) i
 LEFT JOIN qq_bot_moderation_operations o ON o.incident_id=i.id
 GROUP BY i.id,i.group_id,i.member_id,i.reason,i.mute_seconds,i.created_at ORDER BY i.created_at DESC,i.id DESC`, append(args, f.PageSize, (f.Page-1)*f.PageSize)...)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var r ModerationRecord
		if err = rows.Scan(&r.ID, &r.GroupID, &r.MemberID, &r.Reason, &r.MuteSeconds, &r.CreatedAt, &r.MuteStatus, &r.RecallTotal, &r.RecallSucceeded, &r.Pending, &r.Error); err != nil {
			return out, err
		}
		out.Items = append(out.Items, r)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
