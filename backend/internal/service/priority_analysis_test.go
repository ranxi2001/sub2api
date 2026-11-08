package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

type analysisAccounts struct {
	AccountRepository
	accounts []Account
}

func (r *analysisAccounts) ListModelAvailabilityCandidates(_ context.Context, group *int64, platforms []string, includeGrouped bool) ([]Account, error) {
	return r.accounts, nil
}

type analysisHistory struct {
	UsageLogRepository
	signals PrioritySchedulingQuery
	buckets PriorityLatencySeriesQuery
	err     error
	omit    bool
}

func (r *analysisHistory) ReadPrioritySchedulingSignals(_ context.Context, q PrioritySchedulingQuery) (map[int64]PrioritySchedulingSignal, error) {
	r.signals = q
	rows := make(map[int64]PrioritySchedulingSignal)
	for _, id := range q.AccountIDs {
		if r.omit {
			continue
		}
		rows[id] = PrioritySchedulingSignal{}
	}
	return rows, r.err
}

func TestPriorityAnalysisMissingSignalIsIncomplete(t *testing.T) {
	got, err := ReadPriorityAnalysis(context.Background(), &analysisAccounts{accounts: []Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}}}, &analysisHistory{omit: true}, DefaultPrioritySchedulingConfig(), PriorityAnalysisQuery{Model: "fixture", Minutes: 60})
	require.ErrorContains(t, err, "incomplete history")
	require.Nil(t, got)
}
func (r *analysisHistory) ReadPriorityLatencySeries(_ context.Context, q PriorityLatencySeriesQuery) ([]PriorityLatencyBucket, error) {
	r.buckets = q
	return nil, r.err
}

func TestPriorityAnalysisCompleteExplicitScope(t *testing.T) {
	accounts := &analysisAccounts{}
	for i := 1; i <= 125; i++ {
		accounts.accounts = append(accounts.accounts, Account{ID: int64(i), Name: fmt.Sprintf("Fixture %d", i), Platform: PlatformOpenAI, Type: AccountTypeAPIKey})
	}
	history := &analysisHistory{}
	cfg := DefaultPrioritySchedulingConfig()
	group := int64(17)
	got, err := ReadPriorityAnalysis(context.Background(), accounts, history, cfg, PriorityAnalysisQuery{Model: "not-in-latest-pool", GroupID: &group, Minutes: 15})
	require.NoError(t, err)
	require.Len(t, got.Snapshot.Candidates, 125, "analysis must not truncate to the last snapshot's 100 rows")
	require.Len(t, got.Latency.Accounts, 125)
	require.Equal(t, "not-in-latest-pool", history.signals.Model)
	require.Equal(t, group, *history.signals.GroupID)
	require.Equal(t, 15, got.Snapshot.WindowMinutes)
	require.Equal(t, priorityConfigRevision(cfg), got.Snapshot.ConfigRevision)
	require.Equal(t, cfg.TargetTTFTMs, got.Latency.TargetTTFTMs, "display target must come from the captured revision")
	require.Equal(t, history.signals.UsageUntil, history.buckets.Until)
	require.Equal(t, history.signals.UsageSince, history.buckets.Since)
	require.Nil(t, got.Snapshot.Candidates[0].LoadPercent, "unobserved live capacity is unknown, never invented zero")
	require.Equal(t, 10, got.Snapshot.Candidates[0].CapacityBand)
	require.Positive(t, got.Snapshot.Candidates[0].SelectionWeight)
	require.Equal(t, "ready", got.Snapshot.HistoryStatus)
}

func TestPriorityAnalysisReadErrorIsNotEmptyHistory(t *testing.T) {
	history := &analysisHistory{err: errors.New("fixture database failure")}
	got, err := ReadPriorityAnalysis(context.Background(), &analysisAccounts{accounts: []Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}}}, history, DefaultPrioritySchedulingConfig(), PriorityAnalysisQuery{Model: "fixture", Minutes: 60})
	require.Error(t, err)
	require.Nil(t, got, "a failed history read must not appear as zero samples")
}

func TestPriorityAnalysisPublicHistoryAndRoutedQuality(t *testing.T) {
	accounts := &analysisAccounts{accounts: []Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"route-model": "upstream-model"}}}}}
	history := &analysisHistory{}
	got, err := ReadPriorityAnalysis(context.Background(), accounts, history, DefaultPrioritySchedulingConfig(), PriorityAnalysisQuery{Model: "public-alias", RouteModel: "route-model", Minutes: 60})
	require.NoError(t, err)
	require.Len(t, got.Snapshot.Candidates, 1)
	require.Equal(t, "public-alias", history.signals.Model)
	require.Equal(t, "route-model", history.signals.QualityModel)
}

func TestPriorityAnalysisModelCatalogUsesPersistentMappingsAndConcreteNames(t *testing.T) {
	group := int64(17)
	rows := []Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"custom-model": "upstream", "wildcard-*": "upstream"}}}}
	models := priorityAnalysisModelNames(rows, &group, []string{"public-alias", "wildcard-*", "configured-model"})
	require.Contains(t, models, "custom-model")
	require.Contains(t, models, "public-alias")
	require.Contains(t, models, "configured-model")
	require.NotContains(t, models, "wildcard-*")
}
