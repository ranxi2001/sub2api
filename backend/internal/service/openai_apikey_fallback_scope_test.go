package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type oauthFallbackScopedRepo struct {
	OpsRepository
	t        *testing.T
	stats    OpsWindowStats
	overview OpsDashboardOverview
}

func (r *oauthFallbackScopedRepo) check(filter *OpsDashboardFilter) {
	r.t.Helper()
	require.True(r.t, filter.OpenAIOAuthOnly)
	require.Equal(r.t, OpsQueryModeRaw, filter.QueryMode)
	require.Equal(r.t, 5*time.Minute, filter.EndTime.Sub(filter.StartTime))
}
func (r *oauthFallbackScopedRepo) GetWindowStats(_ context.Context, filter *OpsDashboardFilter) (*OpsWindowStats, error) {
	r.check(filter)
	return &r.stats, nil
}
func (r *oauthFallbackScopedRepo) GetDashboardOverview(_ context.Context, filter *OpsDashboardFilter) (*OpsDashboardOverview, error) {
	r.check(filter)
	return &r.overview, nil
}

func TestOpenAIAPIKeyFallbackScopedMetricAndInfrastructureIsolation(t *testing.T) {
	now := time.Now().UTC()
	f := false
	full := 100.0
	repo := &oauthFallbackScopedRepo{t: t,
		stats: OpsWindowStats{SLA: 0.8, RequestCountSLA: 100},
		overview: OpsDashboardOverview{SLA: 1, RequestCountSLA: 100,
			SystemMetrics: &OpsSystemMetricsSnapshot{DBOK: &f, RedisOK: &f, CPUUsagePercent: &full, MemoryUsagePercent: &full},
			JobHeartbeats: []*OpsJobHeartbeat{{LastErrorAt: &now}}},
	}
	svc := &OpsService{opsRepo: repo}
	obs, err := svc.GetOpenAIAPIKeyFallbackMetric(t.Context(), "sla", now.Add(-5*time.Minute), now)
	require.NoError(t, err)
	require.Equal(t, &OpenAIAPIKeyFallbackObservation{Value: 80, Samples: 100}, obs)
	obs, err = svc.GetOpenAIAPIKeyFallbackMetric(t.Context(), "health_score", now.Add(-5*time.Minute), now)
	require.NoError(t, err)
	require.Equal(t, 100.0, obs.Value, "global infrastructure cannot trigger OAuth fallback")
	require.NotNil(t, repo.overview.SystemMetrics, "do not mutate the repository result")
	repo.overview.ErrorRate = 0.2
	obs, err = svc.GetOpenAIAPIKeyFallbackMetric(t.Context(), "health_score", now.Add(-5*time.Minute), now)
	require.NoError(t, err)
	require.Less(t, obs.Value, 85.0, "OAuth request failures still lower health")

	accounts := &apiKeyFallbackAccounts{targets: []*Account{{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive}}}
	accountOps := apiKeyFallbackFixture(t, accounts, &apiKeyFallbackMetrics{})
	accountOps.fallbackSLAReader.Store(openAIAPIKeyFallbackReader{reader: svc})
	repo.stats = OpsWindowStats{} // Non-OAuth failures yield no OAuth sample.
	accountOps.evaluateOpenAIAPIKeyFallback(t.Context())
	require.Empty(t, accounts.enabled)
	state := accountOps.OpenAIAPIKeyFallbackStatus()
	state.LastMetricCheckedAt = nil
	accountOps.fallbackStatus.Store(state)
	repo.stats = OpsWindowStats{SLA: 0.8, RequestCountSLA: 100}
	accountOps.evaluateOpenAIAPIKeyFallback(t.Context())
	require.Equal(t, []int64{10}, accounts.enabled)
}
