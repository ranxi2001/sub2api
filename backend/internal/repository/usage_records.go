package repository

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.UsageRecordsReader = (*usageLogRepository)(nil)

func (r *usageLogRepository) ListUsageRecords(ctx context.Context, userID, keyID int64, start, end time.Time, after *service.UsageRecordPosition, limit int) ([]service.UsageLog, error) {
	// PostgreSQL timestamps have microsecond precision. Round both exclusive-end
	// and inclusive-start bounds upwards to preserve fractional RFC3339 inputs.
	ceilMicro := func(t time.Time) time.Time { return t.Add(time.Microsecond - 1).Truncate(time.Microsecond) }
	// Keep ownership as a residual boolean check. On accounts with many keys,
	// intersecting the much larger user index makes every page scan unrelated keys.
	query := `SELECT id, created_at, COALESCE(request_id, ''), model, requested_model,
		upstream_response_model, reasoning_effort, requested_reasoning_effort, inbound_endpoint, ip_address,
		request_type, stream, openai_ws_mode, billing_mode, input_tokens, output_tokens, cache_read_tokens,
		cache_creation_tokens, COALESCE(image_count, 0), rate_multiplier, actual_cost, total_cost, first_token_ms, duration_ms
		FROM usage_logs WHERE api_key_id = $1 AND (user_id = $2) IS TRUE AND created_at >= $3 AND created_at < $4`
	args := []any{keyID, userID, ceilMicro(start), ceilMicro(end)}
	if after != nil {
		query += " AND (created_at, id) > ($5, $6)"
		args = append(args, after.CreatedAt, after.ID)
	}
	args = append(args, limit+1)
	rows, err := r.sql.QueryContext(ctx, query+" ORDER BY created_at ASC, id ASC LIMIT $"+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var records []service.UsageLog
	for rows.Next() {
		var row service.UsageLog
		var requestedModel sql.NullString
		if err := rows.Scan(&row.ID, &row.CreatedAt, &row.RequestID, &row.Model, &requestedModel,
			&row.UpstreamResponseModel, &row.ReasoningEffort, &row.RequestedReasoningEffort, &row.InboundEndpoint, &row.IPAddress,
			&row.RequestType, &row.Stream, &row.OpenAIWSMode, &row.BillingMode, &row.InputTokens, &row.OutputTokens,
			&row.CacheReadTokens, &row.CacheCreationTokens, &row.ImageCount, &row.RateMultiplier, &row.ActualCost, &row.TotalCost,
			&row.FirstTokenMs, &row.DurationMs); err != nil {
			return nil, err
		}
		row.RequestedModel = coalesceTrimmedString(requestedModel, row.Model)
		records = append(records, row)
	}
	return records, rows.Err()
}
