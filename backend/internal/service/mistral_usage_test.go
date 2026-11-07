//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

const (
	mistralTestBudgetBody = `{
		"vibe_budget": {
			"usage_percentage": 61.05918191733333,
			"initial_budget": 300.0,
			"currency": "USD",
			"reset_at": "2026-12-01T00:00:00Z",
			"payg_enabled": false
		},
		"api_budget": {
			"usage_percentage": 100.0,
			"initial_budget": 30.0,
			"currency": "USD",
			"reset_at": "2026-12-01T00:00:00Z",
			"payg_enabled": false
		},
		"usage_percentage": 100.0,
		"initial_budget": 30.0,
		"currency": "USD",
		"reset_at": "2026-12-01T00:00:00Z"
	}`
	mistralTestExhaustedVibeBudgetBody = `{
		"vibe_budget": {
			"usage_percentage": 100.0,
			"initial_budget": 300.0,
			"currency": "USD",
			"reset_at": "2026-12-01T00:00:00Z",
			"payg_enabled": false
		},
		"api_budget": {
			"usage_percentage": 0.0,
			"initial_budget": 30.0,
			"currency": "USD",
			"reset_at": "2026-12-01T00:00:00Z",
			"payg_enabled": false
		}
	}`
	mistralTestSubscriptionBody = `{
		"plan_name": "INDIVIDUAL",
		"payg_enabled": false,
		"vibe_payg_enabled": false,
		"enterprise": false
	}`
)

func mistralUsageTestAccount(id int64, apiKey string) *Account {
	if apiKey == "" {
		apiKey = "mstrl_test_key_0123456789abcdef"
	}
	return &Account{
		ID:          id,
		Name:        "mistral-test",
		Platform:    PlatformMistral,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":       apiKey,
			"base_url":      DefaultMistralBaseURL,
			"account_mode":  AccountModePayG,
			"api_protocol":  APIProtocolAdaptive,
			"api_base_urls": map[string]any{APIProtocolChatCompletions: DefaultMistralBaseURL},
		},
	}
}

func newMistralBillingUpstream(budgetBody, subscriptionBody string) *commandCodeAlphaUpstream {
	return &commandCodeAlphaUpstream{responses: map[string]commandCodeAlphaResponse{
		"/v1/billing/budget":       {status: http.StatusOK, body: budgetBody},
		"/v1/billing/subscription": {status: http.StatusOK, body: subscriptionBody},
	}}
}

func TestMistralQuotaProbeParsesVibeBudgetAndSubscription(t *testing.T) {
	account := mistralUsageTestAccount(88, "mstrl_vibe_key_123456")
	upstream := newMistralBillingUpstream(mistralTestBudgetBody, mistralTestSubscriptionBody)
	repo := &cnBalanceProbeRepo{account: account}
	svc := NewCNProviderQuotaService(repo, nil, upstream, &config.Config{})

	result, err := svc.QueryUsage(context.Background(), account.ID)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.True(t, result.CredentialValid)
	require.True(t, result.Persisted)
	require.Equal(t, PlatformMistral, result.Provider)
	require.Equal(t, "Pro", result.PlanLevel)
	require.Equal(t, []CNQuotaTier{
		{Window: "monthly", UsedPercent: 61.06, ResetAt: "2026-12-01T00:00:00Z"},
	}, result.Tiers)
	require.NotNil(t, result.Balance)
	require.True(t, result.Balance.Available)
	require.Equal(t, "USD", result.Balance.Currency)
	require.InDelta(t, 116.8225, result.Balance.Balance, 1e-4)

	for _, req := range upstream.requests {
		require.Equal(t, "Bearer mstrl_vibe_key_123456", req.Header.Get("Authorization"))
		require.Equal(t, "api.mistral.ai", req.URL.Host)
	}

	require.Len(t, repo.extraWrites, 1)
	extra := repo.extraWrites[0]
	require.Equal(t, 61.06, extra["mistral_monthly_used_percent"])
	require.Equal(t, "2026-12-01T00:00:00Z", extra["mistral_monthly_reset_at"])
	require.InDelta(t, 116.8225, extra["mistral_balance"], 1e-4)
	require.Equal(t, "USD", extra["mistral_balance_currency"])
	require.Equal(t, true, extra["mistral_balance_available"])
	require.Equal(t, false, extra["mistral_balance_low"])
	require.Equal(t, false, extra["mistral_payg_enabled"])
	require.InDelta(t, 61.06, extra["mistral_vibe_used_percent"], 1e-2)
	require.InDelta(t, 100.0, extra["mistral_api_used_percent"], 1e-2)
}

