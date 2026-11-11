//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPriorityLatencySeriesErrorsOnlyBucket(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	account := mustCreateAccount(t, client, &service.Account{Name: "errors-only", Platform: service.PlatformOpenAI})
	groupID := int64(918273)
	createdAt := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	requestID := fmt.Sprintf("latency-errors-only-%d", time.Now().UnixNano())
	_, err := tx.ExecContext(ctx, `
		INSERT INTO ops_error_logs(account_id,group_id,request_id,model,requested_model,request_path,error_phase,error_type,severity,status_code,created_at)
		VALUES($1,$2,$3,'fixture-model','fixture-model','/v1/responses','upstream','http_error','error',503,$4)
	`, account.ID, groupID, requestID, createdAt)
	require.NoError(t, err)
	group := groupID
	got, err := repo.ReadPriorityLatencySeries(ctx, service.PriorityLatencySeriesQuery{
		AccountIDs: []int64{account.ID}, Model: "fixture-model", GroupID: &group,
		Since: createdAt.Add(-time.Minute), BucketMinutes: 5, SlowTTFTMs: 1000,
	})
	require.NoError(t, err)
	require.Len(t, got, 1, "an upstream error without a usage row must still produce its bucket")
	require.Equal(t, account.ID, got[0].AccountID)
	require.Zero(t, got[0].Samples)
	require.Equal(t, 1, got[0].ErrorSamples)
}

func TestPriorityLatencySeriesUsesConfiguredMeasurementAndPercentile(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	user := mustCreateUser(t, client, &service.User{Email: "latency-measurement@test.invalid"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-latency-measurement-fixture", Name: "test"})
	account := mustCreateAccount(t, client, &service.Account{Name: "measured", Platform: service.PlatformOpenAI})
	delays := []int{1000, 2000, 10000, 20000, 40000}
	now := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	for i, delay := range delays {
		legacy := 99000
		usage := &service.UsageLog{
			UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID,
			RequestID: fmt.Sprintf("priority-series-measurement-%d-%d", time.Now().UnixNano(), i),
			Model:     "fixture-model", InputTokens: 1, Stream: true, FirstTokenMs: &legacy,
			CreatedAt: now.Add(-time.Duration(len(delays)-i) * time.Minute),
		}
		_, err := repo.Create(ctx, usage)
		require.NoError(t, err)
		detail, err := json.Marshal(map[string]any{
			"version": 1, "status": "200", "outcome": "success", "terminal": "completed",
			"truncated": false, "canceled": false, "client_disconnect": false,
			"downstream_error": false, "body_complete": true,
			"attempts": []any{map[string]any{
				"kind": "http", "account_id": account.ID, "start_ms": 1000,
				"status": "200", "error": "", "terminal": "completed",
				"events": map[string]any{"first_semantic": 1000 + delay},
			}},
		})
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `INSERT INTO request_timing_details(usage_log_id,trace_id,detail) VALUES($1,$2,$3)`, usage.ID, uuid.NewString(), string(detail))
		require.NoError(t, err)
	}

	var seriesQuery service.PriorityLatencySeriesQuery
	err := json.Unmarshal([]byte(`{"LatencyPercentile":50,"LatencyMeasurement":"attributed_v1"}`), &seriesQuery)
	require.NoError(t, err)
	seriesQuery.AccountIDs = []int64{account.ID}
	seriesQuery.Model = "fixture-model"
	seriesQuery.Since = now.Add(-time.Hour)
	seriesQuery.BucketMinutes = 1440
	series, err := repo.ReadPriorityLatencySeries(ctx, seriesQuery)
	require.NoError(t, err)
	require.Len(t, series, 1)
	encoded, err := json.Marshal(series[0])
	require.NoError(t, err)
	var bucket map[string]any
	require.NoError(t, json.Unmarshal(encoded, &bucket))
	selected, exists := bucket["selected_ttft_ms"]
	require.True(t, exists, "latency series must expose the configured percentile")
	selectedTTFTMs, ok := selected.(float64)
	require.True(t, ok, "selected latency must be numeric for a measured bucket")
	require.InDelta(t, 10000, selectedTTFTMs, .01, "legacy latency endpoint retains its configured percentile independently of account performance scoring")
}
