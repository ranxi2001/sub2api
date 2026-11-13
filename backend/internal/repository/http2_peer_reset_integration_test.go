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
	now := time.Now().UTC()
	id, err := repo.InsertErrorLog(ctx, &service.OpsInsertErrorLogInput{
		RequestID: "synthetic-http2-reset", ErrorPhase: "network", ErrorType: "transport_error",
		ErrorOwner: "provider", ErrorSource: "upstream_transport", Severity: "error", StatusCode: 200,
		ErrorMessage: "Upstream connection reset by peer (HTTP/2 INTERNAL_ERROR). Content may be incomplete. Do not automatically retry the same request.",
		ErrorBody:    `{"error":{"type":"upstream_error","code":"upstream_http2_stream_error","category":"transport_error","reset_reason":"INTERNAL_ERROR"}}`,
		Stream:       true, CreatedAt: now,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_error_logs WHERE id = $1", id) })
	detail, err := repo.GetErrorLogByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, 200, detail.StatusCode)
	require.Nil(t, detail.UpstreamStatusCode)
	require.Equal(t, "transport_error", detail.Type)
	start, end := now.Add(-time.Second), now.Add(time.Second)
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
	var counted bool
	err = integrationDB.QueryRowContext(ctx, "SELECT (COALESCE(status_code,0) >= 400 OR error_type = 'transport_error') FROM ops_error_logs WHERE id=$1", id).Scan(&counted)
	require.NoError(t, err)
	require.True(t, counted)
}
