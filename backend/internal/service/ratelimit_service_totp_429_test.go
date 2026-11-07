//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type totp429Recorder struct {
	accounts []int64
	err      error
	ctxErr   error
}

func (r *totp429Recorder) CreateTOTPRotationOn429(ctx context.Context, id int64) (*OpenAITOTPRotation, error) {
	r.accounts = append(r.accounts, id)
	r.ctxErr = ctx.Err()
	return &OpenAITOTPRotation{AccountID: id, TaskID: 7}, r.err
}

func totp429Account() *Account {
	return &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{OpenAITOTPRotateOn429ExtraKey: true}}
}

func TestRateLimitTOTPRotationOn429ScopeAndObservation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		mutate func(*Account)
	}{
		{"enabled", 429, func(*Account) {}},
		{"other error", 503, func(*Account) {}},
		{"not opted in", 429, func(a *Account) { a.Extra = nil }},
		{"string opt-in rejected", 429, func(a *Account) { a.Extra[OpenAITOTPRotateOn429ExtraKey] = "true" }},
		{"another platform", 429, func(a *Account) { a.Platform = PlatformAnthropic }},
		{"API key", 429, func(a *Account) { a.Type = AccountTypeAPIKey }},
		{"shadow", 429, func(a *Account) { id := int64(1); a.ParentAccountID = &id }},
		{"independent connection", 429, func(a *Account) { a.AuthConnectionID = "connection" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &totp429Recorder{}
			svc := &RateLimitService{openAITOTP429Rotator: recorder}
			account := totp429Account()
			tc.mutate(account)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			ctx = svc.maybeRotateOpenAITOTPOn429(ctx, account, tc.status)
			svc.maybeRotateOpenAITOTPOn429(ctx, account, tc.status)
			if tc.name == "enabled" {
				require.Equal(t, []int64{42}, recorder.accounts, "one creation per error observation")
				require.NoError(t, recorder.ctxErr, "a canceled request must not cancel task creation")
			} else {
				require.Empty(t, recorder.accounts)
			}
		})
	}
}

func TestRateLimitTOTPRotationOn429PreservesCooldownOnFailure(t *testing.T) {
	repo, recorder := &rateLimitAccountRepoStub{}, &totp429Recorder{err: errors.New("worker unavailable")}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.openAITOTP429Rotator = recorder
	disabled := svc.HandleUpstreamError(t.Context(), totp429Account(), 429, http.Header{}, nil)
	require.False(t, disabled)
	require.Equal(t, []int64{42}, recorder.accounts)
	require.Equal(t, 1, repo.rateLimitedCalls, "rotation failure must not skip ordinary 429 backoff")
}

func TestOpenAIGatewayTOTPRotationOn429AndBPS(t *testing.T) {
	for _, path := range []string{"native", "image", "bps"} {
		t.Run(path, func(t *testing.T) {
			recorder := &totp429Recorder{}
			rates := NewRateLimitService(&rateLimitAccountRepoStub{}, nil, &config.Config{}, nil, nil)
			rates.openAITOTP429Rotator = recorder
			gateway := &OpenAIGatewayService{rateLimitService: rates}
			account := totp429Account()
			switch path {
			case "bps":
				gateway.coolDownExcelBPS(t.Context(), account, "5")
			case "image":
				gateway.handleOpenAIAccountUpstreamError(t.Context(), account, 429, http.Header{}, []byte(`{"error":{"message":"Rate limit reached for gpt-image-2-codex (for limit gpt-image)"}}`))
			default:
				gateway.handleOpenAIAccountUpstreamError(t.Context(), account, 429, http.Header{}, nil)
			}
			require.Equal(t, []int64{42}, recorder.accounts, "gateway and shared handlers must not enqueue twice")
		})
	}
}