func TestMistralQuotaProbeWorkspaceKeySelectsAPIBudget(t *testing.T) {
	account := mistralUsageTestAccount(89, "workspace_api_key_without_vibe_prefix")
	budgetBody := `{
		"vibe_budget": null,
		"api_budget": {
			"usage_percentage": 25.0,
			"initial_budget": 50.0,
			"currency": "EUR",
			"reset_at": "2026-12-01T00:00:00Z",
			"payg_enabled": true
		}
	}`
	upstream := newMistralBillingUpstream(budgetBody, `{"plan_name": "SCALE", "payg_enabled": true}`)
	repo := &cnBalanceProbeRepo{account: account}
	svc := NewCNProviderQuotaService(repo, nil, upstream, &config.Config{})

	result, err := svc.QueryUsage(context.Background(), account.ID)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "Scale", result.PlanLevel)
	require.Equal(t, []CNQuotaTier{
		{Window: "monthly", UsedPercent: 25.0, ResetAt: "2026-12-01T00:00:00Z"},
	}, result.Tiers)
	require.NotNil(t, result.Balance)
	require.Equal(t, "EUR", result.Balance.Currency)
	require.InDelta(t, 37.5, result.Balance.Balance, 1e-4)
	require.Equal(t, true, repo.extraWrites[0]["mistral_payg_enabled"])
}

func TestMistralQuotaProbeExhaustedVibeBudgetMarksLowBalance(t *testing.T) {
	account := mistralUsageTestAccount(90, "mstrl_exhausted_key")
	upstream := newMistralBillingUpstream(mistralTestExhaustedVibeBudgetBody, mistralTestSubscriptionBody)
	repo := &cnBalanceProbeRepo{account: account}
	svc := NewCNProviderQuotaService(repo, nil, upstream, &config.Config{})

	result, err := svc.QueryUsage(context.Background(), account.ID)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.NotNil(t, result.Balance)
	require.InDelta(t, 0.0, result.Balance.Balance, 1e-9)
	require.False(t, result.Balance.Available)
	require.Equal(t, false, repo.extraWrites[0]["mistral_balance_available"])
	require.Equal(t, true, repo.extraWrites[0]["mistral_balance_low"])
}

func TestMistralQuotaProbeSubscriptionFailureDegradesGracefully(t *testing.T) {
	account := mistralUsageTestAccount(91, "mstrl_vibe_key")
	upstream := &commandCodeAlphaUpstream{responses: map[string]commandCodeAlphaResponse{
		"/v1/billing/budget":       {status: http.StatusOK, body: mistralTestBudgetBody},
		"/v1/billing/subscription": {status: http.StatusForbidden, body: `{"message":"forbidden"}`},
	}}
	repo := &cnBalanceProbeRepo{account: account}
	svc := NewCNProviderQuotaService(repo, nil, upstream, &config.Config{})

	result, err := svc.QueryUsage(context.Background(), account.ID)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Empty(t, result.PlanLevel)
	require.Len(t, result.Tiers, 1)
	require.NotNil(t, result.Balance)
}

