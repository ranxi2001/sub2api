//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsNormalizedContextLimitRetainsRawStatusInSQL(t *testing.T) {
	for _, tc := range []struct {
		status, upstream int
		message          string
		want             bool
	}{
		{400, 502, "context_length_exceeded: Too many tokens", true},
		{400, 200, "context_length_exceeded: Too many tokens", true},
		{502, 502, "context_length_exceeded", false},
		{400, 502, "temporary upstream outage", false},
		{400, 502, "request payload is too large", false},
	} {
		var got bool
		query := "SELECT " + opsClientRejectionSQL("e") + ` FROM (
 SELECT $1::int AS status_code,$2::int AS upstream_status_code,$3::text AS error_message,
 ''::text AS upstream_error_message,false AS is_business_limited,'provider'::text AS error_owner,
 'upstream'::text AS error_phase,'upstream_error'::text AS error_type,1::bigint AS account_id,
 'upstream_http'::text AS error_source,'[]'::jsonb AS upstream_errors) e`
		require.NoError(t, integrationDB.QueryRowContext(context.Background(), query, tc.status, tc.upstream, tc.message).Scan(&got))
		require.Equal(t, tc.want, got)
		require.Equal(t, tc.want, service.IsOpsContextLimitRejection(tc.status, tc.upstream, tc.message))
	}
}

func TestChannelMonitorExcludesClientRejections(t *testing.T) {
	ctx := context.Background()
	ops := NewOpsRepository(integrationDB).(*opsRepository)
	repo := NewChannelMonitorV2Repository(integrationDB).(*channelMonitorV2Repository)
	start := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	end := start.Add(time.Hour)
	model := "client-isolation-test"
	cfg := service.ChannelMonitorV2Config{Platforms: []service.ChannelMonitorV2PlatformConfig{{Platform: "openai", Enabled: true}}}
	rows := []*service.OpsInsertErrorLogInput{
		{RequestID: "client-isolation-model", ErrorPhase: "request", ErrorType: "model_not_found", ErrorOwner: "client", ErrorSource: "client_request", StatusCode: 404, IsBusinessLimited: true},
		{RequestID: "client-isolation-balance", ErrorPhase: "request", ErrorType: "billing_error", ErrorOwner: "client", ErrorSource: "client_request", StatusCode: 403, IsBusinessLimited: true},
		{RequestID: "client-isolation-legacy-model", ErrorPhase: "routing", ErrorType: "model_not_found", ErrorOwner: "platform", ErrorSource: "gateway", StatusCode: 404, IsBusinessLimited: true},
		{RequestID: "client-isolation-legacy-balance", ErrorPhase: "request", ErrorType: "api_error", ErrorOwner: "client", ErrorSource: "client_request", StatusCode: 403, IsBusinessLimited: true},
		{RequestID: "client-isolation-provider", ErrorPhase: "upstream", ErrorType: "upstream_error", ErrorOwner: "provider", ErrorSource: "upstream_http", StatusCode: 502, ErrorMessage: "Insufficient account balance"},
		{RequestID: "client-isolation-capacity", ErrorPhase: "routing", ErrorType: "rate_limit_error", ErrorOwner: "platform", ErrorSource: "gateway", StatusCode: 429, IsBusinessLimited: true, ErrorMessage: "All available accounts are currently rate-limited"},
		// A final local rejection must not resurrect an earlier provider failure when deduplicating.
		{RequestID: "client-isolation-dedup", ErrorPhase: "upstream", ErrorType: "upstream_error", ErrorOwner: "provider", StatusCode: 502},
		{RequestID: "client-isolation-dedup", ErrorPhase: "request", ErrorType: "billing_error", ErrorOwner: "client", ErrorSource: "client_request", StatusCode: 403, IsBusinessLimited: true},
	}
	for i, row := range rows {
		row.Platform = "openai"
		row.Model = model
		row.Severity = "P3"
		row.CreatedAt = start.Add(time.Duration(i+1) * time.Minute)
		_, err := ops.InsertErrorLog(ctx, row)
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_error_logs WHERE request_id LIKE 'client-isolation-%'")
	})
	require.NoError(t, repo.RecomputeRange(ctx, start, end))
	// V2 and V3 share these facts. User rejections must not inflate either denominator or errors.
	var count int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COALESCE(SUM(error_requests),0) FROM channel_monitor_v2_metrics_1m WHERE bucket_start >= $1 AND bucket_start < $2 AND model=$3", start, end, model).Scan(&count))
	require.EqualValues(t, 2, count)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COALESCE(SUM(error_requests),0) FROM channel_monitor_v2_metrics_rollup WHERE bucket_seconds=3600 AND bucket_start >= $1 AND bucket_start < $2 AND model=$3", start, end, model).Scan(&count))
	require.EqualValues(t, 2, count, "historical V2/V3 rollups use the same exclusion")
	details, err := repo.loadErrorDetails(ctx, service.ChannelMonitorV2Filter{Start: start, End: end, Models: []string{model}}, cfg)
	require.NoError(t, err)
	var detailCount int64
	for _, group := range details {
		for _, detail := range group {
			if detail.Model == model {
				detailCount += detail.Count
			}
		}
	}
	require.EqualValues(t, 2, detailCount)
	// Ops retains user errors in the excluded view and full request history.
	result, err := ops.ListErrorLogs(ctx, &service.OpsErrorLogFilter{StartTime: &start, EndTime: &end, View: "excluded", Owner: "client", Model: model})
	require.NoError(t, err)
	require.EqualValues(t, 5, result.Total, "legacy local model rejection is also attributed to the client")
	result, err = ops.ListErrorLogs(ctx, &service.OpsErrorLogFilter{StartTime: &start, EndTime: &end, View: "errors", Model: model})
	require.NoError(t, err)
	require.EqualValues(t, 3, result.Total, "raw history retains the earlier provider failure even after a final local rejection")
	var errorRequestIDs []string
	for _, item := range result.Errors {
		errorRequestIDs = append(errorRequestIDs, item.RequestID)
	}
	require.ElementsMatch(t, []string{"client-isolation-provider", "client-isolation-capacity", "client-isolation-dedup"}, errorRequestIDs)
}

func TestChannelMonitorClientRejectionRequiresLocalEvidence(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name           string
		owner, source  string
		upstreamStatus int
		events         string
		want           bool
	}{
		{"local client", "client", "client_request", 0, "[]", true},
		{"unknown owner", "", "", 0, "[]", false},
		{"provider", "provider", "upstream_http", 0, "[]", false},
		{"upstream status", "client", "client_request", 403, "[]", false},
		{"upstream source", "client", "upstream_http", 0, "[]", false},
		{"upstream events", "client", "client_request", 0, `[{}]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var excluded bool
			err := integrationDB.QueryRowContext(ctx, "SELECT "+channelMonitorClientRejectionSQL+` FROM (
    SELECT TRUE AS is_business_limited, NULLIF($1::text,'') AS error_owner,
     $2::text AS error_source, NULLIF($3::int,0) AS upstream_status_code,
     $4::jsonb AS upstream_errors, 'request' AS error_phase,
     'api_error' AS error_type, NULL::bigint AS account_id,
     403 AS status_code, NULL::text AS error_message, NULL::text AS upstream_error_message
   ) current_error`, tc.owner, tc.source, tc.upstreamStatus, tc.events).Scan(&excluded)
			require.NoError(t, err)
			require.Equal(t, tc.want, excluded)
		})
	}
}
