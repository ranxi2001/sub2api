//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type scheduling429Repo struct {
	rateLimitAccountRepoStub
	ids        []int64
	values     []bool
	err        error
	ctxErr     error
	owner      *Account
	getErr     error
	reads      []int64
	readCtxErr error
}

func (r *scheduling429Repo) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.reads = append(r.reads, id)
	r.readCtxErr = ctx.Err()
	return r.owner, r.getErr
}

func (r *scheduling429Repo) SetSchedulable(ctx context.Context, id int64, enabled bool) error {
	r.ids = append(r.ids, id)
	r.values = append(r.values, enabled)
	r.ctxErr = ctx.Err()
	return r.err
}

func scheduling429Account() *Account {
	return &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Schedulable: true,
		Extra: map[string]any{OpenAIDisableSchedulingOn429ExtraKey: true}}
}

func TestRateLimitDisableSchedulingOn429ScopeAndObservation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		mutate func(*Account)
		want   bool
	}{
		{"enabled without saved credentials", 429, func(*Account) {}, true},
		{"managed connection keeps owner identity", 429, func(a *Account) { a.AuthConnectionID = "connection"; a.Extra["openai_connections_managed"] = true }, true},
		{"unverified connection marker", 429, func(a *Account) { a.AuthConnectionID = "connection" }, false},
		{"SIWC", 429, func(a *Account) { a.Extra[OpenAISIWCEnabledKey] = true }, true},
		{"PAT", 429, func(a *Account) { a.Credentials = map[string]any{"auth_mode": "personal_access_token"} }, true},
		{"agent identity", 429, func(a *Account) { a.Credentials = map[string]any{"auth_mode": OpenAIAuthModeAgentIdentity} }, true},
		{"another error", 503, func(*Account) {}, false},
		{"not opted in", 429, func(a *Account) { a.Extra = nil }, false},
		{"explicit false", 429, func(a *Account) { a.Extra[OpenAIDisableSchedulingOn429ExtraKey] = false }, false},
		{"string opt-in rejected", 429, func(a *Account) { a.Extra[OpenAIDisableSchedulingOn429ExtraKey] = "true" }, false},
		{"another platform", 429, func(a *Account) { a.Platform = PlatformAnthropic }, false},
		{"API key", 429, func(a *Account) { a.Type = AccountTypeAPIKey }, false},
		{"Spark shadow", 429, func(a *Account) { id := int64(99); a.ParentAccountID = &id }, false},
		{"unsaved account", 429, func(a *Account) { a.ID = 0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, blocker := &scheduling429Repo{owner: scheduling429Account()}, &runtimeBlockRecorder{}
			svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			svc.SetAccountRuntimeBlocker(blocker)
			account := scheduling429Account()
			tc.mutate(account)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			ctx, disabled := svc.maybeDisableOpenAISchedulingOn429(ctx, account, tc.status)
			_, repeated := svc.maybeDisableOpenAISchedulingOn429(ctx, account, tc.status)
			require.Equal(t, tc.want, disabled)
			require.Equal(t, disabled, repeated)
			if tc.want {
				require.Equal(t, []int64{42}, repo.ids, "one write for this error; never mutate another owner")
				require.Equal(t, []bool{false}, repo.values)
				require.NoError(t, repo.ctxErr, "request cancellation must not cancel the scheduling change")
				require.Equal(t, []string{"openai_429_scheduling_disabled"}, blocker.reasons)
				require.True(t, account.Schedulable, "do not mutate a shared request snapshot")
			} else {
				require.Empty(t, repo.ids)
				require.Empty(t, blocker.accounts)
			}
		})
	}
}

func TestRateLimitDisableSchedulingOn429PreservesOtherPolicies(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved", true: "write failed"}[fail], func(t *testing.T) {
			repo, rotation := &scheduling429Repo{owner: scheduling429Account()}, &totp429Recorder{}
			if fail {
				repo.err = errors.New("database unavailable")
			}
			rates := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			rates.openAITOTP429Rotator = rotation
			account := scheduling429Account()
			account.Extra[OpenAITOTPRotateOn429ExtraKey] = true
			disabled := rates.HandleUpstreamError(t.Context(), account, 429, http.Header{}, nil)
			require.Equal(t, !fail, disabled)
			require.Equal(t, []int64{42}, repo.ids)
			require.Equal(t, []int64{42}, rotation.accounts, "automatic rotation remains independent")
			require.Equal(t, 1, repo.rateLimitedCalls, "scheduling persistence must not skip ordinary 429 cooldown")
		})
	}
}