func TestMistralBalanceProbeFetchesBudgetOnly(t *testing.T) {
	account := mistralUsageTestAccount(92, "mstrl_vibe_key")
	upstream := newMistralBillingUpstream(mistralTestBudgetBody, mistralTestSubscriptionBody)
	repo := &cnBalanceProbeRepo{account: account}
	svc := NewCNProviderBalanceService(repo, nil, upstream, &config.Config{})

	result, err := svc.QueryBalance(context.Background(), account.ID)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.True(t, result.Persisted)
	require.InDelta(t, 116.8225, result.Balance, 1e-4)
	require.Equal(t, "USD", result.Currency)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "/v1/billing/budget", upstream.requests[0].URL.Path)
	require.Equal(t, 61.06, repo.extraWrites[0]["mistral_monthly_used_percent"])
}

func TestMistralUsageRequiresOfficialHost(t *testing.T) {
	account := mistralUsageTestAccount(93, "mstrl_vibe_key")
	require.Equal(t, PlatformMistral, account.GetCodingPlanProvider())
	require.NoError(t, validateCodingPlanAccount(account))
	require.NoError(t, validatePayGAccount(account))

	account.Credentials["base_url"] = "https://relay.example.com/v1"
	account.Credentials["api_protocol"] = APIProtocolChatCompletions
	require.Empty(t, account.GetCodingPlanProvider())
	require.Error(t, validateCodingPlanAccount(account))
	require.Error(t, validatePayGAccount(account))
}

func TestMistralWindowsIgnoredWhenPaygEnabled(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(48 * time.Hour).Format(time.RFC3339)
	account := mistralUsageTestAccount(94, "mstrl_vibe_key")
	account.Extra = map[string]any{
		"mistral_monthly_used_percent": 95.0,
		"mistral_monthly_reset_at":     future,
		"mistral_payg_enabled":         false,
	}
	require.NotEmpty(t, cnProviderThresholdCandidates(account, PlatformMistral))
	decision := EvaluateAccountSchedulingThreshold(account, map[string]int{PlatformMistral: 90}, now)
	require.True(t, decision.ShouldPause)
	require.Equal(t, "monthly", decision.Window)

	account.Extra["mistral_payg_enabled"] = true
	require.Empty(t, cnProviderThresholdCandidates(account, PlatformMistral))
	decision = EvaluateAccountSchedulingThreshold(account, map[string]int{PlatformMistral: 90}, now)
	require.False(t, decision.ShouldPause)
}

func TestCNProviderBalanceCheckRunOnceProbesMistral(t *testing.T) {
	official := *mistralUsageTestAccount(1, "mstrl_vibe_key")
	official.Schedulable = true
	manualOff := *mistralUsageTestAccount(2, "mstrl_vibe_key")
	manualOff.Schedulable = false
	relay := *mistralUsageTestAccount(3, "mstrl_vibe_key")
	relay.Credentials = map[string]any{
		"api_key":      "mstrl_vibe_key",
		"api_protocol": APIProtocolChatCompletions,
		"base_url":     "https://relay.example.com/v1",
	}
	inactive := *mistralUsageTestAccount(4, "mstrl_vibe_key")
	inactive.Status = StatusDisabled

	repo := &commandCodeCheckRepo{accounts: []Account{official, manualOff, relay, inactive}}
	prober := &commandCodeBalanceProber{balance: 0}
	cfg := &config.Config{}
	cfg.Gateway.CNProviders.BalanceThreshold = 0.5
	svc := &CNProviderBalanceCheckService{accountRepo: repo, quotaService: prober, cfg: cfg}

	svc.runOnce()

	require.ElementsMatch(t, []int64{1, 2}, prober.probed)
	require.Equal(t, []int64{1}, repo.paused)
}

