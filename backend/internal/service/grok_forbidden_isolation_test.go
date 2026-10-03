//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSkipGrokForbiddenPausePreservesLegacyAndNonGrok(t *testing.T) {
	legacy := &Account{Platform: PlatformGrok, Extra: map[string]any{"grok_skip_forbidden_pause": "true"}}
	missing := &Account{Platform: PlatformGrok}
	enabled := &Account{Platform: PlatformGrok, Extra: map[string]any{"grok_skip_forbidden_pause": true}}
	other := &Account{Platform: PlatformOpenAI, Extra: map[string]any{"grok_skip_forbidden_pause": true}}

	require.False(t, legacy.SkipGrokForbiddenPause())
	require.False(t, missing.SkipGrokForbiddenPause())
	require.True(t, enabled.SkipGrokForbiddenPause())
	require.False(t, other.SkipGrokForbiddenPause())
}

func TestIsGrokUnknownForbiddenExcludesRecognizedStates(t *testing.T) {
	require.False(t, isGrokUnknownForbidden(http.StatusTooManyRequests, []byte(`{"error":"access denied"}`)))
	require.False(t, isGrokUnknownForbidden(http.StatusForbidden, []byte(`{"code":"content_policy","error":"blocked"}`)))
	require.False(t, isGrokUnknownForbidden(http.StatusForbidden, []byte(`{"code":"subscription:free-usage-exhausted","error":"quota"}`)))
	require.False(t, isGrokUnknownForbidden(http.StatusForbidden, []byte(`{"code":"entitlement_required","error":"plan required"}`)))
	require.False(t, isGrokUnknownForbidden(http.StatusForbidden, []byte(`{"error":"spending limit reached"}`)))
	require.False(t, isGrokUnknownForbidden(http.StatusForbidden, []byte(`{"error":{"code":"deactivated_account","message":"denied"}}`)))
	require.True(t, isGrokUnknownForbidden(http.StatusForbidden, []byte(`{"code":"permission-denied","error":"Access to the chat endpoint is denied"}`)))
	require.True(t, isGrokUnknownForbidden(http.StatusForbidden, nil))
}

func TestHandleGrokUnknownForbiddenOptOutSkipsAccountMutation(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{
		ID:       880100,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"grok_skip_forbidden_pause": true},
	}
	body := []byte(`{"code":"permission-denied","error":"Access to the chat endpoint is denied"}`)

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusForbidden, nil, body)

	require.Zero(t, repo.tempUnschedCalls)
	require.Zero(t, repo.rateLimitedCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestHandleGrokUnknownForbiddenLegacyStillCools(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 880101, Platform: PlatformGrok, Type: AccountTypeOAuth}
	before := time.Now()

	svc.handleGrokAccountUpstreamError(
		context.Background(), account, http.StatusForbidden, nil,
		[]byte(`{"code":"permission-denied","error":"Access to the chat endpoint is denied"}`),
	)

	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Equal(t, "grok access or entitlement denied", repo.lastTempUnschedReason)
	require.WithinDuration(t, before.Add(30*time.Minute), repo.lastTempUnschedUntil, time.Second)
}

