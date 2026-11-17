package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *channelRepository) ListProviderSpendTargets(ctx context.Context) ([]service.ProviderSpendTarget, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT p.id, p.revision FROM mature_providers p
		JOIN provider_profit_links l ON l.provider_id=p.id
		WHERE p.credential_cipher<>''
		ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []service.ProviderSpendTarget
	for rows.Next() {
		var target service.ProviderSpendTarget
		if err = rows.Scan(&target.ID, &target.Revision); err != nil {
			return nil, err
		}
		out = append(out, target)
	}
	return out, rows.Err()
}

func (r *channelRepository) ProviderSpendFresh(ctx context.Context, providerID int64, from, until time.Time, within time.Duration) (bool, error) {
	var fresh bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM provider_spend_snapshots
		WHERE provider_id=$1 AND window_from=$2 AND window_until=$3
		  AND amount IS NOT NULL AND observed_at >= $4)`,
		providerID, from, until, time.Now().Add(-within)).Scan(&fresh)
	return fresh, err
}

func (r *channelRepository) SaveProviderSpend(ctx context.Context, snap service.ProviderSpendSnapshot) error {
	if snap.Amount == nil || !snap.Until.After(snap.From) {
		return service.ErrMatureProviderValidation
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO provider_spend_snapshots
		(provider_id, window_from, window_until, amount, currency, observed_at, source, last_error, last_error_at)
		VALUES ($1,$2,$3,$4,$5,clock_timestamp(),$6,'',NULL)
		ON CONFLICT (provider_id, window_from, window_until) DO UPDATE SET
		  amount=EXCLUDED.amount, currency=EXCLUDED.currency, observed_at=EXCLUDED.observed_at,
		  source=EXCLUDED.source, last_error='', last_error_at=NULL`,
		snap.ProviderID, snap.From, snap.Until, *snap.Amount, snap.Currency, snap.Source)
	return err
}

func (r *channelRepository) RecordProviderSpendError(ctx context.Context, providerID int64, from, until time.Time, message string) error {
	if message == "" || !until.After(from) {
		return nil
	}
	if len(message) > 500 {
		message = message[:500]
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO provider_spend_snapshots
		(provider_id, window_from, window_until, amount, currency, observed_at, source, last_error, last_error_at)
		VALUES ($1,$2,$3,NULL,'',NULL,'',$4,clock_timestamp())
		ON CONFLICT (provider_id, window_from, window_until) DO UPDATE SET
		  last_error=EXCLUDED.last_error, last_error_at=EXCLUDED.last_error_at`,
		providerID, from, until, message)
	return err
}
