//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func addPriorityAnalysisTiming(t *testing.T, db sqlExecutor, row *service.UsageLog) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `INSERT INTO request_timing_details(usage_log_id,trace_id,created_at,detail)
VALUES($1,$2::uuid,$3,jsonb_build_object('version',1,'started_at',$4::text,'outcome','success','status',200,
'body_complete',true,'canceled',false,'truncated',false,'client_disconnect',false,'downstream_error',false,'terminal','completed'))`,
		row.ID, uuid.NewString(), row.CreatedAt, row.CreatedAt.Format(time.RFC3339Nano))
	require.NoError(t, err)
}

func TestPriorityAnalysisPostgresScopeWindowAndCompletePool(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	accounts := newAccountRepositoryWithSQL(client, tx, nil)
	usage := newUsageLogRepositoryWithSQL(client, tx)
	group := mustCreateGroup(t, client, &service.Group{Name: "analysis-fixture", Platform: service.PlatformOpenAI})
	other := mustCreateGroup(t, client, &service.Group{Name: "analysis-other", Platform: service.PlatformOpenAI})
	user := mustCreateUser(t, client, &service.User{Email: "analysis@test.invalid"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-analysis-fixture", Name: "fixture"})
	var first *service.Account
	for i := 0; i < 105; i++ {
		a := mustCreateAccount(t, client, &service.Account{Name: fmt.Sprintf("analysis-fixture-%d", i), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
		mustBindAccountToGroup(t, client, a.ID, group.ID, 1)
		if first == nil {
			first = a
		}
	}
	excluded := mustCreateAccount(t, client, &service.Account{Name: "different-group", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	mustBindAccountToGroup(t, client, excluded.ID, other.ID, 1)
	blocked := mustCreateAccount(t, client, &service.Account{Name: "group-model-blocked", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	_, err := client.AccountGroup.Create().SetAccountID(blocked.ID).SetGroupID(group.ID).SetPriority(1).SetAllowedModels([]string{"allowed-only"}).Save(ctx)
	require.NoError(t, err)
	now := time.Now()
	add := func(model string, g *int64, at time.Time, ms int) {
		duration := 10000
		row := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: first.ID, RequestID: fmt.Sprintf("analysis-%d", time.Now().UnixNano()), Model: model, RequestedModel: model, GroupID: g, Stream: true, FirstTokenMs: &ms, DurationMs: &duration, InputTokens: 10, OutputTokens: 10, ActualCost: 1, TotalCost: 2, CreatedAt: at}
		_, err := usage.Create(ctx, row)
		require.NoError(t, err)
		addPriorityAnalysisTiming(t, tx, row)
	}
	add("non-latest-model", &group.ID, now.Add(-time.Minute), 100)
	add("non-latest-model", &group.ID, now.Add(-30*time.Minute), 1000)
	add("other-model", &group.ID, now.Add(-time.Minute), 9999)
	add("non-latest-model", &other.ID, now.Add(-time.Minute), 9999)
	add("non-latest-model", &group.ID, now.Add(time.Minute), 9999)
	cfg := service.DefaultPrioritySchedulingConfig()
	q := service.PriorityAnalysisQuery{Model: "non-latest-model", GroupID: &group.ID, Minutes: 15}
	got, err := service.ReadPriorityAnalysis(ctx, accounts, usage, cfg, q)
	require.NoError(t, err)
	require.Len(t, got.Snapshot.Candidates, 105, "full configured pool must exceed the old 100-row snapshot limit")
	require.Empty(t, got.Latency.Accounts, "shared performance replaces the parallel latency series")
	for _, row := range got.Snapshot.Candidates {
		require.NotEqual(t, excluded.ID, row.AccountID)
		require.NotEqual(t, blocked.ID, row.AccountID)
	}
	find := func(value *service.PriorityAnalysis) service.PrioritySchedulingScore {
		for _, row := range value.Snapshot.Candidates {
			if row.AccountID == first.ID {
				return row
			}
		}
		t.Fatal("missing first account")
		return service.PrioritySchedulingScore{}
	}
	require.Equal(t, 1, find(got).ProfitSamples)
	require.Equal(t, 4, find(got).Samples, "account performance includes all models and groups in its global window")
	require.NotNil(t, find(got).SelectedTTFTMs)
	require.InDelta(t, 5274.5, *find(got).SelectedTTFTMs, .01)
	q.Minutes = 60
	wide, err := service.ReadPriorityAnalysis(ctx, accounts, usage, cfg, q)
	require.NoError(t, err)
	require.Equal(t, 2, find(wide).ProfitSamples)
	require.Equal(t, find(got).Samples, find(wide).Samples)
	require.Equal(t, find(got).SelectedTTFTMs, find(wide).SelectedTTFTMs)
	require.Equal(t, got.Snapshot.ConfigRevision, wide.Snapshot.ConfigRevision, "display window is not a saved configuration mutation")
	q.Model = "no-samples"
	empty, err := service.ReadPriorityAnalysis(ctx, accounts, usage, cfg, q)
	require.NoError(t, err)
	require.Equal(t, "ready", empty.Snapshot.HistoryStatus)
	require.Zero(t, find(empty).ProfitSamples)
	require.Equal(t, find(got).Samples, find(empty).Samples, "changing the financial model must not erase shared account evidence")
	// Enforce a true read-only PostgreSQL session after fixture setup. No data
	// mutator can hide behind a successful GET implementation in this assertion.
	_, err = tx.ExecContext(ctx, `SET LOCAL transaction_read_only = on`)
	require.NoError(t, err)
	_, err = service.ReadPriorityAnalysis(ctx, accounts, usage, cfg, q)
	require.NoError(t, err, "analysis must run in a read-only PostgreSQL transaction")
}

func TestPriorityAnalysisPostgresReadFailureIsNotNoSamples(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	a := mustCreateAccount(t, client, &service.Account{Name: "analysis-error", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	require.Positive(t, a.ID)
	// Rename only in this rollback-only isolated test transaction. Account reads
	// succeed; the actual history SQL fails with PostgreSQL undefined_table.
	_, err := tx.ExecContext(ctx, `ALTER TABLE usage_logs RENAME TO analysis_unavailable_usage_logs`)
	require.NoError(t, err)
	got, err := service.ReadPriorityAnalysis(ctx, newAccountRepositoryWithSQL(client, tx, nil), newUsageLogRepositoryWithSQL(client, tx), service.DefaultPrioritySchedulingConfig(), service.PriorityAnalysisQuery{Model: "fixture", Minutes: 60})
	require.Error(t, err)
	require.ErrorContains(t, err, "priority analysis history")
	require.Nil(t, got)
}

func TestPriorityAnalysisPostgresPublicAliasAndRouteQuality(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	group := mustCreateGroup(t, client, &service.Group{Name: "analysis-alias", Platform: service.PlatformOpenAI})
	account := mustCreateAccount(t, client, &service.Account{Name: "route-only", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"route-model": "upstream-model"}}})
	mustBindAccountToGroup(t, client, account.ID, group.ID, 1)
	user := mustCreateUser(t, client, &service.User{Email: "analysis-alias@test.invalid"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-analysis-alias-fixture", Name: "fixture"})
	usage := newUsageLogRepositoryWithSQL(client, tx)
	now := time.Now().Add(-time.Minute)
	ms := 1234
	duration := 2000
	row := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: fmt.Sprintf("analysis-alias-%d", time.Now().UnixNano()), Model: "route-model", RequestedModel: "public-alias", GroupID: &group.ID, Stream: true, FirstTokenMs: &ms, DurationMs: &duration, InputTokens: 1, OutputTokens: 2, CreatedAt: now}
	_, err := usage.Create(ctx, row)
	require.NoError(t, err)
	addPriorityAnalysisTiming(t, tx, row)
	rows, err := tx.QueryContext(ctx, `INSERT INTO scheduled_test_plans(account_id,model_id,cron_expression,enabled,pelican_config,updated_at) VALUES($1,'route-model','*/30 * * * *',true,'{"quality":{},"parallel_count":1}',NOW()-INTERVAL '1 hour') RETURNING id`, account.ID)
	require.NoError(t, err)
	var plan int64
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&plan))
	require.NoError(t, rows.Close())
	_, err = tx.ExecContext(ctx, `INSERT INTO scheduled_test_results(plan_id,status,started_at,finished_at,created_at,pelican_config,quality_round_id,quality_action,quality_judgment) VALUES($1,'success',$2,$2,$2,'{"model_id":"route-model","parallel_count":1}','analysis-alias-round','passed','{"verdict":"correct"}')`, plan, now)
	require.NoError(t, err)
	got, err := service.ReadPriorityAnalysis(ctx, newAccountRepositoryWithSQL(client, tx, nil), usage, service.DefaultPrioritySchedulingConfig(), service.PriorityAnalysisQuery{Model: "public-alias", RouteModel: "route-model", GroupID: &group.ID, Minutes: 60})
	require.NoError(t, err)
	require.Len(t, got.Snapshot.Candidates, 1)
	require.Equal(t, "public-alias", got.Snapshot.Model)
	require.Equal(t, 1, got.Snapshot.Candidates[0].Samples)
	require.Equal(t, 1, got.Snapshot.Candidates[0].QualitySamples)
	require.Equal(t, 1, got.Snapshot.Candidates[0].QualityPassed)
}
