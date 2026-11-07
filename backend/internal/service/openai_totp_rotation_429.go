package service

import (
	"context"
	"log/slog"
	"net/http"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const OpenAITOTPRotateOn429ExtraKey = "openai_totp_rotate_on_429"

type openAITOTP429Rotator interface {
	CreateTOTPRotationOn429(context.Context, int64) (*OpenAITOTPRotation, error)
}

func openAITOTPRotateOn429Enabled(account *Account) bool {
	if account == nil || account.ID <= 0 || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth ||
		account.IsShadow() || account.AuthConnectionID != "" || account.IsSIWCEnabled() ||
		account.IsOpenAIPersonalAccessToken() || account.IsOpenAIAgentIdentity() {
		return false
	}
	enabled, _ := account.Extra[OpenAITOTPRotateOn429ExtraKey].(bool)
	return enabled
}

type openAITOTP429ObservedKey struct{}

// Only enqueue the durable task here; the existing credential worker performs
// the upstream operation. Preserve cancellation independence of error handling.
func (s *RateLimitService) maybeRotateOpenAITOTPOn429(ctx context.Context, account *Account, status int) context.Context {
	if s == nil || s.openAITOTP429Rotator == nil || status != http.StatusTooManyRequests || !openAITOTPRotateOn429Enabled(account) {
		return ctx
	}
	if observed, _ := ctx.Value(openAITOTP429ObservedKey{}).(int64); observed == account.ID {
		return ctx
	}
	ctx = context.WithValue(ctx, openAITOTP429ObservedKey{}, account.ID)
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	task, err := s.openAITOTP429Rotator.CreateTOTPRotationOn429(stateCtx, account.ID)
	if err != nil {
		// Log a stable reason only, never a response, password or TOTP secret.
		slog.Warn("openai_429_totp_rotation_skipped", "account_id", account.ID, "reason", infraerrors.Reason(err))
	} else if task != nil {
		slog.Info("openai_429_totp_rotation_queued", "account_id", account.ID, "task_id", task.TaskID)
	}
	return ctx
}
