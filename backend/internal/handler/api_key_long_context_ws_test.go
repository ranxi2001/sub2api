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
				h.requireClosed(coderws.StatusPolicyViolation, "long context is disabled")
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
				h.requireClosed(coderws.StatusPolicyViolation, "long context is disabled")
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
			h.requireClosed(coderws.StatusTryAgainLater, "configuration changed")
			require.Equal(t, 1, h.upstream.frameCount())
		})
	}
}
