//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsMonitorCapabilityRejectionsAndForwardedDuplicates(t *testing.T) {
	ctx := context.Background()
	ops := NewOpsRepository(integrationDB).(*opsRepository)
	monitor := NewChannelMonitorV2Repository(integrationDB).(*channelMonitorV2Repository)
	start := time.Now().UTC().Truncate(time.Hour).Add(-4 * time.Hour)
	end := start.Add(time.Hour)
	model := "monitor-rejection-regression"
	up404, up400, up401, up413, up502 := 404, 400, 401, 413, 502
	capability := `Model "example" is not available for this group`
	payload := "Request payload is too large"
	wrappedContext := `{"type":"response.failed","response":{"error":{"code":"context_length_exceeded"}}}`
	rows := []*service.OpsInsertErrorLogInput{
		{RequestID: "monitor-reg-missing-code", StatusCode: 502, UpstreamStatusCode: &up404, ErrorOwner: "provider", ErrorPhase: "upstream", ErrorMessage: "model_not_found"},
		{RequestID: "monitor-reg-admission", StatusCode: 404, ErrorOwner: "client", ErrorPhase: "request", ErrorType: "model_not_found", IsBusinessLimited: true, ErrorMessage: "The requested model is not supported by the selected account"},
		{RequestID: "monitor-reg-model", StatusCode: 502, UpstreamStatusCode: &up404, ErrorOwner: "provider", ErrorPhase: "upstream", UpstreamErrorMessage: &capability},
		{RequestID: "monitor-reg-codex", StatusCode: 400, UpstreamStatusCode: &up400, ErrorOwner: "provider", ErrorPhase: "upstream", ErrorType: "invalid_request_error", ErrorMessage: "The 'example' model is not supported when using Codex with a ChatGPT account."},
		{RequestID: "monitor-reg-local", StatusCode: 400, ErrorOwner: "client", ErrorPhase: "request", ErrorType: "invalid_request_error"},
		{RequestID: "monitor-reg-balance", StatusCode: 403, ErrorOwner: "client", ErrorPhase: "request", ErrorType: "billing_error", ErrorMessage: "Insufficient user balance"},
		{RequestID: "monitor-reg-claude-context", StatusCode: 400, UpstreamStatusCode: &up400, ErrorOwner: "provider", ErrorPhase: "upstream", ErrorMessage: "prompt is too long: compact the conversation and retry."},
		{RequestID: "monitor-reg-payload", StatusCode: 400, UpstreamStatusCode: &up400, ErrorOwner: "provider", ErrorPhase: "upstream", ErrorType: "invalid_request_error", ErrorMessage: payload},
		{RequestID: "monitor-reg-wrapped-payload", StatusCode: 502, UpstreamStatusCode: &up413, ErrorOwner: "provider", ErrorPhase: "upstream", ErrorSource: "upstream_http", UpstreamErrorMessage: &payload},
		{RequestID: "monitor-reg-context-502-message", StatusCode: 502, UpstreamStatusCode: &up502, ErrorOwner: "provider", ErrorPhase: "upstream", ErrorSource: "upstream_http", UpstreamErrorMessage: &wrappedContext},
		{RequestID: "monitor-reg-context-502-detail", StatusCode: 502, UpstreamStatusCode: &up502, ErrorOwner: "provider", ErrorPhase: "upstream", ErrorSource: "upstream_http", ErrorMessage: "Upstream request failed", UpstreamErrorDetail: &wrappedContext},
		{RequestID: "monitor-reg-generic-502", StatusCode: 502, UpstreamStatusCode: &up502, ErrorOwner: "provider", ErrorPhase: "upstream", ErrorMessage: "Bad Gateway"},
		{RequestID: "monitor-reg-vague-context-502", StatusCode: 502, UpstreamStatusCode: &up502, ErrorOwner: "provider", ErrorPhase: "upstream", ErrorMessage: "prompt is too long"},
		// Insert raw legacy flags to exercise read-time compatibility, not new-log normalization.
		{RequestID: "monitor-reg-capacity-503", StatusCode: 503, ErrorOwner: "platform", ErrorPhase: "routing", ErrorSource: "gateway", IsBusinessLimited: true, ErrorMessage: `Accounts supporting model "example" are currently rate-limited`},
		{RequestID: "monitor-reg-capacity-429", StatusCode: 429, ErrorOwner: "platform", ErrorPhase: "routing", ErrorType: "rate_limit_error", ErrorSource: "gateway", IsBusinessLimited: true, ErrorMessage: "All available accounts are currently rate-limited"},
		{RequestID: "monitor-reg-supplier", StatusCode: 502, ErrorOwner: "provider", ErrorPhase: "upstream", ErrorMessage: "Insufficient account balance"},
		{RequestID: "monitor-reg-auth", StatusCode: 503, UpstreamStatusCode: &up401, ErrorOwner: "provider", ErrorPhase: "upstream", ErrorMessage: "Your authentication token has expired"},
		{RequestID: "monitor-reg-validation", StatusCode: 400, UpstreamStatusCode: &up400, ErrorOwner: "provider", ErrorPhase: "upstream", ErrorType: "invalid_request_error", ErrorMessage: "Encrypted content could not be verified"},
		{RequestID: "monitor-reg-pod", StatusCode: 503, ErrorOwner: "platform", ErrorPhase: "internal", ErrorMessage: "Bound Pod unavailable; the existing conversation was not moved"},
		{RequestID: "monitor-reg-duplicate", StatusCode: 502, UpstreamStatusCode: &up502, ErrorOwner: "provider", ErrorPhase: "upstream"},
		{RequestID: "monitor-reg-duplicate", StatusCode: 502, ErrorOwner: "provider", ErrorPhase: "upstream"},
	}
	for i, row := range rows {
		row.Platform, row.Model, row.Severity = "openai", model, "P3"
		if row.ErrorType == "" {
			row.ErrorType = "api_error"
		}
		row.CreatedAt = start.Add(time.Duration(i+1) * time.Minute)
		_, err := ops.InsertErrorLog(ctx, row)
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_error_logs WHERE request_id LIKE 'monitor-reg-%'")
		for _, table := range []string{"channel_monitor_v2_metrics_1m", "channel_monitor_v2_error_metrics_1m", "channel_monitor_v2_metrics_rollup", "channel_monitor_v2_error_metrics_rollup"} {
			_, _ = integrationDB.ExecContext(ctx, "DELETE FROM "+table+" WHERE model=$1", model)
		}
	})
	filter := &service.OpsDashboardFilter{StartTime: start, EndTime: end, Platform: "openai"}
	total, excluded, sla, _, _, _, err := ops.queryErrorCounts(ctx, filter, start, end)
	require.NoError(t, err)
	require.EqualValues(t, 20, total, "the forwarded error has one request outcome")
	require.EqualValues(t, 11, excluded)
	require.EqualValues(t, 9, sla, "generic 502, vague length, provider balance/auth/validation, Pod and account-pool capacity failures remain")
	trend, err := ops.GetErrorTrend(ctx, filter, 60)
	require.NoError(t, err)
	var trendTotal, trendSLA int64
	for _, point := range trend.Points {
		trendTotal += point.ErrorCountTotal
		trendSLA += point.ErrorCountSLA
	}
	require.Equal(t, total, trendTotal)
	require.Equal(t, sla, trendSLA)
	dist, err := ops.GetErrorDistribution(ctx, filter)
	require.NoError(t, err)
	var distTotal, distSLA int64
	for _, item := range dist.Items {
		distTotal += item.Total
		distSLA += item.SLA
	}
	require.Equal(t, total, distTotal)
	require.Equal(t, sla, distSLA)
	require.NoError(t, ops.UpsertHourlyMetrics(ctx, start, end))
	var hourlyTotal, hourlySLA int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT error_count_total,error_count_sla FROM ops_metrics_hourly WHERE bucket_start=$1 AND platform='openai' AND group_id IS NULL", start).Scan(&hourlyTotal, &hourlySLA))
	require.Equal(t, total, hourlyTotal)
	require.Equal(t, sla, hourlySLA)
	require.NoError(t, monitor.RecomputeRange(ctx, start, end))
	var errors int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT sum(error_requests) FROM channel_monitor_v2_metrics_1m WHERE model=$1", model).Scan(&errors))
	require.EqualValues(t, 9, errors)
	result, err := ops.ListErrorLogs(ctx, &service.OpsErrorLogFilter{StartTime: &start, EndTime: &end, Model: model, View: "excluded"})
	require.NoError(t, err)
	require.EqualValues(t, 11, result.Total, "legacy rows are visible under excluded without rewriting raw logs")
	for _, item := range result.Errors {
		require.Equal(t, "client", item.Owner)
		require.Equal(t, "request", item.Phase)
		detail, err := ops.GetErrorLogByID(ctx, item.ID)
		require.NoError(t, err)
		require.True(t, detail.IsBusinessLimited)
		require.Equal(t, item.Owner, detail.Owner)
		if item.RequestID == "monitor-reg-context-502-message" || item.RequestID == "monitor-reg-context-502-detail" {
			require.Equal(t, "context_limit", detail.Type)
			require.Equal(t, up502, detail.StatusCode)
		}
	}
	result, err = ops.ListErrorLogs(ctx, &service.OpsErrorLogFilter{StartTime: &start, EndTime: &end, Model: model, View: "errors"})
	require.NoError(t, err)
	require.EqualValues(t, 10, result.Total, "raw history retains both forwarded error records; SLA counts one outcome")
	var capacityCount int
	var errorRequestIDs []string
	for _, item := range result.Errors {
		errorRequestIDs = append(errorRequestIDs, item.RequestID)
		if item.RequestID == "monitor-reg-capacity-503" || item.RequestID == "monitor-reg-capacity-429" {
			capacityCount++
			detail, err := ops.GetErrorLogByID(ctx, item.ID)
			require.NoError(t, err)
			require.False(t, detail.IsBusinessLimited)
			require.Equal(t, "platform", detail.Owner)
			require.Equal(t, "routing", detail.Phase)
		}
	}
	require.ElementsMatch(t, []string{
		"monitor-reg-capacity-503", "monitor-reg-capacity-429", "monitor-reg-supplier",
		"monitor-reg-auth", "monitor-reg-validation", "monitor-reg-pod",
		"monitor-reg-duplicate", "monitor-reg-duplicate",
		"monitor-reg-generic-502", "monitor-reg-vague-context-502",
	}, errorRequestIDs)
	require.Equal(t, 2, capacityCount)
	var raw int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM ops_error_logs WHERE model=$1", model).Scan(&raw))
	require.Equal(t, 21, raw, "diagnostic records stay intact")
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM ops_error_logs WHERE request_id IN ('monitor-reg-capacity-503', 'monitor-reg-capacity-429') AND is_business_limited").Scan(&raw))
	require.Equal(t, 2, raw, "historical capacity flags are corrected at read time without rewriting evidence")
}

func TestOpsMetricMissingIDsAndRecoveredAttempts(t *testing.T) {
	ctx := context.Background()
	repo := NewOpsRepository(integrationDB).(*opsRepository)
	start := time.Date(2004, 4, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_error_logs WHERE created_at >= $1 AND created_at < $2", start, end)
	})
	for i, id := range []string{"", "", "monitor-scoped", "monitor-scoped"} {
		status := 502
		if i == 3 {
			status = 200
		}
		_, err := repo.InsertErrorLog(ctx, &service.OpsInsertErrorLogInput{RequestID: id, Platform: "openai", StatusCode: status,
			ErrorOwner: "provider", ErrorPhase: "upstream", ErrorType: "upstream_error", Severity: "P3", CreatedAt: start.Add(time.Duration(i) * time.Minute)})
		require.NoError(t, err)
	}
	total, _, sla, provider, _, _, err := repo.queryErrorCounts(ctx, nil, start, end)
	require.NoError(t, err)
	require.EqualValues(t, 3, total, "missing IDs are separate failures")
	require.Equal(t, total, sla)
	require.EqualValues(t, 4, provider, "recovered provider attempts remain visible separately")
}
