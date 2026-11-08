package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type priorityLatencySeriesRegressionReader struct {
	UsageLogRepository
	query PriorityLatencySeriesQuery
}

func (r *priorityLatencySeriesRegressionReader) ReadPriorityLatencySeries(_ context.Context, query PriorityLatencySeriesQuery) ([]PriorityLatencyBucket, error) {
	r.query = query
	return nil, nil
}

func TestPriorityLatencySeriesNoHistorySerializesEmptyBuckets(t *testing.T) {
	svc := &OpenAIGatewayService{usageLogRepo: &priorityLatencySeriesRegressionReader{}}
	cfg := DefaultPrioritySchedulingConfig()
	svc.priorityScheduling.latestInput = &prioritySnapshotInput{
		limited: true,
		request: OpenAIAccountScheduleRequest{RequestedModel: "fixture-model"},
		config:  cfg,
		candidates: []openAIAccountCandidateScore{
			{account: &Account{ID: 77, Name: "No history"}},
		},
	}

	got, err := svc.PriorityLatencySeries(context.Background(), 60, 5)
	require.NoError(t, err)
	require.Len(t, got.Accounts, 1)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	var response struct {
		Accounts []struct {
			Buckets json.RawMessage `json:"buckets"`
		} `json:"accounts"`
	}
	require.NoError(t, json.Unmarshal(raw, &response))
	require.JSONEq(t, `[]`, string(response.Accounts[0].Buckets), "the public API must represent an empty series as an array")
}

func TestPriorityLatencySeriesExposesConfigurationScope(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	svc := &OpenAIGatewayService{usageLogRepo: &priorityLatencySeriesRegressionReader{}}
	svc.priorityScheduling.latestInput = &prioritySnapshotInput{
		limited:    true,
		request:    OpenAIAccountScheduleRequest{RequestedModel: "fixture-model"},
		config:     cfg,
		candidates: []openAIAccountCandidateScore{{account: &Account{ID: 77, Name: "Fixture"}}},
	}
	series, err := svc.PriorityLatencySeries(context.Background(), 60, 5)
	require.NoError(t, err)
	raw, err := json.Marshal(series)
	require.NoError(t, err)
	var response map[string]any
	require.NoError(t, json.Unmarshal(raw, &response))
	require.Equal(t, priorityConfigRevision(cfg), response["config_revision"], "latency scope must expose exact configuration revision")
}

func TestPriorityLatencySeriesUsesSnapshotMeasurementAndPercentile(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	cfg.LatencyPercentile = 50
	cfg.LatencyMeasurement = "attributed_v1"
	reader := &priorityLatencySeriesRegressionReader{}
	svc := &OpenAIGatewayService{usageLogRepo: reader}
	svc.priorityScheduling.latestInput = &prioritySnapshotInput{
		limited: true,
		request: OpenAIAccountScheduleRequest{RequestedModel: "fixture-model"},
		config:  cfg,
		candidates: []openAIAccountCandidateScore{
			{account: &Account{ID: 77, Name: "No history"}},
		},
	}

	_, err := svc.PriorityLatencySeries(context.Background(), 60, 5)
	require.NoError(t, err)
	raw, err := json.Marshal(reader.query)
	require.NoError(t, err)
	var query map[string]any
	require.NoError(t, json.Unmarshal(raw, &query))
	require.Equal(t, float64(50), query["LatencyPercentile"])
	require.Equal(t, "attributed_v1", query["LatencyMeasurement"])
}

func TestPriorityRecomputeDoesNotTreatHistoryErrorAsSuccess(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	reader := &priorityReaderStub{err: context.DeadlineExceeded}
	svc := priorityGateway(cfg, reader)
	svc.priorityScheduling.latestInput = newPrioritySnapshotInput(
		OpenAIAccountScheduleRequest{RequestedModel: "fixture-model"}, cfg,
		[]openAIAccountCandidateScore{priorityCandidate(77, 1, 1)},
		time.Now(),
	)

	result, err := svc.RecomputePriorityScheduling(context.Background())
	require.Error(t, err, "a history error is not a successful recomputation")
	require.Nil(t, result)
}

type priorityRecomputeScopeRegressionReader struct {
	UsageLogRepository
	once    sync.Once
	service *OpenAIGatewayService
}

func (r *priorityRecomputeScopeRegressionReader) ReadPrioritySchedulingSignals(_ context.Context, query PrioritySchedulingQuery) (map[int64]PrioritySchedulingSignal, error) {
	r.once.Do(func() {
		time.Sleep(25 * time.Millisecond)
		group := int64(22)
		next := newPrioritySnapshotInput(
			OpenAIAccountScheduleRequest{RequestedModel: "fixture-model", GroupID: &group},
			DefaultPrioritySchedulingConfig(),
			[]openAIAccountCandidateScore{priorityCandidate(88, 1, 1)},
			time.Now().Add(time.Second),
		)
		r.service.priorityScheduling.mu.Lock()
		r.service.priorityScheduling.latestInput = next
		r.service.priorityScheduling.mu.Unlock()
	})
	result := make(map[int64]PrioritySchedulingSignal, len(query.AccountIDs))
	for _, id := range query.AccountIDs {
		result[id] = PrioritySchedulingSignal{Samples: 10, P90TTFTMs: float64(id)}
	}
	return result, nil
}

func TestPriorityRecomputeKeepsCapturedScopeAcrossNewSelection(t *testing.T) {
	reader := &priorityRecomputeScopeRegressionReader{}
	svc := &OpenAIGatewayService{usageLogRepo: reader}
	reader.service = svc
	cfg := DefaultPrioritySchedulingConfig()
	group := int64(11)
	svc.priorityScheduling.latestInput = newPrioritySnapshotInput(
		OpenAIAccountScheduleRequest{RequestedModel: "fixture-model", GroupID: &group}, cfg,
		[]openAIAccountCandidateScore{priorityCandidate(77, 1, 1)},
		time.Now(),
	)
	_ = svc.priorityHistory(svc.priorityScheduling.latestInput.request, cfg, svc.priorityScheduling.latestInput.candidates)

	result, err := svc.RecomputePriorityScheduling(context.Background())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Before.GroupID)
	require.NotNil(t, result.After.GroupID)
	require.Equal(t, int64(11), *result.Before.GroupID)
	require.Equal(t, int64(11), *result.After.GroupID, "a newer selection must not move an in-flight recompute to another group")
}
