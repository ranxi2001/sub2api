package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// This directory identifies possible ownership for reports and bill matching,
// never for routing. A URL alone neither proves a charge nor breaks a tie.
type profitURLProvider struct {
	ProviderID, ChannelID, Revision          int64
	OriginHash, Name, Code, Remark, SiteType string
}

func profitURLDirectory(ctx context.Context, tx *sql.Tx) (map[string]profitURLProvider, error) {
	rows, err := tx.QueryContext(ctx, `SELECT p.base_url,p.id,c.id,c.revision,provider_profit_origin(p.base_url),c.name,c.code,c.remark,p.site_type
		FROM mature_providers p JOIN provider_profit_links l ON l.provider_id=p.id JOIN upstream_channels c ON c.id=l.channel_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	directory := make(map[string]profitURLProvider)
	seen := make(map[string]bool)
	for rows.Next() {
		var base string
		var p profitURLProvider
		if err := rows.Scan(&base, &p.ProviderID, &p.ChannelID, &p.Revision, &p.OriginHash, &p.Name, &p.Code, &p.Remark, &p.SiteType); err != nil {
			return nil, err
		}
		key := service.ProviderProfitSiteKey(base)
		if key == "" {
			continue
		}
		if seen[key] {
			delete(directory, key)
			continue
		}
		seen[key] = true
		directory[key] = p
	}
	return directory, rows.Err()
}

// ProfitChannelAccountIDs uses the same current ownership projection as the
// report, without aggregating usage or requesting a provider wallet.
func (r *channelRepository) ProfitChannelAccountIDs(ctx context.Context, channel int64) ([]int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	raw, err := profitOperationsAccounts(ctx, tx, service.UpstreamOperationsFilter{})
	if err != nil {
		return nil, err
	}
	var accounts []struct {
		ID        int64 `json:"id"`
		ChannelID int64 `json:"channel_id"`
		Deleted   bool  `json:"deleted"`
	}
	if err = json.Unmarshal(raw, &accounts); err != nil {
		return nil, err
	}
	ids := make([]int64, 0)
	for _, account := range accounts {
		if !account.Deleted && account.ChannelID == channel {
			ids = append(ids, account.ID)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}
