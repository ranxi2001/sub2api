//go:build unit && issue985pg

package handler

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// Strict CI-only DB fixture: missing infrastructure is failure, never a skip.
// The host, role and database are isolated from every production connection.
func issue985Postgres(t *testing.T) *sql.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dsn := os.Getenv("ISSUE985_TEST_POSTGRES_DSN")
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"127.0.0.1", "localhost"}, u.Hostname())
	require.Equal(t, "fixture", u.User.Username())
	require.Equal(t, "/issue985_fixture", u.Path)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx := context.Background()
	require.NoError(t, db.PingContext(ctx))
	require.NoError(t, repository.ApplyMigrations(ctx, db))
	return db
}

func TestIssue985PostgresBilling(t *testing.T) {
	db := issue985Postgres(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `INSERT INTO groups(id,name,platform) VALUES(9300,'issue985','anthropic');
 INSERT INTO users(id,email,password_hash,balance) VALUES(9303,'issue985@example.invalid','fixture',100);
 INSERT INTO accounts(id,name,platform,type) VALUES(9301,'issue985','anthropic','apikey');
 INSERT INTO api_keys(id,user_id,key,name,group_id) VALUES(9302,9303,'issue985-fixture','issue985',9300);
 INSERT INTO account_groups(account_id,group_id) VALUES(9301,9300)`)
	require.NoError(t, err)
	real := repository.NewUsageBillingRepository(nil, db)
	durable, ok := real.(service.DurableUsageBillingRepository)
	require.True(t, ok)
	canonical := compatStreamUsagePartialStream + compatStreamUsageCompletion
	missing := strings.ReplaceAll(strings.ReplaceAll(canonical, `,"usage":{"input_tokens":12,"output_tokens":1}`, ""), `,"usage":{"output_tokens":7}`, "")
	for _, tc := range []struct {
		name, wire    string
		fail, billed  bool
		input, output int
	}{
		{"valid", strings.ReplaceAll(canonical, "\ndata:", "\nid: fixture\ndata:"), false, true, 12, 7},
		{"missing", missing, true, false, 0, 0},
		{"explicit_zero", strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(canonical, `"input_tokens":12`, `"input_tokens":0`), `"output_tokens":1`, `"output_tokens":0`), `"output_tokens":7`, `"output_tokens":0`), false, true, 0, 0},
		{"partial_protocol", compatStreamUsagePartialStream, true, true, 12, 1},
		{"partial_error", compatStreamUsagePartialStream + "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"fixture\"}}\n\n", true, true, 12, 1},
	} {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				requestID := fmt.Sprintf("issue985-%s-%t", tc.name, stream)
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("X-Request-Id", requestID)
					_, _ = io.WriteString(w, tc.wire)
				}))
				defer upstream.Close()
				billing := &issue985Billing{UsageBillingRepository: real, durable: durable}
				gateway, done := issue985Gateway(t, upstream.URL, billing)
				_, wire := issue985Request(t, gateway, done, fmt.Sprintf(`{"model":"claude-sonnet-4.5","stream":%t,"messages":[{"role":"user","content":"fixture"}]}`, stream))
				require.Equal(t, int32(1), calls.Load())
				if tc.fail {
					require.Contains(t, wire, `"error"`)
					require.NotContains(t, wire, "[DONE]")
					require.NotContains(t, wire, `"finish_reason":"stop"`)
				} else {
					require.Contains(t, wire, "partial")
				}
				var events, logs, quotes int
				require.NoError(t, db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM usage_billing_events WHERE request_id=$1),(SELECT count(*) FROM usage_logs WHERE request_id=$1),(SELECT count(*) FROM usage_billing_quotes WHERE request_id=$1)`, requestID).Scan(&events, &logs, &quotes))
				if !tc.billed {
					require.Zero(t, events)
					require.Zero(t, logs)
					require.Zero(t, quotes)
					return
				}
				require.Equal(t, 1, events)
				require.Equal(t, 1, logs)
				require.Equal(t, 1, quotes)
				var state string
				var input, output int
				var actual, quoted, canonicalCost float64
				var debit sql.NullFloat64
				require.NoError(t, db.QueryRowContext(ctx, `SELECT e.state,u.input_tokens,u.output_tokens,u.actual_cost,q.quoted_actual_cost,q.canonical_actual_cost,q.wallet_debit FROM usage_billing_events e JOIN usage_logs u USING(request_id,api_key_id) JOIN usage_billing_quotes q USING(request_id,api_key_id) WHERE e.request_id=$1`, requestID).Scan(&state, &input, &output, &actual, &quoted, &canonicalCost, &debit))
				require.Equal(t, "complete", state)
				require.Equal(t, tc.input, input)
				require.Equal(t, tc.output, output)
				if tc.input > 0 {
					require.Greater(t, actual, 0.0)
					require.Greater(t, quoted, 0.0)
					require.Greater(t, canonicalCost, 0.0)
					require.True(t, debit.Valid)
					require.InDelta(t, actual, debit.Float64, 1e-8)
				} else {
					require.Zero(t, actual)
					require.Zero(t, quoted)
					require.Zero(t, canonicalCost)
					require.False(t, debit.Valid)
				}
				recovered, err := durable.ApplyStagedUsageBilling(ctx, service.UsageBillingEventKey{RequestID: requestID, APIKeyID: 9302})
				require.NoError(t, err)
				require.False(t, recovered.Result.Applied, "readback/recovery cannot charge twice")
				require.Equal(t, int32(1), calls.Load())
			})
		}
	}
}

func TestIssue985NativePostgresBilling(t *testing.T) {
	db := issue985Postgres(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `INSERT INTO groups(id,name,platform) VALUES(9400,'issue985-native','deepseek');
 INSERT INTO users(id,email,password_hash,balance) VALUES(9403,'issue985-native@example.invalid','fixture',100);
 INSERT INTO accounts(id,name,platform,type) VALUES(9401,'issue985-native','deepseek','apikey');
 INSERT INTO api_keys(id,user_id,key,name,group_id) VALUES(9402,9403,'issue985-native-fixture','issue985-native',9400);
 INSERT INTO account_groups(account_id,group_id) VALUES(9401,9400)`)
	require.NoError(t, err)
	real := repository.NewUsageBillingRepository(nil, db)
	durable, ok := real.(service.DurableUsageBillingRepository)
	require.True(t, ok)
	for _, route := range []string{"chat", "responses", "messages"} {
		for _, tc := range issue985NativeCases() {
			for _, stream := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", route, tc.name, stream), func(t *testing.T) {
					requestID := fmt.Sprintf("issue985-native-%s-%s-%t", route, tc.name, stream)
					billing := &issue985Billing{UsageBillingRepository: real, durable: durable}
					issue985NativeRequest(t, tc, route, stream, requestID, billing)
					var events, logs, quotes int
					require.NoError(t, db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM usage_billing_events WHERE request_id=$1),(SELECT count(*) FROM usage_logs WHERE request_id=$1),(SELECT count(*) FROM usage_billing_quotes WHERE request_id=$1)`, requestID).Scan(&events, &logs, &quotes))
					if tc.name == "unknown" {
						require.Empty(t, billing.snapshot())
						require.Zero(t, events)
						require.Zero(t, logs)
						require.Zero(t, quotes, "unknown cannot persist an ordinary zero quote")
						return
					}
					require.Len(t, billing.snapshot(), 1)
					require.Equal(t, 1, events)
					require.Equal(t, 1, logs)
					require.Equal(t, 1, quotes)
					var state string
					var input, output, cached, created, ttl int
					var actual, quoted, canonicalCost float64
					var debit sql.NullFloat64
					require.NoError(t, db.QueryRowContext(ctx, `SELECT e.state,u.input_tokens,u.output_tokens,u.cache_read_tokens,u.cache_creation_tokens,u.cache_creation_1h_tokens,u.actual_cost,q.quoted_actual_cost,q.canonical_actual_cost,q.wallet_debit FROM usage_billing_events e JOIN usage_logs u USING(request_id,api_key_id) JOIN usage_billing_quotes q USING(request_id,api_key_id) WHERE e.request_id=$1`, requestID).Scan(&state, &input, &output, &cached, &created, &ttl, &actual, &quoted, &canonicalCost, &debit))
					require.Equal(t, "complete", state)
					require.Equal(t, tc.input, input)
					require.Equal(t, tc.output, output)
					require.Equal(t, tc.cached, cached)
					require.Equal(t, tc.created, created)
					require.Equal(t, tc.created, ttl)
					if tc.input+tc.output+tc.cached+tc.created > 0 {
						require.Positive(t, actual)
						require.Positive(t, quoted)
						require.Positive(t, canonicalCost)
						require.True(t, debit.Valid)
						require.InDelta(t, actual, debit.Float64, 1e-8)
					} else {
						require.Zero(t, actual)
						require.Zero(t, quoted)
						require.Zero(t, canonicalCost)
						require.False(t, debit.Valid)
					}
					recovered, err := durable.ApplyStagedUsageBilling(ctx, service.UsageBillingEventKey{RequestID: requestID, APIKeyID: 9402})
					require.NoError(t, err)
					require.False(t, recovered.Result.Applied, "recovery cannot charge known partial usage twice")
				})
			}
		}
	}
}
