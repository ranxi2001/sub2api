//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPriorityObservationPersistedPublicAliasAndGroup(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	user := mustCreateUser(t, client, &service.User{Email: "observation@test.invalid"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-observation-synthetic", Name: "test"})
	account := mustCreateAccount(t, client, &service.Account{Name: "observation", Platform: service.PlatformOpenAI})
	group := mustCreateGroup(t, client, &service.Group{Name: "observation", Platform: service.PlatformOpenAI})
	query := service.PrioritySchedulingQuery{AccountIDs: []int64{account.ID}, Model: "public-composite", QualityModel: "channel-route", GroupID: &group.ID, UsageSince: time.Now().Add(-time.Hour), QualitySince: time.Now().Add(-time.Hour)}
	committedReader := newUsageLogRepositoryWithSQL(integrationEntClient, integrationDB)
	before, err := committedReader.ReadPrioritySchedulingSignals(ctx, query)
	require.NoError(t, err)
	require.Zero(t, before[account.ID].Samples)
	ttft := 1234
	upstream := "account-upstream"
	add := func(id, public string, groupID *int64, first *int) {
		t.Helper()
		_, err := repo.Create(ctx, &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: id, Model: "channel-route", UpstreamModel: &upstream, RequestedModel: public, GroupID: groupID, Stream: true, FirstTokenMs: first, InputTokens: 10, ActualCost: 1, TotalCost: 2, CreatedAt: time.Now()})
		require.NoError(t, err)
	}
	add("observation-completed", "public-composite", &group.ID, &ttft)
	add("observation-unknown", "public-composite", &group.ID, nil)
	add("observation-other-model", "other-public", &group.ID, &ttft)
	add("observation-other-group", "public-composite", nil, &ttft)
	// Quality remains the route/account probe population, independently of the
	// public usage alias. This must exercise the SQL QualityModel parameter.
	rows, err := tx.QueryContext(ctx, `INSERT INTO scheduled_test_plans(account_id,model_id,cron_expression,enabled,pelican_config,updated_at) VALUES($1,'channel-route','*/30 * * * *',true,'{"quality":{},"parallel_count":1}',NOW()-INTERVAL '1 hour') RETURNING id`, account.ID)
	require.NoError(t, err)
	var plan int64
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&plan))
	require.NoError(t, rows.Close())
	_, err = tx.ExecContext(ctx, `INSERT INTO scheduled_test_results(plan_id,status,started_at,finished_at,created_at,pelican_config,quality_round_id,quality_action,quality_judgment) VALUES($1,'success',NOW(),NOW(),NOW(),'{"model_id":"channel-route","parallel_count":1}','observation-round','passed','{"verdict":"correct"}')`, plan)
	require.NoError(t, err)
	// A separate SQL connection must see no uncommitted usage, then see the
	// completed canonical inserts after commit. No in-flight timing injection.
	uncommitted, err := committedReader.ReadPrioritySchedulingSignals(ctx, query)
	require.NoError(t, err)
	require.Zero(t, uncommitted[account.ID].Samples)
	require.NoError(t, tx.Commit())
	t.Cleanup(func() {
		cleanup := []struct {
			sql string
			id  int64
		}{
			{`DELETE FROM scheduled_test_results WHERE plan_id=$1`, plan},
			{`DELETE FROM scheduled_test_plans WHERE id=$1`, plan},
			{`DELETE FROM usage_logs WHERE account_id=$1`, account.ID},
			{`DELETE FROM accounts WHERE id=$1`, account.ID},
			{`DELETE FROM api_keys WHERE id=$1`, key.ID},
			{`DELETE FROM groups WHERE id=$1`, group.ID},
			{`DELETE FROM users WHERE id=$1`, user.ID},
		}
		for _, q := range cleanup {
			_, err := integrationDB.ExecContext(ctx, q.sql, q.id)
			require.NoError(t, err)
		}
	})
	got, err := committedReader.ReadPrioritySchedulingSignals(ctx, query)
	require.NoError(t, err)
	require.Equal(t, 2, got[account.ID].ProfitSamples)
	require.NotNil(t, got[account.ID].Performance)
	require.Nil(t, got[account.ID].Performance.AvgTTFTMs, "untraced legacy usage is not valid performance evidence")
	require.Equal(t, 1, got[account.ID].Performance.QualitySamples)
	query.QualityModel = "different-route"
	qualityOther, err := committedReader.ReadPrioritySchedulingSignals(ctx, query)
	require.NoError(t, err)
	require.Equal(t, got[account.ID].Performance.QualitySamples, qualityOther[account.ID].Performance.QualitySamples, "account performance pools probe models")
	query.Model = "other-public"
	other, err := committedReader.ReadPrioritySchedulingSignals(ctx, query)
	require.NoError(t, err)
	require.Equal(t, 1, other[account.ID].ProfitSamples)
	query.Model, query.GroupID = "public-composite", nil
	other, err = committedReader.ReadPrioritySchedulingSignals(ctx, query)
	require.NoError(t, err)
	require.Equal(t, 1, other[account.ID].ProfitSamples)
	query.Model = "channel-route"
	other, err = committedReader.ReadPrioritySchedulingSignals(ctx, query)
	require.NoError(t, err)
	require.Zero(t, other[account.ID].ProfitSamples, "financial history retains public model identity")
}
