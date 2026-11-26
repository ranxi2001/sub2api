//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAlertPlatformStatsFinalOutcomes(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{})
	group := mustCreateGroup(t, client, &service.Group{Name: "platform-alert", Platform: "openai"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID})
	account := mustCreateAccount(t, client, &service.Account{Name: "health", Platform: "deepseek", Type: "apikey"})
	start := time.Date(2021, 5, 18, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM usage_logs WHERE user_id=$1", user.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_error_logs WHERE api_key_id=$1", key.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM api_keys WHERE id=$1", key.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM groups WHERE id=$1", group.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM accounts WHERE id=$1", account.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE id=$1", user.ID)
	})
	usage := func(request, platform string, stream bool, detail string) {
		var id int64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,request_id,model,created_at,platform,stream) VALUES ($1,$2,$3,$4,$5,'alias',$6,$7,$8) RETURNING id`, user.ID, key.ID, account.ID, group.ID, request, start, platform, stream).Scan(&id))
	}
	logError := func(req, clientID string, code int, at time.Time) {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_error_logs(api_key_id,account_id,group_id,request_id,client_request_id,platform,status_code,created_at,error_type,error_phase) VALUES ($1,$2,$3,$4,$5,'openai',$6,$7,'upstream_error','upstream')`, key.ID, account.ID, group.ID, req, clientID, code, at)
		require.NoError(t, err)
	}
	usage("client:recovered", "anthropic", false, "")
	logError("r", "recovered", 200, start)
	logError("r", "recovered", 200, start)
	usage("local:partial", "deepseek", true, "")
	logError("partial", "", 502, start)
	logError("partial", "", 502, start)
	usage("terminal", "deepseek", true, `{"outcome":"failed","terminal":"failed","status":200}`)
	usage("complete", "anthropic", true, `{"outcome":"success","terminal":"completed","status":200}`)
	usage("unknown", "", true, "")
	logError("outside", "", 502, end)
	stats, err := NewOpsRepository(integrationDB).GetAlertPlatformStats(ctx, service.OpsDashboardFilter{StartTime: start, EndTime: end, GroupID: &group.ID})
	require.NoError(t, err)
	require.ElementsMatch(t, []service.OpsAlertPlatformStats{
		{Platform: "openai"}, {Platform: "deepseek", Failed: 1, Unknown: 1}, {Platform: "anthropic", Succeeded: 1, Unknown: 1}, {Unknown: 1},
	}, stats)
}
