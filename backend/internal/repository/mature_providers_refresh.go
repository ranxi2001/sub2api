package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *channelRepository) ClaimMatureProviderRefresh(parent context.Context) (int64, int64, error) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	var id, revision int64
	err := r.db.QueryRowContext(ctx, `WITH due AS (SELECT id FROM mature_providers WHERE refresh_interval_seconds>0 AND credential_cipher<>'' AND next_refresh_at<=NOW() ORDER BY next_refresh_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE mature_providers p SET next_refresh_at=NOW()+make_interval(secs=>p.refresh_interval_seconds) FROM due WHERE p.id=due.id RETURNING p.id,p.revision`).Scan(&id, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	return id, revision, err
}

func updateMatureProviderAlert(ctx context.Context, tx *sql.Tx, p *service.MatureProvider) error {
	// Failed observations preserve the previous episode. Unknown is neither
	// recovered nor a zero balance; threshold comparisons retain source units.
	if p.Status != "connected" || p.Metrics.Balance == nil {
		return nil
	}
	if p.LowBalanceThreshold == nil || p.SuppressLowBalanceAlert || *p.Metrics.Balance >= *p.LowBalanceThreshold {
		_, err := tx.ExecContext(ctx, `UPDATE mature_provider_alerts SET resolved_at=clock_timestamp() WHERE provider_id=$1 AND resolved_at IS NULL`, p.ID)
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO mature_provider_alerts(provider_id,balance,threshold,currency,opened_at,last_seen_at) VALUES($1,$2,$3,$4,clock_timestamp(),COALESCE($5,clock_timestamp())) ON CONFLICT(provider_id) WHERE resolved_at IS NULL DO UPDATE SET balance=EXCLUDED.balance,threshold=EXCLUDED.threshold,currency=EXCLUDED.currency,last_seen_at=GREATEST(mature_provider_alerts.last_seen_at,EXCLUDED.last_seen_at)`, p.ID, *p.Metrics.Balance, *p.LowBalanceThreshold, p.Metrics.Currency, p.ObservedAt)
	return err
}
