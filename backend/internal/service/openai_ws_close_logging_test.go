//go:build unit

package service

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSCloseLoggingPreservesLocalRecovery(t *testing.T) {
	local := &OpenAIAgentRecoveryError{Reason: "owner_unavailable"}
	err := wrapOpenAIWSIngressTurnError("read_upstream", fmt.Errorf("local refusal: %w", local), false)
	require.Equal(t, err.Error(), relayErrorText(err), "local diagnostics must retain their existing meaning")
	var got *OpenAIAgentRecoveryError
	require.True(t, errors.As(err, &got))
	require.Same(t, local, got)
	require.ErrorIs(t, err, errOpenAIAgentRecoveryUnavailable)
	_, _, closeClient := openAIWSPassthroughRelayClientClose(openaiwsv2.RelayExit{Stage: "read_upstream", Err: err}, 1)
	require.False(t, closeClient, "the handler must still own local recovery refusal delivery")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	SetOpenAIClientTransport(c, OpenAIClientTransportWS)
	BeginOpsStreamTurn(c, 2)
	markOpenAIWSUpstreamClose(c, err)
	require.Empty(t, GetOpsStreamErrors(c), "local refusal must not become an upstream close failure")
}

func TestOpenAIWSCloseLoggingPreservesTypedRoutingCause(t *testing.T) {
	const code = "encrypted_agent_recovery_owner_unavailable"
	for _, status := range []coderws.StatusCode{1000, 1001, 1005, 1006, 1008} {
		for _, reason := range []string{"private-close-canary", code + ": private-close-canary", `{"error":{"code":"` + code + `","message":"private-close-canary"}}`} {
			t.Run(fmt.Sprintf("%d/%s", status, reason), func(t *testing.T) {
				original := coderws.CloseError{Code: status, Reason: reason}
				err := wrapOpenAIWSIngressTurnError("read_upstream", fmt.Errorf("transport: %w", original), false)
				require.NotContains(t, relayErrorText(err), "private-close-canary")
				require.Contains(t, relayErrorText(err), fmt.Sprintf("status=%d", status))
				var got coderws.CloseError
				require.True(t, errors.As(err, &got))
				require.Equal(t, original, got)
				require.True(t, isOpenAIWSIngressTurnRetryable(err), "logging must not alter typed routing decisions")
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				SetOpenAIClientTransport(c, OpenAIClientTransportWS)
				BeginOpsStreamTurn(c, 2)
				markOpenAIWSUpstreamClose(c, err)
				failure, ok := GetOpsStreamError(c)
				require.True(t, ok)
				require.Equal(t, 2, failure.Turn)
				require.Equal(t, "read_upstream", failure.UpstreamWSFailure.Phase)
				require.EqualValues(t, status, failure.UpstreamWSFailure.CloseCode)
				if reason == "private-close-canary" {
					require.Equal(t, 502, failure.IntendedStatus)
					require.Equal(t, "upstream_websocket_closed", failure.Code)
				} else {
					require.Equal(t, 409, failure.IntendedStatus)
					require.Equal(t, code, failure.Code)
				}
			})
		}
	}
}
