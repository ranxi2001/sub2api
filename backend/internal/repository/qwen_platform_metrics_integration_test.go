//go:build integration

package repository

import (
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestQwenPlatformMetricsAreSeparateFromOpenAI(t *testing.T) {
	ctx := t.Context()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID})
	qwen := mustCreateAccount(t, client, &service.Account{Name: "qwen-metrics", Platform: service.PlatformQwen, Type: service.AccountTypeAPIKey})
	openai := mustCreateAccount(t, client, &service.Account{Name: "openai-metrics", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth})
	qwenGroup := mustCreateGroup(t, client, &service.Group{Name: "qwen-metrics", Platform: service.PlatformQwen})
	openaiGroup := mustCreateGroup(t, client, &service.Group{Name: "openai-metrics", Platform: service.PlatformOpenAI})
	start := time.Date(2021, 5, 16, 0, 0, 0, 0, time.UTC)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_error_logs WHERE api_key_id=$1", key.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM usage_logs WHERE user_id=$1", user.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM api_keys WHERE id=$1", key.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM accounts WHERE id IN ($1,$2)", qwen.ID, openai.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM groups WHERE id IN ($1,$2)", qwenGroup.ID, openaiGroup.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE id=$1", user.ID)
	})
	for _, pair := range [][2]int64{{qwen.ID, qwenGroup.ID}, {openai.ID, openaiGroup.ID}} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,request_id,model,created_at) VALUES($1,$2,$3,$4,$5,'alias',$6)`, user.ID, key.ID, pair[0], pair[1], fmt.Sprintf("qwen-separation-%d", pair[0]), start)
		require.NoError(t, err)
	}
	// Both an upstream error and pre-selection capacity error belong to qwen.
	for _, id := range []*int64{&qwen.ID, nil} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_error_logs(api_key_id,group_id,account_id,platform,model,error_phase,error_type,error_owner,severity,status_code,created_at) VALUES($1,$2,$3,'qwen','qwen3.8-max','routing','no_accounts','platform','error',503,$4)`, key.ID, qwenGroup.ID, id, start)
		require.NoError(t, err)
	}
	repo := NewOpsRepository(integrationDB).(*opsRepository)
	for platform, want := range map[string]int64{service.PlatformOpenAI: 0, service.PlatformQwen: 2} {
		stats, err := repo.GetWindowStats(ctx, &service.OpsDashboardFilter{StartTime: start, EndTime: start.Add(time.Minute), Platform: platform})
		require.NoError(t, err)
		require.EqualValues(t, 1, stats.SuccessCount)
		require.Equal(t, want, stats.ErrorCountSLA)
	}
}
