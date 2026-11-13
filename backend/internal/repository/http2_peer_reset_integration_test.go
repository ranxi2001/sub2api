//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestHTTP2PeerResetPersistence(t *testing.T) {
	ctx := context.Background()
	repo := NewOpsRepository(integrationDB).(*opsRepository)
	start := time.Now().UTC().Truncate(time.Hour).Add(-11 * time.Hour)
	end := start.Add(time.Hour)
	now := start.Add(time.Minute)
	model := "synthetic-http2-reset"
	userID := int64(900833)
	id, err := repo.InsertErrorLog(ctx, &service.OpsInsertErrorLogInput{
		RequestID: "synthetic-http2-reset", ErrorPhase: "network", ErrorType: "transport_error",
		ErrorOwner: "provider", ErrorSource: "upstream_transport", Severity: "error", StatusCode: 200,
		ErrorMessage: "Upstream connection reset by peer (HTTP/2 INTERNAL_ERROR). Content may be incomplete. Do not automatically retry the same request.",
		ErrorBody:    `{"error":{"type":"upstream_error","code":"upstream_http2_stream_error","category":"transport_error","reset_reason":"INTERNAL_ERROR"}}`,
		UserID:       &userID, Stream: true, CreatedAt: now, Platform: "openai", Model: model,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_error_logs WHERE id = $1", id) })
	detail, err := repo.GetErrorLogByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, 200, detail.StatusCode)
	require.Nil(t, detail.UpstreamStatusCode)
	require.Equal(t, "transport_error", detail.Type)

	list, err := repo.ListErrorLogs(ctx, &service.OpsErrorLogFilter{StartTime: &start, EndTime: &end, ErrorTypesAny: []string{"transport_error"}, Page: 1, PageSize: 20})
	require.NoError(t, err)
	found := false
	for _, row := range list.Errors {
		if row.ID == id {
			found = true
		}
	}
	require.True(t, found, "HTTP 200 transport failures must remain visible in request-error lists")
	user := service.ToUserErrorRequestDetail(detail)
	require.Equal(t, "transport_error", user.Category)
	ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	phases, types := service.CategoryToFilter("transport_error")
	owned, err := ops.ListUserErrorRequests(ctx, userID, &service.OpsErrorLogFilter{StartTime: &start, EndTime: &end, ErrorPhasesAny: phases, ErrorTypesAny: types, StatusCodes: []int{200}, Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, owned.Items, 1, "HTTP 200 transport errors must survive user/category/status filtering")
	require.Equal(t, id, owned.Items[0].ID)
	ownedDetail, err := ops.GetUserErrorRequestDetail(ctx, userID, id)
	require.NoError(t, err)
	require.Equal(t, "transport_error", ownedDetail.Category)
	require.Nil(t, ownedDetail.UpstreamStatusCode)

	filter := &service.OpsDashboardFilter{StartTime: start, EndTime: end, Platform: "openai"}
	total, excluded, sla, _, _, _, err := repo.queryErrorCounts(ctx, filter, start, end)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Zero(t, excluded)
	require.EqualValues(t, 1, sla, "failed HTTP-200 stream belongs in SLA errors")
	trend, err := repo.GetErrorTrend(ctx, filter, 60)
	require.NoError(t, err)
	var trendSLA int64
	for _, point := range trend.Points {
		trendSLA += point.ErrorCountSLA
	}
	require.Equal(t, sla, trendSLA)
	require.NoError(t, repo.UpsertHourlyMetrics(ctx, start, end))
	var hourlySLA int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT error_count_sla FROM ops_metrics_hourly WHERE bucket_start=$1 AND platform='openai' AND group_id IS NULL", start).Scan(&hourlySLA))
	require.Equal(t, sla, hourlySLA)
	monitor := NewChannelMonitorV2Repository(integrationDB).(*channelMonitorV2Repository)
	require.NoError(t, monitor.RecomputeRange(ctx, start, end))
	var category string
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT error_category FROM channel_monitor_v2_error_metrics_1m WHERE model=$1", model).Scan(&category))
	require.Equal(t, "transport_or_stream", category, "monitor must not classify peer reset as upstream 5xx")
	t.Cleanup(func() {
		for _, table := range []string{"channel_monitor_v2_metrics_1m", "channel_monitor_v2_error_metrics_1m", "channel_monitor_v2_metrics_rollup", "channel_monitor_v2_error_metrics_rollup"} {
			_, _ = integrationDB.ExecContext(ctx, "DELETE FROM "+table+" WHERE model=$1", model)
		}
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_metrics_hourly WHERE bucket_start=$1", start)
	})
}
