//go:build integration

package repository

import (
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIOAuthFallbackAttribution(t *testing.T) {
	ctx := t.Context()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID})
	group := mustCreateGroup(t, client, &service.Group{Name: "fallback-mixed", Platform: service.PlatformOpenAI})
	oauth := mustCreateAccount(t, client, &service.Account{Name: "fallback-oauth", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth})
	api := mustCreateAccount(t, client, &service.Account{Name: "fallback-compatible", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	other := mustCreateAccount(t, client, &service.Account{Name: "fallback-other-oauth", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth})
	start := time.Date(2021, 4, 15, 0, 0, 0, 0, time.UTC)
	end := start.Add(3 * time.Hour)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_error_logs WHERE api_key_id = $1", key.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM usage_logs WHERE user_id = $1", user.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM api_keys WHERE id = $1", key.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM accounts WHERE id IN ($1,$2,$3)", oauth.ID, api.ID, other.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM groups WHERE id = $1", group.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE id = $1", user.ID)
	})
	for _, accountID := range []int64{oauth.ID, oauth.ID, oauth.ID, api.ID, other.ID} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs
 (user_id,api_key_id,account_id,group_id,request_id,model,created_at)
 VALUES ($1,$2,$3,$4,$5,'model-alias',$6)`, user.ID, key.ID, accountID, group.ID, fmt.Sprintf("fallback-%d-%d", key.ID, time.Now().UnixNano()), start)
		require.NoError(t, err)
	}
	insertError := func(request string, accountID *int64, model, attempts, kind string, status int, at time.Time) {
		t.Helper()
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_error_logs
 (api_key_id,group_id,request_id,account_id,model,upstream_errors,error_phase,error_type,error_owner,severity,status_code,platform,created_at)
 VALUES ($1,$2,$3,$4,$5,$6::jsonb,'upstream',$7,'provider','error',$8,'openai',$9)`,
			key.ID, group.ID, request, accountID, model, attempts, kind, status, at)
		require.NoError(t, err)
	}
	oauthAttempts := fmt.Sprintf(`[{"account_id":%d}]`, oauth.ID)
	for i, tc := range []struct {
		account         *int64
		model, attempts string
	}{
		{&api.ID, "qwen3.8-max", "[]"},
		{&api.ID, "gpt-example", oauthAttempts}, // Earlier OAuth failure does not override the final API Key.
		{&other.ID, "gpt-example", "[]"},
		{nil, "qwen3.8-max", "[]"},
		{nil, "gpt-example", "[]"}, // Model/group alone is not attribution.
		{nil, "gpt-example", fmt.Sprintf(`[{"account_id":%d},{"account_id":%d}]`, oauth.ID, api.ID)},
		{nil, "gpt-example", `[{}, {"account_id":"9999999999999999999999999"}]`},
		{nil, "gpt-example", `{"invalid":"shape"}`},
	} {
		insertError(fmt.Sprintf("unrelated-%d", i), tc.account, tc.model, tc.attempts, "upstream_error", 503, start)
	}
	// The latest persisted final outcome wins before attribution.
	insertError("duplicated", &oauth.ID, "alias", "[]", "upstream_error", 502, start)
	insertError("duplicated", &api.ID, "alias", "[]", "upstream_error", 502, start.Add(time.Second))
	insertError("canceled", &oauth.ID, "alias", "[]", "client_canceled", 499, start)
	insertError("outside", &oauth.ID, "alias", "[]", "upstream_error", 503, end)
	repo := NewOpsRepository(integrationDB).(*opsRepository)
	filter := &service.OpsDashboardFilter{StartTime: start, EndTime: end, Platform: service.PlatformOpenAI, GroupID: &group.ID, OpenAIOAuthOnly: true}
	stats, err := repo.GetWindowStats(ctx, filter)
	require.NoError(t, err)
	require.EqualValues(t, 3, stats.SuccessCount)
	require.Zero(t, stats.ErrorCountSLA)
	require.Equal(t, 1.0, stats.SLA, "Qwen, API Key, unknown and canceled errors must not open fallback")
	// A failing/paused account's historical evidence remains relevant.
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET status='error',schedulable=false,deleted_at=NOW() WHERE id=$1", oauth.ID)
	require.NoError(t, err)
	insertError("oauth-auth", &oauth.ID, "nonstandard-alias", "[]", "upstream_error", 503, start)
	insertError("oauth-exhausted", nil, "nonstandard-alias", oauthAttempts, "upstream_error", 503, start)
	stats, err = repo.GetWindowStats(ctx, filter)
	require.NoError(t, err)
	require.EqualValues(t, 2, stats.ErrorCountSLA)
	require.EqualValues(t, 5, stats.RequestCountSLA)
	require.InDelta(t, 0.6, stats.SLA, 1e-9)
	// Platform hourly aggregates have no account type; even forced preagg must
	// read raw scoped rows instead of returning missing or unscoped aggregates.
	for _, mode := range []service.OpsQueryMode{service.OpsQueryModeRaw, service.OpsQueryModeAuto, service.OpsQueryModePreagg} {
		filter.QueryMode = mode
		overview, err := repo.GetDashboardOverview(ctx, filter)
		require.NoError(t, err)
		require.EqualValues(t, 5, overview.RequestCountSLA)
		require.InDelta(t, 0.6, overview.SLA, 1e-9)
	}
	filter.OpenAIOAuthOnly = false
	global, err := repo.GetWindowStats(ctx, filter)
	require.NoError(t, err)
	require.EqualValues(t, 5, global.SuccessCount)
	require.Greater(t, global.ErrorCountSLA, stats.ErrorCountSLA, "regular operations monitoring retains compatible traffic")
}
