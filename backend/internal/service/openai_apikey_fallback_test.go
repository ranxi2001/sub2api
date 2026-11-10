package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type apiKeyFallbackAccounts struct {
	AccountRepository
	oauth             []Account
	targets           []*Account
	listErr, writeErr error
	enabled           []int64
	revision          string
}

func (r *apiKeyFallbackAccounts) ListAllWithFilters(_ context.Context, platform, kind, status, search string, groupID int64, privacy string) ([]Account, error) {
	if platform != PlatformOpenAI || kind != AccountTypeOAuth || status != "" {
		return nil, errors.New("incorrect OAuth inventory scope")
	}
	return r.oauth, r.listErr
}
func (r *apiKeyFallbackAccounts) GetByIDs(context.Context, []int64) ([]*Account, error) {
	return r.targets, nil
}
func (r *apiKeyFallbackAccounts) EnableOpenAIAPIKeyFallback(_ context.Context, id int64, revision string) (bool, error) {
	if r.writeErr != nil {
		return false, r.writeErr
	}
	r.enabled = append(r.enabled, id)
	r.revision = revision
	for _, a := range r.targets {
		if a.ID == id {
			a.Schedulable = true
		}
	}
	return true, nil
}

type apiKeyFallbackMetrics struct {
	value   float64
	samples int64
	err     error
	calls   int
	metric  string
	window  time.Duration
}

