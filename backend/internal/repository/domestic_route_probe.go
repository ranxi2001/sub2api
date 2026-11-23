package repository

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *accountRepository) ClaimDomesticRouteProbes(ctx context.Context) ([]service.DomesticRouteClaim, error) {
	rows, err := r.sql.QueryContext(ctx, `WITH due AS (
		SELECT id FROM accounts WHERE deleted_at IS NULL AND status='active' AND type='apikey'
		AND platform IN ('deepseek','qwen','kimi','zhipu','minimax')
		AND extra @> '{"domestic_route_auto_schedule":true}'
		AND (domestic_route_probe_at IS NULL OR domestic_route_probe_at <= now()-interval '1 hour')
		ORDER BY domestic_route_probe_at NULLS FIRST,id LIMIT 16 FOR UPDATE SKIP LOCKED
	) UPDATE accounts a SET domestic_route_probe_at=now() FROM due WHERE a.id=due.id
	RETURNING a.id,a.domestic_route_probe_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var claims []service.DomesticRouteClaim
	for rows.Next() {
		var c service.DomesticRouteClaim
		if err := rows.Scan(&c.AccountID, &c.ClaimedAt); err != nil {
			return nil, err
		}
		claims = append(claims, c)
	}
	return claims, rows.Err()
}

func (r *accountRepository) ApplyDomesticRouteProbe(ctx context.Context, c service.DomesticRouteClaim, enable bool) error {
	result, err := r.sql.ExecContext(ctx, `WITH changed AS (
		UPDATE accounts SET schedulable=$3,updated_at=now()
		WHERE id=$1 AND domestic_route_probe_at=$2 AND deleted_at IS NULL AND status='active'
		AND extra @> '{"domestic_route_auto_schedule":true}' AND schedulable IS DISTINCT FROM $3
		RETURNING id
	) INSERT INTO scheduler_outbox (event_type,account_id) SELECT $4,id FROM changed`, c.AccountID, c.ClaimedAt, enable, service.SchedulerOutboxEventAccountChanged)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n > 0 {
		r.syncSchedulerAccountSnapshot(ctx, c.AccountID)
	}
	return nil
}
