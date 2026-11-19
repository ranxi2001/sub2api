package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// Select individual public fields; never select raw metrics, dashboard account,
// observations' errors, URL, key provenance, or encrypted credentials.
const providerProfitLinkSelect = `SELECT l.cost_mode,p.id,p.name,p.site_type,c.id,c.name,l.revision,p.observed_at,p.status,
 CASE WHEN jsonb_typeof(p.metrics->'today_spend')='number' THEN p.metrics->>'today_spend' END,
 CASE WHEN jsonb_typeof(p.metrics->'total_spend')='number' THEN p.metrics->>'total_spend' END,
 CASE WHEN p.metrics->>'currency' IN ('USD','CNY','quota_units') THEN p.metrics->>'currency' ELSE '' END
 FROM provider_profit_links l JOIN mature_providers p ON p.id=l.provider_id JOIN upstream_channels c ON c.id=l.channel_id`

func scanProviderProfitLink(row interface{ Scan(...any) error }) (*service.ProviderProfitLink, error) {
	var v service.ProviderProfitLink
	err := row.Scan(&v.CostMode, &v.ProviderID, &v.ProviderName, &v.SiteType, &v.ChannelID, &v.ChannelName, &v.Revision, &v.ObservedAt, &v.Status, &v.Metrics.TodaySpend, &v.Metrics.TotalSpend, &v.Metrics.Currency)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrProviderProfitLinkNotFound
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (r *channelRepository) ListProviderProfitLinks(ctx context.Context, page, size int) (*service.ProviderProfitLinkPage, error) {
	if err := service.ValidateProviderProfitLinkPage(page, size); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	out := &service.ProviderProfitLinkPage{Items: []service.ProviderProfitLink{}, Page: page, PageSize: size}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM provider_profit_links`).Scan(&out.Total); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, providerProfitLinkSelect+` ORDER BY p.id LIMIT $1 OFFSET $2`, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		v, err := scanProviderProfitLink(rows)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, *v)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func isProviderProfitForeignKeyViolation(err error) bool {
	var p *pq.Error
	if errors.As(err, &p) && (p.Code == "23503" || p.Code == "23001" || strings.HasPrefix(p.Constraint, "provider_profit_")) {
		return true
	}
	// PostgreSQL 18 reports ON DELETE RESTRICT as restrict_violation (23001).
	// Match the constraint text if the driver does not surface that code.
	return err != nil && strings.Contains(err.Error(), "provider_profit_links_channel_id_fkey")
}

func (r *channelRepository) SetProviderProfitLink(ctx context.Context, id int64, m service.ProviderProfitLinkMutation) (*service.ProviderProfitLink, error) {
	if err := m.Validate(id); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var out *service.ProviderProfitLink
	err := r.runInTx(ctx, func(tx *sql.Tx) error {
		var channel, revision int64
		var costMode string
		err := tx.QueryRowContext(ctx, `SELECT channel_id,revision,cost_mode FROM provider_profit_links WHERE provider_id=$1 FOR UPDATE`, id).Scan(&channel, &revision, &costMode)
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrProviderProfitLinkNotFound
		}
		if err != nil {
			return err
		}
		if m.Revision != revision {
			return service.ErrProviderProfitLinkConflict
		}
		if m.CostMode != nil {
			costMode = *m.CostMode
		}
		if m.ChannelID != channel || m.CostMode != nil {
			_, err = tx.ExecContext(ctx, `UPDATE provider_profit_links SET channel_id=$2,cost_mode=$3,revision=revision+1 WHERE provider_id=$1`, id, m.ChannelID, costMode)
			if isUniqueViolation(err) {
				return service.ErrProviderProfitLinkConflict
			}
			if isProviderProfitForeignKeyViolation(err) {
				return service.ErrProviderProfitLinkNotFound
			}
			if err != nil {
				return err
			}
		}
		out, err = scanProviderProfitLink(tx.QueryRowContext(ctx, providerProfitLinkSelect+` WHERE p.id=$1`, id))
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

var _ service.ProviderProfitLinkRepository = (*channelRepository)(nil)
