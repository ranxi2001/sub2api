//go:build unit && issue854pg

package handler

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Explicit build tag and exact disposable database name prevent this fixture
// from silently skipping or using an application/production DSN.
func TestIssue854PostgresRoundtrip(t *testing.T) {
	dsn := os.Getenv("ISSUE854_TEST_POSTGRES_DSN")
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"127.0.0.1", "localhost"}, u.Hostname())
	require.Equal(t, "/issue854_fixture", u.Path)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	require.NoError(t, db.PingContext(ctx))
	require.NoError(t, repository.ApplyMigrations(ctx, db))
	repo := repository.NewOpsRepository(db)
	for _, tc := range []struct {
		name, scenario, protocol, phase string
		turn, wire                      int
		knownUnsent                     bool
	}{
		{"ws_guard", "issue854_guard_refusal", "websocket", "before_upstream_write", 2, 101, true},
		{"ws_fresh", "issue854_fresh_refusal", "websocket", "before_dispatch", 1, 101, true},
		{"sse", "", "sse", "forwarding", 0, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestID := "fixture-issue854-pg-" + tc.name
			_, err := db.ExecContext(ctx, "DELETE FROM ops_error_logs WHERE request_id=$1", requestID)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = db.ExecContext(context.Background(), "DELETE FROM ops_error_logs WHERE request_id=$1", requestID)
			})
			if tc.scenario != "" {
				runReadableWSContinuation(t, service.OpenAIWSIngressModePassthrough, tc.scenario, repo)
			} else {
				setupOpsErrorLogTestQueue(t, 4)
				ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				router := gin.New()
				done := make(chan struct{}, 1)
				router.Use(func(c *gin.Context) { c.Next(); done <- struct{}{} })
				router.Use(middleware.RequestLogger(), OpsErrorLoggerMiddleware(ops))
				router.POST("/v1/responses", func(c *gin.Context) {
					service.SetOpenAIClientTransport(c, service.OpenAIClientTransportHTTP)
					setOpsRequestContext(c, "fixture-model", true)
					c.Header("Content-Type", "text/event-stream")
					c.Status(http.StatusOK)
					c.Writer.Flush()
					h := &OpenAIGatewayHandler{}
					require.True(t, h.handleOpenAIAgentRecoveryError(c, &service.OpenAIAgentRecoveryError{Reason: "owner_unavailable"}, true))
				})
				server := httptest.NewServer(router)
				defer server.Close()
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/responses", nil)
				require.NoError(t, err)
				req.Header.Set("X-Request-ID", requestID)
				response, err := server.Client().Do(req)
				require.NoError(t, err)
				body, err := io.ReadAll(response.Body)
				_ = response.Body.Close()
				require.NoError(t, err)
				require.Equal(t, 200, response.StatusCode)
				require.Contains(t, string(body), "encrypted_agent_recovery_owner_unavailable")
				<-done
				for len(opsErrorLogQueue) > 0 {
					flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
				}
			}
			// Real repository query, not the capture stub. Count and read back
			// committed rows after OpsService -> INSERT through the real pool.
			var count int
			require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ops_error_logs WHERE request_id=$1", requestID).Scan(&count))
			require.Equal(t, 1, count, "one terminal failure must be committed")
			listed, err := repo.ListErrorLogs(ctx, &service.OpsErrorLogFilter{RequestID: requestID, View: "all", Page: 1, PageSize: 10})
			require.NoError(t, err)
			require.Len(t, listed.Errors, 1, "wire101/200 failure must be searchable as an error")
			row, err := repo.GetErrorLogByID(ctx, listed.Errors[0].ID)
			require.NoError(t, err)
			require.Equal(t, requestID, row.RequestID)
			require.Equal(t, 409, row.StatusCode)
			require.Nil(t, row.UpstreamStatusCode)
			require.Equal(t, "internal", row.Phase)
			require.Equal(t, "platform", row.Owner)
			fields := gjson.Get(row.ErrorBody, "error")
			require.Equal(t, "encrypted_agent_recovery_owner_unavailable", fields.Get("code").String())
			require.Equal(t, tc.protocol, fields.Get("protocol").String())
			require.EqualValues(t, tc.turn, fields.Get("turn").Int())
			require.Equal(t, tc.phase, fields.Get("phase").String())
			require.EqualValues(t, tc.wire, fields.Get("wire_status").Int())
			require.EqualValues(t, 409, fields.Get("status").Int())
			require.Equal(t, "failed", fields.Get("outcome").String())
			if tc.knownUnsent {
				require.Equal(t, gjson.False, fields.Get("request_written").Type)
			} else {
				require.False(t, fields.Get("request_written").Exists(), "unknown send state must not be fabricated")
			}
			require.NotContains(t, row.ErrorBody, "fixture-opaque")
			t.Logf("PostgreSQL INSERT/readback: %s wire=%d logical=409 turn=%d rows=%d", tc.name, tc.wire, tc.turn, count)
			require.Positive(t, row.ID)
		})
	}
}
