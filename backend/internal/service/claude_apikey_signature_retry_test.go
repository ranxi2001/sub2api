package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestClaudeAPIKeySignatureRetryDefaults(t *testing.T) {
	const rejection = "{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"Invalid `signature` in `thinking` block\"}}"
	const success = `{"id":"msg_fixture","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`
	const history = `{"model":"claude-opus-5-5","max_tokens":100,"messages":[{"role":"user","content":"first"},{"role":"assistant","content":[{"type":"thinking","thinking":"fixture","signature":"fixture-signature"},{"type":"text","text":"first answer"}]},{"role":"user","content":"continue"}]}`

	for _, tc := range []struct {
		name        string
		settings    string
		retry       bool
		passthrough bool
	}{
		{"unconfigured", "", true, false},
		{"explicitly disabled", `{"enabled":true,"apikey_signature_enabled":false}`, false, false},
		{"disabled globally", `{"enabled":false,"apikey_signature_enabled":true}`, false, false},
		{"saved before API key setting existed", `{"enabled":true,"thinking_signature_enabled":true}`, false, false},
		{"explicit passthrough", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := make(map[string]string)
			if tc.settings != "" {
				values[SettingKeyRectifierSettings] = tc.settings
			}
			upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{
				{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(rejection))},
				{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(success))},
			}}
			cfg := &config.Config{}
			svc := &GatewayService{
				cfg:                  cfg,
				httpUpstream:         upstream,
				settingService:       NewSettingService(&panelRateLimitSettingRepo{values: values}, cfg),
				rateLimitService:     &RateLimitService{},
				responseHeaderFilter: compileResponseHeaderFilter(cfg),
				deferredService:      &DeferredService{},
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			account := newAnthropicAPIKeyAccountForTest()
			if !tc.passthrough {
				delete(account.Extra, "anthropic_passthrough")
			}
			parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(history)), PlatformAnthropic)
			require.NoError(t, err)
			result, err := svc.Forward(c.Request.Context(), c, account, parsed)
			require.NotEmpty(t, upstream.bodies)
			require.Equal(t, "fixture-signature", gjson.GetBytes(upstream.bodies[0], "messages.1.content.0.signature").String())
			if !tc.retry {
				require.Error(t, err)
				require.Equal(t, http.StatusBadRequest, rec.Code)
				require.Len(t, upstream.bodies, 1)
				return
			}
			require.NoError(t, err)
			require.Len(t, upstream.bodies, 2)
			require.NotContains(t, string(upstream.bodies[1]), "fixture-signature")
			require.Equal(t, "first answer", gjson.GetBytes(upstream.bodies[1], "messages.1.content.1.text").String())
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, 2, result.Usage.OutputTokens)
		})
	}
}