func TestHandleGrokForbiddenOptOutKeepsEntitlementAndAdminRule(t *testing.T) {
	t.Run("entitlement", func(t *testing.T) {
		repo := &grokQuotaAccountRepo{}
		svc := &OpenAIGatewayService{accountRepo: repo}
		account := &Account{
			ID:       880102,
			Platform: PlatformGrok,
			Type:     AccountTypeOAuth,
			Extra:    map[string]any{"grok_skip_forbidden_pause": true},
		}

		svc.handleGrokAccountUpstreamError(
			context.Background(), account, http.StatusForbidden, nil,
			[]byte(`{"code":"entitlement_required","error":"subscription required"}`),
		)

		require.Equal(t, 1, repo.tempUnschedCalls)
		require.Equal(t, "grok access or entitlement denied", repo.lastTempUnschedReason)
	})

	t.Run("deactivated account", func(t *testing.T) {
		repo := &grokQuotaAccountRepo{}
		svc := &OpenAIGatewayService{accountRepo: repo}
		account := &Account{ID: 880112, Platform: PlatformGrok, Type: AccountTypeOAuth, Extra: map[string]any{"grok_skip_forbidden_pause": true}}
		svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusForbidden, nil, []byte(`{"error":{"code":"deactivated_account","message":"denied"}}`))
		require.Equal(t, 1, repo.tempUnschedCalls)
		require.Equal(t, "grok access or entitlement denied", repo.lastTempUnschedReason)
	})

	t.Run("admin rule", func(t *testing.T) {
		repo := &grokQuotaAccountRepo{}
		svc := &OpenAIGatewayService{accountRepo: repo}
		account := &Account{
			ID:       880103,
			Platform: PlatformGrok,
			Type:     AccountTypeOAuth,
			Extra:    map[string]any{"grok_skip_forbidden_pause": true},
			Credentials: map[string]any{
				"temp_unschedulable_enabled": true,
				"temp_unschedulable_rules": []any{map[string]any{
					"error_code":       float64(http.StatusForbidden),
					"keywords":         []any{"chat endpoint"},
					"duration_minutes": float64(7),
				}},
			},
		}
		before := time.Now()

		svc.handleGrokAccountUpstreamError(
			context.Background(), account, http.StatusForbidden, nil,
			[]byte(`{"code":"permission-denied","error":"Access to the chat endpoint is denied"}`),
		)

		require.Equal(t, 1, repo.tempUnschedCalls)
		require.Equal(t, "grok configured forbidden rule", repo.lastTempUnschedReason)
		require.WithinDuration(t, before.Add(7*time.Minute), repo.lastTempUnschedUntil, time.Second)
	})
}

func TestHandleGrokModelFreeUsageDoesNotRateLimitAccountAndHonorsShortReset(t *testing.T) {
	accountID := int64(880104)
	model := "grok-4.5"
	t.Cleanup(func() { clearGrokModelQuotaBlocksForAccount(accountID) })
	resetAt := time.Now().Add(10 * time.Minute).Truncate(time.Second)
	headers := http.Header{
		"Retry-After":                  []string{"600"},
		"X-Ratelimit-Remaining-Tokens": []string{"0"},
		"X-Ratelimit-Reset-Tokens":     []string{resetAt.Format(time.RFC3339)},
	}
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: accountID, Platform: PlatformGrok, Type: AccountTypeOAuth}
	body := []byte(`{"code":"subscription:free-usage-exhausted","error":"used all free usage for model grok-4.5"}`)

	svc.handleGrokAccountUpstreamError(
		withGrokTeamRateLimitModel(context.Background(), model),
		account, http.StatusForbidden, headers, body,
	)

	require.Zero(t, repo.rateLimitedCalls)
	require.Zero(t, repo.tempUnschedCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.True(t, isGrokModelQuotaBlocked(accountID, model, time.Now().Add(9*time.Minute)))
	require.False(t, isGrokModelQuotaBlocked(accountID, model, resetAt.Add(time.Second)))
	require.False(t, isGrokModelQuotaBlocked(accountID, "grok-4", time.Now()))
}

func TestClearRateLimitClearsOnlyOwnedGrokProcessBlocks(t *testing.T) {
	accountID := int64(880105)
	otherID := int64(880106)
	t.Cleanup(func() {
		clearGrokModelQuotaBlocksForAccount(accountID)
		clearGrokModelQuotaBlocksForAccount(otherID)
		clearGrokTeamModelRateLimitsForAccount(&Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"team_id": "team-a"}})
		clearGrokTeamModelRateLimitsForAccount(&Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"team_id": "team-b"}})
	})
	account := &Account{ID: accountID, Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"team_id": "team-a"}}
	other := &Account{ID: otherID, Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"team_id": "team-b"}}
	markGrokModelQuotaBlock(accountID, "grok-heavy", time.Now().Add(time.Hour))
	markGrokModelQuotaBlock(otherID, "grok-heavy", time.Now().Add(time.Hour))
	markGrokTeamModelRateLimit(account, "grok-heavy", time.Now().Add(30*time.Minute))
	markGrokTeamModelRateLimit(other, "grok-heavy", time.Now().Add(30*time.Minute))

	repo := &rateLimitClearRepoStub{getByIDAccount: account}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	require.NoError(t, svc.ClearRateLimit(context.Background(), accountID))

	require.False(t, isGrokModelQuotaBlocked(accountID, "grok-heavy", time.Now()))
	require.True(t, isGrokModelQuotaBlocked(otherID, "grok-heavy", time.Now()))
	require.False(t, isGrokTeamModelRateLimited(account, "grok-heavy", time.Now()))
	require.True(t, isGrokTeamModelRateLimited(other, "grok-heavy", time.Now()))
}

