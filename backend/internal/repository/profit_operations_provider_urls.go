package repository

import (
	"context"
	"database/sql"

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