func (r *apiKeyFallbackMetrics) GetOpenAIAPIKeyFallbackMetric(_ context.Context, metric string, start, end time.Time) (*OpenAIAPIKeyFallbackObservation, error) {
	r.calls++
	r.metric = metric
	r.window = end.Sub(start)
	return &OpenAIAPIKeyFallbackObservation{Value: r.value, Samples: r.samples}, r.err
}
func apiKeyFallbackFixture(t *testing.T, accounts *apiKeyFallbackAccounts, metric *apiKeyFallbackMetrics) *AccountOpsService {
	t.Helper()
	cfg := DefaultOAuthAutoConfig()
	cfg.Revision = "fallback-revision"
	cfg.OpenAIAPIKeyFallback.Enabled, cfg.OpenAIAPIKeyFallback.AccountIDs = true, []int64{10}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	svc := NewAccountOpsService(&accountOpsSettingsStub{raw: string(raw)}, nil, nil)
	svc.accounts = accounts
	svc.fallbackSharedJobs.Store(true)
	svc.fallbackSLAReader.Store(openAIAPIKeyFallbackReader{reader: metric})
	return svc
}
func TestOpenAIAPIKeyFallbackIndependentTriggersAndNoSampleGuard(t *testing.T) {
	until := time.Now().Add(time.Hour)
	for _, tc := range []struct {
		name   string
		oauth  []Account
		metric apiKeyFallbackMetrics
		want   bool
	}{
		{name: "all OAuth manually paused", oauth: []Account{{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}}, want: true},
		{name: "all OAuth are error", oauth: []Account{{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusError}}, want: true},
		{name: "all OAuth rate limited", oauth: []Account{{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, RateLimitResetAt: &until}}, want: true},
		{name: "one available OAuth", oauth: []Account{{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}}},
		{name: "no OAuth is not an outage"},
		{name: "no OAuth but poor SLA", metric: apiKeyFallbackMetrics{value: 94, samples: 100}, want: true},
		{name: "no samples", metric: apiKeyFallbackMetrics{value: 0, samples: 0}},
		{name: "equal SLA threshold", metric: apiKeyFallbackMetrics{value: 95, samples: 100}},
		{name: "metric failure", metric: apiKeyFallbackMetrics{value: 0, samples: 100, err: errors.New("unavailable")}},
		{name: "metric failure does not block OAuth trigger", oauth: []Account{{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusError}}, metric: apiKeyFallbackMetrics{err: errors.New("unavailable")}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, RateLimitResetAt: &until, ErrorMessage: "keep", Credentials: map[string]any{"keep": "unchanged"}}
			repo := &apiKeyFallbackAccounts{oauth: tc.oauth, targets: []*Account{account}}
			svc := apiKeyFallbackFixture(t, repo, &tc.metric)
			svc.evaluateOpenAIAPIKeyFallback(t.Context())
			require.Equal(t, tc.want, account.Schedulable)
			if tc.want {
				require.Equal(t, []int64{10}, repo.enabled)
				require.Equal(t, "fallback-revision", repo.revision)
			}
			require.Equal(t, &until, account.RateLimitResetAt)
			require.Equal(t, "keep", account.ErrorMessage)
			require.Equal(t, map[string]any{"keep": "unchanged"}, account.Credentials)
		})
	}
}
func TestOpenAIAPIKeyFallbackTargetsAndFullNodeBoundary(t *testing.T) {
	parent := int64(1)
	past := time.Now().Add(-time.Hour)
	for _, tc := range []struct {
		name    string
		target  Account
		gateway bool
	}{
		{name: "unselected", target: Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive}},
		{name: "error", target: Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusError}},
		{name: "expired", target: Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, ExpiresAt: &past}},
		{name: "inactive", target: Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusDisabled}},
		{name: "shadow", target: Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, ParentAccountID: &parent}},
		{name: "other platform", target: Account{ID: 10, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive}},
		{name: "OAuth target", target: Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}},
		{name: "gateway", target: Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive}, gateway: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &apiKeyFallbackAccounts{targets: []*Account{&tc.target}}
			metric := &apiKeyFallbackMetrics{value: 90, samples: 10}
			svc := apiKeyFallbackFixture(t, repo, metric)
			if tc.gateway {
				svc.fallbackSharedJobs.Store(false)
			}
			svc.evaluateOpenAIAPIKeyFallback(t.Context())
			require.Empty(t, repo.enabled)
		})
	}
}
func TestOpenAIAPIKeyFallbackCachesMetricsAndNeverClosesAfterRecovery(t *testing.T) {
	a := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive}
	repo := &apiKeyFallbackAccounts{targets: []*Account{a}}
	metric := &apiKeyFallbackMetrics{value: 90, samples: 100}
	svc := apiKeyFallbackFixture(t, repo, metric)
	svc.evaluateOpenAIAPIKeyFallback(t.Context())
	svc.evaluateOpenAIAPIKeyFallback(t.Context())
	require.Equal(t, 1, metric.calls)
	require.Equal(t, 5*time.Minute, metric.window)
	require.Equal(t, []int64{10}, repo.enabled)
	state := svc.OpenAIAPIKeyFallbackStatus()
	old := time.Now().Add(-2 * time.Minute)
	state.LastMetricCheckedAt = &old
	svc.fallbackStatus.Store(state)
	metric.value = 100
	svc.evaluateOpenAIAPIKeyFallback(t.Context())
	require.True(t, a.Schedulable)
	require.Equal(t, []int64{10}, repo.enabled)
}
func TestOpenAIAPIKeyFallbackConfiguration(t *testing.T) {
	cfg, err := GetOAuthAutoConfig(t.Context(), &accountOpsSettingsStub{raw: `{"platform":"openai"}`})
	require.NoError(t, err)
	require.Equal(t, DefaultOpenAIAPIKeyFallbackConfig(), cfg.OpenAIAPIKeyFallback)
	for _, mutate := range []func(*OpenAIAPIKeyFallbackConfig){
		func(c *OpenAIAPIKeyFallbackConfig) { c.Metric = "other" },
		func(c *OpenAIAPIKeyFallbackConfig) { c.Threshold = math.NaN() },
		func(c *OpenAIAPIKeyFallbackConfig) { c.Threshold = 101 },
		func(c *OpenAIAPIKeyFallbackConfig) { c.WindowMinutes = 0 },
		func(c *OpenAIAPIKeyFallbackConfig) { c.WindowMinutes = 1441 },
		func(c *OpenAIAPIKeyFallbackConfig) { c.AccountIDs = []int64{10, 10} },
		func(c *OpenAIAPIKeyFallbackConfig) { c.Enabled = true },
	} {
		c := DefaultOpenAIAPIKeyFallbackConfig()
		mutate(&c)
		require.Error(t, c.Validate())
	}
	c := DefaultOpenAIAPIKeyFallbackConfig()
	c.Metric = "health_score"
	c.WindowMinutes = 1440
	require.NoError(t, c.Validate())
	repo := &apiKeyFallbackAccounts{targets: []*Account{{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}}}
	svc := apiKeyFallbackFixture(t, repo, &apiKeyFallbackMetrics{})
	c.Enabled, c.AccountIDs = true, []int64{10}
	require.Error(t, svc.validateOpenAIAPIKeyFallbackAccounts(t.Context(), c))
	c.Enabled = false
	require.NoError(t, svc.validateOpenAIAPIKeyFallbackAccounts(t.Context(), c))
}

func TestOpenAIAPIKeyFallbackHealthMetricAndConfiguredWindow(t *testing.T) {
	repo := &apiKeyFallbackAccounts{targets: []*Account{{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive}}}
	metric := &apiKeyFallbackMetrics{value: 70, samples: 100}
	svc := apiKeyFallbackFixture(t, repo, metric)
	cfg, err := svc.GetOAuthAutoConfig(t.Context())
	require.NoError(t, err)
	cfg.OpenAIAPIKeyFallback.Metric, cfg.OpenAIAPIKeyFallback.Threshold, cfg.OpenAIAPIKeyFallback.WindowMinutes = "health_score", 80, 30
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	settings, ok := svc.settings.(*accountOpsSettingsStub)
	require.True(t, ok)
	settings.raw = string(raw)
	svc.evaluateOpenAIAPIKeyFallback(t.Context())
	require.Equal(t, "health_score", metric.metric)
	require.Equal(t, 30*time.Minute, metric.window)
	require.Equal(t, []int64{10}, repo.enabled)
	state := svc.OpenAIAPIKeyFallbackStatus()
	require.Equal(t, "health_score", state.Metric)
	require.Equal(t, "metric_below_threshold", state.LastTriggerReason)
	require.Equal(t, 70.0, *state.MetricValue)
}
