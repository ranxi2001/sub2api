//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsDeepSeekContentRejectionHistoryAndSLA(t *testing.T) {
	ctx := context.Background()
	ops := NewOpsRepository(integrationDB).(*opsRepository)
	start := time.Now().UTC().Truncate(time.Hour).Add(-6 * time.Hour)
	end := start.Add(time.Hour)
	model := "deepseek-content-rejection-regression"
	up400, up502 := 400, 502
	message := service.DeepSeekInputContentRejectionMessage
	rows := []*service.OpsInsertErrorLogInput{
		{RequestID: "deepseek-content-legacy", StatusCode: 400, UpstreamStatusCode: &up400, ErrorMessage: message},
		{RequestID: "deepseek-content-normalized", StatusCode: 403, UpstreamStatusCode: &up400, UpstreamErrorMessage: &message},
		{RequestID: "deepseek-content-generic", StatusCode: 400, UpstreamStatusCode: &up400, ErrorMessage: "Invalid parameter"},
		{RequestID: "deepseek-content-provider", StatusCode: 502, UpstreamStatusCode: &up502, ErrorMessage: message},
		{RequestID: "deepseek-content-auth", StatusCode: 403, UpstreamStatusCode: &up400, ErrorPhase: "account_auth", ErrorMessage: message},
		{RequestID: "deepseek-content-output", StatusCode: 400, UpstreamStatusCode: &up400, ErrorMessage: "Output text data may contain inappropriate content."},
		{RequestID: "deepseek-content-other-platform", Platform: "openai", StatusCode: 400, UpstreamStatusCode: &up400, ErrorMessage: message},
	}
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_error_logs WHERE model=$1", model)
		for _, table := range []string{"channel_monitor_v2_metrics_1m", "channel_monitor_v2_error_metrics_1m", "channel_monitor_v2_metrics_rollup", "channel_monitor_v2_error_metrics_rollup"} {
			_, _ = integrationDB.ExecContext(ctx, "DELETE FROM "+table+" WHERE model=$1", model)
		}
	})
	for i, row := range rows {
		if row.Platform == "" {
			row.Platform = "deepseek"
		}
		if row.ErrorPhase == "" {
			row.ErrorPhase = "upstream"
		}
		row.Model, row.Severity, row.ErrorType = model, "P3", "invalid_request_error"
		row.ErrorOwner, row.ErrorSource = "provider", "upstream_http"
		row.CreatedAt = start.Add(time.Duration(i+1) * time.Minute)
		// Repository insertion deliberately skips the service normalizer so
		// historical provider-attributed rows exercise read-time compatibility.
		id, err := ops.InsertErrorLog(ctx, row)
		require.NoError(t, err)
		detail, err := ops.GetErrorLogByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, i < 2, detail.IsBusinessLimited)
		var storedStatus, storedUpstreamStatus int
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT status_code, upstream_status_code FROM ops_error_logs WHERE id=$1", id).Scan(&storedStatus, &storedUpstreamStatus))
		require.Equal(t, row.StatusCode, storedStatus, "historical client HTTP evidence is not rewritten")
		require.Equal(t, *row.UpstreamStatusCode, storedUpstreamStatus, "historical upstream HTTP evidence is not rewritten")
		// Ops detail displays the upstream status when present.
		require.Equal(t, *row.UpstreamStatusCode, detail.StatusCode)
		require.Equal(t, row.UpstreamStatusCode, detail.UpstreamStatusCode)
		if i < 2 {
			require.Equal(t, "client", detail.Owner)
			require.Equal(t, "request", detail.Phase)
			require.Equal(t, "content_policy", detail.Type)
		}
	}
	filter := &service.OpsDashboardFilter{StartTime: start, EndTime: end, Platform: "deepseek"}
	total, excluded, sla, _, _, _, err := ops.queryErrorCounts(ctx, filter, start, end)
	require.NoError(t, err)
	require.EqualValues(t, 6, total)
	require.EqualValues(t, 2, excluded)
	require.EqualValues(t, 4, sla)
	list, err := ops.ListErrorLogs(ctx, &service.OpsErrorLogFilter{StartTime: &start, EndTime: &end, Model: model, View: "excluded"})
	require.NoError(t, err)
	require.EqualValues(t, 2, list.Total)
	monitor := NewChannelMonitorV2Repository(integrationDB).(*channelMonitorV2Repository)
	require.NoError(t, monitor.RecomputeRange(ctx, start, end))
	var monitorErrors int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COALESCE(sum(error_requests),0) FROM channel_monitor_v2_metrics_1m WHERE model=$1 AND platform='deepseek'", model).Scan(&monitorErrors))
	require.EqualValues(t, 4, monitorErrors)
}