func TestOpenAIGatewayDisableSchedulingOn429Paths(t *testing.T) {
	for _, path := range []string{"native", "image", "bps", "managed", "managed-bps", "siwc", "websocket-error"} {
		t.Run(path, func(t *testing.T) {
			repo, rotation := &scheduling429Repo{owner: scheduling429Account()}, &totp429Recorder{}
			rates := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			rates.openAITOTP429Rotator = rotation
			gateway := &OpenAIGatewayService{rateLimitService: rates}
			rates.SetAccountRuntimeBlocker(gateway)
			account := scheduling429Account()
			if path == "managed" || path == "managed-bps" {
				account.AuthConnectionID = "connection"
				account.Extra["openai_connections_managed"] = true
			}
			if path == "siwc" {
				account.Extra[OpenAISIWCEnabledKey] = true
			}
			switch path {
			case "bps", "managed-bps":
				gateway.coolDownExcelBPS(t.Context(), account, "5")
			case "image":
				disabled := gateway.handleOpenAIAccountUpstreamError(t.Context(), account, 429, http.Header{}, []byte(`{"error":{"message":"Rate limit reached for gpt-image-2-codex (for limit gpt-image)"}}`))
				require.True(t, disabled)
			case "websocket-error":
				require.True(t, gateway.handleOpenAIWSFailureAccountSideEffects(t.Context(), account, "gpt-5.5", http.Header{}, []byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit reached"}}`)))
			default:
				disabled := gateway.handleOpenAIAccountUpstreamError(t.Context(), account, 429, http.Header{}, nil)
				require.True(t, disabled)
			}
			require.Equal(t, []int64{42}, repo.ids, "gateway and common paths must share one durable scheduling change")
			require.Equal(t, []bool{false}, repo.values)
			require.Empty(t, rotation.accounts, "scheduling policy needs no 2FA opt-in or credentials")
			require.True(t, gateway.isOpenAIAccountRuntimeBlocked(account))
			require.False(t, gateway.shouldRetryOpenAIOAuth429OnSameAccount(account, 429, false), "the same-account retry window must not reopen a disabled account")
		})
	}
}

func TestRateLimitDisableSchedulingOn429UsesAuthoritativeManagedOwner(t *testing.T) {
	for _, tc := range []struct {
		name           string
		owner          *Account
		readErr        error
		writeErr       error
		projectedType  string
		projectedOptIn bool
		want           bool
	}{
		{name: "historical API Key connection under OAuth owner", owner: scheduling429Account(), projectedType: AccountTypeAPIKey, want: true},
		{name: "OAuth projection with stale opt out", owner: scheduling429Account(), want: true},
		{name: "API Key owner cannot opt in through projection", owner: &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{OpenAIDisableSchedulingOn429ExtraKey: true}}, projectedOptIn: true},
		{name: "OAuth owner opted out since selection", owner: &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, projectedOptIn: true},
		{name: "owner lookup failure", readErr: errors.New("fixture unavailable"), projectedOptIn: true},
		{name: "owner missing", projectedOptIn: true},
		{name: "owner identity mismatch", owner: &Account{ID: 99, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{OpenAIDisableSchedulingOn429ExtraKey: true}}, projectedOptIn: true},
		{name: "scheduling persistence failure", owner: scheduling429Account(), writeErr: errors.New("fixture write failure"), projectedOptIn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &scheduling429Repo{owner: tc.owner, getErr: tc.readErr, err: tc.writeErr}
			blocker := &runtimeBlockRecorder{}
			svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			svc.SetAccountRuntimeBlocker(blocker)
			projection := scheduling429Account()
			projection.AuthConnectionID = "synthetic-connection"
			projection.Extra["openai_connections_managed"] = true
			projection.Extra[OpenAIDisableSchedulingOn429ExtraKey] = tc.projectedOptIn
			if tc.projectedType != "" {
				projection.Type = tc.projectedType
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			ctx, disabled := svc.maybeDisableOpenAISchedulingOn429(ctx, projection, 429)
			_, repeated := svc.maybeDisableOpenAISchedulingOn429(ctx, projection, 429)
			require.Equal(t, tc.want, disabled)
			require.Equal(t, disabled, repeated)
			require.Equal(t, []int64{42}, repo.reads, "read the mother account once; never infer policy from the projection")
			require.NoError(t, repo.readCtxErr, "cancelled upstream requests still allow the bounded owner lookup")
			if tc.want || tc.writeErr != nil {
				require.Equal(t, []int64{42}, repo.ids)
				require.Equal(t, []bool{false}, repo.values)
				require.NoError(t, repo.ctxErr)
			} else {
				require.Empty(t, repo.ids)
			}
			if tc.want {
				require.Equal(t, []string{"openai_429_scheduling_disabled"}, blocker.reasons)
			} else {
				require.Empty(t, blocker.accounts)
			}
			require.True(t, projection.Schedulable, "do not mutate shared request snapshots")
		})
	}
}

type scheduling429RecoveryRepo struct {
	rateLimitClearRepoStub
	writes []bool
}

func (r *scheduling429RecoveryRepo) SetSchedulable(_ context.Context, _ int64, enabled bool) error {
	r.writes = append(r.writes, enabled)
	return nil
}

func TestRateLimitDisableSchedulingOn429SurvivesSuccessfulTestRecovery(t *testing.T) {
	account := scheduling429Account()
	account.Schedulable = false
	resetAt := time.Now().Add(-time.Minute)
	account.RateLimitResetAt = &resetAt
	repo := &scheduling429RecoveryRepo{rateLimitClearRepoStub: rateLimitClearRepoStub{getByIDAccount: account}}
	rates := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	result, err := rates.RecoverAccountAfterSuccessfulTest(t.Context(), account.ID)
	require.NoError(t, err)
	require.True(t, result.ClearedRateLimit)
	require.Empty(t, repo.writes, "recovering quota state must not re-enable the persistent scheduling switch")
	require.False(t, account.Schedulable)
}
