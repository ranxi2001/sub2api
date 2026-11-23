//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestRelayLegacyThinkingAtMessagesEndpoints(t *testing.T) {
	for _, tc := range []struct {
		path, model string
		passthrough bool
	}{
		{"/v1/messages", "claude-opus-5-5", false},
		{"/v1/messages/count_tokens", "claude-fable-5-2", true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			account := newAnthropicAPIKeyAccountForTest()
			account.Credentials["base_url"] = "https://relay.example.com"
			account.Credentials["model_mapping"] = map[string]any{"client-alias": tc.model}
			account.Extra["anthropic_passthrough"] = tc.passthrough
			body := []byte(`{"model":"client-alias","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":1024},"max_tokens":1152,"output_config":{"effort":"low"}}`)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"input_tokens":1,"usage":{"input_tokens":1,"output_tokens":1}}`))}}
			svc := newForwardPartialUsageServiceForTest(upstream)
			if tc.path == "/v1/messages" {
				_, err = svc.Forward(context.Background(), c, account, parsed)
			} else {
				err = svc.ForwardCountTokens(context.Background(), c, account, parsed)
			}
			require.NoError(t, err)
			require.Equal(t, tc.model, gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, "adaptive", gjson.GetBytes(upstream.lastBody, "thinking.type").String())
			require.False(t, gjson.GetBytes(upstream.lastBody, "thinking.budget_tokens").Exists())
			require.Equal(t, "low", gjson.GetBytes(upstream.lastBody, "output_config.effort").String())
			if tc.path == "/v1/messages" {
				require.EqualValues(t, 1152, gjson.GetBytes(upstream.lastBody, "max_tokens").Int())
			} else {
				require.False(t, gjson.GetBytes(upstream.lastBody, "max_tokens").Exists())
			}
			require.Equal(t, body, parsed.Body.Bytes())
		})
	}
}

func TestRelayThinkingHonorsDisabledAndValidatesOtherFields(t *testing.T) {
	account := newAnthropicAPIKeyAccountForTest()
	account.Credentials["base_url"] = "https://relay.example.com"
	for _, tc := range []struct{ body, want string }{
		{`{"thinking":{"type":"disabled"}}`, "received thinking.type=disabled"},
		{`{"thinking":{"type":"enabled","budget_tokens":1024},"tool_choice":{"type":"any"}}`, "forced tool_choice"},
	} {
		parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(tc.body)), PlatformAnthropic)
		require.NoError(t, err)
		_, err = prepareClaudeMessagesRequest(account, parsed, "claude-opus-5-5")
		require.ErrorContains(t, err, tc.want)
	}
	account.Credentials["base_url"] = "https://api.anthropic.com"
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"thinking":{"type":"enabled","budget_tokens":1024}}`)), PlatformAnthropic)
	require.NoError(t, err)
	_, err = prepareClaudeMessagesRequest(account, parsed, "claude-opus-5-5")
	require.ErrorContains(t, err, "requires adaptive thinking")
}
