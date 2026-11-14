package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const matureProviderColumns = `refresh_interval_seconds,auth_mode,account,user_id,(credential_cipher<>''),last_error,id,revision,name,site_type,base_url,notes,cost_multiplier,low_balance_threshold,suppress_low_balance_alert,status,metrics,observed_at,updated_at`

func scanMatureProvider(row interface{ Scan(...any) error }, extra ...any) (*service.MatureProvider, error) {
	var p service.MatureProvider
	var raw []byte
	args := []any{&p.RefreshIntervalSeconds, &p.AuthMode, &p.Account, &p.UserID, &p.HasCredential, &p.LastError, &p.ID, &p.Revision, &p.Name, &p.SiteType, &p.BaseURL, &p.Notes, &p.CostMultiplier, &p.LowBalanceThreshold, &p.SuppressLowBalanceAlert, &p.Status, &raw, &p.ObservedAt, &p.UpdatedAt}
	err := row.Scan(append(args, extra...)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrMatureProviderNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &p.Metrics); err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *channelRepository) ListMatureProviders(parent context.Context, search, status string, page, size int) (service.MatureProviderPage, error) {
	out := service.MatureProviderPage{Items: []service.MatureProvider{}, Page: page, PageSize: size}
	if page < 1 || page > 1000000 || size < 1 || size > 100 || len(search) > 120 {
		return out, service.ErrMatureProviderValidation
	}
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	// Count and page share one snapshot even while another instance writes.
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	where := ` WHERE ($1='' OR strpos(lower(name),lower($1))>0) AND ($2='' OR status=$2)`
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mature_providers`+where, search, status).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+matureProviderColumns+` FROM mature_providers`+where+` ORDER BY id DESC LIMIT $3 OFFSET $4`, search, status, size, (page-1)*size)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		p, e := scanMatureProvider(rows)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, *p)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if err = rows.Close(); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (r *channelRepository) GetMatureProvider(parent context.Context, id int64) (*service.MatureProvider, error) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	return scanMatureProvider(r.db.QueryRowContext(ctx, `SELECT `+matureProviderColumns+` FROM mature_providers WHERE id=$1`, id))
}

func (r *channelRepository) CreateMatureProvider(parent context.Context, in service.MatureProviderInput) (*service.MatureProvider, error) {
	if err := service.ValidateMatureProviderInput(&in); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	return scanMatureProvider(r.db.QueryRowContext(ctx, `INSERT INTO mature_providers(name,site_type,base_url,notes,cost_multiplier,low_balance_threshold,suppress_low_balance_alert,auth_mode,account,user_id,credential_cipher,refresh_interval_seconds) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,COALESCE($11,''),COALESCE($12,0)) RETURNING `+matureProviderColumns, in.Name, in.SiteType, in.BaseURL, in.Notes, in.CostMultiplier, in.LowBalanceThreshold, in.SuppressLowBalanceAlert, in.AuthMode, in.Account, in.UserID, in.CredentialCipher, in.RefreshIntervalSeconds))
}

func (r *channelRepository) UpdateMatureProvider(parent context.Context, id int64, in service.MatureProviderInput) (*service.MatureProvider, error) {
	if err := service.ValidateMatureProviderInput(&in); err != nil {
		return nil, err
	}
	if id < 1 || in.Revision == nil || *in.Revision < 1 {
		return nil, service.ErrMatureProviderValidation
	}
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// A single conditional update is the cross-instance compare-and-swap. An
	// identity change invalidates observations; it never carries old metrics.
	p, err := scanMatureProvider(tx.QueryRowContext(ctx, `UPDATE mature_providers SET
 status=CASE WHEN base_url<>$5 OR site_type<>$4 OR auth_mode<>$10 OR account<>$11 OR user_id<>$12 OR $13::text IS NOT NULL THEN 'unknown' ELSE status END,
 metrics=CASE WHEN base_url<>$5 OR site_type<>$4 OR auth_mode<>$10 OR account<>$11 OR user_id<>$12 OR $13::text IS NOT NULL THEN '{"balance":null,"today_spend":null,"total_spend":null,"total_recharge":null,"currency":""}'::jsonb ELSE metrics END,
 observed_at=CASE WHEN base_url<>$5 OR site_type<>$4 OR auth_mode<>$10 OR account<>$11 OR user_id<>$12 OR $13::text IS NOT NULL THEN NULL ELSE observed_at END,
 credential_cipher=COALESCE($13,CASE WHEN base_url<>$5 OR site_type<>$4 OR auth_mode<>$10 OR account<>$11 OR user_id<>$12 OR $13::text IS NOT NULL THEN '' ELSE credential_cipher END),
 last_error=CASE WHEN base_url<>$5 OR site_type<>$4 OR auth_mode<>$10 OR account<>$11 OR user_id<>$12 OR $13::text IS NOT NULL THEN '' ELSE last_error END,
 refresh_interval_seconds=COALESCE($14,refresh_interval_seconds),auth_mode=$10,account=$11,user_id=$12,name=$3,site_type=$4,base_url=$5,notes=$6,cost_multiplier=$7,low_balance_threshold=$8,suppress_low_balance_alert=$9,revision=revision+1,updated_at=NOW()
 WHERE id=$1 AND revision=$2 RETURNING `+matureProviderColumns, id, *in.Revision, in.Name, in.SiteType, in.BaseURL, in.Notes, in.CostMultiplier, in.LowBalanceThreshold, in.SuppressLowBalanceAlert, in.AuthMode, in.Account, in.UserID, in.CredentialCipher, in.RefreshIntervalSeconds))
	if errors.Is(err, service.ErrMatureProviderNotFound) {
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mature_providers WHERE id=$1)`, id).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, service.ErrMatureProviderNotFound
		}
		return nil, service.ErrMatureProviderConflict
	}
	if err != nil {
		return nil, err
	}
	// Identity/credential changes invalidate observations and their episode.
	// Resolve old alerts in the same transaction as the configuration update.
	if p.Status == "unknown" || p.LowBalanceThreshold == nil || p.SuppressLowBalanceAlert {
		if _, err = tx.ExecContext(ctx, `UPDATE mature_provider_alerts SET resolved_at=clock_timestamp() WHERE provider_id=$1 AND resolved_at IS NULL`, id); err != nil {
			return nil, err
		}
	} else if err = updateMatureProviderAlert(ctx, tx, p); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return p, nil
}

func (r *channelRepository) matureProviderMissingRevision(ctx context.Context, id int64) error {
	var exists bool
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mature_providers WHERE id=$1)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return service.ErrMatureProviderNotFound
	}
	return service.ErrMatureProviderConflict
}

func (r *channelRepository) DeleteMatureProvider(parent context.Context, id, revision int64) error {
	if id < 1 || revision < 1 {
		return service.ErrMatureProviderValidation
	}
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	result, err := r.db.ExecContext(ctx, `DELETE FROM mature_providers WHERE id=$1 AND revision=$2`, id, revision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return r.matureProviderMissingRevision(ctx, id)
	}
	return nil
}