func TestHandleUpstreamError_Mistral402PausesInsteadOfError(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := mistralUsageTestAccount(95, "mstrl_vibe_key")

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusPaymentRequired,
		http.Header{},
		[]byte(`{"message":"Vibe budget exhausted.","type":"budget_exhausted"}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
	require.Contains(t, repo.lastTempReason, cnBalanceLowReasonPrefix)
	require.Equal(t, true, repo.lastExtraUpdates["mistral_balance_low"])
}

func TestMistralProbeErrorAndFallbackCases(t *testing.T) {
	account := mistralUsageTestAccount(96, "mstrl_vibe_key")

	// 1. Top-level budget fallback + subscription vibe_payg_enabled fallback
	topLevelUpstream := newMistralBillingUpstream(
		`{"vibe_budget":{"usage_percentage":100,"initial_budget":300,"currency":"USD"},"usage_percentage":10,"initial_budget":50,"currency":"USD"}`,
		`{"plan_name":"TEAM","vibe_payg_enabled":true}`,
	)
	repo := &cnBalanceProbeRepo{account: account}
	res, err := NewCNProviderQuotaService(repo, nil, topLevelUpstream, &config.Config{}).QueryUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.True(t, res.Success)
	require.Equal(t, "Team", res.PlanLevel)
	require.True(t, res.Balance.Available)
	require.Equal(t, true, repo.extraWrites[0]["mistral_payg_enabled"])

	// 2. 401 on /v1/billing/budget marks CredentialValid=false and skips persistence
	unauthUpstream := &commandCodeAlphaUpstream{responses: map[string]commandCodeAlphaResponse{
		"/v1/billing/budget": {status: http.StatusUnauthorized, body: `{"message":"Unauthorized"}`},
	}}
	unauthRepo := &cnBalanceProbeRepo{account: account}
	res, err = NewCNProviderQuotaService(unauthRepo, nil, unauthUpstream, &config.Config{}).QueryUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.False(t, res.Success)
	require.False(t, res.CredentialValid)
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)
	require.Contains(t, res.Error, "Authentication failed (HTTP 401)")
	require.Empty(t, unauthRepo.extraWrites)

	balRes, err := NewCNProviderBalanceService(unauthRepo, nil, unauthUpstream, &config.Config{}).QueryBalance(context.Background(), account.ID)
	require.NoError(t, err)
	require.False(t, balRes.Success)
	require.Equal(t, http.StatusUnauthorized, balRes.StatusCode)

	// 3. 500 error and invalid JSON body
	err500Upstream := &commandCodeAlphaUpstream{responses: map[string]commandCodeAlphaResponse{
		"/v1/billing/budget": {status: http.StatusInternalServerError, body: `{"message":"server boom"}`},
	}}
	res, err = NewCNProviderQuotaService(&cnBalanceProbeRepo{account: account}, nil, err500Upstream, &config.Config{}).QueryUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.False(t, res.Success)
	require.True(t, res.CredentialValid)
	require.Contains(t, res.Error, "HTTP 500")

	badJSONUpstream := &commandCodeAlphaUpstream{responses: map[string]commandCodeAlphaResponse{
		"/v1/billing/budget": {status: http.StatusOK, body: `{}`},
	}}
	res, err = NewCNProviderQuotaService(&cnBalanceProbeRepo{account: account}, nil, badJSONUpstream, &config.Config{}).QueryUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.False(t, res.Success)
	require.Equal(t, "Invalid usage response", res.Error)

	// 4. URL allowlist policy rejection
	blockedCfg := cnProbeAllowlistConfig("api.openai.com")
	recUpstream := &recordingHTTPUpstream{}
	_, err = NewCNProviderQuotaService(&cnBalanceProbeRepo{account: account}, nil, recUpstream, blockedCfg).QueryUsage(context.Background(), account.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "CN_QUOTA_URL_REJECTED")
	require.Zero(t, recUpstream.calls)
}

func TestMistralBillingPermissionDeniedDoesNotInvalidateKey(t *testing.T) {
	account := mistralUsageTestAccount(97, "mstrl_permission_limited_key")
	upstream := &commandCodeAlphaUpstream{responses: map[string]commandCodeAlphaResponse{
		"/v1/billing/budget": {status: http.StatusForbidden, body: `{"message":"missing see_org_inference_usage"}`},
	}}
	repo := &cnBalanceProbeRepo{account: account}
	quota, err := NewCNProviderQuotaService(repo, nil, upstream, &config.Config{}).QueryUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.False(t, quota.Success)
	require.True(t, quota.CredentialValid, "billing permission alone does not establish an invalid inference key")
	require.Equal(t, http.StatusForbidden, quota.StatusCode)
	require.Contains(t, quota.Error, "permission denied")
	require.NotContains(t, quota.Error, "Authentication failed")
	require.False(t, quota.Persisted)

	balance, err := NewCNProviderBalanceService(repo, nil, upstream, &config.Config{}).QueryBalance(context.Background(), account.ID)
	require.NoError(t, err)
	require.False(t, balance.Success)
	require.Equal(t, http.StatusForbidden, balance.StatusCode)
	require.Contains(t, balance.Error, "permission denied")
	require.False(t, balance.Persisted)
	require.Empty(t, repo.extraWrites, "unavailable billing data must not overwrite the saved balance")
}

func TestMistralZeroAllowanceDoesNotSwitchToOtherBudget(t *testing.T) {
	account := mistralUsageTestAccount(97, "mstrl_zero_vibe")
	upstream := newMistralBillingUpstream(
		`{"vibe_budget":{"usage_percentage":0,"initial_budget":0,"currency":"USD","payg_enabled":false},"api_budget":{"usage_percentage":0,"initial_budget":50,"currency":"USD","payg_enabled":false}}`,
		mistralTestSubscriptionBody,
	)
	repo := &cnBalanceProbeRepo{account: account}
	res, err := NewCNProviderQuotaService(repo, nil, upstream, &config.Config{}).QueryUsage(context.Background(), account.ID)

	require.NoError(t, err)
	require.True(t, res.Success)
	require.Empty(t, res.Tiers)
	require.NotNil(t, res.Balance)
	require.Zero(t, res.Balance.Balance)
	require.False(t, res.Balance.Available)
	require.Equal(t, true, repo.extraWrites[0]["mistral_balance_low"])
}

func TestMistralBalanceProbePreservesSubscriptionPaygAndCheckMistralDoesNotPausePayg(t *testing.T) {
	until := time.Now().Add(time.Hour)
	account := mistralUsageTestAccount(98, "mstrl_payg_key")
	account.TempUnschedulableUntil = &until
	account.TempUnschedulableReason = cnBalanceLowReason("Vibe budget exhausted.")

	// Budget omits payg_enabled; subscription has vibe_payg_enabled=true.
	upstream := newMistralBillingUpstream(
		`{"vibe_budget":{"usage_percentage":100,"initial_budget":300,"currency":"USD"}}`,
		`{"plan_name":"TEAM","vibe_payg_enabled":true}`,
	)
	statefulRepo := &commandCodeStatefulRepo{account: account}

	// Balance probe fetches subscription fallback when budget.payg_enabled is nil and clears 402 pause.
	bal, err := NewCNProviderBalanceService(statefulRepo, nil, upstream, &config.Config{}).QueryBalance(context.Background(), account.ID)
	require.NoError(t, err)
	require.True(t, bal.Success)
	require.True(t, bal.Available)
	require.Equal(t, 1, statefulRepo.clearCalls)
	require.True(t, account.IsSchedulable())

	// Periodic checkMistral does not pause PAYG account even when included balance is 0 < threshold (0.5).
	quotaSvc := NewCNProviderQuotaService(statefulRepo, nil, upstream, &config.Config{})
	cfg := &config.Config{}
	cfg.Gateway.CNProviders.BalanceThreshold = 0.5
	checkSvc := &CNProviderBalanceCheckService{accountRepo: statefulRepo, quotaService: quotaSvc, cfg: cfg}
	outcome := checkSvc.checkMistral(context.Background(), account, 0.5)
	require.Equal(t, cnBalanceNoChange, outcome)
	require.True(t, account.IsSchedulable())
}