func TestGrokRefreshSuccessPreservesUnrelatedForbiddenCooldown(t *testing.T) {
	until := time.Now().Add(20 * time.Minute)
	account := &Account{
		ID:                      880107,
		Platform:                PlatformGrok,
		Type:                    AccountTypeOAuth,
		TempUnschedulableUntil:  &until,
		TempUnschedulableReason: "grok access or entitlement denied",
	}
	repo := &tokenRefreshAccountRepo{}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, nil, &config.Config{}, nil)

	svc.postRefreshActions(context.Background(), account)

	require.Zero(t, repo.clearTempCalls)
}

func TestGrokRefreshSuccessClearsCredentialUnauthorizedCooldown(t *testing.T) {
	until := time.Now().Add(10 * time.Minute)
	account := &Account{
		ID:                      880108,
		Platform:                PlatformGrok,
		Type:                    AccountTypeOAuth,
		TempUnschedulableUntil:  &until,
		TempUnschedulableReason: "grok credentials unauthorized",
	}
	repo := &tokenRefreshAccountRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, nil, &config.Config{}, nil)

	svc.postRefreshActions(context.Background(), account)

	require.Equal(t, 1, repo.clearTempCalls)
}

func TestGrokRefreshDoesNotClearArbitrary401ReasonOrConcurrentForbidden(t *testing.T) {
	until := time.Now().Add(10 * time.Minute)
	stale := &Account{
		ID:                      880109,
		Platform:                PlatformGrok,
		Type:                    AccountTypeOAuth,
		TempUnschedulableUntil:  &until,
		TempUnschedulableReason: "quota token401 remaining",
	}
	repo := &tokenRefreshAccountRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{stale.ID: stale}}}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, nil, &config.Config{}, nil)
	svc.postRefreshActions(context.Background(), stale)
	require.Zero(t, repo.clearTempCalls)

	latest := &Account{
		ID:                      stale.ID,
		Platform:                PlatformGrok,
		Type:                    AccountTypeOAuth,
		TempUnschedulableUntil:  &until,
		TempUnschedulableReason: "grok access or entitlement denied",
	}
	repo.accountsByID[stale.ID] = latest
	stale.TempUnschedulableReason = "grok credentials unauthorized"
	svc.postRefreshActions(context.Background(), stale)
	require.Zero(t, repo.clearTempCalls)
}

func TestSuccessfulTestDoesNotClearHiddenGrokProcessBlock(t *testing.T) {
	accountID := int64(880110)
	t.Cleanup(func() { clearGrokModelQuotaBlocksForAccount(accountID) })
	account := &Account{ID: accountID, Platform: PlatformGrok, Type: AccountTypeOAuth, Status: StatusActive}
	markGrokModelQuotaBlock(accountID, "grok-heavy", time.Now().Add(time.Hour))
	repo := &rateLimitClearRepoStub{getByIDAccount: account}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

	result, err := svc.RecoverAccountAfterSuccessfulTest(context.Background(), accountID)

	require.NoError(t, err)
	require.False(t, result.ClearedRateLimit)
	require.Zero(t, repo.clearRateLimitCalls)
	require.True(t, isGrokModelQuotaBlocked(accountID, "grok-heavy", time.Now()))
}

func TestSuccessfulTestWithAccountCooldownPreservesOtherModelAndTeamBlock(t *testing.T) {
	accountID := int64(880117)
	until := time.Now().Add(time.Hour)
	account := &Account{ID: accountID, Platform: PlatformGrok, Type: AccountTypeOAuth, Status: StatusActive,
		TempUnschedulableUntil: &until, Credentials: map[string]any{"team_id": "synthetic-team-success"}}
	t.Cleanup(func() {
		clearGrokModelQuotaBlocksForAccount(accountID)
		clearGrokTeamModelRateLimitsForAccount(account)
	})
	markGrokModelQuotaBlock(accountID, "grok-heavy", until)
	markGrokTeamModelRateLimit(account, "grok-heavy", until)
	repo := &rateLimitClearRepoStub{getByIDAccount: account}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	result, err := svc.RecoverAccountAfterSuccessfulTest(context.Background(), accountID)
	require.NoError(t, err)
	require.True(t, result.ClearedRateLimit)
	require.Zero(t, repo.clearModelRateLimitCalls)
	require.True(t, isGrokModelQuotaBlocked(accountID, "grok-heavy", time.Now()))
	require.True(t, isGrokTeamModelRateLimited(account, "grok-heavy", time.Now()))
}

