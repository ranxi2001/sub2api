//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// This regression can be copied unchanged into the fixed production baseline:
// decoding Until through JSON compiles there too, but the old reader ignores it.
func TestPriorityAnalysisLatencyCapturedUpperBound(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	user := mustCreateUser(t, client, &service.User{Email: "analysis-window@test.invalid"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-analysis-window-fixture", Name: "fixture"})
	account := mustCreateAccount(t, client, &service.Account{Name: "analysis-window", Platform: service.PlatformOpenAI})
	now := time.Now().UTC()
	ms := 100
	_, err := repo.Create(ctx, &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: fmt.Sprintf("analysis-window-%d", now.UnixNano()), Model: "fixture", FirstTokenMs: &ms, InputTokens: 1, Stream: true, CreatedAt: now.Add(-time.Minute)})
	require.NoError(t, err)
	raw, err := json.Marshal(map[string]any{"AccountIDs": []int64{account.ID}, "Model": "fixture", "Since": now.Add(-time.Hour), "Until": now.Add(-5 * time.Minute), "BucketMinutes": 5})
	require.NoError(t, err)
	var query service.PriorityLatencySeriesQuery
	require.NoError(t, json.Unmarshal(raw, &query))
	rows, err := repo.ReadPriorityLatencySeries(ctx, query)
	require.NoError(t, err)
	require.Empty(t, rows, "analysis latency must honor the captured window end")
}
