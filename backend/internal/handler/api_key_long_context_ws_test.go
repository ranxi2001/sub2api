//go:build unit

package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyLongContextWSRejectsBeforeUpstream(t *testing.T) {
	largeInput, err := json.Marshal(strings.Repeat(" x", 280_001))
	require.NoError(t, err)
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough} {
		for _, tc := range []struct{ name, payload string }{
			{"large input", `{"type":"response.create","model":"gpt-5.4","input":` + string(largeInput) + `}`},
			{"hidden response history", `{"type":"response.create","model":"gpt-5.4","previous_response_id":"resp_hidden","input":"hello"}`},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				key := wsRevalidationKey(0, false, nil, false)
				key.AllowLongContext = false
				h := newWSRevalidationHarness(t, key, service.APIKeyQueuePolicy{}, mode)
				h.sendTurn(tc.payload)
				requireAPIKeyContextWSRejected(t, h)
				require.Zero(t, h.upstream.frameCount())
			})
		}
	}
}

func TestAPIKeyLongContextWSChecksEveryTurn(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough} {
		for _, payload := range []string{
			`{"type":"response.create","previous_response_id":"resp_hidden","input":"hello"}`,
			`{"type":"session.update","session":{"instructions":"hidden instructions"}}`,
		} {
			t.Run(mode+"/"+payload, func(t *testing.T) {
				key := wsRevalidationKey(0, false, nil, false)
				key.AllowLongContext = false
				h := newWSRevalidationHarness(t, key, service.APIKeyQueuePolicy{}, mode)
				h.sendTurn(`{"type":"response.create","model":"gpt-5.4","input":"hello","stream":false}`)
				h.requireCompleted(5 * time.Second)
				h.sendTurn(payload)
				requireAPIKeyContextWSRejected(t, h)
				require.Equal(t, 1, h.upstream.frameCount())
			})
		}
	}
}

func TestAPIKeyLongContextWSDisabledSettingChangeRequiresReconnect(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) {
			key := wsRevalidationKey(0, false, nil, false)
			h := newWSRevalidationHarness(t, key, service.APIKeyQueuePolicy{}, mode)
			h.sendTurn(`{"type":"response.create","model":"gpt-5.4","input":"hello","stream":false}`)
			h.requireCompleted(5 * time.Second)
			latest := *key
			latest.AllowLongContext = false
			h.repo.set(&latest)
			h.sendTurn(`{"type":"response.create","input":"hello","stream":false}`)
			h.requireClosed(coderws.StatusTryAgainLater, service.APIKeyContextPolicyRetryMessage)
			require.Equal(t, 1, h.upstream.frameCount())
		})
	}
}

func requireAPIKeyContextWSRejected(t *testing.T, h *wsRevalidationHarness) {
	t.Helper()
	event, closeCode, reason := h.readTurn(5 * time.Second)
	require.Zero(t, closeCode, reason)
	var payload struct {
		Type     string `json:"type"`
		Response struct {
			Status    string                         `json:"status"`
			CreatedAt int64                          `json:"created_at"`
			Error     struct{ Code, Message string } `json:"error"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal(event, &payload))
	require.Equal(t, "response.failed", payload.Type)
	require.Equal(t, "failed", payload.Response.Status)
	require.Positive(t, payload.Response.CreatedAt)
	require.Equal(t, service.APIKeyContextLengthExceededCode, payload.Response.Error.Code)
	require.Equal(t, service.APIKeyContextLengthExceededMessage, payload.Response.Error.Message)
	require.NotContains(t, string(event), "disabled")
	require.NotContains(t, string(event), "context_unverifiable")
	h.requireClosed(coderws.StatusPolicyViolation, service.APIKeyContextLengthExceededMessage)
}

func TestAPIKeyContextTokenLimitWSChecksAllowedKeyEveryTurn(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) {
			key := wsRevalidationKey(0, false, nil, false)
			key.AllowLongContext, key.ContextTokenLimit = true, 200
			h := newWSRevalidationHarness(t, key, service.APIKeyQueuePolicy{}, mode)
			h.sendTurn(`{"type":"response.create","model":"gpt-5.4","input":"hello","stream":false}`)
			h.requireCompleted(5 * time.Second)
			large, err := json.Marshal(strings.Repeat(" x", 300))
			require.NoError(t, err)
			h.sendTurn(`{"type":"response.create","model":"gpt-5.4","input":` + string(large) + `}`)
			requireAPIKeyContextWSRejected(t, h)
			require.Equal(t, 1, h.upstream.frameCount())
		})
	}
}

func TestAPIKeyContextTokenLimitWSChangeRequiresFreshAdmission(t *testing.T) {
	key := wsRevalidationKey(0, false, nil, false)
	key.AllowLongContext, key.ContextTokenLimit = true, 1000
	h := newWSRevalidationHarness(t, key, service.APIKeyQueuePolicy{}, service.OpenAIWSIngressModeCtxPool)
	h.sendTurn(`{"type":"response.create","model":"gpt-5.4","input":"hello","stream":false}`)
	h.requireCompleted(5 * time.Second)
	latest := *key
	latest.ContextTokenLimit = 100
	h.repo.set(&latest)
	h.sendTurn(`{"type":"response.create","model":"gpt-5.4","input":"hello","stream":false}`)
	h.requireClosed(coderws.StatusTryAgainLater, service.APIKeyContextPolicyRetryMessage)
	require.Equal(t, 1, h.upstream.frameCount())
}
