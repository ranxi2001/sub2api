//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

const ipUnauthorizedTestBody = `{"error":{"message":"Your IP is not authorized to make this request.","type":"invalid_request_error","code":null}}`

func TestOpenAIIPUnauthorizedClassifier(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		match  bool
	}{
		{"observed policy response", 401, ipUnauthorizedTestBody, true},
		{"wrong status", 403, ipUnauthorizedTestBody, false},
		{"expired token", 401, `{"error":{"message":"Token expired"}}`, false},
		{"plain text", 401, "Your IP is not authorized to make this request.", false},
		{"unrelated field", 401, `{"input":"Your IP is not authorized to make this request."}`, false},
		{"different policy", 401, `{"error":{"message":"Your account is not authorized to make this request."}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.match, isOpenAIIPUnauthorizedResponse(tc.status, []byte(tc.body)))
		})
	}
}

func TestOpenAIIPUnauthorizedWindow(t *testing.T) {
	start := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		delay time.Duration
		pause bool
	}{
		{"concurrent", 0, true},
		{"within window", 4 * time.Second, true},
		{"exact boundary", 5 * time.Second, true},
		{"expired", 5*time.Second + time.Nanosecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var streak openAIIPUnauthorizedStreak
			require.False(t, streak.observe(10, "first", start))
			require.Equal(t, tc.pause, streak.observe(10, "second", start.Add(tc.delay)))
			require.True(t, streak.observe(10, "third", start.Add(tc.delay+time.Second)), "an expired observation starts a new window")
		})
	}
	var streak openAIIPUnauthorizedStreak
	require.False(t, streak.observe(10, "first", start))
	require.False(t, streak.observe(10, "first", start.Add(4*time.Second)), "duplicate response must not count twice")
	require.False(t, streak.observe(10, "second", start.Add(6*time.Second)), "duplicate must not extend the observation window")
	require.False(t, streak.observe(11, "third", start.Add(7*time.Second)), "each account has a separate streak")
	require.True(t, streak.observe(10, "fourth", start.Add(7*time.Second)))
}

func TestOpenAIIPUnauthorizedConcurrentResponses(t *testing.T) {
	var streak openAIIPUnauthorizedStreak
	now := time.Now()
	start := make(chan struct{})
	results := make(chan bool, 2)
	var workers sync.WaitGroup
	for _, id := range []string{"first", "second"} {
		workers.Add(1)
		go func(id string) {
			defer workers.Done()
			<-start
			results <- streak.observe(10, id, now)
		}(id)
	}
	close(start)
	workers.Wait()
	close(results)
	pauses := 0
	for pause := range results {
		if pause {
			pauses++
		}
	}
	require.Equal(t, 1, pauses, "exactly the second distinct response reaches the threshold")
}

type ipUnauthorizedAccountRepo struct {
	rateLimitAccountRepoStub
	until time.Time
}

func (r *ipUnauthorizedAccountRepo) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.until = until
	return r.rateLimitAccountRepoStub.SetTempUnschedulable(ctx, id, until, reason)
}

func TestOpenAIIPUnauthorizedGatewayCooldown(t *testing.T) {
	ctx := context.Background()
	repo := &ipUnauthorizedAccountRepo{}
	cfg := &config.Config{RateLimit: config.RateLimitConfig{OAuth401CooldownMinutes: 1}}
	rateLimits := NewRateLimitService(repo, nil, cfg, nil, nil)
	gateway := &OpenAIGatewayService{rateLimitService: rateLimits}
	rateLimits.SetAccountRuntimeBlocker(gateway)
	account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "test-refresh"}}
	headers := http.Header{"X-Request-Id": []string{"first"}}
	body := []byte(ipUnauthorizedTestBody)

	require.False(t, gateway.handleOpenAIAccountUpstreamError(ctx, account, 401, headers, body))
	require.Zero(t, repo.tempCalls)
	require.Zero(t, repo.setErrorCalls)
	require.False(t, gateway.isOpenAIAccountRuntimeBlocked(account))
	require.False(t, gateway.handleOpenAIAccountUpstreamError(ctx, account, 401, headers, body), "duplicate handling of the first response does not park the account")
	require.Zero(t, repo.tempCalls)

	headers.Set("X-Request-Id", "second")
	require.True(t, gateway.handleOpenAIAccountUpstreamError(ctx, account, 401, headers, body))
	require.Equal(t, 1, repo.tempCalls)
	require.WithinDuration(t, time.Now().Add(time.Minute), repo.until, time.Second)
	require.Contains(t, repo.lastTempReason, "OAuth 401: Your IP is not authorized")
	require.Zero(t, repo.setErrorCalls)
	require.True(t, gateway.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIIPUnauthorizedSuccessBreaksStreak(t *testing.T) {
	repo := &ipUnauthorizedAccountRepo{}
	rateLimits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	gateway := &OpenAIGatewayService{rateLimitService: rateLimits}
	account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "test-refresh"}}
	ctx := context.Background()
	body := []byte(ipUnauthorizedTestBody)
	require.False(t, gateway.handleOpenAIAccountUpstreamError(ctx, account, 401, nil, body))
	gateway.ReportOpenAIAccountScheduleResult(account, "test-model", true, nil)
	require.False(t, gateway.handleOpenAIAccountUpstreamError(ctx, account, 401, nil, body), "the first error after success starts a fresh streak")
	require.Zero(t, repo.tempCalls)
	require.True(t, gateway.handleOpenAIAccountUpstreamError(ctx, account, 401, nil, body))
	require.Equal(t, 1, repo.tempCalls)
}

func TestOpenAIIPUnauthorizedOtherResponseBreaksStreak(t *testing.T) {
	repo := &ipUnauthorizedAccountRepo{}
	rateLimits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "test-refresh"}}
	ctx := context.Background()
	body := []byte(ipUnauthorizedTestBody)
	require.False(t, rateLimits.HandleUpstreamError(ctx, account, 401, nil, body))
	require.False(t, rateLimits.HandleUpstreamError(ctx, account, 400, nil, []byte(`{"error":{"message":"Invalid parameter"}}`)))
	require.False(t, rateLimits.HandleUpstreamError(ctx, account, 401, nil, body))
	require.Zero(t, repo.tempCalls)
}

func TestOpenAIIPUnauthorizedPermanentAuthErrorsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		refresh bool
	}{
		{"missing refresh token", ipUnauthorizedTestBody, false},
		{"permanent unauthorized", `{"detail":"Unauthorized","error":{"message":"Your IP is not authorized to make this request."}}`, true},
		{"revoked token", `{"error":{"code":"token_revoked","message":"Your IP is not authorized to make this request."}}`, true},
		{"invalidated token", `{"error":{"code":"token_invalidated","message":"Your IP is not authorized to make this request."}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &ipUnauthorizedAccountRepo{}
			rateLimits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			if tc.refresh {
				account.Credentials = map[string]any{"refresh_token": "test-refresh"}
			}
			require.True(t, rateLimits.HandleUpstreamError(context.Background(), account, 401, nil, []byte(tc.body)))
			require.Equal(t, 1, repo.setErrorCalls)
			require.Zero(t, repo.tempCalls)
		})
	}
}

