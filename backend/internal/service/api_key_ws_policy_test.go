package service

import (
	"context"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Exercise real client WS frames through the passthrough ingress: the second
// turn must not restore the client's model or effort after the initial request.
func TestAPIKeyWSPolicyAppliesToFirstAndSubsequentFrames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	key := &APIKey{ModelMapping: map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}, ReasoningEffort: "high"}
	upstream := newStagedPassthroughConn()
	hooks := &OpenAIWSIngressHooks{
		ForcedReasoningEffort: key.ReasoningEffort,
		APIKeyModelMapping:    true,
		MaxReasoningEffort:    "medium",
		MapRequestModel:       func(_ int, model string) (string, error) { return MapAPIKeyModel(key, model), nil },
	}
	server, _ := startPassthroughHookRecordingServer(t, ctx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleAccount(), hooks)
	defer server.Close()
	client := dialPassthroughLifecycleClientWithPayload(t, server, `{"type":"response.create","model":"gpt-6.1-sol","stream":false}`)
	defer func() { _ = client.CloseNow() }()

	first := requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
	require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(first, "model").String())
	require.Equal(t, "medium", gjson.GetBytes(first, "reasoning.effort").String())
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_key_1","model":"gpt-5.6-sol","usage":{"input_tokens":1,"output_tokens":1}}}`)
	_, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
	require.NoError(t, err)

	writeCtx, cancelWrite := context.WithTimeout(ctx, 3*time.Second)
	err = client.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-6.1-sol","Model":"gpt-6.1-sol","reasoning":{"effort":"ultra"}}`))
	cancelWrite()
	require.NoError(t, err)
	second := requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
	require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(second, "model").String())
	require.Equal(t, "medium", gjson.GetBytes(second, "reasoning.effort").String())
	require.False(t, gjson.GetBytes(second, "Model").Exists())
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_key_2","model":"gpt-5.6-sol","usage":{"input_tokens":1,"output_tokens":1}}}`)
	_, err = readPassthroughLifecycleFrame(t, client, 3*time.Second)
	require.NoError(t, err)
}
