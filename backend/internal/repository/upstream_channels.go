package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"sort"
	"time"
)

const upstreamChannelColumns = `c.id,c.code,c.name,c.remark,c.revision,(SELECT count(*) FROM upstream_channel_bindings b WHERE b.channel_id=c.id)`

func scanUpstreamChannel(row interface{ Scan(...any) error }) (*service.UpstreamChannel, error) {
	c := &service.UpstreamChannel{}
	e := row.Scan(&c.ID, &c.Code, &c.Name, &c.Remark, &c.Revision, &c.Members)
	if e == sql.ErrNoRows {
		return nil, service.ErrUpstreamChannelNotFound
	}
	return c, e
}
func (r *channelRepository) GetUpstreamChannel(ctx context.Context, id int64) (*service.UpstreamChannel, error) {
	if id < 1 {
		return nil, service.ErrUpstreamChannelValidation
	}
	return scanUpstreamChannel(r.db.QueryRowContext(ctx, `SELECT `+upstreamChannelColumns+` FROM upstream_channels c WHERE c.id=$1`, id))
}
func (r *channelRepository) ListUpstreamChannels(ctx context.Context, search string, page, size int) (*service.UpstreamChannelPage, error) {
	if page < 1 || page > 1000000 || size < 1 || size > 100 || len(search) > 200 {
		return nil, service.ErrUpstreamChannelValidation
	}
	out := &service.UpstreamChannelPage{Items: []service.UpstreamChannel{}, Page: page, PageSize: size}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback() }()
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM upstream_channels WHERE $1='' OR name ILIKE '%'||$1||'%' OR code ILIKE '%'||$1||'%'`, search).Scan(&out.Total); e != nil {
		return nil, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+upstreamChannelColumns+` FROM upstream_channels c WHERE $1='' OR c.name ILIKE '%'||$1||'%' OR c.code ILIKE '%'||$1||'%' ORDER BY c.id LIMIT $2 OFFSET $3`, search, size, (page-1)*size)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		c, err := scanUpstreamChannel(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out.Items = append(out.Items, *c)
	}
	e = rows.Err()
	_ = rows.Close()
	if e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (r *channelRepository) CreateUpstreamChannel(ctx context.Context, m service.UpstreamChannelMutation) (*service.UpstreamChannel, error) {
	if e := service.ValidateUpstreamChannelMutation(&m, true); e != nil {
		return nil, e
	}
	var id int64
	e := r.db.QueryRowContext(ctx, `INSERT INTO upstream_channels(code,name,remark) VALUES($1,$2,$3) RETURNING id`, m.Code, m.Name, m.Remark).Scan(&id)
	if isUniqueViolation(e) {
		return nil, service.ErrUpstreamChannelConflict
	}
	if e != nil {
		return nil, e
	}
	return r.GetUpstreamChannel(ctx, id)
}
func (r *channelRepository) UpdateUpstreamChannel(ctx context.Context, id int64, m service.UpstreamChannelMutation) (*service.UpstreamChannel, error) {
	if id < 1 {
		return nil, service.ErrUpstreamChannelValidation
	}
	if e := service.ValidateUpstreamChannelMutation(&m, false); e != nil {
		return nil, e
	}
	var saved int64
	e := r.db.QueryRowContext(ctx, `UPDATE upstream_channels SET name=$2,remark=$3,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND revision=$4 AND ($5='' OR code=$5) RETURNING id`, id, m.Name, m.Remark, *m.Revision, m.Code).Scan(&saved)
	if e == sql.ErrNoRows || isUniqueViolation(e) {
		return nil, service.ErrUpstreamChannelConflict
	}
	if e != nil {
		return nil, e
	}
	return r.GetUpstreamChannel(ctx, saved)
}
func (r *channelRepository) DeleteUpstreamChannel(ctx context.Context, id int64, m service.UpstreamChannelMutation) error {
	if id < 1 || m.Revision == nil || *m.Revision < 1 || !m.Confirm {
		return service.ErrUpstreamChannelValidation
	}
	return r.runInTx(ctx, func(tx *sql.Tx) error {
		var rev int64
		e := tx.QueryRowContext(ctx, `SELECT revision FROM upstream_channels WHERE id=$1 FOR UPDATE`, id).Scan(&rev)
		if e == sql.ErrNoRows {
			return service.ErrUpstreamChannelNotFound
		}
		if e != nil {
			return e
		}
		if rev != *m.Revision {
			return service.ErrUpstreamChannelConflict
		}
		var n int64
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM upstream_channel_bindings WHERE channel_id=$1`, id).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			return service.ErrUpstreamChannelConflict
		}
		_, e = tx.ExecContext(ctx, `DELETE FROM upstream_channels WHERE id=$1 AND revision=$2`, id, rev)
		// An active provider link also owns its channel, even with no manual members.
		if isProviderProfitForeignKeyViolation(e) {
			return service.ErrUpstreamChannelConflict
		}
		return e
	})
}
func (r *channelRepository) ReadUpstreamChannelBindings(ctx context.Context, ids []int64) ([]service.UpstreamChannelBinding, error) {
	if len(ids) < 1 || len(ids) > 100 {
		return nil, service.ErrUpstreamChannelValidation
	}
	for _, id := range ids {
		if id < 1 {
			return nil, service.ErrUpstreamChannelValidation
		}
	}
	raw, e := json.Marshal(ids)
	if e != nil {
		return nil, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT a.id,a.name,a.deleted_at IS NOT NULL,COALESCE(b.channel_id,0),COALESCE(b.revision,0),COALESCE(c.code,''),COALESCE(c.name,''),COALESCE(c.remark,''),COALESCE(c.revision,0) FROM accounts a LEFT JOIN upstream_channel_bindings b ON b.account_id=a.id LEFT JOIN upstream_channels c ON c.id=b.channel_id WHERE a.id IN (SELECT jsonb_array_elements_text($1::jsonb)::bigint) ORDER BY a.id`, string(raw))
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	out := []service.UpstreamChannelBinding{}
	for rows.Next() {
		var b service.UpstreamChannelBinding
		var c service.UpstreamChannel
		if e = rows.Scan(&b.AccountID, &b.AccountName, &b.Deleted, &b.ChannelID, &b.Revision, &c.Code, &c.Name, &c.Remark, &c.Revision); e != nil {
			return nil, e
		}
		if b.ChannelID > 0 {
			c.ID = b.ChannelID
			b.Channel = &c
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
func (r *channelRepository) BindUpstreamChannelAccounts(ctx context.Context, m service.UpstreamChannelBindingsMutation) ([]service.UpstreamChannelBinding, error) {
	if e := service.ValidateUpstreamChannelBindings(m); e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	sort.Slice(m.Bindings, func(i, j int) bool { return m.Bindings[i].AccountID < m.Bindings[j].AccountID })
	ids := make([]int64, 0, len(m.Bindings))
	e := r.runInTx(ctx, func(tx *sql.Tx) error {
		channels := map[int64]bool{}
		if m.ChannelID > 0 {
			channels[m.ChannelID] = true
		}
		for _, b := range m.Bindings {
			if b.ChannelID > 0 {
				channels[b.ChannelID] = true
			}
		}
		ordered := make([]int64, 0, len(channels))
		for id := range channels {
			ordered = append(ordered, id)
		}
		sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
		for _, id := range ordered {
			var found int64
			e := tx.QueryRowContext(ctx, `SELECT id FROM upstream_channels WHERE id=$1 FOR UPDATE`, id).Scan(&found)
			if e == sql.ErrNoRows {
				return service.ErrUpstreamChannelConflict
			}
			if e != nil {
				return e
			}
		}
		for _, b := range m.Bindings {
			var deleted bool
			e := tx.QueryRowContext(ctx, `SELECT deleted_at IS NOT NULL FROM accounts WHERE id=$1 FOR UPDATE`, b.AccountID).Scan(&deleted)
			if e == sql.ErrNoRows {
				return service.ErrUpstreamChannelNotFound
			}
			if e != nil {
				return e
			}
			if deleted && m.ChannelID != 0 {
				return service.ErrUpstreamChannelValidation
			}
			var revision, old int64
			e = tx.QueryRowContext(ctx, `SELECT revision,COALESCE(channel_id,0) FROM upstream_channel_bindings WHERE account_id=$1`, b.AccountID).Scan(&revision, &old)
			if e != nil && e != sql.ErrNoRows {
				return e
			}
			if revision != b.Revision || old != b.ChannelID {
				return service.ErrUpstreamChannelConflict
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO upstream_channel_bindings(account_id,channel_id,revision) VALUES($1,NULLIF($2::bigint,0),1) ON CONFLICT(account_id) DO UPDATE SET channel_id=EXCLUDED.channel_id,revision=upstream_channel_bindings.revision+1,updated_at=CURRENT_TIMESTAMP`, b.AccountID, m.ChannelID)
			if e != nil {
				return e
			}
			ids = append(ids, b.AccountID)
		}
		for _, id := range ordered {
			if _, e := tx.ExecContext(ctx, `UPDATE upstream_channels SET revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, id); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	return r.ReadUpstreamChannelBindings(ctx, ids)
}
func (r *channelRepository) UpstreamChannelMembers(ctx context.Context, id int64, page, size int) (*service.UpstreamChannelMembers, error) {
	if id < 1 || page < 1 || page > 1000000 || size < 1 || size > 100 {
		return nil, service.ErrUpstreamChannelValidation
	}
	out := &service.UpstreamChannelMembers{Items: []service.UpstreamChannelBinding{}}
	if e := r.db.QueryRowContext(ctx, `SELECT count(*) FROM upstream_channel_bindings WHERE channel_id=$1`, id).Scan(&out.Total); e != nil {
		return nil, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT a.id,a.name,a.deleted_at IS NOT NULL,b.channel_id,b.revision FROM upstream_channel_bindings b JOIN accounts a ON a.id=b.account_id WHERE b.channel_id=$1 ORDER BY a.id LIMIT $2 OFFSET $3`, id, size, (page-1)*size)
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var b service.UpstreamChannelBinding
		if e = rows.Scan(&b.AccountID, &b.AccountName, &b.Deleted, &b.ChannelID, &b.Revision); e != nil {
			return nil, e
		}
		out.Items = append(out.Items, b)
	}
	return out, rows.Err()
}

var _ service.UpstreamChannelRepository = (*channelRepository)(nil)
