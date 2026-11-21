package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// The rule predicates on each source are identical to the dashboard predicates.
func (r *opsRepository) GetAlertPlatformStats(ctx context.Context, filter service.OpsDashboardFilter) ([]service.OpsAlertPlatformStats, error) {
	start, end := filter.StartTime, filter.EndTime
	if start.IsZero() || !end.After(start) || end.Sub(start) > 24*time.Hour {
		return nil, fmt.Errorf("invalid alert statistics scope")
	}
	join, usageWhere, args, next := buildUsageWhere(&filter, start, end, 1)
	errorWhere, errorArgs, next := buildErrorWhere(&filter, start, end, next)
	args = append(args, errorArgs...)

	// Include configured group platforms even when the window is quiet.
	catalog := "SELECT DISTINCT platform FROM groups WHERE deleted_at IS NULL AND status='active' AND platform NOT IN ('','composite')"
	if filter.GroupID != nil {
		catalog += fmt.Sprintf(" AND id=$%d", next)
		args = append(args, *filter.GroupID)
		next++
	}
	if filter.Platform != "" {
		catalog += fmt.Sprintf(" AND platform=$%d", next)
		args = append(args, filter.Platform)
	}

	// Final errors take precedence over billable partial output. Recovered
	// attempts alone cannot prove completion. Never expand upstream_errors.
	q := `WITH usage_rows AS (
 SELECT ul.id, ul.api_key_id, ul.request_id, ul.platform, ul.stream, ul.openai_ws_mode
 FROM usage_logs ul ` + join + " " + usageWhere + `
), errors AS (
 SELECT DISTINCT ON (api_key_id, group_id, request_id, missing_id) e.*
 FROM (
   SELECT id, api_key_id, group_id, request_id, client_request_id, account_id, platform, created_at,
     CASE WHEN NULLIF(request_id,'') IS NULL THEN id END AS missing_id,
     (COALESCE(status_code,0)>=400 OR error_type='transport_error') AS final,
     (` + opsBusinessLimitedSQL("e") + ` OR error_type='client_canceled') AS excluded
   FROM ops_error_logs e ` + errorWhere + `
 ) e ORDER BY api_key_id, group_id, request_id, missing_id, final DESC, created_at DESC, id DESC
), matches AS (
 SELECT DISTINCT ON (e.id) e.id AS error_id, u.id AS usage_id, u.platform
 FROM errors e CROSS JOIN LATERAL (VALUES
   (NULLIF(e.request_id,''),1), ('local:'||NULLIF(e.request_id,''),2),
   ('client:'||NULLIF(e.client_request_id,''),3)
 ) k(request_id,priority)
 JOIN usage_rows u ON u.request_id=k.request_id
 WHERE u.api_key_id=e.api_key_id AND (k.priority<>3 OR NOT u.openai_ws_mode)
 ORDER BY e.id,k.priority,u.id
), observations AS (
 SELECT u.id AS business_id, u.platform, 1 AS priority, u.id AS source_id,
 CASE
   WHEN t.n>1 THEN 'unknown'
   WHEN t.detail->>'client_disconnect'='true' OR t.detail->>'outcome'='client_disconnected' THEN 'exclude'
   WHEN t.detail->>'terminal' IN ('failed','incomplete') OR t.detail->>'outcome'='failed' THEN 'failed'
   WHEN t.detail->>'downstream_error'='true' THEN 'failed'
   WHEN t.detail->>'canceled'='true' THEN 'exclude'
   WHEN u.stream THEN CASE WHEN t.detail->>'terminal'='completed' AND t.detail->>'outcome'='success' THEN 'success' ELSE 'unknown' END
   ELSE 'success'
 END AS outcome
 FROM usage_rows u LEFT JOIN LATERAL (
   SELECT COUNT(*) AS n, (array_agg(d.detail))[1] AS detail FROM
   (SELECT detail FROM request_timing_details WHERE usage_log_id=u.id LIMIT 2) d
 ) t ON true
 UNION ALL
 SELECT COALESCE(m.usage_id,-e.id), COALESCE(NULLIF(m.platform,''),NULLIF(a.platform,''),e.platform),
   CASE WHEN e.final THEN 2 ELSE 0 END,e.id,
   CASE WHEN NOT e.final THEN 'unknown' WHEN e.excluded THEN 'exclude' ELSE 'failed' END
 FROM errors e LEFT JOIN matches m ON m.error_id=e.id
 LEFT JOIN accounts a ON a.id=e.account_id
), requests AS (
 SELECT DISTINCT ON (business_id) platform,outcome FROM observations
 ORDER BY business_id,priority DESC,source_id DESC
), platform_rows AS (
 SELECT platform,outcome FROM requests WHERE outcome<>'exclude'
 UNION ALL SELECT platform,'no_sample' FROM (` + catalog + `) configured
)
SELECT COALESCE(NULLIF(platform,'composite'),''),COUNT(*) FILTER (WHERE outcome='success'),
 COUNT(*) FILTER (WHERE outcome='failed'),COUNT(*) FILTER (WHERE outcome='unknown')
 FROM platform_rows GROUP BY 1 ORDER BY 1`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []service.OpsAlertPlatformStats
	for rows.Next() {
		var row service.OpsAlertPlatformStats
		if err := rows.Scan(&row.Platform, &row.Succeeded, &row.Failed, &row.Unknown); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
