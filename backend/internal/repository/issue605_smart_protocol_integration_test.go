//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSmartProtocolTimingAndOwnershipReadback(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newUsageLogRepositoryWithSQL(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{Email: "synthetic-" + uuid.NewString() + "@example.invalid"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "fixture-" + uuid.NewString(), Name: "synthetic"})
	account := mustCreateAccount(t, client, &service.Account{Name: "synthetic-" + uuid.NewString()})
	// testEntClient writes committed rows. Remove only this fixture's data so
	// later dashboard integration suites observe their own request/user totals.
	t.Cleanup(func() {
		cleanup := context.Background()
		for _, statement := range []struct {
			sql string
			id  int64
		}{
			{"DELETE FROM request_timing_details WHERE usage_log_id IN (SELECT id FROM usage_logs WHERE user_id=$1)", user.ID},
			{"DELETE FROM usage_logs WHERE user_id=$1", user.ID},
			{"DELETE FROM api_keys WHERE user_id=$1", user.ID},
			{"DELETE FROM accounts WHERE id=$1", account.ID},
			{"DELETE FROM users WHERE id=$1", user.ID},
		} {
			_, err := integrationDB.ExecContext(cleanup, statement.sql, statement.id)
			require.NoError(t, err, "clean synthetic timing fixture")
		}
	})
	usage := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: uuid.NewString(), Model: "synthetic", InputTokens: 3, OutputTokens: 7, CreatedAt: time.Now().UTC()}
	_, err := repo.Create(ctx, usage)
	require.NoError(t, err)
	collector := requesttiming.New(time.Now(), 0)
	timed := requesttiming.With(ctx, collector)
	requesttiming.SetProtocolDecision(timed, requesttiming.ProtocolDecision{Ingress: "ws", Upstream: "http", Candidate: "http", Mode: "auto", Reason: "large_output_cap", PredictionSource: "heuristic_cap"})
	requesttiming.Mark(timed, "ws_first_upstream_event")
	requesttiming.Mark(timed, "ws_first_actual_content")
	event, content := 3, 30
	requesttiming.RecordWSTurn(timed, requesttiming.WSTurnTiming{Turn: 1, Upstream: "http", FirstUpstreamEventMS: &event, FirstActualContentMS: &content, FirstActualContentKind: "visible_text"})
	var snapshot requesttiming.Snapshot
	collector.WhenFinished(func(s requesttiming.Snapshot) { snapshot = s })
	collector.Finish(200, false)
	require.NoError(t, repo.writeTiming(ctx, timingWrite{requestID: usage.RequestID, apiKeyID: key.ID, data: snapshot}))
	rows, err := repo.RequestTimings(ctx, usage.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	var readback requesttiming.Snapshot
	require.NoError(t, json.Unmarshal(rows[0], &readback))
	require.NotNil(t, readback.ProtocolDecision)
	require.Equal(t, "http", readback.ProtocolDecision.Upstream)
	require.Equal(t, "heuristic_cap", readback.ProtocolDecision.PredictionSource)
	require.Contains(t, readback.Events, "ws_first_upstream_event")
	require.Contains(t, readback.Events, "ws_first_actual_content")
	require.Len(t, readback.WSTurns, 1)
	require.Equal(t, 3, *readback.WSTurns[0].FirstUpstreamEventMS)
	require.Equal(t, 30, *readback.WSTurns[0].FirstActualContentMS)
	require.Equal(t, "visible_text", readback.WSTurns[0].FirstActualContentKind)
	// Existing timing JSON without the new optional field retains unknown state.
	snapshot = requesttiming.Snapshot{}
	require.NoError(t, json.Unmarshal([]byte(`{"version":1,"status":200}`), &snapshot))
	require.Nil(t, snapshot.ProtocolDecision)
	rdb := testRedis(t)
	writer := service.NewOpenAIWSStateStore(NewGatewayCache(rdb))
	reader := service.NewOpenAIWSStateStore(NewGatewayCache(rdb))
	response := "resp_" + uuid.NewString()
	require.NoError(t, writer.BindHTTPResponseOwner(ctx, 17, response, user.ID, key.ID, time.Minute))
	gotUser, gotKey, found, err := reader.GetHTTPResponseOwner(ctx, 17, response)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, user.ID, gotUser)
	require.Equal(t, key.ID, gotKey)
	require.NoError(t, rdb.Del(ctx, httpResponseOwnerKeys(17, response)...).Err())
	_, _, found, err = reader.GetHTTPResponseOwner(ctx, 17, response)
	require.NoError(t, err)
	require.False(t, found, "deleted ownership must not survive a warm cache")
}
