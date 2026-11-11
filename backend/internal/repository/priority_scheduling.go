package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// Performance has one account-wide source, shared with account management and
// the performance monitor. Only financial history retains model/group scope.
func (r *usageLogRepository) ReadPrioritySchedulingSignals(ctx context.Context, query service.PrioritySchedulingQuery) (map[int64]service.PrioritySchedulingSignal, error) {
	out := make(map[int64]service.PrioritySchedulingSignal, len(query.AccountIDs))
	if len(query.AccountIDs) == 0 {
		return out, nil
	}
	if query.UsageUntil.IsZero() {
		query.UsageUntil = time.Now()
	}
	performance, err := readAccountPerformance(ctx, r.sql, query.AccountIDs, query.UsageUntil)
	if err != nil {
		return nil, err
	}
	for _, id := range query.AccountIDs {
		metrics, ok := performance[id]
		if !ok {
			continue
		}
		out[id] = service.PrioritySchedulingSignal{Performance: &metrics}
	}
	rows, err := r.sql.QueryContext(ctx, `
SELECT account_id, COUNT(*), COALESCE(SUM(actual_cost),0),
 COALESCE(SUM(COALESCE(account_stats_cost,total_cost)),0)
FROM usage_logs
WHERE account_id=ANY($1) AND created_at >= $2 AND created_at <= $3
 AND COALESCE(NULLIF(requested_model,''),model)=$4
 AND group_id IS NOT DISTINCT FROM $5::bigint
 AND ($6::text IS NULL OR COALESCE(reasoning_effort,'')=$6)
 AND (actual_cost>0 OR total_cost>0 OR account_stats_cost>0
  OR input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens>0)
GROUP BY account_id`, pq.Array(query.AccountIDs), query.UsageSince, query.UsageUntil, query.Model, query.GroupID, query.ObservationEffort)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var financial service.PrioritySchedulingSignal
		if err := rows.Scan(&id, &financial.ProfitSamples, &financial.Revenue, &financial.BaseCost); err != nil {
			return nil, err
		}
		if signal, ok := out[id]; ok {
			signal.ProfitSamples, signal.Revenue, signal.BaseCost = financial.ProfitSamples, financial.Revenue, financial.BaseCost
			out[id] = signal
		}
	}
	return out, rows.Err()
}