func TestOpenAIIPUnauthorizedShadowSharesCredentialOwnerStreak(t *testing.T) {
	ctx := context.Background()
	parent := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "test-refresh"}}
	shadow := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parent.ID, QuotaDimension: QuotaDimensionSpark}
	repo := &ipUnauthorizedAccountRepo{}
	repo.accountsByID = map[int64]*Account{parent.ID: parent}
	rateLimits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	gateway := &OpenAIGatewayService{rateLimitService: rateLimits}
	body := []byte(ipUnauthorizedTestBody)
	require.False(t, rateLimits.HandleUpstreamError(ctx, parent, 401, nil, body))
	gateway.ReportOpenAIAccountScheduleResult(shadow, "test-model", true, nil)
	require.False(t, rateLimits.HandleUpstreamError(ctx, shadow, 401, nil, body), "shadow success resets its credential owner's streak")
	require.True(t, rateLimits.HandleUpstreamError(ctx, parent, 401, nil, body), "parent and shadow failures share one streak")
	require.Equal(t, 1, repo.tempCalls)
	require.Equal(t, parent.ID, repo.lastTempID)
	require.Zero(t, repo.setErrorCalls)
}

func TestOpenAIIPUnauthorizedGatewayEarlyReturnsBreakStreak(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"ordinary_400_control", 400, "{\"error\":{\"message\":\"Invalid parameter\"}}"},
		{"context_window_400", 400, "{\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"maximum context length exceeded\"}}"},
		{"capacity_shed_503", 503, "{\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"The server is overloaded\"}}"},
		{"wrapped_concurrency_401", 401, `{"error":{"type":"authentication_error","message":"authentication failed (status=503): Too many concurrent requests"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &ipUnauthorizedAccountRepo{}
			rateLimits := NewRateLimitService(repo, nil, &config.Config{RateLimit: config.RateLimitConfig{OAuth401CooldownMinutes: 1}}, nil, nil)
			gateway := &OpenAIGatewayService{rateLimitService: rateLimits}
			rateLimits.SetAccountRuntimeBlocker(gateway)
			account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "test-refresh"}}
			ctx := context.Background()
			firstHeaders := http.Header{"X-Request-Id": []string{"first-ip-401"}}
			secondHeaders := http.Header{"X-Request-Id": []string{"second-ip-401"}}
			require.False(t, gateway.handleOpenAIAccountUpstreamError(ctx, account, 401, firstHeaders, []byte(ipUnauthorizedTestBody)))
			require.False(t, gateway.handleOpenAIAccountUpstreamError(ctx, account, tc.status, nil, []byte(tc.body)))
			gateway.ReportOpenAIAccountScheduleResult(account, "test-model", false, nil, errors.New("intervening upstream HTTP error"))
			require.False(t, gateway.handleOpenAIAccountUpstreamError(ctx, account, 401, secondHeaders, []byte(ipUnauthorizedTestBody)), "an intervening upstream HTTP error must break the consecutive IP-401 streak")
			require.Zero(t, repo.tempCalls)
			require.False(t, gateway.isOpenAIAccountRuntimeBlocked(account))
		})
	}
}

func TestOpenAIIPUnauthorizedGatewayRevokedTokenStillDisables(t *testing.T) {
	repo := &ipUnauthorizedAccountRepo{}
	rateLimits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	gateway := &OpenAIGatewayService{rateLimitService: rateLimits}
	rateLimits.SetAccountRuntimeBlocker(gateway)
	account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "fixture-refresh"}}
	body := []byte(`{"error":{"code":"token_revoked","message":"Token revoked"}}`)
	require.True(t, gateway.handleOpenAIAccountUpstreamError(context.Background(), account, 401, nil, body))
	require.Equal(t, 1, repo.setErrorCalls)
	require.True(t, gateway.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIIPUnauthorizedOtherAccountTypesUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name, platform, accountType string
		temp, permanent             int
	}{
		{"OpenAI API key", PlatformOpenAI, AccountTypeAPIKey, 0, 1},
		{"Anthropic OAuth", PlatformAnthropic, AccountTypeOAuth, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &ipUnauthorizedAccountRepo{}
			rateLimits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			account := &Account{ID: 10, Platform: tc.platform, Type: tc.accountType, Credentials: map[string]any{"refresh_token": "test-refresh"}}
			require.True(t, rateLimits.HandleUpstreamError(context.Background(), account, 401, nil, []byte(ipUnauthorizedTestBody)))
			require.Equal(t, tc.temp, repo.tempCalls)
			require.Equal(t, tc.permanent, repo.setErrorCalls)
		})
	}
}
