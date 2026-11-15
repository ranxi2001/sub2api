package repository

import (
	"context"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Reuse the provider catalog's metadata. Credentials, login sessions and
// upstream HTTP requests remain exclusively owned by the provider module.
func (r *accountRepository) ReadAccountOpsProviders(ctx context.Context, ids []int64) ([]service.MatureProvider, error) {
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT id,name,status,metrics,observed_at,refresh_interval_seconds FROM mature_providers WHERE id IN (SELECT jsonb_array_elements_text($1::jsonb)::bigint)`, string(raw))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	providers := []service.MatureProvider{}
	for rows.Next() {
		var p service.MatureProvider
		var data []byte
		if err = rows.Scan(&p.ID, &p.Name, &p.Status, &data, &p.ObservedAt, &p.RefreshIntervalSeconds); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &p.Metrics); err != nil {
			return nil, err
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}
