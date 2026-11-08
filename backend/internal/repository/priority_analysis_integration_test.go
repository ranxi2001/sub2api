//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

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
	now := time.Now()
	add := func(model string, g *int64, at time.Time, ms int) {
		_, err := usage.Create(ctx, &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: first.ID, RequestID: fmt.Sprintf("analysis-%d", time.Now().UnixNano()), Model: model, RequestedModel: model, GroupID: g, Stream: true, FirstTokenMs: &ms, InputTokens: 10, ActualCost: 1, TotalCost: 2, CreatedAt: at})
		require.NoError(t, err)
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
	require.Len(t, got.Latency.Accounts, 105)
	for _, row := range got.Snapshot.Candidates {
		require.NotEqual(t, excluded.ID, row.AccountID)
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
	require.Equal(t, 1, find(got).Samples)
	require.InDelta(t, 100, find(got).P90TTFTMs, .01)
	q.Minutes = 60
	wide, err := service.ReadPriorityAnalysis(ctx, accounts, usage, cfg, q)
	require.NoError(t, err)
	require.Equal(t, 2, find(wide).Samples)
	require.Equal(t, got.Snapshot.ConfigRevision, wide.Snapshot.ConfigRevision, "display window is not a saved configuration mutation")
	q.Model = "no-samples"
	empty, err := service.ReadPriorityAnalysis(ctx, accounts, usage, cfg, q)
	require.NoError(t, err)
	require.Equal(t, "ready", empty.Snapshot.HistoryStatus)
	require.Zero(t, find(empty).Samples)
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