func TestModelQuotaSnapshotDoesNotInstallAccountSchedulingThreshold(t *testing.T) {
	accountID := int64(880111)
	model := "grok-4.5"
	t.Cleanup(func() { clearGrokModelQuotaBlocksForAccount(accountID) })
	resetAt := time.Now().Add(10 * time.Minute).Truncate(time.Second)
	headers := http.Header{
		"Retry-After":                  []string{"600"},
		"X-Ratelimit-Limit-Tokens":     []string{"100"},
		"X-Ratelimit-Remaining-Tokens": []string{"0"},
		"X-Ratelimit-Reset-Tokens":     []string{resetAt.Format(time.RFC3339)},
	}
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: accountID, Platform: PlatformGrok, Type: AccountTypeOAuth}
	body := []byte(`{"code":"subscription:free-usage-exhausted","error":"used all free usage for model grok-4.5"}`)

	svc.handleGrokAccountUpstreamError(withGrokTeamRateLimitModel(context.Background(), model), account, http.StatusForbidden, headers, body)

	updates := repo.updates[accountID]
	require.NotContains(t, updates, "grok_sched_utilization")
	require.NotContains(t, updates, "grok_sched_reset_at")
	require.Contains(t, updates, grokQuotaSnapshotExtraKey)
	account.Extra = updates
	decision := EvaluateAccountSchedulingThreshold(account, map[string]int{PlatformGrok: 50}, time.Now())
	require.False(t, decision.ShouldPause)
}

func TestObserveGrokTestUnknownForbiddenRespectsOptOutAndModelHeaders(t *testing.T) {
	accountID := int64(880301)
	model := "grok-4.5"
	t.Cleanup(func() { clearGrokModelQuotaBlocksForAccount(accountID) })
	resetAt := time.Now().Add(8 * time.Minute).Truncate(time.Second)
	headers := http.Header{
		"Retry-After":                  []string{"480"},
		"X-Ratelimit-Remaining-Tokens": []string{"0"},
		"X-Ratelimit-Reset-Tokens":     []string{resetAt.Format(time.RFC3339)},
	}
	repo := &grokQuotaAccountRepo{}
	svc := &AccountTestService{accountRepo: repo}
	account := &Account{ID: accountID, Platform: PlatformGrok, Type: AccountTypeOAuth, Extra: map[string]any{"grok_skip_forbidden_pause": true}}
	body := `{"code":"permission-denied","error":"Access to the chat endpoint is denied"}`
	resp := &http.Response{StatusCode: http.StatusForbidden, Header: headers, Body: io.NopCloser(strings.NewReader(body))}

	svc.observeGrokTestResponse(withGrokTeamRateLimitModel(context.Background(), model), account, resp)

	require.Zero(t, repo.tempUnschedCalls)
	require.Zero(t, repo.rateLimitedCalls)
	require.NotContains(t, repo.updates[accountID], "grok_sched_utilization")
	restored, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(restored), "chat endpoint")

	modelBody := `{"code":"subscription:free-usage-exhausted","error":"used all free usage for model grok-4.5"}`
	modelResp := &http.Response{StatusCode: http.StatusForbidden, Header: headers.Clone(), Body: io.NopCloser(strings.NewReader(modelBody))}
	svc.observeGrokTestResponse(withGrokTeamRateLimitModel(context.Background(), model), account, modelResp)
	require.Zero(t, repo.rateLimitedCalls)
	require.True(t, isGrokModelQuotaBlocked(accountID, model, time.Now().Add(7*time.Minute)))
	require.False(t, isGrokModelQuotaBlocked(accountID, model, resetAt.Add(time.Second)))
}
