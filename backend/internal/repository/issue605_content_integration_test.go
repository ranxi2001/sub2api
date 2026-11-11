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

func TestIssue605UsageContentBatchAndLayeredSQL(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	u := mustCreateUser(t, client, &service.User{Email: "issue605@synthetic.invalid"})
	k := mustCreateApiKey(t, client, &service.APIKey{UserID: u.ID, Key: "synthetic605", Name: "synthetic"})
	a := mustCreateAccount(t, client, &service.Account{Name: "synthetic605", Platform: service.PlatformOpenAI})
	now := time.Now().UTC().Add(-time.Minute)
	end := 50.0
	var ids []int64
	for i, effort := range []string{"xhigh", "medium"} {
		latency := 100 + i*200
		usage := &service.UsageLog{UserID: u.ID, APIKeyID: k.ID, AccountID: a.ID, Model: "fixture", Stream: true, InputTokens: 1, FirstTokenMs: &latency, ReasoningEffort: &effort, CreatedAt: now}
		_, err := repo.Create(ctx, usage)
		require.NoError(t, err)
		ids = append(ids, usage.ID)
		s := requesttiming.Snapshot{TotalMS: 1000, Attempts: []requesttiming.Attempt{{Kind: "http", AccountID: a.ID, StartMS: 0, EndMS: &end, ProxyID: 2, Events: map[string]float64{"first_byte": 10, "first_reasoning": float64(latency), "first_visible_text": float64(latency + 100)}}}}
		raw, err := json.Marshal(s)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `INSERT INTO request_timing_details(usage_log_id,trace_id,detail)VALUES($1,$2,$3::jsonb)`, usage.ID, uuid.NewString(), string(raw))
		require.NoError(t, err)
	}
	items, err := repo.ReadUsageContentTimings(ctx, ids)
	require.NoError(t, err)
	require.Equal(t, 100.0, *items[ids[0]].ReasoningFirstMS)
	require.Equal(t, 200.0, *items[ids[0]].VisibleTextFirstMS)
	require.Equal(t, 10.0, *items[ids[0]].ResponseByteFirstMS)
	require.Equal(t, "proxy_obscured", items[ids[0]].UpstreamSocket.Status)
	groups, err := repo.ReadLatencyReport(ctx, service.LatencyReportFilter{Start: now.Add(-time.Minute), End: now.Add(time.Minute), UserID: u.ID})
	require.NoError(t, err)
	require.Len(t, groups, 2)
	for _, g := range groups {
		require.Equal(t, a.ID, g.AccountID)
		require.Equal(t, "fixture", g.Model)
		require.EqualValues(t, 1, g.ReasoningSamples)
		require.EqualValues(t, 1, g.TextSamples)
		require.EqualValues(t, 1, g.ByteSamples)
		require.NotNil(t, g.TextP90MS)
	}
	effort := "xhigh"
	signals, err := repo.ReadPrioritySchedulingSignals(ctx, service.PrioritySchedulingQuery{AccountIDs: []int64{a.ID}, Model: "fixture", UsageSince: now.Add(-time.Minute), UsageUntil: now.Add(time.Minute), QualitySince: now.Add(-time.Minute), ObservationEffort: &effort})
	require.NoError(t, err)
	require.EqualValues(t, 1, signals[a.ID].ProfitSamples)
	require.Nil(t, signals[a.ID].Performance.AvgTTFTMs, "incomplete trace evidence remains unknown in shared account performance")
	// A failed earlier attempt on another account must not enter the billed
	// account's typed latency population, even though upload components remain.
	mixed := requesttiming.Snapshot{TotalMS: 1000, Attempts: []requesttiming.Attempt{
		{Kind: "http", AccountID: a.ID + 99, StartMS: 0, EndMS: &end, Events: map[string]float64{"first_reasoning": 900, "first_visible_text": 950}},
		{Kind: "http", AccountID: a.ID, StartMS: 0, EndMS: &end, Events: map[string]float64{"first_byte": 10, "first_reasoning": 100, "first_visible_text": 200}},
	}}
	payload, err := json.Marshal(mixed)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE request_timing_details SET detail=$2::jsonb WHERE usage_log_id=$1`, ids[0], string(payload))
	require.NoError(t, err)
	groups, err = repo.ReadLatencyReport(ctx, service.LatencyReportFilter{Start: now.Add(-time.Minute), End: now.Add(time.Minute), UserID: u.ID})
	require.NoError(t, err)
	for _, g := range groups {
		if g.ReasoningEffort == "xhigh" {
			require.EqualValues(t, 1, g.TextSamples)
			require.Equal(t, 200.0, *g.TextP90MS, "only final matching-account content can enter account/model/effort quantiles")
		}
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO request_timing_details(usage_log_id,trace_id,detail)VALUES($1,$2,'{}'::jsonb)`, ids[0], uuid.NewString())
	require.NoError(t, err)
	items, err = repo.ReadUsageContentTimings(ctx, ids)
	require.NoError(t, err)
	require.Nil(t, items[ids[0]].ReasoningFirstMS, "multiple traces for one billing row cannot invent one timeline")
}
